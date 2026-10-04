package server

import (
	"encoding/json"
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
	for range 2 {
		response := httptest.NewRecorder()
		app.sessionPalette(response, httptest.NewRequest(http.MethodGet, "/sessions/palette?session="+url.QueryEscape(path), nil))
		var payload struct{ Sessions []map[string]any }
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || len(payload.Sessions) != 1 {
			t.Fatalf("palette = %d %q", response.Code, response.Body.String())
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
