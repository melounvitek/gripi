package server

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/melounvitek/gripi/internal/agent"
)

// scratchSessions are the scratch sessions of the gateway's users, one each at most: sessions that Pi keeps only in
// memory, which run without a project folder and are never listed.
type scratchSessions struct {
	// changing lets one scratch session start or end at a time.
	changing sync.Mutex
	folders  *scratchFolders
	mu       sync.Mutex
	// byOwner holds each user's scratch session by the workspace that started it, "" in single-user mode.
	byOwner map[string]*scratchSession
	// ended holds the keys of the scratch sessions that ended or are ending. They stay scratch keys, so that a request
	// that was already under way can't start a saved session under one.
	ended map[string]bool
}

type scratchSession struct {
	key string
	dir string
	// images are copies of the latest prompts' images, oldest first, which a page fetches to put a prompt back in the
	// editor when Pi refuses it after it left. The oldest go once they take more than scratchHandBackBytes.
	images []scratchImage
}

type scratchImage struct {
	token    string
	data     []byte
	mimeType string
}

const (
	scratchHandBackBytes = 64 << 20
	// Pi repeats a prompt as one line of its output, which Gripi reads up to 65 MiB, and a Pi lost to a longer one
	// would take the session with it.
	scratchPromptImageBytes = 32 << 20
)

var errScratchGone = &agent.RequestError{Message: "This scratch session has ended."}

// scratchRefusedRoutes need a saved session or keep something about it. Discard ends a scratch session instead of
// delete. Pi itself would offer more of them for a session that it keeps only in memory.
var scratchRefusedRoutes = map[string]bool{
	"POST /sessions/new": true, "POST /sessions/rename": true, "POST /sessions/delete": true, "GET /sessions/fork_messages": true,
	"GET /sessions/tree_entries": true, "POST /sessions/tree": true, "POST /sessions/tree/label": true, "POST /sessions/fork": true,
	"POST /sessions/clone": true, "POST /sessions/export": true, "POST /sessions/takeover": true, "POST /sessions/pin": true,
	"GET /sessions/tags": true, "POST /sessions/tags": true, "POST /sessions/mark_read": true, "POST /workers/move": true,
	"POST /workers/return": true, "POST /workers/forward": true, "POST /workers/unforward": true,
}

// scratchRefusedCommands are the slash commands of the refused routes.
var scratchRefusedCommands = []string{"name", "export", "fork", "tree", "clone", "new"}

func (app *application) refuseScratchRoutes(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if _, pattern := mux.Handler(request); scratchRefusedRoutes[pattern] && app.scratch.contains(request.FormValue("session")) {
			http.Error(response, "A scratch session doesn’t offer this.", http.StatusConflict)
			return
		}
		mux.ServeHTTP(response, request)
	})
}

func (scratch *scratchSessions) session(owner string) *scratchSession {
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	return scratch.byOwner[owner]
}

// key returns the key of the owner's scratch session, or "" when it has none or it is ending.
func (scratch *scratchSessions) key(owner string) string {
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	if session := scratch.byOwner[owner]; session != nil && !scratch.ended[session.key] {
		return session.key
	}
	return ""
}

func (scratch *scratchSessions) contains(key string) bool {
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	if scratch.ended[key] {
		return true
	}
	for _, session := range scratch.byOwner {
		if session.key == key {
			return true
		}
	}
	return false
}

// scratchGone tells whether the Pi of a scratch session has ended, such as after it crashed, which lost the session.
func (app *application) scratchGone(key string) bool {
	client := app.rpcClients.Client(key)
	exiter, ok := client.(agent.Exiter)
	return client == nil || ok && exiter.Exited()
}

// scratchHistory reads from the scratch session's Pi, which keeps the session only in memory. It doesn't wait for
// what Pi is doing, because Pi answers Gripi's extension at once.
func (app *application) scratchHistory(ctx context.Context, key string, read func(agent.MemoryHistory) error) error {
	called := false
	err := app.rpcClients.WithActiveClient(ctx, key, false, func(client agent.Client) error {
		history, ok := client.(agent.MemoryHistory)
		if !ok {
			return agent.ErrInvalidResponse
		}
		called = true
		return read(history)
	})
	if err == nil && !called {
		return errScratchGone
	}
	return err
}

func (app *application) scratchWindow(ctx context.Context, key string, cursor, after *int) (window agent.Window, err error) {
	err = app.scratchHistory(ctx, key, func(history agent.MemoryHistory) error {
		window, err = history.MemoryWindow(ctx, cursor, after)
		return err
	})
	return window, err
}

// keepImage keeps a copy of an image of a prompt to the scratch session key, and returns where a page gets it.
func (scratch *scratchSessions) keepImage(key string, data []byte, mimeType string) (string, error) {
	token, err := randomBrowserToken()
	if err != nil {
		return "", err
	}
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	for _, session := range scratch.byOwner {
		if session.key != key {
			continue
		}
		session.images = append(session.images, scratchImage{token: token, data: data, mimeType: mimeType})
		total := 0
		for index := len(session.images) - 1; index >= 0; index-- {
			if total += len(session.images[index].data); total > scratchHandBackBytes {
				session.images = slices.Clone(session.images[index+1:])
				break
			}
		}
		return "/scratch/images/" + token, nil
	}
	return "", errScratchGone
}

func (scratch *scratchSessions) image(owner, token string) (scratchImage, bool) {
	scratch.mu.Lock()
	defer scratch.mu.Unlock()
	session := scratch.byOwner[owner]
	if session == nil || scratch.ended[session.key] {
		return scratchImage{}, false
	}
	index := slices.IndexFunc(session.images, func(image scratchImage) bool { return image.token == token })
	if index < 0 {
		return scratchImage{}, false
	}
	return session.images[index], true
}

func (app *application) registerScratchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /scratch/new", app.newScratchSession)
	mux.HandleFunc("POST /scratch/discard", app.discardScratchSession)
	mux.HandleFunc("GET /scratch/images/{token}", app.scratchPromptImage)
}

// scratchPromptImages reads the images of a prompt to a scratch session into memory, and keeps copies that a page can
// put back in the editor.
func (app *application) scratchPromptImages(key string, files []*multipart.FileHeader) ([]agent.PromptImage, error) {
	images := make([]agent.PromptImage, 0, len(files))
	for _, header := range files {
		file, err := header.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(file)
		file.Close()
		if err != nil {
			return nil, err
		}
		mimeType := header.Header.Get("Content-Type")
		source, err := app.scratch.keepImage(key, data, mimeType)
		if err != nil {
			return nil, err
		}
		images = append(images, agent.PromptImage{Data: data, MIMEType: mimeType, Size: int64(len(data)), Src: source})
	}
	return images, nil
}

func (app *application) scratchPromptImage(response http.ResponseWriter, request *http.Request) {
	image, ok := app.scratch.image(currentWorkspaceID(request), request.PathValue("token"))
	if !ok {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", image.mimeType)
	_, _ = response.Write(image.data)
}

// ownsScratch tells whether path is the scratch session of the browser that asks. gripi commands and workers never
// reach a scratch session.
func (app *application) ownsScratch(request *http.Request, path string) bool {
	_, fromWorker := workerOf(request)
	return path != "" && !commandRequest(request) && !fromWorker && app.scratch.key(currentWorkspaceID(request)) == path
}

// newScratchSession opens the user's scratch session, and starts one when there is none.
func (app *application) newScratchSession(response http.ResponseWriter, request *http.Request) {
	if app.scratch.folders == nil {
		http.Error(response, "Scratch sessions are unavailable on this gateway.", http.StatusServiceUnavailable)
		return
	}
	app.scratch.changing.Lock()
	defer app.scratch.changing.Unlock()
	owner := currentWorkspaceID(request)
	if current := app.scratch.session(owner); current != nil {
		if !app.scratchGone(current.key) && app.scratch.key(owner) == current.key {
			app.showScratchSession(response, request, current.key)
			return
		}
		if err := app.endScratchSession(owner, current); err != nil {
			writeInternalError(response, "end scratch session", err)
			return
		}
	}
	dir, err := app.scratch.folders.create()
	if err != nil {
		writeInternalError(response, "create scratch session folder", err)
		return
	}
	key, err := app.startScratchSession(request, owner, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		app.writeActionRPCError(response, err)
		return
	}
	app.showScratchSession(response, request, key)
}

func (app *application) startScratchSession(request *http.Request, owner, dir string) (string, error) {
	variables, err := app.piEnvironment(app.environmentUser(request))
	if err != nil {
		return "", err
	}
	// Pi writes long tool output to the OS's temporary folder, which the scratch session's folder holds instead.
	variables = append(variables, "TMPDIR="+filepath.Join(dir, "tmp"))
	start := func(cwd string) (agent.Client, error) {
		return app.adapter.StartInCWD(cwd, "", agent.StartOptions{Scratch: true, Env: variables, Diagnostics: app.rpcDiagnostics})
	}
	return agent.StartNewSession(request.Context(), filepath.Join(dir, "work"), start, app.sessionStore, app.rpcClients, app.pendingSessions, func(key string) (func() error, error) {
		// The key is a scratch key before its client is registered, so that nothing starts a saved session under it.
		app.scratch.mu.Lock()
		app.scratch.byOwner[owner] = &scratchSession{key: key, dir: dir}
		app.scratch.mu.Unlock()
		return func() error {
			app.scratch.mu.Lock()
			delete(app.scratch.byOwner, owner)
			app.scratch.ended[key] = true
			app.scratch.mu.Unlock()
			return nil
		}, nil
	})
}

func (app *application) discardScratchSession(response http.ResponseWriter, request *http.Request) {
	app.scratch.changing.Lock()
	defer app.scratch.changing.Unlock()
	owner := currentWorkspaceID(request)
	if current := app.scratch.session(owner); current != nil {
		if err := app.endScratchSession(owner, current); err != nil {
			writeInternalError(response, "end scratch session", err)
			return
		}
	}
	writeJSON(response, map[string]bool{"ok": true})
}

// endScratchSession stops the session's Pi, even while it works, and removes its folder. The caller holds
// scratch.changing. When it fails, the session stays ending, and ending it again can finish.
func (app *application) endScratchSession(owner string, session *scratchSession) error {
	app.scratch.mu.Lock()
	app.scratch.ended[session.key] = true
	app.scratch.mu.Unlock()
	if _, err := app.rpcClients.CloseClient(session.key); err != nil {
		return err
	}
	if err := os.RemoveAll(session.dir); err != nil {
		return err
	}
	app.pendingSessions.Forget(session.key)
	app.scratch.mu.Lock()
	if app.scratch.byOwner[owner] == session {
		delete(app.scratch.byOwner, owner)
	}
	app.scratch.mu.Unlock()
	return nil
}

// endScratchSessions ends every scratch session, as the gateway stops.
func (app *application) endScratchSessions() error {
	app.scratch.changing.Lock()
	defer app.scratch.changing.Unlock()
	app.scratch.mu.Lock()
	sessions := make(map[string]*scratchSession, len(app.scratch.byOwner))
	for owner, session := range app.scratch.byOwner {
		sessions[owner] = session
	}
	app.scratch.mu.Unlock()
	var err error
	for owner, session := range sessions {
		err = errors.Join(err, app.endScratchSession(owner, session))
	}
	if app.scratch.folders != nil {
		err = errors.Join(err, app.scratch.folders.Close())
	}
	return err
}

// showScratchSession sends the browser to the scratch session's page, which the scratch overlay shows.
func (app *application) showScratchSession(response http.ResponseWriter, request *http.Request, key string) {
	target := "/?" + url.Values{"session": {key}, "session_only": {"1"}}.Encode()
	if wantsJSON(request) {
		writeJSON(response, map[string]string{"session": key, "redirect": target})
		return
	}
	http.Redirect(response, request, target, http.StatusSeeOther)
}
