package server

import (
	"bytes"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/melounvitek/gripi/internal/sessions"
)

var (
	attachmentSessionPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	attachmentFilePattern    = regexp.MustCompile(`^[a-f0-9]{64}\.(png|jpg|gif|webp)$`)
)

func (app *application) registerSessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", app.index)
	mux.HandleFunc("GET /sidebar", app.sidebar)
	mux.HandleFunc("GET /new_session_modal", app.newSessionModal)
	mux.HandleFunc("GET /session_fragment", app.sessionFragment)
	mux.HandleFunc("GET /conversation_older", app.conversationOlder)
	mux.HandleFunc("GET /attachments/{session_hash}/{file}", app.attachment)
	mux.HandleFunc("POST /composer/path_suggestions", app.composerPathSuggestions)
	mux.HandleFunc("POST /markdown", app.renderMarkdown)
	mux.HandleFunc("POST /sessions/pin", app.pinSession)
	mux.HandleFunc("POST /sessions/mark_read", app.markSessionRead)
	mux.HandleFunc("GET /events", app.events)
	mux.HandleFunc("GET /status", app.liveSessionStatus)
	mux.HandleFunc("GET /commands", app.commands)
}

func acquireRequestSlot(response http.ResponseWriter, request *http.Request, slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	case <-request.Context().Done():
		http.Error(response, "Request cancelled", http.StatusServiceUnavailable)
		return false
	}
}
func releaseRequestSlot(slots chan struct{}) { <-slots }

func (app *application) index(response http.ResponseWriter, request *http.Request) {
	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	if app.config.SessionsRoot == "" {
		http.NotFound(response, request)
		return
	}
	view, err := app.preparePage(request, true)
	if err != nil {
		logInternalError("prepare session page", err)
		http.Error(response, "Unable to read sessions", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := app.templates.ExecuteTemplate(response, "index.html", view); err != nil {
		return
	}
}

func (app *application) sidebar(response http.ResponseWriter, request *http.Request) {
	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	view, err := app.preparePage(request, false)
	if err != nil {
		logInternalError("prepare sidebar", err)
		http.Error(response, "Unable to read sessions", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = app.templates.ExecuteTemplate(response, "sidebar", view)
}

func (app *application) newSessionModal(response http.ResponseWriter, request *http.Request) {
	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	view, err := app.preparePage(request, false)
	if err != nil {
		logInternalError("prepare new session modal", err)
		http.Error(response, "Unable to read sessions", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = app.templates.ExecuteTemplate(response, "new_session_modal", view)
}

func (app *application) sessionFragment(response http.ResponseWriter, request *http.Request) {
	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	view, err := app.preparePage(request, true)
	if err != nil {
		logInternalError("prepare session fragment", err)
		http.Error(response, "Unable to read sessions", http.StatusInternalServerError)
		return
	}
	parts := make(map[string]string)
	for name, templateName := range map[string]string{"sidebar_html": "sidebar", "conversation_html": "conversation", "new_session_modal_html": "new_session_modal", "fork_session_modal_html": "fork_session_modal"} {
		if name == "sidebar_html" && view.SessionOnly {
			continue
		}
		var output bytes.Buffer
		if err := app.templates.ExecuteTemplate(&output, templateName, view); err != nil {
			http.Error(response, "Unable to render session", http.StatusInternalServerError)
			return
		}
		parts[name] = output.String()
	}
	values := map[string]any{"url": sessionViewURL(view), "title": "", "session": nil, "sidebar_html": nil, "conversation_html": parts["conversation_html"], "new_session_modal_html": parts["new_session_modal_html"], "fork_session_modal_html": parts["fork_session_modal_html"]}
	if !view.SessionOnly {
		values["sidebar_html"] = parts["sidebar_html"]
	}
	if view.Selected != nil {
		values["title"], values["session"] = view.Selected.DisplayName, view.Selected.Path
	}
	writeJSON(response, values)
}

func sessionViewURL(view *pageView) string {
	values := url.Values{}
	if view.Selected != nil {
		values.Set("session", view.Selected.Path)
		if retained := retainedSessionsLimit(view, view.Selected); retained != "" {
			values.Set("sidebar_sessions_limit", retained)
		}
	} else if len(view.Sessions) > 0 {
		values.Set("no_session", "1")
	}
	if view.SelectedProject != "" {
		values.Set("project", view.SelectedProject)
	}
	if view.SearchQuery != "" {
		values.Set("session_search", view.SearchQuery)
	}
	if view.SessionOnly {
		values.Set("session_only", "1")
	}
	return "/?" + values.Encode()
}

func (app *application) conversationOlder(response http.ResponseWriter, request *http.Request) {
	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	empty := map[string]any{"html": "", "next_cursor": 0, "has_older_messages": false, "older_message_count": 0}
	path, owned := app.requireOwnedSession(response, request, request.URL.Query().Get("session"))
	if !owned {
		return
	}
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	session, ok := store.Session(path)
	if !ok {
		writeJSON(response, empty)
		return
	}
	cursor := boundedQueryInteger(request.URL.Query().Get("cursor"))
	var after *int
	if raw, exists := request.URL.Query()["after"]; exists {
		value := boundedQueryInteger(raw[0])
		after = &value
	}
	leaf, supplied := request.URL.Query()["tree_leaf"]
	leafID := ""
	if supplied && len(leaf) > 0 {
		leafID = leaf[0]
	}
	window, err := store.Window(session.Path, leafID, supplied, &cursor, after)
	if err != nil {
		http.Error(response, "Session changed while it was read", http.StatusServiceUnavailable)
		return
	}
	matches := (sessions.AttachmentStore{Root: app.config.AttachmentsRoot}).Match(session.Path, window.Messages)
	view := &pageView{Home: app.config.Home, Attachments: matches}
	var html bytes.Buffer
	for _, message := range window.Messages {
		_ = app.templates.ExecuteTemplate(&html, "message", struct {
			View    *pageView
			Message *sessions.Message
		}{view, message})
	}
	next := window.StartIndex
	remaining := next
	if after != nil {
		next = window.StartIndex + len(window.Messages)
		remaining = window.EndIndex - next
	}
	writeJSON(response, map[string]any{"html": html.String(), "next_cursor": next, "has_older_messages": remaining > 0, "older_message_count": max(remaining, 0)})
}

func boundedQueryInteger(value string) int {
	parsed, err := strconv.Atoi(value)
	if err == nil {
		return parsed
	}
	if number, ok := err.(*strconv.NumError); ok && number.Err == strconv.ErrRange {
		if strings.HasPrefix(strings.TrimSpace(value), "-") {
			return -int(^uint(0)>>1) - 1
		}
		return int(^uint(0) >> 1)
	}
	return 0
}

func (app *application) attachment(response http.ResponseWriter, request *http.Request) {
	sessionHash, fileName := request.PathValue("session_hash"), request.PathValue("file")
	if !attachmentSessionPattern.MatchString(sessionHash) || !attachmentFilePattern.MatchString(fileName) {
		http.NotFound(response, request)
		return
	}
	if app.ownershipStore != nil && app.config.MultiUserMode {
		owned, err := app.ownershipStore.OwnsHash(sessionHash, currentWorkspaceID(request))
		if err != nil || !owned {
			http.NotFound(response, request)
			return
		}
	} else if !app.knownSessionHash(sessionHash) {
		http.NotFound(response, request)
		return
	}
	root, err := filepath.EvalSymlinks(app.config.AttachmentsRoot)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, sessionHash, fileName))
	if err != nil {
		http.NotFound(response, request)
		return
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(path)))
	http.ServeFile(response, request, path)
}

func (app *application) knownSessionHash(hash string) bool {
	app.sessionHashesMu.Lock()
	fresh := time.Since(app.sessionHashesAt) < time.Second
	known := app.knownSessionHashes[hash]
	app.sessionHashesMu.Unlock()
	if fresh {
		return known
	}
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	all, err := store.Sessions()
	if err != nil {
		return false
	}
	app.rememberSessionHashes(all)
	app.sessionHashesMu.Lock()
	known = app.knownSessionHashes[hash]
	app.sessionHashesMu.Unlock()
	return known
}

func (app *application) composerPathSuggestions(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	mode, query := request.FormValue("mode"), request.FormValue("query")
	if mode != "fuzzy" && mode != "path" {
		writeText(response, http.StatusBadRequest, "Invalid suggestion mode")
		return
	}
	if len(query) > sessions.MaxSuggestionQueryBytes || strings.ContainsRune(query, 0) {
		writeText(response, http.StatusBadRequest, "Invalid query")
		return
	}
	raw, owned := app.requireOwnedSession(response, request, request.FormValue("session"))
	if !owned {
		return
	}
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	session, ok := store.Session(raw)
	if !ok {
		http.NotFound(response, request)
		return
	}
	if mode == "fuzzy" {
		if !acquireRequestSlot(response, request, app.fdRequests) {
			return
		}
		defer releaseRequestSlot(app.fdRequests)
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, map[string]any{"suggestions": sessions.SuggestPaths(session.CWD, app.config.Home, mode, query)})
}

func (app *application) pinSession(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.requireOwnedSession(response, request, request.FormValue("session"))
	if !ok {
		return
	}
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	session, ok := store.Session(path)
	if !ok {
		http.NotFound(response, request)
		return
	}
	var pinned bool
	switch request.FormValue("pinned") {
	case "true":
		pinned = true
	case "false":
		pinned = false
	default:
		writeText(response, http.StatusBadRequest, "Invalid pinned state")
		return
	}
	if err := app.gatewayState.SetPinned(session.Path, pinned); err != nil {
		logInternalError("update pinned sessions", err)
		http.Error(response, "Unable to update pinned sessions", http.StatusInternalServerError)
		return
	}
	writeJSON(response, map[string]any{"session": session.Path, "pinned": pinned})
}

func (app *application) markSessionRead(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	path, ok := app.requireOwnedSession(response, request, request.FormValue("session"))
	if !ok {
		return
	}
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	session, ok := store.Session(path)
	if !ok {
		http.NotFound(response, request)
		return
	}
	countText, generation := request.FormValue("assistant_response_count"), request.FormValue("session_generation")
	if len(countText) == 0 || len(countText) > 10 || len(generation) > 256 {
		writeText(response, http.StatusBadRequest, "Invalid read state")
		return
	}
	for _, digit := range countText {
		if digit < '0' || digit > '9' {
			writeText(response, http.StatusBadRequest, "Invalid read state")
			return
		}
	}
	count, err := strconv.Atoi(countText)
	if err != nil || count < 0 || count > assistantResponseMax {
		writeText(response, http.StatusBadRequest, "Invalid read state")
		return
	}
	if generation != store.Generation(session.Path) {
		writeText(response, http.StatusConflict, "Session changed")
		return
	}
	if err := app.gatewayState.MarkRead(session.Path, count); err != nil {
		logInternalError("update session read state", err)
		http.Error(response, "Unable to update read state", http.StatusInternalServerError)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (app *application) renderMarkdown(response http.ResponseWriter, request *http.Request) {
	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	if !parseForm(response, request) {
		return
	}
	writeJSON(response, map[string]string{"html": app.markdown.Render(request.FormValue("text"))})
}
