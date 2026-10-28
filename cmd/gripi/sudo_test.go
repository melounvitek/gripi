package main

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	gateway "github.com/melounvitek/gripi/internal/server"
)

type cliResult struct {
	code           int
	stdout, stderr string
}

// startCLI runs a command that blocks, such as gripi sudo, in the background.
func startCLI(arguments ...string) <-chan cliResult {
	result := make(chan cliResult, 1)
	go func() {
		code, stdout, stderr := runCLI(arguments...)
		result <- cliResult{code, stdout, stderr}
	}()
	return result
}

func browserRequest(method, target string, form url.Values, cookie string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://127.0.0.1:4567"+target, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response := httptest.NewRecorder()
	browser.ServeHTTP(response, request)
	return response
}

// sudoRequestsInEvents returns the sudo requests that the browser's live events show for a session.
func sudoRequestsInEvents(t *testing.T, session, cookie string) []map[string]any {
	t.Helper()
	response := browserRequest(http.MethodGet, "/events?session="+url.QueryEscape(session), nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("events of %s = %d %s", session, response.Code, response.Body.String())
	}
	var payload struct {
		SudoRequests []map[string]any `json:"sudo_requests"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.SudoRequests == nil {
		t.Fatalf("events of %s have no sudo requests (%v): %s", session, err, response.Body.String())
	}
	return payload.SudoRequests
}

func sudoRequestsUntil(t *testing.T, session, cookie string, count int) []map[string]any {
	t.Helper()
	var requests []map[string]any
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if requests = sudoRequestsInEvents(t, session, cookie); len(requests) == count {
			return requests
		}
	}
	t.Fatalf("%s shows %d sudo requests, want %d: %v", session, len(requests), count, requests)
	return nil
}

var renderedSudoRequests = regexp.MustCompile(`data-sudo-requests="([^"]*)"`)

func cancelSudo(id, cookie string) int {
	return browserRequest(http.MethodPost, "/sudo/cancel", url.Values{"id": {id}}, cookie).Code
}

func outsideSessions(t *testing.T) {
	t.Setenv("PI_SESSION_FILE", "")
	t.Setenv("HERMES_SESSION_ID", "")
}

func TestSudoAsksInTheConversationUntilTheUserCancels(t *testing.T) {
	alpha, beta := fakePiGateway(t)
	outsideSessions(t)
	t.Setenv("PI_SESSION_FILE", alpha)
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	asked := time.Now()
	result := startCLI("sudo", "--", "apt", "install", "-y", "two words", "", "x\u202ey")
	requests := sudoRequestsUntil(t, alpha, "", 1)
	request := requests[0]
	keys := make([]string, 0, len(request))
	for key := range request {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	// The browser sees only what the dialog shows.
	if !slices.Equal(keys, []string{"cwd", "display", "expires_at", "id", "session"}) {
		t.Fatalf("the browser sees %v", request)
	}
	expires, _ := time.Parse(time.RFC3339Nano, request["expires_at"].(string))
	if request["session"] != alpha || request["cwd"] != directory || request["id"] == "" || expires.Before(asked.Add(gateway.SudoTimeout)) || expires.After(time.Now().Add(gateway.SudoTimeout)) {
		t.Fatalf("sudo request = %v", request)
	}
	if display := request["display"]; display != `apt install -y "two words" "" "x\u202ey"` {
		t.Fatalf("display = %s", display)
	}
	if others := sudoRequestsInEvents(t, beta, ""); len(others) != 0 {
		t.Fatalf("another session shows %v", others)
	}

	page := browserRequest(http.MethodGet, "/?session="+url.QueryEscape(alpha), nil, "")
	match := renderedSudoRequests.FindStringSubmatch(page.Body.String())
	if page.Code != http.StatusOK || match == nil {
		t.Fatalf("page = %d without sudo requests", page.Code)
	}
	var rendered []map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(match[1])), &rendered); err != nil || len(rendered) != 1 || rendered[0]["id"] != request["id"] || rendered[0]["display"] != request["display"] {
		t.Fatalf("rendered sudo requests = %v (%v)", rendered, err)
	}

	if code := cancelSudo(request["id"].(string), ""); code != http.StatusOK {
		t.Fatalf("cancel = %d", code)
	}
	if answered := <-result; answered.code != 1 || answered.stdout != "" || !strings.Contains(answered.stderr, "gripi sudo: ") || !strings.Contains(answered.stderr, "cancelled") {
		t.Fatalf("gripi sudo = %+v", answered)
	}
	// The first answer wins, and an answered request is gone.
	if code := cancelSudo(request["id"].(string), ""); code != http.StatusConflict {
		t.Fatalf("second cancel = %d", code)
	}
	if requests := sudoRequestsInEvents(t, alpha, ""); len(requests) != 0 {
		t.Fatalf("answered request is still shown: %v", requests)
	}
}

func TestSudoGivesUpWhenNobodyAnswers(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	outsideSessions(t)
	t.Setenv("PI_SESSION_FILE", alpha)
	defer func(timeout time.Duration) { gateway.SudoTimeout = timeout }(gateway.SudoTimeout)
	gateway.SudoTimeout = 300 * time.Millisecond

	if code, stdout, stderr := runCLI("sudo", "--", "true"); code != 1 || stdout != "" || !strings.Contains(stderr, "in time") {
		t.Fatalf("gripi sudo = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if requests := sudoRequestsInEvents(t, alpha, ""); len(requests) != 0 {
		t.Fatalf("expired request is still shown: %v", requests)
	}
}

func TestSudoForgetsARequestWhoseCommandEnded(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://gripi/sudo", strings.NewReader(url.Values{"session": {alpha}, "cwd": {"/"}, "argv": {"true"}}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if response, err := socketClient(os.Getenv("GRIPI_SOCKET_PATH")).Do(request); err == nil {
			response.Body.Close()
		}
	}()
	sudoRequestsUntil(t, alpha, "", 1)
	cancel()
	<-done
	sudoRequestsUntil(t, alpha, "", 0)
}

func TestSudoFailsAtOnceOutsideASessionOfTheGateway(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	outsideSessions(t)
	for _, usage := range [][]string{{"sudo"}, {"sudo", "--"}, {"sudo", "apt", "update"}} {
		if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help sudo") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
		}
	}
	if code, _, stderr := runCLI("sudo", "--", "true"); code != 1 || !strings.Contains(stderr, "PI_SESSION_FILE") || !strings.Contains(stderr, "HERMES_SESSION_ID") {
		t.Fatalf("gripi sudo outside a session = %d, stderr %q", code, stderr)
	}
	for variable, value := range map[string]string{"PI_SESSION_FILE": alpha + ".missing", "HERMES_SESSION_ID": "unknown"} {
		outsideSessions(t)
		t.Setenv(variable, value)
		if code, _, stderr := runCLI("sudo", "--", "true"); code != 1 || !strings.Contains(stderr, "no session of yours") {
			t.Fatalf("gripi sudo in an unknown session = %d, stderr %q", code, stderr)
		}
	}
}

func TestSudoAsksInANewSessionBeforeItsFirstReply(t *testing.T) {
	fakePiGateway(t)
	outsideSessions(t)
	_, stdout, _ := runCLI("new", t.TempDir(), "--json")
	started := decodeSession(t, stdout)
	t.Setenv("PI_SESSION_FILE", started.Path)

	result := startCLI("sudo", "--", "true")
	request := sudoRequestsUntil(t, started.Path, "", 1)[0]
	if code := cancelSudo(request["id"].(string), ""); code != http.StatusOK {
		t.Fatalf("cancel = %d", code)
	}
	if answered := <-result; answered.code != 1 {
		t.Fatalf("gripi sudo = %+v", answered)
	}
}

func TestSudoAsksInTheHermesConversationThatRunsIt(t *testing.T) {
	hermesGateway(t, fakeHermesSession{SessionID: "hermes-e2e", CWD: t.TempDir(), Title: "A Hermes session", UpdatedAt: "2026-03-01T10:00:00Z"})
	outsideSessions(t)
	t.Setenv("HERMES_SESSION_ID", "hermes-e2e")

	result := startCLI("sudo", "--", "true")
	request := sudoRequestsUntil(t, "hermes:hermes-e2e", "", 1)[0]
	if code := cancelSudo(request["id"].(string), ""); code != http.StatusOK {
		t.Fatalf("cancel = %d", code)
	}
	if answered := <-result; answered.code != 1 || !strings.Contains(answered.stderr, "cancelled") {
		t.Fatalf("gripi sudo = %+v", answered)
	}
}

func TestOnlyTheOwnerOfTheSessionSeesAndAnswersItsSudoRequest(t *testing.T) {
	alpha, beta := multiUserGateway(t)
	outsideSessions(t)
	alice, bob := workspaceCookies[aliceToken], workspaceCookies[bobToken]
	t.Setenv("PI_SESSION_FILE", alpha)

	t.Setenv("GRIPI_USER_TOKEN", bobToken)
	if code, _, stderr := runCLI("sudo", "--", "true"); code != 1 || !strings.Contains(stderr, "no session of yours") {
		t.Fatalf("gripi sudo for Bob in Alice's session = %d, stderr %q", code, stderr)
	}

	t.Setenv("GRIPI_USER_TOKEN", aliceToken)
	result := startCLI("sudo", "--", "true")
	request := sudoRequestsUntil(t, alpha, alice, 1)[0]
	if events := browserRequest(http.MethodGet, "/events?session="+url.QueryEscape(alpha), nil, bob); events.Code != http.StatusNotFound {
		t.Fatalf("Bob reads Alice's events: %d", events.Code)
	}
	if requests := sudoRequestsInEvents(t, beta, bob); len(requests) != 0 {
		t.Fatalf("Bob sees %v", requests)
	}
	if code := cancelSudo(request["id"].(string), bob); code != http.StatusConflict {
		t.Fatalf("Bob's cancel = %d", code)
	}
	if requests := sudoRequestsInEvents(t, alpha, alice); len(requests) != 1 {
		t.Fatalf("Bob's cancel changed Alice's requests: %v", requests)
	}
	if code := cancelSudo(request["id"].(string), alice); code != http.StatusOK {
		t.Fatalf("Alice's cancel = %d", code)
	}
	if answered := <-result; answered.code != 1 || !strings.Contains(answered.stderr, "cancelled") {
		t.Fatalf("gripi sudo = %+v", answered)
	}
}
