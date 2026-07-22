package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestGoGatewayMutationRoutesUseNativeFakePiContracts(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	sessionsRoot := filepath.Join(home, ".pi", "agent", "sessions")
	attachmentsRoot := filepath.Join(home, ".pi", "gripi", "attachments")
	project := filepath.Join(root, "project")
	for _, directory := range []string{sessionsRoot, attachmentsRoot, project} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	sessionPath := filepath.Join(sessionsRoot, "fixture.jsonl")
	writeActionSession(t, sessionPath, project)
	_, file, _, _ := runtime.Caller(0)
	fakePi := filepath.Join(filepath.Dir(file), "..", "..", "e2e", "support", "fake_pi.mjs")
	fakeLog := filepath.Join(root, "fake-pi.log")
	t.Setenv("GRIPI_E2E_SESSIONS_ROOT", sessionsRoot)
	t.Setenv("GRIPI_E2E_FAKE_PI_LOG", fakeLog)
	cfg := config.Config{
		Address: "127.0.0.1:4567", Environment: "test", Home: home,
		SessionsRoot: sessionsRoot, AttachmentsRoot: attachmentsRoot,
		ReadStatePath: filepath.Join(root, "read.json"), PinnedSessionsPath: filepath.Join(root, "pinned.json"),
		BrowserAccessPath: filepath.Join(root, "browser.json"), BrowserAuthDisabled: true,
		PiCommand: []string{"node", fakePi}, RPCIdleTimeout: 0,
	}
	handler, err := gateway.NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closer, ok := handler.(interface{ Close(context.Context) error }); ok {
			_ = closer.Close(context.Background())
		}
	}()

	prompt := multipartRequest(t, "/prompt", map[string]string{"session": sessionPath, "message": "Go mutation integration"}, "images[]", "fixture.png", "image/png", []byte("fixture-image"))
	prompt.Header.Set("Accept", "application/json")
	promptResponse := serveAction(handler, prompt)
	if promptResponse.Code != http.StatusOK {
		t.Fatalf("prompt = %d %s", promptResponse.Code, promptResponse.Body.String())
	}
	var promptPayload map[string]any
	decodeActionJSON(t, promptResponse, &promptPayload)
	if promptPayload["session"] != sessionPath || !strings.Contains(promptPayload["redirect"].(string), url.QueryEscape(sessionPath)) {
		t.Fatalf("prompt payload = %#v", promptPayload)
	}
	waitForDeferredSidebar(t, handler)
	metadata, err := os.ReadFile(filepath.Join(attachmentsRoot, sessions.SessionHash(sessionPath)+".jsonl"))
	if err != nil || !strings.Contains(string(metadata), `"count":1`) || !strings.Contains(string(metadata), `"mime_types":["image/png"]`) {
		t.Fatalf("attachment metadata = %s, %v", metadata, err)
	}

	waitForFakePiSettled(t, handler, sessionPath)
	reloaded := serveAction(handler, getActionRequest("/?session="+url.QueryEscape(sessionPath)))
	if reloaded.Code != http.StatusOK || !strings.Contains(reloaded.Body.String(), "/attachments/"+sessions.SessionHash(sessionPath)+"/") || !strings.Contains(reloaded.Body.String(), `alt="Attached image"`) {
		t.Fatalf("reloaded attachment = %d %s", reloaded.Code, reloaded.Body.String())
	}
	bash := formActionRequest("/prompt", map[string]string{"session": sessionPath, "message": "!!printf go"}, true)
	bashResponse := serveAction(handler, bash)
	if bashResponse.Code != http.StatusOK || !strings.Contains(bashResponse.Body.String(), `"command":"bash"`) || !strings.Contains(bashResponse.Body.String(), `"exclude_from_context":true`) {
		t.Fatalf("bash = %d %s", bashResponse.Code, bashResponse.Body.String())
	}

	settingsResponse := serveAction(handler, getActionRequest("/sessions/model_settings?session="+url.QueryEscape(sessionPath)))
	if settingsResponse.Code != http.StatusOK || !strings.Contains(settingsResponse.Body.String(), `"fixture-model"`) {
		t.Fatalf("model settings = %d %s", settingsResponse.Code, settingsResponse.Body.String())
	}
	setSettings := serveAction(handler, formActionRequest("/sessions/model_settings", map[string]string{"session": sessionPath, "provider": "e2e", "model": "contract-model", "thinking": "high"}, true))
	if setSettings.Code != http.StatusOK || !strings.Contains(setSettings.Body.String(), `"thinking":"high"`) {
		t.Fatalf("set model = %d %s", setSettings.Code, setSettings.Body.String())
	}
	cycle := serveAction(handler, formActionRequest("/sessions/cycle_thinking", map[string]string{"session": sessionPath}, true))
	if cycle.Code != http.StatusOK || !strings.Contains(cycle.Body.String(), `"thinking":"off"`) {
		t.Fatalf("cycle thinking = %d %s", cycle.Code, cycle.Body.String())
	}

	tree := serveAction(handler, getActionRequest("/sessions/tree_entries?session="+url.QueryEscape(sessionPath)+"&filter=all"))
	if tree.Code != http.StatusOK || !strings.Contains(tree.Body.String(), `"settings"`) || !strings.Contains(tree.Body.String(), `"entries"`) {
		t.Fatalf("tree = %d %s", tree.Code, tree.Body.String())
	}
	var treePayload map[string]any
	decodeActionJSON(t, tree, &treePayload)
	entries := treePayload["entries"].([]any)
	entryID := entries[0].(map[string]any)["entryId"].(string)
	label := serveAction(handler, formActionRequest("/sessions/tree/label", map[string]string{"session": sessionPath, "entry_id": entryID, "label": "Checkpoint"}, true))
	if label.Code != http.StatusOK || !strings.Contains(label.Body.String(), `"label":"Checkpoint"`) {
		t.Fatalf("tree label = %d %s", label.Code, label.Body.String())
	}
	navigate := serveAction(handler, formActionRequest("/sessions/tree", map[string]string{"session": sessionPath, "entry_id": entryID, "summary_mode": "none"}, true))
	if navigate.Code != http.StatusOK || !strings.Contains(navigate.Body.String(), `"cancelled":false`) {
		t.Fatalf("tree navigate = %d %s", navigate.Code, navigate.Body.String())
	}

	compact := serveAction(handler, formActionRequest("/compact", map[string]string{"session": sessionPath, "instructions": "Keep decisions"}, false))
	if compact.Code != http.StatusSeeOther {
		t.Fatalf("compact = %d %s", compact.Code, compact.Body.String())
	}
	forkPoints := serveAction(handler, getActionRequest("/sessions/fork_messages?session="+url.QueryEscape(sessionPath)))
	if forkPoints.Code != http.StatusOK || !strings.Contains(forkPoints.Body.String(), `"entryId"`) {
		t.Fatalf("fork messages = %d %s", forkPoints.Code, forkPoints.Body.String())
	}
	clone := serveAction(handler, formActionRequest("/sessions/clone", map[string]string{"session": sessionPath}, true))
	if clone.Code != http.StatusOK {
		t.Fatalf("clone = %d %s", clone.Code, clone.Body.String())
	}
	var clonePayload map[string]any
	decodeActionJSON(t, clone, &clonePayload)
	clonePath := clonePayload["session"].(string)
	if clonePath == sessionPath || !strings.Contains(clonePayload["redirect"].(string), url.QueryEscape(clonePath)) {
		t.Fatalf("clone payload = %#v", clonePayload)
	}
	fork := serveAction(handler, formActionRequest("/sessions/fork", map[string]string{"session": clonePath, "entry_id": entryID}, true))
	if fork.Code != http.StatusOK || !strings.Contains(fork.Body.String(), `"text"`) {
		t.Fatalf("fork = %d %s", fork.Code, fork.Body.String())
	}

	newSession := serveAction(handler, formActionRequest("/sessions/new_at_cwd", map[string]string{"cwd": project, "project": filepath.Join(root, "stale-project")}, true))
	if newSession.Code != http.StatusOK || !strings.Contains(newSession.Body.String(), `"session"`) {
		t.Fatalf("new session = %d %s", newSession.Code, newSession.Body.String())
	}
	var newPayload map[string]any
	decodeActionJSON(t, newSession, &newPayload)
	if strings.Contains(newPayload["redirect"].(string), "project=") {
		t.Fatalf("new session redirect retained stale project filter: %#v", newPayload)
	}
	pendingPath := newPayload["session"].(string)
	pendingPage := serveAction(handler, getActionRequest("/?session="+url.QueryEscape(pendingPath)))
	if pendingPage.Code != http.StatusOK || !strings.Contains(pendingPage.Body.String(), "New session (pending first assistant response)") {
		t.Fatalf("pending session page = %d %s", pendingPage.Code, pendingPage.Body.String())
	}
	pendingClone := serveAction(handler, formActionRequest("/sessions/clone", map[string]string{"session": pendingPath}, true))
	if pendingClone.Code != http.StatusOK {
		t.Fatalf("pending clone = %d %s", pendingClone.Code, pendingClone.Body.String())
	}
	sidebarAfterPendingClone := serveAction(handler, getActionRequest("/sidebar"))
	if strings.Contains(sidebarAfterPendingClone.Body.String(), pendingPath) {
		t.Fatalf("old pending session remained in sidebar: %s", sidebarAfterPendingClone.Body.String())
	}

	log, err := os.ReadFile(fakeLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"type":"prompt"`, `"mimeType":"image/png"`, `"type":"bash"`, `"excludeFromContext":true`, `"type":"set_model"`, `"modelId":"contract-model"`, `/gripi_tree_snapshot`, `"type":"compact"`, `"type":"clone"`, `"type":"fork"`} {
		if !strings.Contains(string(log), expected) {
			t.Errorf("fake Pi log does not contain %s", expected)
		}
	}
}

func TestGoGatewayRejectsInvalidMutationValuesBeforeStartingPi(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	sessionsRoot := filepath.Join(root, "sessions")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionsRoot, "session.jsonl")
	writeActionSession(t, path, project)
	cfg := config.Config{Address: "127.0.0.1:4567", Environment: "test", Home: root, SessionsRoot: sessionsRoot, AttachmentsRoot: filepath.Join(root, "attachments"), ReadStatePath: filepath.Join(root, "read"), PinnedSessionsPath: filepath.Join(root, "pinned"), BrowserAccessPath: filepath.Join(root, "browser"), BrowserAuthDisabled: true, PiCommand: []string{"false"}}
	handler, err := gateway.NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, route string
		values      map[string]string
		message     string
	}{
		{"empty prompt", "/prompt", map[string]string{"session": path, "message": "  "}, "Message cannot be empty"},
		{"streaming behavior", "/prompt", map[string]string{"session": path, "message": "hello", "streaming_behavior": "later"}, "Invalid streaming behavior"},
		{"thinking level", "/sessions/model_settings", map[string]string{"session": path, "provider": "e2e", "model": "fixture", "thinking": "extreme"}, "Invalid thinking level"},
		{"provider bytes", "/sessions/model_settings", map[string]string{"session": path, "provider": strings.Repeat("p", providerIDTestBytes+1), "model": "fixture", "thinking": "off"}, "Provider is too long"},
		{"model bytes", "/sessions/model_settings", map[string]string{"session": path, "provider": "e2e", "model": strings.Repeat("m", modelIDTestBytes+1), "thinking": "off"}, "Model is too long"},
		{"tree entry", "/sessions/tree", map[string]string{"session": path, "entry_id": strings.Repeat("x", treeEntryIDTestBytes+1)}, "Tree entry id is too long"},
		{"fork entry", "/sessions/fork", map[string]string{"session": path, "entry_id": strings.Repeat("x", treeEntryIDTestBytes+1)}, "Fork entry id is too long"},
		{"tree label", "/sessions/tree/label", map[string]string{"session": path, "entry_id": "entry", "label": strings.Repeat("x", treeLabelTestBytes+1)}, "Label is too long"},
		{"extension id", "/extension_ui_response", map[string]string{"session": path, "id": strings.Repeat("x", extensionIDTestBytes+1), "cancelled": "true"}, "Missing extension UI request id"},
		// Ruby relies on the global body cap here; Go's lower per-value cap avoids retaining a giant editor response while preserving normal Pi editor workflows.
		{"extension value", "/extension_ui_response", map[string]string{"session": path, "id": "dialog", "value": strings.Repeat("x", extensionValueTestBytes+1)}, "Extension UI response is too long"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := serveAction(handler, formActionRequest(test.route, test.values, true))
			if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), test.message) {
				t.Fatalf("response = %d %s", result.Code, result.Body.String())
			}
		})
	}
}

const (
	treeEntryIDTestBytes    = 1_024
	treeLabelTestBytes      = 4_096
	extensionIDTestBytes    = 1_024
	providerIDTestBytes     = 4_096
	modelIDTestBytes        = 4_096
	extensionValueTestBytes = 1 << 20
)

func TestGoGatewayRejectsForgedSessionPathsWithoutStartingPi(t *testing.T) {
	root := t.TempDir()
	sessionsRoot := filepath.Join(root, "sessions")
	if err := os.Mkdir(sessionsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "pi-started")
	script := filepath.Join(root, "pi")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Address: "127.0.0.1:4567", Environment: "test", Home: root, SessionsRoot: sessionsRoot, AttachmentsRoot: filepath.Join(root, "attachments"), BrowserAccessPath: filepath.Join(root, "browser"), BrowserAuthDisabled: true, PiCommand: []string{script}}
	handler, err := gateway.NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/sessions/new", "/abort"} {
		response := serveAction(handler, formActionRequest(route, map[string]string{"session": filepath.Join(root, "forged.jsonl")}, true))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s = %d %s", route, response.Code, response.Body.String())
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Pi start marker error = %v", err)
	}
}

func TestGoGatewayValidatesAndBrowsesNewSessionDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "alpine", ".away", "beta"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{Address: "127.0.0.1:4567", Environment: "test", Home: root, SessionsRoot: filepath.Join(root, "sessions"), AttachmentsRoot: filepath.Join(root, "attachments"), BrowserAccessPath: filepath.Join(root, "browser.json"), BrowserAuthDisabled: true, PiCommand: []string{"false"}}
	handler, err := gateway.NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	validate := serveAction(handler, getActionRequest("/sessions/validate_cwd?cwd="+url.QueryEscape(filepath.Join(root, "alpha"))))
	if validate.Code != http.StatusOK || !strings.Contains(validate.Body.String(), `"valid":true`) {
		t.Fatalf("validate = %d %s", validate.Code, validate.Body.String())
	}
	tilde := serveAction(handler, getActionRequest("/sessions/validate_cwd?cwd="+url.QueryEscape("~/alpha")))
	if tilde.Code != http.StatusOK || !strings.Contains(tilde.Body.String(), filepath.Join(root, "alpha")) {
		t.Fatalf("tilde validate = %d %s", tilde.Code, tilde.Body.String())
	}
	browse := serveAction(handler, getActionRequest("/sessions/browse_cwd?cwd="+url.QueryEscape("~/al")))
	if browse.Code != http.StatusOK || !strings.Contains(browse.Body.String(), filepath.Join(root, "alpha")) || !strings.Contains(browse.Body.String(), filepath.Join(root, "alpine")) || strings.Contains(browse.Body.String(), ".away") {
		t.Fatalf("browse = %d %s", browse.Code, browse.Body.String())
	}
	invalid := serveAction(handler, getActionRequest("/sessions/validate_cwd?cwd="+url.QueryEscape(filepath.Join(root, "missing"))))
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), `"valid":false`) {
		t.Fatalf("invalid = %d %s", invalid.Code, invalid.Body.String())
	}
	if runtime.GOOS != "windows" {
		inaccessible := filepath.Join(root, "inaccessible")
		if err := os.Mkdir(inaccessible, 0400); err != nil {
			t.Fatal(err)
		}
		blocked := serveAction(handler, getActionRequest("/sessions/validate_cwd?cwd="+url.QueryEscape(inaccessible)))
		if blocked.Code != http.StatusUnprocessableEntity || !strings.Contains(blocked.Body.String(), "Directory is not accessible") {
			t.Fatalf("inaccessible = %d %s", blocked.Code, blocked.Body.String())
		}
	}
}

func waitForDeferredSidebar(t *testing.T, handler http.Handler) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sidebar := serveAction(handler, getActionRequest("/sidebar"))
		if sidebar.Code == http.StatusOK && strings.Contains(sidebar.Body.String(), "data-sidebar-metadata-deferred") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("sidebar metadata was not deferred while Pi was busy")
}

func waitForFakePiSettled(t *testing.T, handler http.Handler, sessionPath string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		response := serveAction(handler, getActionRequest("/events?session="+url.QueryEscape(sessionPath)+"&after=0"))
		if response.Code == http.StatusOK && strings.Contains(response.Body.String(), `"type":"agent_settled"`) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("fake Pi did not settle")
}

func writeActionSession(t *testing.T, path, cwd string) {
	t.Helper()
	records := []map[string]any{
		{"type": "session", "version": 3, "id": "go-action", "timestamp": "2026-01-01T00:00:00Z", "cwd": cwd},
		{"type": "message", "id": "user-1", "parentId": nil, "timestamp": "2026-01-01T00:00:01Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Fixture prompt"}}, "timestamp": 1_767_225_601_000}},
		{"type": "message", "id": "assistant-1", "parentId": "user-1", "timestamp": "2026-01-01T00:00:02Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Fixture answer"}}, "stopReason": "stop", "timestamp": 1_767_225_602_000}},
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func multipartRequest(t *testing.T, target string, fields map[string]string, fileField, fileName, contentType string, contents []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	header := make(map[string][]string)
	header["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fileField, fileName)}
	header["Content-Type"] = []string{contentType}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4567"+target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func formActionRequest(target string, fields map[string]string, jsonResponse bool) *http.Request {
	values := url.Values{}
	for key, value := range fields {
		values.Set(key, value)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4567"+target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if jsonResponse {
		request.Header.Set("Accept", "application/json")
	}
	return request
}

func getActionRequest(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4567"+target, nil)
}

func serveAction(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeActionJSON(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil && err != io.EOF {
		t.Fatal(err)
	}
}
