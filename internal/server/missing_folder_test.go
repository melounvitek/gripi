package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

func TestSessionWhoseFolderIsGoneIsReadOnlyAndNeverStartsPi(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	sessionsRoot := filepath.Join(home, ".pi", "agent", "sessions")
	if err := os.MkdirAll(sessionsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(home, "Work", "gone-project")
	sessionPath := filepath.Join(sessionsRoot, "gone.jsonl")
	writeActionSession(t, sessionPath, folder)
	started := filepath.Join(root, "pi-started")
	handler, err := gateway.NewHandler(config.Config{
		Address: "127.0.0.1:4567", Environment: "test", Home: home,
		SessionsRoot: sessionsRoot, AttachmentsRoot: filepath.Join(root, "attachments"),
		ReadStatePath: filepath.Join(root, "read.json"), PinnedSessionsPath: filepath.Join(root, "pinned.json"), SessionTagsPath: filepath.Join(root, "tags.json"),
		BrowserAccessPath: filepath.Join(root, "browser.json"), BrowserAuthDisabled: true,
		// Pi itself would exit at once; this one only records that it was asked to start.
		PiCommand: []string{"sh", "-c", `echo "$@" >> "$0"; exit 1`, started},
	}, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	view := "/?session=" + url.QueryEscape(sessionPath)

	page := serveAction(handler, getActionRequest(view)).Body.String()
	for _, expected := range []string{
		"Fixture answer",
		`<span class="session-folder-missing" title="Folder ~/Work/gone-project no longer exists">no folder</span>`,
		"<strong>This session’s folder no longer exists.</strong> <span>~/Work/gone-project was moved or deleted.",
		`placeholder="Sending is paused." disabled`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("page lacks %q:\n%s", expected, page)
		}
	}
	if strings.Contains(page, `data-new-session-project="`+folder+`"`) {
		t.Fatal("the new session picker offers the missing folder")
	}
	var events struct {
		SessionSync struct{ Mode string } `json:"session_sync"`
	}
	decodeActionJSON(t, serveAction(handler, getActionRequest("/events?session="+url.QueryEscape(sessionPath))), &events)
	if events.SessionSync.Mode != "folder_missing" {
		t.Fatalf("events sync mode = %q", events.SessionSync.Mode)
	}

	with := func(fields map[string]string) map[string]string {
		result := map[string]string{"session": sessionPath}
		for key, value := range fields {
			result[key] = value
		}
		return result
	}
	for _, request := range []*http.Request{
		formActionRequest("/prompt", with(map[string]string{"message": "Hello"}), true),
		formActionRequest("/prompt", with(map[string]string{"message": "!ls"}), true),
		formActionRequest("/prompt", with(map[string]string{"message": "/compact"}), true),
		formActionRequest("/prompt", with(map[string]string{"message": "/new"}), true),
		formActionRequest("/sessions/new", with(nil), true),
		formActionRequest("/sessions/clone", with(nil), true),
		formActionRequest("/sessions/fork", with(map[string]string{"entry_id": "user-1"}), true),
		formActionRequest("/sessions/tree", with(map[string]string{"entry_id": "user-1"}), true),
		formActionRequest("/sessions/tree/label", with(map[string]string{"entry_id": "user-1", "label": "start"}), true),
		formActionRequest("/sessions/rename", with(map[string]string{"name": "Renamed"}), true),
		formActionRequest("/sessions/model_settings", with(map[string]string{"provider": "e2e", "model": "fixture-model", "thinking": "high"}), true),
		formActionRequest("/sessions/cycle_thinking", with(nil), true),
		formActionRequest("/compact", with(nil), true),
		formActionRequest("/abort", with(nil), true),
		formActionRequest("/sessions/export", with(nil), true),
		getActionRequest("/sessions/model_settings?session=" + url.QueryEscape(sessionPath)),
		getActionRequest("/sessions/fork_messages?session=" + url.QueryEscape(sessionPath)),
		getActionRequest("/sessions/tree_entries?session=" + url.QueryEscape(sessionPath)),
		getActionRequest("/commands?session=" + url.QueryEscape(sessionPath)),
	} {
		response := serveAction(handler, request)
		var payload map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &payload)
		if response.Code != http.StatusConflict || payload["error"] != "This session’s folder no longer exists. Continue in another folder to keep working." {
			t.Fatalf("%s %s = %d %s", request.Method, request.URL, response.Code, response.Body.String())
		}
	}
	if _, err := os.Stat(started); !os.IsNotExist(err) {
		contents, _ := os.ReadFile(started)
		t.Fatalf("Pi was started: %s", contents)
	}

	// A folder that comes back, such as a remounted drive, makes the session usable again.
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if page := serveAction(handler, getActionRequest(view)).Body.String(); !strings.Contains(page, `data-session-sync-mode="available"`) || !strings.Contains(page, `placeholder="Ask Pi…"`) {
		t.Fatalf("session stayed read-only once its folder came back:\n%s", page)
	}
}
