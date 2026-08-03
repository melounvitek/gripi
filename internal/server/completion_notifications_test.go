package server

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/push"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestCompletedAssistantReplyAcceptsOnlyFinalTextFromCompletedAssistantMessages(t *testing.T) {
	commentarySignature := `{"v":1,"id":"commentary","phase":"commentary"}`
	tests := []struct {
		name  string
		event map[string]any
		text  string
		ok    bool
	}{
		{name: "final reply", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "working", "textSignature": commentarySignature},
			map[string]any{"type": "text", "text": "completed"},
		}}}, text: "completed", ok: true},
		{name: "partial update", event: map[string]any{"type": "message_update", "message": map[string]any{"role": "assistant", "content": []any{"partial"}}}},
		{name: "commentary only", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "working", "textSignature": commentarySignature}}}}},
		{name: "tool message", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "toolResult", "content": []any{"done"}}}},
		{name: "user message", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "user", "content": []any{"done"}}}},
		{name: "empty final", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{"  "}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text, ok := completedAssistantReply(test.event)
			if text != test.text || ok != test.ok {
				t.Fatalf("completedAssistantReply() = %q, %t", text, ok)
			}
		})
	}
}

func TestCompletionNotifierDeliversTheSessionTitlePreviewAndURL(t *testing.T) {
	root := t.TempDir()
	sessionPath := writeNotificationSession(t, root, "Named session")
	fake := &recordingPushNotifier{}
	app := notificationTestApplication(t, root, false, true, fake)
	client := &remapClient{}
	if err := app.rpcClients.Register(sessionPath, client); err != nil {
		t.Fatal(err)
	}

	notifier := newCompletionNotifier(app)
	if err := notifier.deliver(context.Background(), completedReply{client: client, text: "**Completed** reply"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.owners, []string{singleUserOwner}) {
		t.Fatalf("delivery owners = %#v", fake.owners)
	}
	var payload map[string]string
	if err := json.Unmarshal(fake.payloads[0], &payload); err != nil {
		t.Fatal(err)
	}
	if payload["type"] != "gripi-notification" || payload["title"] != "Named session" || payload["body"] != "**Completed** reply" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload["tag"] != "gripi-final-reply:"+sessions.SessionHash(sessionPath) {
		t.Fatalf("tag = %q", payload["tag"])
	}
	parsed, err := url.Parse(payload["url"])
	if err != nil || parsed.Query().Get("session") != sessionPath {
		t.Fatalf("url = %q, %v", payload["url"], err)
	}
}

func TestCompletionNotifierTargetsEveryApprovedBrowserAndDropsRevokedOwners(t *testing.T) {
	root := t.TempDir()
	fake := &recordingPushNotifier{}
	app := notificationTestApplication(t, root, false, false, fake)
	for _, token := range []string{"approved-a", "approved-b"} {
		if _, err := app.browserStore.ApproveCurrentBrowser(token, "test"); err != nil {
			t.Fatal(err)
		}
		owner, ok := app.pushOwner(requestWithCookie("gripi_browser=" + token))
		if !ok {
			t.Fatal("approved browser has no owner")
		}
		if err := app.pushSubscriptions.Upsert(owner, pushRouteSubscription(token)); err != nil {
			t.Fatal(err)
		}
	}
	revokedOwner := "browser:" + strings.Repeat("a", 64)
	if err := app.pushSubscriptions.Upsert(revokedOwner, pushRouteSubscription("revoked")); err != nil {
		t.Fatal(err)
	}

	notifier := newCompletionNotifier(app)
	owners, err := notifier.owners("/session")
	if err != nil || len(owners) != 2 {
		t.Fatalf("approved owners = %#v, %v", owners, err)
	}
	if stale, err := app.pushSubscriptions.List(revokedOwner); err != nil || len(stale) != 0 {
		t.Fatalf("revoked subscriptions = %#v, %v", stale, err)
	}
}

func TestCompletionNotifierResolvesAndClaimsAPendingSessionForItsWorkspace(t *testing.T) {
	root := t.TempDir()
	actualPath := writeNotificationSession(t, root, "New session")
	pendingPath := filepath.Join(root, "pending.jsonl")
	fake := &recordingPushNotifier{}
	app := notificationTestApplication(t, root, true, false, fake)
	if err := app.workspaceStore.ApproveWorkspace("workspace-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ownershipStore.Claim(pendingPath, "workspace-a"); err != nil {
		t.Fatal(err)
	}
	client := &remapClient{state: map[string]any{"data": map[string]any{"sessionFile": actualPath}}}
	if err := app.rpcClients.Register(pendingPath, client); err != nil {
		t.Fatal(err)
	}
	app.pendingSessions.Remember(pendingPath, root)

	notifier := newCompletionNotifier(app)
	if err := notifier.deliver(context.Background(), completedReply{client: client, text: "done"}); err != nil {
		t.Fatal(err)
	}
	owned, err := app.ownershipStore.OwnedBy(actualPath, "workspace-a")
	if err != nil || !owned {
		t.Fatalf("actual session ownership = %t, %v", owned, err)
	}
	if !slices.Equal(fake.owners, []string{"workspace:workspace-a"}) {
		t.Fatalf("delivery owners = %#v", fake.owners)
	}
	var payload map[string]string
	if err := json.Unmarshal(fake.payloads[0], &payload); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(payload["url"])
	if err != nil || parsed.Query().Get("session") != actualPath {
		t.Fatalf("pending delivery URL = %q, %v", payload["url"], err)
	}
}

type recordingPushNotifier struct {
	owners   []string
	payloads [][]byte
}

func (notifier *recordingPushNotifier) Deliver(_ context.Context, owner string, payload []byte) error {
	notifier.owners = append(notifier.owners, owner)
	notifier.payloads = append(notifier.payloads, append([]byte(nil), payload...))
	return nil
}

func notificationTestApplication(t *testing.T, root string, multiUser, authDisabled bool, notifier pushNotifier) *application {
	t.Helper()
	cfg := config.Config{
		Home:                   root,
		SessionsRoot:           root,
		BrowserAuthDisabled:    authDisabled,
		MultiUserMode:          multiUser,
		AdminPassword:          "secret",
		BrowserAccessPath:      filepath.Join(root, "browser-access.json"),
		WorkspaceAccessPath:    filepath.Join(root, "workspace-access.json"),
		WorkspaceOwnershipPath: filepath.Join(root, "session-owners.json"),
		PushSubscriptionsPath:  filepath.Join(root, "subscriptions.json"),
	}
	app := &application{
		config:            cfg,
		browserStore:      access.NewBrowserStore(cfg.BrowserAccessPath),
		workspaceStore:    access.NewWorkspaceStore(cfg.WorkspaceAccessPath),
		ownershipStore:    access.NewWorkspaceOwnershipStore(cfg.WorkspaceOwnershipPath, cfg.SessionsRoot),
		pushSubscriptions: push.NewSubscriptionStore(cfg.PushSubscriptionsPath),
		pushNotifier:      notifier,
		sessionCache:      sessions.NewCache(),
		pendingSessions:   rpc.NewPendingSessionRegistry(nil),
		rpcClients:        rpc.NewRegistry(nil, nil),
	}
	return app
}

func writeNotificationSession(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, "session.jsonl")
	contents := `{"type":"session","version":3,"id":"notification","timestamp":"2026-01-01T00:00:00Z","cwd":` + quotedJSON(t, root) + `}` + "\n" +
		`{"type":"session_info","name":` + quotedJSON(t, name) + `,"timestamp":"2026-01-01T00:00:01Z"}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func quotedJSON(t *testing.T, value string) string {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
