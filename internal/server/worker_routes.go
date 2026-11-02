package server

import (
	"context"
	"net/http"
)

// WorkerPlacement says which sessions run on one worker, which the gripi commands that run there may reach.
type WorkerPlacement interface {
	// Session reports whether a session, by its key, runs on the worker.
	Session(key string) bool
}

// workerRoutes are the routes of Local that workers may use. claim is left out because it looks for the session among
// all of main's, sudo because it runs commands as root on main, and new sessions because they would start on main.
var workerRoutes = []string{
	"GET /sessions", "GET /conversation", "POST /prompt", "POST /abort", "POST /sessions/rename",
	"POST /sessions/pin", "POST /sessions/tags", "POST /sessions/delete", "GET /tags",
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
	routes.HandleFunc("/", func(response http.ResponseWriter, _ *http.Request) {
		writeJSONStatus(response, http.StatusForbidden, map[string]string{"error": "Gripi does not run this command for workers"})
	})
	return handler.app.limitRequestBody(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		routes.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), workerKey{}, placement)))
	}))
}

// reachesSession reports whether a request may touch a session: a worker's reaches only the sessions on that worker.
func reachesSession(request *http.Request, key string) bool {
	placement, fromWorker := request.Context().Value(workerKey{}).(WorkerPlacement)
	return !fromWorker || placement.Session(key)
}
