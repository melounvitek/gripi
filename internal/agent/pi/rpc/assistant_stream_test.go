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
