package server

import (
	"html/template"
	"net/http"
	"time"
)

// Worker is a worker that `gripi worker add` saved, with how this machine's connection to it is.
type Worker struct {
	Name        string `json:"name"`
	Destination string `json:"destination"`
	// State is disconnected, connecting, copying, connected, unreachable or gone.
	State string `json:"state"`
	// GoneAt is when an unreachable worker is gone.
	GoneAt time.Time `json:"gone_at,omitzero"`
}

// WorkerLocation is where a session runs while it is on a worker, or is moving to one.
type WorkerLocation struct {
	Worker string `json:"worker"`
	// State and GoneAt are as in Worker, but another session's copy being copied to the worker doesn't make it
	// copying, and State is returning while the session comes back, or until a return that failed finishes.
	State  string    `json:"state"`
	GoneAt time.Time `json:"gone_at,omitzero"`
	// Repository is the main checkout of the session's repository.
	Repository string `json:"repository"`
	// Copying is when the session started moving to the worker, which it is until it runs there.
	Copying time.Time `json:"copying,omitzero"`
	// Returning is when the session started coming back, while it does: without it, State returning is a return that
	// stopped before it finished.
	Returning time.Time `json:"returning,omitzero"`
	// Waiting names the session on this machine whose step a move or return of the session waits for.
	Waiting string       `json:"waiting,omitempty"`
	Ports   []WorkerPort `json:"ports"`
}

// Indicator is what a session's indicator shows: the worker's state, or copying while the session moves there.
func (location WorkerLocation) Indicator() string {
	if !location.Copying.IsZero() {
		return "copying"
	}
	return location.State
}

// WorkerPort is a port of a worker that this machine makes available at Local.
type WorkerPort struct {
	Port  int `json:"port"`
	Local int `json:"local"`
}

// WorkerNotice says what a session's return from a worker kept as this machine had it, though the worker changed it
// too: the worker's versions are in Aside, at their paths, and its commits under Refs, until the notice is dismissed.
type WorkerNotice struct {
	Repository string `json:"repository"`
	Worker     string `json:"worker"`
	Aside      string `json:"aside"`
	// Paths are the files, worktrees and refs that this machine kept, which Items says more about.
	Paths []string           `json:"paths"`
	Items []WorkerNoticeItem `json:"items"`
	Refs  []string           `json:"refs"`
	// Text says it all, for the user.
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

// WorkerNoticeItem is something that a return kept as this machine had it, and why, with the ref of the worker's
// commit if it kept one.
type WorkerNoticeItem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Ref    string `json:"ref,omitempty"`
}

func (app *application) registerWorkerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /workers", app.listWorkers)
	mux.HandleFunc("POST /workers/move", app.moveToWorker)
	mux.HandleFunc("POST /workers/return", app.returnFromWorker)
	mux.HandleFunc("POST /workers/forward", app.forwardPort)
	mux.HandleFunc("POST /workers/unforward", app.unforwardPort)
	mux.HandleFunc("POST /workers/dismiss_notices", app.dismissWorkerNotices)
}

// listWorkers lists the workers for the browser, with what would move to one with the session: the main checkout of
// its repository and the worktrees that its copy gets.
func (app *application) listWorkers(response http.ResponseWriter, request *http.Request) {
	key, ok := app.workerSession(response, request)
	if !ok {
		return
	}
	workers, err := app.workers.List()
	if err != nil {
		writeInternalError(response, "list the workers", err)
		return
	}
	main, worktrees, err := app.workers.Selection(key, app.currentSessionCWD(key))
	if err != nil {
		writeWorkersError(response, "find what moves with a session", err)
		return
	}
	writeJSON(response, map[string]any{"workers": workers, "repository": map[string]any{"main": main, "roots": worktrees}})
}

func (app *application) dismissWorkerNotices(response http.ResponseWriter, request *http.Request) {
	key, ok := app.workerSession(response, request)
	if !ok {
		return
	}
	if err := app.workers.DismissNotices(request.Context(), key); err != nil {
		writeInternalError(response, "dismiss the notices of a session's return", err)
		return
	}
	writeJSON(response, map[string]bool{"ok": true})
}

// sessionWorker returns where a session runs while it is on a worker, or nil, and the notices of its returns.
func (app *application) sessionWorker(path string) (*WorkerLocation, []WorkerNotice) {
	notices := []WorkerNotice{}
	if app.workers == nil || app.agentOf(path) == "hermes" {
		return nil, notices
	}
	notices = append(notices, app.workers.Notices(path)...)
	location, found := app.workers.Location(path)
	if !found {
		return nil, notices
	}
	app.workerWaitsMu.Lock()
	location.Waiting = app.workerWaits[path]
	app.workerWaitsMu.Unlock()
	return &location, notices
}

// serverIcon marks what runs on a worker.
func serverIcon() template.HTML {
	return `<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><rect x="3.5" y="4" width="17" height="6.5" rx="1.6"/><rect x="3.5" y="13.5" width="17" height="6.5" rx="1.6"/><path d="M7.5 7.25h.01M7.5 16.75h.01"/></svg>`
}
