package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/sessions"
)

// compactingClient holds prompts back while it compacts, as Pi's client does.
type compactingClient struct {
	*clearQueueClient
	deferring atomic.Bool
}

func (client *compactingClient) DeferringCompactionPrompts() bool { return client.deferring.Load() }
func (client *compactingClient) DeferPrompt(context.Context, agent.Prompt) (agent.PromptResult, bool, error) {
	return agent.PromptResult{QueuedAfterCompaction: true}, true, nil
}

// subSessionReportFixture has a sub-session whose client owes its parent a report, and records the reports sent.
func subSessionReportFixture(t *testing.T) (*clearQueueFixture, *compactingClient, string, <-chan string) {
	t.Helper()
	f := newClearQueueFixture(t)
	client := &compactingClient{clearQueueClient: f.client}
	path := f.session(t, "sub")
	if err := f.app.rpcClients.Register(path, client); err != nil {
		t.Fatal(err)
	}
	reported := make(chan string, 4)
	f.app.commandRoutes = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		reported <- request.FormValue("message")
		writeJSON(response, map[string]any{"session": request.FormValue("session")})
	})
	f.app.subSessionReports.app = f.app
	f.app.subSessionReports.expect(client, f.path)
	return f, client, path, reported
}

func nextReport(t *testing.T, reported <-chan string) string {
	t.Helper()
	select {
	case message := <-reported:
		return message
	case <-time.After(5 * time.Second):
		t.Fatal("no report")
		return ""
	}
}

func TestAReportWaitsForTheMessageThatACompactionHoldsBack(t *testing.T) {
	f, client, _, reported := subSessionReportFixture(t)
	reports := &f.app.subSessionReports
	// The run that compacts settles before the parent's message, which the client held back, runs.
	client.deferring.Store(true)
	reports.Observe(client, agent.Lifecycle{Step: agent.RunSettled})
	client.deferring.Store(false)
	reports.Observe(client, agent.Lifecycle{Step: agent.RunEnded, Error: "the held-back message failed"})
	reports.Observe(client, agent.Lifecycle{Step: agent.RunSettled})
	if message := nextReport(t, reported); !strings.Contains(message, "failed: the held-back message failed") {
		t.Fatalf("report = %q", message)
	}
}

// settlingClient settles the run of a prompt before Prompt returns, as Pi's reader may when the run fails at once.
type settlingClient struct {
	*clearQueueClient
	reports *subSessionReports
}

func (client *settlingClient) Prompt(context.Context, agent.Prompt) (agent.PromptResult, error) {
	client.reports.Observe(client, agent.Lifecycle{Step: agent.RunSettled})
	return agent.PromptResult{}, nil
}

func TestARunThatSettlesBeforePromptReturnsReportsOnce(t *testing.T) {
	f, _, _, reported := subSessionReportFixture(t)
	reports := &f.app.subSessionReports
	client := &settlingClient{clearQueueClient: f.client, reports: reports}
	path := f.session(t, "fast")
	if err := f.app.rpcClients.Register(path, client); err != nil {
		t.Fatal(err)
	}
	promptFromParent(t, f, path, "fail at once")
	if message := nextReport(t, reported); !strings.Contains(message, "finished") {
		t.Fatalf("report = %q", message)
	}
	// A later turn, such as from the browser, owes the parent nothing.
	reports.Observe(client, agent.Lifecycle{Step: agent.RunSettled})
	select {
	case message := <-reported:
		t.Fatalf("a later turn reported %q", message)
	case <-time.After(200 * time.Millisecond):
	}
}

// handlingClient runs an extension command at once while it compacts, as Pi's client does.
type handlingClient struct{ *compactingClient }

func (client *handlingClient) DeferPrompt(context.Context, agent.Prompt) (agent.PromptResult, bool, error) {
	return agent.PromptResult{Disposition: "handled"}, true, nil
}

func TestACommandThatRunsAtOnceDuringACompactionOwesNoReport(t *testing.T) {
	f, _, _, reported := subSessionReportFixture(t)
	reports := &f.app.subSessionReports
	client := &handlingClient{&compactingClient{clearQueueClient: f.client}}
	path := f.session(t, "compacting")
	if err := f.app.rpcClients.Register(path, client); err != nil {
		t.Fatal(err)
	}
	promptFromParent(t, f, path, "/immediate-command")
	// A later turn, such as from the browser, owes the parent nothing.
	reports.Observe(client, agent.Lifecycle{Step: agent.RunSettled})
	select {
	case message := <-reported:
		t.Fatalf("a later turn reported %q", message)
	case <-time.After(200 * time.Millisecond):
	}
}

// promptFromParent links the session under the fixture's session and sends it a message from there, as gripi send does.
func promptFromParent(t *testing.T, f *clearQueueFixture, path, message string) {
	t.Helper()
	f.app.gatewayState = sessions.NewGatewayState(filepath.Join(t.TempDir(), "read.json"), "", "", nil)
	if err := f.app.gatewayState.SetSessionParent(path, f.path); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"session": {path}, "message": {message}, "streaming_behavior": {"follow_up"}, "bash_mode": {"prompt"}, "sender": {f.path}}
	request := httptest.NewRequest(http.MethodPost, "/prompt", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()
	commandRequests(f.handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
	}
}

func TestAClearedMessageOfTheParentLeavesNoReportForALaterTurn(t *testing.T) {
	f, client, path, reported := subSessionReportFixture(t)
	reports := &f.app.subSessionReports
	// The parent's message waited for a compaction, without a run, when the user cleared the queue.
	clearQueuePayload(t, f.serve(context.Background(), path), http.StatusOK)
	// A later turn, such as from the browser, owes the parent nothing.
	reports.Observe(client, agent.Lifecycle{Step: agent.RunSettled})
	select {
	case message := <-reported:
		t.Fatalf("a later turn reported %q", message)
	case <-time.After(200 * time.Millisecond):
	}
}
