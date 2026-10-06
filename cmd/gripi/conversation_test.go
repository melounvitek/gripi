package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	gateway "github.com/melounvitek/gripi/internal/server"
)

// A turn in which Pi thinks, runs a command and then replies.
func appendToolTurn(t *testing.T, path string) {
	t.Helper()
	message := func(id, parent, time string, body map[string]any) map[string]any {
		return map[string]any{"type": "message", "id": id, "parentId": parent, "timestamp": "2026-01-03T00:00:0" + time + "Z", "message": body}
	}
	text := func(value string) map[string]any { return map[string]any{"type": "text", "text": value} }
	appendSessionRecords(t, path,
		message("user-2", "assistant-1", "1", map[string]any{"role": "user", "content": []any{text("List the files")}}),
		message("assistant-2", "user-2", "2", map[string]any{"role": "assistant", "stopReason": "toolUse", "content": []any{
			map[string]any{"type": "thinking", "thinking": "A listing will do."},
			text("I'll list them."),
			map[string]any{"type": "toolCall", "id": "call-1", "name": "bash", "arguments": map[string]any{"command": "ls\n  -la"}},
		}}),
		message("result-1", "assistant-2", "3", map[string]any{"role": "toolResult", "toolCallId": "call-1", "toolName": "bash", "isError": false, "content": []any{text("file-a\nfile-b")}}),
		message("assistant-3", "result-1", "4", map[string]any{"role": "assistant", "stopReason": "stop", "content": []any{text("Two files:\n\n- file-a\n- file-b")}}),
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
	expected := "[user]\nPrompt for 0a1-beta\n\n[assistant]\nAnswer from 0a1-beta\n\n[user]\nList the files\n\n[assistant]\nI'll list them.\n\n[tool] $ ls -la\n\n[assistant]\nTwo files:\n\n- file-a\n- file-b\n"
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
	if !reflect.DeepEqual(roles, []string{"user", "assistant", "user", "assistant", "tool", "assistant"}) || messages[4].Text != "$ ls\n  -la" {
		t.Fatalf("conversation = %+v", messages)
	}
}

func TestShowHasNothingToPrintBeforeTheFirstReply(t *testing.T) {
	fakePiGateway(t)
	_, stdout, _ := runCLI("new", t.TempDir(), "--json")
	started := decodeSession(t, stdout)

	code, stdout, stderr := runCLI("show", started.Path)
	if code != 0 || stdout != "" || !strings.Contains(stderr, "no reply yet") {
		t.Fatalf("gripi show = %d, stdout %q, stderr %q", code, stdout, stderr)
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
