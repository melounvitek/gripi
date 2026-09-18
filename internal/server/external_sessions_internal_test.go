package server

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestUnopenedExternalSessionIsReadAcrossPageAndSidebarViews(t *testing.T) {
	for _, target := range []string{"/?session=", "/sidebar?no_session=1", "/?no_session=1&session_search=hidden"} {
		t.Run(target, func(t *testing.T) {
			app, path, _ := externalSessionTestApplication(t)
			appendExternalSessionReply(t, path, "external", "")
			if target == "/?session=" {
				target += url.QueryEscape(path)
			}
			view, err := app.preparePage(httptest.NewRequest(http.MethodGet, "http://app.test"+target, nil), !strings.HasPrefix(target, "/sidebar"))
			if err != nil {
				t.Fatal(err)
			}
			if view.Unread[path] || view.UnreadCount != 0 {
				t.Fatalf("external session is unread: %v, total %d", view.Unread, view.UnreadCount)
			}
			if count, err := app.gatewayState.ReadCount(path); err != nil || count != 1 {
				t.Fatalf("external read baseline = %d, %v", count, err)
			}
			if !strings.Contains(target, "session_search") {
				var html strings.Builder
				if err := app.templates.ExecuteTemplate(&html, "sidebar", view); err != nil {
					t.Fatal(err)
				}
				for _, expected := range []string{`data-session-sync-mode="external_follow"`, `class="session-external-indicator"`, `title="Active outside Gripi · notifications and unread indicators paused"`, `data-unread-session-count="0"`, `data-external-response-count="1"`} {
					if !strings.Contains(html.String(), expected) {
						t.Errorf("sidebar missing %s", expected)
					}
				}
			}
		})
	}
}

func TestSidebarKeepsExternalSessionQuietDuringAnExclusiveOperation(t *testing.T) {
	app, path, _ := externalSessionTestApplication(t)
	appendExternalSessionReply(t, path, "external", "")
	refresh := func() error {
		view, err := app.preparePage(httptest.NewRequest(http.MethodGet, "http://app.test/sidebar?no_session=1", nil), false)
		if err != nil {
			return err
		}
		if !view.ExternalFollow[path] || view.Unread[path] {
			t.Fatalf("external state lost: external=%v, unread=%v", view.ExternalFollow, view.Unread)
		}
		return nil
	}
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	appendExternalSessionReply(t, path, "external-latest", "external")
	if err := app.synchronizer.WithExclusiveOperation(path, refresh); err != nil {
		t.Fatal(err)
	}
	if count := app.gatewayState.ExternalResponseCounts()[path]; count != 2 {
		t.Fatalf("external response boundary = %d, want 2", count)
	}
}

func TestSidebarObservationDoesNotBlockPendingSessions(t *testing.T) {
	app, actual, _ := externalSessionTestApplication(t)
	path := filepath.Join(app.config.SessionsRoot, "pending.jsonl")
	app.pendingSessions.Remember(path, app.config.SessionsRoot)
	appendExternalSessionReply(t, actual, "external", "")
	view, err := app.preparePage(httptest.NewRequest(http.MethodGet, "http://app.test/?session="+url.QueryEscape(path), nil), true)
	if err != nil {
		t.Fatal(err)
	}
	if view.Selected == nil || view.Selected.Path != path || view.SessionSyncBlocked {
		t.Fatalf("pending session selection = %#v, blocked = %t", view.Selected, view.SessionSyncBlocked)
	}
	if state := app.synchronizer.KnownBlocked(path); state != nil {
		t.Fatalf("pending session was inspected before its file existed: %#v", state)
	}
	if !view.ExternalFollow[actual] {
		t.Fatal("pending alias without a client deferred observation of disk session")
	}
}

func TestSidebarResolvesOwnedPendingAliasesBeforeObservingDiskSessions(t *testing.T) {
	for _, unavailable := range []string{"unreported", "rpc lane busy", "remap busy", "resolved"} {
		t.Run(unavailable, func(t *testing.T) {
			app, path, client := externalSessionTestApplication(t)
			pending := filepath.Join(app.config.SessionsRoot, "pending.jsonl")
			now := time.Now()
			app.rpcClients = rpc.NewRegistry(nil, func() time.Time { return now })
			app.synchronizer = sessions.NewSynchronizer(app.config.SessionsRoot, app.config.Home, app.sessionCache, app.rpcClients)
			app.pendingSessions.Remember(pending, app.config.SessionsRoot)
			if err := app.rpcClients.Register(pending, client); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Hour)
			reported := map[string]any{"data": map[string]any{"sessionFile": path}}
			if unavailable != "unreported" {
				client.state = reported
			}
			target := "http://app.test/sidebar?no_session=1"
			poll := func() error {
				view, err := app.preparePage(httptest.NewRequest(http.MethodGet, target, nil), false)
				if err != nil {
					return err
				}
				if view.ExternalFollow[path] || app.synchronizer.KnownBlocked(path) != nil {
					t.Fatalf("managed append classified as external: %v", view.ExternalFollow)
				}
				if _, recorded := app.gatewayState.ExternalResponseCounts()[path]; recorded {
					t.Fatal("managed reply recorded as an external response")
				}
				return nil
			}
			parent := ""
			for _, id := range []string{"managed-first", "managed-second"} {
				appendExternalSessionReply(t, path, id, parent)
				parent = id
				client.position = rpc.SessionEntries{Known: true, LeafID: id, Entries: []map[string]any{{"id": id}}}
				var err error
				switch unavailable {
				case "rpc lane busy":
					err = app.rpcClients.WithExistingClient(context.Background(), pending, false, func(rpc.RPCClient) error { return poll() })
				case "remap busy":
					err = app.rpcClients.WithActiveClient(context.Background(), pending, false, func(rpc.RPCClient) error { return poll() })
				default:
					err = poll()
				}
				if err != nil {
					t.Fatal(err)
				}
				// Selecting the alias on the next poll must not identify it twice.
				target = "http://app.test/sidebar?no_session=1&session=" + url.QueryEscape(pending)
			}
			wantCalls := int32(2)
			if unavailable == "rpc lane busy" {
				wantCalls = 0
			} else if unavailable == "resolved" {
				wantCalls = 1
			}
			if calls := client.getStateCalls.Load(); calls != wantCalls {
				t.Fatalf("identification calls = %d, want %d", calls, wantCalls)
			}
			if unavailable != "resolved" {
				if !app.rpcClients.Active(pending) || app.rpcClients.Active(path) {
					t.Fatal("unresolved client was identified by CWD alone")
				}
				if idle := app.rpcClients.IdleClientPaths(time.Minute, now, nil); len(idle) != 1 || idle[0] != pending {
					t.Fatalf("background identification refreshed idle timestamp: %v", idle)
				}
			}
			client.state = reported
			if err := poll(); err != nil {
				t.Fatal(err)
			}
			if app.rpcClients.Active(pending) || !app.rpcClients.Active(path) {
				t.Fatal("reported session was not remapped after contention cleared")
			}
		})
	}
}

func TestSidebarKeepsPendingSessionManagedDuringRemapPreparation(t *testing.T) {
	app, path, client := externalSessionTestApplication(t)
	pending := filepath.Join(app.config.SessionsRoot, "pending.jsonl")
	app.pendingSessions.Remember(pending, app.config.SessionsRoot)
	if err := app.rpcClients.Register(pending, client); err != nil {
		t.Fatal(err)
	}
	entered, release, block := idleRetirementBarrier()
	defer release()
	var moveErr error
	done := idleRetirementRun(t, func() {
		moveErr = app.remapPendingRPCClient(pending, path, func() (func() error, error) {
			block()
			return nil, nil
		})
	})
	idleRetirementWait(t, entered, "remap preparation")
	if app.rpcClients.Active(pending) {
		t.Fatal("pending client remained active during remap preparation")
	}
	refresh := func() {
		t.Helper()
		view, err := app.preparePage(httptest.NewRequest(http.MethodGet, "http://app.test/sidebar?no_session=1", nil), false)
		if err != nil {
			t.Fatal(err)
		}
		if view.ExternalFollow[path] || app.synchronizer.KnownBlocked(path) != nil {
			t.Errorf("managed append classified as external: %v", view.ExternalFollow)
		}
		if _, recorded := app.gatewayState.ExternalResponseCounts()[path]; recorded {
			t.Error("managed reply recorded as an external response")
		}
	}
	refresh()
	appendExternalSessionReply(t, path, "managed", "")
	client.position = rpc.SessionEntries{Known: true, LeafID: "managed", Entries: []map[string]any{{"id": "managed"}}}
	refresh()
	release()
	idleRetirementWait(t, done, "remap completion")
	if moveErr != nil {
		t.Fatal(moveErr)
	}
	refresh()
	if app.rpcClients.Client(path) != client || app.rpcClients.Active(pending) {
		t.Fatal("background refresh closed or lost the remapped client")
	}
}

func TestSidebarDoesNotQueryUnownedPendingAliases(t *testing.T) {
	app, path, client := externalSessionTestApplication(t)
	pending := filepath.Join(app.config.SessionsRoot, "unowned.jsonl")
	client.state = map[string]any{"data": map[string]any{"sessionFile": path}}
	app.pendingSessions.Remember(pending, app.config.SessionsRoot)
	if err := app.rpcClients.Register(pending, client); err != nil {
		t.Fatal(err)
	}
	app.ownershipStore = nil
	app.ownsSession = func(_ *http.Request, candidate string) bool { return candidate == path }
	appendExternalSessionReply(t, path, "external", "")
	view, err := app.preparePage(httptest.NewRequest(http.MethodGet, "http://app.test/sidebar?no_session=1", nil), false)
	if err != nil {
		t.Fatal(err)
	}
	if calls := client.getStateCalls.Load(); calls != 0 {
		t.Fatalf("queried unowned pending client %d times", calls)
	}
	if !view.ExternalFollow[path] {
		t.Fatal("unowned pending alias deferred observation of owned disk session")
	}
}

func TestTakeoverClearsUnobservedExternalRepliesButNewRepliesBecomeUnread(t *testing.T) {
	app, path, client := externalSessionTestApplication(t)
	appendExternalSessionReply(t, path, "external", "")
	if _, err := app.synchronizer.Inspect(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	// This reply arrives after the last sidebar observation, before takeover.
	appendExternalSessionReply(t, path, "external-latest", "external")
	client.position = rpc.SessionEntries{Known: true, LeafID: "external-latest"}
	request := httptest.NewRequest(http.MethodPost, "http://app.test/sessions/takeover", strings.NewReader(url.Values{"session": {path}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.takeOverSession(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("takeover = %d: %s", response.Code, response.Body.String())
	}
	if count, err := app.gatewayState.ReadCount(path); err != nil || count != 2 {
		t.Fatalf("takeover read baseline = %d, %v", count, err)
	}
	view, err := app.preparePage(httptest.NewRequest(http.MethodGet, "http://app.test/sidebar?no_session=1", nil), false)
	if err != nil {
		t.Fatal(err)
	}
	if view.Unread[path] || view.UnreadCount != 0 {
		t.Fatalf("CLI replies became unread after takeover: %v", view.Unread)
	}
	var html strings.Builder
	if err := app.templates.ExecuteTemplate(&html, "sidebar", view); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html.String(), `data-external-response-count="2"`) {
		t.Fatal("sidebar missing takeover reply boundary")
	}
	if strings.Contains(html.String(), `class="session-external-indicator"`) {
		t.Fatal("external icon remained after takeover")
	}
	// A queued notification predating external activity must not catch up either.
	notifier := newCompletionNotifier(app)
	if err := notifier.deliver(context.Background(), completedReply{client: client, path: path, text: "Old reply", readCountKnown: true}); err != nil {
		t.Fatal(err)
	}
	if deliveries := app.pushNotifier.(*recordingPushNotifier).owners; len(deliveries) != 0 {
		t.Fatalf("queued notification caught up after takeover: %v", deliveries)
	}

	appendExternalSessionReply(t, path, "managed", "external-latest")
	client.position = rpc.SessionEntries{Known: true, LeafID: "managed", Entries: []map[string]any{{"id": "managed"}}}
	view, err = app.preparePage(httptest.NewRequest(http.MethodGet, "http://app.test/sidebar?no_session=1", nil), false)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Unread[path] || view.UnreadCount != 1 {
		t.Fatalf("new gateway reply was not unread: %v, total %d", view.Unread, view.UnreadCount)
	}
}

func TestTakeoverKeepsQueuedNotificationsQuietWhileSavingTheReadBoundary(t *testing.T) {
	app, path, client := externalSessionTestApplication(t)
	appendExternalSessionReply(t, path, "external", "")
	if _, err := app.synchronizer.Inspect(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	client.position = rpc.SessionEntries{Known: true, LeafID: "external"}
	notifier := newCompletionNotifier(app)
	reply := completedReply{client: client, path: path, text: "Old reply", readCountKnown: true}
	_, err := app.synchronizer.TakeOver(context.Background(), path, func() error {
		// Delivery interleaves after Pi loaded the file but before read state is saved.
		if err := notifier.deliver(context.Background(), reply); err != nil {
			return err
		}
		if deliveries := app.pushNotifier.(*recordingPushNotifier).owners; len(deliveries) != 0 {
			t.Errorf("notification delivered before takeover read boundary was saved: %v", deliveries)
		}
		return app.gatewayState.MarkExternalRead(path, 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.deliver(context.Background(), reply); err != nil {
		t.Fatal(err)
	}
	if deliveries := app.pushNotifier.(*recordingPushNotifier).owners; len(deliveries) != 0 {
		t.Fatalf("notification delivered after takeover: %v", deliveries)
	}
}

func TestTakeoverReadStateFailureKeepsExternalFollow(t *testing.T) {
	app, path, client := externalSessionTestApplication(t)
	appendExternalSessionReply(t, path, "external", "")
	if _, err := app.synchronizer.Inspect(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	// Force persistence to fail after Pi has successfully loaded the external leaf.
	client.position = rpc.SessionEntries{Known: true, LeafID: "external"}
	if err := os.WriteFile(filepath.Join(app.config.SessionsRoot, "read.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://app.test/sessions/takeover", strings.NewReader(url.Values{"session": {path}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.takeOverSession(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("takeover with broken read state = %d", response.Code)
	}
	if state := app.synchronizer.KnownBlocked(path); state == nil || state.Mode != sessions.SyncExternalFollow {
		t.Fatalf("failed takeover enabled the session: %#v", state)
	}
}

func externalSessionTestApplication(t *testing.T) (*application, string, *remapClient) {
	t.Helper()
	root := t.TempDir()
	path := writeNotificationSession(t, root, "External session")
	app := notificationTestApplication(t, root, false, true, &recordingPushNotifier{})
	client := &remapClient{}
	app.rpcClients = rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return client, nil }, nil)
	app.synchronizer = sessions.NewSynchronizer(root, root, app.sessionCache, app.rpcClients)
	var err error
	app.templates, err = template.New("").Funcs(templateFunctions(rendering.NewMarkdown())).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	// Seed observation through the sidebar only; never open the conversation.
	if _, err := app.preparePage(httptest.NewRequest(http.MethodGet, "http://app.test/sidebar?no_session=1", nil), false); err != nil {
		t.Fatal(err)
	}
	return app, path, client
}

func appendExternalSessionReply(t *testing.T, path, id, parent string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(map[string]any{"type": "message", "id": id, "parentId": parent, "timestamp": "2026-01-01T00:00:02Z", "message": map[string]any{"role": "assistant", "stopReason": "stop", "content": []any{map[string]any{"type": "text", "text": id}}}}); err != nil {
		t.Fatal(err)
	}
}
