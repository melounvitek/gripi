package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func assertSettledUnread(t *testing.T, app *application, path string, want bool) {
	t.Helper()
	templates, err := template.New("").Funcs(templateFunctions(rendering.NewMarkdown())).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/sidebar?no_session=1", "/?no_session=1"} {
		view, err := app.preparePage(httptest.NewRequest("GET", target, nil), target[1] == '?')
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		if want {
			count = 1
		}
		if view.Unread[path] != want || view.UnreadCount != count {
			t.Errorf("%s unread=%v count=%d, want %t/%d", target, view.Unread[path], view.UnreadCount, want, count)
		}
		var html strings.Builder
		if err := templates.ExecuteTemplate(&html, "sidebar", view); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(html.String(), fmt.Sprintf(`data-unread-session-count="%d"`, count)) {
			t.Errorf("%s rendered wrong unread count", target)
		}
		if strings.Contains(html.String(), `>Unread: </span>`) != want {
			t.Errorf("%s rendered wrong unread highlighting", target)
		}
	}
}

func TestSidebarUnreadWaitsForSettledAndSurvivesRetirement(t *testing.T) {
	for _, ending := range []string{"eligible", "read final", "read progress", "aborted"} {
		t.Run(ending, func(t *testing.T) {
			app, notifier, client, path := settledNotificationFixture(t)
			assertSettledUnread(t, app, path, false)
			notifier.Observe(client, map[string]any{"type": "agent_start"})
			progress := notificationMessage("progress", "Progress", "stop")
			appendExternalSessionReply(t, path, "progress", "")
			notifier.Observe(client, map[string]any{"type": "message_end", "message": progress})
			notifier.Observe(client, notificationAgentEnd(progress))
			assertSettledUnread(t, app, path, false)
			if ending == "read progress" {
				if err := app.gatewayState.MarkRead(path, 1); err != nil {
					t.Fatal(err)
				}
			}
			final := notificationMessage("final", "Final", "stop")
			appendExternalSessionReply(t, path, "final", "progress")
			if ending == "aborted" {
				final["stopReason"] = "aborted"
			}
			notifier.Observe(client, map[string]any{"type": "message_end", "message": final})
			notifier.Observe(client, notificationAgentEnd(progress, final))
			if ending == "read final" {
				// A selected page marks raw persisted responses read even before settled.
				if _, err := app.preparePage(httptest.NewRequest("GET", "/?session="+url.QueryEscape(path), nil), false); err != nil {
					t.Fatal(err)
				}
			}
			assertSettledUnread(t, app, path, false)
			notifier.Observe(client, map[string]any{"type": "agent_settled"})
			want := ending == "eligible" || ending == "read progress"
			assertSettledUnread(t, app, path, want)
			if _, err := app.rpcClients.CloseClientWithoutOperations(path); err != nil {
				t.Fatal(err)
			}
			assertSettledUnread(t, app, path, want)
			app.gatewayState = sessions.NewGatewayState(filepath.Join(app.config.SessionsRoot, "read.json"), filepath.Join(app.config.SessionsRoot, "pinned.json"), filepath.Join(t.TempDir(), "tags.json"), app.config.SessionsRoot)
			assertSettledUnread(t, app, path, want)
		})
	}
}

func TestBusyRPCSidebarKeepsWorkingIndicatorAndReadFinalSuppression(t *testing.T) {
	app, notifier, _, path := settledNotificationFixture(t)
	if _, err := app.rpcClients.CloseClientWithoutOperations(path); err != nil {
		t.Fatal(err)
	}
	in, input := io.Pipe()
	output, out := io.Pipe()
	observed := make(chan struct{}, 1)
	client := rpc.NewClient(input, output, nil, rpc.ClientOptions{EventObserver: func(client *rpc.Client, event map[string]any) {
		notifier.Observe(client, event)
		observed <- struct{}{}
	}})
	t.Cleanup(func() { _ = client.Close(); _ = in.Close(); _ = out.Close() })
	if err := app.rpcClients.Register(path, client); err != nil {
		t.Fatal(err)
	}
	send := func(event map[string]any) {
		t.Helper()
		if err := json.NewEncoder(out).Encode(event); err != nil {
			t.Fatal(err)
		}
		select {
		case <-observed:
		case <-time.After(time.Second):
			t.Fatal("observer blocked")
		}
	}
	assertSettledUnread(t, app, path, false)
	send(map[string]any{"type": "agent_start"})
	appendExternalSessionReply(t, path, "progress", "")
	progress := notificationMessage("progress", "Progress", "stop")
	send(map[string]any{"type": "message_end", "message": progress})
	assertSettledUnread(t, app, path, false)
	view, err := app.preparePage(httptest.NewRequest("GET", "/sidebar?no_session=1", nil), false)
	if err != nil {
		t.Fatal(err)
	}
	if !view.SidebarActivity[path].Busy {
		t.Fatal("working indicator disappeared during progress")
	}
	appendExternalSessionReply(t, path, "final", "progress")
	final := notificationMessage("final", "Final", "stop")
	send(map[string]any{"type": "message_end", "message": final})
	if err := app.gatewayState.MarkRead(path, 2); err != nil {
		t.Fatal(err)
	}
	assertSettledUnread(t, app, path, false)
	send(notificationAgentEnd(progress, final))
	assertSettledUnread(t, app, path, false)
	send(map[string]any{"type": "agent_settled"})
	assertSettledUnread(t, app, path, false)
	readCount, err := app.gatewayState.ReadCount(path)
	if err != nil || readCount != 2 {
		t.Fatalf("read count = %d, %v; want raw count 2", readCount, err)
	}
	view, err = app.preparePage(httptest.NewRequest("GET", "/sidebar?no_session=1", nil), false)
	if err != nil {
		t.Fatal(err)
	}
	if view.SidebarActivity[path].Busy {
		t.Fatal("working indicator remained after settled")
	}
}

func TestPendingCompletionMigratesWithoutLosingUnreadOrReadSuppression(t *testing.T) {
	for _, timing := range []string{"running", "settled", "read settled"} {
		t.Run(timing, func(t *testing.T) {
			app, notifier, _, path := settledNotificationFixture(t)
			if _, err := app.rpcClients.CloseClientWithoutOperations(path); err != nil {
				t.Fatal(err)
			}
			pending := filepath.Join(app.config.SessionsRoot, "pending.jsonl")
			app.pendingSessions.Remember(pending, app.config.SessionsRoot, "")
			app.config.AttachmentsRoot = t.TempDir()
			client := notificationRPCClient(t, app, pending)
			notifier.Observe(client, map[string]any{"type": "agent_start"})
			appendExternalSessionReply(t, path, "final", "")
			final := notificationMessage("final", "Final", "stop")
			notifier.Observe(client, map[string]any{"type": "message_end", "message": final})
			notifier.Observe(client, notificationAgentEnd(final))
			if timing != "running" {
				notifier.Observe(client, map[string]any{"type": "agent_settled"})
			}
			if timing == "read settled" {
				if err := app.gatewayState.MarkRead(pending, 1); err != nil {
					t.Fatal(err)
				}
			}
			if err := app.remapPendingRPCClient(pending, path, func() (func() error, error) { return nil, nil }); err != nil {
				t.Fatal(err)
			}
			if timing == "running" {
				assertSettledUnread(t, app, path, false)
				notifier.Observe(client, map[string]any{"type": "agent_settled"})
			}
			assertSettledUnread(t, app, path, timing != "read settled")
			view, err := app.preparePage(httptest.NewRequest("GET", "/sidebar?no_session=1", nil), false)
			if err != nil {
				t.Fatal(err)
			}
			if view.CompletedReplies[path].ID != completedReplyID(map[string]any{"message": final}) {
				t.Fatal("completion did not follow pending remap")
			}
		})
	}
}

func TestUnresolvedPendingSidebarDoesNotExposeProgressOrReadIt(t *testing.T) {
	for _, unavailable := range []string{"unreported", "rpc lane busy"} {
		t.Run(unavailable, func(t *testing.T) {
			app, notifier, _, path := settledNotificationFixture(t)
			if _, err := app.rpcClients.CloseClientWithoutOperations(path); err != nil {
				t.Fatal(err)
			}
			older := filepath.Join(app.config.SessionsRoot, "older.jsonl")
			if err := os.Rename(path, older); err != nil {
				t.Fatal(err)
			}
			assertSettledUnread(t, app, older, false)
			appendExternalSessionReply(t, older, "older", "")
			assertSettledUnread(t, app, older, true)

			pending := filepath.Join(app.config.SessionsRoot, "pending.jsonl")
			app.pendingSessions.Remember(pending, app.config.SessionsRoot, "")
			app.config.AttachmentsRoot = t.TempDir()
			in, input := io.Pipe()
			output, out := io.Pipe()
			client := rpc.NewClient(input, output, nil, rpc.ClientOptions{})
			t.Cleanup(func() { _ = client.Close(); _ = in.Close(); _ = out.Close() })
			if err := app.rpcClients.Register(pending, client); err != nil {
				t.Fatal(err)
			}
			var reported atomic.Bool
			go func() {
				decoder := json.NewDecoder(in)
				for {
					var command map[string]any
					if decoder.Decode(&command) != nil {
						return
					}
					data := map[string]any{}
					if reported.Load() {
						data["sessionFile"] = path
					}
					if json.NewEncoder(out).Encode(map[string]any{"type": "response", "command": command["type"], "id": command["id"], "success": true, "data": data}) != nil {
						return
					}
				}
			}()
			notifier.Observe(client, map[string]any{"type": "agent_start"})
			writeNotificationSession(t, app.config.SessionsRoot, "Pending run")
			poll := func() error {
				for _, target := range []string{"/sidebar?no_session=1", "/?no_session=1"} {
					view, err := app.preparePage(httptest.NewRequest("GET", target, nil), false)
					if err != nil {
						return err
					}
					if view.Unread[path] || !view.Unread[older] || view.UnreadCount != 1 {
						t.Errorf("%s unread=%v count=%d; want only older reply unread", target, view.Unread, view.UnreadCount)
					}
				}
				return nil
			}
			parent := ""
			for _, id := range []string{"progress-one", "progress-two"} {
				appendExternalSessionReply(t, path, id, parent)
				parent = id
				notifier.Observe(client, map[string]any{"type": "message_end", "message": notificationMessage(id, id, "stop")})
				var err error
				if unavailable == "rpc lane busy" {
					err = app.rpcClients.WithExistingClient(context.Background(), pending, false, func(rpc.RPCClient) error { return poll() })
				} else {
					err = poll()
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !app.rpcClients.Active(pending) {
				t.Fatal("unreported pending client was remapped by CWD alone")
			}
			// Only a background refresh resolves it; no originating-tab poll or settlement.
			reported.Store(true)
			if err := poll(); err != nil {
				t.Fatal(err)
			}
			if app.rpcClients.Active(pending) || !app.rpcClients.Active(path) {
				t.Fatal("background sidebar did not resolve the reported file")
			}
			if count, err := app.gatewayState.ReadCount(path); err != nil || count != 0 {
				t.Fatalf("progress initialized the read baseline: %d, %v", count, err)
			}
		})
	}
}

func TestInterruptedRunRetirementDoesNotRetainClientOrExposeProgress(t *testing.T) {
	app, notifier, client, path := settledNotificationFixture(t)
	notifier.Observe(client, map[string]any{"type": "agent_start"})
	appendExternalSessionReply(t, path, "progress", "")
	notifier.Observe(client, map[string]any{"type": "message_end", "message": notificationMessage("progress", "Progress", "stop")})
	if _, err := app.rpcClients.CloseClientWithoutOperations(path); err != nil {
		t.Fatal(err)
	}
	assertSettledUnread(t, app, path, false)
	if len(notifier.clients) != 0 {
		t.Fatal("interrupted client retained after retirement")
	}
}

func TestSidebarKeepsOlderUnreadDuringAbortedRun(t *testing.T) {
	app, notifier, client, path := settledNotificationFixture(t)
	assertSettledUnread(t, app, path, false)
	notifier.Observe(client, map[string]any{"type": "agent_start"})
	appendExternalSessionReply(t, path, "old", "")
	notifier.Observe(client, notificationAgentEnd(notificationMessage("old", "Older reply", "stop")))
	notifier.Observe(client, map[string]any{"type": "agent_settled"})
	assertSettledUnread(t, app, path, true)
	notifier.Observe(client, map[string]any{"type": "agent_start"})
	appendExternalSessionReply(t, path, "progress", "old")
	notifier.Observe(client, map[string]any{"type": "message_end", "message": notificationMessage("progress", "Progress", "stop")})
	assertSettledUnread(t, app, path, true)
	notifier.Observe(client, notificationAgentEnd(notificationMessage("aborted", "", "aborted")))
	notifier.Observe(client, map[string]any{"type": "agent_settled"})
	assertSettledUnread(t, app, path, true)
	// Reading only the previous completion must clear unread, not expose aborted progress.
	if err := app.gatewayState.MarkRead(path, 1); err != nil {
		t.Fatal(err)
	}
	assertSettledUnread(t, app, path, false)
}
