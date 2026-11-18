package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
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
	"github.com/melounvitek/gripi/internal/sessions"
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
		// Pi CLI waits for the prompt too, so it counts as sent.
		{name: "prompt", route: "/prompt", command: "prompt", form: url.Values{"message": {"/deploy"}}, status: http.StatusOK},
		{name: "steer", route: "/prompt", command: "prompt", form: url.Values{"message": {"/deploy"}, "streaming_behavior": {"steer"}}, status: http.StatusOK},
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

// A prompt that Pi answers only after the gateway stops waiting counts as sent, so that sending it again cannot run it
// twice. One that Pi refuses then comes back to the editor with its images.
func TestAPromptThatPiAnswersLateCountsAsSent(t *testing.T) {
	for _, test := range []struct {
		name, behavior string
		late           map[string]any
	}{
		{name: "refused prompt", late: map[string]any{"success": false, "error": "No API key found for e2e."}},
		{name: "refused follow-up", behavior: "follow_up", late: map[string]any{"success": false, "error": "No API key found for e2e."}},
		{name: "taken prompt", late: map[string]any{"success": true, "data": map[string]any{"disposition": "started"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				pi := newSlowPi(t, "prompt")
				cursor := pi.client.EventSequence()
				response := pi.postImage(url.Values{"message": {"Summarise the logs"}, "streaming_behavior": {test.behavior}})
				if response.Code != http.StatusOK {
					t.Fatalf("response = %d %s", response.Code, response.Body.String())
				}
				if !pi.client.Busy() {
					t.Fatal("the session reads as idle while Pi holds its prompt")
				}
				held := <-pi.held
				answer := map[string]any{"type": "response", "id": held["id"], "command": "prompt"}
				for key, value := range test.late {
					answer[key] = value
				}
				if err := json.NewEncoder(pi.stdout).Encode(answer); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if pi.client.Busy() || !pi.running() {
					t.Fatalf("after Pi's answer busy = %v, running = %v", pi.client.Busy(), pi.running())
				}
				refused := refusedPrompts(t, pi.client.EventsAfter(cursor).Events)
				if test.late["success"] == true {
					if len(refused) != 0 {
						t.Fatalf("a prompt that Pi took came back: %#v", refused)
					}
					return
				}
				if len(refused) != 1 || refused[0].EditorText != "Summarise the logs" || refused[0].Error != "Prompt failed to send: No API key found for e2e." || len(refused[0].Images) != 1 {
					t.Fatalf("refused prompts = %#v", refused)
				}
				// The browser loads the image from the attachments that the gateway keeps.
				image := refused[0].Images[0]
				file, found := strings.CutPrefix(image.Src, "/attachments/")
				if contents, err := os.ReadFile(filepath.Join(pi.app.config.AttachmentsRoot, file)); !found || err != nil || string(contents) != slowPiImage || image.MIMEType != "image/png" {
					t.Fatalf("image %#v: %q, %v", image, contents, err)
				}
			})
		})
	}
}

// A message from a sub-session's parent that Pi refuses late gets the parent a report at once, as no run follows.
func TestASubSessionReportsAPromptThatPiRefusesLate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pi := newSlowPi(t, "prompt")
		reported := pi.reportToParent(t)
		response := pi.postFromParent(url.Values{"message": {"Run the tests"}, "streaming_behavior": {"follow_up"}})
		if response.Code != http.StatusOK {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		held := <-pi.held
		if err := json.NewEncoder(pi.stdout).Encode(map[string]any{"type": "response", "id": held["id"], "command": "prompt", "success": false, "error": "No API key found for e2e."}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case report := <-reported:
			if !strings.Contains(report, "failed: No API key found for e2e.") {
				t.Fatalf("report = %q", report)
			}
		default:
			t.Fatal("no report")
		}
	})
}

// Pi that handles a message itself late, such as with an extension's command, starts no run, so a later run owes the
// parent nothing.
func TestAPromptThatPiHandlesLateOwesNoReport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pi := newSlowPi(t, "prompt")
		reported := pi.reportToParent(t)
		if response := pi.postFromParent(url.Values{"message": {"/deploy"}, "streaming_behavior": {"follow_up"}}); response.Code != http.StatusOK {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		held := <-pi.held
		for _, record := range []map[string]any{
			{"type": "response", "id": held["id"], "command": "prompt", "success": true, "data": map[string]any{"disposition": "handled"}},
			// A later run, such as from the browser.
			{"type": "agent_start"}, {"type": "agent_end", "messages": []any{}}, {"type": "agent_settled"},
		} {
			if err := json.NewEncoder(pi.stdout).Encode(record); err != nil {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		select {
		case report := <-reported:
			t.Fatalf("a later run reported %q", report)
		default:
		}
	})
}

type refusedPrompt struct {
	Error      string `json:"error"`
	EditorText string `json:"editorText"`
	Images     []struct {
		Src      string `json:"src"`
		MIMEType string `json:"mimeType"`
	} `json:"images"`
}

func refusedPrompts(t *testing.T, events []json.RawMessage) []refusedPrompt {
	t.Helper()
	var refused []refusedPrompt
	for _, event := range events {
		var prompt struct {
			Type string `json:"type"`
			refusedPrompt
		}
		if err := json.Unmarshal(event, &prompt); err != nil {
			t.Fatal(err)
		}
		if prompt.Type == "prompt_refused" {
			refused = append(refused, prompt.refusedPrompt)
		}
	}
	return refused
}

func TestStopClosesAPiThatAnswersNeitherAPromptNorTheStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pi := newSlowPi(t, "prompt", "abort")
		if response := pi.post("/prompt", url.Values{"message": {"/deploy"}}); response.Code != http.StatusOK {
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
	parent  string
}

// newSlowPi serves a new session through a Pi that answers each command at once, but holds the slow ones until the
// test answers them.
func newSlowPi(t *testing.T, slow ...string) *slowPi {
	t.Helper()
	root := t.TempDir()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	pi := &slowPi{path: filepath.Join(root, "new.jsonl"), held: make(chan map[string]any, len(slow)), stdout: stdoutWriter}
	// Pi's lifecycle reaches the gateway, as the adapter passes it on.
	observer := func(_ *rpc.Client, event map[string]any) {
		if lifecycle, ok := agentpi.Lifecycle(event); ok {
			pi.app.subSessionReports.Observe(pi.client, lifecycle)
		}
	}
	pi.client = &agentpi.Client{Client: rpc.NewClient(stdinWriter, stdoutReader, nil, rpc.ClientOptions{EventObserver: observer})}
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
	pi.app.subSessionReports.app = pi.app
	mux := http.NewServeMux()
	pi.app.registerActionRoutes(mux)
	pi.handler = mux
	return pi
}

const slowPiImage = "\x89PNG fake image"

// postImage sends a prompt with an image, as the browser does.
func (pi *slowPi) postImage(fields url.Values) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields.Set("session", pi.path)
	for key, values := range fields {
		for _, value := range values {
			_ = writer.WriteField(key, value)
		}
	}
	part, _ := writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="images[]"; filename="logs.png"`}, "Content-Type": {"image/png"}})
	_, _ = part.Write([]byte(slowPiImage))
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/prompt", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()
	pi.handler.ServeHTTP(response, request)
	return response
}

// reportToParent makes the session a sub-session of another, and returns the reports that it sends there.
func (pi *slowPi) reportToParent(t *testing.T) <-chan string {
	t.Helper()
	pi.parent = filepath.Join(filepath.Dir(pi.path), "parent.jsonl")
	pi.app.pendingSessions.Remember(pi.parent, filepath.Dir(pi.path), "")
	pi.app.gatewayState = sessions.NewGatewayState(filepath.Join(t.TempDir(), "state.json"), "", "", nil)
	if err := pi.app.gatewayState.SetSessionParent(pi.path, pi.parent); err != nil {
		t.Fatal(err)
	}
	reported := make(chan string, 4)
	pi.app.commandRoutes = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		reported <- request.FormValue("message")
		writeJSON(response, map[string]any{"session": request.FormValue("session")})
	})
	return reported
}

// postFromParent sends a message from the parent, as gripi send run there does.
func (pi *slowPi) postFromParent(fields url.Values) *httptest.ResponseRecorder {
	form := url.Values{"session": {pi.path}, "sender": {pi.parent}, "bash_mode": {"prompt"}}
	for key, values := range fields {
		form[key] = values
	}
	request := httptest.NewRequest(http.MethodPost, "/prompt", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()
	commandRequests(pi.handler).ServeHTTP(response, request)
	return response
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
