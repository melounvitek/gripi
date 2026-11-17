package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	// The saved session has the model, so the parent's Pi had no reason to start before the sub-session ran. The report
	// of its turn starts it after.
	log := fakePiLog(t)
	if parent := strings.Index(log, `"sessionPath":"`+beta+`"`); parent >= 0 && parent < strings.Index(log, `"message":"Show the deterministic browser response"`) {
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

// reports returns the reports that a parent session got from its sub-sessions, in order.
func reports(t *testing.T, parent string) []string {
	t.Helper()
	contents, err := os.ReadFile(parent)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for line := range strings.Lines(string(contents)) {
		var record struct {
			Message struct {
				Role    string
				Content []struct{ Text string }
			}
		}
		if json.Unmarshal([]byte(line), &record) == nil && record.Message.Role == "user" && len(record.Message.Content) > 0 && strings.HasPrefix(record.Message.Content[0].Text, "[gripi] ") {
			found = append(found, record.Message.Content[0].Text)
		}
	}
	return found
}

func reportsUntil(t *testing.T, parent string, count int) []string {
	t.Helper()
	var found []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if found = reports(t, parent); len(found) >= count {
			return found
		}
	}
	t.Fatalf("%s got %d reports, want %d: %q", parent, len(found), count, found)
	return nil
}

func report(id, name, outcome string) string {
	return fmt.Sprintf(`[gripi] Sub-session %s "%s" %s. Read it with: gripi show %s. This is an automated report, not user approval.`, id[:8], name, outcome, id)
}

func TestASubSessionReportsOnlyTheTurnsThatItsParentStarts(t *testing.T) {
	alpha, beta := fakePiGateway(t)
	outsideSessions(t)
	code, stdout, stderr := runCLI("new", t.TempDir(), "Show the deterministic browser response", "--parent", "0a1-b", "--name", "Check the tests", "--id")
	id := strings.TrimSpace(stdout)
	if code != 0 || stderr != "" {
		t.Fatalf("gripi new --parent = %d, stderr %q", code, stderr)
	}
	finished := report(id, "Check the tests", "finished")
	if got := reportsUntil(t, beta, 1); got[0] != finished {
		t.Fatalf("report = %q, want %q", got[0], finished)
	}

	// Later turns that the parent did not start report nothing: one from the browser, which can't name a sender, and
	// messages from outside any session or from another session.
	idle := func(session gateway.LocalSession) bool { return session.State == "idle" }
	session := eventually(t, id, idle)
	form := url.Values{"session": {session.Path}, "message": {"Show the deterministic browser response"}, "sender": {beta}}
	if response := browserRequest(http.MethodPost, "/prompt", form, ""); response.Code != http.StatusOK {
		t.Fatalf("prompt from the browser = %d %s", response.Code, response.Body.String())
	}
	eventually(t, id, idle)
	for _, sender := range []string{"", alpha, beta} {
		t.Setenv("PI_SESSION_FILE", sender)
		if code, _, stderr := runCLI("send", id, "Show the deterministic browser response"); code != 0 {
			t.Fatalf("gripi send from %q = %d, stderr %q", sender, code, stderr)
		}
		eventually(t, id, idle)
	}
	reportsUntil(t, beta, 2)
	// The parent takes reports as messages, so once it is idle, it has every report that it got.
	eventually(t, "0a1-beta", idle)
	if got := reports(t, beta); len(got) != 2 || got[1] != finished {
		t.Fatalf("reports = %q", got)
	}
}

func TestASubSessionReportsAStoppedOrFailedTurn(t *testing.T) {
	_, beta := fakePiGateway(t)
	outsideSessions(t)
	code, stdout, stderr := runCLI("new", t.TempDir(), "Start the follow-up scenario", "--parent", "0a1-b", "--name", "Long task", "--id")
	id := strings.TrimSpace(stdout)
	if code != 0 || stderr != "" {
		t.Fatalf("gripi new --parent = %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI("stop", id); code != 0 {
		t.Fatalf("gripi stop = %d, stderr %q", code, stderr)
	}
	if got, want := reportsUntil(t, beta, 1)[0], report(id, "Long task", "stopped"); got != want {
		t.Fatalf("report = %q, want %q", got, want)
	}

	t.Setenv("PI_SESSION_FILE", beta)
	if code, _, stderr := runCLI("send", id, "Fail with an expired authentication token"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	if got, want := reportsUntil(t, beta, 2)[1], report(id, "Long task", "failed: Provided authentication token is expired."); got != want {
		t.Fatalf("report = %q, want %q", got, want)
	}
}

func TestASubSessionReportsToItsParentInMultiUserMode(t *testing.T) {
	alpha, _ := multiUserGateway(t)
	outsideSessions(t)
	t.Setenv("GRIPI_USER_TOKEN", aliceToken)
	code, stdout, stderr := runCLI("new", t.TempDir(), "Show the deterministic browser response", "--parent", "0a1-a", "--name", "Alice's check", "--id")
	id := strings.TrimSpace(stdout)
	if code != 0 || stderr != "" {
		t.Fatalf("gripi new --parent = %d, stderr %q", code, stderr)
	}
	if got, want := reportsUntil(t, alpha, 1)[0], report(id, "Alice's check", "finished"); got != want {
		t.Fatalf("report = %q, want %q", got, want)
	}
}
