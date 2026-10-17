package rpc

import (
	"reflect"
	"testing"
)

func TestAssistantStreamIgnoresMalformedDeltasAndIndexes(t *testing.T) {
	stream := newAssistantStreamState(map[string]any{"role": "assistant"}, 1<<20)
	stream.apply(map[string]any{"type": "text_start", "contentIndex": 0})
	stream.apply(map[string]any{"type": "text_delta", "contentIndex": 0, "delta": "safe"})
	stream.apply(map[string]any{"type": "text_end", "contentIndex": 0, "content": 42})
	stream.apply(map[string]any{"type": "text_delta", "contentIndex": -1, "delta": "negative"})
	stream.apply(map[string]any{"type": "text_delta", "contentIndex": 1.5, "delta": "fractional"})
	stream.apply(map[string]any{"type": "text_delta", "contentIndex": maxAssistantStreamParts, "delta": "oversized"})

	want := []any{map[string]any{"type": "text", "text": "safe"}}
	if content := stream.partialMessage()["content"]; !reflect.DeepEqual(content, want) {
		t.Fatalf("content = %#v, want %#v", content, want)
	}
}

func TestAssistantStreamShowsToolCallArgumentsAsTheyArrive(t *testing.T) {
	stream := newAssistantStreamState(map[string]any{"role": "assistant"}, 1<<20)
	stream.apply(map[string]any{"type": "toolcall_start", "contentIndex": 0})
	if content := stream.partialMessage()["content"]; !reflect.DeepEqual(content, []any{}) {
		t.Fatalf("tool call without identity = %#v", content)
	}

	stream.apply(map[string]any{"type": "toolcall_start", "contentIndex": 0, "id": "edit-1", "toolName": "edit"})
	for _, step := range []struct {
		delta     string
		arguments map[string]any
	}{
		{"", map[string]any{}},
		{`{"path": "a.go", "edits": [{"oldText": "old \u00`, map[string]any{"path": "a.go", "edits": []any{map[string]any{"oldText": "old "}}}},
		// A key without its value can't be shown yet, so the arguments so far stay.
		{`e9\n", "new`, map[string]any{"path": "a.go", "edits": []any{map[string]any{"oldText": "old "}}}},
		{`Text": "new\\"`, map[string]any{"path": "a.go", "edits": []any{map[string]any{"oldText": "old é\n", "newText": `new\`}}}},
	} {
		stream.apply(map[string]any{"type": "toolcall_delta", "contentIndex": 0, "delta": step.delta})
		want := []any{map[string]any{"type": "toolCall", "id": "edit-1", "name": "edit", "arguments": step.arguments}}
		if content := stream.partialMessage()["content"]; !reflect.DeepEqual(content, want) {
			t.Fatalf("after %q content = %#v, want %#v", step.delta, content, want)
		}
	}

	toolCall := map[string]any{"type": "toolCall", "id": "edit-1", "name": "edit", "arguments": map[string]any{"path": "final.go"}}
	stream.apply(map[string]any{"type": "toolcall_end", "contentIndex": 0, "toolCall": toolCall})
	stream.apply(map[string]any{"type": "toolcall_delta", "contentIndex": 0, "delta": `"}]}`})
	if content := stream.partialMessage()["content"]; !reflect.DeepEqual(content, []any{toolCall}) {
		t.Fatalf("finished content = %#v", content)
	}
}

func TestAssistantStreamStopsProjectingAfterItsSizeLimit(t *testing.T) {
	message := map[string]any{"role": "assistant"}
	metadataBytes := jsonSize(map[string]any{"role": "assistant", "content": []any{}})
	stream := newAssistantStreamState(message, metadataBytes+4)
	stream.apply(map[string]any{"type": "thinking_start", "contentIndex": 0})
	stream.apply(map[string]any{"type": "thinking_delta", "contentIndex": 0, "delta": "1234"})
	stream.apply(map[string]any{"type": "thinking_delta", "contentIndex": 0, "delta": "5"})

	if message := stream.partialMessage(); message != nil {
		t.Fatalf("oversized partial message = %#v", message)
	}
}
