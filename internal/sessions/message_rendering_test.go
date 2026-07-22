package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistedAssistantThinkingAndFinalAnswerRemainSeparateLikeLiveMessages(t *testing.T) {
	raw := map[string]any{"type": "message", "timestamp": "2026-01-01T00:00:00Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "Reasoning"}, map[string]any{"type": "text", "text": "Answer"}}}}
	messages := messagesFromRaw(raw, "/home/tester")
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if !messages[0].Thinking || messages[0].Text != "Reasoning" || messages[0].FinalAssistantResponse {
		t.Fatalf("thinking message = %#v", messages[0])
	}
	if messages[1].Thinking || messages[1].Text != "Answer" || !messages[1].FinalAssistantResponse {
		t.Fatalf("answer message = %#v", messages[1])
	}
}

func TestPersistedCommentarySignatureMatchesRubyAndLiveParser(t *testing.T) {
	tests := []struct {
		name      string
		signature string
		final     bool
	}{
		{"valid commentary", `{"v":1,"id":"progress","phase":"commentary"}`, false},
		{"valid final answer", `{"v":1,"id":"answer","phase":"final_answer"}`, true},
		{"missing id", `{"v":1,"phase":"commentary"}`, true},
		{"non-string id", `{"v":1,"id":2,"phase":"commentary"}`, true},
		{"future version", `{"v":2,"id":"progress","phase":"commentary"}`, true},
		{"unknown phase", `{"v":1,"id":"progress","phase":"analysis"}`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := map[string]any{"type": "message", "timestamp": "2026-01-01T00:00:00Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Visible", "textSignature": test.signature}}}}
			messages := messagesFromRaw(raw, "")
			if test.final {
				if len(messages) != 1 || !messages[0].FinalAssistantResponse || messages[0].Text != "Visible" {
					t.Fatalf("messages = %#v", messages)
				}
			} else if len(messages) != 1 || messages[0].FinalAssistantResponse {
				t.Fatalf("commentary messages = %#v", messages)
			}
		})
	}
}

func TestNativeBashValidationMatchesTheOversizedScanner(t *testing.T) {
	tests := []struct {
		name  string
		patch string
		valid bool
	}{
		{"integer exit", `"exitCode":7,"cancelled":false,"truncated":true,"fullOutputPath":"/tmp/full.log","timestamp":1,"excludeFromContext":false`, true},
		{"null exit and omitted optional values", `"exitCode":null,"cancelled":false,"truncated":false,"timestamp":1`, true},
		{"missing timestamp", `"exitCode":0,"cancelled":false,"truncated":false`, false},
		{"fractional exit", `"exitCode":1.5,"cancelled":false,"truncated":false,"timestamp":1`, false},
		{"string exit", `"exitCode":"1","cancelled":false,"truncated":false,"timestamp":1`, false},
		{"invalid cancelled", `"exitCode":0,"cancelled":null,"truncated":false,"timestamp":1`, false},
		{"invalid full path", `"exitCode":0,"cancelled":false,"truncated":false,"fullOutputPath":7,"timestamp":1`, false},
		{"invalid excluded state", `"exitCode":0,"cancelled":false,"truncated":false,"timestamp":1,"excludeFromContext":null`, false},
	}
	for _, outputBytes := range []int{0, MaxIndexedEntryBytes + 1024} {
		for _, test := range tests {
			t.Run(fmt.Sprintf("%s/%d", test.name, outputBytes), func(t *testing.T) {
				root, project, path := sessionFixture(t)
				line := `{"type":"message","id":"bash","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"bashExecution","command":"run","output":"` + strings.Repeat("x", outputBytes) + `",` + test.patch + `}}`
				writeSessionLines(t, path, []string{sessionLine(project), line})
				window, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil)
				if test.valid {
					if err != nil || len(window.Messages) != 1 || window.Messages[0].Role != "bashExecution" {
						t.Fatalf("valid bash: window=%#v err=%v", window, err)
					}
				} else if err == nil && len(window.Messages) != 0 {
					t.Fatalf("invalid bash rendered: %#v", window.Messages)
				}
			})
		}
	}

}

func TestStatusEstimatesContextAfterCompactionSupersedesUsage(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "status.jsonl")
	lines := []string{
		`{"type":"session","version":3,"id":"session","timestamp":"2026-01-01T00:00:00Z","cwd":"` + project + `"}`,
		`{"type":"message","id":"answer","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"old"}],"provider":"test","model":"model","usage":{"totalTokens":100},"stopReason":"stop"}}`,
		`{"type":"compaction","id":"compact","parentId":"answer","timestamp":"2026-01-01T00:00:02Z","summary":"12345678","firstKeptEntryId":"missing","tokensBefore":100}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := (Store{Root: root, Home: root, Cache: NewCache()}).Status(path)
	if err != nil {
		t.Fatal(err)
	}
	if !status.ContextEstimated || !status.HasContextTokens || status.ContextTokens != 2 || status.HasContextLimit {
		t.Fatalf("status = %#v", status)
	}
}
