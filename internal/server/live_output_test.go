package server

import (
	"encoding/json"
	"html"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestLiveOutputHydratesActiveAssistantEvent(t *testing.T) {
	for _, event := range []string{
		`null`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0},"gatewayPartialMessage":{"role":"assistant","content":[{"type":"thinking","thinking":"Consider <script> carefully"}]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":1},"message":{"role":"assistant","content":[{"type":"toolCall","name":"read","arguments":{}}]}}`,
	} {
		var snapshot rpc.LiveSnapshot
		if err := json.Unmarshal([]byte(`{"event_sequence":42,"active_assistant_event":`+event+`}`), &snapshot); err != nil {
			t.Fatal(err)
		}
		view := &pageView{Selected: &sessions.Session{Path: "/session", CWD: "/project"}, LiveOutput: liveOutputFrom(snapshot, nil, "", nil)}
		templates, err := template.New("").Funcs(templateFunctions(rendering.NewMarkdown())).ParseFS(templateFiles, "templates/*.html")
		if err != nil {
			t.Fatal(err)
		}
		var rendered strings.Builder
		if err := templates.ExecuteTemplate(&rendered, "conversation", view); err != nil {
			t.Fatal(err)
		}
		_, rest, found := strings.Cut(rendered.String(), `data-active-assistant-event="`)
		if !found {
			t.Fatal("missing assistant hydration attribute")
		}
		attribute, _, _ := strings.Cut(rest, `"`)
		var got, want any
		if err := json.Unmarshal([]byte(html.UnescapeString(attribute)), &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(event), &want); err != nil {
			t.Fatal(err)
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) || !strings.Contains(rendered.String(), `data-events-after="42"`) {
			t.Fatalf("hydration = %s, want %s", gotJSON, wantJSON)
		}
	}
}

func TestLiveOutputFiltersPersistedToolsWithoutLosingPairing(t *testing.T) {
	stamp := time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)
	snapshot := rpc.LiveSnapshot{ActiveToolEvents: []map[string]any{
		{"type": "tool_execution_update", "toolName": "bash", "toolCallId": "paired"},
		{"type": "tool_execution_start", "toolName": "read", "toolCallId": "persisted"},
		{"type": "tool_execution_end", "toolName": "subagent", "toolCallId": "older-result"},
		{"type": "tool_execution_end", "toolName": "subagent", "toolCallId": "unpersisted"},
		{"type": "tool_execution_update", "toolName": "custom", "toolCallId": "running", "gatewayTimestamp": stamp.UnixMilli()},
	}}
	messages := []*sessions.Message{
		{Role: "assistant", ToolCallID: "paired", ToolResultPersisted: true},
		{Role: "toolResult", ToolCallID: "persisted"},
	}
	context := map[string]sessions.ToolCallContext{
		"older-result": {ResultPersisted: true},
		"unpersisted":  {Timestamp: stamp, Prompt: "Review active work"},
	}
	output := liveOutputFrom(snapshot, messages, "", context)
	var events []map[string]any
	if err := json.Unmarshal([]byte(output.ActiveToolEventsJSON), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0]["toolCallId"] != "unpersisted" || events[0]["type"] != "tool_execution_end" || events[1]["toolCallId"] != "running" || events[1]["gatewayTimestamp"] != float64(stamp.UnixMilli()) {
		t.Fatalf("filtered events = %#v", events)
	}
	if output.ActiveToolTimestampsJSON != `{"unpersisted":"2026-01-01T00:00:02Z"}` || output.ActiveToolPromptsJSON != `{"unpersisted":"Review active work"}` {
		t.Fatalf("pairing = %#v", output)
	}
}
