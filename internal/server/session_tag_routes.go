package server

import (
	"net/http"
	"sort"

	"github.com/melounvitek/gripi/internal/sessions"
)

func (app *application) lockGatewayStateSession(response http.ResponseWriter, request *http.Request, raw string) (string, func(), bool) {
	path, ok := app.requireOwnedSession(response, request, raw)
	if !ok {
		return "", nil, false
	}
	path, unlock, err := app.lockResolvedImagePromptPath(request, path)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return "", nil, false
	}
	path, ok = app.gatewayStateSessionPath(path)
	if !ok {
		unlock()
		http.NotFound(response, request)
		return "", nil, false
	}
	unlockMutation := app.sessionMutationLocks.Lock(path)
	release := func() { unlockMutation(); unlock() }
	path, ok = app.gatewayStateSessionPath(path)
	if !ok {
		release()
		http.NotFound(response, request)
		return "", nil, false
	}
	return path, release, true
}

func (app *application) tags(response http.ResponseWriter, request *http.Request) {
	tags, err := app.gatewayState.SessionTags()
	if err != nil {
		logInternalError("read session tags", err)
		http.Error(response, "Unable to read session tags", http.StatusInternalServerError)
		return
	}
	available, err := app.visibleTagCounts(request, tags)
	if err != nil {
		logInternalError("list session tags", err)
		http.Error(response, "Unable to list session tags", http.StatusInternalServerError)
		return
	}
	colors, err := app.visibleTagColors(available)
	if err != nil {
		logInternalError("read tag colors", err)
		http.Error(response, "Unable to read tag colors", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, map[string]any{"tags": available, "tag_colors": colors})
}

func (app *application) sessionTags(response http.ResponseWriter, request *http.Request) {
	raw := request.URL.Query().Get("session")
	if request.Method == http.MethodPost {
		if !parseForm(response, request) {
			return
		}
		raw = request.FormValue("session")
	}
	path, unlock, ok := app.lockGatewayStateSession(response, request, raw)
	if !ok {
		return
	}
	defer unlock()

	if request.Method == http.MethodPost {
		assigned := request.FormValue("assigned")
		if assigned != "true" && assigned != "false" {
			writeText(response, http.StatusBadRequest, "Invalid assigned state")
			return
		}
		if err := app.gatewayState.SetTag(path, request.FormValue("tag"), assigned == "true"); err != nil {
			if err == sessions.ErrInvalidTag || err == sessions.ErrTooManyTags {
				writeText(response, http.StatusBadRequest, err.Error())
				return
			}
			logInternalError("update session tags", err)
			http.Error(response, "Unable to update session tags", http.StatusInternalServerError)
			return
		}
	}
	tags, err := app.gatewayState.SessionTags()
	if err != nil {
		logInternalError("read session tags", err)
		http.Error(response, "Unable to read session tags", http.StatusInternalServerError)
		return
	}
	available, err := app.visibleTagCounts(request, tags)
	if err != nil {
		logInternalError("list session tags", err)
		http.Error(response, "Unable to list session tags", http.StatusInternalServerError)
		return
	}
	colors, err := app.visibleTagColors(available)
	if err != nil {
		logInternalError("read tag colors", err)
		http.Error(response, "Unable to read tag colors", http.StatusInternalServerError)
		return
	}
	names := tags[path]
	if names == nil {
		names = []string{}
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, map[string]any{"session": path, "tags": names, "available_tags": available, "tag_colors": colors})
}

func (app *application) visibleTagCounts(request *http.Request, tags map[string][]string) ([]sessions.TagCount, error) {
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	all, err := store.Sessions()
	if err != nil {
		return nil, err
	}
	paths := make(map[string]bool, len(all))
	for _, session := range all {
		paths[session.Path] = true
	}
	for _, pending := range app.pendingSessions.Entries() {
		paths[pending.Path] = true
	}
	var ownedPaths map[string]bool
	if app.ownsSession != nil && app.ownershipStore != nil {
		ownedPaths, err = app.ownershipStore.OwnedPaths(currentWorkspaceID(request))
		if err != nil {
			return nil, err
		}
	}
	counts := make(map[string]int)
	for path := range paths {
		if app.ownsSession != nil {
			owned := ownedPaths[path]
			if app.ownershipStore == nil {
				owned = app.ownsSession(request, path)
			}
			if !owned {
				continue
			}
		}
		for _, name := range tags[path] {
			counts[name]++
		}
	}
	result := make([]sessions.TagCount, 0, len(counts))
	for name, count := range counts {
		result = append(result, sessions.TagCount{Name: name, Count: count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
