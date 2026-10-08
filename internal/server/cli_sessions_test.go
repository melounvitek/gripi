package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var toggleMarkup = regexp.MustCompile(`<button [^>]*data-sidebar-cli-toggle[^>]*>.*?</button>`)

func TestSidebarHidesPiCLISessionsUntilTheBrowserShowsThem(t *testing.T) {
	cfg, alpha, _, project := fakePiConfig(t)
	path := func(id string) string { return filepath.Join(cfg.SessionsRoot, id+".jsonl") }
	// With alpha and beta, these fill more than the first page of 20.
	for day := 1; day <= 20; day++ {
		id := fmt.Sprintf("filler-%02d", day)
		writeLocalSession(t, path(id), id, project, fmt.Sprintf("2026-02-%02dT00:00:0", day))
	}
	cliPinned, cliAlpha, cliOpen := path("cli-pinned"), path("cli-alpha"), path("cli-open")
	for day, id := range []string{"cli-pinned", "cli-alpha", "cli-open"} {
		writeLocalSession(t, path(id), id, project, fmt.Sprintf("2026-03-%02dT00:00:0", day+1))
	}
	external, err := json.Marshal([]string{cliPinned, cliAlpha, cliOpen})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.ReadStatePath), "external-sessions.json"), external, 0600); err != nil {
		t.Fatal(err)
	}
	handler := startGateway(t, cfg)
	if response := serve(t, handler, http.MethodPost, "/sessions/pin", url.Values{"session": {cliPinned}, "pinned": {"true"}}.Encode()); response.Code != http.StatusOK {
		t.Fatalf("pin = %d %s", response.Code, response.Body.String())
	}

	sidebar := func(t *testing.T, query url.Values, shown bool) (toggle, header, current, list string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "http://app.test/sidebar?"+query.Encode(), nil)
		request.RemoteAddr = "127.0.0.1:1234"
		if shown {
			request.AddCookie(&http.Cookie{Name: "gripi_show_cli_sessions", Value: "1"})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("sidebar = %d %s", response.Code, response.Body.String())
		}
		header, list, _ = strings.Cut(response.Body.String(), `<div class="sessions-list">`)
		header, current, _ = strings.Cut(header, `class="current-session-section"`)
		return toggleMarkup.FindString(header), header, current, list
	}
	listed := func(html, path string) bool { return strings.Contains(html, `data-session-path="`+path+`"`) }
	rows := func(list string) int { return strings.Count(list, `<div class="session-row`) }

	t.Run("hidden by default", func(t *testing.T) {
		toggle, header, _, list := sidebar(t, url.Values{"no_session": {"1"}}, false)
		if listed(list, cliAlpha) || listed(list, cliOpen) || !listed(header, cliPinned) {
			t.Fatal("Pi CLI sessions are listed, or the pinned one is not")
		}
		for _, expected := range []string{`aria-pressed="false"`, `title="Show 2 Pi CLI sessions"`, `aria-label="Show 2 Pi CLI sessions"`, `class="sidebar-tool-slash"`} {
			if !strings.Contains(toggle, expected) {
				t.Errorf("toggle missing %s", expected)
			}
		}
		if strings.Contains(header, "data-sidebar-filter-count") {
			t.Error("hiding Pi CLI sessions counts as a filter")
		}
		if count := rows(list); count != 20 || !strings.Contains(list, "Load 2 more") {
			t.Errorf("first page has %d rows and no load more for the other two", count)
		}
		if !strings.Contains(list, `data-session-shortcut="2"`) || strings.Contains(list, `data-session-shortcut="1"`) {
			t.Error("shortcuts do not follow the listed rows")
		}
	})

	t.Run("shown with the cookie", func(t *testing.T) {
		toggle, header, _, list := sidebar(t, url.Values{"no_session": {"1"}}, true)
		if !listed(list, cliAlpha) || !listed(list, cliOpen) || !listed(header, cliPinned) || listed(list, cliPinned) {
			t.Fatal("Pi CLI sessions are not listed once shown")
		}
		for _, expected := range []string{`aria-pressed="true"`, `title="Hide 2 Pi CLI sessions"`} {
			if !strings.Contains(toggle, expected) {
				t.Errorf("toggle missing %s", expected)
			}
		}
		if strings.Contains(toggle, "sidebar-tool-slash") {
			t.Error("shown toggle is slashed")
		}
		if !strings.Contains(list, "Load 4 more") {
			t.Error("pagination does not count the shown Pi CLI sessions")
		}
	})

	t.Run("open session stays visible", func(t *testing.T) {
		_, _, current, list := sidebar(t, url.Values{"session": {cliOpen}}, false)
		if !listed(current, cliOpen) || listed(list, cliOpen) {
			t.Fatal("the open Pi CLI session is not the current session")
		}
	})

	t.Run("filters count only listed sessions", func(t *testing.T) {
		search := url.Values{"no_session": {"1"}, "session_search": {"alpha"}}
		toggle, header, _, list := sidebar(t, search, false)
		if !listed(list, alpha) || listed(list, cliAlpha) {
			t.Fatal("search lists a hidden Pi CLI session")
		}
		if !strings.Contains(header, `data-sidebar-filter-count>1 of 22</span>`) || !strings.Contains(toggle, `title="Show 1 Pi CLI session"`) {
			t.Error("hidden search counts the hidden Pi CLI session")
		}
		_, header, _, list = sidebar(t, search, true)
		if !listed(list, cliAlpha) || !strings.Contains(header, `data-sidebar-filter-count>2 of 24</span>`) {
			t.Error("shown search does not list and count the Pi CLI session")
		}

		// The slashed toggle hints at what a search finds only among hidden sessions.
		toggle, _, _, list = sidebar(t, url.Values{"no_session": {"1"}, "session_search": {"cli-alpha"}}, false)
		if !strings.Contains(toggle, `title="Show 1 Pi CLI session"`) || !strings.Contains(list, "No sessions match.") {
			t.Error("search matching only a hidden Pi CLI session gives no hint")
		}
	})

	t.Run("no toggle without listable Pi CLI sessions", func(t *testing.T) {
		for _, query := range []string{"beta", "pinned"} {
			if toggle, _, _, _ := sidebar(t, url.Values{"no_session": {"1"}, "session_search": {query}}, true); toggle != "" {
				t.Errorf("search %q shows the toggle", query)
			}
		}
	})
}
