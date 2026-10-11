package server

import (
	"context"
	"encoding/json"
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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/prompts"
	"github.com/melounvitek/gripi/internal/sessions"
)

const (
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

func (app *application) registerActionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /prompt", app.prompt)
	mux.HandleFunc("POST /abort", app.abortSession)
	mux.HandleFunc("POST /clear_queue", app.clearQueue)
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
	composer := app.adapter.Composer(path)
	if composer.InternalCommand(message) {
		app.writeRequestError(response, request, http.StatusBadRequest, strings.Fields(message)[0]+" is a command that Gripi uses itself, so it cannot be sent as a message")
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
	// Deletion may have completed after actionSessionPath validated the request.
	if !app.commandSessionAvailable(path) {
		http.NotFound(response, request)
		return
	}
	if err := app.retireStaleRPCClient(request, path); err != nil {
		// Such as a model that the new variables have no key for.
		if refused := refusal(err); refused != nil {
			app.writeRPCFailure(response, request, refused.Message, false)
			return
		}
		app.writeActionRPCError(response, err)
		return
	}
	// In prompt mode the browser sends a shell command's text as a prompt.
	if command, shell := composer.ShellCommand(message); shell && request.FormValue("bash_mode") != "prompt" {
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
	command := agent.SlashCommand{}
	if behavior != "follow_up" {
		command = composer.SlashCommand(message)
	}
	if command.Unavailable != "" {
		app.writePromptResult(response, request, path, map[string]any{"command": command.Name, "message": command.Unavailable})
		return
	}
	if command.Name == "fork" || command.Name == "tree" || command.Name == "model" {
		app.writePromptResult(response, request, path, map[string]any{"command": command.Name})
		return
	}
	if command.Name == "new" || command.Name == "clone" {
		app.replaceSessionFromAction(response, request, path, command.Name, "")
		return
	}
	if len(imageFiles) > 0 && command.Name == "" {
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
	attachmentStore := sessions.AttachmentStore{Root: app.config.AttachmentsRoot, Keys: app.sessionStore}
	var result agent.PromptResult
	var currentName string
	var promptImages []agent.PromptImage
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
	prepareImages := func() ([]agent.PromptImage, error) {
		if len(promptImages) > 0 {
			return promptImages, nil
		}
		images, cleanup, err := prompts.PersistUploadedImages(imageFiles, filepath.Join(app.config.AttachmentsRoot, sessions.SessionHash(path)))
		if err != nil {
			return nil, err
		}
		cleanupImages = cleanup
		if request.MultipartForm != nil {
			_ = request.MultipartForm.RemoveAll()
			request.MultipartForm = nil
		}
		for _, image := range images {
			promptImages = append(promptImages, agent.PromptImage{Path: image.Path, MIMEType: image.MIMEType, Size: image.Size})
			attachmentPaths = append(attachmentPaths, image.Path)
			mimeTypes = append(mimeTypes, image.MIMEType)
		}
		return promptImages, nil
	}
	recordImages := func() error {
		if len(promptImages) == 0 {
			return nil
		}
		keepImages = true
		return attachmentStore.RecordPrompt(path, message, len(promptImages), submittedAt, attachmentPaths, mimeTypes)
	}
	prompt := agent.Prompt{Text: message}
	switch behavior {
	case "steer":
		prompt.Delivery = agent.Steer
	case "follow_up":
		prompt.Delivery = agent.FollowUp
	}
	if len(imageFiles) > 0 {
		prompt.Images = prepareImages
	}
	call := func(client agent.Client) error {
		var err error
		// A steered command saves its images as a prompt would, though it doesn't send them.
		if command.Name != "" && behavior != "" && len(imageFiles) > 0 {
			if _, err := prepareImages(); err != nil {
				return err
			}
		}
		switch command.Name {
		case "":
			result, err = client.Prompt(request.Context(), prompt)
		case "name":
			if command.Argument == "" {
				var state agent.State
				state, err = client.State(request.Context())
				currentName = state.Name
				break
			}
			var renamer agent.Renamer
			if renamer, err = capability[agent.Renamer](client); err == nil {
				err = renamer.Rename(request.Context(), command.Argument)
			}
		case "compact":
			var compacter agent.Compacter
			if compacter, err = capability[agent.Compacter](client); err == nil {
				err = compacter.Compact(request.Context(), command.Argument)
			}
			// The live events show a failed compaction, so the prompt only reports the command.
			if refusal(err) != nil {
				return nil
			}
		case "reload":
			var reloader agent.Reloader
			if reloader, err = capability[agent.Reloader](client); err == nil {
				err = reloader.Reload(request.Context())
			}
		}
		if err == nil {
			err = recordImages()
		}
		return err
	}
	if behavior != "" && command.Name == "" {
		if blocked := app.guard.KnownBlocked(path); blocked != nil {
			err = &agent.SyncBlockedError{Mode: blocked.Mode, Message: app.guard.Message(*blocked)}
		} else {
			deferred := false
			path, ok = app.resolveActionPendingPath(response, request, path)
			if !ok {
				return
			}
			// While a compaction holds the operation lane, the client may still take the prompt and deliver it later.
			err = app.rpcClients.WithActiveClient(request.Context(), path, true, func(client agent.Client) error {
				deferrer, ok := client.(agent.CompactionPromptDeferrer)
				if !ok {
					return nil
				}
				var deferErr error
				result, deferred, deferErr = deferrer.DeferPrompt(request.Context(), prompt)
				if deferErr == nil && deferred {
					deferErr = recordImages()
				}
				return deferErr
			})
			if err == nil && !deferred {
				err = app.withSynchronizedClient(request, path, call)
			}
		}
	} else {
		err = app.withSynchronizedClient(request, path, call)
	}
	if refused := refusal(err); refused != nil {
		if err := cleanupFailedImages(); err != nil {
			http.Error(response, "Unable to clean up prompt attachments", http.StatusInternalServerError)
			return
		}
		app.writeRPCFailure(response, request, refused.Message, command.Name != "")
		return
	}
	if err != nil {
		app.writeActionRPCError(response, errors.Join(err, cleanupFailedImages()))
		return
	}
	payload := map[string]any{}
	if behavior == "steer" {
		payload["steer"] = true
	}
	if behavior == "follow_up" {
		payload["follow_up"] = true
	}
	if result.QueuedAfterCompaction {
		payload["queued_after_compaction"] = true
	}
	if result.Disposition != "" {
		payload["disposition"] = string(result.Disposition)
	}
	if result.Activity != nil {
		payload["compacting"] = result.Activity.Compacting
		payload["running"] = result.Activity.Running
	}
	if command.Name != "" {
		payload["command"] = command.Name
		if command.Name == "name" && command.Argument != "" {
			payload["name"] = command.Argument
		}
		if command.Name == "name" && command.Argument == "" {
			if currentName == "" {
				payload["error"] = "Usage: /name <name>"
			} else {
				payload["name"], payload["current"] = currentName, true
			}
		}
	}
	if command.Name == "" && strings.HasPrefix(strings.TrimSpace(message), "/") && !strings.ContainsAny(message, "\r\n") {
		var state agent.State
		if app.rpcClients.WithExistingClient(request.Context(), path, true, func(client agent.Client) error {
			var stateErr error
			state, stateErr = client.State(request.Context())
			return stateErr
		}) == nil {
			payload["running"] = state.Streaming
			payload["compacting"] = state.Compacting
		}
	}
	app.writePromptResult(response, request, path, payload)
}

var errUnsupportedAction = errors.New("the agent does not support this action")

// capability finds a feature that only some agents have.
func capability[T any](client agent.Client) (T, error) {
	feature, ok := client.(T)
	if !ok {
		return feature, errUnsupportedAction
	}
	return feature, nil
}

func (app *application) runBash(response http.ResponseWriter, request *http.Request, path, command string, excluded bool) {
	var run agent.ShellRun
	err := app.withSynchronizedBashClient(request, path, func(client agent.Client) error {
		shell, err := capability[agent.ShellRunner](client)
		if err != nil {
			return err
		}
		run, err = shell.RunShell(request.Context(), command, excluded)
		return err
	})
	var shellErr *agent.ShellError
	if errors.As(err, &shellErr) {
		run, err = shellErr.Run, nil
	}
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	if !wantsJSON(request) && run.Failed {
		app.writeRPCFailure(response, request, run.Error, false)
		return
	}
	if wantsJSON(request) {
		payload := map[string]any{"command": "bash", "bash_id": run.ID, "data": run.Data, "exclude_from_context": excluded, "session": path, "redirect": app.sessionRedirectPath(request, path)}
		if run.Failed {
			payload["error"] = run.Error
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
	result := agent.StopResult{}
	queuedText := ""
	stoppedQueuedRun := false
	if app.sessionStore.Exists(requested) {
		if client := app.rpcClients.Client(requested); client != nil {
			var queued agent.Queue

			retire := func() (bool, error) {
				retired, messages, retireErr := app.rpcClients.RetireQueuedClient(requested, client)
				queued = messages
				return retired, retireErr
			}

			if app.guard != nil {
				stoppedQueuedRun, err = app.guard.RetireManagedClientIfAvailable(requested, retire)
			} else {
				stoppedQueuedRun, err = retire()
			}
			if err != nil {
				app.writeActionRPCError(response, err)
				return
			}
			if stoppedQueuedRun {
				queuedText = strings.Join(slices.Concat(queued.Steering, queued.FollowUp), "\n\n")
				result.Forced = true
			}
		}
	}
	abortClient := func(client agent.Client) error {
		var err error
		if shell, ok := client.(agent.ShellRunner); ok && shell.ShellRunning() {
			err = shell.CancelShell(request.Context())
		} else {
			err = client.Cancel(request.Context())
		}
		// A stop reports only that the agent could not be reached.
		if refusal(err) != nil {
			return nil
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
		result, err = agent.StopResultFor(err)
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
	err := app.withSynchronizedClient(request, path, func(client agent.Client) error {
		compacter, err := capability[agent.Compacter](client)
		if err != nil {
			return err
		}
		err = compacter.Compact(request.Context(), strings.TrimSpace(request.FormValue("instructions")))
		// The live events show a failed compaction.
		if refusal(err) != nil {
			return nil
		}
		return err
	})
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	http.Redirect(response, request, app.sessionRedirectPath(request, path), http.StatusSeeOther)
}

func (app *application) modelSettings(response http.ResponseWriter, request *http.Request) {
	path, ok := app.actionSessionPath(response, request, request.URL.Query().Get("session"), true)
	if !ok {
		return
	}
	var settings agent.ModelSettings
	err := app.withSynchronizedClient(request, path, func(client agent.Client) error {
		models, err := capability[agent.ModelSelector](client)
		if err != nil {
			return err
		}
		settings, err = models.ModelSettings(request.Context())
		return err
	})
	if errors.Is(err, agent.ErrInvalidResponse) {
		writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Could not load model settings"})
		return
	}
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	writeJSON(response, map[string]any{"state": settings.State, "models": settings.Models, "scopedModels": settings.ScopedModels})
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
	if !slices.Contains(app.adapter.Composer(path).ThinkingLevels(), thinking) {
		writeText(response, http.StatusBadRequest, "Invalid thinking level")
		return
	}
	var choice agent.ModelChoice
	err := app.withSynchronizedClient(request, path, func(client agent.Client) error {
		models, err := capability[agent.ModelSelector](client)
		if err != nil {
			return err
		}
		choice, err = models.SetModel(request.Context(), provider, modelID, thinking)
		return err
	})
	if errors.Is(err, agent.ErrInvalidResponse) {
		writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Could not confirm model settings"})
		return
	}
	if app.writeSettingError(response, err) {
		return
	}
	writeJSON(response, map[string]any{"model": choice.Model, "thinking": choice.ThinkingLevel})
}

func (app *application) cycleThinking(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), true)
	if !ok {
		return
	}
	var level string
	err := app.withSynchronizedClient(request, path, func(client agent.Client) error {
		models, err := capability[agent.ModelSelector](client)
		if err != nil {
			return err
		}
		level, err = models.CycleThinkingLevel(request.Context())
		return err
	})
	if errors.Is(err, agent.ErrInvalidResponse) {
		writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Could not change thinking level"})
		return
	}
	if app.writeSettingError(response, err) {
		return
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
	// A new session in the folder of another one runs the same agent.
	newPath, err := app.startNewSession(request, app.currentSessionCWD(path), "", app.agentOf(path))
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
	forkFrom := ""
	if raw := request.FormValue("fork_from"); raw != "" {
		// Pi CLI can carry on in the same file from another folder, which RPC mode cannot, so Pi forks the session into the
		// folder as it does when told to open a session from another project.
		source, ok := app.persistedActionSession(response, request, raw)
		if !ok {
			return
		}
		forkFrom = source.Path
	}
	request.Form.Del("project")
	newPath, err := app.startNewSession(request, cwd, forkFrom, request.FormValue("agent"))
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
	err := app.withSynchronizedClient(request, path, func(client agent.Client) error {
		renamer, err := capability[agent.Renamer](client)
		if err != nil {
			return err
		}
		return renamer.Rename(request.Context(), name)
	})
	if !wasActive && err == nil {
		if closed, closeErr := app.rpcClients.CloseClientIfIdle(path); closeErr != nil {
			logInternalError("close renamed session client", closeErr)
		} else if closed {
			app.guard.Forget(path)
		}
	}
	return err
}

func (app *application) deleteSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.actionSessionPath(response, request, request.FormValue("session"), false)
	if !ok {
		return
	}
	app.pendingRemapMu.Lock()
	defer app.pendingRemapMu.Unlock()
	path, ok = app.resolveActionPendingPath(response, request, path)
	if !ok {
		return
	}
	unlock := app.sessionMutationLocks.Lock(path)
	defer unlock()
	_, releaseDeletion, err := app.promptAdmissions.navigate(func() (string, error) { return path, nil })
	if app.writeRPCError(response, err) {
		return
	}
	defer releaseDeletion()
	if !app.knownOrPendingSession(request, path) {
		http.NotFound(response, request)
		return
	}
	currentPath, ok := app.resolveActionPendingPath(response, request, request.FormValue("current_session"))
	if !ok {
		return
	}
	if reason := app.deleteSessionBlockReason(currentPath, path); reason != "" {
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": reason})
		return
	}
	method, err := app.deleteSessionData(request, path)
	if errors.Is(err, errDeleteRunning) || errors.Is(err, agent.ErrSyncBusy) {
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": "Cannot delete a running session"})
		return
	}
	if err != nil {
		writeInternalError(response, "delete session", err)
		return
	}
	writeJSON(response, map[string]any{"session": path, "deleted": true, "method": method})
}

func (app *application) persistedActionSession(response http.ResponseWriter, request *http.Request, raw string) (*agent.Session, bool) {
	path, ok := app.actionSessionPath(response, request, raw, true)
	if !ok {
		return nil, false
	}
	session, persisted := app.sessionStore.Session(path)
	if !persisted {
		http.NotFound(response, request)
		return nil, false
	}
	return session, true
}

func (app *application) deleteSessionBlockReason(currentPath, targetPath string) string {
	if current, found := app.sessionStore.Session(currentPath); found {
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
	// Prompt acceptance can precede agent_start delivery. The caller holds both
	// admission exclusion and the operation lock through check, close, and deletion.
	err := app.rpcClients.WithExistingClient(ctx, path, false, func(client agent.Client) error {
		state, err := client.State(ctx)
		if err != nil {
			return err
		}
		if state.Streaming || state.Compacting {
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

func (app *application) deleteSessionData(request *http.Request, path string) (string, error) {
	method := ""
	err := app.guard.WithExclusiveOperation(path, func() error {
		if app.rpcClients.Busy(path) || app.rpcClients.Compacting(path) {
			return errDeleteRunning
		}
		if err := app.closeDeleteSessionClient(request.Context(), path); err != nil {
			return err
		}
		// Pi may not create a file until the first assistant response. Check
		// after closing the client so any file it did persist is still deleted.
		_, pending := app.pendingSessions.CWD(path)
		if pending && !app.sessionStore.Exists(path) {
			method = "pending"
		} else {
			var err error
			method, err = app.sessionStore.Delete(path)
			if err != nil {
				return err
			}
		}
		app.cleanupDeletedSession(request, path)
		return nil
	})
	return method, err
}

func (app *application) cleanupDeletedSession(request *http.Request, path string) {
	app.guard.Forget(path)
	app.pendingSessions.Forget(path)
	if err := app.gatewayState.Forget(path); err != nil {
		logInternalError("clean deleted session state", err)
	}
	attachments := sessions.AttachmentStore{Root: app.config.AttachmentsRoot, Keys: app.sessionStore}
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
	cwd, message, valid := validatedCWD(request.URL.Query().Get("cwd"), app.config.Home)
	if !valid {
		writeJSON(response, map[string]any{"valid": false, "error": message})
		return
	}
	directories := []string{}
	entries, _ := os.ReadDir(cwd)
	for _, entry := range entries {
		path := filepath.Join(cwd, entry.Name())
		if stat, err := os.Stat(path); utf8.ValidString(entry.Name()) && err == nil && stat.IsDir() && directoryAccessible(path) {
			directories = append(directories, path)
		}
	}
	writeJSON(response, map[string]any{"valid": true, "cwd": cwd, "directories": directories})
}

func (app *application) forkMessages(response http.ResponseWriter, request *http.Request) {
	path, ok := app.actionSessionPath(response, request, request.URL.Query().Get("session"), true)
	if !ok {
		return
	}
	var messages json.RawMessage
	err := app.withSynchronizedClient(request, path, func(client agent.Client) error {
		forker, err := capability[agent.Forker](client)
		if err != nil {
			return err
		}
		messages, err = forker.ForkMessages(request.Context())
		return err
	})
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	writeJSON(response, map[string]any{"messages": messages})
}

func (app *application) treeEntries(response http.ResponseWriter, request *http.Request) {
	path, ok := app.actionSessionPath(response, request, request.URL.Query().Get("session"), true)
	if !ok {
		return
	}
	filter := request.URL.Query().Get("filter")
	if filter != "" && !slices.Contains(app.adapter.Composer(path).TreeFilters(), filter) {
		writeText(response, http.StatusBadRequest, "Invalid tree filter")
		return
	}
	var tree json.RawMessage
	err := app.withSynchronizedClient(request, path, func(client agent.Client) error {
		navigator, err := capability[agent.TreeNavigator](client)
		if err != nil {
			return err
		}
		tree, err = navigator.Tree(request.Context(), filter)
		return err
	})
	if errors.Is(err, agent.ErrInvalidResponse) {
		writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Could not load session tree"})
		return
	}
	if app.writeSettingError(response, err) {
		return
	}
	writeJSON(response, tree)
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
	err = app.withSynchronizedClient(request, path, func(client agent.Client) error {
		state, err := client.State(request.Context())
		if err != nil {
			return err
		}
		if state.Compacting || client.Compacting() {
			return errSessionBusy
		}
		if !state.Streaming && !client.AgentRunning() {
			if client.Busy() {
				return errSessionBusy
			}
			return nil
		}
		queued := client.LiveSnapshot().Queue
		restoredQueuedText = strings.Join(slices.Concat(queued.Steering, queued.FollowUp), "\n\n")
		if err := client.Cancel(request.Context()); err != nil {
			return err
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
	var navigation agent.Navigation
	err = app.withSynchronizedClient(request, path, func(client agent.Client) error {
		navigator, err := capability[agent.TreeNavigator](client)
		if err != nil {
			return err
		}
		if summary != "custom" {
			instructions = ""
		}
		navigation, err = navigator.NavigateTree(request.Context(), entryID, summary, instructions)
		return err
	})
	// The queued messages that the stop dropped go back to the editor, even when the navigation fails.
	if err != nil && restoredQueuedText != "" {
		if refused := refusal(err); refused != nil {
			writeJSONStatus(response, http.StatusUnprocessableEntity, map[string]any{"error": failureMessage(refused.Message, "Could not navigate the session tree."), "editorText": restoredQueuedText})
		} else {
			writeJSONStatus(response, http.StatusConflict, map[string]any{"error": "Could not navigate the session tree.", "editorText": restoredQueuedText})
		}
		return
	}
	if app.writeSettingError(response, err) {
		return
	}
	payload := map[string]any{"session": path, "redirect": app.sessionRedirectPath(request, path), "cancelled": navigation.Cancelled}
	if restoredQueuedText != "" {
		payload["editorText"] = restoredQueuedText
	} else if navigation.EditorText != nil {
		if len(*navigation.EditorText) > extensionValueBytes {
			writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Extension editor response is too long"})
			return
		}
		payload["editorText"] = *navigation.EditorText
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
	err := app.withSynchronizedClient(request, path, func(client agent.Client) error {
		navigator, err := capability[agent.TreeNavigator](client)
		if err != nil {
			return err
		}
		if client.Busy() {
			return errSessionBusy
		}
		return navigator.LabelTreeEntry(request.Context(), entryID, label)
	})
	if app.writeSettingError(response, err) {
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
	filename, ok := exportDownloadFilename(request.FormValue("filename"), app.sessionStore.ExportName(path))
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

	err = app.withSynchronizedClient(request, path, func(client agent.Client) error {
		exporter, err := capability[agent.Exporter](client)
		if err != nil {
			return err
		}
		return exporter.ExportHTML(request.Context(), temporaryPath)
	})
	if refused := refusal(err); refused != nil {
		app.writeRPCFailure(response, request, refused.Message, true)
		return
	}
	if err != nil {
		app.writeActionRPCError(response, err)
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

func exportDownloadFilename(raw, defaultName string) (string, bool) {
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
		name = defaultName
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
	called := false
	path, ok = app.resolveActionPendingPath(response, request, path)
	if !ok {
		return
	}
	err := app.rpcClients.WithExistingClient(request.Context(), path, true, func(client agent.Client) error {
		responder, err := capability[agent.Responder](client)
		if err != nil {
			return err
		}
		called = true
		return responder.Answer(request.Context(), id, agent.Answer{Cancelled: cancelled, Confirmed: confirmed, Value: value})
	})
	if refused := refusal(err); refused != nil {
		app.writeRPCSettingFailure(response, refused.Message)
		return
	}
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	if !called {
		writeText(response, http.StatusNotFound, "No active Pi session")
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
	state, err := app.guard.TakeOver(request.Context(), path, func() error {
		session, found := app.sessionStore.Session(path)
		if !found {
			return errors.New("session disappeared during takeover")
		}
		if err := app.gatewayState.MarkExternalRead(path, session.AssistantResponseCount); err != nil {
			return err
		}
		_, err := app.gatewayState.RememberProject(session.CWD)
		return err
	})
	if err != nil {
		if errors.Is(err, agent.ErrSyncBusy) {
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
	var mover agent.ClientMover = app.rpcClients
	if app.sessionStore.Exists(previous) {
		mover = app.guard
	}
	newPath, branch, err := agent.BranchSession(request.Context(), previous, cwd, mover, app.sessionStore, app.pendingSessions, func(client agent.Client) (agent.Branch, error) {
		switch operation {
		case "new":
			return client.NewSession(request.Context())
		case "clone":
			cloner, err := capability[agent.Cloner](client)
			if err != nil {
				return agent.Branch{}, err
			}
			return cloner.Clone(request.Context())
		default:
			forker, err := capability[agent.Forker](client)
			if err != nil {
				return agent.Branch{}, err
			}
			return forker.Fork(request.Context(), entryID)
		}
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
		var tagRollback, projectRollback func() error
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
		rollback := func() error {
			var stateErr error
			if tagRollback != nil {
				stateErr = tagRollback()
			}
			if stateRollback != nil {
				stateErr = errors.Join(stateErr, stateRollback())
			}
			if projectRollback != nil {
				stateErr = errors.Join(stateErr, projectRollback())
			}
			var ownershipErr error
			if claimed && app.releaseSession != nil {
				ownershipErr = app.releaseSession(request, to)
			}
			return errors.Join(stateErr, ownershipErr)
		}
		if app.gatewayState != nil {
			projectRollback, err = app.gatewayState.RememberProject(cwd)
			if err != nil {
				return nil, errors.Join(err, rollback())
			}
		}
		return rollback, nil
	})
	if refused := refusal(err); refused != nil {
		app.writeRPCFailure(response, request, refused.Message, true)
		return
	}
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	if newPath != previous {
		app.guard.Forget(previous)
	}
	if branch.Cancelled {
		if wantsJSON(request) {
			writeJSONStatus(response, http.StatusConflict, map[string]any{"cancelled": true, "session": previous})
		} else {
			http.Redirect(response, request, app.sessionRedirectPath(request, previous), http.StatusSeeOther)
		}
		return
	}
	if wantsJSON(request) {
		payload := map[string]any{"session": newPath, "redirect": app.sessionRedirectPath(request, newPath)}
		if operation == "fork" && branch.Text != nil {
			payload["text"] = *branch.Text
		}
		writeJSON(response, payload)
		return
	}
	http.Redirect(response, request, app.sessionRedirectPath(request, newPath), http.StatusSeeOther)
}

// startNewSession starts a session with the agent that agentName names, or the gateway's first agent when it is empty.
func (app *application) startNewSession(request *http.Request, cwd, forkFrom, agentName string) (string, error) {
	if app.newRPCClient == nil {
		return "", errors.New("new Pi RPC client factory is unavailable")
	}
	factory := func(cwd string) (agent.Client, error) {
		return app.newRPCClient(cwd, app.environmentUser(request), forkFrom, agentName)
	}
	return agent.StartNewSession(request.Context(), cwd, factory, app.sessionStore, app.rpcClients, app.pendingSessions, func(path string) (func() error, error) {
		var err error
		claimed := false
		if app.claimSession != nil {
			claimed, err = app.claimSession(request, path)
			if err != nil {
				return nil, err
			}
		}
		var projectRollback, tagRollback func() error
		rollback := func() error {
			var rollbackErr error
			if projectRollback != nil {
				rollbackErr = projectRollback()
			}
			if tagRollback != nil {
				rollbackErr = errors.Join(rollbackErr, tagRollback())
			}
			if claimed && app.releaseSession != nil {
				rollbackErr = errors.Join(rollbackErr, app.releaseSession(request, path))
			}
			return rollbackErr
		}
		if app.gatewayState != nil && forkFrom != "" {
			// Like a fork in Gripi, the copy keeps the original's tags.
			tagRollback, err = app.gatewayState.CopyTags(forkFrom, path)
			if err != nil {
				return nil, errors.Join(err, rollback())
			}
		}
		if app.gatewayState != nil {
			projectRollback, err = app.gatewayState.RememberProject(cwd)
			if err != nil {
				return nil, errors.Join(err, rollback())
			}
		}
		return rollback, nil
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

func (app *application) validOwnedSessionPath(request *http.Request, path string) bool {
	if path == "" || len(path) > maximumSessionPathBytes || !app.sessionStore.ValidKey(path) || strings.ContainsRune(path, 0) {
		return false
	}
	return app.ownsSession == nil || app.ownsSession(request, path)
}

func (app *application) requireOwnedSession(response http.ResponseWriter, request *http.Request, path string) (string, bool) {
	if !app.validOwnedSessionPath(request, path) {
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
	_, ok := app.sessionStore.Session(path)
	return ok
}

func (app *application) currentSessionCWD(path string) string {
	if session, ok := app.sessionStore.Session(path); ok {
		return session.CWD
	}
	if cwd, ok := app.pendingSessions.CWD(path); ok {
		return cwd
	}
	return app.sessionStore.FallbackCWD(path)
}

func (app *application) withSynchronizedBashClient(request *http.Request, path string, call func(agent.Client) error) error {
	resolved, _, err := app.resolveOwnedPendingPath(request, path)
	if err != nil {
		return err
	}
	path = resolved
	ctx := request.Context()
	if app.sessionStore.Exists(path) {
		return app.guard.WithBashClient(ctx, path, call)
	}
	return app.rpcClients.WithBashClient(ctx, path, call)
}

func (app *application) withSynchronizedInterruptClient(request *http.Request, path string, call func(agent.Client) error) error {
	resolved, _, err := app.resolveOwnedPendingPath(request, path)
	if err != nil {
		return err
	}
	path = resolved
	ctx := request.Context()
	if app.sessionStore.Exists(path) {
		return app.guard.WithInterruptClient(ctx, path, call)
	}
	return app.rpcClients.WithInterruptClient(ctx, path, call)
}

func (app *application) abortMatchingPending(request *http.Request, requested string, abort func(agent.Client) error) (string, error) {
	ctx := request.Context()
	session, known := app.sessionStore.Session(requested)
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
		err := app.rpcClients.WithExistingInterruptClient(ctx, pending.Path, func(client agent.Client) error {
			stater, err := capability[agent.InterruptStater](client)
			if err != nil {
				return err
			}
			state, err := stater.InterruptState(ctx)
			// A session that the agent won't name is not the one to stop.
			if refusal(err) != nil {
				return nil
			}
			if err != nil {
				unavailable = true
				return nil
			}
			reported, found := app.sessionStore.Session(state.Key)
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

// refusal returns the error with which the agent refused a request, or nil when it didn't.
func refusal(err error) *agent.RequestError {
	var refused *agent.RequestError
	errors.As(err, &refused)
	return refused
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
	if refused := refusal(err); refused != nil {
		app.writeRPCSettingFailure(response, refused.Message)
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
	if errors.Is(err, agent.ErrBashPending) {
		writeJSONStatus(response, http.StatusConflict, map[string]any{"error": "A bash command is already running for this session"})
		return true
	}
	if app.writeRPCError(response, err) {
		return true
	}
	if errors.Is(err, agent.ErrStartFailed) {
		writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return true
	}
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, agent.ErrProcessExited) {
		writeJSONStatus(response, http.StatusBadGateway, map[string]any{"error": "Pi RPC client disconnected"})
		return true
	}
	http.Error(response, "Pi RPC request failed", http.StatusInternalServerError)
	return true
}

// writeRPCFailure explains a request that the agent refused, giving the agent's reason when it has one.
func (app *application) writeRPCFailure(response http.ResponseWriter, request *http.Request, reason string, setting bool) {
	message := failureMessage(reason, "Prompt failed to send")
	if setting {
		message = failureMessage(reason, "Setting could not be changed")
	}
	if wantsJSON(request) {
		writeJSONStatus(response, http.StatusUnprocessableEntity, map[string]any{"success": false, "error": message})
	} else {
		writeText(response, http.StatusUnprocessableEntity, message)
	}
}

func (app *application) writeRPCSettingFailure(response http.ResponseWriter, reason string) {
	writeJSONStatus(response, http.StatusUnprocessableEntity, map[string]any{"success": false, "error": failureMessage(reason, "Setting could not be changed")})
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

func failureMessage(reason, fallback string) string {
	if message := strings.TrimSpace(reason); message != "" {
		return message
	}
	return fallback
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
