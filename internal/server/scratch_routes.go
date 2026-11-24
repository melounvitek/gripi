package server

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/melounvitek/gripi/internal/agent"
)

// scratchSessions are the scratch sessions of the gateway's users, one each at most: sessions that Pi keeps only in
// memory, which run without a project folder and are never listed.
type scratchSessions struct {
	// changing lets one scratch session start or end at a time.
	changing sync.Mutex
	folders  *scratchFolders
	mu       sync.Mutex
	// byOwner holds each user's scratch session by the workspace that started it, "" in single-user mode.
	byOwner map[string]*scratchSession
	// ended holds the keys of the scratch sessions that ended or are ending. They stay scratch keys, so that a request
	// that was already under way can't start a saved session under one.
	ended map[string]bool
}

type scratchSession struct {
	key string
	dir string
}

var errScratchGone = &agent.RequestError{Message: "This scratch session has ended."}

// scratchRefusedRoutes need a saved session or keep something about it. Discard ends a scratch session instead of
// delete. Pi itself would offer more of them for a session that it keeps only in memory.
var scratchRefusedRoutes = map[string]bool{
	"POST /sessions/new": true, "POST /sessions/rename": true, "POST /sessions/delete": true, "GET /sessions/fork_messages": true,
	"GET /sessions/tree_entries": true, "POST /sessions/tree": true, "POST /sessions/tree/label": true, "POST /sessions/fork": true,
	"POST /sessions/clone": true, "POST /sessions/export": true, "POST /sessions/takeover": true, "POST /sessions/pin": true,
	"GET /sessions/tags": true, "POST /sessions/tags": true, "POST /sessions/mark_read": true, "POST /workers/move": true,
	"POST /workers/return": true, "POST /workers/forward": true, "POST /workers/unforward": true,
}

// scratchRefusedCommands are the slash commands of the refused routes.
var scratchRefusedCommands = []string{"name", "export", "fork", "tree", "clone", "new"}

func (app *application) refuseScratchRoutes(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if _, pattern := mux.Handler(request); scratchRefusedRoutes[pattern] && app.scratch.contains(request.FormValue("session")) {
			http.Error(response, "A scratch session doesn’t offer this.", http.StatusConflict)
			return
		}
		mux.ServeHTTP(response, request)
	})
}

func (scratch *scratchSessions) session(owner string) *scratchSession {
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	return scratch.byOwner[owner]
}

// key returns the key of the owner's scratch session, or "" when it has none or it is ending.
func (scratch *scratchSessions) key(owner string) string {
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	if session := scratch.byOwner[owner]; session != nil && !scratch.ended[session.key] {
		return session.key
	}
	return ""
}

func (scratch *scratchSessions) contains(key string) bool {
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	if scratch.ended[key] {
		return true
	}
	for _, session := range scratch.byOwner {
		if session.key == key {
			return true
		}
	}
	return false
}

// scratchGone tells whether the Pi of a scratch session has ended, such as after it crashed, which lost the session.
func (app *application) scratchGone(key string) bool {
	client := app.rpcClients.Client(key)
	exiter, ok := client.(agent.Exiter)
	return client == nil || ok && exiter.Exited()
}

func (app *application) registerScratchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /scratch/new", app.newScratchSession)
	mux.HandleFunc("POST /scratch/discard", app.discardScratchSession)
}

// ownsScratch tells whether path is the scratch session of the browser that asks. gripi commands and workers never
// reach a scratch session.
func (app *application) ownsScratch(request *http.Request, path string) bool {
	_, fromWorker := workerOf(request)
	return path != "" && !commandRequest(request) && !fromWorker && app.scratch.key(currentWorkspaceID(request)) == path
}

// newScratchSession opens the user's scratch session, and starts one when there is none.
func (app *application) newScratchSession(response http.ResponseWriter, request *http.Request) {
	if app.scratch.folders == nil {
		http.Error(response, "Scratch sessions are unavailable on this gateway.", http.StatusServiceUnavailable)
		return
	}
	app.scratch.changing.Lock()
	defer app.scratch.changing.Unlock()
	owner := currentWorkspaceID(request)
	if current := app.scratch.session(owner); current != nil {
		if !app.scratchGone(current.key) && app.scratch.key(owner) == current.key {
			app.showScratchSession(response, request, current.key)
			return
		}
		if err := app.endScratchSession(owner, current); err != nil {
			writeInternalError(response, "end scratch session", err)
			return
		}
	}
	dir, err := app.scratch.folders.create()
	if err != nil {
		writeInternalError(response, "create scratch session folder", err)
		return
	}
	key, err := app.startScratchSession(request, owner, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		app.writeActionRPCError(response, err)
		return
	}
	app.showScratchSession(response, request, key)
}

func (app *application) startScratchSession(request *http.Request, owner, dir string) (string, error) {
	variables, err := app.piEnvironment(app.environmentUser(request))
	if err != nil {
		return "", err
	}
	// Pi writes long tool output to the OS's temporary folder, which the scratch session's folder holds instead.
	variables = append(variables, "TMPDIR="+filepath.Join(dir, "tmp"))
	start := func(cwd string) (agent.Client, error) {
		return app.adapter.StartInCWD(cwd, "", agent.StartOptions{Scratch: true, Env: variables, Diagnostics: app.rpcDiagnostics})
	}
	return agent.StartNewSession(request.Context(), filepath.Join(dir, "work"), start, app.sessionStore, app.rpcClients, app.pendingSessions, func(key string) (func() error, error) {
		// The key is a scratch key before its client is registered, so that nothing starts a saved session under it.
		app.scratch.mu.Lock()
		app.scratch.byOwner[owner] = &scratchSession{key: key, dir: dir}
		app.scratch.mu.Unlock()
		return func() error {
			app.scratch.mu.Lock()
			delete(app.scratch.byOwner, owner)
			app.scratch.ended[key] = true
			app.scratch.mu.Unlock()
			return nil
		}, nil
	})
}

func (app *application) discardScratchSession(response http.ResponseWriter, request *http.Request) {
	app.scratch.changing.Lock()
	defer app.scratch.changing.Unlock()
	owner := currentWorkspaceID(request)
	if current := app.scratch.session(owner); current != nil {
		if err := app.endScratchSession(owner, current); err != nil {
			writeInternalError(response, "end scratch session", err)
			return
		}
	}
	writeJSON(response, map[string]bool{"ok": true})
}

// endScratchSession stops the session's Pi, even while it works, and removes its folder. The caller holds
// scratch.changing. When it fails, the session stays ending, and ending it again can finish.
func (app *application) endScratchSession(owner string, session *scratchSession) error {
	app.scratch.mu.Lock()
	app.scratch.ended[session.key] = true
	app.scratch.mu.Unlock()
	if _, err := app.rpcClients.CloseClient(session.key); err != nil {
		return err
	}
	if err := os.RemoveAll(session.dir); err != nil {
		return err
	}
	app.pendingSessions.Forget(session.key)
	app.scratch.mu.Lock()
	if app.scratch.byOwner[owner] == session {
		delete(app.scratch.byOwner, owner)
	}
	app.scratch.mu.Unlock()
	return nil
}

// endScratchSessions ends every scratch session, as the gateway stops.
func (app *application) endScratchSessions() error {
	app.scratch.changing.Lock()
	defer app.scratch.changing.Unlock()
	app.scratch.mu.Lock()
	sessions := make(map[string]*scratchSession, len(app.scratch.byOwner))
	for owner, session := range app.scratch.byOwner {
		sessions[owner] = session
	}
	app.scratch.mu.Unlock()
	var err error
	for owner, session := range sessions {
		err = errors.Join(err, app.endScratchSession(owner, session))
	}
	if app.scratch.folders != nil {
		err = errors.Join(err, app.scratch.folders.Close())
	}
	return err
}

// showScratchSession sends the browser to the scratch session's page, which the scratch overlay shows.
func (app *application) showScratchSession(response http.ResponseWriter, request *http.Request, key string) {
	target := "/?" + url.Values{"session": {key}, "session_only": {"1"}}.Encode()
	if wantsJSON(request) {
		writeJSON(response, map[string]string{"session": key, "redirect": target})
		return
	}
	http.Redirect(response, request, target, http.StatusSeeOther)
}
