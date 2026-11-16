package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	gateway "github.com/melounvitek/gripi/internal/server"
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

	t.Run("a worker shows while Pi CLI runs it", func(t *testing.T) {
		// Like the Pi CLI of a worker, the process does not descend from the gateway.
		output, err := exec.Command("sh", "-c", `sh -c 'while :; do sleep 1; done' pi --session-id older </dev/null >/dev/null 2>&1 & echo $!`).Output()
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })

		html := sidebar(t, url.Values{"no_session": {"1"}}, false)
		if got, want := rows(html), []string{"pinned-parent", "parent", "  continued", "  older", "beta", "alpha"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("rows = %q, want %q", got, want)
		}
		_, row, _ := strings.Cut(html, `data-session-path="`+older+`"`)
		row, _, _ = strings.Cut(row, `<div class="session-row`)
		if !strings.Contains(row, `data-busy="true"`) || !strings.Contains(row, "session-running-indicator") {
			t.Errorf("the running worker shows no activity: %s", row)
		}
		if !strings.Contains(toggle(html), `title="Show 2 Pi CLI sessions"`) {
			t.Errorf("the toggle counts the running worker: %s", toggle(html))
		}
	})
}

func TestSubSessionsNeverShowAsUnread(t *testing.T) {
	cfg, alpha, beta, project := fakePiConfig(t)
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
	cursor := fakePiEventCursor(t, handler, alpha)
	prompt(alpha)
	waitForFakePiSettled(t, handler, alpha, cursor)
	prompt(beta)
	waitForLocalSession(t, local, beta, func(session gateway.LocalSession) bool { return session.State == "idle" && session.Unread })
	unread(t, beta)
	sidebar := serveAction(handler, getActionRequest("/sidebar?no_session=1")).Body.String()
	for path, want := range map[string]bool{alpha: false, beta: true} {
		_, row, _ := strings.Cut(sidebar, `data-session-path="`+path+`"`)
		row, _, _ = strings.Cut(row, `<div class="session-row`)
		// The sidebar notifies of a finished reply when this changes.
		if got := !strings.Contains(row, `data-completed-reply-id=""`); got != want {
			t.Errorf("%s has a finished reply to notify of: %t, want %t", filepath.Base(path), got, want)
		}
	}

	// Linked once its reply waits to be read, as gripi link may do, the session no longer counts.
	link(beta)
	unread(t)
}
