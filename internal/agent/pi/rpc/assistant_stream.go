package rpc

import (
	"encoding/json"
	"slices"
)

const maxAssistantStreamParts = 1_024

type assistantStreamState struct {
	message  map[string]any
	parts    map[int]*assistantStreamPart
	bytes    int
	maxBytes int
	disabled bool
}

type assistantStreamPart struct {
	kind     string
	content  []byte
	toolCall map[string]any
	// Set while a tool call streams: its arguments parsed so far from the JSON in content.
	arguments map[string]any
}

func newAssistantStreamState(message map[string]any, maxBytes int) *assistantStreamState {
	metadata := cloneMap(message)
	metadata["content"] = []any{}
	stream := &assistantStreamState{message: metadata, parts: make(map[int]*assistantStreamPart), bytes: jsonSize(metadata), maxBytes: maxBytes}
	stream.disabled = stream.bytes > maxBytes
	return stream
}

func (stream *assistantStreamState) apply(event map[string]any) {
	if stream == nil || stream.disabled {
		return
	}
	index, ok := assistantContentIndex(event["contentIndex"])
	if !ok {
		return
	}

	switch event["type"] {
	case "text_start":
		stream.startPart(index, "text")
	case "text_delta":
		if delta, ok := event["delta"].(string); ok {
			stream.appendPart(index, "text", delta)
		}
	case "text_end":
		if content, ok := event["content"].(string); ok {
			stream.finishPart(index, "text", content)
		}
	case "thinking_start":
		stream.startPart(index, "thinking")
	case "thinking_delta":
		if delta, ok := event["delta"].(string); ok {
			stream.appendPart(index, "thinking", delta)
		}
	case "thinking_end":
		if content, ok := event["content"].(string); ok {
			stream.finishPart(index, "thinking", content)
		}
	case "toolcall_start":
		// Older Pi versions name the tool only at toolcall_end, so their calls show once complete.
		id, _ := event["id"].(string)
		name, _ := event["toolName"].(string)
		if id != "" && name != "" {
			stream.setToolCall(index, map[string]any{"type": "toolCall", "id": id, "name": name})
			if part := stream.parts[index]; part != nil {
				part.arguments = map[string]any{}
			}
		}
	case "toolcall_delta":
		if delta, ok := event["delta"].(string); ok {
			stream.appendToolCallArguments(index, delta)
		}
	case "toolcall_end":
		toolCall, _ := event["toolCall"].(map[string]any)
		if toolCall != nil {
			stream.setToolCall(index, toolCall)
		}
	}
}

func (stream *assistantStreamState) startPart(index int, kind string) {
	stream.removePart(index)
	stream.parts[index] = &assistantStreamPart{kind: kind}
}

func (stream *assistantStreamState) appendPart(index int, kind, delta string) {
	part := stream.parts[index]
	if part == nil || part.kind != kind || part.toolCall != nil {
		return
	}
	if !stream.reserve(len(delta)) {
		return
	}
	part.content = append(part.content, delta...)
	stream.bytes += len(delta)
}

func (stream *assistantStreamState) appendToolCallArguments(index int, delta string) {
	part := stream.parts[index]
	if part == nil || part.arguments == nil || !stream.reserve(len(delta)) {
		return
	}
	part.content = append(part.content, delta...)
	stream.bytes += len(delta)
	if arguments, ok := partialJSONObject(part.content); ok {
		part.arguments = arguments
	}
}

// partialJSONObject closes the strings and brackets a streamed JSON object has left open.
// It fails while the prefix ends where no value can be closed, such as after a key.
func partialJSONObject(prefix []byte) (map[string]any, bool) {
	var closers []byte
	inString := false
	escapeStart, escapeLength := -1, 0
	for index, char := range prefix {
		switch {
		case escapeStart >= 0:
			if index == escapeStart+1 && char == 'u' {
				escapeLength = len(`\u0000`)
			}
			if index-escapeStart+1 >= escapeLength {
				escapeStart = -1
			}
		case inString && char == '\\':
			escapeStart, escapeLength = index, len(`\n`)
		case char == '"':
			inString = !inString
		case inString:
		case char == '{':
			closers = append(closers, '}')
		case char == '[':
			closers = append(closers, ']')
		case (char == '}' || char == ']') && len(closers) > 0:
			closers = closers[:len(closers)-1]
		}
	}

	completed := prefix
	if escapeStart >= 0 {
		completed = completed[:escapeStart]
	}
	if inString {
		completed = slices.Concat(completed, []byte(`"`))
	}
	slices.Reverse(closers)
	var object map[string]any
	if err := json.Unmarshal(slices.Concat(completed, closers), &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

func (stream *assistantStreamState) finishPart(index int, kind, content string) {
	stream.removePart(index)
	if !stream.reserve(len(content)) {
		return
	}
	stream.parts[index] = &assistantStreamPart{kind: kind, content: append([]byte(nil), content...)}
	stream.bytes += len(content)
}

func (stream *assistantStreamState) setToolCall(index int, toolCall map[string]any) {
	stream.removePart(index)
	size := jsonSize(toolCall)
	if !stream.reserve(size) {
		return
	}
	stream.parts[index] = &assistantStreamPart{kind: "toolCall", toolCall: cloneMap(toolCall)}
	stream.bytes += size
}

func (stream *assistantStreamState) removePart(index int) {
	part := stream.parts[index]
	if part == nil {
		return
	}
	stream.bytes -= len(part.content)
	if part.toolCall != nil {
		stream.bytes -= jsonSize(part.toolCall)
	}
	delete(stream.parts, index)
}

func (stream *assistantStreamState) reserve(size int) bool {
	if size < 0 || stream.bytes+size > stream.maxBytes {
		stream.disabled = true
		stream.parts = nil
		return false
	}
	return true
}

func (stream *assistantStreamState) partialMessage() map[string]any {
	if stream == nil || stream.disabled {
		return nil
	}
	maximumIndex := -1
	for index := range stream.parts {
		maximumIndex = max(maximumIndex, index)
	}
	content := make([]any, maximumIndex+1)
	for index, part := range stream.parts {
		switch part.kind {
		case "text":
			content[index] = map[string]any{"type": "text", "text": string(part.content)}
		case "thinking":
			content[index] = map[string]any{"type": "thinking", "thinking": string(part.content)}
		case "toolCall":
			toolCall := cloneMap(part.toolCall)
			if part.arguments != nil {
				toolCall["arguments"] = part.arguments
			}
			content[index] = toolCall
		}
	}
	message := cloneMap(stream.message)
	message["content"] = content
	return message
}

func assistantContentIndex(value any) (int, bool) {
	number, ok := numberValue(value)
	index := int(number)
	return index, ok && number == float64(index) && index >= 0 && index < maxAssistantStreamParts
}
