package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

func localHandler(t *testing.T, handler http.Handler) http.Handler {
	t.Helper()
	return handler.(interface{ Local() http.Handler }).Local()
}

func TestLocalHandlerServesNoBrowserRoutes(t *testing.T) {
	local := localHandler(t, newHandler(t, testConfig(t)))
	for _, target := range []string{"/", "/sidebar", "/assets/app.css", "/browser-access/status", "/service-worker.js"} {
		response := httptest.NewRecorder()
		local.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gripi"+target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s = %d %s", target, response.Code, response.Body.String())
		}
	}
}

func TestLocalHandlerIsUnavailableInMultiUserMode(t *testing.T) {
	cfg := testConfig(t)
	cfg.MultiUserMode = true
	if local := localHandler(t, newHandler(t, cfg)); local != nil {
		t.Fatal("multi-user gateway offers a handler that bypasses workspace access")
	}
}

// fakePiGateway serves two idle sessions, "alpha" and the more recent "beta", backed by the fake Pi.
func fakePiGateway(t *testing.T) (handler http.Handler, alpha, beta, project string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	sessionsRoot := filepath.Join(home, ".pi", "agent", "sessions")
	project = filepath.Join(root, "project")
	for _, directory := range []string{sessionsRoot, project} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alpha, beta = filepath.Join(sessionsRoot, "alpha.jsonl"), filepath.Join(sessionsRoot, "beta.jsonl")
	writeLocalSession(t, alpha, "alpha-0001", project, "2026-01-01T00:00:0")
	writeLocalSession(t, beta, "beta-0002", project, "2026-01-02T00:00:0")
	_, file, _, _ := runtime.Caller(0)
	t.Setenv("GRIPI_E2E_SESSIONS_ROOT", sessionsRoot)
	t.Setenv("GRIPI_E2E_FAKE_PI_LOG", filepath.Join(root, "fake-pi.log"))
	handler, err := gateway.NewHandler(config.Config{
		Address: "127.0.0.1:4567", Environment: "test", Home: home,
		SessionsRoot: sessionsRoot, AttachmentsRoot: filepath.Join(home, ".pi", "gripi", "attachments"),
		ReadStatePath: filepath.Join(root, "read.json"), PinnedSessionsPath: filepath.Join(root, "pinned.json"), SessionTagsPath: filepath.Join(root, "tags.json"),
		BrowserAccessPath: filepath.Join(root, "browser.json"), BrowserAuthDisabled: true,
		PiCommand: []string{"node", filepath.Join(filepath.Dir(file), "..", "..", "e2e", "support", "fake_pi.mjs")},
	}, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	return handler, alpha, beta, project
}

func writeLocalSession(t *testing.T, path, id, cwd, timestampPrefix string) {
	t.Helper()
	records := []map[string]any{
		{"type": "session", "version": 3, "id": id, "timestamp": timestampPrefix + "0Z", "cwd": cwd},
		{"type": "message", "id": "user-1", "parentId": nil, "timestamp": timestampPrefix + "1Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Prompt for " + id}}}},
		{"type": "message", "id": "assistant-1", "parentId": "user-1", "timestamp": timestampPrefix + "2Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Answer from " + id}}, "stopReason": "stop"}},
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
}

func localSessions(t *testing.T, local http.Handler, query string) []gateway.LocalSession {
	t.Helper()
	response := httptest.NewRecorder()
	local.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gripi/sessions"+query, nil))
	var payload struct {
		Sessions []gateway.LocalSession `json:"sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || response.Code != http.StatusOK {
		t.Fatalf("sessions%s = %d %s (%v)", query, response.Code, response.Body.String(), err)
	}
	return payload.Sessions
}

func waitForLocalSession(t *testing.T, local http.Handler, path string, done func(gateway.LocalSession) bool) gateway.LocalSession {
	t.Helper()
	var last []gateway.LocalSession
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if last = localSessions(t, local, "?session="+url.QueryEscape(path)); len(last) == 1 && done(last[0]) {
			return last[0]
		}
	}
	t.Fatalf("session never reached the expected state: %+v", last)
	return gateway.LocalSession{}
}

func promptOverTCP(t *testing.T, handler http.Handler, fields url.Values) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4567/prompt", strings.NewReader(fields.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("prompt %v = %d %s", fields, response.Code, response.Body.String())
	}
}

func TestLocalSessionsReportFinishedRepliesWithoutMarkingThemRead(t *testing.T) {
	handler, alpha, beta, project := fakePiGateway(t)
	local := localHandler(t, handler)

	response := httptest.NewRecorder()
	local.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gripi/sessions", nil))
	if !strings.Contains(response.Body.String(), `"tags":[]`) {
		t.Fatalf("untagged sessions need an empty list, not null: %s", response.Body.String())
	}
	all := localSessions(t, local, "")
	if len(all) != 2 || all[0].Path != beta || all[1].Path != alpha {
		t.Fatalf("sessions are not ordered by latest activity: %+v", all)
	}
	expected := gateway.LocalSession{ID: "alpha-0001", Path: alpha, Name: "Prompt for alpha-0001", CWD: project, State: "idle", Tags: []string{}, UpdatedAt: time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC), LastReply: "Answer from alpha-0001"}
	if got := all[1]; got.ID != expected.ID || got.Name != expected.Name || got.CWD != expected.CWD || got.State != expected.State || got.Unread || got.Pinned || !got.UpdatedAt.Equal(expected.UpdatedAt) || got.LastReply != expected.LastReply {
		t.Fatalf("session = %+v, expected %+v", got, expected)
	}
	if missing := localSessions(t, local, "?session="+url.QueryEscape(filepath.Join(project, "missing.jsonl"))); len(missing) != 0 {
		t.Fatalf("unknown session = %+v", missing)
	}

	promptOverTCP(t, handler, url.Values{"session": {alpha}, "message": {"Show the deterministic browser response"}})
	waitForLocalSession(t, local, alpha, func(session gateway.LocalSession) bool { return session.State == "idle" && session.Unread })
	if again := localSessions(t, local, ""); again[0].Path != alpha || !again[0].Unread {
		t.Fatalf("listing marked the finished session read or kept the old order: %+v", again)
	}
	opened := httptest.NewRecorder()
	handler.ServeHTTP(opened, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4567/?session="+url.QueryEscape(alpha), nil))
	if opened.Code != http.StatusOK {
		t.Fatalf("open session = %d", opened.Code)
	}
	waitForLocalSession(t, local, alpha, func(session gateway.LocalSession) bool { return !session.Unread })
}

func TestPromptsReportWhatPiDidWithThem(t *testing.T) {
	handler, alpha, _, _ := fakePiGateway(t)
	local := localHandler(t, handler)
	disposition := func(message string) any {
		t.Helper()
		fields := url.Values{"session": {alpha}, "message": {message}, "streaming_behavior": {"follow_up"}}
		request := httptest.NewRequest(http.MethodPost, "http://gripi/prompt", strings.NewReader(fields.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Accept", "application/json")
		response := httptest.NewRecorder()
		local.ServeHTTP(response, request)
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || response.Code != http.StatusOK {
			t.Fatalf("prompt %q = %d %s (%v)", message, response.Code, response.Body.String(), err)
		}
		return payload["disposition"]
	}
	// An extension command starts no turn, which 'gripi send' must know to not wait for one.
	if got := disposition("/immediate-command"); got != "handled" {
		t.Fatalf("extension command = %v", got)
	}
	if got := disposition("Start the follow-up scenario"); got != "started" {
		t.Fatalf("prompt to an idle session = %v", got)
	}
	if got := disposition("Continue with the queued follow-up"); got != "queued" {
		t.Fatalf("prompt during a turn = %v", got)
	}
}

func TestPromptsRefuseTheGatewaysOwnExtensionCommands(t *testing.T) {
	handler, alpha, _, _ := fakePiGateway(t)
	prompt := func(target http.Handler, host string, fields url.Values) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "http://"+host+"/prompt", strings.NewReader(fields.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Accept", "application/json")
		response := httptest.NewRecorder()
		target.ServeHTTP(response, request)
		return response
	}
	for host, target := range map[string]http.Handler{"127.0.0.1:4567": handler, "gripi": localHandler(t, handler)} {
		for _, message := range []string{"/gripi_reload abc123 e30", "/gripi_tree_navigate abc123 e30", "/gripi_scoped_models"} {
			for _, behavior := range []string{"", "steer", "follow_up"} {
				response := prompt(target, host, url.Values{"session": {alpha}, "message": {message}, "streaming_behavior": {behavior}})
				if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), strings.Fields(message)[0]) {
					t.Fatalf("%q with behavior %q on %s = %d %s", message, behavior, host, response.Code, response.Body.String())
				}
			}
		}
	}
	if _, err := os.Stat(os.Getenv("GRIPI_E2E_FAKE_PI_LOG")); !os.IsNotExist(err) {
		t.Fatalf("Pi was started for a refused message (%v)", err)
	}
	// Only a message that Pi would run as the command is refused.
	if response := prompt(handler, "127.0.0.1:4567", url.Values{"session": {alpha}, "message": {"What does /gripi_reload do?"}}); response.Code != http.StatusOK {
		t.Fatalf("message that mentions a command = %d %s", response.Code, response.Body.String())
	}
}

func TestSessionListForCommandsIsNotServedToBrowsers(t *testing.T) {
	response := httptest.NewRecorder()
	newHandler(t, testConfig(t)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4567/sessions", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("GET /sessions over TCP = %d %s", response.Code, response.Body.String())
	}
}
