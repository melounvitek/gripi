package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	gateway "github.com/melounvitek/gripi/internal/server"
)

func sessionMessage(id, parent, second string, body map[string]any) map[string]any {
	return map[string]any{"type": "message", "id": id, "parentId": parent, "timestamp": "2026-01-03T00:00:0" + second + "Z", "message": body}
}

func textPart(value string) map[string]any { return map[string]any{"type": "text", "text": value} }

// A turn in which Pi thinks, uses three tools and then replies.
func appendToolTurn(t *testing.T, path string) {
	t.Helper()
	toolCall := func(id, name string, arguments map[string]any) map[string]any {
		return map[string]any{"type": "toolCall", "id": id, "name": name, "arguments": arguments}
	}
	toolResult := func(id, parent, second, call, name, output string) map[string]any {
		return sessionMessage(id, parent, second, map[string]any{"role": "toolResult", "toolCallId": call, "toolName": name, "isError": false, "content": []any{textPart(output)}})
	}
	appendSessionRecords(t, path,
		sessionMessage("user-2", "assistant-1", "1", map[string]any{"role": "user", "content": []any{textPart("List the files")}}),
		sessionMessage("assistant-2", "user-2", "2", map[string]any{"role": "assistant", "stopReason": "toolUse", "content": []any{
			map[string]any{"type": "thinking", "thinking": "A listing will do."},
			textPart("I'll list them."),
			toolCall("call-1", "bash", map[string]any{"command": "ls\n  -la"}),
			toolCall("call-2", "grep", map[string]any{"pattern": "file"}),
			toolCall("call-3", "subagent", map[string]any{"task": "Count them"}),
		}}),
		toolResult("result-1", "assistant-2", "3", "call-1", "bash", "file-a\nfile-b"),
		toolResult("result-2", "result-1", "4", "call-2", "grep", "file-a"),
		toolResult("result-3", "result-2", "5", "call-3", "subagent", "Two."),
		sessionMessage("assistant-3", "result-3", "6", map[string]any{"role": "assistant", "stopReason": "stop", "content": []any{textPart("Two files:\n\n- file-a\n- file-b")}}),
	)
}

func TestShowPrintsTheLatestReplyInFull(t *testing.T) {
	_, beta := fakePiGateway(t)
	appendToolTurn(t, beta)

	code, stdout, stderr := runCLI("show", "0a1-b")
	if code != 0 || stderr != "" || stdout != "Two files:\n\n- file-a\n- file-b\n" {
		t.Fatalf("gripi show = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI("show", beta, "--json")
	var messages []gateway.LocalMessage
	if err := json.Unmarshal([]byte(stdout), &messages); err != nil || code != 0 || stderr != "" {
		t.Fatalf("gripi show --json = %d, stderr %q, %v:\n%s", code, stderr, err, stdout)
	}
	if len(messages) != 1 || messages[0].Role != "assistant" || messages[0].Text != "Two files:\n\n- file-a\n- file-b" || messages[0].Timestamp.IsZero() {
		t.Fatalf("latest reply = %+v", messages)
	}
}

func TestShowPrintsTheConversationWithToolCallsOnOneLine(t *testing.T) {
	_, beta := fakePiGateway(t)
	appendToolTurn(t, beta)

	code, stdout, stderr := runCLI("show", "0a1-b", "--all")
	// Each tool call is listed once, whether or not its result is stored apart from it.
	expected := "[user]\nPrompt for 0a1-beta\n\n[assistant]\nAnswer from 0a1-beta\n\n[user]\nList the files\n\n[assistant]\nI'll list them.\n\n[tool] $ ls -la\n\n[tool] grep\n\n[tool] subagent\n\n[assistant]\nTwo files:\n\n- file-a\n- file-b\n"
	if code != 0 || stderr != "" || stdout != expected {
		t.Fatalf("gripi show --all = %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}

	code, stdout, stderr = runCLI("show", "0a1-b", "--all", "--json")
	var messages []gateway.LocalMessage
	if err := json.Unmarshal([]byte(stdout), &messages); err != nil || code != 0 || stderr != "" {
		t.Fatalf("gripi show --all --json = %d, stderr %q, %v:\n%s", code, stderr, err, stdout)
	}
	var roles []string
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	// Thinking is left out, and a tool call carries what was run, not its output.
	if !reflect.DeepEqual(roles, []string{"user", "assistant", "user", "assistant", "tool", "tool", "tool", "assistant"}) || messages[4].Text != "$ ls\n  -la" {
		t.Fatalf("conversation = %+v", messages)
	}
}

func TestShowDoesNotPassOffAnOlderReplyAsTheAnswerToTheLatestMessage(t *testing.T) {
	_, beta := fakePiGateway(t)
	appendSessionRecords(t, beta,
		sessionMessage("user-2", "assistant-1", "1", map[string]any{"role": "user", "content": []any{textPart("Try again")}}),
		sessionMessage("assistant-2", "user-2", "2", map[string]any{"role": "assistant", "stopReason": "error", "errorMessage": "429 rate limited", "content": []any{}}),
	)
	code, stdout, stderr := runCLI("show", "0a1-b")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "no reply") || !strings.Contains(stderr, "--all") {
		t.Fatalf("gripi show after a failed turn = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runCLI("show", "0a1-b", "--all"); code != 0 || stderr != "" || !strings.HasSuffix(stdout, "[user]\nTry again\n\n[error]\n429 rate limited\n") {
		t.Fatalf("gripi show --all after a failed turn = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestShowHasNothingToPrintBeforeTheFirstReply(t *testing.T) {
	fakePiGateway(t)
	_, stdout, _ := runCLI("new", t.TempDir(), "--json")
	started := decodeSession(t, stdout)

	for _, arguments := range [][]string{{"show", started.Path}, {"show", started.Path, "--all"}} {
		if code, stdout, stderr := runCLI(arguments...); code != 0 || stdout != "" || !strings.Contains(stderr, "gripi show: ") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", arguments, code, stdout, stderr)
		}
	}
	for _, arguments := range [][]string{{"show", started.Path, "--json"}, {"show", started.Path, "--all", "--json"}} {
		// Nothing but JSON is printed, so "2>&1 | jq" keeps working.
		if code, stdout, stderr := runCLI(arguments...); code != 0 || strings.TrimSpace(stdout) != "[]" || stderr != "" {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", arguments, code, stdout, stderr)
		}
	}
}

func TestShowExplainsWhatItCannotShow(t *testing.T) {
	fakePiGateway(t)
	for _, usage := range [][]string{{"show"}, {"show", ""}, {"show", "0a1-a", "0a1-b"}, {"show", "0a1-a", "--bogus"}} {
		if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help show") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
		}
	}
	if code, stdout, stderr := runCLI("show", "zzz"); code != 1 || stdout != "" || !strings.Contains(stderr, "gripi list") {
		t.Fatalf("gripi show on an unknown session = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// piOnPath puts a pi command on PATH and records how 'gripi open' starts it instead of letting it replace the test.
func piOnPath(t *testing.T) (pi string, started *[]string, directory *string) {
	t.Helper()
	bin := t.TempDir()
	pi = filepath.Join(bin, "pi")
	if err := os.WriteFile(pi, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Restores the working directory that the command changes.
	t.Chdir(t.TempDir())
	started, directory = new([]string), new(string)
	replaceProcess = func(path string, arguments, _ []string) error {
		*started = append([]string{path}, arguments...)
		current, err := os.Getwd()
		if err != nil {
			return err
		}
		*directory, err = filepath.EvalSymlinks(current)
		return err
	}
	t.Cleanup(func() { replaceProcess = syscall.Exec })
	return pi, started, directory
}

func TestOpenContinuesASessionInPiCLIFromItsProjectDirectory(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	project, err := filepath.EvalSymlinks(listedSession(t, "0a1-alpha").CWD)
	if err != nil {
		t.Fatal(err)
	}
	pi, started, directory := piOnPath(t)

	code, stdout, stderr := runCLI("open", "0a1-a")
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("gripi open = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !reflect.DeepEqual(*started, []string{pi, "pi", "--session", alpha}) || *directory != project {
		t.Fatalf("started %q in %q, expected pi --session %s in %s", *started, *directory, alpha, project)
	}
}

func TestOpenLeavesASessionAloneWhileOnlyTheGatewaysPiHasIt(t *testing.T) {
	fakePiGateway(t)
	_, started, _ := piOnPath(t)
	if code, _, stderr := runCLI("send", "0a1-b", "Start the follow-up scenario"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	code, stdout, stderr := runCLI("open", "0a1-b")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "working") || *started != nil {
		t.Fatalf("gripi open on a working session = %d, stdout %q, stderr %q, started %q", code, stdout, stderr, *started)
	}

	// A new session has no file yet: the gateway's Pi writes it with the first reply.
	_, stdout, _ = runCLI("new", t.TempDir(), "--json")
	code, stdout, stderr = runCLI("open", decodeSession(t, stdout).Path)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "no reply") || *started != nil {
		t.Fatalf("gripi open on a new session = %d, stdout %q, stderr %q, started %q", code, stdout, stderr, *started)
	}
}

func TestOpenNeedsATerminalAndPiCLI(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	_, started, _ := piOnPath(t)
	// Redirected, Pi CLI would run once on whatever stdin holds rather than open its interface.
	redirected, err := os.Open(alpha)
	if err != nil {
		t.Fatal(err)
	}
	defer redirected.Close()
	for name, streams := range map[string]struct {
		stdin  io.Reader
		stdout io.Writer
	}{"stdin": {redirected, &bytes.Buffer{}}, "stdout": {&bytes.Buffer{}, redirected}} {
		var stderr bytes.Buffer
		if code := run([]string{"open", "0a1-a"}, streams.stdin, streams.stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "terminal") || *started != nil {
			t.Fatalf("gripi open with %s redirected = %d, stderr %q, started %q", name, code, stderr.String(), *started)
		}
	}

	for _, usage := range [][]string{{"open"}, {"open", ""}, {"open", "0a1-a", "0a1-b"}, {"open", "0a1-a", "--json"}} {
		if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help open") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
		}
	}
	if code, stdout, stderr := runCLI("open", "zzz"); code != 1 || stdout != "" || !strings.Contains(stderr, "gripi list") {
		t.Fatalf("gripi open on an unknown session = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	t.Setenv("PATH", t.TempDir())
	if code, stdout, stderr := runCLI("open", "0a1-a"); code != 1 || stdout != "" || !strings.Contains(stderr, "PATH") || *started != nil {
		t.Fatalf("gripi open without Pi CLI = %d, stdout %q, stderr %q, started %q", code, stdout, stderr, *started)
	}
}
