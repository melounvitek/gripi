package server_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

// hermesConfig configures a gateway with the fake Pi and the fake Hermes, which has saved the given sessions, each a
// map in the fake's state format.
func hermesConfig(t *testing.T, sessions ...map[string]any) config.Config {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "hermes-state.json")
	encoded, err := json.Marshal(map[string]any{"sessions": sessions})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HERMES_STATE", state)
	_, file, _, _ := runtime.Caller(0)
	support := filepath.Join(filepath.Dir(file), "..", "..", "e2e", "support")
	return config.Config{
		Address: "127.0.0.1:4567", Environment: "test", Home: root,
		SessionsRoot: filepath.Join(root, "sessions"), AttachmentsRoot: filepath.Join(root, "attachments"),
		ReadStatePath: filepath.Join(root, "read.json"), PinnedSessionsPath: filepath.Join(root, "pinned.json"), SessionTagsPath: filepath.Join(root, "tags.json"),
		BrowserAccessPath: filepath.Join(root, "browser.json"), BrowserAuthDisabled: true, PermittedHosts: []string{"example.com"},
		EnvironmentPath: filepath.Join(root, "environment.json"), RPCIdleTimeout: time.Hour,
		PiCommand: []string{"node", filepath.Join(support, "fake_pi.mjs")}, HermesPath: filepath.Join(support, "fake_hermes.mjs"),
	}
}

// hermesHandler runs the gateway with the fake Hermes, which has saved the given sessions.
func hermesHandler(t *testing.T, sessions ...map[string]any) http.Handler {
	t.Helper()
	handler, err := gateway.NewHandlerWithHermes(hermesConfig(t, sessions...), gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	return handler
}

func getPage(t *testing.T, handler http.Handler, target string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	body, _ := io.ReadAll(recorder.Result().Body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, recorder.Code, body)
	}
	return string(body)
}

func textUpdate(kind, text string) map[string]any {
	return map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text}}
}

func toolUpdate(kind, id string, fields map[string]any) map[string]any {
	update := map[string]any{"sessionUpdate": kind, "toolCallId": id}
	for key, value := range fields {
		update[key] = value
	}
	return update
}

func toolText(text string) []any {
	return []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}}
}

func TestAHermesSessionShowsItsSavedConversation(t *testing.T) {
	project := t.TempDir()
	handler := hermesHandler(t, map[string]any{
		"sessionId": "hermes-tests", "cwd": project, "title": "Run the test suite", "updatedAt": "2026-03-01T10:00:00Z",
		"model": "anthropic:claude-sonnet-4-5", "contextUsed": 41000,
		"history": []any{
			textUpdate("user_message_chunk", "Run the tests and fix what fails"),
			textUpdate("agent_thought_chunk", "I should run the whole suite first."),
			toolUpdate("tool_call", "tc-1", map[string]any{"title": "terminal: npm test", "kind": "execute", "content": toolText("$ npm test")}),
			toolUpdate("tool_call_update", "tc-1", map[string]any{"kind": "execute", "status": "failed", "content": toolText("1 failing: totals rounds once")}),
			toolUpdate("tool_call", "tc-2", map[string]any{"title": "read_file: src/totals.ts", "kind": "read"}),
			toolUpdate("tool_call_update", "tc-2", map[string]any{"kind": "read", "status": "completed", "content": toolText("export function total() {}")}),
			textUpdate("agent_message_chunk", "Fixed **the rounding** in `total`."),
		},
	})

	page := getPage(t, handler, panelPage("hermes:hermes-tests"))
	for _, expected := range []string{
		"Run the tests and fix what fails",
		"I should run the whole suite first.",
		"terminal: npm test",
		"1 failing: totals rounds once",
		"read_file: src/totals.ts",
		"<strong>the rounding</strong>",
		"anthropic/claude-sonnet-4-5",
	} {
		if !strings.Contains(page, expected) {
			t.Errorf("page lacks %q", expected)
		}
	}
	if !strings.Contains(page, `data-tool-call-id="tc-1"`) {
		t.Errorf("page lacks the tool call's block")
	}
}

// newHermesConversation starts a new Hermes conversation, as the Hermes panel's new button does, and returns its key.
func newHermesConversation(t *testing.T, handler http.Handler) string {
	t.Helper()
	code, payload := postJSON(t, handler, "/hermes/new", nil)
	key, _ := payload["session"].(string)
	if code != http.StatusOK || !strings.HasPrefix(key, "hermes:") {
		t.Fatalf("new Hermes conversation = %d %v", code, payload)
	}
	return key
}

// panelPage is the page of a Hermes conversation that the Hermes panel shows.
func panelPage(key string) string {
	return "/?" + url.Values{"session": {key}, "session_only": {"1"}}.Encode()
}

func postJSON(t *testing.T, handler http.Handler, target string, values url.Values) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Origin", "http://example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var payload map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	return recorder.Code, payload
}

type liveEvent struct {
	Type                  string   `json:"type"`
	ID                    string   `json:"id"`
	Method                string   `json:"method"`
	Title                 string   `json:"title"`
	Options               []string `json:"options"`
	ToolCallID            string   `json:"toolCallId"`
	ToolName              string   `json:"toolName"`
	IsError               bool     `json:"isError"`
	Message               any      `json:"message"`
	Messages              []any    `json:"messages"`
	AssistantMessageEvent struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
}

// repliedText joins the thinking and the text of the replies that ended among the events.
func repliedText(events []liveEvent) (thinking, text string) {
	for _, event := range events {
		message, _ := event.Message.(map[string]any)
		if event.Type != "message_end" || message["role"] != "assistant" {
			continue
		}
		for _, part := range message["content"].([]any) {
			part := part.(map[string]any)
			thought, _ := part["thinking"].(string)
			said, _ := part["text"].(string)
			thinking += thought
			text += said
		}
	}
	return thinking, text
}

// eventsUntil polls the session's live events from the start until one of the given type arrives.
func eventsUntil(t *testing.T, handler http.Handler, key, last string) []liveEvent {
	t.Helper()
	events, _ := eventsAfterUntil(t, handler, key, 0, last)
	return events
}

// eventsAfterUntil polls the session's live events after a cursor until one of the given type arrives, and returns
// them with the cursor after them.
func eventsAfterUntil(t *testing.T, handler http.Handler, key string, after int64, last string) ([]liveEvent, int64) {
	t.Helper()
	var events []liveEvent
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var batch struct {
			Events  []liveEvent `json:"events"`
			LastSeq int64       `json:"last_seq"`
		}
		if err := json.Unmarshal([]byte(getPage(t, handler, fmt.Sprintf("/events?after=%d&session=%s", after, url.QueryEscape(key)))), &batch); err != nil {
			t.Fatal(err)
		}
		events = batch.Events
		for _, event := range events {
			if event.Type == last {
				return events, batch.LastSeq
			}
		}
	}
	t.Fatalf("no %s event among %+v", last, events)
	return nil, 0
}

func TestAHermesSessionRunsAPromptLive(t *testing.T) {
	handler := hermesHandler(t)

	key := newHermesConversation(t, handler)
	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files"}}); code != http.StatusOK {
		t.Fatalf("prompt = %d %v", code, payload)
	}

	events := eventsUntil(t, handler, key, "agent_settled")
	var toolEnd *liveEvent
	types := []string{}
	for index, event := range events {
		types = append(types, event.Type)
		if event.Type == "tool_execution_end" {
			toolEnd = &events[index]
		}
	}
	if thinking, text := repliedText(events); types[0] != "agent_start" || thinking != "Planning the answer." || text != "Checking the files.Hermes says: List the files" {
		t.Fatalf("events %v streamed thinking %q and text %q", types, thinking, text)
	}
	if toolEnd == nil || toolEnd.ToolCallID != "tc-1" || toolEnd.ToolName != "terminal: ls" || toolEnd.IsError {
		t.Fatalf("tool end = %+v", toolEnd)
	}

	// After the run, the page shows the same conversation from the saved session.
	page := getPage(t, handler, panelPage(key))
	for _, expected := range []string{"List the files", "Planning the answer.", "Checking the files.", "terminal: ls", "README.md", "Hermes says: List the files"} {
		if !strings.Contains(page, expected) {
			t.Errorf("page after the run lacks %q", expected)
		}
	}
}

func TestImagesReachHermesAndStayInTheConversation(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hermes.log")
	t.Setenv("FAKE_HERMES_LOG", log)
	handler := hermesHandler(t)
	key := newHermesConversation(t, handler)
	request := multipartRequest(t, "/prompt", map[string]string{"session": key, "message": "What does this show?"}, "images[]", "shot.png", "image/png", []byte("fixture-image"))
	request.Header.Set("Accept", "application/json")
	if response := serveAction(handler, request); response.Code != http.StatusOK {
		t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
	}

	image := map[string]any{"type": "image", "mimeType": "image/png", "data": "Zml4dHVyZS1pbWFnZQ=="}
	var shown []any
	for _, event := range eventsUntil(t, handler, key, "agent_settled") {
		if message, _ := event.Message.(map[string]any); event.Type == "message_end" && message["role"] == "user" {
			shown = message["content"].([]any)
		}
	}
	if len(shown) != 2 || fmt.Sprint(shown[1]) != fmt.Sprint(image) {
		t.Fatalf("the live message shows %v", shown)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var sent []any
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		var record struct {
			Method string `json:"method"`
			Params struct {
				Prompt []any `json:"prompt"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(line), &record) == nil && record.Method == "session/prompt" {
			sent = record.Params.Prompt
		}
	}
	if len(sent) != 2 || fmt.Sprint(sent[1]) != fmt.Sprint(image) {
		t.Fatalf("Hermes got the prompt %v", sent)
	}
	page := getPage(t, handler, panelPage(key))
	attachment := regexp.MustCompile(`src="(/attachments/[^"]+)"`).FindStringSubmatch(page)
	if attachment == nil || !strings.Contains(page, `alt="Attached image"`) {
		t.Fatal("the page after the run lacks the image")
	}
	// The page around the panel, which lists no Hermes conversation, doesn't keep the panel from showing the image.
	getPage(t, handler, "/")
	if response := serveRequest(handler, http.MethodGet, html.UnescapeString(attachment[1])); response.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", attachment[1], response.Code)
	}
}

// sharedFiles writes a PNG image and a text file named like one for Hermes to share, and returns their paths and the
// image's contents.
func sharedFiles(t *testing.T) (image, notImage string, contents []byte) {
	t.Helper()
	folder := t.TempDir()
	image, notImage = filepath.Join(folder, "page shot.png"), filepath.Join(folder, "notes.png")
	contents = []byte("\x89PNG\r\n\x1a\nfixture-image")
	if err := os.WriteFile(image, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notImage, []byte("plain notes"), 0600); err != nil {
		t.Fatal(err)
	}
	return image, notImage, contents
}

// shownImages fetches the images that a page shows from the session's files.
func shownImages(t *testing.T, handler http.Handler, page string) [][]byte {
	t.Helper()
	var images [][]byte
	for _, match := range regexp.MustCompile(`src="(/session_images/[^"]+)"`).FindAllStringSubmatch(page, -1) {
		source := html.UnescapeString(match[1])
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, source, nil))
		if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("GET %s = %d %s", source, recorder.Code, recorder.Header().Get("Content-Type"))
		}
		images = append(images, recorder.Body.Bytes())
		// The gateway serves only the files that the session's replies share.
		other := strings.Replace(source, "/0.png", "/1.png", 1)
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, other, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d", other, recorder.Code)
		}
	}
	return images
}

func TestAnImageHermesSharesShowsInPlaceOfItsTag(t *testing.T) {
	image, notImage, contents := sharedFiles(t)
	tag := `MEDIA:"` + image + `"`
	// Nothing writes to the pipe, which would hold a read of it forever.
	pipe := filepath.Join(t.TempDir(), "pipe.png")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	reply := func(id, text string) map[string]any {
		update := textUpdate("agent_message_chunk", text)
		update["messageId"] = id
		return update
	}
	handler := hermesHandler(t, map[string]any{
		"sessionId": "hermes-shot", "cwd": t.TempDir(), "title": "Screenshot the page", "updatedAt": "2026-03-01T10:00:00Z",
		"history": []any{
			textUpdate("user_message_chunk", "Screenshot the page"),
			reply("reply-1", "```shot```\n\nHere it is: `"+tag+"`\n\nYou can share one too:\n````\n```\n"+tag+"\n```\n````\n\nMy notes: MEDIA:"+notImage+"\n\nA pipe: MEDIA:"+pipe),
			reply("reply-2", "```"+tag+"```"),
		},
	})

	page := html.UnescapeString(getPage(t, handler, panelPage("hermes:hermes-shot")))
	if !strings.Contains(page, "<p>Here it is:</p>") {
		t.Error("the page doesn't show the image in place of its tag")
	}
	// Like Hermes, the gateway leaves an example in code, and the tag of a file that the browser can't show as an image.
	if !strings.Contains(page, "<code>```\n"+tag) || !strings.Contains(page, "MEDIA:"+notImage) || !strings.Contains(page, "MEDIA:"+pipe) {
		t.Error("the page lacks the tags that share no image")
	}
	// A reply of only an image keeps its tag as its text.
	if article := page[strings.Index(page, `data-message-key="2:0"`):]; !strings.Contains(article[:strings.Index(article, "</article>")], tag) {
		t.Error("the reply of only an image lacks its tag")
	}
	if images := shownImages(t, handler, page); len(images) != 2 || !bytes.Equal(images[0], contents) || !bytes.Equal(images[1], contents) {
		t.Fatalf("the page shows the images %q", images)
	}
}

func TestAnImageHermesSharesShowsLiveAndAfterTheRun(t *testing.T) {
	image, _, contents := sharedFiles(t)
	handler := hermesHandler(t)
	key := newHermesConversation(t, handler)
	// The fake Hermes repeats the prompt in its reply.
	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {`Look: **MEDIA:"` + image + `"**`}}); code != http.StatusOK {
		t.Fatalf("prompt = %d %v", code, payload)
	}

	var reply []any
	for _, event := range eventsUntil(t, handler, key, "agent_settled") {
		if message, _ := event.Message.(map[string]any); event.Type == "message_end" && message["role"] == "assistant" {
			reply = message["content"].([]any)
		}
	}
	shared := map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(contents)}
	if len(reply) != 2 || fmt.Sprint(reply[0]) != fmt.Sprint(map[string]any{"type": "text", "text": "Hermes says: Look: "}) || fmt.Sprint(reply[1]) != fmt.Sprint(shared) {
		t.Fatalf("the live reply is %v", reply)
	}
	if images := shownImages(t, handler, getPage(t, handler, panelPage(key))); len(images) != 1 || !bytes.Equal(images[0], contents) {
		t.Fatalf("the page after the run shows the images %q", images)
	}
}

func TestHermesRefusesAMessageTooLargeForIt(t *testing.T) {
	handler := hermesHandler(t)
	key := newHermesConversation(t, handler)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("session", key)
	_ = writer.WriteField("message", "Compare these")
	// Four images of the most an upload takes are more than Hermes reads once encoded.
	for index := range 4 {
		part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="images[]"; filename="%d.png"`, index)}, "Content-Type": {"image/png"}})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(bytes.Repeat([]byte{'x'}, 10<<20))
	}
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4567/prompt", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Accept", "application/json")

	response := serveAction(handler, request)
	if response.Code == http.StatusOK || !strings.Contains(response.Body.String(), "too large for Hermes") {
		t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
	}
}

func TestStopEndsAHermesRunAndRefusesMessagesWhileItRuns(t *testing.T) {
	handler := hermesHandler(t)
	key := newHermesConversation(t, handler)
	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Wait for stop"}}); code != http.StatusOK {
		t.Fatalf("prompt = %d %v", code, payload)
	}
	eventsUntil(t, handler, key, "agent_start")

	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Also do this"}, "streaming_behavior": {"steer"}}); code == http.StatusOK || !strings.Contains(payload["error"].(string), "Hermes is working") {
		t.Fatalf("steer while Hermes runs = %d %v", code, payload)
	}
	if code, payload := postJSON(t, handler, "/abort", url.Values{"session": {key}}); code != http.StatusOK {
		t.Fatalf("abort = %d %v", code, payload)
	}
	eventsUntil(t, handler, key, "agent_settled")

	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files"}}); code != http.StatusOK {
		t.Fatalf("prompt after stop = %d %v", code, payload)
	}
}

func TestHermesAsksBeforeADangerousCommandInADialog(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hermes.log")
	t.Setenv("FAKE_HERMES_LOG", log)
	handler := hermesHandler(t)
	key := newHermesConversation(t, handler)
	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Delete the build folder"}}); code != http.StatusOK {
		t.Fatalf("prompt = %d %v", code, payload)
	}

	var question liveEvent
	for _, event := range eventsUntil(t, handler, key, "extension_ui_request") {
		if event.Type == "extension_ui_request" {
			question = event
		}
	}
	message, _ := question.Message.(string)
	if question.Method != "select" || question.Title != "recursive delete: rm -rf build" || !strings.Contains(message, "$ rm -rf build") || strings.Join(question.Options, ", ") != "Allow once, Allow for session, Deny" {
		t.Fatalf("question = %+v", question)
	}
	// A reload brings the question back.
	if page := getPage(t, handler, panelPage(key)); !strings.Contains(page, question.ID) {
		t.Fatalf("the page lacks the question %s", question.ID)
	}

	if code, payload := postJSON(t, handler, "/extension_ui_response", url.Values{"session": {key}, "id": {question.ID}, "value": {"Allow once"}}); code != http.StatusOK {
		t.Fatalf("answer = %d %v", code, payload)
	}
	var toolEnd *liveEvent
	events := eventsUntil(t, handler, key, "agent_settled")
	for index, event := range events {
		if event.Type == "tool_execution_end" {
			toolEnd = &events[index]
		}
	}
	if toolEnd == nil || toolEnd.ToolName != "terminal: rm -rf build" || toolEnd.IsError {
		t.Fatalf("tool end = %+v", toolEnd)
	}
	// Hermes asks about sensitive files only, like its CLI, which never asks before an edit.
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"method":"session/set_mode","params":{"modeId":"dont_ask"`) {
		t.Fatalf("Gripi did not set Hermes's edit approval mode:\n%s", contents)
	}
}

// hermesStarts reads the fake Hermes's log for how each of its processes started.
func hermesStarts(t *testing.T, log string) []map[string]any {
	t.Helper()
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var starts []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["argv"] != nil {
			starts = append(starts, record)
		}
	}
	return starts
}

func TestSavedVariablesReachHermesBeforeItsNextMessage(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hermes.log")
	t.Setenv("FAKE_HERMES_LOG", log)
	handler := hermesHandler(t)
	key := newHermesConversation(t, handler)
	// A session that Hermes hasn't saved lives only in the running Hermes, so its first message runs there.
	if code, payload := postJSON(t, handler, "/environment/variable", url.Values{"name": {"FAKE_HERMES_PROBE"}, "value": {"saved in Gripi"}}); code != http.StatusOK {
		t.Fatalf("save variable = %d %v", code, payload)
	}
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files"}})
	_, cursor := eventsAfterUntil(t, handler, key, 0, "agent_settled")
	if starts := hermesStarts(t, log); len(starts) != 1 {
		t.Fatalf("Hermes restarted with a session it hadn't saved: %v", starts)
	}

	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files again"}}); code != http.StatusOK {
		t.Fatalf("prompt after saving a variable = %d %v", code, payload)
	}
	events, _ := eventsAfterUntil(t, handler, key, cursor, "agent_settled")

	starts := hermesStarts(t, log)
	if len(starts) != 2 || starts[0]["probe"] != nil || starts[1]["probe"] != "saved in Gripi" {
		t.Fatalf("Hermes started %v", starts)
	}
	// The browser's cursor from before the restart still reads the new run.
	if events[0].Type != "agent_start" {
		t.Fatalf("events after the restart = %+v", events)
	}
	// The session reloaded in the new Hermes keeps its conversation.
	if page := getPage(t, handler, panelPage(key)); !strings.Contains(page, "Hermes says: List the files again") || !strings.Contains(page, "Checking the files.") {
		t.Fatal("the conversation is missing after the restart")
	}
}

// serveRequest serves a request from Gripi's own pages.
func serveRequest(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	request.Header.Set("Origin", "http://example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// openedConversation is the Hermes conversation that a redirect to the Hermes panel's page opens.
func openedConversation(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	location, err := url.Parse(response.Header().Get("Location"))
	if response.Code != http.StatusSeeOther || err != nil || location.Path != "/" || location.Query().Get("session_only") != "1" {
		t.Fatalf("response = %d to %q", response.Code, response.Header().Get("Location"))
	}
	return location.Query().Get("session")
}

func currentConversation(t *testing.T, handler http.Handler) string {
	t.Helper()
	var current struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal([]byte(getPage(t, handler, "/hermes/current")), &current); err != nil {
		t.Fatal(err)
	}
	return current.Session
}

func TestEveryHermesPanelShowsOneConversationUntilANewOneReplacesIt(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hermes.log")
	t.Setenv("FAKE_HERMES_LOG", log)
	cfg := hermesConfig(t)
	handler, err := gateway.NewHandlerWithHermes(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })

	if current := currentConversation(t, handler); current != "" {
		t.Fatalf("a new gateway has the Hermes conversation %q", current)
	}
	first := openedConversation(t, serveRequest(handler, http.MethodGet, "/hermes"))
	if !strings.HasPrefix(first, "hermes:") {
		t.Fatalf("the panel opened %q", first)
	}
	if again := openedConversation(t, serveRequest(handler, http.MethodGet, "/hermes")); again != first {
		t.Fatalf("the panel opened %q, then %q", first, again)
	}
	if current := currentConversation(t, handler); current != first {
		t.Fatalf("the current conversation is %q, not %q", current, first)
	}

	// The new button posts a form, and the panel follows the redirect.
	second := openedConversation(t, serveRequest(handler, http.MethodPost, "/hermes/new"))
	if second == first || !strings.HasPrefix(second, "hermes:") {
		t.Fatalf("new opened %q after %q", second, first)
	}
	if opened, current := openedConversation(t, serveRequest(handler, http.MethodGet, "/hermes")), currentConversation(t, handler); opened != second || current != second {
		t.Fatalf("after new, the panel opens %q and the current conversation is %q, not %q", opened, current, second)
	}

	// A Hermes conversation belongs to no project, so it starts in the home folder.
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var cwds []string
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		var record struct {
			Method string `json:"method"`
			Params struct {
				CWD string `json:"cwd"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(line), &record) == nil && record.Method == "session/new" {
			cwds = append(cwds, record.Params.CWD)
		}
	}
	if !slices.Equal(cwds, []string{cfg.Home, cfg.Home}) {
		t.Fatalf("Hermes started conversations in %v", cwds)
	}
}

func TestHermesConversationsShowOnlyInTheHermesPanel(t *testing.T) {
	project := t.TempDir()
	cfg := hermesConfig(t, map[string]any{
		"sessionId": "hermes-chat", "cwd": project, "title": "Hermes chat", "updatedAt": "2026-03-01T10:00:00Z",
		"history": []any{textUpdate("user_message_chunk", "Will it rain?"), textUpdate("agent_message_chunk", "Not today.")},
	})
	piSession := filepath.Join(cfg.SessionsRoot, "pi.jsonl")
	if err := os.MkdirAll(cfg.SessionsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	records := []string{
		`{"type":"session","version":3,"id":"pi-chat","timestamp":"2026-02-01T10:00:00Z","cwd":"` + project + `"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-02-01T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"Pi chat"}]}}`,
	}
	if err := os.WriteFile(piSession, []byte(strings.Join(records, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := gateway.NewHandlerWithHermes(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	newConversation := newHermesConversation(t, handler)

	// Neither the sidebar nor the palette lists Hermes's conversations, saved or new, and the main page shows none.
	for _, target := range []string{"/", "/sidebar", "/sessions/palette", "/?session=" + url.QueryEscape("hermes:hermes-chat"), "/?session=" + url.QueryEscape(newConversation)} {
		page := getPage(t, handler, target)
		if !strings.Contains(page, "Pi chat") {
			t.Errorf("%s lacks the Pi session", target)
		}
		for _, hermes := range []string{"Hermes chat", "Not today.", newConversation} {
			if strings.Contains(page, hermes) {
				t.Errorf("%s shows %q", target, hermes)
			}
		}
	}
	main := serveRequest(handler, http.MethodGet, "/?session="+url.QueryEscape(piSession))
	if !strings.Contains(main.Body.String(), `data-hermes-toggle`) {
		t.Error("the main page lacks the Hermes button")
	}
	// Only the panel's page shows in a frame, and only in Gripi's own pages.
	if policy := main.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "frame-ancestors 'none'") {
		t.Errorf("the main page's policy is %q", policy)
	}
	panel := serveRequest(handler, http.MethodGet, panelPage("hermes:hermes-chat"))
	if policy := panel.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "frame-ancestors 'self'") {
		t.Errorf("the panel's policy is %q", policy)
	}
	for _, expected := range []string{"Will it rain?", "Not today.", `action="/hermes/new"`, `aria-label="Message to Hermes"`} {
		if !strings.Contains(panel.Body.String(), expected) {
			t.Errorf("the panel lacks %q", expected)
		}
	}
	if strings.Contains(panel.Body.String(), "Pi chat") {
		t.Error("the panel shows the Pi session")
	}
}

// unlist has the fake Hermes leave the conversation out of its list, as a recent Hermes does with every conversation
// that it files under its own platform.
func unlist(t *testing.T, key string) {
	t.Helper()
	if err := os.WriteFile(os.Getenv("FAKE_HERMES_UNLISTED"), []byte(strings.TrimPrefix(key, "hermes:")), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAConversationThatHermesDoesNotListIsReadLikeAnyOther(t *testing.T) {
	t.Setenv("FAKE_HERMES_UNLISTED", filepath.Join(t.TempDir(), "unlisted"))
	handler := hermesHandler(t)
	key := openedConversation(t, serveRequest(handler, http.MethodGet, "/hermes"))
	unlist(t, key)
	// The panel shows the conversation before its first message, and counts the reply that streams in after.
	page := getPage(t, handler, panelPage(key))
	count := regexp.MustCompile(`data-assistant-response-count="(\d+)"`).FindStringSubmatch(page)
	generation := regexp.MustCompile(`data-session-generation="([^"]*)"`).FindStringSubmatch(page)
	if count == nil || generation == nil {
		t.Fatal("the page lacks the conversation's read state")
	}
	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Hello"}}); code != http.StatusOK {
		t.Fatalf("prompt = %d %v", code, payload)
	}
	eventsUntil(t, handler, key, "agent_settled")

	shown, _ := strconv.Atoi(count[1])
	if code, _ := postJSON(t, handler, "/sessions/mark_read", url.Values{"session": {key}, "assistant_response_count": {strconv.Itoa(shown + 1)}, "session_generation": {generation[1]}}); code != http.StatusNoContent {
		t.Fatalf("mark read = %d", code)
	}
}

// Hermes loads a conversation that it doesn't list by its ID, also once the Hermes that ran it has exited.
func TestTheHermesPanelKeepsAConversationThatHermesDoesNotListAcrossARestart(t *testing.T) {
	t.Setenv("FAKE_HERMES_UNLISTED", filepath.Join(t.TempDir(), "unlisted"))
	handler := hermesHandler(t)
	key := openedConversation(t, serveRequest(handler, http.MethodGet, "/hermes"))
	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Hello"}}); code != http.StatusOK {
		t.Fatalf("prompt = %d %v", code, payload)
	}
	_, cursor := eventsAfterUntil(t, handler, key, 0, "agent_settled")
	unlist(t, key)

	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Exit now"}})
	eventsAfterUntil(t, handler, key, cursor, "agent_settled")
	time.Sleep(2 * time.Second) // Gripi lists Hermes's sessions afresh after this long, which starts Hermes again.
	if page := getPage(t, handler, panelPage(key)); !strings.Contains(page, "Hermes says: Hello") {
		t.Fatal("the page lacks the conversation")
	}
	if opened := openedConversation(t, serveRequest(handler, http.MethodGet, "/hermes")); opened != key {
		t.Fatalf("the panel opened %q in place of %q", opened, key)
	}
}

func TestPagesNeverWaitForHermesToStart(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hermes.log")
	t.Setenv("FAKE_HERMES_LOG", log)
	handler := hermesHandler(t)

	// An image of a session that the gateway hasn't listed yet makes it list the sessions.
	serveRequest(handler, http.MethodGet, "/attachments/"+strings.Repeat("a", 64)+"/0.png")
	for _, target := range []string{"/", "/sidebar", "/sessions/palette", "/tags"} {
		getPage(t, handler, target)
	}
	if _, err := os.Stat(log); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the gateway or a page started Hermes: %v", hermesStarts(t, log))
	}
	// The Hermes panel does start it.
	openedConversation(t, serveRequest(handler, http.MethodGet, "/hermes"))
	if starts := hermesStarts(t, log); len(starts) != 1 {
		t.Fatalf("Hermes started %v", starts)
	}
}

func TestAGatewayWithoutHermesOffersNoHermesPanel(t *testing.T) {
	cfg := hermesConfig(t)
	cfg.HermesPath = ""
	handler, err := gateway.NewPiHandler(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	if strings.Contains(getPage(t, handler, "/"), "data-hermes-toggle") {
		t.Error("the page offers the Hermes panel")
	}
	if response := serveRequest(handler, http.MethodGet, "/hermes"); response.Code != http.StatusNotFound {
		t.Errorf("GET /hermes = %d", response.Code)
	}
}

func TestAQuestionHermesStopsWaitingForCloses(t *testing.T) {
	handler := hermesHandler(t)
	key := newHermesConversation(t, handler)
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Delete the build folder unless I am too slow"}})

	types := []string{}
	for _, event := range eventsUntil(t, handler, key, "agent_settled") {
		types = append(types, event.Type)
	}
	asked, closed, toolEnded := slices.Index(types, "extension_ui_request"), slices.Index(types, "extension_ui_reset"), slices.Index(types, "tool_execution_end")
	// The question closes as Hermes's check ends, before the tool call that it asked about.
	if asked < 0 || closed < asked || toolEnded < closed {
		t.Fatalf("events %v", types)
	}
}

func TestAHermesThatExitsIsReplacedForTheNextMessage(t *testing.T) {
	handler := hermesHandler(t)
	key := newHermesConversation(t, handler)
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files"}})
	_, cursor := eventsAfterUntil(t, handler, key, 0, "agent_settled")
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Exit now"}})
	events, _ := eventsAfterUntil(t, handler, key, cursor, "agent_settled")
	for _, event := range events {
		message, _ := event.Message.(map[string]any)
		// The partial reply of a run that failed is no finished reply to tell the user about.
		if event.Type == "message_end" && message["role"] == "assistant" && message["stopReason"] != "error" || event.Type == "agent_end" && len(event.Messages) > 0 {
			t.Fatalf("the failed run ended %+v", event)
		}
	}

	// As after every run, the browser asks for the session's status, which finds Hermes gone.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status?session="+url.QueryEscape(key), nil))
	if code, payload := postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"List the files again"}}); code != http.StatusOK {
		t.Fatalf("prompt after Hermes exited = %d %v", code, payload)
	}
	events, _ = eventsAfterUntil(t, handler, key, 0, "agent_settled")
	if _, text := repliedText(events); !strings.HasSuffix(text, "Hermes says: List the files again") {
		t.Fatalf("the restarted Hermes streamed %q", text)
	}
}

func TestAStreamingReplyIsDeliveredOnceHoweverLateThePageAsks(t *testing.T) {
	t.Setenv("FAKE_HERMES_STEP_MS", "1")
	handler := hermesHandler(t)
	key := newHermesConversation(t, handler)
	postJSON(t, handler, "/prompt", url.Values{"session": {key}, "message": {"Think at length, then wait for stop"}})

	var batch struct {
		Events []map[string]any `json:"events"`
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := json.Unmarshal([]byte(getPage(t, handler, "/events?after=0&session="+url.QueryEscape(key))), &batch); err != nil {
			t.Fatal(err)
		}
		if update := batch.Events[len(batch.Events)-1]; update["gatewayPartialMessage"] != nil && strings.Contains(fmt.Sprint(update["gatewayPartialMessage"]), "piece 199") {
			break
		}
	}
	updates := 0
	for _, event := range batch.Events {
		if event["type"] == "message_update" {
			updates++
		}
	}
	// One update stands for the 200 pieces, with all of them in the reply it carries.
	if reply := fmt.Sprint(batch.Events[len(batch.Events)-1]["gatewayPartialMessage"]); updates != 1 || !strings.Contains(reply, "piece 0 piece 1 ") || !strings.Contains(reply, "piece 199") {
		t.Fatalf("a late page got %d updates: %v", updates, batch.Events[len(batch.Events)-1])
	}
	postJSON(t, handler, "/abort", url.Values{"session": {key}})
	eventsUntil(t, handler, key, "agent_settled")
}
