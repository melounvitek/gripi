package main

import (
	"context"
	"encoding/json"
	"net/http"
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

// fakePiGateway runs a gateway backed by the fake Pi on a socket the commands
// will find. It has two idle sessions, 0a1-alpha and the more recent 0a1-beta.
func fakePiGateway(t *testing.T) (alpha, beta string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	sessionsRoot := filepath.Join(home, ".pi", "agent", "sessions")
	project := filepath.Join(root, "project")
	for _, directory := range []string{sessionsRoot, project} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alpha, beta = filepath.Join(sessionsRoot, "alpha.jsonl"), filepath.Join(sessionsRoot, "beta.jsonl")
	for path, session := range map[string][2]string{alpha: {"0a1-alpha", "2026-01-01T00:00:0"}, beta: {"0a1-beta", "2026-01-02T00:00:0"}} {
		appendSessionRecords(t, path,
			map[string]any{"type": "session", "version": 3, "id": session[0], "timestamp": session[1] + "0Z", "cwd": project},
			map[string]any{"type": "message", "id": "user-1", "parentId": nil, "timestamp": session[1] + "1Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Prompt for " + session[0]}}}},
			map[string]any{"type": "message", "id": "assistant-1", "parentId": "user-1", "timestamp": session[1] + "2Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Answer from " + session[0]}}, "stopReason": "stop"}},
		)
	}
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
	socket := socketPath(t)
	server, err := startLocalServer(handler.(interface{ Local() http.Handler }).Local(), socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		server.Close()
		handler.(interface{ Close(context.Context) error }).Close(context.Background())
	})
	t.Setenv("GRIPI_SOCKET_PATH", socket)
	return alpha, beta
}

func appendSessionRecords(t *testing.T, path string, records ...map[string]any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, record := range records {
		if err := json.NewEncoder(file).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
}

func decodeSession(t *testing.T, output string) gateway.LocalSession {
	t.Helper()
	var session gateway.LocalSession
	if err := json.Unmarshal([]byte(output), &session); err != nil {
		t.Fatalf("not one JSON session (%v): %q", err, output)
	}
	return session
}

func eventually(t *testing.T, reference string, done func(gateway.LocalSession) bool) gateway.LocalSession {
	t.Helper()
	var listed []gateway.LocalSession
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		_, stdout, _ := runCLI("list", "--json")
		if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
			t.Fatal(err)
		}
		for _, session := range listed {
			if (session.ID == reference || session.Path == reference) && done(session) {
				return session
			}
		}
	}
	t.Fatalf("%s never reached the expected state: %+v", reference, listed)
	return gateway.LocalSession{}
}

func TestSendDeliversPromptsAndReturnsOnceTheSessionIsWorking(t *testing.T) {
	alpha, _ := fakePiGateway(t)

	code, stdout, stderr := runCLI("send", "0a1-a", "Start the follow-up scenario", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	// No polling here: a following 'gripi wait' must already see the turn.
	if session := decodeSession(t, stdout); session.ID != "0a1-alpha" || session.State != "working" {
		t.Fatalf("session after send = %+v", session)
	}

	code, stdout, stderr = runCLIWithInput("Continue with the queued follow-up\n", "send", alpha)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "0a1-alpha") || !strings.Contains(stdout, "working") {
		t.Fatalf("queued gripi send = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	eventually(t, "0a1-alpha", func(session gateway.LocalSession) bool { return session.State == "idle" })
	contents, err := os.ReadFile(alpha)
	if err != nil {
		t.Fatal(err)
	}
	for _, sent := range []string{`"text":"Start the follow-up scenario"`, `"text":"Continue with the queued follow-up"`} {
		if !strings.Contains(string(contents), sent) {
			t.Fatalf("Pi did not receive %s:\n%s", sent, contents)
		}
	}
}

func TestSendNeverRunsItsTextAsAShellCommand(t *testing.T) {
	_, beta := fakePiGateway(t)
	if code, _, stderr := runCLI("send", "0a1-b", "!touch pwned"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	eventually(t, "0a1-beta", func(session gateway.LocalSession) bool { return session.State == "idle" })
	contents, err := os.ReadFile(beta)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"role":"user","content":[{"type":"text","text":"!touch pwned"}]`) || strings.Contains(string(contents), "bashExecution") {
		t.Fatalf("message was not delivered as a plain prompt:\n%s", contents)
	}
}

func TestSendSteersTheRunningTurn(t *testing.T) {
	fakePiGateway(t)
	if code, _, stderr := runCLI("send", "0a1-a", "Start the steer scenario"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLIWithInput("Use the steered direction", "send", "--steer", "0a1-a", "-"); code != 0 {
		t.Fatalf("gripi send --steer = %d, stderr %q", code, stderr)
	}
	eventually(t, "0a1-alpha", func(session gateway.LocalSession) bool {
		return session.State == "idle" && session.LastReply == "Steered direction accepted."
	})
}

func TestSendTakesAMessageThatLooksLikeAFlagAfterTheSeparator(t *testing.T) {
	_, beta := fakePiGateway(t)
	if code, _, stderr := runCLI("send", "0a1-b", "--json", "--", "--steer is only text here"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	eventually(t, "0a1-beta", func(session gateway.LocalSession) bool { return session.State == "idle" })
	if contents, err := os.ReadFile(beta); err != nil || !strings.Contains(string(contents), `"text":"--steer is only text here"`) {
		t.Fatalf("Pi did not receive the message (%v):\n%s", err, contents)
	}
}

func TestSendExplainsWhatItCannotDeliver(t *testing.T) {
	_, beta := fakePiGateway(t)
	// The composer's commands are refused: steered they would run, and otherwise Pi would get them as text.
	for _, usage := range [][]string{{"send"}, {"send", "0a1-a", "  "}, {"send", "0a1-a", "one", "two"}, {"send", "", "message"}, {"send", "0a1-a", "--steer", "/new"}, {"send", "0a1-a", "/compact keep the plan"}} {
		if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help send") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
		}
	}
	if code, _, stderr := runCLI("send", "0a1", "message"); code != 1 || !strings.Contains(stderr, "0a1-alpha") || !strings.Contains(stderr, "0a1-beta") {
		t.Fatalf("ambiguous session = %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI("send", "zzz", "message"); code != 1 || !strings.Contains(stderr, "gripi list") {
		t.Fatalf("unknown session = %d, stderr %q", code, stderr)
	}

	// Pi CLI appends to a session the gateway has already seen.
	appendSessionRecords(t, beta, map[string]any{"type": "message", "id": "external", "parentId": "assistant-1", "timestamp": "2026-01-02T00:00:09Z", "message": map[string]any{"role": "assistant", "stopReason": "stop", "content": []any{map[string]any{"type": "text", "text": "From Pi CLI"}}}})
	code, stdout, stderr := runCLI("send", "0a1-beta", "message")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "Pi CLI") {
		t.Fatalf("send to a session used in Pi CLI = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestWaitReturnsTheReplyToAMessageSentJustBefore(t *testing.T) {
	fakePiGateway(t)
	if code, _, stderr := runCLI("send", "0a1-a", "Show the deterministic browser response"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	code, stdout, stderr := runCLI("wait", "0a1-a", "--json")
	session := decodeSession(t, stdout)
	if code != 0 || stderr != "" || session.State != "idle" {
		t.Fatalf("gripi wait = %d, stderr %q, session %+v", code, stderr, session)
	}
	if session.LastReply != "Deterministic browser response complete." {
		t.Fatalf("wait returned before the new reply was readable: %q", session.LastReply)
	}

	// Waiting on a settled session returns at once, as a table by default.
	if code, stdout, _ := runCLI("wait", "0a1-b"); code != 0 || !strings.Contains(stdout, "0a1-beta") || !strings.Contains(stdout, "idle") {
		t.Fatalf("gripi wait on an idle session = %d, stdout %q", code, stdout)
	}
}

func TestWaitGivesUpAfterItsTimeoutWhileTheSessionKeepsWorking(t *testing.T) {
	fakePiGateway(t)
	if code, _, stderr := runCLI("send", "0a1-a", "Start the follow-up scenario"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	started := time.Now()
	code, stdout, stderr := runCLI("wait", "0a1-a", "--timeout", "0.6", "--json")
	if elapsed := time.Since(started); code != 3 || elapsed < 600*time.Millisecond {
		t.Fatalf("gripi wait --timeout = %d after %s, stderr %q", code, elapsed, stderr)
	}
	if session := decodeSession(t, stdout); session.State != "working" || !strings.Contains(stderr, "still working") {
		t.Fatalf("timed-out wait printed %+v, stderr %q", session, stderr)
	}
}

func TestWaitEndsWhenPiNeedsAnAnswerInTheBrowser(t *testing.T) {
	fakePiGateway(t)
	if code, _, stderr := runCLI("send", "0a1-b", "Ask me for release approval"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	code, stdout, stderr := runCLI("wait", "0a1-b", "--timeout", "5", "--json")
	if session := decodeSession(t, stdout); code != 0 || session.State != "waiting" {
		t.Fatalf("gripi wait = %d, stderr %q, session %+v", code, stderr, session)
	}
}

func TestWaitExplainsWhatItCannotWaitFor(t *testing.T) {
	fakePiGateway(t)
	for _, usage := range [][]string{{"wait"}, {"wait", "0a1-a", "0a1-b"}, {"wait", "0a1-a", "--timeout", "soon"}, {"wait", "0a1-a", "--timeout", "-1"}} {
		if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help wait") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
		}
	}
	if code, stdout, stderr := runCLI("wait", "zzz"); code != 1 || stdout != "" || !strings.Contains(stderr, "gripi list") {
		t.Fatalf("unknown session = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
