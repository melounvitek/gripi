package hermes

import (
	"context"
	"encoding/json"
	"log"
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
	cwd  string
	// prompted is whether the gateway has sent the session a message, with which Hermes saves a new session.
	prompted bool

	mu          sync.Mutex
	transcript  transcript
	model       string
	contextUsed float64
	contextSize float64
	commands    []agent.Command
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

// requested asks the user what Hermes asks before a dangerous command or an edit of a sensitive file. A question
// outside a run that the gateway follows is declined.
func (adapter *Adapter) requested(conn *connection, id json.RawMessage, method string, params json.RawMessage) {
	if method != "session/request_permission" {
		_ = conn.respondError(id, method)
		return
	}
	var request permissionRequest
	if json.Unmarshal(params, &request) == nil {
		adapter.sessionsMu.Lock()
		session := adapter.sessions[request.SessionID]
		adapter.sessionsMu.Unlock()
		if session != nil {
			session.mu.Lock()
			client := session.client
			session.mu.Unlock()
			if client != nil && client.ask(id, request) {
				return
			}
		}
	}
	_ = conn.respond(id, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
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

// load has the running Hermes load the session, unless it has already, which replays the session's transcript. It
// returns the session with the connection to the Hermes that loaded it.
func (adapter *Adapter) load(ctx context.Context, listed listedSession) (*session, *connection, error) {
	conn, err := adapter.connected(ctx)
	if err != nil {
		return nil, nil, err
	}
	session := adapter.session(listed.SessionID)
	session.loadMu.Lock()
	defer session.loadMu.Unlock()
	session.mu.Lock()
	loaded := session.conn == conn
	if !loaded {
		session.transcript = transcript{}
	}
	session.mu.Unlock()
	if loaded {
		return session, conn, nil
	}
	var answer struct {
		Models struct {
			CurrentModelID string `json:"currentModelId"`
		} `json:"models"`
	}
	params := map[string]any{"sessionId": listed.SessionID, "cwd": listed.CWD, "mcpServers": []any{}}
	if err := conn.request(ctx, "session/load", params, &answer); err != nil {
		return nil, nil, err
	}
	setEditMode(ctx, conn, listed.SessionID)
	session.mu.Lock()
	session.transcript.commit()
	session.model, session.conn, session.cwd = answer.Models.CurrentModelID, conn, listed.CWD
	session.mu.Unlock()
	return session, conn, nil
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

// setEditMode lets Hermes edit files without asking, except sensitive ones, which comes closest to its CLI: the CLI
// never asks before an edit, and ACP asks before every edit by default. Hermes forgets the mode with the process.
func setEditMode(ctx context.Context, conn *connection, id string) {
	if err := conn.request(ctx, "session/set_mode", map[string]any{"sessionId": id, "modeId": "dont_ask"}, nil); err != nil {
		log.Printf("set Hermes's edit approval mode: %v", err)
	}
}
