package server

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
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
	exportFilenameBytes     = 255
	sessionNameBytes        = 4 << 10
)

var errDeleteRunning = errors.New("session is running")

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
	mux.HandleFunc("POST /sessions/rename", app.renameSession)
	mux.HandleFunc("POST /sessions/delete", app.deleteSession)
	mux.HandleFunc("GET /sessions/model_settings", app.modelSettings)
	mux.HandleFunc("POST /sessions/model_settings", app.setModelSettings)
	mux.HandleFunc("POST /sessions/cycle_thinking", app.cycleThinking)
	mux.HandleFunc("GET /sessions/fork_messages", app.forkMessages)
	mux.HandleFunc("GET /sessions/tree_entries", app.treeEntries)
	mux.HandleFunc("POST /sessions/tree", app.navigateTree)
	mux.HandleFunc("POST /sessions/tree/label", app.setTreeLabel)
	mux.HandleFunc("POST /sessions/fork", app.forkSession)
	mux.HandleFunc("POST /sessions/clone", app.cloneSession)
	mux.HandleFunc("POST /sessions/export", app.exportSession)
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
	path, releasePrompt, err := app.promptAdmissions.prompt(request.Context(), func() (string, error) {
		resolved, _, err := app.resolveOwnedPendingPath(request, path)
		return resolved, err
	})
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	defer releasePrompt()
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
	command := prompts.SlashCommand{}
	if behavior != "follow_up" {
		command = prompts.ParseSlashCommand(message)
	}
	if command.Type == "login" || command.Type == "logout" {
		guidance := map[string]string{
			"login":  "`/login` isn’t available in Gripi. Run `/login` in the Pi CLI, then restart the Gripi gateway to load the new credentials.",
			"logout": "`/logout` isn’t available in Gripi. Run `/logout` in the Pi CLI, then restart the Gripi gateway to reload credentials.",
		}
		app.writePromptResult(response, request, path, map[string]any{"command": command.Type, "message": guidance[command.Type]})
		return
	}
	if command.Type == "fork" || command.Type == "tree" || command.Type == "model" {
		app.writePromptResult(response, request, path, map[string]any{"command": command.Type})
		return
	}
	if command.Type == "new" || command.Type == "clone" {
		app.replaceSessionFromAction(response, request, path, command.Type, "")
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
	var extensionCommandWithImages, handledSlashCommand, compactingAfterHandledCommand, runningAfterHandledCommand bool
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
		if extensionCommandWithImages || len(imageFiles) == 0 || len(rpcImages) > 0 || behavior == "" && command.Type != "" {
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
		if len(imageFiles) > 0 && app.extensionSlashCommand(request, client, rpcMessage) {
			extensionCommandWithImages = true
		}
		if err := prepareImages(); err != nil {
			return err
		}
		switch command.Type {
		case "name":
			if command.Name != "" {
				rpcResponse, err = actions.SetSessionName(request.Context(), command.Name)
			} else {
				rpcResponse, err = client.GetState(request.Context())
			}
		case "compact":
			rpcResponse, err = actions.Compact(request.Context(), command.Instructions)
		case "reload":
			rpcResponse, err = actions.Reload(request.Context())
		default:
			switch behavior {
			case "steer":
				rpcResponse, err = actions.PromptWithBehavior(request.Context(), rpcMessage, rpcImages, "steer")
			case "follow_up":
				rpcResponse, err = actions.PromptWithBehavior(request.Context(), rpcMessage, rpcImages, "followUp")
			default:
				rpcResponse, err = actions.Prompt(request.Context(), rpcMessage, rpcImages)
			}
		}
		if err == nil {
			err = recordImages()
		}
		return err
	}
	if behavior != "" && command.Type == "" {
		if blocked := app.synchronizer.KnownBlocked(path); blocked != nil {
			err = &sessions.SyncBlockedError{Mode: blocked.Mode, Message: app.synchronizer.Message(*blocked)}
		} else {
			queued, handled := false, false
			path, ok = app.resolveActionPendingPath(response, request, path)
			if !ok {
				return
			}
			err = app.rpcClients.WithActiveClient(request.Context(), path, true, func(client rpc.RPCClient) error {
				actions, actionErr := checkedActionClient(client)
				if actionErr != nil {
					return actionErr
				}
				nativeBehavior := "steer"
				if behavior == "follow_up" {
					nativeBehavior = "followUp"
				}
				deferringCompactionPrompts := client.Compacting()
				if state, valid := client.(interface{ DeferringCompactionPrompts() bool }); valid {
					deferringCompactionPrompts = state.DeferringCompactionPrompts()
				}
				extensionSlashCommand := (deferringCompactionPrompts || len(imageFiles) > 0) && app.extensionSlashCommand(request, client, rpcMessage)
				extensionCommandWithImages = extensionSlashCommand && len(imageFiles) > 0
				if err := prepareImages(); err != nil {
					return err
				}
				if deferringCompactionPrompts && extensionSlashCommand {
					rpcResponse, actionErr = actions.PromptWithBehavior(request.Context(), rpcMessage, rpcImages, nativeBehavior)
					handled = actionErr == nil
					handledSlashCommand = handled
					compactingAfterHandledCommand = client.Compacting()
					runningAfterHandledCommand = client.AgentRunning()
				} else {
					rpcResponse, queued, actionErr = actions.QueueCompactionPrompt(request.Context(), rpcMessage, rpcImages, nativeBehavior)
				}
				if actionErr == nil && (queued || handled) {
					actionErr = recordImages()
				}
				return actionErr
			})
			if err == nil && !queued && !handled {
				err = app.withSynchronizedClient(request, path, call)
			}
		}
	} else {
		err = app.withSynchronizedClient(request, path, call)
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
	}
	if behavior != "" && rpcResponse["compacting"] == true {
		payload["queued_after_compaction"] = true
	}
	if handledSlashCommand {
		payload["compacting"] = compactingAfterHandledCommand
		payload["running"] = runningAfterHandledCommand
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
	if command.Type == "" && strings.HasPrefix(strings.TrimSpace(message), "/") && !strings.ContainsAny(message, "\r\n") {
		var state map[string]any
		if app.rpcClients.WithExistingClient(request.Context(), path, true, func(client rpc.RPCClient) error {
			var stateErr error
			state, stateErr = client.GetState(request.Context())
			return stateErr
		}) == nil {
			data := responseData(state)
			streaming, streamingKnown := data["isStreaming"].(bool)
			compacting, compactingKnown := data["isCompacting"].(bool)
			if streamingKnown {
				payload["running"] = streaming
			}
			if compactingKnown {
				payload["compacting"] = compacting
			}
		}
	}
	app.writePromptResult(response, request, path, payload)
}

func (app *application) extensionSlashCommand(request *http.Request, client rpc.RPCClient, message string) bool {
	if !strings.HasPrefix(message, "/") {
		return false
	}
	name := strings.TrimPrefix(strings.SplitN(message, " ", 2)[0], "/")
	response, err := client.GetCommands(request.Context())
	if err != nil {
		return false
	}
	for _, command := range rpc.CommandsFrom(response) {
		if command["name"] == name && command["source"] == "extension" {
			return true
		}
	}
	return false
}

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
	queuedText := ""
	stoppedQueuedRun := false
	if _, statErr := os.Stat(requested); statErr == nil {
		if client := app.rpcClients.Client(requested); client != nil {
			var queued map[string][]string

			retire := func() (bool, error) {
				retired, messages, retireErr := app.rpcClients.RetireQueuedClient(requested, client)
				queued = messages
				return retired, retireErr
			}

			if app.synchronizer != nil {
				stoppedQueuedRun, err = app.synchronizer.RetireManagedClientIfAvailable(requested, retire)
			} else {
				stoppedQueuedRun, err = retire()
			}
			if err != nil {
				app.writeActionRPCError(response, err)
				return
			}
			if stoppedQueuedRun {
				queuedText = strings.Join(append(append([]string{}, queued["steering"]...), queued["followUp"]...), "\n\n")
				result.Forced = true
			}
		}
	}
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
	if !stoppedQueuedRun {
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
	}
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
		if queuedText != "" {
			payload["editorText"] = queuedText
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

func (app *application) renameSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	session, ok := app.persistedActionSession(response, request, request.FormValue("session"))
	if !ok {
		return
	}
	name := strings.TrimSpace(request.FormValue("name"))
	if name == "" || len(name) > sessionNameBytes {
		message := "Session name cannot be empty"
		if name != "" {
			message = "Session name is too long"
		}
		writeText(response, http.StatusBadRequest, message)
		return
	}

	if err := app.setSessionName(request, session.Path, name); app.writeSettingError(response, err) {
		return
	}
	writeJSON(response, map[string]any{"session": session.Path, "name": name})
}

func (app *application) setSessionName(request *http.Request, path, name string) error {
	wasActive := app.rpcClients.Active(path)
	var result map[string]any
	err := app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		result, err = actions.SetSessionName(request.Context(), name)
		return err
	})
	if err == nil && !successfulRPCResponse(result) {
		err = &rpcSettingError{response: result}
	}
	if !wasActive && err == nil {
		if closed, closeErr := app.rpcClients.CloseClientIfIdle(path); closeErr != nil {
			logInternalError("close renamed session client", closeErr)
		} else if closed {
			app.synchronizer.Forget(path)
		}
	}
	return err
}

func (app *application) deleteSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	session, ok := app.persistedActionSession(response, request, request.FormValue("session"))
	if !ok {
		return
	}
	app.pendingRemapMu.Lock()
	defer app.pendingRemapMu.Unlock()
	unlock := app.sessionMutationLocks.Lock(session.Path)
	defer unlock()

	if reason := app.deleteSessionBlockReason(request.FormValue("current_session"), session.Path); reason != "" {
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": reason})
		return
	}
	method, err := app.deletePersistedSession(request, session.Path)
	if errors.Is(err, errDeleteRunning) || errors.Is(err, sessions.ErrSyncBusy) {
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": "Cannot delete a running session"})
		return
	}
	if err != nil {
		writeInternalError(response, "delete session", err)
		return
	}
	writeJSON(response, map[string]any{"session": session.Path, "deleted": true, "method": method})
}

func (app *application) persistedActionSession(response http.ResponseWriter, request *http.Request, raw string) (*sessions.Session, bool) {
	path, ok := app.actionSessionPath(response, request, raw, true)
	if !ok {
		return nil, false
	}
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	session, persisted := store.Session(path)
	if !persisted {
		http.NotFound(response, request)
		return nil, false
	}
	return session, true
}

func (app *application) deleteSessionBlockReason(currentPath, targetPath string) string {
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	if current, found := store.Session(currentPath); found {
		currentPath = current.Path
	}
	if currentPath == targetPath {
		return "Cannot delete the current session"
	}
	if app.rpcClients.Busy(targetPath) || app.rpcClients.Compacting(targetPath) {
		return "Cannot delete a running session"
	}
	return ""
}

func (app *application) closeDeleteSessionClient(ctx context.Context, path string) error {
	if !app.rpcClients.Active(path) {
		return nil
	}
	// Prompt acceptance can precede agent_start delivery. The caller holds the
	// session's exclusive operation lock through this check, close, and deletion.
	err := app.rpcClients.WithExistingClient(ctx, path, false, func(client rpc.RPCClient) error {
		state, err := client.GetState(ctx)
		if err != nil {
			return err
		}
		if !successfulRPCResponse(state) {
			return &rpcSettingError{response: state}
		}
		data := responseData(state)
		if data["isStreaming"] == true || data["isCompacting"] == true {
			return errDeleteRunning
		}
		return nil
	})
	if err != nil {
		return err
	}
	closed, err := app.rpcClients.CloseClientIfIdle(path)
	if err != nil {
		return err
	}
	if !closed {
		return errDeleteRunning
	}
	return nil
}

func (app *application) deletePersistedSession(request *http.Request, path string) (string, error) {
	method := ""
	err := app.synchronizer.WithExclusiveOperation(path, func() error {
		if app.rpcClients.Busy(path) || app.rpcClients.Compacting(path) {
			return errDeleteRunning
		}
		if err := app.closeDeleteSessionClient(request.Context(), path); err != nil {
			return err
		}
		var err error
		method, err = sessions.DeleteSessionFile(path)
		if err == nil {
			app.cleanupDeletedSession(request, path)
		}
		return err
	})
	return method, err
}

func (app *application) cleanupDeletedSession(request *http.Request, path string) {
	app.sessionCache.Forget(path)
	app.synchronizer.Forget(path)
	app.pendingSessions.Forget(path)
	if err := app.gatewayState.Forget(path); err != nil {
		logInternalError("clean deleted session state", err)
	}
	attachments := sessions.AttachmentStore{Root: app.config.AttachmentsRoot, SessionsRoot: app.config.SessionsRoot}
	if err := attachments.Delete(path); err != nil {
		logInternalError("clean deleted session attachments", err)
	}
	if app.releaseSession != nil {
		if err := app.releaseSession(request, path); err != nil {
			logInternalError("release deleted session ownership", err)
		}
	}
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
	path, releaseNavigation, err := app.promptAdmissions.navigate(func() (string, error) {
		resolved, _, err := app.resolveOwnedPendingPath(request, path)
		return resolved, err
	})
	if app.writeSettingError(response, err) {
		return
	}
	defer releaseNavigation()
	if app.rpcClients.DeferringCompactionPrompts(path) {
		app.writeSettingError(response, errSessionBusy)
		return
	}
	restoredQueuedText := ""
	abortedRun := false
	err = app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		state, err := client.GetState(request.Context())
		if err != nil {
			return err
		}
		if !successfulRPCResponse(state) {
			return &rpcSettingError{response: state}
		}
		data := responseData(state)
		if data["isCompacting"] == true || client.Compacting() {
			return errSessionBusy
		}
		if data["isStreaming"] != true && !client.AgentRunning() {
			if client.Busy() {
				return errSessionBusy
			}
			return nil
		}
		queued := client.LiveSnapshot().QueuedMessages
		restoredQueuedText = strings.Join(append(append([]string{}, queued["steering"]...), queued["followUp"]...), "\n\n")
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		aborted, err := actions.Abort(request.Context())
		if err != nil {
			return err
		}
		if !successfulRPCResponse(aborted) {
			return &rpcSettingError{response: aborted}
		}
		abortedRun = true
		return nil
	})
	if app.writeSettingError(response, err) {
		return
	}
	if abortedRun {
		closed, err := app.rpcClients.CloseClientWithoutOperations(path)
		if err != nil {
			app.writeActionRPCError(response, err)
			return
		}
		if !closed {
			app.writeSettingError(response, errSessionBusy)
			return
		}
	}
	var result map[string]any
	err = app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		if summary != "custom" {
			instructions = ""
		}
		result, err = actions.NavigateTree(request.Context(), entryID, summary, instructions)
		return err
	})
	if err != nil && restoredQueuedText != "" {
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": "Could not navigate the session tree.", "editorText": restoredQueuedText})
		return
	}
	if app.writeSettingError(response, err) {
		return
	}
	if !successfulRPCResponse(result) {
		if restoredQueuedText != "" {
			writeJSONStatus(response, http.StatusUnprocessableEntity, map[string]any{"error": rpcErrorMessage(result, "Could not navigate the session tree."), "editorText": restoredQueuedText})
			return
		}
		app.writeRPCSettingFailure(response, result)
		return
	}
	data := responseData(result)
	payload := map[string]any{"session": path, "redirect": app.sessionRedirectPath(request, path), "cancelled": data["cancelled"] == true}
	if restoredQueuedText != "" {
		payload["editorText"] = restoredQueuedText
	} else if editor, valid := data["editorText"].(string); valid {
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
	app.replaceSessionFromAction(response, request, path, "fork", entryID)
}

func (app *application) cloneSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	app.replaceSessionFromAction(response, request, path, "clone", "")
}

func (app *application) exportSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	filename, ok := exportDownloadFilename(request.FormValue("filename"), path)
	if !ok {
		app.writeRequestError(response, request, http.StatusBadRequest, "Export filename is too long")
		return
	}
	temporary, err := os.CreateTemp("", "gripi-export-*.html")
	if err != nil {
		writeInternalError(response, "create temporary session export", err)
		return
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		writeInternalError(response, "close temporary session export", err)
		return
	}
	defer os.Remove(temporaryPath)

	var result map[string]any
	err = app.withSynchronizedClient(request, path, func(client rpc.RPCClient) error {
		actions, err := checkedActionClient(client)
		if err != nil {
			return err
		}
		result, err = actions.ExportHTML(request.Context(), temporaryPath)
		return err
	})
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	if !successfulRPCResponse(result) {
		app.writeRPCFailure(response, request, result, true)
		return
	}
	file, err := os.Open(temporaryPath)
	if err != nil {
		writeInternalError(response, "open temporary session export", err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeInternalError(response, "inspect temporary session export", err)
		return
	}

	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	response.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(response, request, filename, info.ModTime(), file)
}

func exportDownloadFilename(raw, sessionPath string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name != "" {
		name = path.Base(strings.ReplaceAll(name, `\`, "/"))
		name = strings.TrimSpace(strings.Map(func(character rune) rune {
			if unicode.IsControl(character) {
				return -1
			}
			return character
		}, name))
	}
	if name == "" || name == "." || name == "/" {
		base := strings.TrimSuffix(filepath.Base(sessionPath), filepath.Ext(sessionPath))
		if base == "" || base == "." {
			base = "session"
		}
		name = "pi-session-" + base
	}
	if !strings.EqualFold(filepath.Ext(name), ".html") {
		name += ".html"
	}
	return name, len(name) <= exportFilenameBytes
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
	state, err := app.synchronizer.TakeOver(request.Context(), path, func() error {
		store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
		session, found := store.Session(path)
		if !found {
			return errors.New("session disappeared during takeover")
		}
		if err := app.gatewayState.MarkExternalRead(path, session.AssistantResponseCount); err != nil {
			return err
		}
		return app.gatewayState.RememberProject(session.CWD)
	})
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

func (app *application) replaceSessionFromAction(response http.ResponseWriter, request *http.Request, previous, operation, entryID string) {
	resolved, unlock, err := app.lockResolvedImagePromptPath(request, previous)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return
	}
	defer unlock()
	app.pendingRemapMu.Lock()
	defer app.pendingRemapMu.Unlock()
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
		switch operation {
		case "new":
			return actions.NewSession(request.Context(), "")
		case "clone":
			return actions.CloneSession(request.Context())
		default:
			return actions.Fork(request.Context(), entryID)
		}
	}, func(path string) (string, error) {
		configured, ok := sessions.ConfiguredSessionPath(app.config.SessionsRoot, path)
		if !ok {
			return "", errors.New("Pi reported a session path outside the configured sessions root")
		}
		return configured, nil
	}, func(from, to string) (func() error, error) {
		if app.gatewayState != nil && app.gatewayState.SessionForgotten(to) {
			return nil, os.ErrNotExist
		}
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
		var stateRollback func() error
		if wasPending {
			var err error
			stateRollback, err = app.migratePendingSessionState(from, to, false)
			if err != nil {
				if claimed && app.releaseSession != nil {
					err = errors.Join(err, app.releaseSession(request, to))
				}
				return nil, err
			}
		}
		var tagRollback func() error
		if operation != "new" && app.gatewayState != nil {
			tagRollback, err = app.gatewayState.CopyTags(from, to)
			if err != nil {
				if stateRollback != nil {
					err = errors.Join(err, stateRollback())
				}
				if claimed && app.releaseSession != nil {
					err = errors.Join(err, app.releaseSession(request, to))
				}
				return nil, err
			}
		}
		return func() error {
			var stateErr error
			if tagRollback != nil {
				stateErr = tagRollback()
			}
			if stateRollback != nil {
				stateErr = errors.Join(stateErr, stateRollback())
			}
			var ownershipErr error
			if claimed && app.releaseSession != nil {
				ownershipErr = app.releaseSession(request, to)
			}
			return errors.Join(stateErr, ownershipErr)
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
		if operation == "fork" {
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
	if err := request.ParseForm(); err != nil {
		return "", err
	}
	names, err := sessions.NormalizeTags(request.PostForm["tags"])
	if err != nil {
		return "", err
	}
	if app.newRPCClient == nil {
		return "", errors.New("new Pi RPC client factory is unavailable")
	}
	path, err := rpc.StartNewSession(request.Context(), cwd, app.config.SessionsRoot, app.newRPCClient, app.rpcClients, app.pendingSessions, func(path string) (string, func() error, error) {
		path, ok := sessions.ConfiguredSessionPath(app.config.SessionsRoot, path)
		if !ok {
			return "", nil, errors.New("Pi reported a session path outside the configured sessions root")
		}
		claimed := false
		if app.claimSession != nil {
			claimed, err = app.claimSession(request, path)
			if err != nil {
				return "", nil, err
			}
		}
		var tagRollback func() error
		rollback := func() error {
			var rollbackErr error
			if tagRollback != nil {
				rollbackErr = tagRollback()
			}
			if claimed && app.releaseSession != nil {
				rollbackErr = errors.Join(rollbackErr, app.releaseSession(request, path))
			}
			return rollbackErr
		}
		if app.gatewayState != nil {
			tagRollback, err = app.gatewayState.SetTags(path, names)
			if err != nil {
				return "", nil, errors.Join(err, rollback())
			}
		}
		return path, rollback, nil
	})
	if err == nil && app.gatewayState != nil {
		err = app.gatewayState.RememberProject(cwd)
	}
	return path, err
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
		if app.writeRPCError(response, err) {
			return "", false
		}
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
			reported, found := store.Session(sessionFileFrom(state))
			if found && reported.Path == session.Path {
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
	if errors.Is(err, sessions.ErrInvalidTag) || errors.Is(err, sessions.ErrTooManyTags) {
		writeJSONStatus(response, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return true
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
	for _, key := range []string{"project", "session_search", "session_only", "tag"} {
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
