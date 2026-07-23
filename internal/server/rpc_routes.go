package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/melounvitek/gripi/internal/rpc"
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
	if _, err := os.Stat(path); err == nil {
		state := app.synchronizer.InspectIfAvailable(request.Context(), path, false)
		if state != nil {
			payload["session_sync"] = map[string]any{"mode": state.Mode, "revision": nullableString(state.Revision), "error": nullableString(state.Error), "gateway_busy": app.rpcClients.Busy(path)}
		}
	} else {
		payload["session_sync"] = map[string]any{"mode": sessions.SyncAvailable, "revision": nil, "error": nil, "gateway_busy": app.rpcClients.Busy(path)}
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
	var status sessions.Status
	if live == nil || !live.diskIndependent {
		if _, err := os.Stat(path); err == nil {
			store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
			persisted, err := store.Status(path)
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
	var rpcResponse map[string]any
	err = app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		var requestErr error
		rpcResponse, requestErr = client.GetCommands(request.Context())
		return requestErr
	})
	commands := rpc.CommandsFrom(rpcResponse)
	if err != nil {
		if !commandCatalogFallbackError(err) && app.writeRPCError(response, err) {
			return
		}
		commands = rpc.BuiltinCommands()
	}
	groups := make([]commandGroup, 0, 4)
	for _, source := range []string{"extension", "prompt", "skill", "other"} {
		group := commandGroup{Source: source}
		for _, command := range commands {
			commandSource := stringFromAny(command["source"])
			if commandSource == "" {
				commandSource = "other"
			}
			if commandSource == source {
				group.Commands = append(group.Commands, commandView{Name: stringFromAny(command["name"]), Description: stringFromAny(command["description"])})
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

type liveStatus struct {
	provider, modelID, thinking string
	context                     map[string]any
	diskIndependent             bool
}

func (app *application) readLiveStatus(ctx context.Context, path string) *liveStatus {
	var stateResponse, statsResponse map[string]any
	err := app.rpcClients.WithObservingClient(ctx, path, false, func(client rpc.RPCClient) error {
		var err error
		stateResponse, err = client.GetState(ctx)
		if err != nil {
			return err
		}
		statsResponse, err = client.GetSessionStats(ctx)
		return err
	})
	if err != nil || stateResponse == nil || statsResponse == nil {
		return nil
	}
	state := successfulData(stateResponse)
	stats := successfulData(statsResponse)
	result := &liveStatus{}
	if model, ok := state["model"].(map[string]any); ok {
		result.provider = nonemptyString(model["provider"])
		result.modelID = nonemptyString(model["id"])
	}
	result.thinking = nonemptyString(state["thinkingLevel"])
	if contextUsage, ok := stats["contextUsage"].(map[string]any); ok && validContextUsage(contextUsage) {
		result.context = contextUsage
	}
	result.diskIndependent = result.provider != "" && result.modelID != "" && result.thinking != "" && result.context != nil
	return result
}

func applyLiveStatus(status *sessions.Status, live *liveStatus) {
	if live.provider != "" {
		status.Provider = live.provider
	}
	if live.modelID != "" {
		status.ModelID = live.modelID
	}
	if live.thinking != "" {
		status.ThinkingLevel = live.thinking
	}
	if live.context != nil {
		if value, ok := numericValue(live.context["tokens"]); ok {
			status.ContextTokens = value
			status.HasContextTokens = true
		} else {
			status.ContextTokens = 0
			status.HasContextTokens = false
		}
		if value, ok := numericValue(live.context["contextWindow"]); ok {
			status.ContextLimit = value
			status.HasContextLimit = true
		} else {
			status.ContextLimit = 0
			status.HasContextLimit = false
		}
		if value, ok := numericValue(live.context["percent"]); ok {
			status.ContextPercent = value
		} else {
			status.ContextPercent = 0
		}
		status.ContextEstimated = false
	}
}

func (app *application) withSynchronizedClient(request *http.Request, path string, call func(rpc.RPCClient) error) error {
	resolved, _, err := app.resolveOwnedPendingPath(request, path)
	if err != nil {
		return err
	}
	path = resolved
	ctx := request.Context()
	if _, err := os.Stat(path); err == nil {
		return app.synchronizer.WithMutableClient(ctx, path, call)
	}
	return app.rpcClients.WithClient(ctx, path, call)
}

func (app *application) writeRPCError(response http.ResponseWriter, err error) bool {
	var blocked *sessions.SyncBlockedError
	var timeout *rpc.RequestTimeoutError
	switch {
	case errors.As(err, &blocked):
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": blocked.Error(), "session_sync_mode": blocked.Mode})
		return true
	case errors.Is(err, sessions.ErrSyncBusy), errors.Is(err, rpc.ErrOperationPending):
		writeJSONStatus(response, http.StatusConflict, map[string]any{"code": "session_operation_pending", "error": "Another session operation is pending. Please retry."})
		return true
	case errors.Is(err, rpc.ErrClientRetiring):
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
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	_, ok := store.Session(path)
	return ok
}

func (app *application) canonicalRPCSessionPath(request *http.Request, path string) (string, error) {
	ctx := request.Context()
	if resolved, remapped, err := app.resolveOwnedPendingPath(request, path); err != nil {
		return path, err
	} else if remapped {
		return resolved, nil
	}
	if cwd, ok := app.pendingSessions.CWD(path); ok && app.rpcClients.Active(path) {
		var state map[string]any
		if app.rpcClients.WithExistingClient(ctx, path, true, func(client rpc.RPCClient) error { var err error; state, err = client.GetState(ctx); return err }) == nil {
			reported := sessionFileFrom(state)
			if reported != "" {
				store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
				if session, ok := store.Session(reported); ok && session.CWD == cwd {
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
	if app.rpcClients.Active(path) {
		return path, nil
	}
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	session, known := store.Session(path)
	if !known {
		return path, nil
	}
	for _, pending := range app.pendingSessions.Entries() {
		if app.ownsSession != nil && !app.ownsSession(request, pending.Path) {
			continue
		}
		if pending.CWD != session.CWD || !app.rpcClients.Active(pending.Path) {
			continue
		}
		var state map[string]any
		err := app.rpcClients.WithExistingClient(ctx, pending.Path, true, func(client rpc.RPCClient) error {
			var requestErr error
			state, requestErr = client.GetState(ctx)
			return requestErr
		})
		if err == nil {
			reported, found := store.Session(sessionFileFrom(state))
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
	unlock := app.imagePromptLocks.Lock(from)
	defer unlock()
	app.pendingRemapMu.Lock()
	defer app.pendingRemapMu.Unlock()
	if app.ownsSession != nil && !app.ownsSession(request, from) {
		return errors.New("pending session is not owned by the requester")
	}
	if from == to {
		if app.claimSession != nil {
			if _, err := app.claimSession(request, to); err != nil {
				return err
			}
		}
		app.pendingSessions.Forget(from)
		return nil
	}
	return app.rpcClients.MoveWithCommit(from, to, func() (func() error, error) {
		claimed := false
		if app.claimSession != nil {
			var err error
			claimed, err = app.claimSession(request, to)
			if err != nil {
				return nil, err
			}
		}
		attachmentRollback, err := (sessions.AttachmentStore{Root: app.config.AttachmentsRoot}).Migrate(from, to)
		if err != nil {
			if claimed && app.releaseSession != nil {
				err = errors.Join(err, app.releaseSession(request, to))
			}
			return nil, err
		}
		return func() error {
			var attachmentErr error
			if attachmentRollback != nil {
				attachmentErr = attachmentRollback()
			}
			var ownershipErr error
			if claimed && app.releaseSession != nil {
				ownershipErr = app.releaseSession(request, to)
			}
			return errors.Join(attachmentErr, ownershipErr)
		}, nil
	}, func() {
		app.pendingSessions.Remap(from, to)
	})
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
		var closed bool
		var err error
		if _, statErr := os.Stat(path); statErr == nil {
			app.synchronizer.ReconcileIfAvailable(ctx, path, false, func(sessions.SyncResult) {
				closed, err = app.rpcClients.CloseClientIfExpired(path, app.config.RPCIdleTimeout, now, nil)
				if closed {
					app.synchronizer.Forget(path)
				}
			})
		} else {
			closed, err = app.rpcClients.CloseClientIfExpired(path, app.config.RPCIdleTimeout, now, nil)
		}
		if err != nil && first == nil {
			first = err
		}
		if closed {
			app.pendingSessions.Forget(path)
		}
	}
	return first
}

func successfulData(response map[string]any) map[string]any {
	if response["success"] != true {
		return nil
	}
	data, _ := response["data"].(map[string]any)
	return data
}
func sessionFileFrom(response map[string]any) string {
	data := response
	if nested, ok := response["data"].(map[string]any); ok {
		data = nested
	}
	for _, key := range []string{"sessionFile", "session_file", "path"} {
		if value := stringFromAny(data[key]); value != "" {
			return value
		}
	}
	return ""
}
func validContextUsage(value map[string]any) bool {
	for _, key := range []string{"tokens", "contextWindow", "percent"} {
		if value[key] == nil {
			continue
		}
		if _, ok := numericValue(value[key]); !ok {
			return false
		}
	}
	return true
}
func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		result, err := typed.Float64()
		return result, err == nil
	default:
		return 0, false
	}
}
func stringFromAny(value any) string  { result, _ := value.(string); return result }
func nonemptyString(value any) string { return stringFromAny(value) }
func commandCatalogFallbackError(err error) bool {
	return errors.Is(err, rpc.ErrOperationPending) || errors.Is(err, rpc.ErrClientRetiring) || errors.Is(err, rpc.ErrClientStarting) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, rpc.ErrProcessExited)
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
