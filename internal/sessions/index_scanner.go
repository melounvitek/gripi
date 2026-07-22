package sessions

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	indexCaptureBytes = 8 << 10
	indexMaxDepth     = 100
	indexMaxValues    = 2_000
	indexMaxScalar    = 128
)

type scannedString struct {
	capture             []byte
	preview             []byte
	previewStarted      bool
	bytes               int
	characters          int
	nonWhitespace       int
	nonTrimWhitespace   int
	bytesAfterLastSlash int
	truncated           bool
	valid               bool
	escaped             bool
	unicodeDigits       int
	unicodeValue        rune
	highSurrogate       rune
	utfBytes            [4]byte
	utfLength           int
	utfExpected         int
	utfValue            rune
	utfMinimum          rune
}

func newScannedString() *scannedString { return &scannedString{valid: true} }

func (value *scannedString) consume(byteValue byte) {
	if !value.valid {
		return
	}
	if value.unicodeDigits > 0 {
		digit, ok := hexadecimal(byteValue)
		if !ok {
			value.valid = false
			return
		}
		value.unicodeValue = value.unicodeValue*16 + rune(digit)
		value.unicodeDigits++
		if value.unicodeDigits == 5 {
			value.finishUnicodeEscape()
		}
		return
	}
	if value.escaped {
		value.escaped = false
		if value.highSurrogate != 0 && byteValue != 'u' {
			value.valid = false
			return
		}
		switch byteValue {
		case '"', '\\', '/':
			value.emitRune(rune(byteValue))
		case 'b':
			value.emitRune('\b')
		case 'f':
			value.emitRune('\f')
		case 'n':
			value.emitRune('\n')
		case 'r':
			value.emitRune('\r')
		case 't':
			value.emitRune('\t')
		case 'u':
			value.unicodeDigits = 1
			value.unicodeValue = 0
		default:
			value.valid = false
		}
		return
	}
	if byteValue == '\\' {
		if value.utfExpected != 0 {
			value.valid = false
			return
		}
		value.escaped = true
		return
	}
	if byteValue < 0x20 {
		value.valid = false
		return
	}
	if value.utfExpected == 0 {
		switch {
		case byteValue < utf8.RuneSelf:
			if value.highSurrogate != 0 {
				value.valid = false
				return
			}
			value.emitRune(rune(byteValue))
		case byteValue >= 0xC2 && byteValue <= 0xDF:
			value.startUTF8(byteValue, 1, rune(byteValue&0x1F), 0x80)
		case byteValue >= 0xE0 && byteValue <= 0xEF:
			value.startUTF8(byteValue, 2, rune(byteValue&0x0F), 0x800)
		case byteValue >= 0xF0 && byteValue <= 0xF4:
			value.startUTF8(byteValue, 3, rune(byteValue&0x07), 0x10000)
		default:
			value.valid = false
		}
		return
	}
	if byteValue < 0x80 || byteValue > 0xBF {
		value.valid = false
		return
	}
	value.utfBytes[value.utfLength] = byteValue
	value.utfLength++
	value.utfValue = value.utfValue<<6 | rune(byteValue&0x3F)
	value.utfExpected--
	if value.utfExpected == 0 {
		codepoint := value.utfValue
		if codepoint < value.utfMinimum || codepoint > utf8.MaxRune || codepoint >= 0xD800 && codepoint <= 0xDFFF || value.highSurrogate != 0 {
			value.valid = false
			return
		}
		value.emitEncoded(value.utfBytes[:value.utfLength], codepoint)
		value.utfLength = 0
	}
}

func (value *scannedString) startUTF8(first byte, expected int, codepoint, minimum rune) {
	if value.highSurrogate != 0 {
		value.valid = false
		return
	}
	value.utfBytes[0] = first
	value.utfLength = 1
	value.utfExpected = expected
	value.utfValue = codepoint
	value.utfMinimum = minimum
}

func (value *scannedString) finishUnicodeEscape() {
	codepoint := value.unicodeValue
	value.unicodeDigits = 0
	value.unicodeValue = 0
	if codepoint >= 0xD800 && codepoint <= 0xDBFF {
		if value.highSurrogate != 0 {
			value.valid = false
			return
		}
		value.highSurrogate = codepoint
		return
	}
	if codepoint >= 0xDC00 && codepoint <= 0xDFFF {
		if value.highSurrogate == 0 {
			value.valid = false
			return
		}
		codepoint = 0x10000 + (value.highSurrogate-0xD800)<<10 + codepoint - 0xDC00
		value.highSurrogate = 0
	} else if value.highSurrogate != 0 {
		value.valid = false
		return
	}
	value.emitRune(codepoint)
}

func (value *scannedString) emitRune(codepoint rune) {
	var encoded [utf8.UTFMax]byte
	length := utf8.EncodeRune(encoded[:], codepoint)
	value.emitEncoded(encoded[:length], codepoint)
}

func (value *scannedString) emitEncoded(encoded []byte, codepoint rune) {
	value.bytes += len(encoded)
	value.characters++
	if codepoint != 0 && !(codepoint >= '\t' && codepoint <= '\r') && codepoint != ' ' {
		value.nonWhitespace += len(encoded)
	}
	if !unicode.IsSpace(codepoint) {
		value.nonTrimWhitespace += len(encoded)
		value.previewStarted = true
	}
	if value.previewStarted && len(value.preview)+len(encoded) <= indexCaptureBytes {
		value.preview = append(value.preview, encoded...)
	}
	if codepoint == '/' {
		value.bytesAfterLastSlash = 0
	} else {
		value.bytesAfterLastSlash += len(encoded)
	}
	if !value.truncated && len(value.capture)+len(encoded) <= indexCaptureBytes {
		value.capture = append(value.capture, encoded...)
	} else {
		value.truncated = true
	}
}

func (value *scannedString) finish() bool {
	return value.valid && !value.escaped && value.unicodeDigits == 0 && value.highSurrogate == 0 && value.utfExpected == 0
}

func (value *scannedString) exact() (string, bool) {
	if value == nil || value.truncated || !value.valid {
		return "", false
	}
	return string(value.capture), true
}

func (value *scannedString) prefix() string {
	if value == nil {
		return ""
	}
	return string(value.capture)
}

func (value *scannedString) sessionPrefix() string {
	if value == nil {
		return ""
	}
	if value.nonTrimWhitespace > 0 && strings.TrimSpace(string(value.capture)) == "" {
		return string(value.preview)
	}
	return string(value.capture)
}

func hexadecimal(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

type scanPathPart struct {
	key   string
	index int
	array bool
}

type scanFrame struct {
	kind  byte
	path  []scanPathPart
	state byte
	key   string
	index int
	keys  map[string]bool
}

type indexJSONScanner struct {
	collector        *indexCollector
	stack            []scanFrame
	rootState        byte
	token            byte
	text             []byte
	string           *scannedString
	stringKey        bool
	structuralKeys   int
	structuralValues int
	valid            bool
}

const (
	scanValue byte = iota + 1
	scanValueOrEnd
	scanKeyOrEnd
	scanKey
	scanColon
	scanCommaOrEnd
	scanDone
	scanString
	scanNumber
	scanLiteral
)

func newIndexJSONScanner() *indexJSONScanner {
	return &indexJSONScanner{collector: newIndexCollector(), rootState: scanValue, valid: true}
}

func (scanner *indexJSONScanner) feed(chunk []byte) {
	for position := 0; scanner.valid && position < len(chunk); {
		if scanner.consume(chunk[position]) {
			position++
		}
	}
}

func (scanner *indexJSONScanner) consume(value byte) bool {
	switch scanner.token {
	case scanString:
		if value == '"' && !scanner.string.escaped && scanner.string.unicodeDigits == 0 && scanner.string.utfExpected == 0 {
			if !scanner.string.finish() {
				scanner.valid = false
				return true
			}
			scanner.token = 0
			if scanner.stringKey {
				key, exact := scanner.string.exact()
				if !exact || len(scanner.stack) == 0 {
					scanner.valid = false
					return true
				}
				frame := &scanner.stack[len(scanner.stack)-1]
				if frame.keys != nil {
					if frame.keys[key] || scanner.structuralKeys >= indexMaxValues {
						scanner.valid = false
						return true
					}
					frame.keys[key] = true
					scanner.structuralKeys++
				}
				frame.key = key
				frame.state = scanColon
				scanner.collector.objectKey(frame.path, key)
			} else {
				scanner.collector.stringValue(scanner.currentPath(), scanner.string)
				scanner.completeValue()
			}
			return true
		}
		scanner.string.consume(value)
		if !scanner.string.valid {
			scanner.valid = false
		}
		return true
	case scanNumber, scanLiteral:
		if scalarByte(value) {
			if len(scanner.text) >= indexMaxScalar {
				scanner.valid = false
				return true
			}
			scanner.text = append(scanner.text, value)
			return true
		}
		scanner.finishScalar()
		return false
	}
	if value == ' ' || value == '\t' || value == '\r' || value == '\n' {
		return true
	}
	state := scanner.rootState
	if len(scanner.stack) > 0 {
		state = scanner.stack[len(scanner.stack)-1].state
	}
	switch state {
	case scanValue, scanValueOrEnd:
		if state == scanValueOrEnd && value == ']' {
			scanner.closeContainer('[')
		} else {
			scanner.startValue(value)
		}
	case scanKeyOrEnd:
		if value == '}' {
			scanner.closeContainer('{')
		} else if value == '"' {
			scanner.startString(true)
		} else {
			scanner.valid = false
		}
	case scanKey:
		if value == '"' {
			scanner.startString(true)
		} else {
			scanner.valid = false
		}
	case scanColon:
		if value == ':' {
			scanner.stack[len(scanner.stack)-1].state = scanValue
		} else {
			scanner.valid = false
		}
	case scanCommaOrEnd:
		frame := &scanner.stack[len(scanner.stack)-1]
		if frame.kind == '{' {
			if value == ',' {
				frame.state = scanKey
			} else if value == '}' {
				scanner.closeContainer('{')
			} else {
				scanner.valid = false
			}
		} else if value == ',' {
			frame.state = scanValue
		} else if value == ']' {
			scanner.closeContainer('[')
		} else {
			scanner.valid = false
		}
	default:
		scanner.valid = false
	}
	return true
}

func (scanner *indexJSONScanner) startValue(value byte) {
	scanner.structuralValues++
	if scanner.structuralValues > indexMaxValues {
		scanner.valid = false
		return
	}
	switch {
	case value == '{' || value == '[':
		if len(scanner.stack) >= indexMaxDepth {
			scanner.valid = false
			return
		}
		path := scanner.currentPath()
		scanner.collector.startContainer(path, value)
		frame := scanFrame{kind: value, path: path}
		if value == '{' {
			frame.state = scanKeyOrEnd
			if canonicalKeyPath(path) {
				frame.keys = make(map[string]bool)
			}
		} else {
			frame.state = scanValueOrEnd
		}
		scanner.stack = append(scanner.stack, frame)
	case value == '"':
		scanner.startString(false)
	case value == '-' || value >= '0' && value <= '9':
		scanner.token = scanNumber
		scanner.text = append(scanner.text[:0], value)
	case value == 't' || value == 'f' || value == 'n':
		scanner.token = scanLiteral
		scanner.text = append(scanner.text[:0], value)
	default:
		scanner.valid = false
	}
}

func (scanner *indexJSONScanner) startString(key bool) {
	scanner.token = scanString
	scanner.string = newScannedString()
	scanner.stringKey = key
}

func (scanner *indexJSONScanner) finishScalar() {
	text := string(scanner.text)
	var value any
	valid := true
	if scanner.token == scanLiteral {
		switch text {
		case "true":
			value = true
		case "false":
			value = false
		case "null":
			value = nil
		default:
			valid = false
		}
	} else {
		var number json.Number = json.Number(text)
		parsed, err := number.Float64()
		if err != nil || math.IsInf(parsed, 0) || !validJSONNumber(text) {
			valid = false
		} else {
			value = parsed
		}
	}
	if !valid {
		scanner.valid = false
		return
	}
	scanner.collector.scalarValue(scanner.currentPath(), value)
	scanner.token = 0
	scanner.text = scanner.text[:0]
	scanner.completeValue()
}

func (scanner *indexJSONScanner) closeContainer(kind byte) {
	if len(scanner.stack) == 0 || scanner.stack[len(scanner.stack)-1].kind != kind {
		scanner.valid = false
		return
	}
	scanner.stack = scanner.stack[:len(scanner.stack)-1]
	scanner.completeValue()
}

func (scanner *indexJSONScanner) completeValue() {
	if len(scanner.stack) == 0 {
		scanner.rootState = scanDone
		return
	}
	frame := &scanner.stack[len(scanner.stack)-1]
	if frame.kind == '{' {
		frame.key = ""
	} else {
		frame.index++
	}
	frame.state = scanCommaOrEnd
}

func (scanner *indexJSONScanner) currentPath() []scanPathPart {
	if len(scanner.stack) == 0 {
		return nil
	}
	frame := &scanner.stack[len(scanner.stack)-1]
	path := make([]scanPathPart, len(frame.path), len(frame.path)+1)
	copy(path, frame.path)
	if frame.kind == '{' {
		path = append(path, scanPathPart{key: frame.key})
	} else {
		path = append(path, scanPathPart{array: true, index: frame.index})
	}
	return path
}

func (scanner *indexJSONScanner) finish() (entry, bool) {
	if scanner.token == scanNumber || scanner.token == scanLiteral {
		scanner.finishScalar()
	}
	if !scanner.valid || scanner.token != 0 || len(scanner.stack) != 0 || scanner.rootState != scanDone || !scanner.collector.valid {
		return entry{}, false
	}
	return scanner.collector.metadata()
}

func scalarByte(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value == '+' || value == '-' || value == '.'
}

func validJSONNumber(value string) bool {
	if value == "" {
		return false
	}
	position := 0
	if value[position] == '-' {
		position++
		if position == len(value) {
			return false
		}
	}
	if value[position] == '0' {
		position++
	} else if value[position] >= '1' && value[position] <= '9' {
		for position < len(value) && value[position] >= '0' && value[position] <= '9' {
			position++
		}
	} else {
		return false
	}
	if position < len(value) && value[position] == '.' {
		position++
		start := position
		for position < len(value) && value[position] >= '0' && value[position] <= '9' {
			position++
		}
		if position == start {
			return false
		}
	}
	if position < len(value) && (value[position] == 'e' || value[position] == 'E') {
		position++
		if position < len(value) && (value[position] == '+' || value[position] == '-') {
			position++
		}
		start := position
		for position < len(value) && value[position] >= '0' && value[position] <= '9' {
			position++
		}
		if position == start {
			return false
		}
	}
	return position == len(value)
}

type indexCollector struct {
	keys                 map[string][]string
	strings              map[string]*scannedString
	scalars              map[string]any
	containers           map[string]byte
	seen                 map[string]byte
	argumentBytes        map[int]int
	argumentCommands     map[int]*scannedString
	generalToolNames     map[int]string
	generalToolArguments map[int]map[string]*scannedString
	generalOutputBytes   int
	generalStreaming     *scannedString
	generalLastTextItem  *scannedString
	generalAmbiguous     bool
	allJSONMinimum       int64
	tracked              int
	valid                bool
}

func newIndexCollector() *indexCollector {
	return &indexCollector{
		keys: make(map[string][]string), strings: make(map[string]*scannedString), scalars: make(map[string]any),
		containers: make(map[string]byte), seen: make(map[string]byte), argumentBytes: make(map[int]int),
		argumentCommands: make(map[int]*scannedString), generalToolNames: make(map[int]string),
		generalToolArguments: make(map[int]map[string]*scannedString), valid: true,
	}
}

func (collector *indexCollector) reserve() bool {
	collector.tracked++
	if collector.tracked > indexMaxValues {
		collector.valid = false
		return false
	}
	return true
}

func (collector *indexCollector) startContainer(path []scanPathPart, kind byte) {
	if !collector.valid {
		return
	}
	collector.allJSONMinimum++
	if generalScalarStringPath(path) {
		collector.generalAmbiguous = true
	}
	if interestingContainer(path) && collector.reserve() {
		collector.containers[pathCode(path)] = kind
	}
	collector.markContentItem(path, kind)
}

func (collector *indexCollector) objectKey(path []scanPathPart, key string) {
	if !collector.valid {
		return
	}
	collector.allJSONMinimum += int64(len(key))
	if !canonicalKeyPath(path) || !collector.reserve() {
		return
	}
	code := pathCode(path)
	collector.keys[code] = append(collector.keys[code], key)
}

func (collector *indexCollector) stringValue(path []scanPathPart, value *scannedString) {
	if !collector.valid || !value.finish() {
		collector.valid = false
		return
	}
	collector.allJSONMinimum += int64(value.bytes)
	collector.markContentItem(path, 's')
	collector.trackGeneralString(path, value)
	if argumentStringPath(path) {
		collector.argumentBytes[path[2].index] += value.bytes
		if len(path) == 5 && path[4].key == "command" {
			collector.argumentCommands[path[2].index] = value
		}
	}
	if interestingString(path) && collector.reserve() {
		collector.strings[pathCode(path)] = value
	}
}

func (collector *indexCollector) trackGeneralString(path []scanPathPart, value *scannedString) {
	if pathEquals(path, "message", "details", "streamingText") {
		collector.generalStreaming = value
		return
	}
	if len(path) == 4 && pathEqualsPrefix(path, "message", "details", "textItems") && path[3].array {
		collector.generalLastTextItem = value
		return
	}
	if len(path) == 5 && pathEqualsPrefix(path, "message", "details", "tools") && path[3].array && path[4].key == "output" {
		collector.generalOutputBytes += value.nonTrimWhitespace
		return
	}
	if len(path) == 5 && pathEqualsPrefix(path, "message", "details", "tools") && path[3].array && path[4].key == "name" {
		if name, exact := value.exact(); exact && collector.reserve() {
			collector.generalToolNames[path[3].index] = name
		}
		return
	}
	if len(path) == 6 && pathEqualsPrefix(path, "message", "details", "tools") && path[3].array && path[4].key == "args" && contains([]string{"command", "path", "file_path", "pattern"}, path[5].key) {
		if !collector.reserve() {
			return
		}
		arguments := collector.generalToolArguments[path[3].index]
		if arguments == nil {
			arguments = make(map[string]*scannedString)
			collector.generalToolArguments[path[3].index] = arguments
		}
		arguments[path[5].key] = value
	}
}

func (collector *indexCollector) scalarValue(path []scanPathPart, value any) {
	if !collector.valid {
		return
	}
	collector.allJSONMinimum++
	if generalScalarStringPath(path) {
		collector.generalAmbiguous = true
		if len(path) == 4 && pathEqualsPrefix(path, "message", "details", "textItems") && path[3].array {
			collector.generalLastTextItem = nil
		}
	}
	collector.markContentItem(path, 'v')
	if interestingScalar(path) && collector.reserve() {
		collector.scalars[pathCode(path)] = value
	}
}

func (collector *indexCollector) markContentItem(path []scanPathPart, kind byte) {
	if !collector.valid {
		return
	}
	if messageContentItem(path) || customContentItem(path) {
		collector.seen[pathCode(path)] = kind
	}
}

func (collector *indexCollector) metadata() (entry, bool) {
	typeName, ok := collector.exact("type")
	if !ok || !collector.canonicalTopLevel(typeName) {
		return entry{}, false
	}
	switch typeName {
	case "message":
		return collector.messageMetadata()
	case "compaction":
		return collector.compactionMetadata()
	case "branch_summary":
		result, valid := collector.baseMetadata(typeName)
		_, fromOK := collector.exact("fromId")
		_, summaryOK := collector.exact("summary")
		return result, valid && fromOK && summaryOK
	case "custom_message":
		return collector.customMessageMetadata()
	default:
		return entry{}, false
	}
}

func (collector *indexCollector) baseMetadata(typeName string) (entry, bool) {
	id, idOK := collector.exact("id")
	parentID, parentOK := collector.nullableString("parentId")
	if !idOK || !parentOK {
		return entry{}, false
	}
	return entry{Type: typeName, ID: id, ParentID: parentID}, true
}

func (collector *indexCollector) messageMetadata() (entry, bool) {
	result, ok := collector.baseMetadata("message")
	if !ok {
		return entry{}, false
	}
	role, roleOK := collector.exact("message", "role")
	if !roleOK || !collector.canonicalMessage(role) {
		return entry{}, false
	}
	result.Role = role
	var parts []scannedPart
	if role != "bashExecution" {
		var partsOK bool
		parts, partsOK = collector.contentParts([]string{"message", "content"})
		if !partsOK {
			return entry{}, false
		}
	}
	result.Session.Role = role
	result.Session.Timestamp, _ = collector.exact("timestamp")
	result.Session.MetadataKnown = true
	switch role {
	case "assistant":
		result.Segments, result.SubagentIDs, ok = assistantScanSegments(parts, collector.argumentBytes, collector.argumentCommands)
		if !ok {
			return entry{}, false
		}
		result.Status = collector.assistantStatus(parts)
		result.Session.FinalText, result.Session.HasFinalText, result.Session.MetadataKnown = finalScannedAssistantText(parts)
	case "user":
		if visibleScannedContent(parts) {
			result.Segments = []segment{{Role: role, Minimum: userScannedMinimum(parts)}}
		}
		result.Status.EstimateChars, result.Status.EstimateKnown = estimatedScannedCharacters(parts)
		result.Session.Text = joinedScannedContent(parts)
	case "toolResult":
		toolCallID, callOK := collector.exact("message", "toolCallId")
		toolName, nameOK := collector.exact("message", "toolName")
		if !callOK || !nameOK {
			return entry{}, false
		}
		visible := visibleScannedContent(parts)
		if !visible && toolName == "edit" {
			diff := collector.stringStats("message", "details", "diff")
			visible = diff != nil && diff.bytes > 0
		}
		generalSubagent := toolName == "subagent" && collector.container("message", "details", "tools") == '[' && collector.container("message", "details", "usage") == '{'
		if !visible && generalSubagent {
			visible = true
		}
		if visible {
			minimum := scannedContentMinimum(parts)
			if generalSubagent {
				minimum = collector.generalSubagentMinimum(parts)
			}
			if toolName == "edit" {
				if diff := collector.stringStats("message", "details", "diff"); diff != nil && diff.bytes > 0 {
					minimum = int64(diff.bytes * 2)
				}
			}
			pairedMinimum := minimum
			isError := collector.scalar("message", "isError") == true
			switch toolName {
			case "read":
				if isError {
					pairedMinimum = scannedTrimmedContentMinimum(parts)
				} else {
					pairedMinimum = scannedImageBytes(parts)
				}
			case "write":
				pairedMinimum = scannedTrimmedContentMinimum(parts)
			case "edit":
				if isError {
					if diff := collector.stringStats("message", "details", "diff"); diff != nil && diff.bytes > 0 {
						pairedMinimum = int64(diff.nonTrimWhitespace * 2)
					} else {
						pairedMinimum = scannedTrimmedContentMinimum(parts)
					}
				}
			}
			result.Segments = []segment{{Role: role, ToolCallID: toolCallID, ToolName: toolName, Minimum: minimum, PairedMinimum: pairedMinimum}}
		}
		result.Status.EstimateChars, result.Status.EstimateKnown = estimatedScannedCharacters(parts)
	case "bashExecution":
		command := collector.stringStats("message", "command")
		output := collector.stringStats("message", "output")
		_, timestampOK := collector.scalar("message", "timestamp").(float64)
		_, cancelledOK := collector.scalar("message", "cancelled").(bool)
		_, truncatedOK := collector.scalar("message", "truncated").(bool)
		keys := collector.keys[pathCodeFromKeys("message")]
		exitCode, exitExists := collector.scalars[pathCodeFromKeys("message", "exitCode")]
		exitNumber, exitNumberOK := exitCode.(float64)
		exitOK := !contains(keys, "exitCode") || exitExists && (exitCode == nil || exitNumberOK && exitNumber == math.Trunc(exitNumber))
		_, excludedOK := collector.scalar("message", "excludeFromContext").(bool)
		excludedOK = !contains(keys, "excludeFromContext") || excludedOK
		_, fullPathOK := collector.exact("message", "fullOutputPath")
		fullPathOK = !contains(keys, "fullOutputPath") || fullPathOK
		if command == nil || output == nil || !timestampOK || !cancelledOK || !truncatedOK || !exitOK || !excludedOK || !fullPathOK {
			return entry{}, false
		}
		minimum := (safeHomeReplacementLower(command) + output.bytes) * 2
		result.Segments = []segment{{Role: role, ToolName: "bash", Minimum: int64(minimum)}}
		excluded := collector.scalar("message", "excludeFromContext") == true
		var exitCodeValue *int
		if exitNumberOK {
			value := int(exitNumber)
			exitCodeValue = &value
		}
		var fullPathCharacters *int
		if fullPath := collector.stringStats("message", "fullOutputPath"); fullPath != nil && fullPath.characters > 0 {
			value := fullPath.characters
			fullPathCharacters = &value
		}
		result.Status = statusData{
			Excluded: excluded, EstimateKnown: !excluded,
			EstimateChars: bashExecutionCharacterLength(command.characters, output.characters, output.bytes == 0, exitCodeValue, collector.scalar("message", "cancelled") == true, collector.scalar("message", "truncated") == true, fullPathCharacters),
		}
	default:
		return entry{}, false
	}
	return result, true
}

func (collector *indexCollector) generalSubagentMinimum(parts []scannedPart) int64 {
	if collector.generalAmbiguous {
		return 0
	}
	finalBytes := 0
	if collector.generalStreaming != nil && collector.generalStreaming.bytes > 0 {
		finalBytes = collector.generalStreaming.bytes
	} else if collector.generalLastTextItem != nil && collector.generalLastTextItem.bytes > 0 {
		finalBytes = collector.generalLastTextItem.bytes
	} else {
		finalBytes = int(scannedContentMinimum(parts) / 2)
	}
	argumentBytes := 0
	for index, arguments := range collector.generalToolArguments {
		bytesFor := func(keys ...string) int {
			for _, key := range keys {
				if value := arguments[key]; value != nil {
					return value.bytes
				}
			}
			return 0
		}
		switch collector.generalToolNames[index] {
		case "bash":
			argumentBytes += bytesFor("command")
		case "read", "write", "edit", "ls":
			argumentBytes += bytesFor("path", "file_path")
		case "grep", "find":
			argumentBytes += bytesFor("pattern") + bytesFor("path")
		}
	}
	return int64((collector.generalOutputBytes + finalBytes + argumentBytes) * 2)
}

func (collector *indexCollector) assistantStatus(parts []scannedPart) statusData {
	usage := map[string]any{}
	for _, key := range []string{"totalTokens", "total_tokens", "tokens", "contextWindow", "context_window", "contextLimit", "context_limit", "contextPercent", "context_percent", "costTotal", "cost_total"} {
		if value, exists := collector.scalars[pathCodeFromKeys("message", "usage", key)]; exists {
			usage[key] = value
		}
	}
	if total, exists := collector.scalars[pathCodeFromKeys("message", "usage", "cost", "total")]; exists {
		usage["cost"] = map[string]any{"total": total}
	}
	if collector.container("message", "usage") != '{' {
		usage = nil
	}
	estimate, known := estimatedScannedCharacters(parts)
	provider, _ := collector.exact("message", "provider")
	model, _ := collector.exact("message", "model")
	stop, _ := collector.exact("message", "stopReason")
	return statusData{Kind: "assistant", Provider: provider, ModelID: model, Usage: usage, StopReason: stop, EstimateChars: estimate, EstimateKnown: known}
}

func (collector *indexCollector) compactionMetadata() (entry, bool) {
	result, ok := collector.baseMetadata("compaction")
	firstKept, firstOK := collector.exact("firstKeptEntryId")
	summary := collector.stringStats("summary")
	if !ok || !firstOK || summary == nil {
		return entry{}, false
	}
	minimum := int64(summary.nonTrimWhitespace * 2)
	if summary.nonTrimWhitespace == 0 {
		minimum = collector.allJSONMinimum * 2
	}
	result.Segments = []segment{{Role: "status", Minimum: minimum}}
	result.Status = statusData{Kind: "compaction", SummaryLength: summary.characters, FirstKeptID: firstKept}
	return result, true
}

func (collector *indexCollector) customMessageMetadata() (entry, bool) {
	result, ok := collector.baseMetadata("custom_message")
	_, typeOK := collector.exact("customType")
	display, displayOK := collector.scalar("display").(bool)
	if !ok || !typeOK || !displayOK {
		return entry{}, false
	}
	parts, partsOK := collector.contentParts([]string{"content"})
	if collector.hasString("content") {
		parts = []scannedPart{{direct: collector.stringStats("content")}}
		partsOK = true
	}
	if !partsOK {
		return entry{}, false
	}
	for _, part := range parts {
		if part.direct == nil && part.typeName != "text" && part.typeName != "image" {
			return entry{}, false
		}
	}
	if display {
		result.Segments = []segment{{Role: "custom", Minimum: scannedContentMinimum(parts)}}
	}
	return result, true
}

func (collector *indexCollector) canonicalTopLevel(typeName string) bool {
	expected := map[string][]string{
		"message":        {"type", "id", "parentId", "timestamp", "message"},
		"compaction":     {"type", "id", "parentId", "timestamp", "summary", "firstKeptEntryId", "tokensBefore", "details", "fromHook"},
		"branch_summary": {"type", "id", "parentId", "timestamp", "fromId", "summary", "details", "fromHook"},
		"custom_message": {"type", "customType", "content", "display", "details", "id", "parentId", "timestamp"},
	}[typeName]
	required := map[string][]string{
		"message":        {"type", "id", "parentId", "timestamp", "message"},
		"compaction":     {"type", "id", "parentId", "timestamp", "summary", "firstKeptEntryId", "tokensBefore"},
		"branch_summary": {"type", "id", "parentId", "timestamp", "fromId", "summary"},
		"custom_message": {"type", "customType", "content", "display", "id", "parentId", "timestamp"},
	}[typeName]
	actual := collector.keys[pathCode(nil)]
	return orderedSubset(actual, expected) && containsAll(actual, required)
}

func (collector *indexCollector) canonicalMessage(role string) bool {
	keys := collector.keys[pathCodeFromKeys("message")]
	switch role {
	case "user":
		return equalStrings(keys, []string{"role", "content"}) || equalStrings(keys, []string{"role", "content", "timestamp"})
	case "toolResult":
		expected := []string{"role", "toolCallId", "toolName", "content", "details", "addedToolNames", "isError", "timestamp"}
		return orderedSubset(keys, expected) && len(keys) >= 4 && equalStrings(keys[:4], expected[:4]) && contains(keys, "isError")
	case "assistant":
		if equalStrings(keys, []string{"role", "content"}) {
			return true
		}
		if len(keys) < 8 || !equalStrings(keys[:8], []string{"role", "content", "api", "provider", "model", "usage", "stopReason", "timestamp"}) {
			return false
		}
		_, providerOK := collector.exact("message", "provider")
		_, modelOK := collector.exact("message", "model")
		return providerOK && modelOK && orderedSubset(keys[8:], []string{"responseModel", "responseId", "diagnostics", "errorMessage"})
	case "bashExecution":
		expected := []string{"role", "command", "output", "exitCode", "cancelled", "truncated", "fullOutputPath", "timestamp", "excludeFromContext"}
		return orderedSubset(keys, expected) && containsAll(keys, []string{"role", "command", "output", "cancelled", "truncated", "timestamp"})
	default:
		return false
	}
}

func (collector *indexCollector) contentParts(root []string) ([]scannedPart, bool) {
	if collector.container(root...) != '[' {
		if len(root) == 2 && root[0] == "message" && root[1] == "content" && collector.hasString(root...) {
			return []scannedPart{{direct: collector.stringStats(root...)}}, true
		}
		return nil, false
	}
	var result []scannedPart
	for index := 0; ; index++ {
		itemPath := append(keyPath(root...), scanPathPart{array: true, index: index})
		kind, exists := collector.seen[pathCode(itemPath)]
		if !exists {
			break
		}
		if kind == 's' {
			result = append(result, scannedPart{index: index, direct: collector.strings[pathCode(itemPath)]})
			continue
		}
		if kind != '{' {
			return nil, false
		}
		partRoot := append(itemPath, scanPathPart{key: "type"})
		typeName, typeOK := collector.strings[pathCode(partRoot)].exact()
		if !typeOK || !collector.canonicalPart(itemPath, typeName) {
			return nil, false
		}
		part := scannedPart{index: index, typeName: typeName}
		part.text = collector.strings[pathCode(append(itemPath, scanPathPart{key: "text"}))]
		part.thinking = collector.strings[pathCode(append(itemPath, scanPathPart{key: "thinking"}))]
		part.output = collector.strings[pathCode(append(itemPath, scanPathPart{key: "output"}))]
		part.result = collector.strings[pathCode(append(itemPath, scanPathPart{key: "result"}))]
		part.data = collector.strings[pathCode(append(itemPath, scanPathPart{key: "data"}))]
		part.signature = collector.strings[pathCode(append(itemPath, scanPathPart{key: "textSignature"}))]
		var idOK, nameOK bool
		part.id, idOK = collector.strings[pathCode(append(itemPath, scanPathPart{key: "id"}))].exact()
		part.name, nameOK = collector.strings[pathCode(append(itemPath, scanPathPart{key: "name"}))].exact()
		if typeName == "toolCall" && (!idOK || !nameOK) {
			return nil, false
		}
		part.toolName, _ = collector.strings[pathCode(append(itemPath, scanPathPart{key: "toolName"}))].exact()
		part.mimeType, _ = collector.strings[pathCode(append(itemPath, scanPathPart{key: "mimeType"}))].exact()
		result = append(result, part)
	}
	return result, true
}

func (collector *indexCollector) canonicalPart(path []scanPathPart, typeName string) bool {
	expected := map[string][]string{
		"text": {"type", "text", "textSignature"}, "thinking": {"type", "thinking", "thinkingSignature", "redacted"},
		"toolCall": {"type", "id", "name", "arguments", "thoughtSignature"}, "toolResult": {"type", "output", "result", "toolName", "name"},
		"image": {"type", "data", "mimeType"},
	}[typeName]
	required := map[string][]string{"text": {"type", "text"}, "thinking": {"type", "thinking"}, "toolCall": {"type", "id", "name", "arguments"}, "toolResult": {"type"}, "image": {"type", "data", "mimeType"}}[typeName]
	actual := collector.keys[pathCode(path)]
	return orderedSubset(actual, expected) && containsAll(actual, required)
}

func (collector *indexCollector) exact(keys ...string) (string, bool) {
	return collector.stringStats(keys...).exact()
}
func (collector *indexCollector) nullableString(keys ...string) (string, bool) {
	if value, ok := collector.exact(keys...); ok {
		return value, true
	}
	value, exists := collector.scalars[pathCodeFromKeys(keys...)]
	return "", exists && value == nil
}
func (collector *indexCollector) stringStats(keys ...string) *scannedString {
	return collector.strings[pathCodeFromKeys(keys...)]
}
func (collector *indexCollector) hasString(keys ...string) bool {
	return collector.stringStats(keys...) != nil
}
func (collector *indexCollector) scalar(keys ...string) any {
	return collector.scalars[pathCodeFromKeys(keys...)]
}
func (collector *indexCollector) container(keys ...string) byte {
	return collector.containers[pathCodeFromKeys(keys...)]
}

type scannedPart struct {
	index                           int
	typeName, id, name, toolName    string
	mimeType                        string
	direct, text, thinking          *scannedString
	output, result, data, signature *scannedString
}

func assistantScanSegments(parts []scannedPart, argumentBytes map[int]int, argumentCommands map[int]*scannedString) ([]segment, []string, bool) {
	var segments []segment
	var subagents []string
	plainBytes := 0
	plainParts := 0
	flush := func() {
		renderedBytes := plainBytes + max(plainParts-1, 0)
		if renderedBytes > 0 {
			segments = append(segments, segment{Role: "assistant", Minimum: int64(renderedBytes * 2)})
		}
		plainBytes = 0
		plainParts = 0
	}
	for _, part := range parts {
		if part.typeName == "toolCall" && part.name == "subagent" {
			if part.id != "" {
				subagents = append(subagents, part.id)
			}
			continue
		}
		switch part.typeName {
		case "thinking":
			flush()
			stats, ok := renderedThinking(part.thinking)
			if !ok {
				return nil, nil, false
			}
			if stats.characters > 0 {
				segments = append(segments, segment{Role: "assistant", Minimum: int64(stats.bytes * 2)})
			}
		case "toolCall", "toolResult":
			flush()
			toolName := firstString(part.name, part.toolName)
			minimum := int64(0)
			if part.typeName == "toolResult" {
				if stats := scannedContentStats(part); stats != nil {
					minimum = int64(stats.bytes * 2)
				}
			} else if toolName == "bash" {
				minimum = int64(safeHomeReplacementLower(argumentCommands[part.index]) * 2)
			} else if toolName != "read" && toolName != "write" && toolName != "edit" {
				minimum = int64(argumentBytes[part.index] * 2)
			}
			segments = append(segments, segment{Role: "assistant", ToolCallID: part.id, ToolName: toolName, Minimum: minimum})
		default:
			if stats := scannedContentStats(part); stats != nil {
				plainBytes += stats.bytes
				plainParts++
			}
		}
	}
	flush()
	return segments, subagents, true
}

func visibleScannedContent(parts []scannedPart) bool {
	textBytes, textParts := scannedJoinedText(parts)
	if textBytes+max(textParts-1, 0) > 0 {
		return true
	}
	for _, part := range parts {
		if part.typeName == "image" && imageMIMETypes[part.mimeType] && part.data != nil && part.data.bytes > 0 {
			return true
		}
		if part.typeName == "toolCall" && part.name != "bash" && part.name != "read" && part.name != "write" && part.name != "edit" {
			return true
		}
	}
	return false
}

func scannedJoinedText(parts []scannedPart) (int, int) {
	bytes := 0
	count := 0
	for _, part := range parts {
		stats := scannedContentStats(part)
		if part.typeName == "thinking" {
			stats, _ = renderedThinking(stats)
		}
		if stats != nil {
			bytes += stats.bytes
			count++
		} else if part.typeName == "toolResult" {
			bytes += len("[tool result]")
			count++
		}
	}
	return bytes, count
}

func scannedContentStats(part scannedPart) *scannedString {
	if part.direct != nil {
		return part.direct
	}
	if part.text != nil {
		return part.text
	}
	if part.typeName == "thinking" {
		return part.thinking
	}
	if part.typeName == "toolResult" {
		if part.output != nil {
			return part.output
		}
		return part.result
	}
	return nil
}

func safeHomeReplacementLower(value *scannedString) int {
	if value == nil {
		return 0
	}
	return value.bytesAfterLastSlash
}

func scannedContentMinimum(parts []scannedPart) int64 {
	textBytes, textParts := scannedJoinedText(parts)
	return int64((textBytes+max(textParts-1, 0))*2) + scannedImageBytes(parts)
}

func scannedTrimmedContentMinimum(parts []scannedPart) int64 {
	value := int64(0)
	for _, part := range parts {
		if stats := scannedContentStats(part); stats != nil {
			value += int64(stats.nonTrimWhitespace * 2)
		}
	}
	return value + scannedImageBytes(parts)
}

func scannedImageBytes(parts []scannedPart) int64 {
	value := int64(0)
	for _, part := range parts {
		if part.typeName == "image" && imageMIMETypes[part.mimeType] && part.data != nil {
			value += int64(part.data.bytes)
		}
	}
	return value
}

func userScannedMinimum(parts []scannedPart) int64 {
	for _, part := range parts {
		if stats := scannedContentStats(part); stats != nil {
			if strings.HasPrefix(strings.TrimLeft(stats.prefix(), " \t\r\n"), "<skill name=\"") {
				return scannedImageBytes(parts)
			}
			break
		}
	}
	return scannedContentMinimum(parts)
}

func renderedThinking(value *scannedString) (*scannedString, bool) {
	if value == nil {
		return nil, false
	}
	if exact, ok := value.exact(); ok {
		return statsFromString(stripThinkingHeading(exact)), true
	}
	trimmed := strings.TrimLeft(value.prefix(), " \t\r\n")
	if trimmed == "" || trimmed == "*" || strings.HasPrefix(trimmed, "**") {
		return nil, false
	}
	return value, true
}

func statsFromString(value string) *scannedString {
	result := newScannedString()
	for _, codepoint := range value {
		result.emitRune(codepoint)
	}
	return result
}

func estimatedScannedCharacters(parts []scannedPart) (int, bool) {
	values := 0
	count := 0
	for _, part := range parts {
		if part.typeName == "toolCall" || part.typeName == "toolResult" {
			return 0, false
		}
		stats := scannedContentStats(part)
		if part.typeName == "thinking" {
			var ok bool
			stats, ok = renderedThinking(stats)
			if !ok {
				return 0, false
			}
		}
		if stats != nil {
			values += stats.characters
			count++
		}
	}
	if count > 1 {
		values += count - 1
	}
	return values, true
}

func joinedScannedContent(parts []scannedPart) string {
	values := make([]string, 0, len(parts))
	remaining := indexCaptureBytes
	for _, part := range parts {
		stats := scannedContentStats(part)
		if stats == nil || remaining <= 0 {
			continue
		}
		text := boundedUTF8Prefix(stats.sessionPrefix(), remaining)
		values = append(values, text)
		remaining -= len(text)
	}
	return strings.Join(values, "\n")
}

func finalScannedAssistantText(parts []scannedPart) (string, bool, bool) {
	var values []string
	nonWhitespace := false
	remaining := indexCaptureBytes
	for _, part := range parts {
		if part.direct == nil && part.typeName != "text" {
			continue
		}
		if part.signature != nil && strings.HasPrefix(part.signature.prefix(), "{") {
			signature, exact := part.signature.exact()
			if !exact {
				return "", false, false
			}
			if assistantTextPhase(signature) == "commentary" {
				continue
			}
		}
		stats := scannedContentStats(part)
		if stats == nil {
			continue
		}
		nonWhitespace = nonWhitespace || stats.nonTrimWhitespace > 0
		if remaining > 0 {
			text := boundedUTF8Prefix(stats.sessionPrefix(), remaining)
			values = append(values, text)
			remaining -= len(text)
		}
	}
	return strings.TrimSpace(strings.Join(values, "\n")), nonWhitespace, true
}

func boundedUTF8Prefix(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func canonicalKeyPath(path []scanPathPart) bool {
	return len(path) == 0 || pathEquals(path, "message") || pathEquals(path, "message", "details") || messagePartPath(path) || customPartPath(path) ||
		(len(path) == 4 && pathEqualsPrefix(path, "message", "details", "tools") && path[3].array) ||
		(len(path) == 5 && pathEqualsPrefix(path, "message", "details", "tools") && path[3].array && path[4].key == "args")
}

func interestingContainer(path []scanPathPart) bool {
	return len(path) == 0 || pathEquals(path, "message") || pathEquals(path, "message", "content") || pathEquals(path, "content") ||
		messageContentItem(path) || customContentItem(path) || pathEquals(path, "message", "usage") || pathEquals(path, "message", "usage", "cost") ||
		pathEquals(path, "message", "details") || pathEquals(path, "message", "details", "tools") || pathEquals(path, "message", "details", "usage")
}

func interestingString(path []scanPathPart) bool {
	if len(path) == 1 && !path[0].array && contains([]string{"type", "id", "parentId", "timestamp", "summary", "firstKeptEntryId", "customType", "fromId", "content"}, path[0].key) {
		return true
	}
	if len(path) == 2 && pathEqualsPrefix(path, "message") && contains([]string{"role", "toolCallId", "toolName", "provider", "model", "stopReason", "content", "command", "output", "fullOutputPath"}, path[1].key) {
		return true
	}
	if messageContentItem(path) || customContentItem(path) || messagePartValuePath(path) || customPartValuePath(path) {
		return true
	}
	return pathEquals(path, "message", "details", "diff")
}

func interestingScalar(path []scanPathPart) bool {
	return pathEquals(path, "parentId") || pathEquals(path, "display") || pathEquals(path, "message", "isError") ||
		(len(path) == 2 && pathEqualsPrefix(path, "message") && contains([]string{"exitCode", "cancelled", "truncated", "timestamp", "excludeFromContext", "fullOutputPath"}, path[1].key)) ||
		pathEqualsPrefix(path, "message", "usage") || messageContentItem(path) || customContentItem(path)
}

func generalScalarStringPath(path []scanPathPart) bool {
	return pathEquals(path, "message", "details", "streamingText") ||
		(len(path) == 4 && pathEqualsPrefix(path, "message", "details", "textItems") && path[3].array) ||
		(len(path) == 5 && pathEqualsPrefix(path, "message", "details", "tools") && path[3].array && contains([]string{"name", "output"}, path[4].key)) ||
		(len(path) == 6 && pathEqualsPrefix(path, "message", "details", "tools") && path[3].array && path[4].key == "args" && contains([]string{"command", "path", "file_path", "pattern"}, path[5].key))
}

func argumentStringPath(path []scanPathPart) bool {
	return len(path) >= 5 && pathEqualsPrefix(path, "message", "content") && path[2].array && !path[3].array && path[3].key == "arguments"
}

func messageContentItem(path []scanPathPart) bool {
	return len(path) == 3 && pathEqualsPrefix(path, "message", "content") && path[2].array
}
func customContentItem(path []scanPathPart) bool {
	return len(path) == 2 && pathEqualsPrefix(path, "content") && path[1].array
}
func messagePartPath(path []scanPathPart) bool { return messageContentItem(path) }
func customPartPath(path []scanPathPart) bool  { return customContentItem(path) }
func messagePartValuePath(path []scanPathPart) bool {
	return len(path) == 4 && messageContentItem(path[:3])
}
func customPartValuePath(path []scanPathPart) bool {
	return len(path) == 3 && customContentItem(path[:2])
}

func pathEquals(path []scanPathPart, keys ...string) bool {
	if len(path) != len(keys) {
		return false
	}
	return pathEqualsPrefix(path, keys...)
}
func pathEqualsPrefix(path []scanPathPart, keys ...string) bool {
	if len(path) < len(keys) {
		return false
	}
	for index, key := range keys {
		if path[index].array || path[index].key != key {
			return false
		}
	}
	return true
}
func keyPath(keys ...string) []scanPathPart {
	result := make([]scanPathPart, len(keys))
	for index, key := range keys {
		result[index].key = key
	}
	return result
}
func pathCodeFromKeys(keys ...string) string { return pathCode(keyPath(keys...)) }
func pathCode(path []scanPathPart) string {
	var result strings.Builder
	for _, part := range path {
		if part.array {
			result.WriteByte('i')
			result.WriteString(strconv.Itoa(part.index))
			result.WriteByte(';')
		} else {
			result.WriteByte('k')
			result.WriteString(strconv.Itoa(len(part.key)))
			result.WriteByte(':')
			result.WriteString(part.key)
		}
	}
	return result.String()
}

func orderedSubset(actual, expected []string) bool {
	position := -1
	seen := map[string]bool{}
	for _, value := range actual {
		if seen[value] {
			return false
		}
		seen[value] = true
		found := -1
		for index := position + 1; index < len(expected); index++ {
			if expected[index] == value {
				found = index
				break
			}
		}
		if found < 0 {
			return false
		}
		position = found
	}
	return true
}
func containsAll(actual, required []string) bool {
	for _, value := range required {
		if !contains(actual, value) {
			return false
		}
	}
	return true
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
