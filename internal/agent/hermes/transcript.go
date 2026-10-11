package hermes

import (
	"encoding/json"
	"strings"

	"github.com/melounvitek/gripi/internal/agent"
)

// update is an ACP session update, with the fields of every kind the transcript reads.
type update struct {
	SessionUpdate string `json:"sessionUpdate"`
	// Content is one block for a message chunk, and a list of tool call contents for a tool call.
	Content    json.RawMessage `json:"content"`
	MessageID  string          `json:"messageId"`
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	// Size and Used are a usage update's context window and how much of it the session fills.
	Size float64 `json:"size"`
	Used float64 `json:"used"`
}

// transcript projects a session's updates onto the gateway's messages, the same way for a replayed history and a
// live run.
type transcript struct {
	messages []*agent.Message
	// messageID is the ACP message that the last message continues.
	messageID string
	tools     map[string]*agent.Message
}

func (transcript *transcript) apply(value update) {
	switch value.SessionUpdate {
	case "user_message_chunk":
		transcript.appendText(value, "user", false)
	case "agent_message_chunk":
		transcript.appendText(value, "assistant", false)
	case "agent_thought_chunk":
		transcript.appendText(value, "assistant", true)
	case "tool_call":
		message := &agent.Message{Role: "assistant", Compact: true, ToolCallID: value.ToolCallID, ToolName: toolName(value), Summary: value.Title, ToolPending: true}
		transcript.updateTool(message, value)
		transcript.add(message, "")
		if transcript.tools == nil {
			transcript.tools = make(map[string]*agent.Message)
		}
		transcript.tools[value.ToolCallID] = message
	case "tool_call_update":
		// Hermes also updates calls it never announced, such as the checks behind its permission requests.
		if message := transcript.tools[value.ToolCallID]; message != nil {
			transcript.updateTool(message, value)
		}
	}
}

func (transcript *transcript) appendText(value update, role string, thinking bool) {
	var block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(value.Content, &block) != nil || block.Type != "text" || block.Text == "" {
		return
	}
	if count := len(transcript.messages); count > 0 {
		last := transcript.messages[count-1]
		if last.Role == role && last.Thinking == thinking && !last.Compact && value.MessageID == transcript.messageID {
			last.Text += block.Text
			return
		}
	}
	transcript.add(&agent.Message{Role: role, Text: block.Text, Thinking: thinking, FinalAssistantResponse: role == "assistant" && !thinking}, value.MessageID)
}

func (transcript *transcript) add(message *agent.Message, messageID string) {
	message.Key = [2]int{len(transcript.messages), 0}
	transcript.messages = append(transcript.messages, message)
	transcript.messageID = messageID
}

func (transcript *transcript) updateTool(message *agent.Message, value update) {
	if value.Title != "" {
		message.Summary = value.Title
	}
	if text := contentText(value.Content); text != "" {
		message.Text = text
	}
	switch value.Status {
	case "completed", "failed":
		message.ToolPending = false
		message.ToolResultPersisted = true
		message.Error = value.Status == "failed"
	}
}

// toolName is the Hermes tool that a call's title starts with, such as "terminal" in "terminal: npm test".
func toolName(value update) string {
	if name, _, found := strings.Cut(value.Title, ":"); found {
		return name
	}
	if value.Title != "" {
		return value.Title
	}
	return value.Kind
}

// contentText is the text of a tool call's contents, with a diff shown as the lines it changes.
func contentText(raw json.RawMessage) string {
	var contents []struct {
		Type    string `json:"type"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Path    string  `json:"path"`
		OldText *string `json:"oldText"`
		NewText string  `json:"newText"`
	}
	if json.Unmarshal(raw, &contents) != nil {
		return ""
	}
	var parts []string
	for _, content := range contents {
		switch {
		case content.Type == "content" && content.Content.Type == "text":
			parts = append(parts, content.Content.Text)
		case content.Type == "diff":
			old := ""
			if content.OldText != nil {
				old = *content.OldText
			}
			parts = append(parts, changedLines(content.Path, old, content.NewText))
		}
	}
	return strings.Join(parts, "\n\n")
}

// changedLines shows an edit as the lines between the start and the end that the old and new text share.
func changedLines(path, old, new string) string {
	before, after := strings.Split(old, "\n"), strings.Split(new, "\n")
	if old == "" {
		before = nil
	}
	for len(before) > 0 && len(after) > 0 && before[0] == after[0] {
		before, after = before[1:], after[1:]
	}
	for len(before) > 0 && len(after) > 0 && before[len(before)-1] == after[len(after)-1] {
		before, after = before[:len(before)-1], after[:len(after)-1]
	}
	lines := []string{path}
	for _, line := range before {
		lines = append(lines, "-"+line)
	}
	for _, line := range after {
		lines = append(lines, "+"+line)
	}
	return strings.Join(lines, "\n")
}
