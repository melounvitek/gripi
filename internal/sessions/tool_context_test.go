package sessions

import (
	"testing"
	"time"
)

func TestSubagentToolCallContextUsesOnlySelectedBranch(t *testing.T) {
	root, project, path := sessionFixture(t)
	writeSessionLines(t, path, []string{
		sessionLine(project),
		userLine("root", "", "2026-01-01T00:00:01Z", "Root"),
		`{"type":"message","id":"old-call","parentId":"root","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"subagent-1","name":"subagent","arguments":{"task":"Old branch"}}]}}`,
		`{"type":"message","id":"old-result","parentId":"old-call","timestamp":"2026-01-01T00:00:03Z","message":{"role":"toolResult","toolCallId":"subagent-1","toolName":"subagent","content":[{"type":"text","text":"Old result"}],"isError":false}}`,
		`{"type":"message","id":"current-call","parentId":"root","timestamp":"2026-01-01T00:00:04Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"subagent-1","name":"subagent","arguments":{"task":"Current branch"}}]}}`,
	})
	store := Store{Root: root, Home: root, Cache: NewCache()}

	current := store.SubagentToolCallContext(path, "current-call", []string{"subagent-1"})["subagent-1"]
	if current.Prompt != "Current branch" || current.ResultPersisted || !current.Timestamp.Equal(time.Date(2026, 1, 1, 0, 0, 4, 0, time.UTC)) {
		t.Fatalf("current context = %#v", current)
	}

	old := store.SubagentToolCallContext(path, "old-result", []string{"subagent-1"})["subagent-1"]
	if old.Prompt != "Old branch" || !old.ResultPersisted || !old.Timestamp.Equal(time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)) {
		t.Fatalf("old context = %#v", old)
	}
}
