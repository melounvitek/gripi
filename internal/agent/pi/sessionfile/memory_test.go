package sessionfile

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/melounvitek/gripi/internal/agent"
)

// A session that Pi keeps only in memory renders like the same entries saved to a file.
func TestEntriesRenderLikeTheSameSessionFile(t *testing.T) {
	root, project, path := sessionFixture(t)
	lines := []string{
		`{"type":"model_change","id":"m1","parentId":null,"timestamp":"2026-01-01T00:00:00Z","provider":"test","modelId":"model"}`,
		`{"type":"message","id":"u1","parentId":"m1","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"Look"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}]}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Checking"},{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"ls"}}],"stopReason":"toolUse"}}`,
		`{"type":"message","id":"r1","parentId":"a1","timestamp":"2026-01-01T00:00:03Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"bash","content":[{"type":"text","text":"file"}],"isError":false}}`,
		`{"type":"message","id":"a2","parentId":"r1","timestamp":"2026-01-01T00:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"One file."}],"stopReason":"stop"}}`,
		userLine("u2", "a2", "2026-01-01T00:00:05Z", "And now?"),
		`{"type":"message","id":"a3","parentId":"u2","timestamp":"2026-01-01T00:00:06Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c2","name":"bash","arguments":{"command":"sleep 9"}}],"stopReason":"toolUse"}}`,
	}
	writeSessionLines(t, path, append([]string{sessionLine(project)}, lines...))
	store := Store{Root: root, Home: root, Cache: NewCache()}
	var entries []any
	for _, line := range lines {
		var raw any
		if err := json.Unmarshal(withoutImageData([]byte(line)), &raw); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, raw)
	}

	for _, cursor := range []*int{nil, intPointer(3)} {
		saved, err := store.Window(path, "", false, cursor, nil)
		if err != nil {
			t.Fatal(err)
		}
		memory, err := store.WindowFromEntries(entries, cursor, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(saved.Messages) == 0 || !reflect.DeepEqual(withoutKeys(memory.Messages), withoutKeys(saved.Messages)) {
			t.Fatalf("cursor %v: memory %#v, saved %#v", cursor, memory.Messages, saved.Messages)
		}
		if memory.StartIndex != saved.StartIndex || memory.EndIndex != saved.EndIndex || memory.TotalMessageCount != saved.TotalMessageCount {
			t.Fatalf("cursor %v: memory window %d-%d of %d, saved %d-%d of %d", cursor, memory.StartIndex, memory.EndIndex, memory.TotalMessageCount, saved.StartIndex, saved.EndIndex, saved.TotalMessageCount)
		}
	}
}

func intPointer(value int) *int { return &value }

// Keys count the file's lines, which start with the session header.
func withoutKeys(messages []*agent.Message) []agent.Message {
	var result []agent.Message
	for _, message := range messages {
		copied := *message
		copied.Key = [2]int{}
		result = append(result, copied)
	}
	return result
}
