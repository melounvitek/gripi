package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
)

// Workers moves repositories, with all their worktrees and sessions, between this machine and workers.
type Workers interface {
	// Move runs the sessions of the repository that holds folder on the named worker from now on. Before it copies
	// anything, it calls stop with the repository's test for a folder, and stop ends the Pi of every session there.
	Move(ctx context.Context, folder, worker string, stop func(inRepository func(folder string) bool) error) error
	// Return runs the sessions of the repository that holds folder on this machine again, calling stop as Move does.
	Return(ctx context.Context, folder string, stop func(inRepository func(folder string) bool) error) error
}

// SetWorkers lets gripi commands move repositories to workers and back. Call it before serving.
func (handler *Handler) SetWorkers(workers Workers) {
	handler.app.workers = workers
}

// errSessionWorking refuses a move while a session of the repository works, which would lose the step it is on.
var errSessionWorking = errors.New("working")

func (app *application) moveToWorker(response http.ResponseWriter, request *http.Request) {
	app.moveRepository(response, request, func(folder string, stop func(func(string) bool) error) error {
		return app.workers.Move(request.Context(), folder, request.FormValue("worker"), stop)
	})
}

func (app *application) returnFromWorker(response http.ResponseWriter, request *http.Request) {
	app.moveRepository(response, request, func(folder string, stop func(func(string) bool) error) error {
		return app.workers.Return(request.Context(), folder, stop)
	})
}

func (app *application) moveRepository(response http.ResponseWriter, request *http.Request, move func(folder string, stop func(func(string) bool) error) error) {
	if !parseForm(response, request) {
		return
	}
	if app.workers == nil {
		writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": "this gateway runs no sessions on workers"})
		return
	}
	folder := request.FormValue("folder")
	if !filepath.IsAbs(folder) {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "folder must be an absolute path"})
		return
	}
	var stopped []string
	err := move(folder, func(inRepository func(string) bool) error {
		var err error
		stopped, err = app.stopSessionsIn(request, inRepository)
		return err
	})
	// The sessions' files may have come back from the worker, which the guard must not take for another program's.
	for _, key := range stopped {
		app.guard.Forget(key)
	}
	switch {
	case errors.Is(err, errSessionWorking):
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		logInternalError("move a repository between this machine and a worker", err)
		writeJSONStatus(response, http.StatusBadGateway, map[string]string{"error": err.Error()})
	default:
		writeJSON(response, map[string]string{"folder": folder})
	}
}

// stopSessionsIn ends the Pi of every Pi session in a repository, unless one of them is working, and returns their keys.
func (app *application) stopSessionsIn(request *http.Request, inRepository func(string) bool) ([]string, error) {
	saved, err := app.sessionStore.Sessions()
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, session := range saved {
		if inRepository(session.CWD) {
			keys = append(keys, session.Path)
		}
	}
	for _, pending := range app.pendingSessions.Entries() {
		if inRepository(pending.CWD) && !slices.Contains(keys, pending.Path) {
			keys = append(keys, pending.Path)
		}
	}
	keys = slices.DeleteFunc(keys, func(key string) bool { return app.agentOf(key) == "hermes" })
	for _, key := range keys {
		if app.ownsSession != nil && !app.ownsSession(request, key) {
			return nil, errors.New("the repository has sessions of other users, which only they may move")
		}
		if app.rpcClients.Busy(key) {
			return nil, fmt.Errorf("session %s is %w; wait until it finishes or stop it", key, errSessionWorking)
		}
	}
	for _, key := range keys {
		err := app.retireRPCClient(request.Context(), key, func() (bool, error) { return app.rpcClients.CloseClientIfIdle(key) })
		if err == nil && app.rpcClients.Registered(key) {
			err = fmt.Errorf("session %s is %w; wait until it finishes or stop it", key, errSessionWorking)
		}
		if err != nil {
			return keys, err
		}
	}
	return keys, nil
}
