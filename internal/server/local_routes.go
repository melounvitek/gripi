package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/agent"
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
	// Agent names the session's agent when the gateway runs several.
	Agent string `json:"agent,omitempty"`
}

// LocalMessage is how gripi commands see one message of a conversation.
type LocalMessage struct {
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	Timestamp Timestamp `json:"timestamp"`
}

type commandRequestKey struct{}

// commandRequests marks the requests of gripi commands, which see the sessions that pages leave out.
func commandRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), commandRequestKey{}, true)))
	})
}

func commandRequest(request *http.Request) bool {
	return request.Context().Value(commandRequestKey{}) != nil
}

type commandUserKey struct{}

// commandUser lets the commands of a multi-user gateway act for the user whose token they carry.
func (app *application) commandUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		token, bearer := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
		if !bearer {
			token = ""
		}
		workspaceID, err := app.commandWorkspace(token)
		if err != nil {
			writeInternalError(response, "check the user of a gripi command", err)
			return
		}
		if workspaceID == "" {
			writeJSONStatus(response, http.StatusForbidden, map[string]string{"error": "the gateway is in multi-user mode; set GRIPI_USER_TOKEN to your user token"})
			return
		}
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), commandUserKey{}, workspaceID)))
	})
}

// commandWorkspace finds the approved workspace of a user token or of a token from commandToken.
func (app *application) commandWorkspace(token string) (string, error) {
	approved, err := app.workspaceStore.ApprovedWorkspaces()
	if token == "" {
		return "", err
	}
	userWorkspace := access.WorkspaceID(app.workspaceSecret, token)
	for _, workspaceID := range approved {
		if workspaceID == userWorkspace || token == app.commandToken(workspaceID) {
			return workspaceID, nil
		}
	}
	return "", err
}

// commandToken lets the Pi of a user's sessions run gripi commands for that user. Unlike the workspace ID,
// it does not open the user's browser session, so it is safe in an environment that Pi may show to the model.
func (app *application) commandToken(workspaceID string) string {
	return access.WorkspaceID(app.workspaceSecret, "command:"+workspaceID)
}

func (app *application) registerLocalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sessions", app.localSessions)
	mux.HandleFunc("GET /conversation", app.localConversation)
	mux.HandleFunc("POST /prompt", app.prompt)
	mux.HandleFunc("POST /abort", app.abortSession)
	mux.HandleFunc("POST /sessions/new_at_cwd", app.newSessionAtCWD)
	mux.HandleFunc("POST /sessions/rename", app.renameSession)
	mux.HandleFunc("POST /sessions/pin", app.pinSession)
	mux.HandleFunc("POST /sessions/tags", app.sessionTags)
	mux.HandleFunc("POST /sessions/delete", app.deleteSession)
	mux.HandleFunc("POST /sessions/claim", app.localClaim)
	mux.HandleFunc("GET /tags", app.tags)
	mux.HandleFunc("POST /sudo", app.sudo)
}

// localClaim gives the user a session that has no owner, such as one from Pi CLI. It takes the full ID or the path,
// because the user does not see the session to pick it by a prefix.
func (app *application) localClaim(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	reference := request.FormValue("session")
	session, found := app.sessionStore.Session(reference)
	if !found {
		all, err := app.sessionStore.Sessions()
		if err != nil {
			writeInternalError(response, "list sessions to claim one", err)
			return
		}
		var matches []*agent.Session
		for _, candidate := range all {
			if candidate.ID == reference {
				matches = append(matches, candidate)
			}
		}
		switch len(matches) {
		case 0:
			writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no session has the full ID or path %q", reference)})
			return
		case 1:
			session = matches[0]
		default:
			writeJSONStatus(response, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%d sessions have the ID %q; claim one by its file path", len(matches), reference)})
			return
		}
	}
	if app.claimSession != nil {
		_, err := app.claimSession(request, session.Path)
		if errors.Is(err, access.ErrSessionOwnedByAnotherWorkspace) {
			writeJSONStatus(response, http.StatusConflict, map[string]string{"error": "another user owns the session"})
			return
		}
		if err != nil {
			writeInternalError(response, "claim a session for a gripi command", err)
			return
		}
	}
	writeJSON(response, map[string]string{"session": session.Path})
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
			Agent: session.Agent,
		})
	}
	writeJSON(response, map[string]any{"sessions": result})
}

// localConversation answers with the most recent messages of a session, as many as the browser first shows.
func (app *application) localConversation(response http.ResponseWriter, request *http.Request) {
	path, owned := app.requireOwnedSession(response, request, request.URL.Query().Get("session"))
	if !owned {
		return
	}
	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	window, err := app.sessionStore.Window(path, "", false, nil, nil)
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
	switch {
	// A dialog keeps Pi busy until someone answers, so it must not read as working.
	case live.PendingRequests > 0:
		return "waiting"
	// Prompts queued during a compaction are delivered just after it ends.
	case app.rpcClients.DeferringCompactionPrompts(path):
		return "compacting"
	case live.Busy:
		return "working"
	}
	if blocked := app.guard.KnownBlocked(path); blocked != nil {
		if blocked.Mode == agent.SyncConflict {
			return "conflict"
		}
		return "external"
	}
	return "idle"
}
