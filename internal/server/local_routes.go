package server

import (
	"net/http"
	"net/url"
	"time"

	"github.com/melounvitek/gripi/internal/sessions"
)

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
	UpdatedAt time.Time `json:"updated_at"`
	LastReply string    `json:"last_reply"`
}

func (app *application) registerLocalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sessions", app.localSessions)
	mux.HandleFunc("POST /prompt", app.prompt)
	mux.HandleFunc("POST /sessions/pin", app.pinSession)
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
			Tags: tags, UpdatedAt: session.ConversationActivityAt, LastReply: session.LatestAssistantResponsePreview,
		})
	}
	writeJSON(response, map[string]any{"sessions": result})
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
