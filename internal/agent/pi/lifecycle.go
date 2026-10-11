package pi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/agent/pi/sessionfile"
)

// Lifecycle returns the step of a run that a Pi RPC event reports, which the adapter passes to the gateway.
func Lifecycle(event map[string]any) (agent.Lifecycle, bool) {
	switch event["type"] {
	case "agent_start":
		return agent.Lifecycle{Step: agent.RunStarted}, true
	case "message_end":
		message, _ := event["message"].(map[string]any)
		if message["role"] != "assistant" {
			return agent.Lifecycle{}, false
		}
		return agent.Lifecycle{Step: agent.ReplyEnded, Reply: reply(event, message)}, true
	case "agent_end":
		// Only the run's last reply counts, never an earlier one.
		messages, _ := event["messages"].([]any)
		for index := len(messages) - 1; index >= 0; index-- {
			if message, _ := messages[index].(map[string]any); message["role"] == "assistant" {
				return agent.Lifecycle{Step: agent.RunEnded, Reply: reply(nil, message)}, true
			}
		}
		return agent.Lifecycle{Step: agent.RunEnded}, true
	case "agent_settled":
		return agent.Lifecycle{Step: agent.RunSettled}, true
	}
	return agent.Lifecycle{}, false
}

// reply reads an assistant message that ended. It is final only when Pi stopped it normally or at the length limit,
// or didn't say why it stopped.
func reply(event, message map[string]any) agent.Reply {
	result := agent.Reply{Text: sessionfile.FinalAssistantText(message["content"])}
	stopReason, _ := message["stopReason"].(string)
	result.Final = result.Text != "" && (stopReason == "" || stopReason == "stop" || stopReason == "length")
	if result.Final {
		result.ID = replyID(event, message)
	}
	return result
}

// replyID identifies a reply by the ID that Pi gives it, or else by its contents, so that its message_end and the
// agent_end of its run name it alike. Notifications and read state keep the ID, so it must never change.
func replyID(event, message map[string]any) string {
	for _, value := range []any{message["id"], message["messageId"], event["id"], event["messageId"]} {
		if encoded, err := json.Marshal(value); err == nil && string(encoded) != "null" && string(encoded) != `""` {
			return digest(encoded)
		}
	}
	encoded, _ := json.Marshal(message)
	return digest(encoded)
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:16])
}
