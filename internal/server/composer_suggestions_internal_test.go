package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestComposerPathSuggestionsUseSessionCWD(t *testing.T) {
	for _, state := range []string{"persisted", "pending", "unknown", "unowned pending"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			cwd := filepath.Join(root, "projects", "current")
			sessionsRoot := filepath.Join(root, "sessions")
			for _, directory := range []string{cwd, filepath.Join(root, "projects", "sibling"), sessionsRoot} {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(sessionsRoot, "session.jsonl")
			app := &application{
				config:          config.Config{Home: root, SessionsRoot: sessionsRoot},
				sessionCache:    sessions.NewCache(),
				pendingSessions: rpc.NewPendingSessionRegistry(nil),
				ownsSession:     func(*http.Request, string) bool { return state != "unowned pending" },
			}
			switch state {
			case "persisted":
				writeSessionRecords(t, path, []map[string]any{{"type": "session", "version": 3, "id": "session", "timestamp": "2026-01-01T00:00:00Z", "cwd": cwd}})
			case "pending", "unowned pending":
				app.pendingSessions.Remember(path, cwd)
			}

			form := url.Values{"session": {path}, "mode": {"path"}, "query": {"../"}}
			request := httptest.NewRequest(http.MethodPost, "/composer/path_suggestions", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			app.composerPathSuggestions(response, request)
			if state == "unknown" || state == "unowned pending" {
				if response.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404: %s", response.Code, response.Body.String())
				}
				return
			}
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
			}
			var payload struct {
				Suggestions []sessions.Suggestion `json:"suggestions"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(payload.Suggestions, sessions.Suggestion{Path: "../sibling/", Directory: true}) {
				t.Fatalf("missing sibling directory: %+v", payload.Suggestions)
			}
			if state == "pending" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("autocomplete must not persist the pending session: %v", err)
				}
			}
		})
	}
}
