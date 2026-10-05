package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func onlyLocalSession(t *testing.T, app *application) LocalSession {
	t.Helper()
	app.heavyRequests = make(chan struct{}, 1)
	response := httptest.NewRecorder()
	app.localSessions(response, httptest.NewRequest(http.MethodGet, "http://gripi/sessions", nil))
	var payload struct {
		Sessions []LocalSession `json:"sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload.Sessions) != 1 {
		t.Fatalf("sessions = %d %s (%v)", response.Code, response.Body.String(), err)
	}
	return payload.Sessions[0]
}

func TestLocalSessionsReportSessionsChangedOutsideTheGateway(t *testing.T) {
	app, path, _ := externalSessionTestApplication(t)
	appendExternalSessionReply(t, path, "external", "")
	if session := onlyLocalSession(t, app); session.State != "external" || session.Unread || session.LastReply != "external" {
		t.Fatalf("session used in Pi CLI = %+v", session)
	}

	app, path, _ = externalSessionTestApplication(t)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".replacement", contents, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".replacement", path); err != nil {
		t.Fatal(err)
	}
	if session := onlyLocalSession(t, app); session.State != "conflict" {
		t.Fatalf("replaced session file = %+v", session)
	}
}
