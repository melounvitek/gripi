package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/melounvitek/gripi/internal/agent"
)

func (app *application) clearQueue(response http.ResponseWriter, request *http.Request) {
	fail := func(status int, message string) {
		writeJSONStatus(response, status, map[string]any{"error": message})
	}
	err := parseRequestForm(request)
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		fail(status, "Invalid request body")
		return
	}
	path := request.FormValue("session")
	if !app.validOwnedSessionPath(request, path) {
		fail(http.StatusNotFound, "Session not found")
		return
	}
	writeError := func(err error) {
		switch {
		case errors.Is(err, context.Canceled):
			fail(http.StatusRequestTimeout, err.Error())
		case errors.Is(err, context.DeadlineExceeded):
			fail(http.StatusGatewayTimeout, err.Error())
		case errors.Is(err, agent.ErrInterruptPending):
			fail(http.StatusConflict, err.Error())
		default:
			if !app.writeRPCError(response, err) {
				fail(http.StatusBadGateway, err.Error())
			}
		}
	}
	// Resolve recorded aliases under admission, without GetState discovery:
	// discovery can take the operation lane or retire a client on timeout.
	var resolveErr error
	path, release, err := app.promptAdmissions.prompt(request.Context(), func() (string, error) {
		resolved, _, err := app.resolveOwnedPendingPath(request, path)
		resolveErr = err
		return resolved, err
	})
	if resolveErr != nil {
		fail(http.StatusNotFound, "Session not found")
		return
	}
	if err != nil {
		writeError(err)
		return
	}
	defer release()
	if !app.knownOrPendingSession(request, path) {
		fail(http.StatusNotFound, "Session not found")
		return
	}
	if app.guard != nil {
		if blocked := app.guard.KnownBlocked(path); blocked != nil {
			writeError(&agent.SyncBlockedError{Mode: blocked.Mode, Message: app.guard.Message(*blocked)})
			return
		}
	}
	var snapshot agent.LiveSnapshot
	var clearErr error
	found, supported := false, false
	err = app.rpcClients.WithExistingInterruptClient(request.Context(), path, func(client agent.Client) error {
		found = true
		clearer, ok := client.(agent.QueueClearer)
		supported = ok
		if ok {
			clearErr = clearer.ClearQueue(request.Context())
			if clearErr == nil {
				snapshot = client.LiveSnapshot()
			}
		}
		// Clear must never close the client, even on a native timeout. Keep
		// only this operation's errors out of registry terminal-error handling.
		return nil
	})
	refused := refusal(clearErr)
	switch {
	case err != nil:
		writeError(err)
	case !found:
		fail(http.StatusConflict, "No active Pi RPC client for this session")
	case !supported:
		fail(http.StatusNotImplemented, "Pi RPC client does not support clearing the queue")
	case refused != nil:
		fail(http.StatusUnprocessableEntity, failureMessage(refused.Message, "Could not clear the queue"))
	case clearErr != nil:
		writeError(clearErr)
	default:
		if snapshot.QueuedMessages == nil {
			snapshot.QueuedMessages = map[string][]string{}
		}
		writeJSON(response, map[string]any{
			"ok": true, "session": path,
			"queued_messages": snapshot.QueuedMessages, "event_sequence": snapshot.EventSequence,
		})
	}
}
