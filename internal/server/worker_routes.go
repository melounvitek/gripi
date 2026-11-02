package server

import (
	"context"
	"net/http"
)

// WorkerPlacement says which sessions run on one worker, which the gripi commands that run there may reach.
type WorkerPlacement interface {
	// Name names the worker.
	Name() string
	// Session reports whether a session, by its key, runs on the worker.
	Session(key string) bool
}

// workerRoutes are the routes of Local that workers may use. claim is left out because it looks for the session among
// all of main's, and new sessions because they would start on main. sudo runs the command on the worker.
var workerRoutes = []string{
	"GET /sessions", "GET /conversation", "POST /prompt", "POST /abort", "POST /sessions/rename",
	"POST /sessions/pin", "POST /sessions/tags", "POST /sessions/delete", "GET /tags", "POST /sudo",
}

// workerRefusals say why gripi on a worker can't run a command that it has no route for, and what to do instead, by
// path.
var workerRefusals = map[string]string{
	"/sessions/new_at_cwd": "can't start sessions, which would start on main; ask the user to start one in Gripi",
	"/sessions/claim":      "can't claim sessions, which it would look for among main's; ask the user to run gripi claim on main",
	"/workers/move":        "can't move sessions between main and workers; ask the user to do it in Gripi, or with gripi worker on main",
	"/workers/return":      "can't move sessions between main and workers; ask the user to do it in Gripi, or with gripi worker on main",
	"/workers/forward":     "can't forward ports to main; ask the user to do it with Forward a port… in Gripi, or with gripi worker forward on main",
	"/workers/unforward":   "can't forward ports to main; ask the user to do it with Forward a port… in Gripi, or with gripi worker forward on main",
}

type workerKey struct{}

// Worker serves the gripi commands of one worker, which reach only the sessions that placement says run on that
// worker. Serve it on a socket of its own for each worker, because a request is told apart only by the handler that
// gets it. Like Local, it skips browser authentication, and in multi-user mode acts for the user whose token a
// request carries.
func (handler *Handler) Worker(placement WorkerPlacement) http.Handler {
	routes := http.NewServeMux()
	for _, route := range workerRoutes {
		routes.Handle(route, handler.commands)
	}
	routes.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		name := placement.Name()
		instead, known := workerRefusals[request.URL.Path]
		if !known {
			instead = "reaches only the sessions on " + name + " and can't run this command; ask the user to run it on main"
		}
		writeJSONStatus(response, http.StatusForbidden, map[string]string{"error": "gripi on worker " + name + " " + instead})
	})
	return handler.app.limitRequestBody(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		routes.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), workerKey{}, placement)))
	}))
}

// reachesSession reports whether a request may touch a session: a worker's reaches only the sessions on that worker.
func reachesSession(request *http.Request, key string) bool {
	placement, fromWorker := workerOf(request)
	return !fromWorker || placement.Session(key)
}

// workerOf returns the placement of the worker whose gripi sent a request, if a worker's did.
func workerOf(request *http.Request) (WorkerPlacement, bool) {
	placement, fromWorker := request.Context().Value(workerKey{}).(WorkerPlacement)
	return placement, fromWorker
}
