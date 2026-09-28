package server

import (
	"context"
	"html/template"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func settledNotificationFixture(t *testing.T) (*application, *completionNotifier, *rpc.Client, string) {
	t.Helper()
	root := t.TempDir()
	path := writeNotificationSession(t, root, "Background session")
	app := notificationTestApplication(t, root, false, true, &recordingPushNotifier{})
	notifier := newCompletionNotifier(app)
	// Keep deliveries in the queue so assertions are synchronous.
	notifier.started = true
	app.completionNotifications = notifier
	t.Cleanup(notifier.cancel)
	return app, notifier, notificationRPCClient(t, app, path), path
}

func notificationRPCClient(t *testing.T, app *application, path string) *rpc.Client {
	t.Helper()
	in, input := io.Pipe()
	output, out := io.Pipe()
	client := rpc.NewClient(input, output, nil, rpc.ClientOptions{})
	t.Cleanup(func() { _ = client.Close(); _ = in.Close(); _ = out.Close() })
	if err := app.rpcClients.Register(path, client); err != nil {
		t.Fatal(err)
	}
	return client
}

func notificationMessage(id, text, stop string) map[string]any {
	return map[string]any{"role": "assistant", "id": id, "stopReason": stop, "content": []any{map[string]any{"type": "text", "text": text}}}
}

func notificationAgentEnd(messages ...any) map[string]any {
	return map[string]any{"type": "agent_end", "messages": messages}
}

func TestSettledNotificationsConsumeLastAgentEndCandidate(t *testing.T) {
	final := notificationMessage("final", "Final reply", "stop")
	progress := notificationMessage("progress", "Progress", "stop")
	start := map[string]any{"type": "agent_start"}
	settled := map[string]any{"type": "agent_settled"}
	tests := []struct {
		name   string
		events []map[string]any
		want   string
	}{
		{"last assistant, not last message", []map[string]any{notificationAgentEnd(progress, final, map[string]any{"role": "toolResult", "content": "output"})}, "Final reply"},
		{"latest agent end replaces", []map[string]any{notificationAgentEnd(progress), notificationAgentEnd(final)}, "Final reply"},
		{"agent end text is authoritative", []map[string]any{{"type": "message_end", "message": notificationMessage("final", "Outdated text", "stop")}, notificationAgentEnd(final)}, "Final reply"},
		{"empty agent end clears", []map[string]any{notificationAgentEnd(final), notificationAgentEnd()}, ""},
		{"start clears", []map[string]any{notificationAgentEnd(final), start}, ""},
		{"settled without end", nil, ""},
		{"message end alone", []map[string]any{{"type": "message_end", "message": final}}, ""},
		{"length accepted", []map[string]any{notificationAgentEnd(notificationMessage("length", "Truncated", "length"))}, "Truncated"},
		{"missing stop accepted", []map[string]any{notificationAgentEnd(notificationMessage("legacy", "Legacy", ""))}, "Legacy"},
	}
	for _, stop := range []string{"error", "aborted", "toolUse", "unknown"} {
		tests = append(tests, struct {
			name   string
			events []map[string]any
			want   string
		}{stop + " does not fall back", []map[string]any{notificationAgentEnd(final), notificationAgentEnd(progress, notificationMessage("invalid", "Invalid", stop))}, ""})
	}
	commentary := notificationMessage("commentary", "Working", "stop")
	commentary["content"].([]any)[0].(map[string]any)["textSignature"] = `{"v":1,"id":"commentary","phase":"commentary"}`
	for name, invalid := range map[string]any{"commentary": commentary, "empty": notificationMessage("empty", " ", "stop"), "thinking": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "hmm"}}}} {
		tests = append(tests, struct {
			name   string
			events []map[string]any
			want   string
		}{name + " does not fall back", []map[string]any{notificationAgentEnd(final, invalid)}, ""})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, notifier, client, _ := settledNotificationFixture(t)
			notifier.Observe(client, start)
			for _, event := range test.events {
				notifier.Observe(client, event)
			}
			if len(notifier.queue) != 0 {
				t.Fatal("notification queued before settled")
			}
			notifier.Observe(client, settled)
			if test.want == "" {
				if len(notifier.queue) != 0 {
					t.Fatal("ineligible completion queued")
				}
				return
			}
			select {
			case reply := <-notifier.queue:
				if reply.client != nil {
					t.Fatal("settled non-pending reply retained its RPC client")
				}
				if reply.text != test.want {
					t.Fatalf("reply = %q", reply.text)
				}
			default:
				t.Fatal("eligible settled completion not queued")
			}
			notifier.Observe(client, settled)
			for _, event := range test.events {
				notifier.Observe(client, event)
			}
			notifier.Observe(client, settled)
			if len(notifier.queue) != 0 {
				t.Fatal("duplicate completion queued")
			}
		})
	}
}

func TestSidebarCompletionMetadataChangesOnlyOnEligibleSettledReply(t *testing.T) {
	app, notifier, client, path := settledNotificationFixture(t)
	templates, err := template.New("").Funcs(templateFunctions(rendering.NewMarkdown())).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	assertMetadata := func(id, preview string) {
		t.Helper()
		for _, target := range []string{"/sidebar?no_session=1", "/?no_session=1"} {
			view, err := app.preparePage(httptest.NewRequest("GET", target, nil), target[1] == '?')
			if err != nil {
				t.Fatal(err)
			}
			var html strings.Builder
			if err := templates.ExecuteTemplate(&html, "sidebar", view); err != nil {
				t.Fatal(err)
			}
			for _, attr := range []string{`data-completed-reply-id="` + id + `"`, `data-completed-reply-preview="` + template.HTMLEscapeString(preview) + `"`, `data-assistant-response-count=`} {
				if !strings.Contains(html.String(), attr) {
					t.Fatalf("%s missing %s in rendered sidebar", target, attr)
				}
			}
		}
	}
	assertMetadata("", "")
	final := notificationMessage("final", "**Done** <safe>", "stop")
	notifier.Observe(client, map[string]any{"type": "agent_start"})
	// Persisted counts and previews must not produce completion metadata.
	appendExternalSessionReply(t, path, "progress", "")
	notifier.Observe(client, map[string]any{"type": "message_end", "message": final})
	notifier.Observe(client, notificationAgentEnd(final))
	assertMetadata("", "")
	notifier.Observe(client, map[string]any{"type": "agent_settled"})
	id := completedReplyID(map[string]any{"message": final})
	preview := sessions.NotificationPreview("**Done** <safe>")
	assertMetadata(id, preview)
	notifier.Observe(client, map[string]any{"type": "agent_start"})
	appendExternalSessionReply(t, path, "more-progress", "progress")
	notifier.Observe(client, map[string]any{"type": "message_end", "message": notificationMessage("progress", "New progress", "stop")})
	assertMetadata(id, preview)
	notifier.Observe(client, notificationAgentEnd(final, notificationMessage("error", "Failed", "error")))
	notifier.Observe(client, map[string]any{"type": "agent_settled"})
	assertMetadata(id, preview)
	next := notificationMessage("next", "Next reply", "stop")
	notifier.Observe(client, notificationAgentEnd(next))
	notifier.Observe(client, map[string]any{"type": "agent_settled"})
	assertMetadata(completedReplyID(map[string]any{"message": next}), "Next reply")
	if _, err := app.rpcClients.CloseClientWithoutOperations(path); err != nil {
		t.Fatal(err)
	}
	assertMetadata(completedReplyID(map[string]any{"message": next}), "Next reply")
	if len(notifier.clients) != 0 {
		t.Fatal("retired client completion state retained")
	}
	app.gatewayState = sessions.NewGatewayState(filepath.Join(app.config.SessionsRoot, "read.json"), filepath.Join(app.config.SessionsRoot, "pinned.json"), filepath.Join(t.TempDir(), "tags.json"), app.config.SessionsRoot)
	app.completionNotifications = newCompletionNotifier(app)
	t.Cleanup(app.completionNotifications.cancel)
	assertMetadata(completedReplyID(map[string]any{"message": next}), "Next reply")
}

func TestSettledNotificationExternalFollowDoesNotPublishCompletion(t *testing.T) {
	app, path, _ := externalSessionTestApplication(t)
	appendExternalSessionReply(t, path, "external", "")
	if state, err := app.synchronizer.Inspect(context.Background(), path, false); err != nil || state.Mode != sessions.SyncExternalFollow {
		t.Fatalf("external sync = %#v, %v", state, err)
	}
	client := notificationRPCClient(t, app, path)
	notifier := newCompletionNotifier(app)
	notifier.started = true
	t.Cleanup(notifier.cancel)
	app.completionNotifications = notifier
	notifier.Observe(client, notificationAgentEnd(notificationMessage("external", "CLI reply", "stop")))
	notifier.Observe(client, map[string]any{"type": "agent_settled"})
	completions, err := app.gatewayState.Completions()
	if err != nil {
		t.Fatal(err)
	}
	if len(notifier.queue) != 0 || len(completions) != 0 {
		t.Fatal("external completion queued or published")
	}
	view, err := app.preparePage(httptest.NewRequest("GET", "/sidebar?no_session=1", nil), false)
	if err != nil {
		t.Fatal(err)
	}
	var html strings.Builder
	if err := app.templates.ExecuteTemplate(&html, "sidebar", view); err != nil {
		t.Fatal(err)
	}
	for _, attr := range []string{`data-session-sync-mode="external_follow"`, `data-completed-reply-id=""`, `data-completed-reply-preview=""`} {
		if !strings.Contains(html.String(), attr) {
			t.Fatalf("missing %s in external sidebar", attr)
		}
	}
}

func TestSettledNotificationPreservesReadBeforeAgentEnd(t *testing.T) {
	for _, role := range []string{"", "custom", "user", "toolResult"} {
		t.Run("intervening role="+role, func(t *testing.T) {
			app, notifier, client, path := settledNotificationFixture(t)
			final := notificationMessage("final", "Already read", "stop")
			notifier.Observe(client, map[string]any{"type": "agent_start"})
			notifier.Observe(client, map[string]any{"type": "message_end", "message": final})
			if _, _, err := app.gatewayState.ReadAndObserve([]*sessions.Session{{Path: path, AssistantResponseCount: 1}}, &sessions.Session{Path: path, AssistantResponseCount: 1}, true, nil); err != nil {
				t.Fatal(err)
			}
			if role != "" {
				notifier.Observe(client, map[string]any{"type": "message_end", "message": map[string]any{"role": role, "content": "Extension output"}})
			}
			notifier.Observe(client, notificationAgentEnd(final))
			notifier.Observe(client, map[string]any{"type": "agent_settled"})
			select {
			case reply := <-notifier.queue:
				if err := notifier.deliver(context.Background(), reply); err != nil {
					t.Fatal(err)
				}
				fake := app.pushNotifier.(*recordingPushNotifier)
				if len(fake.payloads) != 0 {
					t.Fatal("reply read before agent_end was delivered")
				}
			default:
				t.Fatal("missing settled reply")
			}
		})
	}
}
