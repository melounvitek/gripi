package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/server"
)

func TestSavedVariablesReachPiOnTopOfTheGatewayEnvironment(t *testing.T) {
	t.Setenv("ENVTEST_SHARED", "from the gateway")
	fixture := newEnvironmentFixture(t, false)
	session := fixture.session(t, "existing", "")
	fixture.save(t, "", "ENVTEST_TOKEN", "saved token")
	fixture.save(t, "", "ENVTEST_SHARED", "from the user")

	fixture.startPi(t, session, "")
	created := fixture.post("/sessions/new_at_cwd", url.Values{"cwd": {fixture.project}}, "")
	if created.Code != http.StatusOK {
		t.Fatalf("new session = %d %s", created.Code, created.Body.String())
	}

	starts := fixture.piStarts(t)
	if len(starts) != 2 || !strings.Contains(starts[0].arguments, "--session "+session) || strings.Contains(starts[1].arguments, "--session") {
		t.Fatalf("Pi starts = %#v", starts)
	}
	for _, start := range starts {
		if start.environment["ENVTEST_TOKEN"] != "saved token" || start.environment["ENVTEST_SHARED"] != "from the user" {
			t.Errorf("Pi %q got ENVTEST_TOKEN=%q ENVTEST_SHARED=%q", start.arguments, start.environment["ENVTEST_TOKEN"], start.environment["ENVTEST_SHARED"])
		}
	}
}

func TestMultiUserVariablesStayWithTheirOwner(t *testing.T) {
	fixture := newEnvironmentFixture(t, true)
	owner, other := "gripi_workspace=workspace-a", "gripi_workspace=workspace-b"
	ownerSession := fixture.session(t, "owner", "workspace-a")
	otherSession := fixture.session(t, "other", "workspace-b")
	fixture.save(t, owner, "ENVTEST_TOKEN", "owner token")

	if names := fixture.names(t, other); len(names) != 0 {
		t.Fatalf("another user lists %v", names)
	}
	if value := fixture.get("/environment/value?name=ENVTEST_TOKEN", other); value.Code != http.StatusNotFound {
		t.Fatalf("another user reads the value: %d %s", value.Code, value.Body.String())
	}
	fixture.save(t, other, "ENVTEST_TOKEN", "other token")
	if removed := fixture.post("/environment/variable/delete", url.Values{"name": {"ENVTEST_TOKEN"}}, other); removed.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", removed.Code, removed.Body.String())
	}
	if value := fixture.value(t, owner, "ENVTEST_TOKEN"); value != "owner token" {
		t.Fatalf("owner's value after another user saved and deleted the same name = %q", value)
	}

	fixture.startPi(t, ownerSession, owner)
	fixture.startPi(t, otherSession, other)
	created := fixture.post("/sessions/new_at_cwd", url.Values{"cwd": {fixture.project}}, other)
	if created.Code != http.StatusOK {
		t.Fatalf("new session = %d %s", created.Code, created.Body.String())
	}

	starts := fixture.piStarts(t)
	if len(starts) != 3 || !strings.Contains(starts[0].arguments, "--session "+ownerSession) || !strings.Contains(starts[1].arguments, "--session "+otherSession) {
		t.Fatalf("Pi starts = %#v", starts)
	}
	if value := starts[0].environment["ENVTEST_TOKEN"]; value != "owner token" {
		t.Errorf("owner's session got ENVTEST_TOKEN=%q", value)
	}
	for _, start := range starts[1:] {
		if value, found := start.environment["ENVTEST_TOKEN"]; found {
			t.Errorf("another user's Pi %q got ENVTEST_TOKEN=%q", start.arguments, value)
		}
	}
}

func TestMultiUserEnvironmentDialogSaysWhoGetsTheVariablesAndWhoCouldReadThem(t *testing.T) {
	page := newEnvironmentFixture(t, true).get("/", "gripi_workspace=workspace-a").Body.String()
	description := "Pi and every command it runs get these variables in your sessions only, on top of the gateway's own environment. They are stored on the gateway, where other users' Pi could read them."
	if !strings.Contains(page, description) {
		t.Fatal("the page lacks the multi-user description of the environment dialog")
	}
}

func TestEnvironmentSavingRules(t *testing.T) {
	fixture := newEnvironmentFixture(t, false)
	assertNames := func(step string, expected ...string) {
		t.Helper()
		if names := fixture.names(t, ""); !slices.Equal(names, expected) {
			t.Fatalf("%s: names = %v, want %v", step, names, expected)
		}
	}
	assertValue := func(name, expected string) {
		t.Helper()
		if value := fixture.value(t, "", name); value != expected {
			t.Fatalf("%s = %q, want %q", name, value, expected)
		}
	}
	assertRejected := func(response *httptest.ResponseRecorder, message string) {
		t.Helper()
		var payload struct {
			Error string `json:"error"`
		}
		if response.Code != http.StatusUnprocessableEntity || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Error != message {
			t.Fatalf("response = %d %s, want 422 %q", response.Code, response.Body.String(), message)
		}
	}

	fixture.save(t, "", "FIRST", "secret one")
	fixture.save(t, "", "SECOND", "two")
	fixture.save(t, "", "THIRD", "three")
	list := fixture.get("/environment", "")
	if list.Code != http.StatusOK || list.Body.String() != `{"variables":[{"name":"FIRST"},{"name":"SECOND"},{"name":"THIRD"}]}` {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	if list.Header().Get("Cache-Control") != "no-store" || list.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("list headers = %#v", list.Header())
	}

	replaced := fixture.post("/environment/variable", url.Values{"name": {"SECOND"}, "value": {"second"}}, "")
	if replaced.Code != http.StatusOK || replaced.Body.String() != list.Body.String() || replaced.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("replace = %d %#v %s", replaced.Code, replaced.Header(), replaced.Body.String())
	}
	assertValue("SECOND", "second")

	fixture.save(t, "", "FOURTH", "four")
	assertNames("append", "FIRST", "SECOND", "THIRD", "FOURTH")

	renamed := fixture.post("/environment/variable", url.Values{"name": {"RENAMED"}, "value": {"second"}, "previous_name": {"SECOND"}}, "")
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", renamed.Code, renamed.Body.String())
	}
	assertNames("rename", "FIRST", "RENAMED", "THIRD", "FOURTH")
	assertValue("RENAMED", "second")

	assertRejected(fixture.post("/environment/variable", url.Values{"name": {"FIRST"}, "value": {"moved"}, "previous_name": {"THIRD"}}, ""), "FIRST is already set.")
	assertNames("rename onto a saved name", "FIRST", "RENAMED", "THIRD", "FOURTH")
	assertValue("FIRST", "secret one")
	assertValue("THIRD", "three")

	removed := fixture.post("/environment/variable/delete", url.Values{"name": {"THIRD"}}, "")
	if removed.Code != http.StatusOK || removed.Body.String() != `{"variables":[{"name":"FIRST"},{"name":"RENAMED"},{"name":"FOURTH"}]}` {
		t.Fatalf("delete = %d %s", removed.Code, removed.Body.String())
	}
	if value := fixture.get("/environment/value?name=THIRD", ""); value.Code != http.StatusNotFound {
		t.Fatalf("deleted value = %d %s", value.Code, value.Body.String())
	}

	assertRejected(fixture.post("/environment/variables", url.Values{"text": {"FIRST=changed\nFIFTH=five\nnot a pair\n"}}, ""), "Line 3: expected NAME=value.")
	assertNames("rejected block", "FIRST", "RENAMED", "FOURTH")
	assertValue("FIRST", "secret one")

	block := "  # a comment = ignored\r\n\r\n  FIFTH = five  \r\nFIRST='single quoted'\nSIXTH=\"double quoted\"\nSEVENTH=a=b\nEIGHTH='mismatched\"\n"
	saved := fixture.post("/environment/variables", url.Values{"text": {block}}, "")
	if saved.Code != http.StatusOK || saved.Body.String() != `{"saved":5,"variables":[{"name":"FIRST"},{"name":"RENAMED"},{"name":"FOURTH"},{"name":"FIFTH"},{"name":"SIXTH"},{"name":"SEVENTH"},{"name":"EIGHTH"}]}` {
		t.Fatalf("block = %d %s", saved.Code, saved.Body.String())
	}
	for name, expected := range map[string]string{"FIFTH": "five", "FIRST": "single quoted", "SIXTH": "double quoted", "SEVENTH": "a=b", "EIGHTH": `'mismatched"`} {
		assertValue(name, expected)
	}
}

func TestEnvironmentValidation(t *testing.T) {
	fixture := newEnvironmentFixture(t, false)
	limit := 16 << 10
	for _, test := range []struct{ name, value, message string }{
		{"ASANA API KEY", "value", "“ASANA API KEY” is not a valid name. Use letters, digits and _."},
		{"1PASSWORD", "value", "“1PASSWORD” is not a valid name. Use letters, digits and _."},
		{"", "value", "“” is not a valid name. Use letters, digits and _."},
		{"GRIPI_PORT", "4567", "“GRIPI_PORT” is reserved for Gripi and Pi."},
		{"PI_CODING_AGENT_DIR", "/tmp", "“PI_CODING_AGENT_DIR” is reserved for Gripi and Pi."},
		{"HOME", "/tmp", "“HOME” is reserved for Gripi and Pi."},
		{"GH_TOKEN", "", "GH_TOKEN has no value."},
		{"GH_TOKEN", "a\x00b", "GH_TOKEN has an invalid value."},
		{"GH_TOKEN", strings.Repeat("x", limit+1), "GH_TOKEN is too long."},
	} {
		response := fixture.post("/environment/variable", url.Values{"name": {test.name}, "value": {test.value}}, "")
		if response.Code != http.StatusUnprocessableEntity || response.Body.String() != `{"error":`+jsonString(test.message)+`}` {
			t.Errorf("%q=%.20q: %d %s, want 422 %q", test.name, test.value, response.Code, response.Body.String(), test.message)
		}
	}
	if names := fixture.names(t, ""); len(names) != 0 {
		t.Fatalf("rejected variables were saved: %v", names)
	}
	fixture.save(t, "", "_lower_9", strings.Repeat("x", limit))

	for _, test := range []struct{ text, message string }{
		{"# tokens\n\nGH_TOKEN\n", "Line 3: expected NAME=value."},
		{"GH_TOKEN=one\n\n\n\n\n\nGH_TOKEN=two\n", "Line 7: GH_TOKEN is set twice."},
		{"GH_TOKEN=one\nASANA API KEY=two\n", "Line 2: “ASANA API KEY” is not a valid name. Use letters, digits and _."},
		{"GRIPI_PORT=4567\n", "Line 1: “GRIPI_PORT” is reserved for Gripi and Pi."},
		{"GH_TOKEN=one\nGIT_AUTHOR_NAME=\n", "Line 2: GIT_AUTHOR_NAME has no value."},
		{"GH_TOKEN=''\n", "Line 1: GH_TOKEN has no value."},
		{"GH_TOKEN=" + strings.Repeat("x", limit+1) + "\n", "Line 1: GH_TOKEN is too long."},
	} {
		response := fixture.post("/environment/variables", url.Values{"text": {test.text}}, "")
		if response.Code != http.StatusUnprocessableEntity || response.Body.String() != `{"error":`+jsonString(test.message)+`}` {
			t.Errorf("block %.30q: %d %s, want 422 %q", test.text, response.Code, response.Body.String(), test.message)
		}
	}

	var lines []string
	for index := range 99 {
		lines = append(lines, fmt.Sprintf("FILLER_%d=value", index))
	}
	if filled := fixture.post("/environment/variables", url.Values{"text": {strings.Join(lines, "\n")}}, ""); filled.Code != http.StatusOK {
		t.Fatalf("fill to the limit = %d %s", filled.Code, filled.Body.String())
	}
	for route, values := range map[string]url.Values{
		"/environment/variable":  {"name": {"ONE_TOO_MANY"}, "value": {"value"}},
		"/environment/variables": {"text": {"_lower_9=replaced\nONE_TOO_MANY=value\n"}},
	} {
		message := "At most 100 variables can be saved."
		if route == "/environment/variables" {
			message = "Line 2: " + message
		}
		response := fixture.post(route, values, "")
		if response.Code != http.StatusUnprocessableEntity || response.Body.String() != `{"error":`+jsonString(message)+`}` {
			t.Errorf("%s over the limit: %d %s", route, response.Code, response.Body.String())
		}
	}
	if names := fixture.names(t, ""); len(names) != 100 {
		t.Fatalf("saved %d variables, want 100", len(names))
	}
	fixture.save(t, "", "FILLER_0", "replacing at the limit")
}

func TestMalformedEnvironmentStateIsLeftUntouchedAndStopsPi(t *testing.T) {
	fixture := newEnvironmentFixture(t, false)
	session := fixture.session(t, "existing", "")
	malformed := []byte(`{"users":{"":{"variables":[{"name":"ENVTEST_TOKEN","value":"sec`)
	if err := os.WriteFile(fixture.cfg.EnvironmentPath, malformed, 0600); err != nil {
		t.Fatal(err)
	}

	for _, response := range []*httptest.ResponseRecorder{
		fixture.get("/environment", ""),
		fixture.get("/environment/value?name=ENVTEST_TOKEN", ""),
		fixture.post("/environment/variable", url.Values{"name": {"ENVTEST_TOKEN"}, "value": {"value"}}, ""),
		fixture.post("/environment/variables", url.Values{"text": {"ENVTEST_TOKEN=value"}}, ""),
		fixture.post("/environment/variable/delete", url.Values{"name": {"ENVTEST_TOKEN"}}, ""),
		fixture.post("/prompt", url.Values{"session": {session}, "message": {"hello"}}, ""),
		fixture.post("/sessions/new_at_cwd", url.Values{"cwd": {fixture.project}}, ""),
	} {
		if response.Code != http.StatusInternalServerError {
			t.Errorf("response = %d %s, want 500", response.Code, response.Body.String())
		}
	}
	if starts := fixture.piStarts(t); len(starts) != 0 {
		t.Fatalf("Pi started without its variables: %#v", starts)
	}
	if persisted, err := os.ReadFile(fixture.cfg.EnvironmentPath); err != nil || string(persisted) != string(malformed) {
		t.Fatalf("malformed state was rewritten: %q, %v", persisted, err)
	}
}

func TestChangedVariablesApplyFromTheNextMessage(t *testing.T) {
	fixture := newEnvironmentFixture(t, false)
	session := fixture.session(t, "existing", "")
	fixture.save(t, "", "ENVTEST_TOKEN", "first")
	fixture.prompt(t, session, "Use the first token", "")
	waitForFakePiSettled(t, fixture.handler, session, 0)

	fixture.save(t, "", "ENVTEST_TOKEN", "second")
	fixture.prompt(t, session, "Use the second token", "")
	fixture.assertTokens(t, session, "first", "second")
	waitForFakePiSettled(t, fixture.handler, session, 0)

	fixture.prompt(t, session, "!printf unchanged", "")
	fixture.assertTokens(t, session, "first", "second")

	fixture.save(t, "", "ENVTEST_TOKEN", "third")
	fixture.prompt(t, session, "!printf changed", "")
	fixture.assertTokens(t, session, "first", "second", "third")
}

func TestRestartForChangedVariablesKeepsTheEventCursorValid(t *testing.T) {
	fixture := newEnvironmentFixture(t, false)
	session := fixture.session(t, "existing", "")
	fixture.save(t, "", "ENVTEST_TOKEN", "first")
	fixture.prompt(t, session, "Use the first token", "")
	waitForFakePiSettled(t, fixture.handler, session, 0)
	cursor := fakePiEventCursor(t, fixture.handler, session)

	fixture.save(t, "", "ENVTEST_TOKEN", "second")
	// This turn stays open, so the new Pi's event sequence stays below the browser's cursor.
	fixture.prompt(t, session, "Start the steer scenario", "")
	fixture.assertTokens(t, session, "first", "second")

	for deadline := time.Now().Add(4 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		response := fixture.get(fmt.Sprintf("/events?session=%s&after=%d", url.QueryEscape(session), cursor), "")
		var batch struct {
			Events  []map[string]any `json:"events"`
			LastSeq int64            `json:"last_seq"`
			Missed  bool             `json:"missed"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &batch) != nil || batch.LastSeq >= cursor {
			t.Fatalf("events after the old Pi's last event %d = %d %s", cursor, response.Code, response.Body.String())
		}
		if batch.Missed {
			t.Fatalf("the cursor at the old Pi's last event %d missed events: %s", cursor, response.Body.String())
		}
		if slices.ContainsFunc(batch.Events, func(event map[string]any) bool { return event["type"] == "agent_start" }) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the new turn did not arrive after the old Pi's last event %d: %s", cursor, response.Body.String())
		}
	}
}

func TestChangedVariablesDoNotRestartABusyPi(t *testing.T) {
	fixture := newEnvironmentFixture(t, false)
	session := fixture.session(t, "existing", "")
	fixture.save(t, "", "ENVTEST_TOKEN", "first")
	fixture.prompt(t, session, "Start the steer scenario", "")
	for deadline := time.Now().Add(4 * time.Second); !strings.Contains(fixture.get("/events?session="+url.QueryEscape(session), "").Body.String(), `"gateway_busy":true`); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the turn did not start")
		}
	}

	fixture.save(t, "", "ENVTEST_TOKEN", "second")
	fixture.prompt(t, session, "Use the steered direction", "steer")
	fixture.assertTokens(t, session, "first")
	waitForFakePiSettled(t, fixture.handler, session, 0)

	fixture.prompt(t, session, "Use the second token", "")
	fixture.assertTokens(t, session, "first", "second")
}

func TestChangedVariablesLeaveAnUnsavedNewSessionAlone(t *testing.T) {
	fixture := newEnvironmentFixture(t, false)
	fixture.save(t, "", "ENVTEST_TOKEN", "first")
	created := fixture.post("/sessions/new_at_cwd", url.Values{"cwd": {fixture.project}}, "")
	var payload struct {
		Session string `json:"session"`
	}
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &payload) != nil || payload.Session == "" {
		t.Fatalf("new session = %d %s", created.Code, created.Body.String())
	}

	fixture.save(t, "", "ENVTEST_TOKEN", "second")
	fixture.prompt(t, payload.Session, "Create the first deterministic response", "")
	fixture.assertTokens(t, "", "first")
	waitForFakePiSettled(t, fixture.handler, payload.Session, 0)

	// Pi has saved the session now, so a new process can resume it.
	fixture.prompt(t, payload.Session, "Use the second token", "")
	fixture.assertTokens(t, "", "first", "second")
}

type environmentFixture struct {
	handler http.Handler
	cfg     config.Config
	project string
	piLog   string
}

type piStart struct {
	arguments   string
	environment map[string]string
}

// newEnvironmentFixture serves a gateway whose Pi is the fake Pi behind a script that records each start's
// arguments and environment.
func newEnvironmentFixture(t *testing.T, multiUser bool) *environmentFixture {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture := &environmentFixture{project: filepath.Join(root, "project"), piLog: filepath.Join(root, "pi-starts.log")}
	cfg := multiUserConfig(root)
	cfg.MultiUserMode = multiUser
	cfg.EnvironmentPath = filepath.Join(root, "environment.json")
	for _, directory := range []string{cfg.SessionsRoot, cfg.AttachmentsRoot, fixture.project} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(root, "pi")
	contents := fmt.Sprintf("#!/bin/sh\n{ printf 'pi-start %%s\\n' \"$*\"; env; } >> '%s'\nexec '%s' '%s' \"$@\"\n", fixture.piLog, node, filepath.Join(repoRoot, "e2e", "support", "fake_pi.mjs"))
	if err := os.WriteFile(script, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.PiCommand = []string{script}
	t.Setenv("GRIPI_E2E_SESSIONS_ROOT", cfg.SessionsRoot)
	t.Setenv("GRIPI_E2E_FAKE_PI_LOG", filepath.Join(root, "fake-pi.log"))
	if multiUser {
		for _, workspace := range []string{"workspace-a", "workspace-b"} {
			if err := access.NewWorkspaceStore(cfg.WorkspaceAccessPath).ApproveWorkspace(workspace); err != nil {
				t.Fatal(err)
			}
		}
	}
	fixture.cfg = cfg
	fixture.handler, err = server.NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	closer := fixture.handler.(interface{ Close(context.Context) error })
	t.Cleanup(func() { _ = closer.Close(context.Background()) })
	return fixture
}

func (fixture *environmentFixture) session(t *testing.T, name, workspace string) string {
	t.Helper()
	path := filepath.Join(fixture.cfg.SessionsRoot, name+".jsonl")
	writeActionSession(t, path, fixture.project)
	if workspace != "" {
		if _, err := access.NewWorkspaceOwnershipStore(fixture.cfg.WorkspaceOwnershipPath, fixture.cfg.SessionsRoot).Claim(path, workspace); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func (fixture *environmentFixture) get(target, cookie string) *httptest.ResponseRecorder {
	return getWorkspace(fixture.handler, target, cookie)
}

func (fixture *environmentFixture) post(target string, values url.Values, cookie string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "http://app.test"+target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}

func (fixture *environmentFixture) save(t *testing.T, cookie, name, value string) {
	t.Helper()
	if response := fixture.post("/environment/variable", url.Values{"name": {name}, "value": {value}}, cookie); response.Code != http.StatusOK {
		t.Fatalf("save %s = %d %s", name, response.Code, response.Body.String())
	}
}

func (fixture *environmentFixture) names(t *testing.T, cookie string) []string {
	t.Helper()
	response := fixture.get("/environment", cookie)
	var payload struct {
		Variables []map[string]string `json:"variables"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Variables == nil {
		t.Fatalf("list = %d %s", response.Code, response.Body.String())
	}
	names := []string{}
	for _, variable := range payload.Variables {
		if len(variable) != 1 {
			t.Fatalf("listed variable = %v, want only its name", variable)
		}
		names = append(names, variable["name"])
	}
	return names
}

func (fixture *environmentFixture) value(t *testing.T, cookie, name string) string {
	t.Helper()
	response := fixture.get("/environment/value?name="+url.QueryEscape(name), cookie)
	var payload struct {
		Value string `json:"value"`
	}
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(response.Body.Bytes(), &payload) != nil {
		t.Fatalf("value of %s = %d %#v %s", name, response.Code, response.Header(), response.Body.String())
	}
	return payload.Value
}

// startPi makes the gateway start Pi for the session and waits until it answers.
func (fixture *environmentFixture) startPi(t *testing.T, session, cookie string) {
	t.Helper()
	if response := fixture.get("/sessions/model_settings?session="+url.QueryEscape(session), cookie); response.Code != http.StatusOK {
		t.Fatalf("start Pi for %s = %d %s", session, response.Code, response.Body.String())
	}
}

func (fixture *environmentFixture) prompt(t *testing.T, session, message, streamingBehavior string) {
	t.Helper()
	values := url.Values{"session": {session}, "message": {message}}
	if streamingBehavior != "" {
		values.Set("streaming_behavior", streamingBehavior)
	}
	if response := fixture.post("/prompt", values, ""); response.Code != http.StatusOK {
		t.Fatalf("prompt %q = %d %s", message, response.Code, response.Body.String())
	}
}

// assertTokens checks which ENVTEST_TOKEN each Pi started so far got, oldest first.
func (fixture *environmentFixture) assertTokens(t *testing.T, session string, expected ...string) {
	t.Helper()
	var tokens []string
	for _, start := range fixture.piStarts(t) {
		if session != "" && !strings.Contains(start.arguments, "--session "+session) {
			t.Fatalf("Pi started with %q, want session %s", start.arguments, session)
		}
		tokens = append(tokens, start.environment["ENVTEST_TOKEN"])
	}
	if !slices.Equal(tokens, expected) {
		t.Fatalf("ENVTEST_TOKEN of each Pi start = %q, want %q", tokens, expected)
	}
}

func (fixture *environmentFixture) piStarts(t *testing.T) []piStart {
	t.Helper()
	contents, err := os.ReadFile(fixture.piLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var starts []piStart
	for _, line := range strings.Split(string(contents), "\n") {
		if arguments, found := strings.CutPrefix(line, "pi-start "); found {
			starts = append(starts, piStart{arguments: arguments, environment: map[string]string{}})
		} else if name, value, found := strings.Cut(line, "="); found && len(starts) > 0 {
			starts[len(starts)-1].environment[name] = value
		}
	}
	return starts
}
