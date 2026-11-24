package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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

// What needs a saved session, or keeps something about it, isn't offered for a scratch session.
func TestScratchSessionRefusesWhatNeedsASavedSession(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	cfg, _, _, _ := fakePiConfig(t)
	handler := startGateway(t, cfg)
	scratch := startScratch(t, handler)
	session := url.QueryEscape(scratch.Session)

	for _, target := range []string{"/sessions/fork_messages?session=" + session, "/sessions/tree_entries?session=" + session, "/sessions/tags?session=" + session} {
		if response := serveAction(handler, getActionRequest(target)); response.Code != http.StatusConflict {
			t.Errorf("GET %s = %d %s", target, response.Code, response.Body.String())
		}
	}
	for _, target := range []string{"/sessions/new", "/sessions/rename", "/sessions/delete", "/sessions/tree", "/sessions/tree/label", "/sessions/fork", "/sessions/clone", "/sessions/export", "/sessions/takeover", "/sessions/pin", "/sessions/tags", "/sessions/mark_read", "/workers/move", "/workers/return", "/workers/forward", "/workers/unforward"} {
		fields := map[string]string{"session": scratch.Session, "name": "Renamed", "entry_id": "x", "pinned": "true", "tags": "work", "worker": "box", "port": "3000"}
		if response := serveAction(handler, formActionRequest(target, fields, true)); response.Code != http.StatusConflict {
			t.Errorf("POST %s = %d %s", target, response.Code, response.Body.String())
		}
	}
	for _, message := range []string{"/name Renamed", "/fork", "/tree", "/new", "/clone"} {
		response := serveAction(handler, formActionRequest("/prompt", map[string]string{"session": scratch.Session, "message": message}, true))
		var result struct {
			Message string `json:"message"`
		}
		decodeActionJSON(t, response, &result)
		if response.Code != http.StatusOK || !strings.Contains(result.Message, "isn’t available in a scratch session") {
			t.Errorf("prompt %q = %d %+v", message, response.Code, result)
		}
	}
	commands := serveAction(handler, getActionRequest("/commands?session="+session)).Body.String()
	for _, name := range []string{"name", "export", "fork", "tree", "clone", "new"} {
		if strings.Contains(commands, `data-command-name="`+name+`"`) {
			t.Errorf("the scratch session's commands offer /%s", name)
		}
	}
	if !strings.Contains(commands, `data-command-name="compact"`) {
		t.Fatalf("commands = %s", commands)
	}
}

// Pi keeps a scratch session only in memory, so a page shows its conversation from Pi.
func TestScratchSessionShowsItsConversationAfterAReload(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	cfg, _, _, _ := fakePiConfig(t)
	handler := startGateway(t, cfg)
	scratch := startScratch(t, handler)
	if prompt := serveAction(handler, formActionRequest("/prompt", map[string]string{"session": scratch.Session, "message": "Remember scratchpad"}, true)); prompt.Code != http.StatusOK {
		t.Fatalf("prompt = %d %s", prompt.Code, prompt.Body.String())
	}
	waitForFakePiSettled(t, handler, scratch.Session, 0)

	page := serveAction(handler, getActionRequest(scratch.Redirect)).Body.String()
	if !strings.Contains(page, "Remember scratchpad") || !strings.Contains(page, "message--assistant") {
		t.Fatalf("scratch page after the reply = %s", page)
	}
	older := serveAction(handler, getActionRequest("/conversation_older?session="+url.QueryEscape(scratch.Session)+"&cursor=1"))
	var payload struct {
		HTML string `json:"html"`
	}
	decodeActionJSON(t, older, &payload)
	if older.Code != http.StatusOK || !strings.Contains(payload.HTML, "Remember scratchpad") || strings.Contains(payload.HTML, "message--assistant") {
		t.Fatalf("older messages = %d %s", older.Code, payload.HTML)
	}
}

// A scratch session's images stay in memory: in the prompt, in Pi, and on their way to the page.
func TestScratchSessionImagesNeverReachDisk(t *testing.T) {
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	cfg, _, _, _ := fakePiConfig(t)
	handler := startGateway(t, cfg)
	scratch := startScratch(t, handler)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 2<<20)...)

	request := multipartRequest(t, "/prompt", map[string]string{"session": scratch.Session, "message": "Look"}, "images", "image.png", "image/png", png)
	request.Header.Set("Accept", "application/json")
	prompt := serveAction(handler, request)
	if prompt.Code != http.StatusOK {
		t.Fatalf("image prompt = %d %s", prompt.Code, prompt.Body.String())
	}
	waitForFakePiSettled(t, handler, scratch.Session, 0)
	if attachments := writtenFiles(t, cfg.AttachmentsRoot); len(attachments) != 0 {
		t.Fatalf("the image was saved to %v", attachments)
	}
	if written, _ := filepath.Glob(filepath.Join(temporary, "multipart-*")); len(written) != 0 {
		t.Fatalf("the upload was written to %v", written)
	}

	page := serveAction(handler, getActionRequest(scratch.Redirect)).Body.String()
	source := regexp.MustCompile(`/session_images/[^"]+`).FindString(page)
	if source == "" {
		t.Fatalf("the page shows no image: %s", page)
	}
	image := serveAction(handler, getActionRequest(html.UnescapeString(source)))
	if image.Code != http.StatusOK || image.Header().Get("Content-Type") != "image/png" || !bytes.Equal(image.Body.Bytes(), png) {
		t.Fatalf("image = %d %s, %d bytes", image.Code, image.Header().Get("Content-Type"), image.Body.Len())
	}

	// Pi takes a prompt and repeats it as one line of its output, which Gripi reads up to 65 MiB.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("session", scratch.Session)
	for index := range 4 {
		part, _ := writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="images"; filename="%d.png"`, index)}, "Content-Type": {"image/png"}})
		_, _ = part.Write(bytes.Repeat([]byte{2}, 9<<20))
	}
	_ = writer.Close()
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4567/prompt", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if large := serveAction(handler, request); large.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("36 MiB of images = %d %s", large.Code, large.Body.String())
	}
}
