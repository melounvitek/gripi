package server

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestSessionPaletteListsSessionsWithoutMarkingThemRead(t *testing.T) {
	app, notifier, client, path := settledNotificationFixture(t)
	app.heavyRequests = make(chan struct{}, 1)
	assertSettledUnread(t, app, path, false)
	final := notificationMessage("final", "Final", "stop")
	notifier.Observe(client, map[string]any{"type": "agent_start"})
	appendExternalSessionReply(t, path, "final", "")
	notifier.Observe(client, map[string]any{"type": "message_end", "message": final})
	notifier.Observe(client, notificationAgentEnd(final))
	notifier.Observe(client, map[string]any{"type": "agent_settled"})
	assertSettledUnread(t, app, path, true)

	// Naming a session must not select it: the second listing would then find it read.
	// A browser that takes it packed gets the same list.
	for _, encoding := range []string{"", "gzip"} {
		request := httptest.NewRequest(http.MethodGet, "/sessions/palette?session="+url.QueryEscape(path), nil)
		request.Header.Set("Accept-Encoding", encoding)
		response := httptest.NewRecorder()
		app.sessionPalette(response, request)
		if got := response.Header().Get("Content-Encoding"); got != encoding {
			t.Fatalf("Content-Encoding = %q, want %q", got, encoding)
		}
		var body io.Reader = response.Body
		if encoding == "gzip" {
			unpacked, err := gzip.NewReader(body)
			if err != nil {
				t.Fatal(err)
			}
			body = unpacked
		}
		var payload struct{ Sessions []map[string]any }
		if response.Code != http.StatusOK || json.NewDecoder(body).Decode(&payload) != nil || len(payload.Sessions) != 1 {
			t.Fatalf("palette = %d, sessions = %v", response.Code, payload.Sessions)
		}
		session := payload.Sessions[0]
		// The fixture's project is its home directory.
		want := map[string]any{"path": path, "name": "Background session", "project": "~", "unread": true, "busy": false}
		for key, value := range want {
			if session[key] != value {
				t.Errorf("%s = %#v, want %#v", key, session[key], value)
			}
		}
		for _, key := range []string{"monogram", "color", "age"} {
			if value, _ := session[key].(string); value == "" {
				t.Errorf("%s is missing: %#v", key, session)
			}
		}
	}
}
