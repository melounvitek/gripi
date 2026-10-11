package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func snapshotTestClient(t *testing.T, options ClientOptions) (*Client, func(map[string]any)) {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, options)
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	var sequence int64
	return client, func(event map[string]any) {
		t.Helper()
		writeRecord(t, stdoutWriter, event)
		sequence++
		waitSequence(t, client, sequence)
	}
}

func snapshotAssistant(t *testing.T, client *Client) map[string]any {
	t.Helper()
	data, err := json.Marshal(client.LiveSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	event, _ := snapshot["active_assistant_event"].(map[string]any)
	return event
}

func TestLiveSnapshotTracksEveryRunningTool(t *testing.T) {
	calls := 0
	client, emit := snapshotTestClient(t, ClientOptions{Clock: func() time.Time {
		calls++
		return time.UnixMilli(int64(calls))
	}})
	for _, name := range []string{"read", "bash", "write", "edit", "custom", "subagent"} {
		emit(map[string]any{"type": "tool_execution_start", "toolName": name, "toolCallId": name, "args": map[string]any{"path": "file"}})
	}
	if got := activeToolEventIDs(client.LiveSnapshot().ActiveToolEvents); !reflect.DeepEqual(got, []string{"read", "bash", "write", "edit", "custom", "subagent"}) {
		t.Fatalf("running tools = %v", got)
	}
	emit(map[string]any{"type": "tool_execution_update", "toolName": "bash", "toolCallId": "bash", "partialResult": map[string]any{"content": []any{map[string]any{"type": "text", "text": "working"}}}})
	event := client.LiveSnapshot().ActiveToolEvents[1]
	if event["type"] != "tool_execution_update" || numberOrZero(event["gatewayTimestamp"]) != 2 || calls != 6 {
		t.Fatalf("update lost invocation timestamp: %#v, clock calls %d", event, calls)
	}
	for _, name := range []string{"read", "bash", "write", "edit", "custom", "subagent"} {
		emit(map[string]any{"type": "tool_execution_end", "toolName": name, "toolCallId": name, "result": map[string]any{}})
	}
	if events := client.LiveSnapshot().ActiveToolEvents; len(events) != 1 || events[0]["toolName"] != "subagent" || events[0]["type"] != "tool_execution_end" {
		t.Fatalf("completed tools = %#v", events)
	}
}

func TestLiveSnapshotBoundsAllToolKinds(t *testing.T) {
	client, emit := snapshotTestClient(t, ClientOptions{})
	large := strings.Repeat("x", MaxActiveToolSnapshotBytes*2)
	for index := range MaxActiveToolSnapshots + 2 {
		emit(map[string]any{"type": "tool_execution_start", "toolName": "write", "toolCallId": fmt.Sprint(index), "args": map[string]any{"path": "file", "content": large}})
	}
	events := client.LiveSnapshot().ActiveToolEvents
	if len(events) != MaxActiveToolSnapshots {
		t.Fatalf("snapshot count = %d", len(events))
	}
	for _, event := range events {
		args, _ := event["args"].(map[string]any)
		if event["toolName"] != "write" || args["path"] != "file" || jsonSize(event) > MaxActiveToolSnapshotBytes {
			t.Fatalf("bounded start = %#v", event)
		}
	}
	emit(map[string]any{"type": "tool_execution_update", "toolName": "write", "toolCallId": "0", "partialResult": map[string]any{"content": []any{map[string]any{"type": "text", "text": large}}}})
	event := client.LiveSnapshot().ActiveToolEvents[0]
	if event["toolName"] != "write" || event["type"] != "tool_execution_update" || jsonSize(event) > MaxActiveToolSnapshotBytes {
		t.Fatalf("bounded update = %#v", event)
	}
}

func TestLiveSnapshotRestoresAssistantPhaseAndPartialAfterReplayEviction(t *testing.T) {
	client, emit := snapshotTestClient(t, ClientOptions{EventBufferLimit: 1})
	emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}, "timestamp": 123}})
	if event := snapshotAssistant(t, client); event["type"] != "message_start" {
		t.Fatalf("assistant start = %#v", event)
	}
	for _, event := range []map[string]any{
		{"type": "thinking_start", "contentIndex": 0},
		{"type": "thinking_delta", "contentIndex": 0, "delta": "First"},
		{"type": "thinking_delta", "contentIndex": 0, "delta": " thought"},
	} {
		emit(map[string]any{"type": "message_update", "assistantMessageEvent": event})
	}
	emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "custom"}})
	event := snapshotAssistant(t, client)
	phase, _ := event["assistantMessageEvent"].(map[string]any)
	partial, _ := event["gatewayPartialMessage"].(map[string]any)
	if phase["type"] != "thinking_delta" || phase["contentIndex"] != float64(0) || partial["timestamp"] != float64(123) || !reflect.DeepEqual(partial["content"], []any{map[string]any{"type": "thinking", "thinking": "First thought"}}) {
		t.Fatalf("thinking snapshot = %#v", event)
	}
	for _, subtype := range []string{"toolcall_start", "toolcall_delta"} {
		emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": subtype, "contentIndex": 1, "delta": `{"path":`}})
		event = snapshotAssistant(t, client)
		phase, _ = event["assistantMessageEvent"].(map[string]any)
		if phase["type"] != subtype || phase["contentIndex"] != float64(1) {
			t.Fatalf("preparation snapshot = %#v", event)
		}
	}
}

func TestLiveSnapshotRestoresCumulativeAssistant(t *testing.T) {
	client, emit := snapshotTestClient(t, ClientOptions{})
	emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}}})
	message := map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "legacy thought"}}}
	emit(map[string]any{"type": "message_update", "message": message, "assistantMessageEvent": map[string]any{"type": "thinking_delta", "contentIndex": 0, "delta": "thought"}})
	event := snapshotAssistant(t, client)
	if !reflect.DeepEqual(event["message"], message) || event["gatewayPartialMessage"] != nil {
		t.Fatalf("cumulative snapshot = %#v", event)
	}
}

func TestLiveSnapshotClearsAuthoritativeLifecycle(t *testing.T) {
	for _, ending := range []string{"message_end", "agent_end", "agent_settled", "agent_start", "reader_stop"} {
		t.Run(ending, func(t *testing.T) {
			client, emit := snapshotTestClient(t, ClientOptions{})
			emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}}})
			emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "thinking_start", "contentIndex": 0}})
			emit(map[string]any{"type": "tool_execution_start", "toolName": "bash", "toolCallId": "bash"})
			if snapshotAssistant(t, client) == nil || len(client.LiveSnapshot().ActiveToolEvents) != 1 {
				t.Fatal("missing activity before lifecycle end")
			}
			if ending == "reader_stop" {
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				emit(map[string]any{"type": ending, "message": map[string]any{"role": "assistant", "stopReason": "aborted"}})
			}
			if event := snapshotAssistant(t, client); event != nil {
				t.Fatalf("assistant after %s = %#v", ending, event)
			}
			if ending != "message_end" && len(client.LiveSnapshot().ActiveToolEvents) != 0 {
				t.Fatalf("tools survived %s", ending)
			}
			if ending == "reader_stop" {
				return
			}
			emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "thinking_delta", "contentIndex": 0, "delta": "late"}})
			if event := snapshotAssistant(t, client); event != nil {
				t.Fatalf("late update resurrected assistant: %#v", event)
			}
		})
	}
}

func TestLiveSnapshotAssistantContentRemainsBounded(t *testing.T) {
	for _, cumulative := range []bool{false, true} {
		t.Run(fmt.Sprint(cumulative), func(t *testing.T) {
			client, emit := snapshotTestClient(t, ClientOptions{EventBufferBytes: 1024})
			emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}}})
			emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "thinking_start", "contentIndex": 0}})
			large := strings.Repeat("x", 2048)
			update := map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "thinking_delta", "contentIndex": 0, "delta": large}}
			if cumulative {
				update["message"] = map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": large}}}
			}
			emit(update)
			event := snapshotAssistant(t, client)
			phase, _ := event["assistantMessageEvent"].(map[string]any)
			if phase["type"] != "thinking_delta" || jsonSize(event) > 1024 || event["message"] != nil || event["gatewayPartialMessage"] != nil {
				t.Fatalf("bounded assistant = %#v", event)
			}
		})
	}
}

func TestLiveSnapshotAbortRequestDoesNotOptimisticallyClear(t *testing.T) {
	client, emit := snapshotTestClient(t, ClientOptions{})
	emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}}})
	<-client.writeLane
	defer func() { client.writeLane <- struct{}{} }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Abort(ctx); err != context.Canceled {
		t.Fatalf("abort error = %v", err)
	}
	if snapshotAssistant(t, client) == nil {
		t.Fatal("failed abort cleared active assistant")
	}
}
