package server_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/server"
)

type nativeFixture struct {
	root            string
	home            string
	sessionsRoot    string
	attachmentsRoot string
	configuredCWDs  string
	markerPath      string
	markerTitle     string
}

func TestReadOnlySessionRoutesUseNativeE2EFixtureAndPreservePiJSONL(t *testing.T) {
	fixture := seedNativeFixture(t)
	imageEntry := `{"type":"message","id":"image-entry","parentId":"21000005","timestamp":"2026-07-20T12:30:00.000Z","message":{"role":"user","content":[{"type":"image","data":"cG5n","mimeType":"image/png"}]}}` + "\n"
	file, err := os.OpenFile(fixture.markerPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString(imageEntry); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	handler := fixtureHandler(t, fixture)
	before := snapshotJSONL(t, fixture.sessionsRoot)

	page := serve(t, handler, http.MethodGet, "/?session="+url.QueryEscape(fixture.markerPath), "")
	if page.Code != http.StatusOK {
		t.Fatalf("page status = %d, body = %q", page.Code, page.Body.String())
	}
	for _, contract := range []string{
		fixture.markerTitle, "Contract fixture marker", "The external E2E target is disposable.",
		`class="session-sidebar"`, `id="conversation-scroll"`, `class="message message--user"`,
		`class="message message--assistant"`, `data-message-fingerprint="assistant:`,
		`data-events-url="/events?session=`, `data-status-url="/status?session=`, `src="data:image/png;base64,cG5n"`,
	} {
		if !strings.Contains(page.Body.String(), contract) {
			t.Errorf("page does not contain %q", contract)
		}
	}

	sidebar := serve(t, handler, http.MethodGet, "/sidebar?session="+url.QueryEscape(fixture.markerPath), "")
	if sidebar.Code != http.StatusOK || !strings.Contains(sidebar.Body.String(), `aria-current="page"`) {
		t.Fatalf("sidebar contract missing: status=%d", sidebar.Code)
	}
	modal := serve(t, handler, http.MethodGet, "/new_session_modal?session="+url.QueryEscape(fixture.markerPath), "")
	if modal.Code != http.StatusOK || !strings.Contains(modal.Body.String(), "new-session-cwd-form") {
		t.Fatalf("modal contract missing: status=%d", modal.Code)
	}

	fragment := serve(t, handler, http.MethodGet, "/session_fragment?session="+url.QueryEscape(fixture.markerPath), "")
	var payload map[string]any
	if fragment.Code != http.StatusOK || json.Unmarshal(fragment.Body.Bytes(), &payload) != nil {
		t.Fatalf("fragment = %d %q", fragment.Code, fragment.Body.String())
	}
	if payload["session"] != fixture.markerPath || !strings.Contains(payload["conversation_html"].(string), "Contract fixture marker") {
		t.Fatalf("fragment payload = %#v", payload)
	}

	status := serve(t, handler, http.MethodGet, "/status?session="+url.QueryEscape(fixture.markerPath), "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"model":"e2e/fixture-model"`) || !strings.Contains(status.Body.String(), `"thinking":"medium"`) {
		t.Fatalf("status = %d %q", status.Code, status.Body.String())
	}

	markdown := serve(t, handler, http.MethodPost, "/markdown", url.Values{"text": {"## Safe\n\n<script>alert(1)</script> [bad](javascript:alert(1))"}}.Encode())
	var renderedMarkdown map[string]string
	if markdown.Code != http.StatusOK || json.Unmarshal(markdown.Body.Bytes(), &renderedMarkdown) != nil || !strings.Contains(renderedMarkdown["html"], "<h2") {
		t.Fatalf("markdown = %d %q", markdown.Code, markdown.Body.String())
	}
	if strings.Contains(renderedMarkdown["html"], "<script>") || strings.Contains(strings.ToLower(renderedMarkdown["html"]), "javascript:") {
		t.Fatalf("unsafe markdown = %q", renderedMarkdown["html"])
	}

	suggestions := serve(t, handler, http.MethodPost, "/composer/path_suggestions", url.Values{"session": {fixture.markerPath}, "mode": {"path"}, "query": {""}}.Encode())
	if suggestions.Code != http.StatusOK || !strings.Contains(suggestions.Body.String(), `"suggestions"`) {
		t.Fatalf("suggestions = %d %q", suggestions.Code, suggestions.Body.String())
	}
	pinned := serve(t, handler, http.MethodPost, "/sessions/pin", url.Values{"session": {fixture.markerPath}, "pinned": {"true"}}.Encode())
	if pinned.Code != http.StatusOK {
		t.Fatalf("pin = %d %q", pinned.Code, pinned.Body.String())
	}
	pinnedSidebar := serve(t, handler, http.MethodGet, "/sidebar?session="+url.QueryEscape(fixture.markerPath), "")
	if !strings.Contains(pinnedSidebar.Body.String(), "pinned-sessions-section") {
		t.Fatal("pinned session was not rendered")
	}

	oldestPath := filepath.Join(fixture.sessionsRoot, "e2e", "idle-client.jsonl")
	retainedFragment := serve(t, handler, http.MethodGet, "/session_fragment?session="+url.QueryEscape(oldestPath)+"&sidebar_sessions_limit=40", "")
	var retainedPayload map[string]any
	if json.Unmarshal(retainedFragment.Body.Bytes(), &retainedPayload) != nil || !strings.Contains(retainedPayload["url"].(string), "sidebar_sessions_limit=40") {
		t.Fatalf("fragment did not retain pagination for an older session: %q", retainedFragment.Body.String())
	}

	if commands := serve(t, handler, http.MethodGet, "/commands?session="+url.QueryEscape(fixture.markerPath), ""); commands.Code != http.StatusNotFound {
		t.Fatalf("commands unexpectedly started Round 4 behavior: %d", commands.Code)
	}

	after := snapshotJSONL(t, fixture.sessionsRoot)
	if len(before) != len(after) {
		t.Fatalf("session file count changed: %d -> %d", len(before), len(after))
	}
	for path, contents := range before {
		if string(after[path]) != string(contents) {
			t.Fatalf("Pi JSONL changed at %s", path)
		}
	}
}

func TestMarkdownAcceptsBrowserMultipartFormData(t *testing.T) {
	fixture := seedNativeFixture(t)
	handler := fixtureHandler(t, fixture)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("text", "## Multipart browser markdown"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://app.test/markdown", &body)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	var payload map[string]string
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || !strings.Contains(payload["html"], "Multipart browser markdown</h2>") {
		t.Fatalf("multipart markdown = %d %q", response.Code, response.Body.String())
	}

	oversized := httptest.NewRequest(http.MethodPost, "http://app.test/markdown", strings.NewReader("bounded"))
	oversized.RemoteAddr = "127.0.0.1:1234"
	oversized.ContentLength = 65 << 20
	oversized.Header.Set("Content-Type", writer.FormDataContentType())
	oversizedResponse := httptest.NewRecorder()
	handler.ServeHTTP(oversizedResponse, oversized)
	if oversizedResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized multipart markdown = %d %q", oversizedResponse.Code, oversizedResponse.Body.String())
	}
}

func TestSessionDiscoveryAndReadsRejectSymlinksOutsideTheSessionsRoot(t *testing.T) {
	fixture := seedNativeFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	project := filepath.Join(fixture.root, "projects", "contract-project")
	contents := `{"type":"session","version":3,"id":"outside","timestamp":"2026-01-01T00:00:00Z","cwd":` + jsonString(project) + "}\n" + `{"type":"message","id":"secret","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"OUTSIDE SECRET"}]}}` + "\n"
	if err := os.WriteFile(outside, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(fixture.sessionsRoot, "e2e", "leak.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	handler := fixtureHandler(t, fixture)
	page := serve(t, handler, http.MethodGet, "/", "")
	if strings.Contains(page.Body.String(), "OUTSIDE SECRET") {
		t.Fatal("outside symlink was discovered")
	}
	older := serve(t, handler, http.MethodGet, "/conversation_older?session="+url.QueryEscape(link)+"&cursor=1", "")
	if strings.Contains(older.Body.String(), "OUTSIDE SECRET") {
		t.Fatal("outside symlink was read")
	}
}

func TestConversationPaginationAndAttachmentReadContract(t *testing.T) {
	fixture := seedNativeFixture(t)
	project := filepath.Join(fixture.root, "projects", "history-project")
	path := filepath.Join(fixture.sessionsRoot, "e2e", "bounded-history.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	entries := []string{`{"type":"session","version":3,"id":"history","timestamp":"2026-07-20T12:00:00Z","cwd":` + jsonString(project) + `}`}
	parent := ""
	for index := 1; index <= 180; index++ {
		id := "message-" + strconvItoa(index)
		parentJSON := "null"
		if parent != "" {
			parentJSON = jsonString(parent)
		}
		entries = append(entries, `{"type":"message","id":`+jsonString(id)+`,"parentId":`+parentJSON+`,"timestamp":"2026-07-20T12:00:01Z","message":{"role":"user","content":[{"type":"text","text":`+jsonString("Message "+strconvItoa(index))+`}]}}`)
		parent = id
	}
	if err := os.WriteFile(path, []byte(strings.Join(entries, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	handler := fixtureHandler(t, fixture)
	page := serve(t, handler, http.MethodGet, "/?session="+url.QueryEscape(path), "")
	if !strings.Contains(page.Body.String(), `data-older-message-count="30"`) || strings.Contains(page.Body.String(), ">Message 30<") || !strings.Contains(page.Body.String(), "Message 180") {
		t.Fatalf("initial bounded window contract missing")
	}
	older := serve(t, handler, http.MethodGet, "/conversation_older?session="+url.QueryEscape(path)+"&cursor=30", "")
	var payload struct {
		HTML     string `json:"html"`
		Next     int    `json:"next_cursor"`
		HasOlder bool   `json:"has_older_messages"`
	}
	if err := json.Unmarshal(older.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Next != 0 || payload.HasOlder || !strings.Contains(payload.HTML, "Message 1") || !strings.Contains(payload.HTML, "Message 30") {
		t.Fatalf("older payload = %#v", payload)
	}
	cursorCases := []struct {
		query    string
		next     int
		hasOlder bool
	}{
		{"cursor=-10", 0, false},
		{"cursor=999", 30, true},
		{"cursor=999999999999999999999999999999999", 30, true},
		{"cursor=30&after=-50", 30, false},
		{"cursor=30&after=999999999999999999999999999999999", 30, false},
		{"cursor=30&after=500", 30, false},
		{"cursor=500&after=-50", 150, true},
	}
	for _, test := range cursorCases {
		response := serve(t, handler, http.MethodGet, "/conversation_older?session="+url.QueryEscape(path)+"&"+test.query, "")
		var result struct {
			Next     int  `json:"next_cursor"`
			HasOlder bool `json:"has_older_messages"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Next != test.next || result.HasOlder != test.hasOlder {
			t.Errorf("%s: cursor payload = %#v", test.query, result)
		}
	}

	sum := sha256.Sum256([]byte(path))
	sessionHash := hex.EncodeToString(sum[:])
	fileName := strings.Repeat("a", 64) + ".png"
	attachmentDir := filepath.Join(fixture.attachmentsRoot, sessionHash)
	if err := os.MkdirAll(attachmentDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attachmentDir, fileName), []byte("png fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	attachment := serve(t, handler, http.MethodGet, "/attachments/"+sessionHash+"/"+fileName, "")
	if attachment.Code != http.StatusOK || attachment.Body.String() != "png fixture" {
		t.Fatalf("attachment = %d %q", attachment.Code, attachment.Body.String())
	}
	outside := serve(t, handler, http.MethodGet, "/attachments/"+strings.Repeat("b", 64)+"/"+fileName, "")
	if outside.Code != http.StatusNotFound {
		t.Fatalf("unowned attachment status = %d", outside.Code)
	}

	brokenPath := filepath.Join(fixture.sessionsRoot, "e2e", "broken-pagination.jsonl")
	brokenEntries := []string{
		`{"type":"session","version":3,"id":"broken","timestamp":"2026-07-20T12:00:00Z","cwd":` + jsonString(project) + `}`,
		`{"type":"message","id":"call","parentId":null,"timestamp":"2026-07-20T12:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"tool-1","name":"bash","arguments":{"command":"true"}}]}}`,
		`{"type":"message","id":"result","parentId":"call","timestamp":"2026-07-20T12:00:02Z","message":{"role":"toolResult","toolCallId":"tool-1","toolName":"read","content":[{"type":"text","text":"mismatch"}],"isError":false}}`,
	}
	if err := os.WriteFile(brokenPath, []byte(strings.Join(brokenEntries, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	failedWindow := serve(t, handler, http.MethodGet, "/conversation_older?session="+url.QueryEscape(brokenPath)+"&cursor=1", "")
	if failedWindow.Code != http.StatusServiceUnavailable || strings.Contains(failedWindow.Body.String(), `"has_older_messages":false`) {
		t.Fatalf("failed pagination falsely terminated: %d %q", failedWindow.Code, failedWindow.Body.String())
	}
}

func TestPersistedSubagentAndToolPresentationMatchesRubyAndLiveRendering(t *testing.T) {
	fixture := seedNativeFixture(t)
	project := filepath.Join(fixture.root, "projects", "subagent-project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.sessionsRoot, "e2e", "subagent-rendering.jsonl")
	lines := []string{
		`{"type":"session","version":3,"id":"subagent-rendering","timestamp":"2026-07-20T12:00:00Z","cwd":` + jsonString(project) + `}`,
		`{"type":"message","id":"read-call","parentId":null,"timestamp":"2026-07-20T12:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"read-1","name":"read","arguments":{"path":` + jsonString(filepath.Join(fixture.home, "src", "file.go")) + `,"offset":5,"limit":3}}]}}`,
		`{"type":"message","id":"read-result","parentId":"read-call","timestamp":"2026-07-20T12:00:02Z","message":{"role":"toolResult","toolCallId":"read-1","toolName":"read","content":[{"type":"text","text":"contents"}],"isError":false}}`,
		`{"type":"message","id":"subagent-result","parentId":"read-result","timestamp":"2026-07-20T12:00:03Z","message":{"role":"toolResult","toolCallId":"subagent-1","toolName":"subagent","content":[{"type":"text","text":"fallback"}],"details":{"status":"done","tools":[{"status":"done","name":"read","args":{"path":` + jsonString(filepath.Join(fixture.home, "review.go")) + `,"offset":5,"limit":3},"output":` + jsonString("checked "+fixture.home+"/review.go and /prefix"+fixture.home+"/kept.go") + `}],"textItems":["Review complete"],"usage":{"turns":2,"input":1200,"output":34,"cost":0.125,"contextTokens":2000},"model":"review-model"},"isError":false}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	page := serve(t, fixtureHandler(t, fixture), http.MethodGet, "/?session="+url.QueryEscape(path), "")
	for _, expected := range []string{
		`<span class="tool-command">read</span> <span class="tool-path">~/src/file.go</span><span class="tool-range">:5-7</span>`,
		"✓ read ~/review.go:5-7",
		"checked ~/review.go and /prefix" + fixture.home + "/kept.go",
		"2 turns ↑1.2k ↓34 $0.1250 ctx:2.0k review-model",
	} {
		if !strings.Contains(page.Body.String(), expected) {
			t.Errorf("SSR output does not contain %q: %s", expected, page.Body.String())
		}
	}
}

func seedNativeFixture(t *testing.T) nativeFixture {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node is required to seed the native E2E fixture")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "fixture")
	command := exec.Command("node", filepath.Join(repoRoot, "e2e", "fixtures", "seed.mjs"), root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("seed native fixture: %v\n%s", err, output)
	}
	markerPath := filepath.Join(root, "sessions", "e2e", "contract.jsonl")
	return nativeFixture{root: root, home: filepath.Join(root, "home"), sessionsRoot: filepath.Join(root, "sessions"), attachmentsRoot: filepath.Join(root, "attachments"), configuredCWDs: filepath.Join(root, "state", "configured-cwds"), markerPath: markerPath, markerTitle: "E2E Contract Ready"}
}

func fixtureHandler(t *testing.T, fixture nativeFixture) http.Handler {
	t.Helper()
	cfg := config.Config{Address: "127.0.0.1:4567", Environment: "test", Home: fixture.home, SessionsRoot: fixture.sessionsRoot, AttachmentsRoot: fixture.attachmentsRoot, SessionCwdsPath: fixture.configuredCWDs, ReadStatePath: filepath.Join(fixture.root, "state", "read.json"), PinnedSessionsPath: filepath.Join(fixture.root, "state", "pinned.json"), BrowserAccessPath: filepath.Join(fixture.root, "state", "browser.json"), BrowserAuthDisabled: true}
	handler, err := server.NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serve(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://app.test"+target, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1234"
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func snapshotJSONL(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	_ = filepath.WalkDir(root, func(path string, item fs.DirEntry, err error) error {
		if err == nil && !item.IsDir() && filepath.Ext(path) == ".jsonl" {
			result[path], err = os.ReadFile(path)
		}
		return err
	})
	return result
}
func jsonString(value string) string { data, _ := json.Marshal(value); return string(data) }
func strconvItoa(value int) string   { return fmt.Sprintf("%d", value) }
