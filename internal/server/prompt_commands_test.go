package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	gateway "github.com/melounvitek/gripi/internal/server"
)

func TestPromptsRefusedBeforePiStarts(t *testing.T) {
	handler, alpha, _, _ := fakePiGateway(t)
	for _, test := range []struct {
		message, refusal string
		image            bool
	}{
		{"/gripi_tree_navigate abc123 e30", "/gripi_tree_navigate is a command that Gripi uses itself, so it cannot be sent as a message", false},
		{"!ls", "Images cannot be attached to bash commands", true},
		{"!!ls", "Images cannot be attached to bash commands", true},
	} {
		for _, wantsJSON := range []bool{true, false} {
			fields := map[string]string{"session": alpha, "message": test.message}
			request := formActionRequest("/prompt", fields, wantsJSON)
			if test.image {
				request = multipartRequest(t, "/prompt", fields, "images[]", "screenshot.png", "image/png", []byte("image"))
				if wantsJSON {
					request.Header.Set("Accept", "application/json")
				}
			}
			expected := test.refusal
			if wantsJSON {
				expected = `{"error":"` + test.refusal + `"}`
			}
			if response := serveAction(handler, request); response.Code != http.StatusBadRequest || response.Body.String() != expected {
				t.Errorf("%q (JSON %v) = %d %s", test.message, wantsJSON, response.Code, response.Body.String())
			}
		}
	}
	if _, err := os.Stat(os.Getenv("GRIPI_E2E_FAKE_PI_LOG")); !os.IsNotExist(err) {
		t.Fatalf("Pi was started for a refused prompt (%v)", err)
	}
}

func TestNameWithoutAnArgumentShowsTheCurrentName(t *testing.T) {
	handler, alpha, beta, _ := fakePiGateway(t)
	if response := serveAction(handler, formActionRequest("/prompt", map[string]string{"session": beta, "message": "/name Release planning"}, true)); response.Code != http.StatusOK {
		t.Fatalf("naming = %d %s", response.Code, response.Body.String())
	}
	for path, expected := range map[string]map[string]any{
		alpha: {"command": "name", "error": "Usage: /name <name>"},
		beta:  {"command": "name", "name": "Release planning", "current": true},
	} {
		code, payload := sendPrompt(t, handler, path, "/name", "", false)
		if expected := promptResult(path, expected); code != http.StatusOK || !reflect.DeepEqual(payload, expected) {
			t.Errorf("/name in %s = %d %#v, expected %#v", filepath.Base(path), code, payload, expected)
		}
	}
}

func TestExtensionCommandsRunWithoutAttachedImages(t *testing.T) {
	cfg, alpha, _, _ := fakePiConfig(t)
	handler := startGateway(t, cfg)
	// The first command starts Pi, so the steered and follow-up commands reach the running client.
	for _, behavior := range []string{"", "steer", "follow_up"} {
		code, payload := sendPrompt(t, handler, alpha, "/immediate-command", behavior, true)
		expected := promptResult(alpha, map[string]any{"disposition": "handled", "running": false, "compacting": false})
		if behavior != "" {
			expected[behavior] = true
		}
		if code != http.StatusOK || !reflect.DeepEqual(payload, expected) {
			t.Errorf("command with behavior %q = %d %#v, expected %#v", behavior, code, payload, expected)
		}
	}
	expectCommandsWithoutImages(t, cfg.AttachmentsRoot, 3)
}

func TestExtensionCommandsRunDuringCompaction(t *testing.T) {
	cfg, alpha, _, _ := fakePiConfig(t)
	handler := startGateway(t, cfg)
	compacted := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		compacted <- serveAction(handler, formActionRequest("/compact", map[string]string{"session": alpha}, false))
	}()
	// Polling the gateway before the compaction holds the session would make it refuse the compaction as a concurrent operation.
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(readFakePiLog(t), `"type":"compact"`); time.Sleep(25 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("Pi never received the compaction")
		}
	}
	waitForLocalSession(t, localHandler(t, handler), alpha, func(session gateway.LocalSession) bool { return session.State == "compacting" })
	for _, test := range []struct {
		behavior string
		image    bool
	}{{"steer", true}, {"follow_up", false}} {
		code, payload := sendPrompt(t, handler, alpha, "/immediate-command", test.behavior, test.image)
		expected := promptResult(alpha, map[string]any{test.behavior: true, "disposition": "handled", "running": false, "compacting": true})
		if code != http.StatusOK || !reflect.DeepEqual(payload, expected) {
			t.Errorf("command with behavior %q during compaction = %d %#v, expected %#v", test.behavior, code, payload, expected)
		}
	}
	if response := <-compacted; response.Code != http.StatusSeeOther {
		t.Fatalf("compaction = %d %s", response.Code, response.Body.String())
	}
	expectCommandsWithoutImages(t, cfg.AttachmentsRoot, 2)
}

func sendPrompt(t *testing.T, handler http.Handler, path, message, behavior string, image bool) (int, map[string]any) {
	t.Helper()
	fields := map[string]string{"session": path, "message": message, "streaming_behavior": behavior}
	request := formActionRequest("/prompt", fields, true)
	if image {
		request = multipartRequest(t, "/prompt", fields, "images[]", "screenshot.png", "image/png", []byte("image"))
		request.Header.Set("Accept", "application/json")
	}
	response := serveAction(handler, request)
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("prompt %q = %d %s (%v)", message, response.Code, response.Body.String(), err)
	}
	return response.Code, payload
}

func promptResult(path string, values map[string]any) map[string]any {
	result := map[string]any{"session": path, "redirect": "/?" + url.Values{"session": {path}}.Encode()}
	for key, value := range values {
		result[key] = value
	}
	return result
}

// expectCommandsWithoutImages checks that Pi ran every extension command without the attached images and that none were stored.
func expectCommandsWithoutImages(t *testing.T, attachmentsRoot string, count int) {
	t.Helper()
	commands := 0
	for _, line := range strings.Split(strings.TrimSpace(readFakePiLog(t)), "\n") {
		var entry struct {
			Command map[string]any `json:"command"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Command["type"] == "prompt" && entry.Command["message"] == "/immediate-command" {
			commands++
			if entry.Command["images"] != nil {
				t.Errorf("Pi received images with the extension command: %v", entry.Command)
			}
		}
	}
	if commands != count {
		t.Errorf("Pi received %d extension commands, expected %d", commands, count)
	}
	if stored, err := os.ReadDir(attachmentsRoot); len(stored) != 0 || !os.IsNotExist(err) {
		t.Errorf("attachments = %v (%v)", stored, err)
	}
}

func readFakePiLog(t *testing.T) string {
	t.Helper()
	log, err := os.ReadFile(os.Getenv("GRIPI_E2E_FAKE_PI_LOG"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(log)
}
