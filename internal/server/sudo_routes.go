package server

import (
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SudoTimeout is how long a command of 'gripi sudo' waits for the user's answer. Tests shorten it.
var SudoTimeout = 2 * time.Minute

// sudoRequest is a command that an agent asked to run as root, waiting for the user's answer in the browser.
type sudoRequest struct {
	view sudoRequestView
	// cancelled closes when the user cancels the request.
	cancelled chan struct{}
}

// sudoRequestView is all of a sudo request that the browser gets.
type sudoRequestView struct {
	ID      string `json:"id"`
	Session string `json:"session"`
	// Display and CWD show the command as the user would type it in a shell.
	Display   string    `json:"display"`
	CWD       string    `json:"cwd"`
	ExpiresAt time.Time `json:"expires_at"`
}

// sudoRequests holds the pending requests only in memory. A request leaves as it is answered, so it is never shown again.
type sudoRequests struct {
	mu      sync.Mutex
	pending map[string]*sudoRequest
}

func (requests *sudoRequests) add(session, cwd string, argv []string) (*sudoRequest, error) {
	id, err := randomBrowserToken()
	if err != nil {
		return nil, err
	}
	request := &sudoRequest{
		view:      sudoRequestView{ID: id, Session: session, Display: shellWords(argv), CWD: shellWords([]string{cwd}), ExpiresAt: time.Now().Add(SudoTimeout)},
		cancelled: make(chan struct{}),
	}
	requests.mu.Lock()
	defer requests.mu.Unlock()
	if requests.pending == nil {
		requests.pending = make(map[string]*sudoRequest)
	}
	requests.pending[id] = request
	return request, nil
}

// remove reports whether the request was still pending, which it is not once the user has answered.
func (requests *sudoRequests) remove(id string) bool {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	_, pending := requests.pending[id]
	delete(requests.pending, id)
	return pending
}

// cancel ends the request if it is pending and allowed accepts its session, and reports whether it did.
func (requests *sudoRequests) cancel(id string, allowed func(session string) bool) bool {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	request, pending := requests.pending[id]
	if !pending || !allowed(request.view.Session) {
		return false
	}
	delete(requests.pending, id)
	close(request.cancelled)
	return true
}

func (requests *sudoRequests) of(session string) []sudoRequestView {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	views := []sudoRequestView{}
	for _, request := range requests.pending {
		if request.view.Session == session {
			views = append(views, request.view)
		}
	}
	slices.SortFunc(views, func(left, right sudoRequestView) int { return left.ExpiresAt.Compare(right.ExpiresAt) })
	return views
}

var plainShellWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWords quotes each word that is not plain, which also escapes control and bidirectional characters.
func shellWords(words []string) string {
	shown := make([]string, len(words))
	for index, word := range words {
		shown[index] = word
		if !plainShellWord.MatchString(word) {
			shown[index] = strconv.QuoteToASCII(word)
		}
	}
	return strings.Join(shown, " ")
}

// sudo asks the user, in the conversation of the session that runs 'gripi sudo', to answer a request to run a
// command as root, and waits for the answer.
func (app *application) sudo(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	argv, cwd, session := request.PostForm["argv"], request.PostFormValue("cwd"), request.PostFormValue("session")
	if len(argv) == 0 || argv[0] == "" || !filepath.IsAbs(cwd) {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "a sudo request needs a command and an absolute working directory"})
		return
	}
	if !app.validOwnedSessionPath(request, session) || !app.knownOrPendingSession(request, session) {
		writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": "the command runs in no session of yours in Gripi, so nobody could answer"})
		return
	}
	session, ok := app.resolveActionPendingPath(response, request, session)
	if !ok {
		return
	}
	pending, err := app.sudoRequests.add(session, cwd, argv)
	if err != nil {
		writeInternalError(response, "add a sudo request", err)
		return
	}
	timeout := time.NewTimer(time.Until(pending.view.ExpiresAt))
	defer timeout.Stop()
	select {
	case <-pending.cancelled:
	case <-timeout.C:
	case <-request.Context().Done():
	}
	// The user's answer takes the request away, so it wins even when it comes with the timeout.
	if !app.sudoRequests.remove(pending.view.ID) {
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": "the user cancelled the request in Gripi"})
		return
	}
	if request.Context().Err() == nil {
		writeJSONStatus(response, http.StatusRequestTimeout, map[string]string{"error": "nobody answered the request in Gripi in time"})
	}
}

func (app *application) cancelSudo(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	owned := func(session string) bool { return app.validOwnedSessionPath(request, session) }
	if !app.sudoRequests.cancel(request.PostFormValue("id"), owned) {
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": "The sudo request is no longer waiting for an answer."})
		return
	}
	writeJSON(response, map[string]bool{"ok": true})
}
