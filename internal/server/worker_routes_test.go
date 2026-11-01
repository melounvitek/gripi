package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	gateway "github.com/melounvitek/gripi/internal/server"
)

// workerPlacement stands in for the gateway's record of what is on a worker.
type workerPlacement struct{ sessions, folders []string }

func (placement workerPlacement) Session(key string) bool {
	return slices.Contains(placement.sessions, key)
}

func (placement workerPlacement) Folder(path string) bool {
	return slices.Contains(placement.folders, path)
}

func workerHandler(handler http.Handler, placement gateway.WorkerPlacement) http.Handler {
	return handler.(interface {
		Worker(gateway.WorkerPlacement) http.Handler
	}).Worker(placement)
}

// command sends a request as the gripi command does: a form for POST, the query for GET.
func command(target http.Handler, method, route string, fields url.Values, header ...string) *httptest.ResponseRecorder {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(fields.Encode())
	}
	request := httptest.NewRequest(method, "http://gripi"+route, body)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	request.Header.Set("Accept", "application/json")
	for index := 0; index < len(header); index += 2 {
		request.Header.Set(header[index], header[index+1])
	}
	response := httptest.NewRecorder()
	target.ServeHTTP(response, request)
	return response
}

func TestWorkerCommandsReachOnlyTheSessionsOnTheWorker(t *testing.T) {
	handler, alpha, beta, project := fakePiGateway(t)
	local := localHandler(t, handler)
	worker := workerHandler(handler, workerPlacement{sessions: []string{alpha}, folders: []string{project}})
	if response := command(local, http.MethodPost, "/sessions/tags", url.Values{"session": {beta}, "tag": {"main-only"}, "assigned": {"true"}}); response.Code != http.StatusOK {
		t.Fatalf("tag on main = %d %s", response.Code, response.Body.String())
	}

	if listed := localSessions(t, worker, ""); len(listed) != 1 || listed[0].Path != alpha {
		t.Fatalf("worker's sessions = %+v", listed)
	}
	if listed := localSessions(t, worker, "?session="+url.QueryEscape(beta)); len(listed) != 0 {
		t.Fatalf("worker's listing of a session on main = %+v", listed)
	}
	if tags := command(worker, http.MethodGet, "/tags", nil); tags.Code != http.StatusOK || strings.Contains(tags.Body.String(), "main-only") {
		t.Fatalf("worker's tags = %d %s", tags.Code, tags.Body.String())
	}

	for _, request := range []struct {
		method, route string
		fields        url.Values
	}{
		{http.MethodGet, "/conversation?session=" + url.QueryEscape(beta), nil},
		{http.MethodPost, "/prompt", url.Values{"session": {beta}, "message": {"Show the deterministic browser response"}}},
		{http.MethodPost, "/prompt", url.Values{"session": {beta}, "message": {"!touch " + project + "/ran"}}},
		{http.MethodPost, "/abort", url.Values{"session": {beta}}},
		{http.MethodPost, "/sessions/rename", url.Values{"session": {beta}, "name": {"Renamed by the worker"}}},
		{http.MethodPost, "/sessions/pin", url.Values{"session": {beta}, "pinned": {"true"}}},
		{http.MethodPost, "/sessions/tags", url.Values{"session": {beta}, "tag": {"main-only"}, "assigned": {"false"}}},
		{http.MethodPost, "/sessions/delete", url.Values{"session": {beta}}},
		{http.MethodPost, "/sessions/new_at_cwd", url.Values{"cwd": {project}, "fork_from": {beta}}},
	} {
		if response := command(worker, request.method, request.route, request.fields); response.Code != http.StatusNotFound {
			t.Fatalf("%s %s %v on a session on main = %d %s", request.method, request.route, request.fields, response.Code, response.Body.String())
		}
	}
	if response := command(worker, http.MethodPost, "/sessions/new_at_cwd", url.Values{"cwd": {t.TempDir()}}); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "worker") {
		t.Fatalf("new session in a folder on main = %d %s", response.Code, response.Body.String())
	}
	for _, request := range []struct {
		route  string
		fields url.Values
	}{
		{"/sudo", url.Values{"session": {alpha}, "cwd": {project}, "argv": {"true"}}},
		{"/sudo/askpass", url.Values{"token": {"any"}}},
		{"/sessions/claim", url.Values{"session": {alpha}}},
	} {
		if response := command(worker, http.MethodPost, request.route, request.fields); response.Code != http.StatusForbidden {
			t.Fatalf("%s from a worker = %d %s", request.route, response.Code, response.Body.String())
		}
	}
	if log, _ := os.ReadFile(os.Getenv("GRIPI_E2E_FAKE_PI_LOG")); strings.Contains(string(log), beta) {
		t.Fatalf("a worker's request started Pi for a session on main: %s", log)
	}
	if _, err := os.Stat(project + "/ran"); err == nil {
		t.Fatal("a worker ran a shell command in a session on main")
	}
	if onMain := localSessions(t, local, "?session="+url.QueryEscape(beta)); len(onMain) != 1 || onMain[0].Pinned || onMain[0].Name == "Renamed by the worker" || !slices.Contains(onMain[0].Tags, "main-only") {
		t.Fatalf("session on main after the worker's requests = %+v", onMain)
	}

	if conversation := command(worker, http.MethodGet, "/conversation?session="+url.QueryEscape(alpha), nil); conversation.Code != http.StatusOK || !strings.Contains(conversation.Body.String(), "Answer from alpha-0001") {
		t.Fatalf("conversation on the worker = %d %s", conversation.Code, conversation.Body.String())
	}
	for _, request := range []struct {
		route  string
		fields url.Values
	}{
		{"/sessions/rename", url.Values{"session": {alpha}, "name": {"Renamed by the worker"}}},
		{"/sessions/pin", url.Values{"session": {alpha}, "pinned": {"true"}}},
		{"/sessions/tags", url.Values{"session": {alpha}, "tag": {"on-worker"}, "assigned": {"true"}}},
		{"/prompt", url.Values{"session": {alpha}, "message": {"Show the deterministic browser response"}}},
		{"/abort", url.Values{"session": {alpha}}},
		{"/sessions/new_at_cwd", url.Values{"cwd": {project}}},
	} {
		response := command(worker, http.MethodPost, request.route, request.fields)
		if response.Code != http.StatusOK && response.Code != http.StatusAccepted {
			t.Fatalf("%s %v on the worker = %d %s", request.route, request.fields, response.Code, response.Body.String())
		}
		if request.route == "/sessions/tags" && strings.Contains(response.Body.String(), "main-only") {
			t.Fatalf("tags after tagging on the worker = %s", response.Body.String())
		}
	}
	session := waitForLocalSession(t, worker, alpha, func(session gateway.LocalSession) bool { return session.State == "idle" })
	if session.Name != "Renamed by the worker" || !session.Pinned || !slices.Contains(session.Tags, "on-worker") {
		t.Fatalf("session on the worker = %+v", session)
	}
}

func TestWorkerCommandsInMultiUserModeStillNeedTheUsersToken(t *testing.T) {
	handler, alpha, beta, _ := multiUserFakePiGateway(t)
	worker := workerHandler(handler, workerPlacement{sessions: []string{alpha, beta}})

	if refused := command(worker, http.MethodGet, "/sessions", nil); refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), "GRIPI_USER_TOKEN") {
		t.Fatalf("worker's request without a token = %d %s", refused.Code, refused.Body.String())
	}
	listed := command(worker, http.MethodGet, "/sessions", nil, "Authorization", "Bearer "+alice)
	var payload struct {
		Sessions []gateway.LocalSession `json:"sessions"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &payload); err != nil || len(payload.Sessions) != 1 || payload.Sessions[0].Path != alpha {
		t.Fatalf("alice's sessions on the worker = %d %s", listed.Code, listed.Body.String())
	}
	if other := command(worker, http.MethodGet, "/conversation?session="+url.QueryEscape(beta), nil, "Authorization", "Bearer "+alice); other.Code != http.StatusNotFound {
		t.Fatalf("bob's conversation for alice on the worker = %d %s", other.Code, other.Body.String())
	}
}
