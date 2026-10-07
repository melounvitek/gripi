package server

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/melounvitek/gripi/internal/sessions"
)

// Timestamp is how gripi commands see a time: in UTC with milliseconds,
// so that every one has the same width and they sort as text.
type Timestamp struct{ time.Time }

func (timestamp Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(timestamp.UTC().Format(`"2006-01-02T15:04:05.000Z"`)), nil
}

// LocalSession is how gripi commands see a session.
type LocalSession struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	CWD       string    `json:"cwd"`
	State     string    `json:"state"`
	Unread    bool      `json:"unread"`
	Pinned    bool      `json:"pinned"`
	Tags      []string  `json:"tags"`
	UpdatedAt Timestamp `json:"updated_at"`
	LastReply string    `json:"last_reply"`
}

// LocalMessage is how gripi commands see one message of a conversation.
type LocalMessage struct {
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	Timestamp Timestamp `json:"timestamp"`
}

func (app *application) registerLocalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sessions", app.localSessions)
	mux.HandleFunc("GET /conversation", app.localConversation)
	mux.HandleFunc("POST /prompt", app.prompt)
	mux.HandleFunc("POST /abort", app.abortSession)
	mux.HandleFunc("POST /sessions/new_at_cwd", app.newSessionAtCWD)
	mux.HandleFunc("POST /sessions/pin", app.pinSession)
	mux.HandleFunc("POST /sessions/tags", app.sessionTags)
	mux.HandleFunc("POST /sessions/delete", app.deleteSession)
	mux.HandleFunc("GET /tags", app.tags)
}

func (app *application) localSessions(response http.ResponseWriter, request *http.Request) {
	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	// Observe sessions exactly as a sidebar poll does, which marks none of them read.
	sidebar := request.Clone(request.Context())
	sidebar.URL = &url.URL{Path: "/sidebar", RawQuery: "no_session=1"}
	view, err := app.preparePage(sidebar, false)
	if err != nil {
		writeInternalError(response, "list sessions for gripi commands", err)
		return
	}
	only := request.URL.Query().Get("session")
	result := []LocalSession{}
	for _, session := range view.Sessions {
		if only != "" && session.Path != only {
			continue
		}
		tags := view.SessionTags[session.Path]
		if tags == nil {
			tags = []string{}
		}
		result = append(result, LocalSession{
			ID: session.ID, Path: session.Path, Name: session.DisplayName, CWD: session.CWD,
			State: app.localSessionState(session.Path), Unread: view.Unread[session.Path], Pinned: view.Pinned[session.Path],
			Tags: tags, UpdatedAt: Timestamp{session.ConversationActivityAt}, LastReply: session.LatestAssistantResponsePreview,
		})
	}
	writeJSON(response, map[string]any{"sessions": result})
}

// localConversation answers with the most recent messages of a session, as many as the browser first shows.
func (app *application) localConversation(response http.ResponseWriter, request *http.Request) {
	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	window, err := store.Window(request.URL.Query().Get("session"), "", false, nil, nil)
	// A new session has no file, and so no messages, until Pi has replied once.
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeInternalError(response, "read conversation for gripi commands", err)
		return
	}
	messages := []LocalMessage{}
	for _, message := range window.Messages {
		local := LocalMessage{Role: message.Role, Text: message.Text, Timestamp: Timestamp{message.Timestamp}}
		switch {
		case message.Thinking:
			continue
		// A result stored apart from its tool call would list the call twice.
		// Only a subagent's call is not listed, so its result stands in for it.
		case message.Role == "toolResult" && message.ToolName != "subagent":
			continue
		case message.Compaction:
			local.Text = message.Summary
		// Tool calls and shell commands: the summary says what ran, the text holds the output.
		case message.Compact:
			local.Role, local.Text = "tool", message.Summary
		}
		messages = append(messages, local)
	}
	writeJSON(response, map[string]any{"messages": messages})
}

func (app *application) localSessionState(path string) string {
	live := app.rpcClients.LiveSnapshot(path)
	dialogs, _ := live.ExtensionUI["pending_dialogs"].([]map[string]any)
	switch {
	// A dialog keeps Pi busy until someone answers, so it must not read as working.
	case len(dialogs) > 0:
		return "waiting"
	// Prompts queued during a compaction are delivered just after it ends.
	case app.rpcClients.DeferringCompactionPrompts(path):
		return "compacting"
	case live.Busy:
		return "working"
	}
	if blocked := app.synchronizer.KnownBlocked(path); blocked != nil {
		if blocked.Mode == sessions.SyncConflict {
			return "conflict"
		}
		return "external"
	}
	return "idle"
}
