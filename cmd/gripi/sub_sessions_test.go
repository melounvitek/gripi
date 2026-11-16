package main

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gateway "github.com/melounvitek/gripi/internal/server"
)

// chooseModel records that a session switched to a model and thinking level, as Pi does.
func chooseModel(t *testing.T, path, model, thinking string) {
	t.Helper()
	appendSessionRecords(t, path,
		map[string]any{"type": "model_change", "id": "model-1", "parentId": "assistant-1", "timestamp": "2026-01-02T00:00:03Z", "provider": "e2e", "modelId": model},
		map[string]any{"type": "thinking_level_change", "id": "thinking-1", "parentId": "model-1", "timestamp": "2026-01-02T00:00:04Z", "thinkingLevel": thinking},
	)
}

// startsWithModel reports whether Pi saved a session with the model and thinking level.
func startsWithModel(t *testing.T, path, model, thinking string) bool {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(contents), `"modelId":"`+model+`"`) && strings.Contains(string(contents), `"thinkingLevel":"`+thinking+`"`)
}

func TestNewStartsANamedSubSessionWithItsParentsModel(t *testing.T) {
	_, beta := fakePiGateway(t)
	outsideSessions(t)
	chooseModel(t, beta, "contract-model", "high")
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI("new", directory, "Show the deterministic browser response", "--parent", "0a1-b", "--name", "Check the tests", "--json")
	started := decodeSession(t, stdout)
	if code != 0 || stderr != "" || started.Parent != beta || started.CWD != directory {
		t.Fatalf("gripi new --parent = %d, stderr %q, session %+v", code, stderr, started)
	}
	session := eventually(t, started.ID, func(session gateway.LocalSession) bool {
		return session.State == "idle" && session.Name == "Check the tests"
	})
	if session.Parent != beta || !startsWithModel(t, session.Path, "contract-model", "high") {
		t.Fatalf("sub-session %+v did not start with its parent's model", session)
	}
	// The saved session has the model, so the parent's Pi had no reason to start.
	if log := fakePiLog(t); strings.Contains(log, `"sessionPath":"`+beta+`"`) {
		t.Fatalf("the parent's Pi started:\n%s", log)
	}

	// A name alone needs no parent.
	code, stdout, stderr = runCLI("new", directory, "Show the deterministic browser response", "--name", "On its own", "--id")
	if code != 0 || stderr != "" {
		t.Fatalf("gripi new --name = %d, stderr %q", code, stderr)
	}
	if named := eventually(t, strings.TrimSpace(stdout), func(session gateway.LocalSession) bool { return session.Name == "On its own" }); named.Parent != "" {
		t.Fatalf("a session without --parent got one: %+v", named)
	}
}

func TestNewTakesTheModelThatTheRunningParentUses(t *testing.T) {
	fakePiGateway(t)
	outsideSessions(t)
	// A parent in its first turn has no saved file yet, so only its Pi knows the model.
	code, stdout, stderr := runCLI("new", t.TempDir(), "Start the follow-up scenario", "--json")
	parent := decodeSession(t, stdout)
	if code != 0 || parent.State != "working" {
		t.Fatalf("gripi new = %d, stderr %q, session %+v", code, stderr, parent)
	}
	form := url.Values{"session": {parent.Path}, "provider": {"e2e"}, "model": {"contract-model"}, "thinking": {"low"}}
	if response := browserRequest(http.MethodPost, "/sessions/model_settings", form, ""); response.Code != http.StatusOK {
		t.Fatalf("model settings = %d %s", response.Code, response.Body.String())
	}

	code, stdout, stderr = runCLI("new", t.TempDir(), "Show the deterministic browser response", "--parent", parent.Path, "--id")
	if code != 0 || stderr != "" {
		t.Fatalf("gripi new --parent = %d, stderr %q", code, stderr)
	}
	session := eventually(t, strings.TrimSpace(stdout), func(session gateway.LocalSession) bool { return session.State == "idle" })
	if session.Parent != parent.Path || !startsWithModel(t, session.Path, "contract-model", "low") {
		t.Fatalf("sub-session %+v did not start with its parent's model", session)
	}
}

func TestNewRefusesAParentThatCannotHaveSubSessions(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	outsideSessions(t)
	if code, _, stderr := runCLI("link", "0a1-a", "0a1-b"); code != 0 {
		t.Fatalf("gripi link = %d, stderr %q", code, stderr)
	}
	_, before, _ := runCLI("list", "--all", "--json")
	directory := t.TempDir()

	// A sub-session's own sub-sessions would nest deeper than the sidebar shows.
	if code, stdout, stderr := runCLI("new", directory, "Run the tests", "--parent", "0a1-a"); code != 1 || stdout != "" || !strings.Contains(stderr, "sub-session") {
		t.Fatalf("gripi new under a sub-session = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runCLI("new", directory, "--parent", "zzz"); code != 1 || stdout != "" || !strings.Contains(stderr, "gripi list") {
		t.Fatalf("gripi new under an unknown parent = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(os.Getenv("GRIPI_E2E_FAKE_PI_LOG")); !os.IsNotExist(err) {
		t.Fatalf("a refused gripi new started Pi (%v)", err)
	}
	if _, after, _ := runCLI("list", "--all", "--json"); after != before {
		t.Fatalf("a refused gripi new left a session:\n%s", after)
	}

	// The new session is deleted again when it cannot take the parent's model.
	gamma := filepath.Join(filepath.Dir(alpha), "gamma.jsonl")
	appendSessionRecords(t, gamma,
		map[string]any{"type": "session", "version": 3, "id": "0a1-gamma", "timestamp": "2026-01-03T00:00:00Z", "cwd": directory},
		map[string]any{"type": "message", "id": "user-1", "parentId": nil, "timestamp": "2026-01-03T00:00:01Z", "message": map[string]any{"role": "user", "content": []any{textPart("Plan the release")}}},
		sessionMessage("assistant-1", "user-1", "2", map[string]any{"role": "assistant", "stopReason": "stop", "content": []any{textPart("Planned")}}),
	)
	chooseModel(t, gamma, "retired-model", "high")
	_, before, _ = runCLI("list", "--all", "--json")
	if code, stdout, stderr := runCLI("new", directory, "Run the tests", "--parent", "0a1-g"); code != 1 || stdout != "" || !strings.Contains(stderr, "retired-model") {
		t.Fatalf("gripi new under a parent with a retired model = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, after, _ := runCLI("list", "--all", "--json"); after != before {
		t.Fatalf("a failed gripi new left a session:\n%s", after)
	}
}
