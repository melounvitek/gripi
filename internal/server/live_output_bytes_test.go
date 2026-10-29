package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	agentpi "github.com/melounvitek/gripi/internal/agent/pi"
	"github.com/melounvitek/gripi/internal/agent/pi/rpc"
	"github.com/melounvitek/gripi/internal/agent/pi/sessionfile"
	"github.com/melounvitek/gripi/internal/config"
)

// The browser parses the live state in the page and the polled events, so the gateway must pass them on byte for
// byte, whatever it reads from them.
func TestLiveOutputAndEventsKeepTheirBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	registry := agent.NewRegistry(func(string) (agent.Client, error) { return nil, os.ErrNotExist }, nil)
	app := &application{
		config:          config.Config{SessionsRoot: root, Home: root},
		sessionStore:    NewPiStore(root, root),
		rpcClients:      registry,
		pendingSessions: agent.NewPendingSessionRegistry(nil),
		guard:           sessionfile.NewSynchronizer(registry),
	}
	started := time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC)
	clock := func() time.Time { return started }
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	observed := make(chan struct{}, 1)
	client := &agentpi.Client{Client: rpc.NewClient(stdinWriter, stdoutReader, nil, rpc.ClientOptions{
		Clock: clock, Now: clock, EventObserver: func(*rpc.Client, map[string]any) { observed <- struct{}{} },
	})}
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	if err := registry.Register(path, client); err != nil {
		t.Fatal(err)
	}
	send := func(event string) {
		t.Helper()
		if _, err := io.WriteString(stdoutWriter, event+"\n"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-observed:
		case <-time.After(time.Second):
			t.Fatal("event was not stored")
		}
	}
	for _, event := range []string{
		`{"type":"agent_start"}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hello <b>&\u2028"}}`,
		`{"type":"tool_execution_start","toolCallId":"read-1","toolName":"read","args":{"path":"README.md"}}`,
		`{"type":"tool_execution_start","toolCallId":"agent-1","toolName":"subagent","args":{"task":"Review"}}`,
		`{"type":"tool_execution_update","toolCallId":"agent-1","toolName":"subagent","partialResult":{"content":[{"type":"text","text":"Working"}]}}`,
		`{"type":"queue_update","steering":["Steer <now>"],"followUp":["Then & more"]}`,
		`{"type":"extension_ui_request","id":"dialog-1","method":"confirm","title":"Proceed?","message":"Run it","timeout":30000}`,
		`{"type":"extension_ui_request","id":"status-1","method":"setStatus","statusKey":"build","statusText":"Building"}`,
		`{"type":"extension_ui_request","id":"widget-1","method":"setWidget","widgetKey":"todo","widgetLines":["one","two"]}`,
		`{"type":"extension_ui_request","id":"title-1","method":"setTitle","title":"Gripi <test>"}`,
	} {
		send(event)
	}

	// Shells run in the order of answers: Pi reports the end of two, refuses a third and keeps the last running, which has printed a line.
	commands := json.NewDecoder(stdinReader)
	var shellIDs []string
	runShell := func(command string, exclude bool, answer map[string]any) chan error {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			_, err := client.Client.Bash(context.Background(), command, exclude)
			done <- err
		}()
		var request map[string]any
		if err := commands.Decode(&request); err != nil || request["type"] != "bash" {
			t.Fatalf("shell request = %#v, %v", request, err)
		}
		shellIDs = append(shellIDs, request["id"].(string))
		if answer != nil {
			answer["type"], answer["id"], answer["command"] = "response", request["id"], "bash"
			if err := json.NewEncoder(stdoutWriter).Encode(answer); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		return done
	}
	runShell("printf done", false, map[string]any{"success": true, "data": map[string]any{"output": "done", "exitCode": 0, "cancelled": false, "truncated": false}})
	runShell("false", true, map[string]any{"success": true, "data": map[string]any{"output": "", "exitCode": 1, "cancelled": false, "truncated": true, "fullOutputPath": "/tmp/out"}})
	runShell("missing", false, map[string]any{"success": false, "error": "Bash failed <x>"})
	running := runShell("sleep 10", false, nil)
	for deadline := time.Now().Add(time.Second); client.ActiveBashCommand() == ""; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("shell did not start")
		}
	}
	t.Cleanup(func() {
		_ = json.NewEncoder(stdoutWriter).Encode(map[string]any{"type": "response", "id": shellIDs[3], "command": "bash", "success": true, "data": map[string]any{"output": ""}})
		<-running
	})
	if err := json.NewEncoder(stdoutWriter).Encode(map[string]any{"type": "bash_execution_update", "id": shellIDs[3], "delta": "tick <1>\n"}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Second); client.EventSequence() < 20; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("shell output was not stored")
		}
	}
	ids := strings.NewReplacer(shellIDs[0], "bash-A", shellIDs[1], "bash-B", shellIDs[2], "bash-C", shellIDs[3], "bash-D")
	normalized := func(output liveOutputData) liveOutputData {
		output.ActiveBashJSON = ids.Replace(output.ActiveBashJSON)
		output.CompletedBashEventsJSON = ids.Replace(output.CompletedBashEventsJSON)
		output.PersistedBashJSON = ids.Replace(output.PersistedBashJSON)
		return output
	}

	recorded := started.Add(time.Second)
	success, failure := 0, 1
	persisted := []*agent.Message{
		{Role: "toolResult", ToolCallID: "read-1"},
		{Role: "bashExecution", Summary: "$ printf done", Text: "done", BashExitCode: &success, BashRecordedAt: recorded, Timestamp: recorded},
		{Role: "bashExecution", Summary: "$ false", BashExitCode: &failure, BashTruncated: true, BashExcludedFromContext: true, BashRecordedAt: recorded, Timestamp: recorded},
		// A shell that failed to run stays live even when a saved one looks like it.
		{Role: "bashExecution", Summary: "$ missing", BashRecordedAt: recorded, Timestamp: recorded},
		{Role: "bashExecution", Summary: "$ sleep 10", Timestamp: recorded},
	}
	live := liveOutputData{
		EventAfter:               20,
		ActiveToolEventsJSON:     `[{"args":{"path":"README.md"},"gatewayTimestamp":1767323045006,"toolCallId":"read-1","toolName":"read","type":"tool_execution_start"},{"gatewayTimestamp":1767323045006,"partialResult":{"content":[{"text":"Working","type":"text"}]},"toolCallId":"agent-1","toolName":"subagent","type":"tool_execution_update"}]`,
		ActiveAssistantEventJSON: `{"assistantMessageEvent":{"contentIndex":0,"type":"text_delta"},"gatewayPartialMessage":{"content":[{"text":"Hello \u003cb\u003e\u0026\u2028","type":"text"}],"role":"assistant"},"type":"message_update"}`,
		ActiveToolTimestampsJSON: `{}`,
		ActiveToolPromptsJSON:    `{}`,
		ActiveBashJSON:           `{"bash_id":"bash-D","command":"sleep 10","exclude_from_context":false,"output":"tick \u003c1\u003e\n","started_at":"2026-01-02T03:04:05.006Z"}`,
		CompletedBashEventsJSON:  `[{"bashId":"bash-A","command":"printf done","excludeFromContext":false,"gatewayTimestamp":1767323045006,"result":{"cancelled":false,"exitCode":0,"output":"done","truncated":false},"startedAt":1767323045006,"type":"bash_end"},{"bashId":"bash-B","command":"false","excludeFromContext":true,"gatewayTimestamp":1767323045006,"result":{"cancelled":false,"exitCode":1,"fullOutputPath":"/tmp/out","output":"","truncated":true},"startedAt":1767323045006,"type":"bash_end"},{"bashId":"bash-C","command":"missing","error":"Bash failed \u003cx\u003e","excludeFromContext":false,"gatewayTimestamp":1767323045006,"startedAt":1767323045006,"type":"bash_error"}]`,
		PersistedBashJSON:        `[]`,
		QueuedMessagesJSON:       `{"followUp":["Then \u0026 more"],"steering":["Steer \u003cnow\u003e"]}`,
		ExtensionUIJSON:          `{"pending_dialogs":[{"id":"dialog-1","message":"Run it","method":"confirm","timeout":30000,"title":"Proceed?","type":"extension_ui_request"}],"statuses":[{"id":"status-1","method":"setStatus","statusKey":"build","statusText":"Building","type":"extension_ui_request"}],"title":{"id":"title-1","method":"setTitle","title":"Gripi \u003ctest\u003e","type":"extension_ui_request"},"widgets":[{"id":"widget-1","method":"setWidget","type":"extension_ui_request","widgetKey":"todo","widgetLines":["one","two"]}]}`,
		ComposerState:            "running",
		ComposerStateSince:       "1767323045006",
		ComposerBusySince:        "1767323045006",
		AgentRunning:             true,
	}
	saved := live
	saved.ActiveToolEventsJSON = `[{"gatewayTimestamp":1767323045006,"partialResult":{"content":[{"text":"Working","type":"text"}]},"toolCallId":"agent-1","toolName":"subagent","type":"tool_execution_update"}]`
	saved.ActiveBashJSON = `null`
	saved.CompletedBashEventsJSON = `[{"bashId":"bash-C","command":"missing","error":"Bash failed \u003cx\u003e","excludeFromContext":false,"gatewayTimestamp":1767323045006,"startedAt":1767323045006,"type":"bash_error"}]`
	saved.PersistedBashJSON = `["bash-D","bash-A","bash-B"]`
	idle := liveOutputData{
		ActiveToolEventsJSON: `[]`, ActiveAssistantEventJSON: `null`, ActiveToolTimestampsJSON: `{}`, ActiveToolPromptsJSON: `{}`,
		ActiveBashJSON: `null`, CompletedBashEventsJSON: `[]`, PersistedBashJSON: `[]`,
		QueuedMessagesJSON: `{"followUp":[],"steering":[]}`, ExtensionUIJSON: `{"pending_dialogs":[],"statuses":[],"title":null,"widgets":[]}`,
		ComposerState: "idle",
	}
	for _, test := range []struct {
		name     string
		snapshot agent.LiveSnapshot
		messages []*agent.Message
		want     liveOutputData
	}{
		{"nothing persisted", registry.LiveSnapshot(path), nil, live},
		{"persisted", registry.LiveSnapshot(path), persisted, saved},
		{"no client", registry.LiveSnapshot(filepath.Join(root, "other.jsonl")), nil, idle},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalized(liveOutputFrom(test.snapshot, test.messages, "", nil)); got != test.want {
				t.Errorf("live output = %#v, want %#v", got, test.want)
			}
		})
	}

	response := httptest.NewRecorder()
	app.events(response, httptest.NewRequest(http.MethodGet, "/events?session="+url.QueryEscape(path)+"&after=0", nil))
	want := `{"events":[` +
		`{"gatewayTimestamp":1767323045006,"type":"agent_start"},` +
		`{"message":{"content":[],"role":"assistant"},"type":"message_start"},` +
		`{"assistantMessageEvent":{"contentIndex":0,"delta":"Hello \u003cb\u003e\u0026\u2028","type":"text_delta"},"gatewayPartialMessage":{"content":[{"text":"Hello \u003cb\u003e\u0026\u2028","type":"text"}],"role":"assistant"},"type":"message_update"},` +
		`{"args":{"path":"README.md"},"gatewayTimestamp":1767323045006,"toolCallId":"read-1","toolName":"read","type":"tool_execution_start"},` +
		`{"args":{"task":"Review"},"gatewayTimestamp":1767323045006,"toolCallId":"agent-1","toolName":"subagent","type":"tool_execution_start"},` +
		`{"gatewayTimestamp":1767323045006,"partialResult":{"content":[{"text":"Working","type":"text"}]},"toolCallId":"agent-1","toolName":"subagent","type":"tool_execution_update"},` +
		`{"followUp":["Then \u0026 more"],"steering":["Steer \u003cnow\u003e"],"type":"queue_update"},` +
		`{"id":"dialog-1","message":"Run it","method":"confirm","timeout":30000,"title":"Proceed?","type":"extension_ui_request"},` +
		`{"id":"status-1","method":"setStatus","statusKey":"build","statusText":"Building","type":"extension_ui_request"},` +
		`{"id":"widget-1","method":"setWidget","type":"extension_ui_request","widgetKey":"todo","widgetLines":["one","two"]},` +
		`{"id":"title-1","method":"setTitle","title":"Gripi \u003ctest\u003e","type":"extension_ui_request"},` +
		`{"bashId":"bash-A","command":"printf done","excludeFromContext":false,"gatewayTimestamp":1767323045006,"type":"bash_start"},` +
		`{"bashId":"bash-A","command":"printf done","excludeFromContext":false,"gatewayTimestamp":1767323045006,"result":{"cancelled":false,"exitCode":0,"output":"done","truncated":false},"startedAt":1767323045006,"type":"bash_end"},` +
		`{"bashId":"bash-B","command":"false","excludeFromContext":true,"gatewayTimestamp":1767323045006,"type":"bash_start"},` +
		`{"bashId":"bash-B","command":"false","excludeFromContext":true,"gatewayTimestamp":1767323045006,"result":{"cancelled":false,"exitCode":1,"fullOutputPath":"/tmp/out","output":"","truncated":true},"startedAt":1767323045006,"type":"bash_end"},` +
		`{"bashId":"bash-C","command":"missing","excludeFromContext":false,"gatewayTimestamp":1767323045006,"type":"bash_start"},` +
		`{"bashId":"bash-C","command":"missing","error":"Bash failed \u003cx\u003e","excludeFromContext":false,"gatewayTimestamp":1767323045006,"startedAt":1767323045006,"type":"bash_error"},` +
		`{"bashId":"bash-D","command":"sleep 10","excludeFromContext":false,"gatewayTimestamp":1767323045006,"type":"bash_start"},` +
		`{"bashId":"bash-D","command":"sleep 10","excludeFromContext":false,"gatewayTimestamp":1767323045006,"output":"tick \u003c1\u003e\n","type":"bash_update"}` +
		`],"last_seq":20,"missed":false,"session_sync":{"error":null,"gateway_busy":true,"mode":"available","revision":null},"sudo_requests":[]}`
	if body := ids.Replace(response.Body.String()); response.Code != http.StatusOK || body != want {
		t.Errorf("events = %d %s", response.Code, body)
	}
}
