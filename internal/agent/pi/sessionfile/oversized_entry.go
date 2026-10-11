package sessionfile

import (
	"encoding/json"
	"io"
)

// scanOversizedEntry reads an entry over MaxRenderedEntryBytes, which the
// gateway never holds in memory, for what places it in the session tree. The
// entry may have any shape, but it must be well-formed JSON, so one cut off
// mid-write is still skipped like a malformed line.
func scanOversizedEntry(reader io.Reader) (entry, bool) {
	scanner := oversizedEntryScanner{rootState: scanValue, valid: true}
	buffer := make([]byte, 64<<10)
	for scanner.valid {
		count, err := reader.Read(buffer)
		for _, value := range buffer[:count] {
			if scanner.consume(value); !scanner.valid {
				break
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return entry{}, false
		}
	}
	return scanner.finish()
}

type oversizedFrame struct {
	kind, state byte
	key         string
}

type oversizedEntryScanner struct {
	stack     []oversizedFrame
	rootState byte
	valid     bool
	result    entry
	toolCall  segment
	display   bool
	// Strings are only checked, except near the top, where the fields are.
	inString, stringKey, escaped, keep bool
	unicodeDigits                      int
	kept                               []byte
	inScalar                           bool
	scalar                             []byte
}

func (scanner *oversizedEntryScanner) consume(value byte) {
	if scanner.inString {
		scanner.consumeString(value)
		return
	}
	if scanner.inScalar {
		if scalarByte(value) {
			scanner.scalar = append(scanner.scalar, value)
			scanner.valid = len(scanner.scalar) <= indexMaxScalar
			return
		}
		if scanner.finishScalar(); !scanner.valid {
			return
		}
	}
	if value == ' ' || value == '\t' || value == '\r' || value == '\n' {
		return
	}
	state := scanner.rootState
	if len(scanner.stack) > 0 {
		state = scanner.stack[len(scanner.stack)-1].state
	}
	switch state {
	case scanValue, scanValueOrEnd:
		if state == scanValueOrEnd && value == ']' {
			scanner.closeContainer()
		} else {
			scanner.startValue(value)
		}
	case scanKeyOrEnd, scanKey:
		if state == scanKeyOrEnd && value == '}' {
			scanner.closeContainer()
		} else if value == '"' {
			scanner.startString(true)
		} else {
			scanner.valid = false
		}
	case scanColon:
		scanner.valid = value == ':'
		scanner.stack[len(scanner.stack)-1].state = scanValue
	case scanCommaOrEnd:
		frame := &scanner.stack[len(scanner.stack)-1]
		switch {
		case value == ',' && frame.kind == '{':
			frame.state = scanKey
		case value == ',':
			frame.state = scanValue
		case value == '}' && frame.kind == '{', value == ']' && frame.kind == '[':
			scanner.closeContainer()
		default:
			scanner.valid = false
		}
	default:
		scanner.valid = false
	}
}

func (scanner *oversizedEntryScanner) startValue(value byte) {
	switch {
	case value == '{' || value == '[':
		if len(scanner.stack) == indexMaxDepth || len(scanner.stack) == 0 && value != '{' {
			scanner.valid = false
			return
		}
		state := scanKeyOrEnd
		if value == '[' {
			state = scanValueOrEnd
		}
		scanner.stack = append(scanner.stack, oversizedFrame{kind: value, state: state})
	case len(scanner.stack) == 0:
		scanner.valid = false
	case value == '"':
		scanner.startString(false)
	case value == '-' || value >= '0' && value <= '9' || value == 't' || value == 'f' || value == 'n':
		scanner.inScalar = true
		scanner.scalar = append(scanner.scalar[:0], value)
	default:
		scanner.valid = false
	}
}

func (scanner *oversizedEntryScanner) startString(key bool) {
	scanner.inString, scanner.stringKey, scanner.keep = true, key, len(scanner.stack) <= 2
	scanner.kept = scanner.kept[:0]
}

func (scanner *oversizedEntryScanner) consumeString(value byte) {
	switch {
	case scanner.unicodeDigits > 0:
		_, scanner.valid = hexadecimal(value)
		scanner.unicodeDigits--
	case scanner.escaped:
		scanner.escaped = false
		switch value {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			scanner.unicodeDigits = 4
		default:
			scanner.valid = false
		}
	case value == '\\':
		scanner.escaped = true
	case value == '"':
		scanner.inString = false
		scanner.finishString()
		return
	case value < 0x20:
		scanner.valid = false
	}
	if scanner.keep && len(scanner.kept) <= indexCaptureBytes {
		scanner.kept = append(scanner.kept, value)
	}
}

func (scanner *oversizedEntryScanner) finishString() {
	var text string
	exact := scanner.keep && len(scanner.kept) <= indexCaptureBytes && json.Unmarshal(append(append([]byte{'"'}, scanner.kept...), '"'), &text) == nil
	if scanner.stringKey {
		frame := &scanner.stack[len(scanner.stack)-1]
		frame.key, frame.state = text, scanColon
		return
	}
	if exact {
		switch scanner.field() {
		case "type":
			scanner.result.Type = text
		case "id":
			scanner.result.ID = text
		case "parentId":
			scanner.result.ParentID = text
		case "targetId":
			scanner.result.TargetID = text
		case "timestamp":
			scanner.result.Session.Timestamp = text
		case "message.role":
			scanner.result.Role = text
		case "message.toolCallId":
			scanner.toolCall.ToolCallID = text
		case "message.toolName":
			scanner.toolCall.ToolName = text
		}
	}
	scanner.completeValue()
}

func (scanner *oversizedEntryScanner) finishScalar() {
	scanner.inScalar = false
	text := string(scanner.scalar)
	if text != "true" && text != "false" && text != "null" && !validJSONNumber(text) {
		scanner.valid = false
		return
	}
	switch scanner.field() {
	case "display":
		scanner.display = text == "true"
	case "message.isError":
		scanner.toolCall.Error = text == "true"
	}
	scanner.completeValue()
}

// field names a value at the top of the entry or of its message.
func (scanner *oversizedEntryScanner) field() string {
	if len(scanner.stack) == 1 {
		return scanner.stack[0].key
	}
	if len(scanner.stack) == 2 && scanner.stack[0].key == "message" && scanner.stack[1].kind == '{' {
		return "message." + scanner.stack[1].key
	}
	return ""
}

func (scanner *oversizedEntryScanner) closeContainer() {
	scanner.stack = scanner.stack[:len(scanner.stack)-1]
	scanner.completeValue()
}

func (scanner *oversizedEntryScanner) completeValue() {
	if len(scanner.stack) == 0 {
		scanner.rootState = scanDone
		return
	}
	scanner.stack[len(scanner.stack)-1].state = scanCommaOrEnd
}

// finish returns a stub of the entry. Only the entries Pi shows get a segment,
// in the role they show in, and it renders as a placeholder. A tool result's
// still pairs with its call.
func (scanner *oversizedEntryScanner) finish() (entry, bool) {
	if !scanner.valid || scanner.rootState != scanDone {
		return entry{}, false
	}
	result := scanner.result
	result.Session.Role = result.Role
	switch {
	case result.Type == "message" && result.Role == "toolResult":
		scanner.toolCall.Role = "toolResult"
		result.ResultToolCallID = scanner.toolCall.ToolCallID
		result.Segments = []segment{scanner.toolCall}
	case result.Type == "message" && result.Role != "":
		result.Segments = []segment{{Role: result.Role}}
	case result.Type == "custom_message" && scanner.display:
		result.Segments = []segment{{Role: "custom"}}
	case result.Type == "compaction":
		result.Segments = []segment{{Role: "status"}}
	}
	return result, true
}
