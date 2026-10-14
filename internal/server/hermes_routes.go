package server

import (
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
)

// hermesConversation is the one Hermes conversation that every Hermes panel shows, until a new one replaces it. The
// gateway forgets it when it stops; Hermes keeps its sessions in its own database.
type hermesConversation struct {
	// starting lets one conversation start at a time, which takes Hermes seconds.
	starting sync.Mutex
	key      atomic.Pointer[string]
}

func (conversation *hermesConversation) current() string {
	if key := conversation.key.Load(); key != nil {
		return *key
	}
	return ""
}

func (app *application) registerHermesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /hermes", app.openHermesConversation)
	mux.HandleFunc("POST /hermes/new", app.newHermesConversation)
	mux.HandleFunc("GET /hermes/current", app.currentHermesConversation)
}

// openHermesConversation opens the current conversation, and starts one when there is none or it is gone. A Hermes
// conversation belongs to no project, so it starts in the home folder.
func (app *application) openHermesConversation(response http.ResponseWriter, request *http.Request) {
	app.hermes.starting.Lock()
	defer app.hermes.starting.Unlock()
	key := app.hermes.current()
	if !app.commandSessionAvailable(key) {
		var err error
		if key, err = app.startNewSession(request, app.config.Home, "", "hermes"); err != nil {
			app.writeActionRPCError(response, err)
			return
		}
		app.hermes.key.Store(&key)
	}
	app.showHermesConversation(response, request, key)
}

func (app *application) newHermesConversation(response http.ResponseWriter, request *http.Request) {
	app.hermes.starting.Lock()
	defer app.hermes.starting.Unlock()
	key, err := app.startNewSession(request, app.config.Home, "", "hermes")
	if err != nil {
		app.writeActionRPCError(response, err)
		return
	}
	app.hermes.key.Store(&key)
	app.showHermesConversation(response, request, key)
}

func (app *application) currentHermesConversation(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, map[string]string{"session": app.hermes.current()})
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
