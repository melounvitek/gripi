package server

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/melounvitek/gripi/internal/prompts"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

const (
	cwdSuggestionLimit      = 30
	treeEntryIDBytes        = 1_024
	treeLabelBytes          = 4_096
	treeInstructionsBytes   = 64 << 10
	assistantResponseMax    = 2_147_483_647
	maximumSessionPathBytes = 16 << 10
	extensionRequestIDBytes = 1_024
	providerIDBytes         = 4_096
	modelIDBytes            = 4_096
	extensionValueBytes     = 1 << 20
)

var thinkingLevels = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

var treeFilters = map[string]bool{"default": true, "no-tools": true, "user-only": true, "labeled-only": true, "all": true}

func (app *application) registerActionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /prompt", app.prompt)
	mux.HandleFunc("POST /abort", app.abortSession)
	mux.HandleFunc("POST /compact", app.compactSession)
	mux.HandleFunc("GET /sessions/validate_cwd", app.validateSessionCWD)
	mux.HandleFunc("GET /sessions/browse_cwd", app.browseSessionCWD)
	mux.HandleFunc("POST /sessions/new", app.newSession)
	mux.HandleFunc("POST /sessions/new_at_cwd", app.newSessionAtCWD)
	mux.HandleFunc("GET /sessions/model_settings", app.modelSettings)
	mux.HandleFunc("POST /sessions/model_settings", app.setModelSettings)
	mux.HandleFunc("POST /sessions/cycle_thinking", app.cycleThinking)
	mux.HandleFunc("GET /sessions/fork_messages", app.forkMessages)
	mux.HandleFunc("GET /sessions/tree_entries", app.treeEntries)
	mux.HandleFunc("POST /sessions/tree", app.navigateTree)
	mux.HandleFunc("POST /sessions/tree/label", app.setTreeLabel)
	mux.HandleFunc("POST /sessions/fork", app.forkSession)
	mux.HandleFunc("POST /sessions/clone", app.cloneSession)
	mux.HandleFunc("POST /extension_ui_response", app.extensionUIResponse)
	mux.HandleFunc("POST /sessions/takeover", app.takeOverSession)
}

func (app *application) prompt(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	message := request.FormValue("message")
	imageFiles, ok := uploadedPromptImages(response, request)
	if !ok {
		return
	}
	if strings.TrimSpace(message) == "" && len(imageFiles) == 0 {
		writeText(response, http.StatusBadRequest, "Message cannot be empty")
		return
	}
	if command, bash := prompts.ParseBashCommand(message, request.FormValue("bash_mode")); bash {
		if len(imageFiles) > 0 {
			app.writeRequestError(response, request, http.StatusBadRequest, "Images cannot be attached to bash commands")
			return
		}
		app.runBash(response, request, path, command.Command, command.ExcludeFromContext)
		return
	}

	behavior := request.FormValue("streaming_behavior")
	if behavior != "" && behavior != "steer" && behavior != "follow_up" {
		writeText(response, http.StatusBadRequest, "Invalid streaming behavior")
		return
	}
	if behavior == "steer" && app.rpcClients.Compacting(path) {
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": "Steering is unavailable during compaction"})
		return
	}
	command := prompts.SlashCommand{}
	if behavior == "" {
		command = prompts.ParseSlashCommand(message)
	}
	if command.Type == "fork" || command.Type == "tree" || command.Type == "model" {
		app.writePromptResult(response, request, path, map[string]any{"command": command.Type})
		return
	}
	if command.Type == "new" {
		newPath, err := app.startNewSession(request, app.currentSessionCWD(path))
		if err != nil {
			app.writeActionRPCError(response, err)
			return
		}
		app.redirectToNewSession(response, request, newPath, "new")
		return
	}
	if command.Type == "clone" {
		app.branchFromAction(response, request, path, true, "")
		return
	}
	if len(imageFiles) > 0 && command.Type == "" {
		var unlock func()
		var err error
		path, unlock, err = app.lockResolvedImagePromptPath(request, path)
		if err != nil {
			http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
			return
		}
		defer unlock()
	}

	submittedAt := time.Now()
	attachmentStore := sessions.AttachmentStore{Root: app.config.AttachmentsRoot, SessionsRoot: app.config.SessionsRoot}
	var rpcResponse map[string]any
	var rpcMessage = message
	var rpcImages []rpc.PromptImage
	var attachmentPaths, mimeTypes []string
	cleanupImages := func() error { return nil }
	keepImages, cleanupPending := false, true
	cleanupFailedImages := func() error {
		if keepImages || !cleanupPending {
			return nil
		}
		cleanupPending = false
		return cleanupImages()
	}
	defer func() { _ = cleanupFailedImages() }()
	prepareImages := func() error {
		if len(imageFiles) == 0 || len(rpcImages) > 0 || behavior == "" && command.Type != "" {
			return nil
		}
		images, cleanup, err := prompts.PersistUploadedImages(imageFiles, filepath.Join(app.config.AttachmentsRoot, sessions.SessionHash(path)))
		if err != nil {
			return err
		}
		cleanupImages = cleanup
		if request.MultipartForm != nil {
			_ = request.MultipartForm.RemoveAll()
			request.MultipartForm = nil
		}
		for _, image := range images {
			rpcImages = append(rpcImages, rpc.PromptImage{Path: image.Path, MIMEType: image.MIMEType, Size: image.Size})
			attachmentPaths = append(attachmentPaths, image.Path)
			mimeTypes = append(mimeTypes, image.MIMEType)
		}
		rpcMessage = messageWithAttachmentPaths(message, attachmentPaths)
		return nil
	}
	recordImages := func() error {
		if len(rpcImages) == 0 || !successfulRPCResponse(rpcResponse) {
			return nil
		}
		keepImages = true
		if err := attachmentStore.RecordPrompt(path, rpcMessage, len(rpcImages), submittedAt, attachmentPaths, mimeTypes); err != nil {
			return err
		}
		return nil
	}
	call := func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		if err := prepareImages(); err != nil {
			return err
		}
		switch behavior {
		case "steer":
			if client.Compacting() {
				return errSteeringDuringCompaction
			}
			rpcResponse, err = actions.Steer(request.Context(), rpcMessage, rpcImages)
		case "follow_up":
			rpcResponse, err = actions.FollowUp(request.Context(), rpcMessage, rpcImages)
		default:
			switch command.Type {
			case "name":
				if command.Name != "" {
					rpcResponse, err = actions.SetSessionName(request.Context(), command.Name)
				} else {
					rpcResponse, err = client.GetState(request.Context())
				}
			case "compact":
				rpcResponse, err = actions.Compact(request.Context(), command.Instructions)
			default:
				rpcResponse, err = actions.Prompt(request.Context(), rpcMessage, rpcImages)
			}
		}
		if err == nil {
			err = recordImages()
		}
		return err
	}
	var err error
	if behavior == "follow_up" {
		if blocked := app.synchronizer.KnownBlocked(path); blocked != nil {
			err = &sessions.SyncBlockedError{Mode: blocked.Mode, Message: app.synchronizer.Message(*blocked)}
		} else {
			queued := false
			path, ok = app.resolveActionPendingPath(response, request, path)
			if !ok {
				return
			}
			err = app.rpcClients.WithActiveClient(request.Context(), path, true, func(client rpc.RPCClient) error {
				actions, actionErr := checkedActionClient(client)
				if actionErr != nil {
					return actionErr
				}
				if err := prepareImages(); err != nil {
					return err
				}
				rpcResponse, queued, actionErr = actions.QueueCompactionFollowUp(request.Context(), rpcMessage, rpcImages)
				if actionErr == nil && queued {
					actionErr = recordImages()
				}
				return actionErr
			})
			if err == nil && !queued {
				err = app.withSynchronizedClient(request, path, call)
			}
		}
	} else {
		err = app.withSynchronizedClient(request, path, call)
	}
	if errors.Is(err, errSteeringDuringCompaction) {
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": "Steering is unavailable during compaction"})
		return
	}
	if err != nil {
		app.writeActionRPCError(response, errors.Join(err, cleanupFailedImages()))
		return
	}
	if !successfulRPCResponse(rpcResponse) && command.Type != "compact" {
		if err := cleanupFailedImages(); err != nil {
			http.Error(response, "Unable to clean up prompt attachments", http.StatusInternalServerError)
			return
		}
		app.writeRPCFailure(response, request, rpcResponse, command.Type != "")
		return
	}
	payload := map[string]any{}
	if behavior == "steer" {
		payload["steer"] = true
	}
	if behavior == "follow_up" {
		payload["follow_up"] = true
		if rpcResponse["compacting"] == true {
			payload["queued_after_compaction"] = true
		}
	}
	if command.Type != "" {
		payload["command"] = command.Type
		if command.Name != "" {
			payload["name"] = command.Name
		}
		if command.Type == "name" && command.Name == "" {
			name := stringFromAny(responseData(rpcResponse)["sessionName"])
			if name == "" {
				payload["error"] = "Usage: /name <name>"
			} else {
				payload["name"], payload["current"] = name, true
			}
		}
	}
	if command.Type == "" && behavior == "" && strings.HasPrefix(strings.TrimSpace(message), "/") && !strings.ContainsAny(message, "\r\n") {
		var state map[string]any
		if app.rpcClients.WithExistingClient(request.Context(), path, true, func(client rpc.RPCClient) error {
			var stateErr error
			state, stateErr = client.GetState(request.Context())
			return stateErr
		}) == nil {
			if running, valid := responseData(state)["isStreaming"].(bool); valid {
				payload["running"] = running
			}
		}
	}
	app.writePromptResult(response, request, path, payload)
}

var errSteeringDuringCompaction = errors.New("steering during compaction")

var errUnsupportedActionClient = errors.New("Pi RPC client does not support actions")

func checkedActionClient(client rpc.RPCClient) (rpc.ActionClient, error) {
	actions, ok := client.(rpc.ActionClient)
	if !ok {
		return nil, errUnsupportedActionClient
	}
	return actions, nil
}

func (app *application) runBash(response http.ResponseWriter, request *http.Request, path, command string, excluded bool) {
	var result map[string]any
	err := app.withSynchronizedBashClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		result, err = actions.Bash(request.Context(), command, excluded)
		return err
	})
	var bashErr *rpc.BashRequestError
	if errors.As(err, &bashErr) {
		result = map[string]any{"id": bashErr.BashID, "success": false, "error": bashErr.Error()}
		err = nil
	}
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	if !wantsJSON(request) && !successfulRPCResponse(result) {
		app.writeRPCFailure(response, request, result, false)
		return
	}
	if wantsJSON(request) {
		payload := map[string]any{"command": "bash", "bash_id": result["id"], "data": responseData(result), "exclude_from_context": excluded, "session": path, "redirect": app.sessionRedirectPath(request, path)}
		if result["success"] == false {
			payload["error"] = result["error"]
		}
		writeJSON(response, payload)
		return
	}
	http.Redirect(response, request, app.sessionRedirectPath(request, path), http.StatusSeeOther)
}

func (app *application) abortSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	requested, ok := app.requireOwnedSession(response, request, request.FormValue("session"))
	if !ok {
		return
	}
	if !app.knownOrPendingSession(request, requested) {
		http.NotFound(response, request)
		return
	}
	canonical, err := app.canonicalRPCSessionPath(request, requested)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return
	}
	requested = canonical
	path := requested
	result := rpc.StopResult{}
	abortClient := func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		if actions.ActiveBashCommand() != "" {
			_, err = actions.AbortBash(request.Context())
		} else {
			_, err = actions.Abort(request.Context())
		}
		return err
	}
	err = nil
	if app.rpcClients.Active(requested) {
		err = app.withSynchronizedInterruptClient(request, requested, abortClient)
	} else {
		var matched string
		matched, err = app.abortMatchingPending(request, requested, abortClient)
		if matched != "" {
			path = matched
		} else if err == nil {
			err = app.withSynchronizedInterruptClient(request, requested, abortClient)
		}
	}
	result, err = rpc.StopResultFor(err)
	if err != nil && app.writeActionRPCError(response, err) {
		return
	}
	if wantsJSON(request) {
		payload := map[string]any{"ok": true, "session": path}
		if result.Forced {
			payload["forced"] = true
		}
		if result.Stopping {
			payload["stopping"] = true
		}
		status := http.StatusOK
		if result.Stopping {
			status = http.StatusAccepted
		}
		writeJSONStatus(response, status, payload)
		return
	}
	http.Redirect(response, request, app.sessionRedirectPath(request, path), http.StatusSeeOther)
}

func (app *application) compactSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	var result map[string]any
	err := app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		result, err = actions.Compact(request.Context(), strings.TrimSpace(request.FormValue("instructions")))
		return err
	})
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	_ = result
	http.Redirect(response, request, app.sessionRedirectPath(request, path), http.StatusSeeOther)
}

func (app *application) modelSettings(response http.ResponseWriter, request *http.Request) {
	path, ok := app.actionSessionPath(response, request, request.URL.Query().Get("session"), true)
	if !ok {
		return
	}
	var stateResponse, modelsResponse map[string]any
	err := app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		stateResponse, err = client.GetState(request.Context())
		if err == nil {
			modelsResponse, err = actions.GetAvailableModels(request.Context())
		}
		return err
	})
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	state := successfulData(stateResponse)
	models, valid := successfulData(modelsResponse)["models"].([]any)
	if state == nil || !valid {
		writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Could not load model settings"})
		return
	}
	writeJSON(response, map[string]any{"state": state, "models": models})
}

func (app *application) setModelSettings(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	provider, modelID, thinking := strings.TrimSpace(request.FormValue("provider")), strings.TrimSpace(request.FormValue("model")), strings.TrimSpace(request.FormValue("thinking"))
	if provider == "" {
		writeText(response, http.StatusBadRequest, "Provider cannot be empty")
		return
	}
	if len(provider) > providerIDBytes {
		writeText(response, http.StatusBadRequest, "Provider is too long")
		return
	}
	if modelID == "" {
		writeText(response, http.StatusBadRequest, "Model cannot be empty")
		return
	}
	if len(modelID) > modelIDBytes {
		writeText(response, http.StatusBadRequest, "Model is too long")
		return
	}
	if !thinkingLevels[thinking] {
		writeText(response, http.StatusBadRequest, "Invalid thinking level")
		return
	}
	var stateResponse map[string]any
	err := app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		if client.Busy() {
			return errSessionBusy
		}
		setting, err := actions.SetModel(request.Context(), provider, modelID)
		if err != nil {
			return err
		}
		if !successfulRPCResponse(setting) {
			return &rpcSettingError{response: setting}
		}
		setting, err = actions.SetThinkingLevel(request.Context(), thinking)
		if err != nil {
			return err
		}
		if !successfulRPCResponse(setting) {
			return &rpcSettingError{response: setting}
		}
		stateResponse, err = client.GetState(request.Context())
		return err
	})
	if app.writeSettingError(response, err) {
		return
	}
	state := successfulData(stateResponse)
	model, modelOK := state["model"].(map[string]any)
	confirmedThinking, thinkingOK := state["thinkingLevel"].(string)
	if state == nil || !modelOK || !thinkingOK {
		writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Could not confirm model settings"})
		return
	}
	writeJSON(response, map[string]any{"model": model, "thinking": confirmedThinking})
}

func (app *application) cycleThinking(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	var result map[string]any
	err := app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		if client.Busy() {
			return errSessionBusy
		}
		result, err = actions.CycleThinkingLevel(request.Context())
		return err
	})
	if app.writeSettingError(response, err) {
		return
	}
	if !successfulRPCResponse(result) {
		app.writeRPCSettingFailure(response, result)
		return
	}
	data := responseData(result)
	level := ""
	if data != nil {
		level = stringFromAny(data["level"])
		if level != "" && !thinkingLevels[level] {
			writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Could not change thinking level"})
			return
		}
	}
	var value any
	if level != "" {
		value = level
	}
	writeJSON(response, map[string]any{"thinking": value})
}

func (app *application) newSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	if !app.knownOrPendingSession(request, path) {
		http.NotFound(response, request)
		return
	}
	newPath, err := app.startNewSession(request, app.currentSessionCWD(path))
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	app.redirectToNewSession(response, request, newPath, "")
}

func (app *application) newSessionAtCWD(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	cwd, message, valid := validatedCWD(request.FormValue("cwd"), app.config.Home)
	if !valid {
		if wantsJSON(request) {
			writeJSONStatus(response, http.StatusUnprocessableEntity, map[string]any{"valid": false, "error": message})
		} else {
			writeText(response, http.StatusUnprocessableEntity, message)
		}
		return
	}
	request.Form.Del("project")
	newPath, err := app.startNewSession(request, cwd)
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	app.redirectToNewSession(response, request, newPath, "")
}

func (app *application) validateSessionCWD(response http.ResponseWriter, request *http.Request) {
	cwd, message, valid := validatedCWD(request.URL.Query().Get("cwd"), app.config.Home)
	if !valid {
		writeJSONStatus(response, http.StatusUnprocessableEntity, map[string]any{"valid": false, "error": message})
		return
	}
	writeJSON(response, map[string]any{"valid": true, "cwd": cwd})
}

func (app *application) browseSessionCWD(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	raw := request.URL.Query().Get("cwd")
	cwd, message, valid := validatedCWD(raw, app.config.Home)
	payload := map[string]any{"valid": valid, "directories": []string{}}
	if valid {
		payload["cwd"] = cwd
	} else {
		payload["error"] = message
	}
	if !utf8.ValidString(raw) || strings.TrimSpace(raw) == "" {
		writeJSON(response, payload)
		return
	}
	expanded, err := filepath.Abs(expandHomePath(strings.TrimSpace(raw), app.config.Home))
	if err != nil {
		writeJSON(response, payload)
		return
	}
	parent, prefix := expanded, ""
	if stat, statErr := os.Stat(expanded); statErr != nil || !stat.IsDir() {
		parent, prefix = filepath.Dir(expanded), filepath.Base(expanded)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		writeJSON(response, payload)
		return
	}
	directories := make([]string, 0, cwdSuggestionLimit)
	for _, entry := range entries {
		name := entry.Name()
		if !utf8.ValidString(name) || strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") || !strings.HasPrefix(name, prefix) {
			continue
		}
		path := filepath.Join(parent, name)
		if stat, statErr := os.Stat(path); statErr == nil && stat.IsDir() && directoryAccessible(path) {
			directories = append(directories, path)
		}
	}
	sort.Strings(directories)
	if len(directories) > cwdSuggestionLimit {
		directories = directories[:cwdSuggestionLimit]
	}
	payload["directories"] = directories
	writeJSON(response, payload)
}

func (app *application) forkMessages(response http.ResponseWriter, request *http.Request) {
	path, ok := app.actionSessionPath(response, request, request.URL.Query().Get("session"), true)
	if !ok {
		return
	}
	var result map[string]any
	err := app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		result, err = actions.GetForkMessages(request.Context())
		return err
	})
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	messages, _ := responseData(result)["messages"].([]any)
	if messages == nil {
		messages = []any{}
	}
	writeJSON(response, map[string]any{"messages": messages})
}

func (app *application) treeEntries(response http.ResponseWriter, request *http.Request) {
	path, ok := app.actionSessionPath(response, request, request.URL.Query().Get("session"), true)
	if !ok {
		return
	}
	filter := request.URL.Query().Get("filter")
	if filter != "" && !treeFilters[filter] {
		writeText(response, http.StatusBadRequest, "Invalid tree filter")
		return
	}
	var result map[string]any
	err := app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		result, err = actions.TreeSnapshot(request.Context(), filter)
		return err
	})
	if app.writeSettingError(response, err) {
		return
	}
	if !successfulRPCResponse(result) {
		app.writeRPCSettingFailure(response, result)
		return
	}
	snapshot := responseData(result)
	_, entriesOK := snapshot["entries"].([]any)
	_, settingsOK := snapshot["settings"].(map[string]any)
	if !entriesOK || !settingsOK || !treeFilters[stringFromAny(snapshot["filter"])] {
		writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Could not load session tree"})
		return
	}
	writeJSON(response, snapshot)
}

func (app *application) navigateTree(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	entryID := request.FormValue("entry_id")
	if entryID == "" {
		writeText(response, http.StatusBadRequest, "Tree entry cannot be empty")
		return
	}
	if len(entryID) > treeEntryIDBytes {
		writeText(response, http.StatusBadRequest, "Tree entry id is too long")
		return
	}
	summary := request.FormValue("summary_mode")
	if summary == "" {
		summary = request.FormValue("summary")
	}
	if summary == "" {
		summary = "none"
	}
	if summary != "none" && summary != "default" && summary != "custom" {
		writeText(response, http.StatusBadRequest, "Invalid summary mode")
		return
	}
	instructions := strings.TrimSpace(request.FormValue("custom_instructions"))
	if instructions == "" {
		instructions = strings.TrimSpace(request.FormValue("instructions"))
	}
	if summary == "custom" && instructions == "" {
		writeText(response, http.StatusBadRequest, "Custom summary instructions cannot be empty")
		return
	}
	if len(instructions) > treeInstructionsBytes {
		writeText(response, http.StatusBadRequest, "Custom summary instructions are too long")
		return
	}
	var result map[string]any
	err := app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		if client.Busy() {
			return errSessionBusy
		}
		if summary != "custom" {
			instructions = ""
		}
		result, err = actions.NavigateTree(request.Context(), entryID, summary, instructions)
		return err
	})
	if app.writeSettingError(response, err) {
		return
	}
	if !successfulRPCResponse(result) {
		app.writeRPCSettingFailure(response, result)
		return
	}
	data := responseData(result)
	payload := map[string]any{"session": path, "redirect": app.sessionRedirectPath(request, path), "cancelled": data["cancelled"] == true}
	if editor, valid := data["editorText"].(string); valid {
		if len(editor) > extensionValueBytes {
			writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Extension editor response is too long"})
			return
		}
		payload["editorText"] = editor
	}
	writeJSON(response, payload)
}

func (app *application) setTreeLabel(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	entryID, label := request.FormValue("entry_id"), strings.TrimSpace(request.FormValue("label"))
	if entryID == "" {
		writeText(response, http.StatusBadRequest, "Tree entry cannot be empty")
		return
	}
	if len(entryID) > treeEntryIDBytes {
		writeText(response, http.StatusBadRequest, "Tree entry id is too long")
		return
	}
	if len(label) > treeLabelBytes {
		writeText(response, http.StatusBadRequest, "Label is too long")
		return
	}
	var result map[string]any
	err := app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		if client.Busy() {
			return errSessionBusy
		}
		result, err = actions.SetTreeLabel(request.Context(), entryID, label)
		return err
	})
	if app.writeSettingError(response, err) {
		return
	}
	if !successfulRPCResponse(result) {
		app.writeRPCSettingFailure(response, result)
		return
	}
	payload := map[string]any{"entryId": entryID, "label": nil}
	if label != "" {
		payload["label"] = label
	}
	writeJSON(response, payload)
}

func (app *application) forkSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	entryID := request.FormValue("entry_id")
	if entryID == "" {
		writeText(response, http.StatusBadRequest, "Fork entry cannot be empty")
		return
	}
	if len(entryID) > treeEntryIDBytes {
		writeText(response, http.StatusBadRequest, "Fork entry id is too long")
		return
	}
	app.branchFromAction(response, request, path, false, entryID)
}

func (app *application) cloneSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	app.branchFromAction(response, request, path, true, "")
}

func (app *application) extensionUIResponse(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), false)
	if !ok {
		return
	}
	id := request.FormValue("id")
	if id == "" || len(id) > extensionRequestIDBytes {
		writeText(response, http.StatusBadRequest, "Missing extension UI request id")
		return
	}
	cancelled := request.FormValue("cancelled") == "true"
	var confirmed *bool
	if _, exists := request.Form["confirmed"]; exists {
		value := request.FormValue("confirmed") == "true"
		confirmed = &value
	}
	var value *string
	if _, exists := request.Form["value"]; exists {
		item := request.FormValue("value")
		value = &item
	}
	if value != nil && len(*value) > extensionValueBytes {
		writeText(response, http.StatusBadRequest, "Extension UI response is too long")
		return
	}
	if !cancelled && confirmed == nil && value == nil {
		writeText(response, http.StatusBadRequest, "Invalid extension UI response")
		return
	}
	var result map[string]any
	called := false
	path, ok = app.resolveActionPendingPath(response, request, path)
	if !ok {
		return
	}
	err := app.rpcClients.WithExistingClient(request.Context(), path, true, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		called = true
		result, err = actions.ExtensionUIResponse(request.Context(), id, value, confirmed, cancelled)
		return err
	})
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	if !called {
		writeText(response, http.StatusNotFound, "No active Pi session")
		return
	}
	if !successfulRPCResponse(result) {
		app.writeRPCSettingFailure(response, result)
		return
	}
	writeJSON(response, map[string]any{"ok": true, "session": path})
}

func (app *application) takeOverSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	path, ok = app.resolveActionPendingPath(response, request, path)
	if !ok {
		return
	}
	state, err := app.synchronizer.TakeOver(request.Context(), path)
	if err != nil {
		if errors.Is(err, sessions.ErrSyncBusy) {
			writeJSONStatus(response, http.StatusConflict, map[string]any{"error": "Wait for the gateway task to finish before taking over."})
			return
		}
		if app.writeActionRPCError(response, err) {
			return
		}
		http.Error(response, "Unable to take over session", http.StatusInternalServerError)
		return
	}
	writeJSON(response, map[string]any{"ok": true, "session": path, "session_sync": map[string]any{"mode": state.Mode, "revision": state.Revision}})
}

func (app *application) branchFromAction(response http.ResponseWriter, request *http.Request, previous string, clone bool, entryID string) {
	resolved, unlock, err := app.lockResolvedImagePromptPath(request, previous)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return
	}
	defer unlock()
	previous = resolved
	cwd := app.currentSessionCWD(previous)
	_, wasPending := app.pendingSessions.CWD(previous)
	var mover rpc.SessionClientMover = app.rpcClients
	if _, err := os.Stat(previous); err == nil {
		mover = app.synchronizer
	}
	newPath, actionResponse, err := rpc.BranchSession(request.Context(), previous, cwd, mover, app.pendingSessions, func(client rpc.RPCClient) (map[string]any, error) {
		actions, err := checkedActionClient(client)
		if err != nil {
			return nil, err
		}
		if clone {
			return actions.CloneSession(request.Context())
		}
		return actions.Fork(request.Context(), entryID)
	}, func(path string) (string, error) {
		configured, ok := sessions.ConfiguredSessionPath(app.config.SessionsRoot, path)
		if !ok {
			return "", errors.New("Pi reported a session path outside the configured sessions root")
		}
		return configured, nil
	}, func(from, to string) (func() error, error) {
		if app.ownsSession != nil && !app.ownsSession(request, from) {
			return nil, errors.New("pending session is not owned by the requester")
		}
		claimed := false
		if app.claimSession != nil {
			var err error
			claimed, err = app.claimSession(request, to)
			if err != nil {
				return nil, err
			}
		}
		var attachmentRollback func() error
		if wasPending {
			var err error
			attachmentRollback, err = (sessions.AttachmentStore{Root: app.config.AttachmentsRoot}).Migrate(from, to)
			if err != nil {
				if claimed && app.releaseSession != nil {
					err = errors.Join(err, app.releaseSession(request, to))
				}
				return nil, err
			}
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
	})
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	if !successfulRPCResponse(actionResponse) {
		app.writeRPCFailure(response, request, actionResponse, true)
		return
	}
	if newPath != previous {
		app.synchronizer.Forget(previous)
	}
	if responseData(actionResponse)["cancelled"] == true {
		if wantsJSON(request) {
			writeJSONStatus(response, http.StatusConflict, map[string]any{"cancelled": true, "session": previous})
		} else {
			http.Redirect(response, request, app.sessionRedirectPath(request, previous), http.StatusSeeOther)
		}
		return
	}
	if wantsJSON(request) {
		payload := map[string]any{"session": newPath, "redirect": app.sessionRedirectPath(request, newPath)}
		if !clone {
			if text, valid := responseData(actionResponse)["text"].(string); valid {
				payload["text"] = text
			}
		}
		writeJSON(response, payload)
		return
	}
	http.Redirect(response, request, app.sessionRedirectPath(request, newPath), http.StatusSeeOther)
}

func (app *application) startNewSession(request *http.Request, cwd string) (string, error) {
	if app.newRPCClient == nil {
		return "", errors.New("new Pi RPC client factory is unavailable")
	}
	return rpc.StartNewSession(request.Context(), cwd, app.config.SessionsRoot, app.newRPCClient, app.rpcClients, app.pendingSessions, func(path string) (string, func() error, error) {
		path, ok := sessions.ConfiguredSessionPath(app.config.SessionsRoot, path)
		if !ok {
			return "", nil, errors.New("Pi reported a session path outside the configured sessions root")
		}
		if app.claimSession == nil {
			return path, nil, nil
		}
		claimed, err := app.claimSession(request, path)
		if err != nil {
			return "", nil, err
		}
		return path, func() error {
			if !claimed {
				return nil
			}
			if app.releaseSession == nil {
				return nil
			}
			return app.releaseSession(request, path)
		}, nil
	})
}

func (app *application) redirectToNewSession(response http.ResponseWriter, request *http.Request, path, command string) {
	redirect := app.sessionRedirectPath(request, path)
	if wantsJSON(request) {
		payload := map[string]any{"session": path, "redirect": redirect}
		if command != "" {
			payload["command"] = command
		}
		writeJSON(response, payload)
		return
	}
	http.Redirect(response, request, redirect, http.StatusSeeOther)
}

func (app *application) actionSessionPath(response http.ResponseWriter, request *http.Request, raw string, requireAvailable bool) (string, bool) {
	path, ok := app.requireOwnedSession(response, request, raw)
	if !ok {
		return "", false
	}
	canonical, err := app.canonicalRPCSessionPath(request, path)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return "", false
	}
	available := !requireAvailable || app.commandSessionAvailable(canonical)
	if !available {
		http.NotFound(response, request)
		return "", false
	}
	return canonical, true
}

func (app *application) requireOwnedSession(response http.ResponseWriter, request *http.Request, path string) (string, bool) {
	if path == "" || len(path) > maximumSessionPathBytes || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		http.NotFound(response, request)
		return "", false
	}
	if app.ownsSession != nil && !app.ownsSession(request, path) {
		http.NotFound(response, request)
		return "", false
	}
	return path, true
}

func (app *application) resolveActionPendingPath(response http.ResponseWriter, request *http.Request, path string) (string, bool) {
	resolved, _, err := app.resolveOwnedPendingPath(request, path)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return "", false
	}
	return resolved, true
}

func (app *application) knownOrPendingSession(request *http.Request, path string) bool {
	if _, ok := app.pendingSessions.CWD(path); ok {
		return true
	}
	resolved, _, err := app.resolveOwnedPendingPath(request, path)
	if err != nil {
		return false
	}
	path = resolved
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	_, ok := store.Session(path)
	return ok
}

func (app *application) currentSessionCWD(path string) string {
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	if session, ok := store.Session(path); ok {
		return session.CWD
	}
	if cwd, ok := app.pendingSessions.CWD(path); ok {
		return cwd
	}
	return filepath.Dir(path)
}

func (app *application) withSynchronizedBashClient(request *http.Request, path string, call func(rpc.RPCClient) error) error {
	resolved, _, err := app.resolveOwnedPendingPath(request, path)
	if err != nil {
		return err
	}
	path = resolved
	ctx := request.Context()
	if _, err := os.Stat(path); err == nil {
		return app.synchronizer.WithBashClient(ctx, path, call)
	}
	return app.rpcClients.WithBashClient(ctx, path, call)
}

func (app *application) withSynchronizedInterruptClient(request *http.Request, path string, call func(rpc.RPCClient) error) error {
	resolved, _, err := app.resolveOwnedPendingPath(request, path)
	if err != nil {
		return err
	}
	path = resolved
	ctx := request.Context()
	if _, err := os.Stat(path); err == nil {
		return app.synchronizer.WithInterruptClient(ctx, path, call)
	}
	return app.rpcClients.WithInterruptClient(ctx, path, call)
}

func (app *application) abortMatchingPending(request *http.Request, requested string, abort func(rpc.RPCClient) error) (string, error) {
	ctx := request.Context()
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	session, known := store.Session(requested)
	if !known {
		return "", nil
	}
	unavailable := false
	for _, pending := range app.pendingSessions.Entries() {
		if app.ownsSession != nil && !app.ownsSession(request, pending.Path) {
			continue
		}
		if pending.CWD != session.CWD {
			continue
		}
		matched := false
		err := app.rpcClients.WithExistingInterruptClient(ctx, pending.Path, func(client rpc.RPCClient) error {
			actions, actionErr := checkedActionClient(client)
			if actionErr != nil {
				return actionErr
			}
			state, stateErr := actions.GetStateForInterrupt(ctx)
			if stateErr != nil {
				unavailable = true
				return nil
			}
			if sessionFileFrom(state) == requested {
				matched = true
				return abort(client)
			}
			return nil
		})
		if err != nil {
			unavailable = true
			continue
		}
		if matched {
			return pending.Path, nil
		}
	}
	if unavailable {
		return "", &pendingIdentificationError{}
	}
	return "", nil
}

type pendingIdentificationError struct{}

func (*pendingIdentificationError) Error() string {
	return "Could not identify the active pending Pi session; try stopping it again from its current page"
}

type rpcSettingError struct{ response map[string]any }

func (err *rpcSettingError) Error() string {
	return rpcErrorMessage(err.response, "Setting could not be changed")
}

var errSessionBusy = errors.New("session is busy")

func (app *application) writeSettingError(response http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errSessionBusy) {
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": "Session is busy"})
		return true
	}
	var setting *rpcSettingError
	if errors.As(err, &setting) {
		app.writeRPCSettingFailure(response, setting.response)
		return true
	}
	return app.writeActionRPCError(response, err)
}

func (app *application) writeActionRPCError(response http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var pending *pendingIdentificationError
	if errors.As(err, &pending) {
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": pending.Error()})
		return true
	}
	if errors.Is(err, rpc.ErrBashPending) || errors.Is(err, rpc.ErrBashAlreadyRunning) {
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": "A bash command is already running for this session"})
		return true
	}
	if app.writeRPCError(response, err) {
		return true
	}
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, rpc.ErrProcessExited) {
		writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Pi RPC client disconnected"})
		return true
	}
	http.Error(response, "Pi RPC request failed", http.StatusInternalServerError)
	return true
}

func (app *application) writeRPCFailure(response http.ResponseWriter, request *http.Request, rpcResponse map[string]any, setting bool) {
	message := rpcErrorMessage(rpcResponse, "Prompt failed to send")
	if setting {
		message = rpcErrorMessage(rpcResponse, "Setting could not be changed")
	}
	if wantsJSON(request) {
		writeJSONStatus(response, http.StatusUnprocessableEntity, map[string]any{"success": false, "error": message})
	} else {
		writeText(response, http.StatusUnprocessableEntity, message)
	}
}

func (app *application) writeRPCSettingFailure(response http.ResponseWriter, rpcResponse map[string]any) {
	writeJSONStatus(response, http.StatusUnprocessableEntity, map[string]any{"success": false, "error": rpcErrorMessage(rpcResponse, "Setting could not be changed")})
}

func (app *application) writeRequestError(response http.ResponseWriter, request *http.Request, status int, message string) {
	if wantsJSON(request) {
		writeJSONStatus(response, status, map[string]any{"error": message})
	} else {
		writeText(response, status, message)
	}
}

func (app *application) writePromptResult(response http.ResponseWriter, request *http.Request, path string, values map[string]any) {
	redirect := app.sessionRedirectPath(request, path)
	if wantsJSON(request) {
		payload := map[string]any{"session": path, "redirect": redirect}
		for key, value := range values {
			payload[key] = value
		}
		writeJSON(response, payload)
		return
	}
	http.Redirect(response, request, redirect, http.StatusSeeOther)
}

func (app *application) sessionRedirectPath(request *http.Request, path string) string {
	values := url.Values{"session": []string{path}}
	for _, key := range []string{"project", "session_search", "session_only"} {
		if value := request.FormValue(key); value != "" && (key != "session_only" || value == "1") {
			values.Set(key, value)
		}
	}
	return "/?" + values.Encode()
}

func wantsJSON(request *http.Request) bool {
	return strings.Contains(request.Header.Get("Accept"), "application/json")
}

func successfulRPCResponse(response map[string]any) bool {
	return response != nil && response["success"] == true
}

func responseData(response map[string]any) map[string]any {
	if nested, ok := response["data"].(map[string]any); ok {
		return nested
	}
	return response
}

func rpcErrorMessage(response map[string]any, fallback string) string {
	message := strings.TrimSpace(stringFromAny(response["error"]))
	if message == "" {
		return fallback
	}
	return message
}

func uploadedPromptImages(response http.ResponseWriter, request *http.Request) ([]*multipart.FileHeader, bool) {
	if request.MultipartForm == nil {
		return nil, true
	}
	files := slices.Clone(request.MultipartForm.File["images"])
	files = append(files, request.MultipartForm.File["images[]"]...)
	if err := prompts.ValidateUploadedImages(files); err != nil {
		writeText(response, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return files, true
}

func messageWithAttachmentPaths(message string, paths []string) string {
	if len(paths) == 0 {
		return message
	}
	parts := []string{}
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		parts = append(parts, trimmed)
	}
	parts = append(parts, strings.Join(paths, "\n"))
	return strings.Join(parts, "\n\n")
}

func validatedCWD(raw, home string) (string, string, bool) {
	if !utf8.ValidString(raw) {
		return "", "Path must be an existing directory.", false
	}
	cwd := strings.TrimSpace(raw)
	if cwd == "" {
		return "", "Enter an existing directory.", false
	}
	expanded, err := filepath.Abs(expandHomePath(cwd, home))
	if err != nil {
		return "", "Path must be an existing directory.", false
	}
	stat, err := os.Stat(expanded)
	if err != nil || !stat.IsDir() {
		return "", "Path must be an existing directory.", false
	}
	if !directoryAccessible(expanded) {
		return "", "Directory is not accessible.", false
	}
	real, err := filepath.EvalSymlinks(expanded)
	if err != nil {
		return "", "Path must be an existing directory.", false
	}
	return real, "", true
}

func expandHomePath(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return path
}
