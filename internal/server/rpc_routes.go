package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/sessions"
)

func (app *application) events(response http.ResponseWriter, request *http.Request) {
	raw, ok := app.requireOwnedSession(response, request, request.URL.Query().Get("session"))
	if !ok {
		return
	}
	path, err := app.canonicalCommandSessionPath(request, raw)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return
	}
	if path == "" {
		http.NotFound(response, request)
		return
	}
	after, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
	batch := app.rpcClients.EventsAfter(path, after)
	payload := map[string]any{"events": batch.Events, "last_seq": batch.LastSeq, "missed": batch.Missed}
	if app.sessionStore.Exists(path) {
		state := app.guard.InspectIfAvailable(request.Context(), path, false)
		if state != nil {
			mode := state.Mode
			if app.sessionFolderMissing(path) {
				mode = agent.SyncFolderMissing
			}
			payload["session_sync"] = map[string]any{"mode": mode, "revision": nullableString(state.Revision), "error": nullableString(state.Error), "gateway_busy": app.rpcClients.Busy(path)}
		}
	} else {
		payload["session_sync"] = map[string]any{"mode": agent.SyncAvailable, "revision": nil, "error": nil, "gateway_busy": app.rpcClients.Busy(path)}
	}
	writeJSON(response, payload)
}

func (app *application) liveSessionStatus(response http.ResponseWriter, request *http.Request) {
	raw, ok := app.requireOwnedSession(response, request, request.URL.Query().Get("session"))
	if !ok {
		return
	}
	path, err := app.canonicalCommandSessionPath(request, raw)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return
	}
	if path == "" {
		http.NotFound(response, request)
		return
	}
	live := app.readLiveStatus(request.Context(), path)
	var status agent.Status
	// The saved status fills in what the agent doesn't report.
	if live == nil || live.Provider == "" || live.ModelID == "" || live.ThinkingLevel == "" || live.Context == nil {
		if app.sessionStore.Exists(path) {
			persisted, err := app.sessionStore.Status(path)
			if err != nil {
				http.NotFound(response, request)
				return
			}
			status = persisted
		}
	}
	if live != nil {
		applyLiveStatus(&status, live)
	}
	contextUsage, model := "", ""
	for _, item := range statusItems(status) {
		switch item[0] {
		case "CTX":
			contextUsage = item[1]
		case "Model":
			model = strings.TrimSuffix(item[1], " ("+status.ThinkingLevel+")")
		}
	}
	writeJSON(response, map[string]any{"context": contextUsage, "model": model, "thinking": status.ThinkingLevel})
}

func (app *application) commands(response http.ResponseWriter, request *http.Request) {
	raw, ok := app.requireOwnedSession(response, request, request.URL.Query().Get("session"))
	if !ok {
		return
	}
	path, err := app.canonicalCommandSessionPath(request, raw)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return
	}
	if path == "" {
		http.NotFound(response, request)
		return
	}
	app.rpcDiagnostics.Log("request_operation", map[string]any{"path": request.URL.Path, "session": path, "lane": "operation"})
	composer := app.adapter.Composer(path)
	var agentCommands []agent.Command
	err = app.withSynchronizedClient(request, path, func(client agent.Client) error {
		var requestErr error
		agentCommands, requestErr = client.Commands(request.Context())
		return requestErr
	})
	// Without the agent's own commands, the palette still lists those of the gateway.
	commands := composer.Commands()
	if err == nil {
		commands = append(commands, agentCommands...)
	} else if !commandCatalogFallbackError(err) && app.writeRPCError(response, err) {
		return
	}
	groups := make([]commandGroup, 0, 4)
	for _, source := range composer.CommandGroups() {
		group := commandGroup{Source: source}
		for _, command := range commands {
			if command.Group == source {
				group.Commands = append(group.Commands, commandView{Name: command.Name, Description: command.Description})
			}
		}
		if len(group.Commands) > 0 {
			groups = append(groups, group)
		}
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = app.templates.ExecuteTemplate(response, "commands_list", struct {
		Count  int
		Groups []commandGroup
	}{len(commands), groups})
}

type commandGroup struct {
	Source   string
	Commands []commandView
}
type commandView struct{ Name, Description string }

func (command commandView) Text() string { return "/" + command.Name + " " + command.Description }

// readLiveStatus returns nil when no client runs the session.
func (app *application) readLiveStatus(ctx context.Context, path string) *agent.LiveStatus {
	var status *agent.LiveStatus
	err := app.rpcClients.WithObservingClient(ctx, path, false, func(client agent.Client) error {
		live, err := client.LiveStatus(ctx)
		status = &live
		return err
	})
	if err != nil {
		return nil
	}
	return status
}

func applyLiveStatus(status *agent.Status, live *agent.LiveStatus) {
	if live.Provider != "" {
		status.Provider = live.Provider
	}
	if live.ModelID != "" {
		status.ModelID = live.ModelID
	}
	if live.ThinkingLevel != "" {
		status.ThinkingLevel = live.ThinkingLevel
	}
	if live.Context != nil {
		status.ContextTokens, status.HasContextTokens = live.Context.Tokens, live.Context.HasTokens
		status.ContextLimit, status.HasContextLimit = live.Context.Window, live.Context.HasWindow
		status.ContextPercent = live.Context.Percent
		status.ContextEstimated = false
	}
}

func (app *application) withSynchronizedClient(request *http.Request, path string, call func(agent.Client) error) error {
	resolved, _, err := app.resolveOwnedPendingPath(request, path)
	if err != nil {
		return err
	}
	path = resolved
	ctx := request.Context()
	if app.sessionStore.Exists(path) {
		return app.guard.WithMutableClient(ctx, path, call)
	}
	return app.rpcClients.WithClient(ctx, path, call)
}

func (app *application) writeRPCError(response http.ResponseWriter, err error) bool {
	var blocked *agent.SyncBlockedError
	var timeout *agent.RequestTimeoutError
	switch {
	case errors.As(err, &blocked):
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": blocked.Error(), "session_sync_mode": blocked.Mode})
		return true
	case errors.Is(err, errSessionNavigating):
		writeJSONStatus(response, http.StatusConflict, map[string]any{"code": "session_operation_pending", "retryable": false, "error": err.Error()})
		return true
	case errors.Is(err, agent.ErrSyncBusy), errors.Is(err, agent.ErrOperationPending):
		writeJSONStatus(response, http.StatusConflict, map[string]any{"code": "session_operation_pending", "error": "Another session operation is pending. Please retry."})
		return true
	case errors.Is(err, agent.ErrClientRetiring):
		response.Header().Set("Retry-After", "1")
		writeJSONStatus(response, http.StatusServiceUnavailable, map[string]any{"error": "Pi RPC client is restarting"})
		return true
	case errors.As(err, &timeout):
		writeJSONStatus(response, http.StatusGatewayTimeout, map[string]any{"error": err.Error()})
		return true
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return true
	default:
		return false
	}
}

func (app *application) canonicalCommandSessionPath(request *http.Request, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	canonical, err := app.canonicalRPCSessionPath(request, path)
	if err != nil {
		return "", err
	}
	available := app.commandSessionAvailable(canonical)
	if !available {
		return "", nil
	}
	return canonical, nil
}

func (app *application) commandSessionAvailable(path string) bool {
	if path == "" {
		return false
	}
	if app.rpcClients.Active(path) {
		return true
	}
	_, ok := app.sessionStore.Session(path)
	return ok
}

func (app *application) canonicalRPCSessionPath(request *http.Request, path string) (string, error) {
	ctx := request.Context()
	if resolved, remapped, err := app.resolveOwnedPendingPath(request, path); err != nil {
		return path, err
	} else if remapped {
		return resolved, nil
	}
	session, known := app.sessionStore.Session(path)
	if known {
		path = session.Path
	}
	if cwd, ok := app.pendingSessions.CWD(path); ok && app.rpcClients.Active(path) {
		var state agent.State
		if app.rpcClients.WithExistingClient(ctx, path, false, func(client agent.Client) error { var err error; state, err = client.State(ctx); return err }) == nil {
			reported := state.Key
			if reported != "" {
				if session, ok := app.sessionStore.Session(reported); ok && session.CWD == cwd {
					if err := app.movePendingRPCClient(request, path, session.Path); err != nil {
						return path, err
					}
					resolved, _, err := app.resolveOwnedPendingPath(request, path)
					return resolved, err
				}
			}
		}
		return path, nil
	}
	if !known {
		return path, nil
	}
	if app.rpcClients.Active(path) {
		return path, nil
	}
	for _, pending := range app.pendingSessions.Entries() {
		if app.ownsSession != nil && !app.ownsSession(request, pending.Path) {
			continue
		}
		if pending.CWD != session.CWD || !app.rpcClients.Active(pending.Path) {
			continue
		}
		var state agent.State
		err := app.rpcClients.WithExistingClient(ctx, pending.Path, false, func(client agent.Client) error {
			var requestErr error
			state, requestErr = client.State(ctx)
			return requestErr
		})
		if err == nil {
			reported, found := app.sessionStore.Session(state.Key)
			if found && reported.Path == session.Path {
				if err := app.movePendingRPCClient(request, pending.Path, session.Path); err != nil {
					return path, err
				}
				break
			}
		}
	}
	resolved, _, err := app.resolveOwnedPendingPath(request, path)
	return resolved, err
}

func (app *application) resolveOwnedPendingPath(request *http.Request, path string) (string, bool, error) {
	current := path
	seen := make(map[string]bool)
	for range 256 {
		next, remapped := app.pendingSessions.Resolve(current)
		if !remapped {
			return current, current != path, nil
		}
		if seen[current] || seen[next] {
			return path, false, errors.New("pending session remap cycle")
		}
		if app.ownsSession != nil && (!app.ownsSession(request, current) || !app.ownsSession(request, next)) {
			return path, false, errors.New("pending session remap is not owned by the requester")
		}
		seen[current] = true
		current = next
	}
	return path, false, errors.New("pending session remap chain is too long")
}

func (app *application) lockResolvedImagePromptPath(request *http.Request, path string) (string, func(), error) {
	for range 256 {
		resolved, _, err := app.resolveOwnedPendingPath(request, path)
		if err != nil {
			return "", nil, err
		}
		unlock := app.imagePromptLocks.Lock(resolved)
		latest, _, err := app.resolveOwnedPendingPath(request, resolved)
		if err != nil {
			unlock()
			return "", nil, err
		}
		if latest == resolved {
			return resolved, unlock, nil
		}
		unlock()
		path = latest
	}
	return "", nil, errors.New("pending session changed too many times")
}

func (app *application) movePendingRPCClient(request *http.Request, from, to string) error {
	return app.remapPendingRPCClient(from, to, func() (func() error, error) {
		if app.ownsSession != nil && !app.ownsSession(request, from) {
			return nil, errors.New("pending session is not owned by the requester")
		}
		if app.claimSession == nil {
			return nil, nil
		}
		claimed, err := app.claimSession(request, to)
		if err != nil || !claimed || app.releaseSession == nil {
			return nil, err
		}
		return func() error { return app.releaseSession(request, to) }, nil
	})
}

func (app *application) remapPendingRPCClient(from, to string, claim func() (func() error, error)) error {
	unlock := app.imagePromptLocks.Lock(from)
	defer unlock()
	app.pendingRemapMu.Lock()
	defer app.pendingRemapMu.Unlock()
	unlockMutation := app.sessionMutationLocks.Lock(to)
	defer unlockMutation()
	if app.gatewayState != nil && app.gatewayState.SessionForgotten(to) {
		return os.ErrNotExist
	}

	if resolved, remapped := app.pendingSessions.Resolve(from); remapped {
		if resolved != to {
			return errors.New("pending session was remapped to a different path")
		}
		_, err := claim()
		return err
	}
	if from == to {
		if _, err := claim(); err != nil {
			return err
		}
		app.pendingSessions.Forget(from)
		return nil
	}
	releaseSource, err := app.promptAdmissions.tryPrompt(from)
	if err != nil {
		return err
	}
	defer releaseSource()
	releaseDestination, err := app.promptAdmissions.tryPrompt(to)
	if err != nil {
		return err
	}
	defer releaseDestination()
	// Completion callbacks must not write the synthetic path after its state moves.
	if app.completionNotifications != nil {
		app.completionNotifications.mu.Lock()
		defer app.completionNotifications.mu.Unlock()
	}
	return app.rpcClients.MoveWithCommit(from, to, func() (func() error, error) {
		ownershipRollback, err := claim()
		if err != nil {
			return nil, err
		}
		stateRollback, err := app.migratePendingSessionState(from, to, true)
		if err != nil {
			if ownershipRollback != nil {
				err = errors.Join(err, ownershipRollback())
			}
			return nil, err
		}
		return func() error {
			var stateErr error
			if stateRollback != nil {
				stateErr = stateRollback()
			}
			var ownershipErr error
			if ownershipRollback != nil {
				ownershipErr = ownershipRollback()
			}
			return errors.Join(stateErr, ownershipErr)
		}, nil
	}, func() {
		app.promptAdmissions.remap(from, to, func() {
			app.pendingSessions.Remap(from, to)
		})
	})
}

func (app *application) migratePendingSessionState(from, to string, migrateTags bool) (func() error, error) {
	attachmentRollback, err := (sessions.AttachmentStore{Root: app.config.AttachmentsRoot}).Migrate(from, to)
	if err != nil {
		return nil, err
	}
	var pinRollback func() error
	if app.gatewayState != nil {
		pinRollback, err = app.gatewayState.MigratePinned(from, to)
	}
	if err != nil {
		if attachmentRollback != nil {
			err = errors.Join(err, attachmentRollback())
		}
		return nil, err
	}
	var tagRollback func() error
	if migrateTags && app.gatewayState != nil {
		tagRollback, err = app.gatewayState.MigrateTags(from, to)
		if err != nil {
			if pinRollback != nil {
				err = errors.Join(err, pinRollback())
			}
			if attachmentRollback != nil {
				err = errors.Join(err, attachmentRollback())
			}
			return nil, err
		}
	}
	var completionRollback func() error
	if migrateTags && app.gatewayState != nil {
		completionRollback, err = app.gatewayState.MigrateCompletion(from, to)
		if err != nil {
			if tagRollback != nil {
				err = errors.Join(err, tagRollback())
			}
			if pinRollback != nil {
				err = errors.Join(err, pinRollback())
			}
			if attachmentRollback != nil {
				err = errors.Join(err, attachmentRollback())
			}
			return nil, err
		}
	}
	return func() error {
		var completionErr error
		if completionRollback != nil {
			completionErr = completionRollback()
		}
		var attachmentErr error
		if attachmentRollback != nil {
			attachmentErr = attachmentRollback()
		}
		var pinErr error
		if pinRollback != nil {
			pinErr = pinRollback()
		}
		var tagErr error
		if tagRollback != nil {
			tagErr = tagRollback()
		}
		return errors.Join(attachmentErr, pinErr, tagErr, completionErr)
	}, nil
}

func (app *application) cleanupIdleRPCClients(ctx context.Context) error {
	if app.config.RPCIdleTimeout <= 0 {
		return nil
	}
	now := time.Now()
	paths := app.rpcClients.IdleClientPaths(app.config.RPCIdleTimeout, now, nil)
	var first error
	for _, path := range paths {
		if _, pending := app.pendingSessions.CWD(path); pending {
			continue
		}
		if err := app.cleanupIdleRPCClient(ctx, path, now); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (app *application) cleanupIdleRPCClient(ctx context.Context, path string, now time.Time) error {
	release, admitted := app.promptAdmissions.retireIdle(path)
	if !admitted {
		return nil
	}
	defer release()
	return app.retireRPCClient(ctx, path, func() (bool, error) {
		return app.rpcClients.CloseClientIfExpired(path, app.config.RPCIdleTimeout, now, nil)
	})
}

// retireStaleRPCClient retires the session's idle Pi if it started before the user's variables last changed, so that
// the next client gets them. Pi has not saved a pending session, so a new process could not resume it, and such a
// session restarts at once instead, with what was chosen in it.
func (app *application) retireStaleRPCClient(request *http.Request, path string) error {
	if app.environment == nil {
		return nil
	}
	changedAt := app.environment.ChangedAt(app.environmentUser(request))
	if changedAt.IsZero() {
		return nil
	}
	if _, pending := app.pendingSessions.CWD(path); pending && !app.sessionStore.Exists(path) {
		_, err := agent.RestartUnsavedSession(request.Context(), path, changedAt, app.rpcClients, app.pendingSessions)
		return err
	}
	return app.retireRPCClient(request.Context(), path, func() (bool, error) {
		return app.rpcClients.CloseClientIfStartedBefore(path, changedAt)
	})
}

func (app *application) retireRPCClient(ctx context.Context, path string, close func() (bool, error)) error {
	var closed bool
	var err error
	if app.sessionStore.Exists(path) {
		app.guard.ReconcileIfAvailable(ctx, path, false, func(agent.SyncResult) {
			closed, err = close()
			if closed {
				app.guard.Forget(path)
			}
		})
	} else {
		closed, err = close()
	}
	if closed {
		app.pendingSessions.Forget(path)
	}
	return err
}

func commandCatalogFallbackError(err error) bool {
	return errors.Is(err, agent.ErrOperationPending) || errors.Is(err, agent.ErrClientRetiring) || errors.Is(err, agent.ErrClientStarting) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, agent.ErrProcessExited)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func writeJSONStatus(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	writeJSON(response, value)
}
