package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/access"
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

func TestContinuingInAnotherFolderForksTheSessionThere(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node is required for the fake Pi")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	sessionsRoot := filepath.Join(home, ".pi", "agent", "sessions")
	gone, present, target := filepath.Join(home, "Work", "gone"), filepath.Join(home, "Work", "present"), filepath.Join(home, "Work", "target")
	for _, directory := range []string{sessionsRoot, present, target} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	source, kept := filepath.Join(sessionsRoot, "gone.jsonl"), filepath.Join(sessionsRoot, "present.jsonl")
	writeActionSession(t, source, gone)
	writeActionSession(t, kept, present)
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	fakeLog := filepath.Join(root, "fake-pi.log")
	t.Setenv("GRIPI_E2E_SESSIONS_ROOT", sessionsRoot)
	t.Setenv("GRIPI_E2E_FAKE_PI_LOG", fakeLog)
	handler, err := gateway.NewHandler(config.Config{
		Address: "127.0.0.1:4567", Environment: "test", Home: home,
		SessionsRoot: sessionsRoot, AttachmentsRoot: filepath.Join(root, "attachments"),
		ReadStatePath: filepath.Join(root, "read.json"), PinnedSessionsPath: filepath.Join(root, "pinned.json"), SessionTagsPath: filepath.Join(root, "tags.json"),
		BrowserAccessPath: filepath.Join(root, "browser.json"), BrowserAuthDisabled: true,
		PiCommand: []string{"node", filepath.Join(filepath.Dir(file), "..", "..", "e2e", "support", "fake_pi.mjs")},
	}, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	continueIn := func(cwd, from string) *httptest.ResponseRecorder {
		return serveAction(handler, formActionRequest("/sessions/new_at_cwd", map[string]string{"cwd": cwd, "fork_from": from}, true))
	}

	for _, refused := range []struct {
		cwd, from string
		code      int
	}{
		{target, kept, http.StatusConflict},
		{target, filepath.Join(sessionsRoot, "unknown.jsonl"), http.StatusNotFound},
		{gone, source, http.StatusUnprocessableEntity},
	} {
		if response := continueIn(refused.cwd, refused.from); response.Code != refused.code {
			t.Fatalf("continuing %s in %s = %d %s", refused.from, refused.cwd, response.Code, response.Body.String())
		}
	}
	if _, err := os.Stat(fakeLog); !os.IsNotExist(err) {
		t.Fatal("a refused request started Pi")
	}

	response := continueIn(target, source)
	var payload struct{ Session string }
	decodeActionJSON(t, response, &payload)
	if response.Code != http.StatusOK || payload.Session == "" || payload.Session == source {
		t.Fatalf("continue = %d %s", response.Code, response.Body.String())
	}
	forked, err := os.ReadFile(payload.Session)
	if err != nil {
		t.Fatal(err)
	}
	var header struct{ CWD, ParentSession string }
	if err := json.Unmarshal(forked[:strings.IndexByte(string(forked), '\n')], &header); err != nil || header.CWD != target || header.ParentSession != source || !strings.Contains(string(forked), "Fixture answer") {
		t.Fatalf("forked session = %s", forked)
	}
	if after, err := os.ReadFile(source); err != nil || string(after) != string(original) {
		t.Fatalf("the original session changed: %s", after)
	}
	started, err := os.ReadFile(fakeLog)
	if err != nil || !strings.Contains(string(started), `"event":"started","sessionPath":`+jsonString(payload.Session)+`,"cwd":`+jsonString(target)) {
		t.Fatalf("fake Pi log = %s", started)
	}
	page := serveAction(handler, getActionRequest("/?session="+url.QueryEscape(payload.Session))).Body.String()
	if !strings.Contains(page, "Fixture answer") || !strings.Contains(page, `placeholder="Ask Pi…"`) {
		t.Fatalf("forked session page:\n%s", page)
	}
}

func TestOnlyTheOwnerContinuesASessionInAnotherFolder(t *testing.T) {
	root := t.TempDir()
	cfg := multiUserConfig(root)
	started := filepath.Join(root, "pi-started")
	cfg.PiCommand = []string{"sh", "-c", `echo "$@" >> "$0"; exit 1`, started}
	if err := os.MkdirAll(cfg.SessionsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(cfg.SessionsRoot, "gone.jsonl")
	writeActionSession(t, source, filepath.Join(root, "gone"))
	handler := multiUserHandler(t, cfg)
	if err := access.NewWorkspaceStore(cfg.WorkspaceAccessPath).ApproveWorkspace("workspace-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := access.NewWorkspaceOwnershipStore(cfg.WorkspaceOwnershipPath, cfg.SessionsRoot).Claim(source, "workspace-b"); err != nil {
		t.Fatal(err)
	}

	response := postWorkspaceForm(handler, "/sessions/new_at_cwd", url.Values{"cwd": {root}, "fork_from": {source}}, "gripi_workspace=workspace-a")
	if response.Code != http.StatusNotFound {
		t.Fatalf("continue another workspace's session = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(started); !os.IsNotExist(err) {
		t.Fatal("Pi was started")
	}
}
