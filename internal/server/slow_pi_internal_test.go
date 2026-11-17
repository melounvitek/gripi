package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/melounvitek/gripi/internal/agent"
	agentpi "github.com/melounvitek/gripi/internal/agent/pi"
	"github.com/melounvitek/gripi/internal/agent/pi/rpc"
	"github.com/melounvitek/gripi/internal/agent/pi/sessionfile"
	"github.com/melounvitek/gripi/internal/config"
)

// Pi answers a prompt once an extension's command or a compaction before it is over, and /compact, new, fork and clone
// once they are done, which may be long after the gateway stops waiting.
func TestPiKeepsRunningWhenItAnswersLate(t *testing.T) {
	for _, test := range []struct {
		name, route, command string
		form                 url.Values
		status               int
		late                 map[string]any
		closes               bool
	}{
		{name: "prompt", route: "/prompt", command: "prompt", form: url.Values{"message": {"/deploy"}}, status: http.StatusUnprocessableEntity},
		{name: "steer", route: "/prompt", command: "prompt", form: url.Values{"message": {"/deploy"}, "streaming_behavior": {"steer"}}, status: http.StatusUnprocessableEntity},
		// The live events show how the compaction ends.
		{name: "compact", route: "/prompt", command: "compact", form: url.Values{"message": {"/compact"}}, status: http.StatusOK},
		// Pi that replaces its session late no longer serves the one that the gateway gives it.
		{name: "new", route: "/prompt", command: "new_session", form: url.Values{"message": {"/new"}}, status: http.StatusUnprocessableEntity, closes: true},
		{name: "cancelled new", route: "/prompt", command: "new_session", form: url.Values{"message": {"/new"}}, status: http.StatusUnprocessableEntity, late: map[string]any{"cancelled": true}},
		{name: "clone", route: "/sessions/clone", command: "clone", status: http.StatusUnprocessableEntity, late: map[string]any{"cancelled": false}, closes: true},
		{name: "fork", route: "/sessions/fork", command: "fork", form: url.Values{"entry_id": {"entry"}}, status: http.StatusUnprocessableEntity, late: map[string]any{"text": "hello", "cancelled": false}, closes: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pi := newSlowPi(t, test.command)
				response := pi.post(test.route, test.form)
				if response.Code != test.status || test.status != http.StatusOK && !strings.Contains(response.Body.String(), "Pi RPC command timed out: "+test.command) {
					t.Fatalf("response = %d %s", response.Code, response.Body.String())
				}
				if !pi.running() {
					t.Fatal("Pi was closed while it was still answering")
				}
				held := <-pi.held
				if err := json.NewEncoder(pi.stdout).Encode(map[string]any{"type": "response", "id": held["id"], "command": test.command, "success": true, "data": test.late}); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if pi.running() == test.closes {
					t.Fatalf("Pi running after its late answer = %v", pi.running())
				}
			})
		})
	}
}

func TestStopClosesAPiThatAnswersNeitherAPromptNorTheStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pi := newSlowPi(t, "prompt", "abort")
		if response := pi.post("/prompt", url.Values{"message": {"/deploy"}}); response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
		}
		response := pi.post("/abort", nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"forced":true`) {
			t.Fatalf("stop = %d %s", response.Code, response.Body.String())
		}
		synctest.Wait()
		if pi.running() {
			t.Fatal("Pi kept running")
		}
	})
}

type slowPi struct {
	app     *application
	handler http.Handler
	path    string
	client  *agentpi.Client
	held    chan map[string]any
	stdout  io.Writer
}

// newSlowPi serves a new session through a Pi that answers each command at once, but holds the slow ones until the
// test answers them.
func newSlowPi(t *testing.T, slow ...string) *slowPi {
	t.Helper()
	root := t.TempDir()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	pi := &slowPi{path: filepath.Join(root, "new.jsonl"), client: &agentpi.Client{Client: rpc.NewClient(stdinWriter, stdoutReader, nil, rpc.ClientOptions{})}, held: make(chan map[string]any, len(slow)), stdout: stdoutWriter}
	t.Cleanup(func() { _ = pi.client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	go func() {
		decoder := json.NewDecoder(stdinReader)
		for {
			var command map[string]any
			if decoder.Decode(&command) != nil {
				return
			}
			if kind, _ := command["type"].(string); slices.Contains(slow, kind) {
				pi.held <- command
				continue
			}
			if json.NewEncoder(stdoutWriter).Encode(map[string]any{"type": "response", "id": command["id"], "command": command["type"], "success": true}) != nil {
				return
			}
		}
	}()
	registry := agent.NewRegistry(func(string) (agent.Client, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(pi.path, pi.client); err != nil {
		t.Fatal(err)
	}
	pending := agent.NewPendingSessionRegistry(nil)
	pending.Remember(pi.path, root, "")
	pi.app = &application{adapter: agentpi.New(agentpi.Options{}), config: config.Config{SessionsRoot: root, Home: root, AttachmentsRoot: filepath.Join(root, "attachments")}, sessionStore: NewPiStore(root, root), rpcClients: registry, pendingSessions: pending, guard: sessionfile.NewSynchronizer(registry)}
	mux := http.NewServeMux()
	pi.app.registerActionRoutes(mux)
	pi.handler = mux
	return pi
}

func (pi *slowPi) post(route string, fields url.Values) *httptest.ResponseRecorder {
	form := url.Values{"session": {pi.path}}
	for key, values := range fields {
		form[key] = values
	}
	request := httptest.NewRequest(http.MethodPost, route, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()
	pi.handler.ServeHTTP(response, request)
	return response
}

// running reports whether the session's Pi is still running and serves it.
func (pi *slowPi) running() bool {
	return pi.app.rpcClients.Client(pi.path) == pi.client && !pi.client.Exited()
}
