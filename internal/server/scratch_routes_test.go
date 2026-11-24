package server_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/access"
)

type scratchPayload struct {
	Session  string `json:"session"`
	Redirect string `json:"redirect"`
}

func startScratch(t *testing.T, handler http.Handler) scratchPayload {
	t.Helper()
	response := serveAction(handler, formActionRequest("/scratch/new", nil, true))
	var payload scratchPayload
	decodeActionJSON(t, response, &payload)
	if response.Code != http.StatusOK || payload.Session == "" {
		t.Fatalf("start scratch = %d %s", response.Code, response.Body.String())
	}
	return payload
}

// Files that the gateway or Pi wrote, by path, with their modification times. The gateway's Pi extension, which it
// installs as Pi first starts, and logs don't count.
func writtenFiles(t *testing.T, roots ...string) map[string]time.Time {
	t.Helper()
	files := make(map[string]time.Time)
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && !strings.HasSuffix(path, ".log") && !strings.Contains(path, "/pi-extensions/") {
				if info, err := entry.Info(); err == nil {
					files[path] = info.ModTime()
				}
			}
			return nil
		})
	}
	return files
}

func TestScratchSessionIsNeverListedOrSaved(t *testing.T) {
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	cfg, alpha, _, _ := fakePiConfig(t)
	cfg.RPCIdleTimeout, cfg.RPCIdleSweep = time.Millisecond, 5*time.Millisecond
	handler := startGateway(t, cfg)
	stateRoot := filepath.Dir(cfg.ReadStatePath)
	// The first page load writes what the gateway keeps about the existing sessions.
	serveAction(handler, getActionRequest("/?session="+url.QueryEscape(alpha)))
	before := writtenFiles(t, stateRoot, cfg.Home)

	scratch := startScratch(t, handler)
	if again := startScratch(t, handler); again.Session != scratch.Session {
		t.Fatalf("a second scratch session started: %s, then %s", scratch.Session, again.Session)
	}
	page := "/?" + url.Values{"session": {scratch.Session}, "session_only": {"1"}}.Encode()
	if scratch.Redirect != page {
		t.Fatalf("redirect = %s", scratch.Redirect)
	}
	shown := "/events?session=" + url.QueryEscape(scratch.Session)
	if page := serveAction(handler, getActionRequest(scratch.Redirect)); page.Code != http.StatusOK || !strings.Contains(page.Body.String(), shown) {
		t.Fatalf("scratch page = %d %s", page.Code, page.Body.String())
	}
	prompt := serveAction(handler, formActionRequest("/prompt", map[string]string{"session": scratch.Session, "message": "Hello"}, true))
	if prompt.Code != http.StatusOK {
		t.Fatalf("prompt = %d %s", prompt.Code, prompt.Body.String())
	}
	// Pi isn't stopped for being idle, which would lose the session.
	waitForFakePiSettled(t, handler, scratch.Session, 0)
	time.Sleep(50 * time.Millisecond)
	if prompt := serveAction(handler, formActionRequest("/prompt", map[string]string{"session": scratch.Session, "message": "Still there?"}, true)); prompt.Code != http.StatusOK {
		t.Fatalf("prompt after the idle timeout = %d %s", prompt.Code, prompt.Body.String())
	}

	for _, target := range []string{"/sidebar", "/sessions/palette", "/?session=" + url.QueryEscape(alpha), "/?session=" + url.QueryEscape(scratch.Session)} {
		if body := serveAction(handler, getActionRequest(target)).Body.String(); strings.Contains(body, url.QueryEscape(scratch.Session)) || strings.Contains(body, scratch.Session) {
			t.Fatalf("%s shows the scratch session: %s", target, body)
		}
	}
	local := localHandler(t, handler)
	for _, target := range []string{"/sessions", "/conversation?session=" + url.QueryEscape(scratch.Session)} {
		response := serveAction(local, getActionRequest(target))
		if strings.Contains(response.Body.String(), scratch.Session) || (strings.HasPrefix(target, "/conversation") && response.Code != http.StatusNotFound) {
			t.Fatalf("gripi command %s reached the scratch session: %d %s", target, response.Code, response.Body.String())
		}
	}

	after := writtenFiles(t, stateRoot, cfg.Home)
	for path, modified := range after {
		if previous, existed := before[path]; !existed || !previous.Equal(modified) {
			t.Fatalf("the scratch session wrote %s", path)
		}
	}
	roots, _ := filepath.Glob(filepath.Join(temporary, "gripi-scratch-*"))
	if folders := writtenFiles(t, roots...); len(roots) != 1 || len(folders) != 1 {
		t.Fatalf("scratch folders hold %v, want only the gateway's lock", folders)
	}

	discarded := serveAction(handler, formActionRequest("/scratch/discard", nil, true))
	if discarded.Code != http.StatusOK {
		t.Fatalf("discard = %d %s", discarded.Code, discarded.Body.String())
	}
	sessions, _ := filepath.Glob(filepath.Join(temporary, "gripi-scratch-*", "gateway-*", "session-*"))
	if len(sessions) != 0 {
		t.Fatalf("discard kept %v", sessions)
	}
	if page := serveAction(handler, getActionRequest(scratch.Redirect)); strings.Contains(page.Body.String(), shown) {
		t.Fatal("a discarded scratch session still shows")
	}
	if prompt := serveAction(handler, formActionRequest("/prompt", map[string]string{"session": scratch.Session, "message": "Again"}, true)); prompt.Code != http.StatusNotFound {
		t.Fatalf("prompt to a discarded scratch session = %d %s", prompt.Code, prompt.Body.String())
	}
	if next := startScratch(t, handler); next.Session == scratch.Session {
		t.Fatal("a new scratch session reused the discarded one's key")
	}
}

func TestScratchSessionsBelongToTheirWorkspace(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	cfg, _, _, _ := fakePiConfig(t)
	root := filepath.Dir(cfg.ReadStatePath)
	cfg.MultiUserMode = true
	cfg.WorkspaceSecretPath, cfg.WorkspaceAccessPath, cfg.WorkspaceOwnershipPath = filepath.Join(root, "secret"), filepath.Join(root, "workspace-access.json"), filepath.Join(root, "owners.json")
	handler := startGateway(t, cfg)
	workspaces := access.NewWorkspaceStore(cfg.WorkspaceAccessPath)
	scratch := make(map[string]scratchPayload)
	for _, workspace := range []string{"workspace-a", "workspace-b"} {
		if err := workspaces.ApproveWorkspace(workspace); err != nil {
			t.Fatal(err)
		}
		request := formActionRequest("/scratch/new", nil, true)
		request.Header.Set("Cookie", "gripi_workspace="+workspace)
		response := serveAction(handler, request)
		var payload scratchPayload
		decodeActionJSON(t, response, &payload)
		if payload.Session == "" {
			t.Fatalf("%s got no scratch session: %d %s", workspace, response.Code, response.Body.String())
		}
		scratch[workspace] = payload
	}
	if scratch["workspace-a"].Session == scratch["workspace-b"].Session {
		t.Fatal("two workspaces share a scratch session")
	}

	shown := "/events?session=" + url.QueryEscape(scratch["workspace-a"].Session)
	request := getActionRequest(scratch["workspace-a"].Redirect)
	request.Header.Set("Cookie", "gripi_workspace=workspace-b")
	if page := serveAction(handler, request); strings.Contains(page.Body.String(), shown) {
		t.Fatal("another workspace sees the scratch session")
	}
	prompt := formActionRequest("/prompt", map[string]string{"session": scratch["workspace-a"].Session, "message": "Hello"}, true)
	prompt.Header.Set("Cookie", "gripi_workspace=workspace-b")
	if response := serveAction(handler, prompt); response.Code != http.StatusNotFound {
		t.Fatalf("another workspace's prompt = %d %s", response.Code, response.Body.String())
	}
	own := getActionRequest(scratch["workspace-a"].Redirect)
	own.Header.Set("Cookie", "gripi_workspace=workspace-a")
	if page := serveAction(handler, own); !strings.Contains(page.Body.String(), shown) {
		t.Fatalf("its own workspace doesn't see the scratch session: %d", page.Code)
	}
}

// Pi keeps a scratch session only in memory, so it is gone once its Pi ends, such as after a crash.
func TestAScratchSessionEndsWithItsPi(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	cfg, _, _, _ := fakePiConfig(t)
	handler := startGateway(t, cfg)
	scratch := startScratch(t, handler)
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(cfg.ReadStatePath), "fake-pi.log"))
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(string(contents), "\n", 2)[0]), &started); err != nil || started.PID == 0 {
		t.Fatalf("fake Pi log %q: %v", contents, err)
	}
	if err := syscall.Kill(started.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}

	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		response := serveAction(handler, getActionRequest("/events?session="+url.QueryEscape(scratch.Session)))
		var events struct {
			Gone bool `json:"gone"`
		}
		decodeActionJSON(t, response, &events)
		if events.Gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events after Pi ended = %d %s", response.Code, response.Body.String())
		}
	}
	if prompt := serveAction(handler, formActionRequest("/prompt", map[string]string{"session": scratch.Session, "message": "Hello"}, true)); prompt.Code == http.StatusOK {
		t.Fatalf("a prompt started the ended scratch session again: %s", prompt.Body.String())
	}
	if next := startScratch(t, handler); next.Session == scratch.Session {
		t.Fatal("the ended scratch session was opened again")
	}
	if saved, _ := filepath.Glob(filepath.Join(cfg.SessionsRoot, "*", "*.jsonl")); len(saved) != 0 {
		t.Fatalf("Pi saved %v", saved)
	}
}
