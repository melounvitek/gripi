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

	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestExternalFollowIsReadAcrossPageAndSidebarViews(t *testing.T) {
	for _, target := range []string{"/?session=", "/sidebar?no_session=1", "/?no_session=1&session_search=hidden"} {
		t.Run(target, func(t *testing.T) {
			app, path, _ := externalSessionTestApplication(t)
			appendExternalSessionReply(t, path, "external", "")
			if target != "/?session=" {
				if _, err := app.synchronizer.Inspect(context.Background(), path, false); err != nil {
					t.Fatal(err)
				}
			} else {
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
	if _, err := app.preparePage(httptest.NewRequest(http.MethodGet, "http://app.test/?session="+url.QueryEscape(path), nil), true); err != nil {
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
