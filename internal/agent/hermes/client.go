package hermes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

// client serves one session of the running Hermes. Its live events take the shapes of Pi's, which the browser renders.
type client struct {
	adapter   *Adapter
	session   *session
	conn      *connection
	key       string
	observe   func(agent.Client, agent.Lifecycle)
	closeOnce sync.Once

	mu        sync.Mutex
	events    events
	running   bool
	busySince *time.Time
	settledAt *time.Time
	// reply is the assistant message that streams, in Pi's format, and replyID the ACP message it shows.
	reply   *piMessage
	replyID string
	// tools are the calls that run, in the order they started, with their latest event.
	tools []runningTool
	// questions are Hermes's permission requests that wait for the user, in the order Hermes asked them.
	questions []question
	lastReply agent.Reply
}

var (
	_ agent.Client    = (*client)(nil)
	_ agent.Responder = (*client)(nil)
)

// permissionRequest is what Hermes asks before it runs a dangerous command or edits a sensitive file.
type permissionRequest struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		Title   string          `json:"title"`
		Content json.RawMessage `json:"content"`
	} `json:"toolCall"`
	Options []permissionOption `json:"options"`
}

type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
}

// question is a permission request shown as a dialog of the kind Pi's extensions open, which the browser renders.
type question struct {
	id      string
	rpcID   json.RawMessage
	request permissionRequest
	event   json.RawMessage
}

type runningTool struct {
	id, name string
	event    json.RawMessage
}

// piMessage and piPart are a message in Pi's format.
type piMessage struct {
	Role       string   `json:"role"`
	Content    []piPart `json:"content"`
	StopReason string   `json:"stopReason,omitempty"`
	Timestamp  int64    `json:"timestamp"`
}

type piPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

func (message *piMessage) text() string {
	var parts []string
	for _, part := range message.Content {
		if part.Type == "text" {
			parts = append(parts, part.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

func (adapter *Adapter) newClient(session *session, conn *connection, options agent.StartOptions) *client {
	client := &client{adapter: adapter, session: session, conn: conn, key: sessionKey(session.id), observe: options.Observe}
	session.mu.Lock()
	session.client = client
	session.mu.Unlock()
	adapter.mu.Lock()
	adapter.clients++
	adapter.mu.Unlock()
	return client
}

// Close lets go of the session, which stays loaded in Hermes.
func (client *client) Close() error {
	client.closeOnce.Do(func() {
		client.session.mu.Lock()
		if client.session.client == client {
			client.session.client = nil
		}
		client.session.mu.Unlock()
		client.adapter.mu.Lock()
		client.adapter.clients--
		client.adapter.usedAt = time.Now()
		client.adapter.mu.Unlock()
	})
	return nil
}

func (client *client) Busy() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.running
}

func (client *client) BusySince() *time.Time {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.busySince
}

func (client *client) SettledAt() *time.Time {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.settledAt
}

func (client *client) AgentRunning() bool { return client.Busy() }

func (client *client) Compacting() bool { return false }

func (client *client) EventSequence() int64 {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.events.sequence
}

func (client *client) EventReplayCursor() int64 {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.events.floor
}

func (client *client) EventsAfter(after int64) agent.EventBatch {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.events.after(after)
}

func (client *client) ContinueEventsAfter(sequence int64) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.events.continueAfter(sequence)
}

func (client *client) LiveSnapshot() agent.LiveSnapshot {
	client.mu.Lock()
	defer client.mu.Unlock()
	snapshot := agent.LiveSnapshot{
		EventSequence: client.events.sequence, EventReplayCursor: client.events.floor,
		Busy: client.running, BusySince: client.busySince, AgentBusySince: client.busySince, AgentRunning: client.running,
	}
	for _, tool := range client.tools {
		snapshot.ActiveTools = append(snapshot.ActiveTools, agent.ActiveTool{ID: tool.id, Data: tool.event})
	}
	if client.reply != nil {
		snapshot.ActiveAssistant = encodeEvent(map[string]any{"type": "message_update", "message": client.reply})
	}
	if len(client.questions) > 0 {
		dialogs := make([]json.RawMessage, len(client.questions))
		for index, question := range client.questions {
			dialogs[index] = question.event
		}
		snapshot.PendingRequests = len(dialogs)
		snapshot.ExtensionUI, _ = json.Marshal(map[string]any{"pending_dialogs": dialogs, "statuses": []any{}, "widgets": []any{}, "title": nil})
	}
	return snapshot
}

// ask shows a permission request of the run, and reports false when no run is there to ask about.
func (client *client) ask(rpcID json.RawMessage, request permissionRequest) bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	if !client.running {
		return false
	}
	names := make([]string, len(request.Options))
	for index, option := range request.Options {
		names[index] = option.Name
	}
	id := "hermes-permission-" + string(rpcID)
	event := client.emit(map[string]any{"type": "extension_ui_request", "id": id, "method": "select", "title": request.ToolCall.Title, "message": contentText(request.ToolCall.Content), "options": names})
	client.questions = append(client.questions, question{id: id, rpcID: rpcID, request: request, event: event})
	return true
}

// Answer passes the user's choice on to Hermes.
func (client *client) Answer(_ context.Context, id string, answer agent.Answer) error {
	client.mu.Lock()
	index := slices.IndexFunc(client.questions, func(question question) bool { return question.id == id })
	if index < 0 {
		client.mu.Unlock()
		return &agent.RequestError{Message: "Hermes no longer waits for this answer."}
	}
	asked := client.questions[index]
	outcome := map[string]any{"outcome": "cancelled"}
	if !answer.Cancelled {
		option := -1
		if answer.Value != nil {
			option = slices.IndexFunc(asked.request.Options, func(option permissionOption) bool { return option.Name == *answer.Value })
		}
		if option < 0 {
			client.mu.Unlock()
			return &agent.RequestError{Message: "Hermes offered no such answer."}
		}
		outcome = map[string]any{"outcome": "selected", "optionId": asked.request.Options[option].OptionID}
	}
	client.questions = slices.Delete(client.questions, index, index+1)
	client.mu.Unlock()
	return client.conn.respond(asked.rpcID, map[string]any{"outcome": outcome})
}

func (client *client) State(context.Context) (agent.State, error) {
	state := agent.State{Key: client.key, ID: client.session.id, Streaming: client.Busy()}
	if listed, ok := client.adapter.find(client.key); ok {
		state.Name = listed.Title
	}
	return state, nil
}

func (client *client) LiveStatus(context.Context) (agent.LiveStatus, error) {
	status := client.session.status()
	live := agent.LiveStatus{Provider: status.Provider, ModelID: status.ModelID}
	if status.HasContextTokens || status.HasContextLimit {
		live.Context = &agent.ContextUsage{Tokens: status.ContextTokens, HasTokens: status.HasContextTokens, Window: status.ContextLimit, HasWindow: status.HasContextLimit, Percent: status.ContextPercent}
	}
	return live, nil
}

func (client *client) Commands(context.Context) ([]agent.Command, error) {
	client.session.mu.Lock()
	defer client.session.mu.Unlock()
	return append([]agent.Command(nil), client.session.commands...), nil
}

// Prompt starts a run. While one runs, Hermes would take the prompt as a correction of the run, which Gripi doesn't
// offer yet: the user stops the run first.
func (client *client) Prompt(_ context.Context, prompt agent.Prompt) (agent.PromptResult, error) {
	if prompt.Images != nil {
		return agent.PromptResult{}, &agent.RequestError{Message: "Gripi cannot send images to Hermes yet."}
	}
	client.mu.Lock()
	if client.running {
		client.mu.Unlock()
		return agent.PromptResult{}, &agent.RequestError{Message: "Hermes is working. Stop it before you send another message."}
	}
	now := time.Now()
	client.running, client.busySince, client.lastReply = true, &now, agent.Reply{}
	client.emit(map[string]any{"type": "agent_start"})
	user := &piMessage{Role: "user", Content: []piPart{{Type: "text", Text: prompt.Text}}, Timestamp: now.UnixMilli()}
	client.emit(map[string]any{"type": "message_start", "message": user})
	client.emit(map[string]any{"type": "message_end", "message": user})
	client.emit(map[string]any{"type": "turn_start"})
	client.mu.Unlock()

	client.session.mu.Lock()
	client.session.transcript.addUser(prompt.Text)
	client.session.mu.Unlock()
	client.notify(agent.RunStarted, agent.Reply{})
	go client.run(prompt.Text)
	// Hermes saves a new session with its first message.
	client.adapter.relist()
	return agent.PromptResult{Disposition: "started"}, nil
}

func (client *client) run(text string) {
	var answer struct {
		StopReason string `json:"stopReason"`
	}
	params := map[string]any{"sessionId": client.session.id, "prompt": []any{map[string]any{"type": "text", "text": text}}}
	err := client.conn.request(context.Background(), "session/prompt", params, &answer)
	client.adapter.relist()
	client.finish(answer.StopReason, err)
}

// Cancel asks Hermes to stop the run, which then ends as cancelled.
func (client *client) Cancel(context.Context) error {
	return client.conn.send("session/cancel", map[string]any{"sessionId": client.session.id})
}

func (client *client) NewSession(context.Context) (agent.Branch, error) {
	return agent.Branch{}, &agent.RequestError{Message: "Hermes cannot start a new session in place of this one."}
}

// apply turns a session update of the run into live events.
func (client *client) apply(value update) {
	client.mu.Lock()
	if !client.running {
		client.mu.Unlock()
		return
	}
	var ended *agent.Reply
	switch value.SessionUpdate {
	case "agent_thought_chunk", "agent_message_chunk":
		text := chunkText(value)
		if text == "" {
			break
		}
		if client.reply == nil || value.MessageID != client.replyID {
			ended = client.endReply("stop")
			client.reply = &piMessage{Role: "assistant", Content: []piPart{}, Timestamp: time.Now().UnixMilli()}
			client.replyID = value.MessageID
			client.emit(map[string]any{"type": "message_start", "message": client.reply})
		}
		kind := "text"
		if value.SessionUpdate == "agent_thought_chunk" {
			kind = "thinking"
		}
		content := client.reply.Content
		if len(content) == 0 || content[len(content)-1].Type != kind {
			client.reply.Content = append(content, piPart{Type: kind})
			client.emitDelta(kind+"_start", "")
		}
		last := &client.reply.Content[len(client.reply.Content)-1]
		if kind == "text" {
			last.Text += text
		} else {
			last.Thinking += text
		}
		client.emitDelta(kind+"_delta", text)
	case "tool_call":
		ended = client.endReply("toolUse")
		event := client.emit(map[string]any{"type": "tool_execution_start", "toolCallId": value.ToolCallID, "toolName": value.Title, "args": map[string]any{}})
		client.tools = append(client.tools, runningTool{id: value.ToolCallID, name: value.Title, event: event})
	case "tool_call_update":
		for index, tool := range client.tools {
			if tool.id != value.ToolCallID {
				continue
			}
			result := map[string]any{"content": []any{map[string]any{"type": "text", "text": contentText(value.Content)}}}
			switch value.Status {
			case "completed", "failed":
				client.emit(map[string]any{"type": "tool_execution_end", "toolCallId": tool.id, "toolName": tool.name, "result": result, "isError": value.Status == "failed"})
				client.tools = append(client.tools[:index], client.tools[index+1:]...)
			default:
				client.tools[index].event = client.emit(map[string]any{"type": "tool_execution_update", "toolCallId": tool.id, "toolName": tool.name, "partialResult": result})
			}
			break
		}
	}
	client.mu.Unlock()
	if ended != nil {
		client.notify(agent.ReplyEnded, *ended)
	}
}

// finish ends the run once Hermes answers its prompt.
func (client *client) finish(stopReason string, err error) {
	client.mu.Lock()
	stop := "stop"
	if stopReason == "cancelled" {
		stop = "aborted"
	}
	ended := client.endReply(stop)
	for _, tool := range client.tools {
		client.emit(map[string]any{"type": "tool_execution_end", "toolCallId": tool.id, "toolName": tool.name, "result": map[string]any{"content": []any{}}, "isError": true})
	}
	client.tools = nil
	// Hermes has stopped waiting for what it asked during the run.
	for _, asked := range client.questions {
		_ = client.conn.respond(asked.rpcID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
	}
	if len(client.questions) > 0 {
		client.questions = nil
		client.emit(map[string]any{"type": "extension_ui_reset"})
	}
	client.emit(map[string]any{"type": "turn_end"})
	client.emit(map[string]any{"type": "agent_end", "messages": []any{}})
	now := time.Now()
	client.running, client.busySince, client.settledAt = false, nil, &now
	last := client.lastReply
	settled := map[string]any{"type": "agent_settled"}
	if err != nil {
		settled["errorMessage"] = err.Error()
	}
	client.emit(settled)
	client.mu.Unlock()

	client.session.mu.Lock()
	client.session.transcript.commit()
	if err != nil {
		client.session.transcript.add(&agent.Message{Role: "error", Text: err.Error(), Error: true})
	}
	client.session.mu.Unlock()
	if ended != nil {
		client.notify(agent.ReplyEnded, *ended)
	}
	client.notify(agent.RunEnded, last)
	client.notify(agent.RunSettled, last)
}

// endReply ends the reply that streams, and returns it when it has text. The caller holds client.mu.
func (client *client) endReply(stopReason string) *agent.Reply {
	if client.reply == nil {
		return nil
	}
	message := client.reply
	message.StopReason = stopReason
	client.reply, client.replyID = nil, ""
	client.emit(map[string]any{"type": "message_end", "message": message})
	text := message.text()
	if text == "" {
		return nil
	}
	reply := agent.Reply{Text: text, Final: stopReason == "stop", ID: randomID()}
	if reply.Final {
		client.lastReply = reply
	}
	client.session.countReply()
	return &reply
}

func (client *client) notify(step agent.LifecycleStep, reply agent.Reply) {
	if client.observe != nil {
		client.observe(client, agent.Lifecycle{Step: step, Reply: reply})
	}
}

// emit adds an event in Pi's format and returns it. The caller holds client.mu.
func (client *client) emit(event map[string]any) json.RawMessage {
	return client.events.add(encodeEvent(event))
}

func (client *client) emitDelta(kind, delta string) {
	event := map[string]any{"type": kind, "contentIndex": len(client.reply.Content) - 1}
	if strings.HasSuffix(kind, "_delta") {
		event["delta"] = delta
	}
	client.emit(map[string]any{"type": "message_update", "assistantMessageEvent": event})
}

func encodeEvent(event map[string]any) json.RawMessage {
	event["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	encoded, _ := json.Marshal(event)
	return encoded
}

func randomID() string {
	value := make([]byte, 8)
	_, _ = rand.Read(value)
	return hex.EncodeToString(value)
}

// events is a client's buffer of live events, which pages poll after a cursor.
type events struct {
	sequence int64
	// floor is the sequence after which every event is kept.
	floor int64
	list  []numberedEvent
	bytes int
}

type numberedEvent struct {
	sequence int64
	event    json.RawMessage
}

const (
	eventLimit      = 10000
	eventBytesLimit = 16 << 20
)

func (events *events) add(event json.RawMessage) json.RawMessage {
	events.sequence++
	events.list = append(events.list, numberedEvent{events.sequence, event})
	events.bytes += len(event)
	for len(events.list) > eventLimit || events.bytes > eventBytesLimit {
		events.bytes -= len(events.list[0].event)
		events.floor = events.list[0].sequence
		events.list = events.list[1:]
	}
	return event
}

func (events *events) after(after int64) agent.EventBatch {
	batch := agent.EventBatch{Events: []json.RawMessage{}, LastSeq: events.sequence, Missed: after < events.floor || after > events.sequence}
	if batch.Missed {
		return batch
	}
	for _, entry := range events.list {
		if entry.sequence > after {
			batch.Events = append(batch.Events, entry.event)
		}
	}
	return batch
}

// continueAfter numbers the events after the last one of the client that this one replaces.
func (events *events) continueAfter(sequence int64) {
	events.sequence += sequence
	events.floor += sequence
	for index := range events.list {
		events.list[index].sequence += sequence
	}
}
