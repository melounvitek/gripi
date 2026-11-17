package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	gateway "github.com/melounvitek/gripi/internal/server"
)

// sidebarRow finds the rows of a sidebar, and where the fold of sub-sessions starts, with what it says, and ends.
var sidebarRow = regexp.MustCompile(`<div class="session-row([^"]*)" data-session-tags="[^"]*" data-session-path="([^"]*)"|<details class="worker-fold"[^>]*><summary>.*?<span class="visually-hidden">([^<]+)</span>|</details>`)

// sidebarRows lists the rows of a sidebar in order: a sub-session indented under its parent, and the fold of its
// sub-sessions with those it holds.
func sidebarRows(html string) []string {
	var result []string
	indent := "  "
	for _, match := range sidebarRow.FindAllStringSubmatch(html, -1) {
		id := strings.TrimSuffix(filepath.Base(match[2]), ".jsonl")
		switch {
		case match[3] != "":
			result = append(result, "  ▸ "+match[3])
			indent = "    "
		case match[0] == "</details>":
			indent = "  "
		case strings.Contains(match[1], "is-worker"):
			result = append(result, indent+id)
		default:
			result = append(result, id)
		}
	}
	return result
}

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
		// Pi CLI ran these sub-sessions, and Gripi the others.
		"external-sessions.json": []string{done, pinnedDone},
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
	t.Run("sub-sessions fold under their parent, whoever ran them", func(t *testing.T) {
		for _, shown := range []bool{false, true} {
			html := sidebar(t, url.Values{"no_session": {"1"}}, shown)
			want := []string{"pinned-parent", "  ▸ 1 sub-session", "    pinned-done", "parent", "  ▸ 3 sub-sessions", "    continued", "    done", "    older", "beta", "alpha"}
			if got := sidebarRows(html); !reflect.DeepEqual(got, want) {
				t.Fatalf("with Pi CLI sessions shown %t, rows = %q, want %q", shown, got, want)
			}
			// Sub-sessions coming and going must not renumber the rows below them.
			if !strings.Contains(html, `data-session-shortcut="3"`) || strings.Count(html, "data-session-shortcut=") != 4 {
				t.Error("a sub-session took a shortcut")
			}
		}
	})

	t.Run("sub-sessions follow their parent out of a search", func(t *testing.T) {
		if got, want := sidebarRows(sidebar(t, url.Values{"no_session": {"1"}, "session_search": {"done"}}, false)), []string{"pinned-parent", "  ▸ 1 sub-session", "    pinned-done"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("rows = %q, want %q", got, want)
		}
	})

	t.Run("the open sub-session stays listed though it is done", func(t *testing.T) {
		want := []string{"pinned-parent", "  ▸ 1 sub-session", "    pinned-done", "parent", "  done", "  ▸ 2 sub-sessions", "    continued", "    older", "beta", "alpha"}
		if got := sidebarRows(sidebar(t, url.Values{"session": {done}}, false)); !reflect.DeepEqual(got, want) {
			t.Fatalf("rows = %q, want %q", got, want)
		}
	})

	t.Run("an open sub-session whose parent is not listed is the current session", func(t *testing.T) {
		html := sidebar(t, url.Values{"session": {done}, "session_search": {"done"}}, false)
		_, current, _ := strings.Cut(html, `class="current-session-section"`)
		current, _, _ = strings.Cut(current, `<div class="sessions-list">`)
		if got := sidebarRows(current); len(got) != 1 || got[0] != "done" {
			t.Fatalf("current session rows = %q", got)
		}
	})

	t.Run("running sub-sessions fold first", func(t *testing.T) {
		// It runs until someone answers its dialog.
		if response := postPrompt(handler, browserHost, url.Values{"session": {older}, "message": {"Ask me for release approval"}}); response.Code != http.StatusOK {
			t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
		}
		want := []string{"pinned-parent", "  ▸ 1 sub-session", "    pinned-done", "parent", "  ▸ 3 sub-sessions, 1 running", "    older", "    continued", "    done", "beta", "alpha"}
		var html string
		for deadline := time.Now().Add(5 * time.Second); !reflect.DeepEqual(sidebarRows(html), want); time.Sleep(25 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("rows = %q, want %q", sidebarRows(html), want)
			}
			html = sidebar(t, url.Values{"no_session": {"1"}}, false)
		}
		_, fold, _ := strings.Cut(html, `data-worker-fold="`+parent+`"`)
		if !strings.Contains(fold, `<span class="worker-dot is-running" title="Prompt for older"></span><span class="worker-dot is-done" title="Prompt for continued"></span>`) {
			t.Errorf("the dots don't show the running sub-session first: %s", fold)
		}
		_, row, _ := strings.Cut(fold, `data-session-path="`+older+`"`)
		row, _, _ = strings.Cut(row, `<div class="session-row`)
		if !strings.Contains(row, "session-running-indicator") {
			t.Errorf("the running sub-session shows no activity: %s", row)
		}
	})
}

func TestASubSessionShowsTheUsersTurnAsUnreadUntilOpened(t *testing.T) {
	cfg, alpha, _, project := fakePiConfig(t)
	parent := filepath.Join(cfg.SessionsRoot, "parent.jsonl")
	writeLocalSession(t, parent, "parent", project, "2026-01-03T00:00:0")
	handler := startGateway(t, cfg)
	local := localHandler(t, handler)
	link := func(path string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "http://"+socketHost+"/sessions/parent", strings.NewReader(url.Values{"session": {path}, "parent": {parent}}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if response := serveAction(local, request); response.Code != http.StatusOK {
			t.Fatalf("link = %d %s", response.Code, response.Body.String())
		}
	}
	prompt := func(path string) {
		t.Helper()
		if response := postPrompt(handler, browserHost, url.Values{"session": {path}, "message": {"Hello"}}); response.Code != http.StatusOK {
			t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
		}
	}
	// unread lists the unread sessions as each place that shows them does.
	unread := func(t *testing.T, want ...string) {
		t.Helper()
		var listed, palette []string
		for _, session := range localSessions(t, local, "") {
			if session.Unread {
				listed = append(listed, session.Path)
			}
		}
		response := serveAction(handler, getActionRequest("/sessions/palette"))
		var payload struct {
			Sessions []struct {
				Path   string
				Unread bool
			}
		}
		decodeActionJSON(t, response, &payload)
		for _, session := range payload.Sessions {
			if session.Unread {
				palette = append(palette, session.Path)
			}
		}
		sidebar := serveAction(handler, getActionRequest("/sidebar?no_session=1")).Body.String()
		count := fmt.Sprintf(`data-unread-session-count="%d"`, len(want))
		if !reflect.DeepEqual(listed, want) || !reflect.DeepEqual(palette, want) || !strings.Contains(sidebar, count) {
			t.Fatalf("unread: listed %q, palette %q, sidebar has %s: %t; want %q", listed, palette, count, strings.Contains(sidebar, count), want)
		}
	}

	link(alpha)
	prompt(alpha)
	waitForLocalSession(t, local, alpha, func(session gateway.LocalSession) bool { return session.State == "idle" && session.Unread })
	unread(t, alpha)
	// It folds under its parent as unread, and its row is in bold.
	sidebar := serveAction(handler, getActionRequest("/sidebar?no_session=1")).Body.String()
	if got, want := sidebarRows(sidebar), []string{"parent", "  ▸ 1 sub-session, 1 unread", "    alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
	if !strings.Contains(sidebar, `<span class="worker-dot is-unread"`) {
		t.Error("the unread sub-session's dot is not marked")
	}
	_, row, _ := strings.Cut(sidebar, `data-session-path="`+alpha+`"`)
	if row, _, _ = strings.Cut(row, "</a>"); !strings.Contains(row, `class="session recent-session unread"`) {
		t.Errorf("the unread sub-session is not highlighted: %s", row)
	}

	// Once opened, it is done.
	if response := serveAction(handler, getActionRequest("/?session="+url.QueryEscape(alpha))); response.Code != http.StatusOK {
		t.Fatalf("open = %d", response.Code)
	}
	unread(t)
	sidebar = serveAction(handler, getActionRequest("/sidebar?no_session=1")).Body.String()
	if got, want := sidebarRows(sidebar), []string{"parent", "  ▸ 1 sub-session", "    alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
}
