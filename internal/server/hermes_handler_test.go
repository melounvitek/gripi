package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

// hermesConfig configures a gateway with the fake Pi and the fake Hermes, which has saved the given sessions, each a
// map in the fake's state format.
func hermesConfig(t *testing.T, sessions ...map[string]any) config.Config {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "hermes-state.json")
	encoded, err := json.Marshal(map[string]any{"sessions": sessions})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HERMES_STATE", state)
	_, file, _, _ := runtime.Caller(0)
	support := filepath.Join(filepath.Dir(file), "..", "..", "e2e", "support")
	return config.Config{
		Address: "127.0.0.1:4567", Environment: "test", Home: root,
		SessionsRoot: filepath.Join(root, "sessions"), AttachmentsRoot: filepath.Join(root, "attachments"),
		ReadStatePath: filepath.Join(root, "read.json"), PinnedSessionsPath: filepath.Join(root, "pinned.json"), SessionTagsPath: filepath.Join(root, "tags.json"),
		BrowserAccessPath: filepath.Join(root, "browser.json"), BrowserAuthDisabled: true, PermittedHosts: []string{"example.com"},
		EnvironmentPath: filepath.Join(root, "environment.json"), RPCIdleTimeout: time.Hour,
		PiCommand: []string{"node", filepath.Join(support, "fake_pi.mjs")}, HermesPath: filepath.Join(support, "fake_hermes.mjs"),
	}
}

// hermesHandler runs the gateway with the fake Hermes, which has saved the given sessions.
func hermesHandler(t *testing.T, sessions ...map[string]any) http.Handler {
	t.Helper()
	handler, err := gateway.NewHandlerWithHermes(hermesConfig(t, sessions...), gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	return handler
}

func getPage(t *testing.T, handler http.Handler, target string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	body, _ := io.ReadAll(recorder.Result().Body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, recorder.Code, body)
	}
	return string(body)
}

func textUpdate(kind, text string) map[string]any {
	return map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text}}
}

func toolUpdate(kind, id string, fields map[string]any) map[string]any {
	update := map[string]any{"sessionUpdate": kind, "toolCallId": id}
	for key, value := range fields {
		update[key] = value
	}
	return update
}

func toolText(text string) []any {
	return []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}}
}

func TestAHermesSessionShowsItsSavedConversation(t *testing.T) {
	project := t.TempDir()
	handler := hermesHandler(t, map[string]any{
		"sessionId": "hermes-tests", "cwd": project, "title": "Run the test suite", "updatedAt": "2026-03-01T10:00:00Z",
		"model": "anthropic:claude-sonnet-4-5", "contextUsed": 41000,
		"history": []any{
			textUpdate("user_message_chunk", "Run the tests and fix what fails"),
			textUpdate("agent_thought_chunk", "I should run the whole suite first."),
			toolUpdate("tool_call", "tc-1", map[string]any{"title": "terminal: npm test", "kind": "execute", "content": toolText("$ npm test")}),
			toolUpdate("tool_call_update", "tc-1", map[string]any{"kind": "execute", "status": "failed", "content": toolText("1 failing: totals rounds once")}),
			toolUpdate("tool_call", "tc-2", map[string]any{"title": "read_file: src/totals.ts", "kind": "read"}),
			toolUpdate("tool_call_update", "tc-2", map[string]any{"kind": "read", "status": "completed", "content": toolText("export function total() {}")}),
			textUpdate("agent_message_chunk", "Fixed **the rounding** in `total`."),
		},
	})

	page := getPage(t, handler, "/?session="+url.QueryEscape("hermes:hermes-tests"))
	for _, expected := range []string{
		"Run the test suite",
		"Run the tests and fix what fails",
		"I should run the whole suite first.",
		"terminal: npm test",
		"1 failing: totals rounds once",
		"read_file: src/totals.ts",
		"<strong>the rounding</strong>",
		"anthropic/claude-sonnet-4-5",
	} {
		if !strings.Contains(page, expected) {
			t.Errorf("page lacks %q", expected)
		}
	}
	if !strings.Contains(page, `data-tool-call-id="tc-1"`) {
		t.Errorf("page lacks the tool call's block")
	}
}

func postJSON(t *testing.T, handler http.Handler, target string, values url.Values) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Origin", "http://example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var payload map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	return recorder.Code, payload
}

type liveEvent struct {
	Type                  string   `json:"type"`
	ID                    string   `json:"id"`
	Method                string   `json:"method"`
	Title                 string   `json:"title"`
	Options               []string `json:"options"`
	ToolCallID            string   `json:"toolCallId"`
	ToolName              string   `json:"toolName"`
	IsError               bool     `json:"isError"`
	Message               any      `json:"message"`
	Messages              []any    `json:"messages"`
	AssistantMessageEvent struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
}

// repliedText joins the thinking and the text of the replies that ended among the events.
func repliedText(events []liveEvent) (thinking, text string) {
	for _, event := range events {
		message, _ := event.Message.(map[string]any)
		if event.Type != "message_end" || message["role"] != "assistant" {
			continue
		}
		for _, part := range message["content"].([]any) {
			part := part.(map[string]any)
			thought, _ := part["thinking"].(string)
			said, _ := part["text"].(string)
			thinking += thought
			text += said
		}
	}
	return thinking, text
}

// eventsUntil polls the session's live events from the start until one of the given type arrives.
func eventsUntil(t *testing.T, handler http.Handler, key, last string) []liveEvent {
	t.Helper()
	events, _ := eventsAfterUntil(t, handler, key, 0, last)
	return events
}

// eventsAfterUntil polls the session's live events after a cursor until one of the given type arrives, and returns
// them with the cursor after them.
func eventsAfterUntil(t *testing.T, handler http.Handler, key string, after int64, last string) ([]liveEvent, int64) {
	t.Helper()
	var events []liveEvent
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var batch struct {
			Events  []liveEvent `json:"events"`
			LastSeq int64       `json:"last_seq"`
		}
		if err := json.Unmarshal([]byte(getPage(t, handler, fmt.Sprintf("/events?after=%d&session=%s", after, url.QueryEscape(key)))), &batch); err != nil {
			t.Fatal(err)
		}
		events = batch.Events
		for _, event := range events {
			if event.Type == last {
				return events, batch.LastSeq
			}
		}
	}
	t.Fatalf("no %s event among %+v", last, events)
	return nil, 0
}

func TestAHermesSessionRunsAPromptLive(t *testing.T) {
	project := t.TempDir()
	handler := hermesHandler(t)

	code, payload := postJSON(t, handler, "/sessions/new_at_cwd", url.Values{"cwd": {project}, "agent": {"hermes"}})
	key, _ := payload["session"].(string)
	if code != http.StatusOK || !strings.HasPrefix(key, "hermes:") {
		t.Fatalf("new Hermes session = %d %v", code, payload)
	}
	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files"}}); code != http.StatusOK {
		t.Fatalf("prompt = %d %v", code, payload)
	}

	events := eventsUntil(t, handler, key, "agent_settled")
	var toolEnd *liveEvent
	types := []string{}
	for index, event := range events {
		types = append(types, event.Type)
		if event.Type == "tool_execution_end" {
			toolEnd = &events[index]
		}
	}
	if thinking, text := repliedText(events); types[0] != "agent_start" || thinking != "Planning the answer." || text != "Checking the files.Hermes says: List the files" {
		t.Fatalf("events %v streamed thinking %q and text %q", types, thinking, text)
	}
	if toolEnd == nil || toolEnd.ToolCallID != "tc-1" || toolEnd.ToolName != "terminal: ls" || toolEnd.IsError {
		t.Fatalf("tool end = %+v", toolEnd)
	}

	// After the run, the page shows the same conversation from the saved session.
	page := getPage(t, handler, "/?session="+url.QueryEscape(key))
	for _, expected := range []string{"List the files", "Planning the answer.", "Checking the files.", "terminal: ls", "README.md", "Hermes says: List the files"} {
		if !strings.Contains(page, expected) {
			t.Errorf("page after the run lacks %q", expected)
		}
	}
}

func TestStopEndsAHermesRunAndRefusesMessagesWhileItRuns(t *testing.T) {
	project := t.TempDir()
	handler := hermesHandler(t)
	_, payload := postJSON(t, handler, "/sessions/new_at_cwd", url.Values{"cwd": {project}, "agent": {"hermes"}})
	key := payload["session"].(string)
	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Wait for stop"}}); code != http.StatusOK {
		t.Fatalf("prompt = %d %v", code, payload)
	}
	eventsUntil(t, handler, key, "agent_start")

	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Also do this"}, "streaming_behavior": {"steer"}}); code == http.StatusOK || !strings.Contains(payload["error"].(string), "Hermes is working") {
		t.Fatalf("steer while Hermes runs = %d %v", code, payload)
	}
	if code, payload := postJSON(t, handler, "/abort", url.Values{"session": {key}}); code != http.StatusOK {
		t.Fatalf("abort = %d %v", code, payload)
	}
	eventsUntil(t, handler, key, "agent_settled")

	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files"}}); code != http.StatusOK {
		t.Fatalf("prompt after stop = %d %v", code, payload)
	}
}

func TestHermesAsksBeforeADangerousCommandInADialog(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hermes.log")
	t.Setenv("FAKE_HERMES_LOG", log)
	project := t.TempDir()
	handler := hermesHandler(t)
	_, payload := postJSON(t, handler, "/sessions/new_at_cwd", url.Values{"cwd": {project}, "agent": {"hermes"}})
	key := payload["session"].(string)
	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Delete the build folder"}}); code != http.StatusOK {
		t.Fatalf("prompt = %d %v", code, payload)
	}

	var question liveEvent
	for _, event := range eventsUntil(t, handler, key, "extension_ui_request") {
		if event.Type == "extension_ui_request" {
			question = event
		}
	}
	message, _ := question.Message.(string)
	if question.Method != "select" || question.Title != "recursive delete: rm -rf build" || !strings.Contains(message, "$ rm -rf build") || strings.Join(question.Options, ", ") != "Allow once, Allow for session, Deny" {
		t.Fatalf("question = %+v", question)
	}
	// A reload brings the question back.
	if page := getPage(t, handler, "/?session="+url.QueryEscape(key)); !strings.Contains(page, question.ID) {
		t.Fatalf("the page lacks the question %s", question.ID)
	}

	if code, payload := postJSON(t, handler, "/extension_ui_response", url.Values{"session": {key}, "id": {question.ID}, "value": {"Allow once"}}); code != http.StatusOK {
		t.Fatalf("answer = %d %v", code, payload)
	}
	var toolEnd *liveEvent
	events := eventsUntil(t, handler, key, "agent_settled")
	for index, event := range events {
		if event.Type == "tool_execution_end" {
			toolEnd = &events[index]
		}
	}
	if toolEnd == nil || toolEnd.ToolName != "terminal: rm -rf build" || toolEnd.IsError {
		t.Fatalf("tool end = %+v", toolEnd)
	}
	// Hermes asks about sensitive files only, like its CLI, which never asks before an edit.
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"method":"session/set_mode","params":{"modeId":"dont_ask"`) {
		t.Fatalf("Gripi did not set Hermes's edit approval mode:\n%s", contents)
	}
}

// hermesStarts reads the fake Hermes's log for how each of its processes started.
func hermesStarts(t *testing.T, log string) []map[string]any {
	t.Helper()
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var starts []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["argv"] != nil {
			starts = append(starts, record)
		}
	}
	return starts
}

func TestSavedVariablesReachHermesBeforeItsNextMessage(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hermes.log")
	t.Setenv("FAKE_HERMES_LOG", log)
	project := t.TempDir()
	handler := hermesHandler(t)
	_, payload := postJSON(t, handler, "/sessions/new_at_cwd", url.Values{"cwd": {project}, "agent": {"hermes"}})
	key := payload["session"].(string)
	// A session that Hermes hasn't saved lives only in the running Hermes, so its first message runs there.
	if code, payload := postJSON(t, handler, "/environment/variable", url.Values{"name": {"FAKE_HERMES_PROBE"}, "value": {"saved in Gripi"}}); code != http.StatusOK {
		t.Fatalf("save variable = %d %v", code, payload)
	}
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files"}})
	_, cursor := eventsAfterUntil(t, handler, key, 0, "agent_settled")
	if starts := hermesStarts(t, log); len(starts) != 1 {
		t.Fatalf("Hermes restarted with a session it hadn't saved: %v", starts)
	}

	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files again"}}); code != http.StatusOK {
		t.Fatalf("prompt after saving a variable = %d %v", code, payload)
	}
	events, _ := eventsAfterUntil(t, handler, key, cursor, "agent_settled")

	starts := hermesStarts(t, log)
	if len(starts) != 2 || starts[0]["probe"] != nil || starts[1]["probe"] != "saved in Gripi" {
		t.Fatalf("Hermes started %v", starts)
	}
	// The browser's cursor from before the restart still reads the new run.
	if events[0].Type != "agent_start" {
		t.Fatalf("events after the restart = %+v", events)
	}
	// The session reloaded in the new Hermes keeps its conversation.
	if page := getPage(t, handler, "/?session="+url.QueryEscape(key)); !strings.Contains(page, "Hermes says: List the files again") || !strings.Contains(page, "Checking the files.") {
		t.Fatal("the conversation is missing after the restart")
	}
}

func TestPagesTellAgentsApartOnlyWhenTheGatewayRunsHermes(t *testing.T) {
	project := t.TempDir()
	cfg := hermesConfig(t, map[string]any{"sessionId": "hermes-chat", "cwd": project, "title": "Hermes chat", "updatedAt": "2026-03-01T10:00:00Z"})
	piSession := filepath.Join(cfg.SessionsRoot, "pi.jsonl")
	if err := os.MkdirAll(cfg.SessionsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	records := []string{
		`{"type":"session","version":3,"id":"pi-chat","timestamp":"2026-02-01T10:00:00Z","cwd":"` + project + `"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-02-01T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"Pi chat"}]}}`,
	}
	if err := os.WriteFile(piSession, []byte(strings.Join(records, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	piAlone := cfg
	piAlone.HermesPath = ""
	handler, err := gateway.NewPiHandler(piAlone, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	page := getPage(t, handler, "/?session="+url.QueryEscape(piSession))
	// Absent markup is the requirement here: a gateway without Hermes looks as it did before.
	for _, absent := range []string{`data-agent="`, "session-agent", `name="agent"`, "All agents", "Hermes Agent"} {
		if strings.Contains(page, absent) {
			t.Errorf("the page without Hermes contains %q", absent)
		}
	}
	for _, present := range []string{`aria-label="Message to Pi"`, "Ask Pi…", `aria-label="Abort running Pi"`} {
		if !strings.Contains(page, present) {
			t.Errorf("the page without Hermes lacks %q", present)
		}
	}

	both, err := gateway.NewHandlerWithHermes(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = both.(interface{ Close(context.Context) error }).Close(context.Background()) })
	page = getPage(t, both, "/?session="+url.QueryEscape(piSession))
	for _, present := range []string{`class="session-agent" data-agent="pi"`, `class="session-agent" data-agent="hermes"`, `name="agent" aria-label="Filter sessions by agent"`, `data-new-session-agent="hermes"`, `aria-label="Message to Pi"`, `<main class="conversation-panel" data-agent="pi">`} {
		if !strings.Contains(page, present) {
			t.Errorf("the Pi session's page with Hermes lacks %q", present)
		}
	}
	page = getPage(t, both, "/?session="+url.QueryEscape("hermes:hermes-chat"))
	for _, present := range []string{`<main class="conversation-panel" data-agent="hermes">`, `class="session-header-agent" data-agent="hermes"`, `aria-label="Message to Hermes"`, "Ask Hermes…", `aria-label="Abort running Hermes"`} {
		if !strings.Contains(page, present) {
			t.Errorf("the Hermes session's page lacks %q", present)
		}
	}
	page = getPage(t, both, "/?agent=hermes")
	if !strings.Contains(page, "Hermes chat") || strings.Contains(page, `data-session-name="Pi chat"`) {
		t.Error("the agent filter does not show only Hermes's sessions")
	}
}

func TestAQuestionHermesStopsWaitingForCloses(t *testing.T) {
	handler := hermesHandler(t)
	_, payload := postJSON(t, handler, "/sessions/new_at_cwd", url.Values{"cwd": {t.TempDir()}, "agent": {"hermes"}})
	key := payload["session"].(string)
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Delete the build folder unless I am too slow"}})

	types := []string{}
	for _, event := range eventsUntil(t, handler, key, "agent_settled") {
		types = append(types, event.Type)
	}
	asked, closed, toolEnded := slices.Index(types, "extension_ui_request"), slices.Index(types, "extension_ui_reset"), slices.Index(types, "tool_execution_end")
	// The question closes as Hermes's check ends, before the tool call that it asked about.
	if asked < 0 || closed < asked || toolEnded < closed {
		t.Fatalf("events %v", types)
	}
}

func TestAHermesThatExitsIsReplacedForTheNextMessage(t *testing.T) {
	handler := hermesHandler(t)
	_, payload := postJSON(t, handler, "/sessions/new_at_cwd", url.Values{"cwd": {t.TempDir()}, "agent": {"hermes"}})
	key := payload["session"].(string)
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files"}})
	_, cursor := eventsAfterUntil(t, handler, key, 0, "agent_settled")
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Exit now"}})
	events, _ := eventsAfterUntil(t, handler, key, cursor, "agent_settled")
	for _, event := range events {
		message, _ := event.Message.(map[string]any)
		// The partial reply of a run that failed is no finished reply to tell the user about.
		if event.Type == "message_end" && message["role"] == "assistant" && message["stopReason"] != "error" || event.Type == "agent_end" && len(event.Messages) > 0 {
			t.Fatalf("the failed run ended %+v", event)
		}
	}

	// As after every run, the browser asks for the session's status, which finds Hermes gone.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status?session="+url.QueryEscape(key), nil))
	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files again"}}); code != http.StatusOK {
		t.Fatalf("prompt after Hermes exited = %d %v", code, payload)
	}
	events, _ = eventsAfterUntil(t, handler, key, 0, "agent_settled")
	if _, text := repliedText(events); !strings.HasSuffix(text, "Hermes says: List the files again") {
		t.Fatalf("the restarted Hermes streamed %q", text)
	}
}

func TestAStreamingReplyIsDeliveredOnceHoweverLateThePageAsks(t *testing.T) {
	t.Setenv("FAKE_HERMES_STEP_MS", "1")
	handler := hermesHandler(t)
	_, payload := postJSON(t, handler, "/sessions/new_at_cwd", url.Values{"cwd": {t.TempDir()}, "agent": {"hermes"}})
	key := payload["session"].(string)
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Think at length, then wait for stop"}})

	var batch struct {
		Events []map[string]any `json:"events"`
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := json.Unmarshal([]byte(getPage(t, handler, "/events?after=0&session="+url.QueryEscape(key))), &batch); err != nil {
			t.Fatal(err)
		}
		if update := batch.Events[len(batch.Events)-1]; update["gatewayPartialMessage"] != nil && strings.Contains(fmt.Sprint(update["gatewayPartialMessage"]), "piece 199") {
			break
		}
	}
	updates := 0
	for _, event := range batch.Events {
		if event["type"] == "message_update" {
			updates++
		}
	}
	// One update stands for the 200 pieces, with all of them in the reply it carries.
	if reply := fmt.Sprint(batch.Events[len(batch.Events)-1]["gatewayPartialMessage"]); updates != 1 || !strings.Contains(reply, "piece 0 piece 1 ") || !strings.Contains(reply, "piece 199") {
		t.Fatalf("a late page got %d updates: %v", updates, batch.Events[len(batch.Events)-1])
	}
	postJSON(t, handler, "/abort", url.Values{"session": {key}})
	eventsUntil(t, handler, key, "agent_settled")
}

func TestAHermesSessionReadBeforeARestartTurnsUnreadWithItsNextReply(t *testing.T) {
	project := t.TempDir()
	cfg := hermesConfig(t)
	start := func() http.Handler {
		handler, err := gateway.NewHandlerWithHermes(cfg, gripi.WebFiles)
		if err != nil {
			t.Fatal(err)
		}
		return handler
	}
	handler := start()
	_, payload := postJSON(t, handler, "/sessions/new_at_cwd", url.Values{"cwd": {project}, "agent": {"hermes"}})
	key := payload["session"].(string)
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files"}})
	eventsUntil(t, handler, key, "agent_settled")
	getPage(t, handler, "/?session="+url.QueryEscape(key))
	if err := handler.(interface{ Close(context.Context) error }).Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	handler = start()
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files again"}})
	eventsUntil(t, handler, key, "agent_settled")
	// The sidebar as a poll sees it, which marks no session read.
	if page := getPage(t, handler, "/sidebar?no_session=1"); !strings.Contains(page, `class="session recent-session unread"`) {
		t.Fatal("the new reply did not mark the session unread")
	}
}
