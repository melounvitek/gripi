package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchFindsTheSessionsThatHoldAText(t *testing.T) {
	_, beta := fakePiGateway(t)
	appendSessionRecords(t, beta,
		sessionMessage("user-2", "assistant-1", "1", map[string]any{"role": "user", "content": []any{textPart("Review https://github.com/o/r/pull/77 for me")}}),
		sessionMessage("assistant-2", "user-2", "2", map[string]any{"role": "assistant", "stopReason": "toolUse", "content": []any{
			map[string]any{"type": "toolCall", "id": "call-1", "name": "bash", "arguments": map[string]any{"command": "gh pr view 77 --repo o/r"}},
		}}),
	)
	found := func(arguments ...string) []foundSession {
		t.Helper()
		code, stdout, stderr := runCLI(append([]string{"search", "--json"}, arguments...)...)
		var sessions []foundSession
		if err := json.Unmarshal([]byte(stdout), &sessions); err != nil || code != 0 || stderr != "" {
			t.Fatalf("gripi search %q = %d, stderr %q, %v:\n%s", arguments, code, stderr, err, stdout)
		}
		return sessions
	}

	sessions := found("PULL/77")
	if len(sessions) != 1 || sessions[0].ID != "0a1-beta" || sessions[0].MatchCount != 1 || sessions[0].Matches[0].Role != "user" || sessions[0].Matches[0].Text != "Review https://github.com/o/r/pull/77 for me" {
		t.Fatalf("gripi search PULL/77 = %+v", sessions)
	}
	if sessions := found("o/r", "--role", "tool"); len(sessions) != 1 || sessions[0].Matches[0].Text != `bash {"command":"gh pr view 77 --repo o/r"}` {
		t.Fatalf("gripi search o/r --role tool = %+v", sessions)
	}
	// Newest first, and filtered as gripi list filters.
	if sessions := found("answer from"); len(sessions) != 2 || sessions[0].ID != "0a1-beta" || sessions[1].ID != "0a1-alpha" {
		t.Fatalf("gripi search \"answer from\" = %+v", sessions)
	}
	if sessions := found("answer from", "--name", "alpha"); len(sessions) != 1 || sessions[0].ID != "0a1-alpha" {
		t.Fatalf("gripi search --name alpha = %+v", sessions)
	}
	if sessions := found("nowhere to be found"); len(sessions) != 0 {
		t.Fatalf("gripi search for missing text = %+v", sessions)
	}

	code, stdout, stderr := runCLI("search", "pull/77")
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "0a1-beta  ") || !strings.HasSuffix(stdout, "  project  Prompt for 0a1-beta  (1 match)\n  [user] Review https://github.com/o/r/pull/77 for me\n") {
		t.Fatalf("gripi search = %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	for _, usage := range [][]string{{"search"}, {"search", " "}, {"search", "one", "two"}, {"search", "text", "--role", "thinking"}, {"search", "text", "--state", "busy"}} {
		if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help search") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
		}
	}
}
