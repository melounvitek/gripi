package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

// Workers moves single sessions between this machine and workers, each on a private copy of its repository there,
// and says where they are.
type Workers interface {
	// Move runs a session, which started in cwd, on the named worker from now on. Once it knows the session's
	// repository, whose folders inRepository passes, it calls prepare, which stops the session's own Pi, holds the
	// new prompts of the repository's other sessions on this machine until release, and returns once none of them
	// works.
	Move(ctx context.Context, key, cwd, worker string, prepare func(ctx context.Context, inRepository func(folder string) bool) (release func(), err error)) error
	// Return runs a session on this machine again, with what it changed on its worker, calling prepare as Move does.
	Return(ctx context.Context, key string, prepare func(ctx context.Context, inRepository func(folder string) bool) (release func(), err error)) error
	// Forward makes a port of the worker that runs a session available at a port of this machine, which it returns.
	Forward(ctx context.Context, key string, port int) (local int, err error)
	// Unforward stops forwarding a port of the worker that runs a session.
	Unforward(key string, port int) error
	// List returns the workers.
	List() ([]Worker, error)
	// Location reports where a session runs while it is on a worker or is moving to one.
	Location(key string) (WorkerLocation, bool)
	// Placed returns the folders that the sessions on workers run in, by the sessions' keys.
	Placed() map[string]string
	// Selection returns what would move with a session in cwd: the main checkout of its repository and the worktrees
	// that the session's copy gets, or why it can't move.
	Selection(key, cwd string) (repository string, worktrees []string, err error)
	// Notices returns the notices of a session's returns that kept changes aside, until DismissNotices.
	Notices(key string) []WorkerNotice
	DismissNotices(ctx context.Context, key string) error
}

// refused reports whether an error of Workers refuses what was asked, such as moving a session that is on a worker
// already, rather than fails at it, which it says with a Refused method.
func refused(err error) bool {
	var refusal interface{ Refused() bool }
	return errors.As(err, &refusal) && refusal.Refused()
}

// SetWorkers lets gripi commands and the browser move sessions to workers and back. Call it before serving.
func (handler *Handler) SetWorkers(workers Workers) {
	handler.app.workers = workers
	// A new session that Pi had not saved when it moved exists only on its worker, so it stays a new one.
	for key, cwd := range workers.Placed() {
		if !handler.app.sessionStore.Exists(key) {
			handler.app.pendingSessions.Remember(key, cwd, "")
		}
	}
}

// Guard returns the guard of the sessions, which the gateway's own writes to them from elsewhere, such as of the
// copies of sessions on workers, must tell.
func (handler *Handler) Guard() agent.Guard {
	return handler.app.guard
}

// errSessionWorking refuses to stop a session while it works, which would lose the step it is on.
var errSessionWorking error = sessionWorking{}

type sessionWorking struct{}

func (sessionWorking) Error() string { return "working" }

// Refused makes the error a refusal for the workers, such as of a session that would start on a worker, which the
// gateway answers with a conflict.
func (sessionWorking) Refused() bool { return true }

// moveWaitTimeout bounds how long a move or return waits for the other sessions of the session's repository on this
// machine to finish what they run.
var moveWaitTimeout = 3 * time.Minute

func (app *application) moveToWorker(response http.ResponseWriter, request *http.Request) {
	key, ok := app.workerSession(response, request)
	if !ok {
		return
	}
	app.moveSession(response, request, key, func(ctx context.Context, prepare func(context.Context, func(string) bool) (func(), error)) error {
		return app.workers.Move(ctx, key, app.currentSessionCWD(key), request.FormValue("worker"), prepare)
	})
}

func (app *application) returnFromWorker(response http.ResponseWriter, request *http.Request) {
	key, ok := app.workerSession(response, request)
	if !ok {
		return
	}
	app.moveSession(response, request, key, func(ctx context.Context, prepare func(context.Context, func(string) bool) (func(), error)) error {
		return app.workers.Return(ctx, key, prepare)
	})
}

// movingContext is the context of a move: a gripi command's ends with the command, but a page that reloads or closes
// while a session moves must not stop the move, whose progress every page shows.
func movingContext(request *http.Request) context.Context {
	if commandRequest(request) {
		return request.Context()
	}
	return context.WithoutCancel(request.Context())
}

// workerSession returns the session of a request about where it runs, a session of the user's.
func (app *application) workerSession(response http.ResponseWriter, request *http.Request) (string, bool) {
	if !parseForm(response, request) {
		return "", false
	}
	if app.workers == nil {
		writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": "this gateway runs no sessions on workers"})
		return "", false
	}
	key := request.FormValue("session")
	if !app.validOwnedSessionPath(request, key) || !app.knownOrPendingSession(request, key) || app.agentOf(key) == "hermes" {
		writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": "no such session"})
		return "", false
	}
	canonical, err := app.canonicalRPCSessionPath(request, key)
	if err != nil {
		writeInternalError(response, "find a session", err)
		return "", false
	}
	return canonical, true
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

// moveSession runs a move or return of a session with a prepare that holds the session's own prompts until the move
// has ended, and starts the session's Pi again afterwards where it runs then when Pi hasn't saved the session yet.
func (app *application) moveSession(response http.ResponseWriter, request *http.Request, key string, move func(context.Context, func(context.Context, func(string) bool) (func(), error)) error) {
	ctx := movingContext(request)
	var restart func(context.Context) error
	var releaseSession func()
	stopped := false
	err := move(ctx, func(ctx context.Context, inRepository func(string) bool) (func(), error) {
		release, err := app.prepareMove(ctx, key, inRepository, &releaseSession, &restart)
		stopped = err == nil
		return release, err
	})
	if stopped {
		// The session's file may have come back from the worker, which the guard must not take for another program's.
		app.guard.Forget(key)
	}
	if restart != nil {
		if restartErr := restart(ctx); restartErr != nil {
			err = errors.Join(err, fmt.Errorf("new session %s could not start again: %w", key, restartErr))
			app.workerWaitsMu.Lock()
			if app.failedRestarts == nil {
				app.failedRestarts = map[string]func(context.Context) error{}
			}
			app.failedRestarts[key] = restart
			app.workerWaitsMu.Unlock()
		}
	}
	if releaseSession != nil {
		releaseSession()
	}
	if err != nil {
		writeWorkersError(response, "move a session between this machine and a worker", err)
		return
	}
	writeJSON(response, map[string]string{"session": key})
}

// prepareMove holds the new prompts of a session that moves or returns, and stops its Pi unless it works; and holds
// those of the other sessions on this machine whose folders inRepository passes, also of those that start meanwhile,
// until they are idle. It gives the session's hold to releaseSession, and with a new session that Pi has not saved
// yet, which exists only in its Pi, the function that starts its Pi again to restart. It returns what lets the other
// sessions go.
func (app *application) prepareMove(ctx context.Context, key string, inRepository func(string) bool, releaseSession *func(), restart *func(context.Context) error) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, moveWaitTimeout)
	defer cancel()
	defer app.setWorkerWait(key, "")
	var err error
	if *releaseSession, err = app.hold(ctx, key); err != nil {
		return nil, err
	}
	if app.sessionWorking(key) {
		return nil, fmt.Errorf("session %s is %w; wait until it finishes or stop it", key, errSessionWorking)
	}
	// Sessions on workers don't change this machine's folders, but those moving there do until their own move stops
	// them.
	holds := func(path string) bool {
		if path == key || !inRepository(app.currentSessionCWD(path)) {
			return false
		}
		location, onWorker := app.workers.Location(path)
		return !onWorker || !location.Copying.IsZero()
	}
	release := app.promptAdmissions.gate(holds)
	// A session can take a prompt before it reports the run that the prompt starts, so it must stay idle a moment.
	for idleSince := time.Now(); time.Since(idleSince) < 300*time.Millisecond; {
		working, err := app.workingSession(holds)
		if err != nil {
			release()
			return nil, err
		}
		if working != "" {
			app.setWorkerWait(key, app.sessionName(working))
			idleSince = time.Now()
		}
		select {
		case <-ctx.Done():
			release()
			return nil, app.waitedTooLong(key)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err := app.stopSession(ctx, key, restart); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// workingSession returns a session that holds passes that works or has a prompt being handled, if any.
func (app *application) workingSession(holds func(string) bool) (string, error) {
	saved, err := app.sessionStore.Sessions()
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(saved))
	for _, session := range saved {
		keys = append(keys, session.Path)
	}
	for _, pending := range app.pendingSessions.Entries() {
		keys = append(keys, pending.Path)
	}
	for _, key := range keys {
		if holds(key) && (app.sessionWorking(key) || app.promptAdmissions.handling(key)) {
			return key, nil
		}
	}
	return "", nil
}

// hold makes the new prompts of a session wait until release, once the prompts that it is handling are handled, and
// says meanwhile that its move waits for itself.
func (app *application) hold(ctx context.Context, key string) (func(), error) {
	for {
		if release, held := app.promptAdmissions.retireIdle(key); held {
			return release, nil
		}
		app.setWorkerWait(key, app.sessionName(key))
		select {
		case <-ctx.Done():
			return nil, app.waitedTooLong(key)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// waitedTooLong refuses the move of a session that waited too long for the session that it says it waits for.
func (app *application) waitedTooLong(key string) error {
	app.workerWaitsMu.Lock()
	waiting := app.workerWaits[key]
	app.workerWaitsMu.Unlock()
	return fmt.Errorf("session %s is still %w; try again once it has finished", waiting, errSessionWorking)
}

// sessionWorking reports whether a session runs a step, a shell command or a compaction, or has prompts waiting for
// one.
func (app *application) sessionWorking(key string) bool {
	snapshot := app.rpcClients.LiveSnapshot(key)
	return snapshot.Busy || snapshot.Compacting || len(snapshot.Queue.Steering)+len(snapshot.Queue.FollowUp) > 0 || app.rpcClients.DeferringCompactionPrompts(key)
}

// sessionName names a session for the user: by its name, its first message or its key.
func (app *application) sessionName(key string) string {
	if session, found := app.sessionStore.Session(key); found {
		if session.DisplayName != "" {
			return session.DisplayName
		}
		if session.FirstUserMessage != "" {
			return session.FirstUserMessage
		}
	}
	return key
}

// setWorkerWait notes which session the move or return of the session key waits for, if any.
func (app *application) setWorkerWait(key, waiting string) {
	app.workerWaitsMu.Lock()
	defer app.workerWaitsMu.Unlock()
	if waiting == "" {
		delete(app.workerWaits, key)
		return
	}
	if app.workerWaits == nil {
		app.workerWaits = map[string]string{}
	}
	app.workerWaits[key] = waiting
}

// stopSession ends the Pi of a session, unless it is working. With a new session that Pi has not saved yet, the
// session keeps its key, and restart gets the function that starts its Pi again: with what was chosen in its Pi, or
// in the Pi before a restart that failed.
func (app *application) stopSession(ctx context.Context, key string, restart *func(context.Context) error) error {
	var err error
	if _, pending := app.pendingSessions.CWD(key); pending && !app.sessionStore.Exists(key) {
		*restart, err = agent.StopUnsavedSession(ctx, key, app.rpcClients, app.pendingSessions)
		app.workerWaitsMu.Lock()
		if *restart == nil && err == nil {
			*restart = app.failedRestarts[key]
		}
		delete(app.failedRestarts, key)
		app.workerWaitsMu.Unlock()
	} else {
		err = app.retireRPCClient(ctx, key, func() (bool, error) { return app.rpcClients.CloseClientIfIdle(key) })
	}
	if err == nil && app.rpcClients.Registered(key) {
		err = fmt.Errorf("session %s is %w; wait until it finishes or stop it", key, errSessionWorking)
	}
	return err
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
	key, ok := app.workerSession(response, request)
	if !ok {
		return
	}
	port, ok := workerPort(response, request)
	if !ok {
		return
	}
	local, err := app.workers.Forward(request.Context(), key, port)
	if err != nil {
		writeWorkersError(response, "forward a port of a worker", err)
		return
	}
	writeJSON(response, map[string]int{"port": port, "local": local})
}

func (app *application) unforwardPort(response http.ResponseWriter, request *http.Request) {
	key, ok := app.workerSession(response, request)
	if !ok {
		return
	}
	port, ok := workerPort(response, request)
	if !ok {
		return
	}
	if err := app.workers.Unforward(key, port); err != nil {
		writeWorkersError(response, "stop forwarding a port of a worker", err)
		return
	}
	writeJSON(response, map[string]int{"port": port})
}
