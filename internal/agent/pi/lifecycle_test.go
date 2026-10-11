package pi

import (
	"testing"

	"github.com/melounvitek/gripi/internal/agent"
)

// completedAssistantReply is the final text of the reply that a Pi event ends, which the gateway may notify.
func completedAssistantReply(event map[string]any) (string, bool) {
	lifecycle, _ := Lifecycle(event)
	if lifecycle.Step != agent.ReplyEnded || !lifecycle.Reply.Final {
		return "", false
	}
	return lifecycle.Reply.Text, true
}

func completedReplyID(event map[string]any) string {
	lifecycle, _ := Lifecycle(event)
	return lifecycle.Reply.ID
}

func TestCompletedAssistantReplyAcceptsOnlyFinalTextFromCompletedAssistantMessages(t *testing.T) {
	commentarySignature := `{"v":1,"id":"commentary","phase":"commentary"}`
	tests := []struct {
		name  string
		event map[string]any
		text  string
		ok    bool
	}{
		{name: "final reply", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "working", "textSignature": commentarySignature},
			map[string]any{"type": "text", "text": "completed"},
		}}}, text: "completed", ok: true},
		{name: "partial update", event: map[string]any{"type": "message_update", "message": map[string]any{"role": "assistant", "content": []any{"partial"}}}},
		{name: "commentary only", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "working", "textSignature": commentarySignature}}}}},
		{name: "tool-use stop", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "toolUse", "content": []any{"working"}}}},
		{name: "aborted stop", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "aborted", "content": []any{"partial"}}}},
		{name: "error stop", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "error", "content": []any{"failed"}}}},
		{name: "tool message", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "toolResult", "content": []any{"done"}}}},
		{name: "user message", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "user", "content": []any{"done"}}}},
		{name: "empty final", event: map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{"  "}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text, ok := completedAssistantReply(test.event)
			if text != test.text || ok != test.ok {
				t.Fatalf("completedAssistantReply() = %q, %t", text, ok)
			}
		})
	}
}

func TestCompletedReplyIDIsStableWithoutNativeMessageIDs(t *testing.T) {
	event := map[string]any{"type": "message_end", "message": map[string]any{
		"role": "assistant", "timestamp": float64(1_750_000_000_000), "content": []any{"same reply"},
	}}
	first := completedReplyID(event)
	if second := completedReplyID(event); second != first {
		t.Fatalf("same reply IDs = %q and %q", first, second)
	}
	event["message"].(map[string]any)["timestamp"] = float64(1_750_000_000_001)
	if next := completedReplyID(event); next == first {
		t.Fatalf("distinct timestamp reused reply ID %q", next)
	}

	delete(event["message"].(map[string]any), "timestamp")
	if fallback := completedReplyID(event); fallback != completedReplyID(event) {
		t.Fatalf("content fallback is unstable: %q", fallback)
	}
}

// Saved completions and read state keep reply IDs, so a new gateway must compute those an older one saved.
func TestReplyIDsMatchThoseEarlierGatewaysSaved(t *testing.T) {
	text := []any{map[string]any{"type": "text", "text": "Done <b>"}}
	for _, test := range []struct {
		name  string
		event map[string]any
		want  string
	}{
		{"message id", map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "id": "reply-1", "content": text}}, "b74f5653c0d66cb3f2c9390556018b7f"},
		{"numeric message id", map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "messageId": 42.0, "content": text}}, "73475cb40a568e8da8a045ced110137e"},
		{"event id", map[string]any{"type": "message_end", "id": "event-1", "message": map[string]any{"role": "assistant", "content": text}}, "18e2b9a0da9081e6a4a0f7152552c50a"},
		{"contents", map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "id": "", "content": text, "stopReason": "stop", "usage": map[string]any{"input": 3.0}}}, "4ca3de77ae43eae4f209e7bfee6e6452"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := completedReplyID(test.event); got != test.want {
				t.Fatalf("reply ID = %s, want %s", got, test.want)
			}
		})
	}

	message := map[string]any{"role": "assistant", "content": text, "stopReason": "stop"}
	ended, _ := Lifecycle(map[string]any{"type": "agent_end", "messages": []any{message, map[string]any{"role": "toolResult", "content": "output"}}})
	if ended.Step != agent.RunEnded || !ended.Reply.Final || ended.Reply.ID != completedReplyID(map[string]any{"type": "message_end", "message": message}) {
		t.Fatalf("run end = %#v; want the reply as its message end named it", ended)
	}
}
