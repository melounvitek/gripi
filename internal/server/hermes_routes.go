package server

import (
	"net/http"
	"net/url"
	"sync"
)

// hermesConversation is the one Hermes conversation that every Hermes panel shows, until a new one replaces it. The
// gateway forgets it when it stops; Hermes keeps its sessions in its own database.
type hermesConversation struct {
	mu  sync.Mutex
	key string
}

func (app *application) registerHermesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /hermes", app.openHermesConversation)
	mux.HandleFunc("POST /hermes/new", app.newHermesConversation)
	mux.HandleFunc("GET /hermes/current", app.currentHermesConversation)
}

// openHermesConversation opens the current conversation, and starts one when there is none or it is gone. A Hermes
// conversation belongs to no project, so it starts in the home folder.
func (app *application) openHermesConversation(response http.ResponseWriter, request *http.Request) {
	app.hermes.mu.Lock()
	defer app.hermes.mu.Unlock()
	if !app.commandSessionAvailable(app.hermes.key) {
		key, err := app.startNewSession(request, app.config.Home, "", "hermes")
		if err != nil {
			app.writeActionRPCError(response, err)
			return
		}
		app.hermes.key = key
	}
	app.showHermesConversation(response, request, app.hermes.key)
}

func (app *application) newHermesConversation(response http.ResponseWriter, request *http.Request) {
	app.hermes.mu.Lock()
	defer app.hermes.mu.Unlock()
	key, err := app.startNewSession(request, app.config.Home, "", "hermes")
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	app.hermes.key = key
	app.showHermesConversation(response, request, key)
}

func (app *application) currentHermesConversation(response http.ResponseWriter, _ *http.Request) {
	app.hermes.mu.Lock()
	defer app.hermes.mu.Unlock()
	writeJSON(response, map[string]string{"session": app.hermes.key})
}

// showHermesConversation sends the browser to the Hermes panel's page of the conversation.
func (app *application) showHermesConversation(response http.ResponseWriter, request *http.Request, key string) {
	target := "/?" + url.Values{"session": {key}, "session_only": {"1"}}.Encode()
	if wantsJSON(request) {
		writeJSON(response, map[string]string{"session": key, "redirect": target})
		return
	}
	http.Redirect(response, request, target, http.StatusSeeOther)
}
