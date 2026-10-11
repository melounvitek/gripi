package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

// hermesHandler runs the gateway with the fake Hermes, which has saved the given sessions, each a map in the fake's
// state format.
func hermesHandler(t *testing.T, sessions ...map[string]any) http.Handler {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "hermes-state.json")
	encoded, err := json.Marshal(map[string]any{"sessions": sessions})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HERMES_STATE", state)
	_, file, _, _ := runtime.Caller(0)
	support := filepath.Join(filepath.Dir(file), "..", "..", "e2e", "support")
	handler, err := gateway.NewHandlerWithHermes(config.Config{
		Address: "127.0.0.1:4567", Environment: "test", Home: root,
		SessionsRoot: filepath.Join(root, "sessions"), AttachmentsRoot: filepath.Join(root, "attachments"),
		ReadStatePath: filepath.Join(root, "read.json"), PinnedSessionsPath: filepath.Join(root, "pinned.json"), SessionTagsPath: filepath.Join(root, "tags.json"),
		BrowserAccessPath: filepath.Join(root, "browser.json"), BrowserAuthDisabled: true, PermittedHosts: []string{"example.com"},
		PiCommand: []string{"node", filepath.Join(support, "fake_pi.mjs")}, HermesPath: filepath.Join(support, "fake_hermes.mjs"),
	}, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	return handler
}

func getPage(t *testing.T, handler http.Handler, target string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	body, _ := io.ReadAll(recorder.Result().Body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, recorder.Code, body)
	}
	return string(body)
}

func textUpdate(kind, text string) map[string]any {
	return map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text}}
}

func toolUpdate(kind, id string, fields map[string]any) map[string]any {
	update := map[string]any{"sessionUpdate": kind, "toolCallId": id}
	for key, value := range fields {
		update[key] = value
	}
	return update
}

func toolText(text string) []any {
	return []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}}
}

func TestAHermesSessionShowsItsSavedConversation(t *testing.T) {
	project := t.TempDir()
	handler := hermesHandler(t, map[string]any{
		"sessionId": "hermes-tests", "cwd": project, "title": "Run the test suite", "updatedAt": "2026-03-01T10:00:00Z",
		"model": "anthropic:claude-sonnet-4-5", "contextUsed": 41000,
		"history": []any{
			textUpdate("user_message_chunk", "Run the tests and fix what fails"),
			textUpdate("agent_thought_chunk", "I should run the whole suite first."),
			toolUpdate("tool_call", "tc-1", map[string]any{"title": "terminal: npm test", "kind": "execute", "content": toolText("$ npm test")}),
			toolUpdate("tool_call_update", "tc-1", map[string]any{"kind": "execute", "status": "failed", "content": toolText("1 failing: totals rounds once")}),
			toolUpdate("tool_call", "tc-2", map[string]any{"title": "read_file: src/totals.ts", "kind": "read"}),
			toolUpdate("tool_call_update", "tc-2", map[string]any{"kind": "read", "status": "completed", "content": toolText("export function total() {}")}),
			textUpdate("agent_message_chunk", "Fixed **the rounding** in `total`."),
		},
	})

	page := getPage(t, handler, "/?session="+url.QueryEscape("hermes:hermes-tests"))
	for _, expected := range []string{
		"Run the test suite",
		"Run the tests and fix what fails",
		"I should run the whole suite first.",
		"terminal: npm test",
		"1 failing: totals rounds once",
		"read_file: src/totals.ts",
		"<strong>the rounding</strong>",
		"anthropic/claude-sonnet-4-5",
	} {
		if !strings.Contains(page, expected) {
			t.Errorf("page lacks %q", expected)
		}
	}
	if !strings.Contains(page, `data-tool-call-id="tc-1"`) {
		t.Errorf("page lacks the tool call's block")
	}
}
