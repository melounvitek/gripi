package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/melounvitek/gripi/internal/agent"
)

// Workers moves repositories, with all their worktrees and sessions, between this machine and workers, and says where
// they are.
type Workers interface {
	// Move runs the sessions of the repository that holds folder on the named worker from now on. Before it copies
	// anything, it calls stop with a test for the folder of a session, which the repository's folders pass, and stop
	// ends the Pi of every session there.
	Move(ctx context.Context, folder, worker string, stop func(inRepository func(folder string) bool) error) error
	// Return runs the sessions of the repository that holds folder on this machine again, calling stop as Move does.
	Return(ctx context.Context, folder string, stop func(inRepository func(folder string) bool) error) error
	// Forward makes a port of the worker that runs the repository of folder available at a port of this machine,
	// which it returns.
	Forward(ctx context.Context, folder string, port int) (local int, err error)
	// Unforward stops forwarding a port of the worker that runs the repository of folder.
	Unforward(folder string, port int) error
	// List returns the workers.
	List() ([]Worker, error)
	// Location reports where the sessions of folder run while its repository is on a worker or is being copied to one.
	Location(folder string) (WorkerLocation, bool)
	// Repository returns the main folder and the worktrees of the repository that holds folder, which move together.
	Repository(folder string) (main string, worktrees []string, err error)
	// Notices returns the notices about files of the repository that holds folder that changed on this machine, until
	// DismissNotices.
	Notices(folder string) []WorkerNotice
	DismissNotices(folder string)
}

// refused reports whether an error of Workers refuses what was asked, such as moving a repository that is on a worker
// already, rather than fails at it, which it says with a Refused method.
func refused(err error) bool {
	var refusal interface{ Refused() bool }
	return errors.As(err, &refusal) && refusal.Refused()
}

// SetWorkers lets gripi commands and the browser move repositories to workers and back. Call it before serving.
func (handler *Handler) SetWorkers(workers Workers) {
	handler.app.workers = workers
}

// Guard returns the guard of the sessions, which the gateway's own writes to them from elsewhere, such as of the
// copies of sessions on workers, must tell.
func (handler *Handler) Guard() agent.Guard {
	return handler.app.guard
}

// StopSessions ends the Pi of every Pi session of any user whose folder passes inFolder, unless one of them is working,
// for the workers, whose copy of a session folder that a repository on a worker takes would replace what such a session
// saves there.
func (handler *Handler) StopSessions(inFolder func(folder string) bool) error {
	_, err := handler.app.stopSessionsIn(context.Background(), nil, inFolder, nil)
	return err
}

// errSessionWorking refuses to stop a session while it works, which would lose the step it is on, for a move or for
// the workers' StopSessions.
var errSessionWorking error = sessionWorking{}

type sessionWorking struct{}

func (sessionWorking) Error() string { return "working" }

// Refused makes the error a refusal for the workers, such as of a session that would start on a worker, which the
// gateway answers with a conflict.
func (sessionWorking) Refused() bool { return true }

var errOtherUsersSessions = errors.New("the repository has sessions of other users, which only they may move")

func (app *application) moveToWorker(response http.ResponseWriter, request *http.Request) {
	app.moveRepository(response, request, func(folder string, stop func(func(string) bool) error) error {
		return app.workers.Move(movingContext(request), folder, request.FormValue("worker"), stop)
	})
}

func (app *application) returnFromWorker(response http.ResponseWriter, request *http.Request) {
	app.moveRepository(response, request, func(folder string, stop func(func(string) bool) error) error {
		return app.workers.Return(movingContext(request), folder, stop)
	})
}

// movingContext is the context of a move: a gripi command's ends with the command, but a page that reloads or closes
// while a repository is copied must not stop the copy, whose progress every page shows.
func movingContext(request *http.Request) context.Context {
	if commandRequest(request) {
		return request.Context()
	}
	return context.WithoutCancel(request.Context())
}

// workerFolder returns the folder of a request about the repository that holds it: the folder that a gripi command
// names, or the folder of a session of the browser's user.
func (app *application) workerFolder(response http.ResponseWriter, request *http.Request) (string, bool) {
	if !parseForm(response, request) {
		return "", false
	}
	if app.workers == nil {
		writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": "this gateway runs no sessions on workers"})
		return "", false
	}
	folder := request.FormValue("folder")
	if !commandRequest(request) {
		session := request.FormValue("session")
		if !app.validOwnedSessionPath(request, session) || !app.knownOrPendingSession(request, session) {
			writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": "no such session"})
			return "", false
		}
		folder = app.currentSessionCWD(session)
	}
	if !filepath.IsAbs(folder) {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "folder must be an absolute path"})
		return "", false
	}
	return folder, true
}

// writeWorkersError answers with an error of Workers: a refusal is a conflict, with the reason.
func writeWorkersError(response http.ResponseWriter, action string, err error) {
	if refused(err) {
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	logInternalError(action, err)
	writeJSONStatus(response, http.StatusBadGateway, map[string]string{"error": err.Error()})
}

// holdMoving holds the folders of a repository for its move or return, unless a move or return holds one of them, a
// folder in one of them, or a folder that one of them is in, which holdMoving then returns.
func (app *application) holdMoving(folders []string) (release func(), held string) {
	app.movingMu.Lock()
	defer app.movingMu.Unlock()
	for _, folder := range folders {
		for _, other := range app.movingFolders {
			if folderWithin(folder, other) || folderWithin(other, folder) {
				return nil, other
			}
		}
	}
	app.movingFolders = append(app.movingFolders, folders...)
	return func() {
		app.movingMu.Lock()
		defer app.movingMu.Unlock()
		for _, folder := range folders {
			i := slices.Index(app.movingFolders, folder)
			app.movingFolders = slices.Delete(app.movingFolders, i, i+1)
		}
	}, ""
}

func folderWithin(path, folder string) bool {
	return path == folder || strings.HasPrefix(path, folder+string(filepath.Separator))
}

func (app *application) moveRepository(response http.ResponseWriter, request *http.Request, move func(folder string, stop func(func(string) bool) error) error) {
	folder, ok := app.workerFolder(response, request)
	if !ok {
		return
	}
	// Workers refuse to start a session while its repository moves, so another move or return that started before the
	// new sessions that this one stops have started again would end them. What is on a worker moves as it was placed
	// there, which can be a folder outside Git with Git repositories in it, and a repository's worktrees can be outside it.
	repository := folder
	if location, found := app.workers.Location(folder); found {
		repository = location.Repository
	}
	folders := []string{repository}
	if main, worktrees, err := app.workers.Repository(repository); err == nil {
		folders = append([]string{main}, worktrees...)
	}
	release, held := app.holdMoving(folders)
	if held != "" {
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": "the repository of " + held + " is moving already"})
		return
	}
	defer release()
	var stopped []string
	unsaved := map[string]func(context.Context) error{}
	var owns func(key string) bool
	if app.ownsSession != nil {
		owns = func(key string) bool { return app.ownsSession(request, key) }
	}
	err := move(folder, func(inRepository func(string) bool) error {
		var err error
		stopped, err = app.stopSessionsIn(request.Context(), owns, inRepository, unsaved)
		return err
	})
	// The sessions' files may have come back from the worker, which the guard must not take for another program's.
	for _, key := range stopped {
		app.guard.Forget(key)
	}
	// The Pi of new sessions that Pi has not saved yet starts again where the repository runs now.
	for key, restart := range unsaved {
		if restartErr := restart(movingContext(request)); restartErr != nil {
			err = errors.Join(err, fmt.Errorf("new session %s could not start again: %w", key, restartErr))
		}
	}
	switch {
	case errors.Is(err, errSessionWorking) || errors.Is(err, errOtherUsersSessions):
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		writeWorkersError(response, "move a repository between this machine and a worker", err)
	default:
		writeJSON(response, map[string]string{"folder": folder})
	}
}

// workerPort returns the port of a request to forward one, or answers that it has none.
func workerPort(response http.ResponseWriter, request *http.Request) (int, bool) {
	port, err := strconv.Atoi(request.FormValue("port"))
	if err != nil || port < 1 || port > 65535 {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "port must be a number from 1 to 65535"})
		return 0, false
	}
	return port, true
}

func (app *application) forwardPort(response http.ResponseWriter, request *http.Request) {
	folder, ok := app.workerFolder(response, request)
	if !ok {
		return
	}
	port, ok := workerPort(response, request)
	if !ok {
		return
	}
	local, err := app.workers.Forward(request.Context(), folder, port)
	if err != nil {
		writeWorkersError(response, "forward a port of a worker", err)
		return
	}
	writeJSON(response, map[string]int{"port": port, "local": local})
}

func (app *application) unforwardPort(response http.ResponseWriter, request *http.Request) {
	folder, ok := app.workerFolder(response, request)
	if !ok {
		return
	}
	port, ok := workerPort(response, request)
	if !ok {
		return
	}
	if err := app.workers.Unforward(folder, port); err != nil {
		writeWorkersError(response, "stop forwarding a port of a worker", err)
		return
	}
	writeJSON(response, map[string]int{"port": port})
}

// stopSessionsIn ends the Pi of every Pi session in a repository, unless one of them is working or, with owns, is not
// the user's, and returns their keys. With unsaved, a new session that Pi has not saved yet, which exists only in its
// Pi, keeps its key, and unsaved gets the function that starts its Pi again.
func (app *application) stopSessionsIn(ctx context.Context, owns func(key string) bool, inRepository func(string) bool, unsaved map[string]func(context.Context) error) ([]string, error) {
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
		if owns != nil && !owns(key) {
			return nil, errOtherUsersSessions
		}
		if app.rpcClients.Busy(key) {
			return nil, fmt.Errorf("session %s is %w; wait until it finishes or stop it", key, errSessionWorking)
		}
	}
	for _, key := range keys {
		var err error
		if _, pending := app.pendingSessions.CWD(key); unsaved != nil && pending && !app.sessionStore.Exists(key) {
			var restart func(context.Context) error
			if restart, err = agent.StopUnsavedSession(ctx, key, app.rpcClients, app.pendingSessions); restart != nil {
				unsaved[key] = restart
			}
		} else {
			err = app.retireRPCClient(ctx, key, func() (bool, error) { return app.rpcClients.CloseClientIfIdle(key) })
		}
		if err == nil && app.rpcClients.Registered(key) {
			err = fmt.Errorf("session %s is %w; wait until it finishes or stop it", key, errSessionWorking)
		}
		if err != nil {
			return keys, err
		}
	}
	return keys, nil
}
