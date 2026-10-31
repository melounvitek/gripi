package server

import (
	"html/template"
	"net/http"
	"path/filepath"
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

// WorkerLocation is where the sessions of a folder run while its repository is on a worker, or is being copied to one.
type WorkerLocation struct {
	Worker string `json:"worker"`
	// State and GoneAt are as in Worker, but another repository being copied to the worker doesn't make it copying.
	State  string    `json:"state"`
	GoneAt time.Time `json:"gone_at,omitzero"`
	// Repository is the main folder of the folder's repository.
	Repository string `json:"repository"`
	// Copying is when the repository started being copied to the worker, which it is until its sessions run there.
	Copying time.Time    `json:"copying,omitzero"`
	Ports   []WorkerPort `json:"ports"`
}

// Indicator is what a session's indicator shows: the worker's state, or copying while the repository is copied there.
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

// WorkerNotice says that files of a repository on a worker changed on this machine, which keeps a read-only copy:
// before the worker's versions replaced them, this machine's were saved in Aside, at their paths.
type WorkerNotice struct {
	Repository string    `json:"repository"`
	Aside      string    `json:"aside"`
	Paths      []string  `json:"paths"`
	Time       time.Time `json:"time"`
}

func (app *application) registerWorkerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /workers", app.listWorkers)
	mux.HandleFunc("POST /workers/move", app.moveToWorker)
	mux.HandleFunc("POST /workers/return", app.returnFromWorker)
	mux.HandleFunc("POST /workers/forward", app.forwardPort)
	mux.HandleFunc("POST /workers/unforward", app.unforwardPort)
	mux.HandleFunc("POST /workers/dismiss_notices", app.dismissWorkerNotices)
}

// listWorkers lists the workers for the browser, with what would move to one with the session's repository: its main
// folder, how many worktrees it has, and how many sessions.
func (app *application) listWorkers(response http.ResponseWriter, request *http.Request) {
	folder, ok := app.workerFolder(response, request)
	if !ok {
		return
	}
	workers, err := app.workers.List()
	if err != nil {
		writeInternalError(response, "list the workers", err)
		return
	}
	main, worktrees, err := app.workers.Repository(folder)
	if err != nil {
		writeInternalError(response, "find the repository of a session", err)
		return
	}
	saved, err := app.sessionStore.Sessions()
	if err != nil {
		writeInternalError(response, "list the sessions", err)
		return
	}
	sessions := 0
	for _, session := range saved {
		for _, root := range append([]string{main}, worktrees...) {
			if relative, err := filepath.Rel(root, session.CWD); err == nil && filepath.IsLocal(relative) {
				sessions++
				break
			}
		}
	}
	writeJSON(response, map[string]any{"workers": workers, "repository": map[string]any{"main": main, "worktrees": len(worktrees), "sessions": sessions}})
}

func (app *application) dismissWorkerNotices(response http.ResponseWriter, request *http.Request) {
	folder, ok := app.workerFolder(response, request)
	if !ok {
		return
	}
	app.workers.DismissNotices(folder)
	writeJSON(response, map[string]bool{"ok": true})
}

// sessionWorker returns where the sessions of a session's folder run while its repository is on a worker, or nil, and
// the notices about the repository's files that changed on this machine.
func (app *application) sessionWorker(path string) (*WorkerLocation, []WorkerNotice) {
	notices := []WorkerNotice{}
	if app.workers == nil || app.agentOf(path) == "hermes" {
		return nil, notices
	}
	folder := app.currentSessionCWD(path)
	notices = append(notices, app.workers.Notices(folder)...)
	if location, found := app.workers.Location(folder); found {
		return &location, notices
	}
	return nil, notices
}

// serverIcon marks what runs on a worker.
func serverIcon() template.HTML {
	return `<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><rect x="3.5" y="4" width="17" height="6.5" rx="1.6"/><rect x="3.5" y="13.5" width="17" height="6.5" rx="1.6"/><path d="M7.5 7.25h.01M7.5 16.75h.01"/></svg>`
}
