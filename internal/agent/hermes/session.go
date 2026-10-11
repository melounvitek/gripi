package hermes

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/melounvitek/gripi/internal/agent"
)

// session is what the adapter follows of a session that the running Hermes has loaded.
type session struct {
	id     string
	loadMu sync.Mutex
	// conn is the Hermes that loaded the session. The transcript follows its updates.
	conn *connection

	mu          sync.Mutex
	transcript  transcript
	model       string
	contextUsed float64
	contextSize float64
	commands    []agent.Command
	// replies counts the replies that ended in this gateway, which is all a page compares to mark the session read.
	replies int
	// client serves the session while the gateway runs it, and turns its updates into live events.
	client *client
}

func (adapter *Adapter) session(id string) *session {
	adapter.sessionsMu.Lock()
	defer adapter.sessionsMu.Unlock()
	if adapter.sessions == nil {
		adapter.sessions = make(map[string]*session)
	}
	if adapter.sessions[id] == nil {
		adapter.sessions[id] = &session{id: id}
	}
	return adapter.sessions[id]
}

// notified follows the updates of Hermes's sessions.
func (adapter *Adapter) notified(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var notification struct {
		SessionID string `json:"sessionId"`
		Update    update `json:"update"`
	}
	if json.Unmarshal(params, &notification) != nil || notification.SessionID == "" {
		return
	}
	adapter.session(notification.SessionID).apply(notification.Update)
}

func (session *session) apply(value update) {
	session.mu.Lock()
	switch value.SessionUpdate {
	case "usage_update":
		session.contextUsed, session.contextSize = value.Used, value.Size
	case "available_commands_update":
		session.commands = session.commands[:0]
		for _, command := range value.AvailableCommands {
			session.commands = append(session.commands, agent.Command{Name: command.Name, Description: command.Description, Group: commandGroup})
		}
	default:
		session.transcript.apply(value)
	}
	client := session.client
	session.mu.Unlock()
	if client != nil {
		client.apply(value)
	}
}

func (session *session) countReply() {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.replies++
}

// load has the running Hermes load the session, unless it has already, which replays the session's transcript.
func (adapter *Adapter) load(ctx context.Context, listed listedSession) (*session, error) {
	conn, err := adapter.connected(ctx)
	if err != nil {
		return nil, err
	}
	session := adapter.session(listed.SessionID)
	session.loadMu.Lock()
	defer session.loadMu.Unlock()
	if session.conn == conn {
		return session, nil
	}
	session.mu.Lock()
	session.transcript = transcript{}
	session.mu.Unlock()
	var answer struct {
		Models struct {
			CurrentModelID string `json:"currentModelId"`
		} `json:"models"`
	}
	params := map[string]any{"sessionId": listed.SessionID, "cwd": listed.CWD, "mcpServers": []any{}}
	if err := conn.request(ctx, "session/load", params, &answer); err != nil {
		return nil, err
	}
	session.mu.Lock()
	session.transcript.commit()
	session.model = answer.Models.CurrentModelID
	session.mu.Unlock()
	session.conn = conn
	return session, nil
}

// messages returns copies of the transcript's messages, which later updates leave alone.
func (session *session) messages() []*agent.Message {
	session.mu.Lock()
	defer session.mu.Unlock()
	result := make([]*agent.Message, len(session.transcript.messages))
	for index, message := range session.transcript.messages {
		copied := *message
		result[index] = &copied
	}
	return result
}

func (session *session) status() agent.Status {
	session.mu.Lock()
	defer session.mu.Unlock()
	// Hermes names a model "provider:model".
	provider, model, found := strings.Cut(session.model, ":")
	if !found {
		provider, model = "", session.model
	}
	status := agent.Status{Provider: provider, ModelID: model, ContextTokens: session.contextUsed, HasContextTokens: session.contextUsed > 0, ContextLimit: session.contextSize, HasContextLimit: session.contextSize > 0}
	if status.HasContextTokens && status.HasContextLimit {
		status.ContextPercent = session.contextUsed / session.contextSize * 100
	}
	return status
}

// windowMessages is how many messages a page of a conversation holds at most.
const windowMessages = 150

// window pages messages like Pi's store: the newest before the cursor-th message, or the oldest from the after-th on.
func window(messages []*agent.Message, cursor, after *int, status agent.Status) agent.Window {
	total := len(messages)
	end := total
	if cursor != nil {
		end = min(max(*cursor, 0), total)
	}
	start := max(end-windowMessages, 0)
	if after != nil {
		start = min(max(*after, 0), end)
		end = min(start+windowMessages, end)
	}
	return agent.Window{Messages: messages[start:end], StartIndex: start, EndIndex: end, TotalMessageCount: total, Status: status}
}
