package rpc

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
	case "toolcall_start", "toolcall_delta":
		// Pi does not expose tool identity until toolcall_end, so only complete calls are projected.
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
			content[index] = cloneMap(part.toolCall)
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
