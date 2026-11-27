package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/access"
)

// The fake Pi answers a search with its words, one per line, or never answers, never takes it or fails for these.
const piSearchSlow, piSearchUnanswered, piSearchFail = "e2e-pi-search-slow", "e2e-pi-search-unanswered", "e2e-pi-search-fail"

type piSearchPayload struct {
	Sessions []string `json:"sessions"`
	Error    string   `json:"error"`
}

func piSearchRequest(ctx context.Context, fields map[string]string, cookie string) *http.Request {
	request := formActionRequest("/sessions/find_with_pi", fields, true)
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	return request.WithContext(ctx)
}

func findWithPi(t *testing.T, handler http.Handler, fields map[string]string, cookie string) (int, piSearchPayload) {
	t.Helper()
	response := serveAction(handler, piSearchRequest(context.Background(), fields, cookie))
	var payload piSearchPayload
	decodeActionJSON(t, response, &payload)
	return response.Code, payload
}

type fakePiRecord struct {
	PID     int      `json:"pid"`
	Event   string   `json:"event"`
	Args    []string `json:"args"`
	Command struct {
		Type string `json:"type"`
	} `json:"command"`
}

// piSearchRuns returns the fake Pis that searched for sessions, by PID, with whether each of them got the prompt and
// stopped.
func piSearchRuns(t *testing.T) map[int]*piSearchRun {
	t.Helper()
	contents, err := os.ReadFile(os.Getenv("GRIPI_E2E_FAKE_PI_LOG"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	runs := make(map[int]*piSearchRun)
	for line := range strings.SplitSeq(strings.TrimSpace(string(contents)), "\n") {
		var record fakePiRecord
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if record.Event == "started" && slices.Contains(record.Args, "search_sessions,show_session") {
			runs[record.PID] = &piSearchRun{args: record.Args}
		} else if run := runs[record.PID]; run != nil && record.Event == "command" && record.Command.Type == "prompt" {
			run.prompted = true
		} else if run != nil && record.Event == "stopped" {
			run.stopped = true
		}
	}
	return runs
}

type piSearchRun struct {
	args     []string
	prompted bool
	stopped  bool
}

func waitForPiSearchRuns(t *testing.T, done func(map[int]*piSearchRun) bool) map[int]*piSearchRun {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		runs := piSearchRuns(t)
		if done(runs) {
			return runs
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d fake Pis searched", len(runs))
		}
	}
}

// prompted waits for the fake Pis to get the prompt, by when they stop on SIGTERM with a record of it.
func prompted(count int) func(map[int]*piSearchRun) bool {
	return func(runs map[int]*piSearchRun) bool {
		for _, run := range runs {
			if !run.prompted {
				return false
			}
		}
		return len(runs) == count
	}
}

func allStopped(count int) func(map[int]*piSearchRun) bool {
	return func(runs map[int]*piSearchRun) bool {
		for _, run := range runs {
			if !run.stopped {
				return false
			}
		}
		return len(runs) == count
	}
}

func assertNoPiSearchFolders(t *testing.T, temporary string) {
	t.Helper()
	if folders, _ := filepath.Glob(filepath.Join(temporary, "gripi-scratch-*", "gateway-*", "session-*")); len(folders) != 0 {
		t.Fatalf("Pi's folders stayed: %v", folders)
	}
}

func TestPiFindsOnlyTheUsersOwnSessions(t *testing.T) {
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	handler, _, _, secret := multiUserFakePiGateway(t)

	// Of the IDs in Pi's answer, only alpha-0001 belongs to Alice; Bob owns beta-0002.
	cookie := "gripi_workspace=" + access.WorkspaceID(secret, alice)
	code, payload := findWithPi(t, handler, map[string]string{"q": "beta-0002  alpha-0001\n unknown-0003"}, cookie)
	if code != http.StatusOK || !slices.Equal(payload.Sessions, []string{"alpha-0001"}) {
		t.Fatalf("Alice's search = %d %+v", code, payload)
	}
	// Nor do sessions that the sidebar's filters leave out.
	if code, payload := findWithPi(t, handler, map[string]string{"q": "alpha-0001", "tag": "no-such-tag"}, cookie); code != http.StatusOK || len(payload.Sessions) != 0 {
		t.Fatalf("Alice's search within a tag = %d %+v", code, payload)
	}

	for _, run := range waitForPiSearchRuns(t, allStopped(2)) {
		for _, want := range []string{"--no-session", "--no-skills", "--no-context-files"} {
			if !slices.Contains(run.args, want) {
				t.Fatalf("Pi started with %v, without %s", run.args, want)
			}
		}
		if slices.Contains(run.args, "--skill") {
			t.Fatalf("Pi started with Gripi's skill: %v", run.args)
		}
	}
	assertNoPiSearchFolders(t, temporary)
}

func TestAPiSearchEndsWithItsRequestANewerOneOrTheGateway(t *testing.T) {
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	handler, _, _, _ := fakePiGateway(t)
	startSearch := func(ctx context.Context, query string) <-chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- serveAction(handler, piSearchRequest(ctx, map[string]string{"q": query}, "")) }()
		return done
	}
	start := func(ctx context.Context) <-chan *httptest.ResponseRecorder { return startSearch(ctx, piSearchSlow) }
	ended := func(done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		t.Helper()
		select {
		case response := <-done:
			return response
		case <-time.After(5 * time.Second):
			t.Fatal("the search didn't end")
			return nil
		}
	}

	// Also before Pi takes the prompt, which Gripi otherwise waits for.
	ctx, cancel := context.WithCancel(context.Background())
	unanswered := startSearch(ctx, piSearchUnanswered)
	waitForPiSearchRuns(t, prompted(1))
	cancel()
	ended(unanswered)
	waitForPiSearchRuns(t, allStopped(1))

	ctx, cancel = context.WithCancel(context.Background())
	cancelled := start(ctx)
	waitForPiSearchRuns(t, prompted(2))
	cancel()
	ended(cancelled)
	waitForPiSearchRuns(t, allStopped(2))

	older := start(context.Background())
	waitForPiSearchRuns(t, prompted(3))
	if code, payload := findWithPi(t, handler, map[string]string{"q": "alpha-0001"}, ""); code != http.StatusOK || !slices.Equal(payload.Sessions, []string{"alpha-0001"}) {
		t.Fatalf("the newer search = %d %+v", code, payload)
	}
	if response := ended(older); response.Code == http.StatusOK {
		t.Fatalf("the replaced search = %d %s", response.Code, response.Body.String())
	}
	waitForPiSearchRuns(t, allStopped(4))

	running := start(context.Background())
	waitForPiSearchRuns(t, prompted(5))
	if err := handler.(interface{ Close(context.Context) error }).Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	ended(running)
	waitForPiSearchRuns(t, allStopped(5))
	assertNoPiSearchFolders(t, temporary)
}

func TestAPiSearchSaysWhyItFailed(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	handler, _, _, _ := fakePiGateway(t)

	if code, payload := findWithPi(t, handler, map[string]string{"q": piSearchFail}, ""); code != http.StatusBadGateway || payload.Error != "No API key found for e2e." {
		t.Fatalf("failed search = %d %+v", code, payload)
	}
	if code, payload := findWithPi(t, handler, map[string]string{"q": "nothing-0003"}, ""); code != http.StatusOK || payload.Sessions == nil || len(payload.Sessions) != 0 {
		t.Fatalf("search that found nothing = %d %+v", code, payload)
	}
	for _, query := range []string{" \n ", strings.Repeat("a", 201)} {
		if response := serveAction(handler, piSearchRequest(context.Background(), map[string]string{"q": query}, "")); response.Code != http.StatusBadRequest {
			t.Fatalf("search for %q = %d %s", url.QueryEscape(query), response.Code, response.Body.String())
		}
	}
}

func TestTheSidebarListsWhatPiFoundWithWhatMatchesTheText(t *testing.T) {
	handler, alpha, beta, _ := fakePiGateway(t)
	sidebar := func(values url.Values) string {
		t.Helper()
		values.Set("no_session", "1")
		response := serveAction(handler, getActionRequest("/sidebar?"+values.Encode()))
		if response.Code != http.StatusOK {
			t.Fatalf("sidebar = %d %s", response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	listed := func(body, path string) bool { return strings.Contains(body, `data-session-path="`+path+`"`) }

	body := sidebar(url.Values{"session_search": {"no-such-text"}, "session_found": {"alpha-0001,unknown"}})
	if !listed(body, alpha) || listed(body, beta) {
		t.Fatalf("the sidebar of Pi's search lists alpha %t and beta %t", listed(body, alpha), listed(body, beta))
	}
	// Its links and its project filter keep what Pi found.
	if !strings.Contains(body, "session_found=alpha-0001%2Cunknown") || !strings.Contains(body, `name="session_found" value="alpha-0001,unknown"`) {
		t.Fatalf("the sidebar dropped what Pi found: %s", body)
	}

	// Without the search's text, what Pi found filters nothing and stays nowhere.
	body = sidebar(url.Values{"session_found": {"alpha-0001"}})
	if !listed(body, alpha) || !listed(body, beta) || strings.Contains(body, "session_found") {
		t.Fatalf("the sidebar without a search = %s", body)
	}
}
