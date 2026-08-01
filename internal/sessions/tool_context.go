package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

type ToolCallContext struct {
	Prompt          string
	Timestamp       time.Time
	ResultPersisted bool
}

func (store Store) SubagentToolCallContext(path, leafID string, ids []string) map[string]ToolCallContext {
	requested := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			requested[id] = true
		}
	}
	result := make(map[string]ToolCallContext)
	if len(requested) == 0 {
		return result
	}
	canonical, ok := store.canonicalSessionPath(path)
	if !ok {
		return result
	}
	indexed, err := store.Cache.Index(canonical)
	if err != nil {
		return result
	}
	branch, ok := indexed.entriesForLeaf(leafID, true)
	if !ok {
		return result
	}
	for _, entry := range branch {
		for _, segment := range entry.Segments {
			if segment.Role == "toolResult" && segment.ToolName == "subagent" && requested[segment.ToolCallID] {
				details := result[segment.ToolCallID]
				details.ResultPersisted = true
				result[segment.ToolCallID] = details
			}
		}
	}
	file, err := os.Open(canonical)
	if err != nil {
		return result
	}
	defer file.Close()
	for _, entry := range branch {
		needed := false
		for _, id := range entry.SubagentIDs {
			if requested[id] && result[id].Prompt == "" {
				needed = true
				break
			}
		}
		if !needed || entry.Length > MaxRenderedEntryBytes {
			continue
		}
		data := make([]byte, entry.Length)
		if _, err := file.ReadAt(data, entry.Offset); err != nil && !errors.Is(err, io.EOF) {
			continue
		}
		var raw map[string]any
		if json.Unmarshal(bytes.TrimSpace(data), &raw) != nil {
			continue
		}
		message := asMap(raw["message"])
		if stringValue(message["role"]) != "assistant" {
			continue
		}
		for _, part := range arrayValue(message["content"]) {
			call := asMap(part)
			id := stringValue(call["id"])
			if requested[id] && stringValue(call["type"]) == "toolCall" && stringValue(call["name"]) == "subagent" {
				details := result[id]
				details.Prompt = subagentPrompt(call["arguments"])
				details.Timestamp = parseTime(stringValue(raw["timestamp"]))
				result[id] = details
			}
		}
	}
	stat, err := os.Stat(canonical)
	if err != nil || !indexSnapshotValid(indexed, stat) {
		return map[string]ToolCallContext{}
	}
	return result
}
