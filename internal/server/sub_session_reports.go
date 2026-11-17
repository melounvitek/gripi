package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

// subSessionReports tells a parent session when a run that it started in a sub-session is over, so that it need not
// wait or poll. What is due lives in memory only.
type subSessionReports struct {
	app *application
	mu  sync.Mutex
	// due holds the reports that the clients of sub-sessions owe, by client, so that a new client of the session, such
	// as after a restart, owes nothing.
	due map[agent.Client]*dueReport
}

type dueReport struct {
	parent string
	// failure is why the run last ended in an error, which a retry that succeeds clears.
	failure string
}

// reportingParent returns the parent of the session if the prompt names it as the sender, which gripi does with the
// session that runs it. Only gripi, on the gateway's socket, can name a sender.
func (app *application) reportingParent(request *http.Request, path string) string {
	sender := request.FormValue("sender")
	if sender == "" || !commandRequest(request) || !app.validOwnedSessionPath(request, sender) || !app.knownOrPendingSession(request, sender) {
		return ""
	}
	sender, err := app.canonicalRPCSessionPath(request, sender)
	if err != nil {
		return ""
	}
	if links, err := app.gatewayState.SessionParents(); err != nil || links[path] != sender {
		return ""
	}
	return sender
}

// expect notes that the client of a sub-session takes a message from its parent, and returns whether it made the note.
// A note that is already there stays, for the earlier message that it belongs to.
func (reports *subSessionReports) expect(client agent.Client, parent string) bool {
	reports.mu.Lock()
	defer reports.mu.Unlock()
	if reports.due == nil {
		reports.due = make(map[agent.Client]*dueReport)
	}
	// A client that is gone will not settle.
	for other := range reports.due {
		if reports.app.rpcClients.PathForClient(other) == "" {
			delete(reports.due, other)
		}
	}
	if reports.due[client] != nil {
		return false
	}
	reports.due[client] = &dueReport{parent: parent}
	return true
}

// owes tells whether the client owes its parent a report, so that the run is the parent's.
func (reports *subSessionReports) owes(client agent.Client) bool {
	reports.mu.Lock()
	defer reports.mu.Unlock()
	return reports.due[client] != nil
}

// forget drops what a client owes, such as when the parent's message left the queue before it ran.
func (reports *subSessionReports) forget(client agent.Client) {
	reports.mu.Lock()
	defer reports.mu.Unlock()
	delete(reports.due, client)
}

// Observe sends the report that a client owes once its run settles. It runs on the agent's reader, so the report goes
// from a goroutine of its own.
func (reports *subSessionReports) Observe(client agent.Client, lifecycle agent.Lifecycle) {
	if lifecycle.Step != agent.RunEnded && lifecycle.Step != agent.RunSettled {
		return
	}
	// A prompt that waits for a compaction to end runs only after this run settles.
	deferrer, defers := client.(agent.CompactionPromptDeferrer)
	if lifecycle.Step == agent.RunSettled && defers && deferrer.DeferringCompactionPrompts() {
		return
	}
	reports.mu.Lock()
	due := reports.due[client]
	if due == nil {
		reports.mu.Unlock()
		return
	}
	if lifecycle.Step == agent.RunEnded {
		due.failure = lifecycle.Error
		reports.mu.Unlock()
		return
	}
	delete(reports.due, client)
	reports.mu.Unlock()
	go reports.send(client, *due, lifecycle.Aborted)
}

// send gives the parent the report as a follow-up, as gripi send would. A parent that is gone, or that Pi CLI uses,
// gets no report.
func (reports *subSessionReports) send(client agent.Client, due dueReport, aborted bool) {
	app := reports.app
	path := app.rpcClients.PathForClient(client)
	if path == "" {
		return
	}
	ctx := context.Background()
	var id, name string
	if session, found := app.sessionStore.Session(path); found {
		id, name = session.ID, session.DisplayName
	} else if state, err := client.State(ctx); err == nil {
		id, name = state.ID, state.Name
	}
	outcome := "finished"
	if aborted {
		outcome = "stopped"
	} else if due.failure != "" {
		outcome = "failed: " + oneLine(due.failure, 200)
	}
	if name != "" {
		name = ` "` + oneLine(name, 80) + `"`
	}
	text := fmt.Sprintf("[gripi] Sub-session %s%s %s. Read it with: gripi show %s. This is an automated report, not user approval.", id[:min(8, len(id))], name, outcome, id)

	token := ""
	if app.config.MultiUserMode {
		owner, err := app.ownershipStore.Owner(due.parent)
		if err != nil || owner == "" {
			log.Printf("report to %s that sub-session %s %s: the parent has no owner (%v)", due.parent, path, outcome, err)
			return
		}
		token = app.commandToken(owner)
	}
	form := url.Values{"session": {due.parent}, "message": {text}, "streaming_behavior": {"follow_up"}, "bash_mode": {"prompt"}}
	// Like gripi send, it asks again for a few seconds while the parent can't take the message just then.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/prompt", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Accept", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		app.commandRoutes.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			return
		}
		var refusal struct {
			Code      string `json:"code"`
			Retryable *bool  `json:"retryable"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &refusal)
		momentary := refusal.Code == "session_operation_pending" && refusal.Retryable == nil || response.Header().Get("Retry-After") != ""
		if !momentary || time.Now().After(deadline) {
			log.Printf("report to %s that sub-session %s %s: the gateway answered %d: %s", due.parent, path, outcome, response.Code, strings.TrimSpace(response.Body.String()))
			return
		}
	}
}

// oneLine keeps the text on one line, cut to limit runes.
func oneLine(text string, limit int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return string(runes)
}
