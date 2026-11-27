package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

// The sidebar's search can ask a hidden Pi for the sessions about its text. Pi runs as in a scratch session, but can
// do nothing but search and read the user's sessions, as a conversation it reads could tell it to do anything. It
// answers with the sessions' IDs.

const (
	piSearchTimeout    = 2 * time.Minute
	piSearchQueryBytes = 200
	piSearchResults    = 20
)

const piSearchPrompt = "Find the Gripi sessions about: %s\n\n" +
	"Search their conversations with search_sessions, which matches literal text and ignores case, so try a few short words and phrasings. " +
	"Read a session with show_session when unsure whether it matches.\n" +
	"Reply with only the full IDs of the matching sessions, one per line, best first, at most 20, or NONE."

// piSearchWord finds the words of Pi's answer, among which are the IDs.
var piSearchWord = regexp.MustCompile(`[A-Za-z0-9._-]+`)

var errPiSearchReplaced = errors.New("a newer search replaced this one")

// piSearches are the searches that Pi runs, one per user at a time.
type piSearches struct {
	mu      sync.Mutex
	stopped bool
	byOwner map[string]*piSearch
	running sync.WaitGroup
}

type piSearch struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// begin starts the user's search once the user's older one, which it stops, has ended. It reports false when the
// gateway stops.
func (searches *piSearches) begin(owner string, cancel context.CancelCauseFunc) (finish func(), ok bool) {
	search := &piSearch{cancel: cancel, done: make(chan struct{})}
	searches.mu.Lock()
	if searches.stopped {
		searches.mu.Unlock()
		return nil, false
	}
	if searches.byOwner == nil {
		searches.byOwner = make(map[string]*piSearch)
	}
	older := searches.byOwner[owner]
	searches.byOwner[owner] = search
	searches.running.Add(1)
	searches.mu.Unlock()
	if older != nil {
		older.cancel(errPiSearchReplaced)
		// Pi ends quickly once stopped, and the next search waits for this one.
		<-older.done
	}
	return func() {
		searches.mu.Lock()
		if searches.byOwner[owner] == search {
			delete(searches.byOwner, owner)
		}
		searches.mu.Unlock()
		close(search.done)
		searches.running.Done()
	}, true
}

// stop stops every search and refuses new ones, as the gateway stops.
func (searches *piSearches) stop() {
	searches.mu.Lock()
	defer searches.mu.Unlock()
	searches.stopped = true
	for _, search := range searches.byOwner {
		search.cancel(nil)
	}
}

// wait waits for the Pis of the searches that stop stopped to end.
func (searches *piSearches) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		searches.running.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// EndPiSearches stops the searches that Pi runs for the sidebar, and refuses new ones, so that they don't hold the
// gateway's shutdown.
func (handler *Handler) EndPiSearches() {
	handler.app.piSearches.stop()
}

func (app *application) registerPiSearchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /sessions/find_with_pi", app.findSessionsWithPi)
}

func (app *application) findSessionsWithPi(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	query := strings.Join(strings.Fields(request.FormValue("q")), " ")
	if query == "" || len(query) > piSearchQueryBytes {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("Pi searches for 1 to %d characters.", piSearchQueryBytes)})
		return
	}
	if app.scratch.folders == nil {
		writeJSONStatus(response, http.StatusServiceUnavailable, map[string]string{"error": "Gripi has no folder for Pi to search in."})
		return
	}
	ctx, cancel := context.WithCancelCause(request.Context())
	defer cancel(nil)
	finish, ok := app.piSearches.begin(currentWorkspaceID(request), cancel)
	if !ok {
		writeJSONStatus(response, http.StatusServiceUnavailable, map[string]string{"error": "Gripi is stopping."})
		return
	}
	defer finish()
	timed, stopTimer := context.WithTimeout(ctx, piSearchTimeout)
	defer stopTimer()
	reply, err := app.askPiToFindSessions(timed, request, query)
	switch {
	case err == nil:
	case request.Context().Err() != nil:
		// The page stopped the search.
		return
	case errors.Is(context.Cause(ctx), errPiSearchReplaced):
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": "A newer search replaced this one."})
		return
	case ctx.Err() != nil:
		writeJSONStatus(response, http.StatusServiceUnavailable, map[string]string{"error": "Gripi is stopping."})
		return
	case timed.Err() != nil:
		writeJSONStatus(response, http.StatusGatewayTimeout, map[string]string{"error": "Pi took too long."})
		return
	default:
		writeJSONStatus(response, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	if !acquireRequestSlot(response, request, app.heavyRequests) {
		return
	}
	defer releaseRequestSlot(app.heavyRequests)
	// Only the sessions that the user's sidebar lists count, without marking any of them read.
	sidebar := request.Clone(request.Context())
	sidebar.URL = &url.URL{Path: "/sidebar", RawQuery: "no_session=1"}
	view, err := app.preparePage(sidebar, false)
	if err != nil {
		writeInternalError(response, "list the sessions that Pi found", err)
		return
	}
	listed := make(map[string]bool, len(view.Sessions))
	for _, session := range view.Sessions {
		listed[session.ID] = true
	}
	found := []string{}
	for _, word := range piSearchWord.FindAllString(reply, -1) {
		id := strings.Trim(word, "._-")
		if listed[id] && !slices.Contains(found, id) && len(found) < piSearchResults {
			found = append(found, id)
		}
	}
	writeJSON(response, map[string][]string{"sessions": found})
}

// askPiToFindSessions returns the last reply of a hidden Pi asked for the sessions about query. Pi and its folder are
// gone when it returns.
func (app *application) askPiToFindSessions(ctx context.Context, request *http.Request, query string) (string, error) {
	dir, err := app.scratch.folders.create()
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	variables, err := app.piEnvironment(app.environmentUser(request))
	if err != nil {
		return "", err
	}
	variables = append(variables, "TMPDIR="+filepath.Join(dir, "tmp"))
	// Pi searches within the sidebar's filters, whose sessions alone it would show.
	if project := request.FormValue("project"); project != "" {
		variables = append(variables, "GRIPI_FIND_CWD="+project)
	}
	if tag := request.FormValue("tag"); tag != "" {
		variables = append(variables, "GRIPI_FIND_TAG="+tag)
	}
	run := &piSearchRun{settled: make(chan struct{})}
	client, err := app.adapter.StartInCWD(filepath.Join(dir, "work"), "", agent.StartOptions{Scratch: true, FindSessions: true, Env: variables, Diagnostics: app.rpcDiagnostics, Observe: run.observe})
	if err != nil {
		return "", err
	}
	defer client.Close()
	if _, err := client.Prompt(ctx, agent.Prompt{Text: fmt.Sprintf(piSearchPrompt, query)}); err != nil {
		return "", err
	}
	exiter, _ := client.(agent.Exiter)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-run.settled:
			return run.reply()
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			// Pi's output tells of no crash.
			if exiter != nil && exiter.Exited() {
				return "", errors.New("Pi ended before it answered")
			}
		}
	}
}

// piSearchRun follows the run of a Pi that searches, which may retry before it settles.
type piSearchRun struct {
	mu      sync.Mutex
	ended   agent.Lifecycle
	settle  sync.Once
	settled chan struct{}
}

func (run *piSearchRun) observe(_ agent.Client, lifecycle agent.Lifecycle) {
	switch lifecycle.Step {
	case agent.RunEnded:
		run.mu.Lock()
		run.ended = lifecycle
		run.mu.Unlock()
	case agent.RunSettled:
		run.settle.Do(func() { close(run.settled) })
	}
}

func (run *piSearchRun) reply() (string, error) {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.ended.Error != "" {
		return "", errors.New(run.ended.Error)
	}
	return run.ended.Reply.Text, nil
}
