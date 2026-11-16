package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var sidebarRow = regexp.MustCompile(`<div class="session-row([^"]*)" data-session-tags="[^"]*" data-session-path="([^"]*)"`)

func TestSidebarNestsLinkedSessionsUnderTheirParent(t *testing.T) {
	cfg, alpha, beta, project := fakePiConfig(t)
	path := func(id string) string { return filepath.Join(cfg.SessionsRoot, id+".jsonl") }
	ids := []string{"pinned-parent", "pinned-done", "older", "done", "continued", "parent"}
	for day, id := range ids {
		writeLocalSession(t, path(id), id, project, fmt.Sprintf("2026-03-%02dT00:00:0", day+1))
	}
	parent, pinnedParent, done, older, continued, pinnedDone := path("parent"), path("pinned-parent"), path("done"), path("older"), path("continued"), path("pinned-done")
	state := filepath.Dir(cfg.ReadStatePath)
	for name, value := range map[string]any{
		// Pi CLI ran these workers and is done; the continued one went on in Gripi.
		"external-sessions.json": []string{done, older, pinnedDone},
		"session-parents.json":   map[string]string{done: parent, older: parent, continued: parent, pinnedDone: pinnedParent, alpha: beta, beta: alpha},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler := startGateway(t, cfg)
	if response := serve(t, handler, http.MethodPost, "/sessions/pin", url.Values{"session": {pinnedParent}, "pinned": {"true"}}.Encode()); response.Code != http.StatusOK {
		t.Fatalf("pin = %d %s", response.Code, response.Body.String())
	}

	sidebar := func(t *testing.T, query url.Values, shown bool) string {
		t.Helper()
		request := getActionRequest("/sidebar?" + query.Encode())
		if shown {
			request.AddCookie(&http.Cookie{Name: "gripi_show_cli_sessions", Value: "1"})
		}
		response := serveAction(handler, request)
		if response.Code != http.StatusOK {
			t.Fatalf("sidebar = %d %s", response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	// rows lists the rows in order, a worker as "  " and its ID.
	rows := func(html string) []string {
		var result []string
		for _, match := range sidebarRow.FindAllStringSubmatch(html, -1) {
			row := strings.TrimSuffix(filepath.Base(match[2]), ".jsonl")
			if strings.Contains(match[1], "is-worker") {
				row = "  " + row
			}
			result = append(result, row)
		}
		return result
	}
	toggle := func(html string) string { return toggleMarkup.FindString(html) }

	t.Run("finished workers are hidden until Pi CLI sessions show", func(t *testing.T) {
		html := sidebar(t, url.Values{"no_session": {"1"}}, false)
		if got, want := rows(html), []string{"pinned-parent", "parent", "  continued", "beta", "alpha"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("rows = %q, want %q", got, want)
		}
		if !strings.Contains(toggle(html), `title="Show 3 Pi CLI sessions"`) {
			t.Errorf("the toggle does not count the finished workers: %s", toggle(html))
		}
		// Workers coming and going must not renumber the rows below them.
		if !strings.Contains(html, `data-session-shortcut="3"`) || strings.Count(html, "data-session-shortcut=") != 4 {
			t.Error("a worker took a shortcut")
		}
	})

	t.Run("shown under their parent with Pi CLI sessions", func(t *testing.T) {
		html := sidebar(t, url.Values{"no_session": {"1"}}, true)
		want := []string{"pinned-parent", "  pinned-done", "parent", "  continued", "  done", "  older", "beta", "alpha"}
		if got := rows(html); !reflect.DeepEqual(got, want) {
			t.Fatalf("rows = %q, want %q", got, want)
		}
		if !strings.Contains(toggle(html), `title="Hide 3 Pi CLI sessions"`) {
			t.Errorf("toggle = %s", toggle(html))
		}
	})

	t.Run("workers follow their parent out of a search", func(t *testing.T) {
		if got, want := rows(sidebar(t, url.Values{"no_session": {"1"}, "session_search": {"done"}}, true)), []string{"pinned-parent", "  pinned-done"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("rows = %q, want %q", got, want)
		}
	})

	t.Run("an open hidden worker is the current session", func(t *testing.T) {
		html := sidebar(t, url.Values{"session": {done}}, false)
		_, current, _ := strings.Cut(html, `class="current-session-section"`)
		current, _, _ = strings.Cut(current, `<div class="sessions-list">`)
		if got := rows(current); len(got) != 1 || got[0] != "done" {
			t.Fatalf("current session rows = %q", got)
		}
	})
}
