package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
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
	// ActivityAt is when the session file last changed, which tool calls do too, and never before UpdatedAt.
	ActivityAt Timestamp `json:"activity_at"`
	LastReply  string    `json:"last_reply"`
	// Agent names the session's agent when the gateway runs several.
	Agent string `json:"agent,omitempty"`
}

// LocalMessage is how gripi commands see one message of a conversation.
type LocalMessage struct {
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	Timestamp Timestamp `json:"timestamp"`
	// Images are those stored in the session itself, such as a tool's screenshots.
	Images []LocalImage `json:"images,omitempty"`
}

// LocalImage locates an image that GET /session_images/{entry_id}/{index} returns.
type LocalImage struct {
	EntryID  string `json:"entry_id"`
	Index    int    `json:"index"`
	MIMEType string `json:"mime_type"`
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
	mux.HandleFunc("GET /session_images/{entry_id}/{file}", app.sessionImage)
	mux.HandleFunc("POST /search", app.localSearch)
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
	mux.HandleFunc("POST /workers/move", app.moveToWorker)
	mux.HandleFunc("POST /workers/return", app.returnFromWorker)
	mux.HandleFunc("POST /workers/forward", app.forwardPort)
	mux.HandleFunc("POST /workers/unforward", app.unforwardPort)
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
		if only != "" && session.Path != only || !reachesSession(request, session.Path) {
			continue
		}
		tags := view.SessionTags[session.Path]
		if tags == nil {
			tags = []string{}
		}
		// A session that Pi has not saved yet has no file, and a Hermes session is not a file.
		activity := session.ConversationActivityAt
		if info, err := os.Stat(session.Path); err == nil && info.ModTime().After(activity) {
			activity = info.ModTime()
		}
		result = append(result, LocalSession{
			ID: session.ID, Path: session.Path, Name: session.DisplayName, CWD: session.CWD,
			State: app.localSessionState(session.Path), Unread: view.Unread[session.Path], Pinned: view.Pinned[session.Path],
			Tags: tags, UpdatedAt: Timestamp{session.ConversationActivityAt}, ActivityAt: Timestamp{activity}, LastReply: session.LatestAssistantResponsePreview,
			Agent: session.Agent,
		})
	}
	payload := map[string]any{"sessions": result}
	// A worker's gripi reaches only the sessions there, which it says when it finds none.
	if placement, fromWorker := workerOf(request); fromWorker {
		payload["worker"] = placement.Name()
	}
	writeJSON(response, payload)
}

// localConversation answers with the most recent messages of a session, as many as the browser first shows, or with
// those before the position in before. start is the position of the first, which the next older page ends before,
// and leaf the end of the branch, which the next page names so that it stays on the same branch.
func (app *application) localConversation(response http.ResponseWriter, request *http.Request) {
	path, owned := app.requireOwnedSession(response, request, request.URL.Query().Get("session"))
	if !owned {
		return
	}
	var before *int
	if value := request.URL.Query().Get("before"); value != "" {
		position, err := strconv.Atoi(value)
		if err != nil || position < 0 {
			writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "before must be a position in the conversation"})
			return
		}
		before = &position
	}
	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	leaf := request.URL.Query().Get("leaf")
	window, err := app.sessionStore.Window(path, leaf, leaf != "", before, nil)
	// A new session has no file, and so no messages, until Pi has replied once.
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeInternalError(response, "read conversation for gripi commands", err)
		return
	}
	messages := []LocalMessage{}
	// calls finds the listed tool call of each ID.
	calls := map[string]int{}
	for _, message := range window.Messages {
		local := LocalMessage{Role: message.Role, Text: message.Text, Timestamp: Timestamp{message.Timestamp}}
		for _, image := range message.Images {
			// An image that the user attached in the browser is a file of its own, which commands can read already.
			if image.EntryID != "" {
				local.Images = append(local.Images, LocalImage{EntryID: image.EntryID, Index: image.Index, MIMEType: image.MIMEType})
			}
		}
		switch {
		case message.Thinking:
			continue
		// A result stored apart from its tool call would list the call twice, so its images, such as a
		// screenshot, go with the call. Only a subagent's call is not listed, so its result stands in for it.
		case message.Role == "toolResult" && message.ToolName != "subagent":
			if call, found := calls[message.ToolCallID]; found {
				messages[call].Images = append(messages[call].Images, local.Images...)
				continue
			}
			// The call is on an older page.
			if len(local.Images) == 0 {
				continue
			}
			local.Role, local.Text = "tool", message.ToolName
		case message.Compaction:
			local.Text = message.Summary
		// Tool calls and shell commands: the summary says what ran, the text holds the output.
		case message.Compact:
			local.Role, local.Text = "tool", message.Summary
		}
		if message.ToolCallID != "" && message.Role != "toolResult" {
			calls[message.ToolCallID] = len(messages)
		}
		messages = append(messages, local)
	}
	writeJSON(response, map[string]any{"messages": messages, "start": window.StartIndex, "leaf": window.TreeLeafID})
}

// LocalSearchResult is what gripi search finds in one session.
type LocalSearchResult struct {
	Session string `json:"session"`
	Count   int    `json:"count"`
	// Matches are the latest few, with a snippet of the message as their text.
	Matches []LocalMessage `json:"matches"`
}

// searchMatchesPerSession is how many matches each session shows.
const searchMatchesPerSession = 3

// localSearch finds text in the sessions given by their paths, which the command has filtered as gripi list does.
// It reads the files as they are, without keeping an index of their text, so that searching costs no memory between
// searches.
func (app *application) localSearch(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	query := strings.TrimSpace(request.FormValue("q"))
	if query == "" {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "the search text is empty"})
		return
	}
	searcher, ok := app.sessionStore.(agent.Searcher)
	if !ok {
		writeJSON(response, map[string]any{"results": []LocalSearchResult{}})
		return
	}
	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	var paths []string
	for _, path := range request.Form["session"] {
		if app.validOwnedSessionPath(request, path) {
			paths = append(paths, path)
		}
	}
	results := make([]LocalSearchResult, len(paths))
	failures := make([]error, len(paths))
	next := make(chan int)
	var searches sync.WaitGroup
	// A few files at once keep the disk busy without taking every core from the sessions that run.
	for range min(4, len(paths)) {
		searches.Go(func() {
			for index := range next {
				found, err := searcher.Search(paths[index], query, request.Form["role"], searchMatchesPerSession)
				// A session that Pi has not saved yet has nothing to find.
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				result := LocalSearchResult{Session: paths[index], Count: found.Count, Matches: []LocalMessage{}}
				for _, match := range found.Matches {
					result.Matches = append(result.Matches, LocalMessage{Role: match.Role, Text: match.Snippet, Timestamp: Timestamp{match.Timestamp}})
				}
				results[index], failures[index] = result, err
			}
		})
	}
	for index := range paths {
		if request.Context().Err() != nil {
			break
		}
		next <- index
	}
	close(next)
	searches.Wait()
	if err := errors.Join(failures...); err != nil {
		writeInternalError(response, "search sessions for gripi search", err)
		return
	}
	found := []LocalSearchResult{}
	for _, result := range results {
		if result.Count > 0 {
			found = append(found, result)
		}
	}
	writeJSON(response, map[string]any{"results": found})
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
