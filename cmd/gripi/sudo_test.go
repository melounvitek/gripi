package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	gateway "github.com/melounvitek/gripi/internal/server"
)

type cliResult struct {
	code           int
	stdout, stderr string
}

// startCLI runs a command that blocks, such as gripi sudo, in the background.
func startCLI(arguments ...string) <-chan cliResult {
	result := make(chan cliResult, 1)
	go func() {
		code, stdout, stderr := runCLI(arguments...)
		result <- cliResult{code, stdout, stderr}
	}()
	return result
}

func browserRequest(method, target string, form url.Values, cookie string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://127.0.0.1:4567"+target, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response := httptest.NewRecorder()
	browser.ServeHTTP(response, request)
	return response
}

// sudoRequestsInEvents returns the sudo requests that the browser's live events show for a session.
func sudoRequestsInEvents(t *testing.T, session, cookie string) []map[string]any {
	t.Helper()
	response := browserRequest(http.MethodGet, "/events?session="+url.QueryEscape(session), nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("events of %s = %d %s", session, response.Code, response.Body.String())
	}
	var payload struct {
		SudoRequests []map[string]any `json:"sudo_requests"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.SudoRequests == nil {
		t.Fatalf("events of %s have no sudo requests (%v): %s", session, err, response.Body.String())
	}
	return payload.SudoRequests
}

func sudoRequestsUntil(t *testing.T, session, cookie string, count int) []map[string]any {
	t.Helper()
	var requests []map[string]any
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if requests = sudoRequestsInEvents(t, session, cookie); len(requests) == count {
			return requests
		}
	}
	t.Fatalf("%s shows %d sudo requests, want %d: %v", session, len(requests), count, requests)
	return nil
}

var renderedSudoRequests = regexp.MustCompile(`data-sudo-requests="([^"]*)"`)

func cancelSudo(id, cookie string) int {
	return browserRequest(http.MethodPost, "/sudo/cancel", url.Values{"id": {id}}, cookie).Code
}

func outsideSessions(t *testing.T) {
	t.Setenv("PI_SESSION_FILE", "")
	t.Setenv("HERMES_SESSION_ID", "")
}

func TestSudoAsksInTheConversationUntilTheUserCancels(t *testing.T) {
	alpha, beta := fakePiGateway(t)
	outsideSessions(t)
	t.Setenv("PI_SESSION_FILE", alpha)
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	asked := time.Now()
	result := startCLI("sudo", "--", "apt", "install", "-y", "two words", "", "x\u202ey")
	requests := sudoRequestsUntil(t, alpha, "", 1)
	request := requests[0]
	keys := make([]string, 0, len(request))
	for key := range request {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	// The browser sees only what the dialog shows.
	if !slices.Equal(keys, []string{"cwd", "display", "expires_at", "id", "session"}) {
		t.Fatalf("the browser sees %v", request)
	}
	expires, _ := time.Parse(time.RFC3339Nano, request["expires_at"].(string))
	if request["session"] != alpha || request["cwd"] != directory || request["id"] == "" || expires.Before(asked.Add(gateway.SudoTimeout)) || expires.After(time.Now().Add(gateway.SudoTimeout)) {
		t.Fatalf("sudo request = %v", request)
	}
	if display := request["display"]; display != `apt install -y "two words" "" "x\u202ey"` {
		t.Fatalf("display = %s", display)
	}
	if others := sudoRequestsInEvents(t, beta, ""); len(others) != 0 {
		t.Fatalf("another session shows %v", others)
	}

	page := browserRequest(http.MethodGet, "/?session="+url.QueryEscape(alpha), nil, "")
	match := renderedSudoRequests.FindStringSubmatch(page.Body.String())
	if page.Code != http.StatusOK || match == nil {
		t.Fatalf("page = %d without sudo requests", page.Code)
	}
	var rendered []map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(match[1])), &rendered); err != nil || len(rendered) != 1 || rendered[0]["id"] != request["id"] || rendered[0]["display"] != request["display"] {
		t.Fatalf("rendered sudo requests = %v (%v)", rendered, err)
	}

	if code := cancelSudo(request["id"].(string), ""); code != http.StatusOK {
		t.Fatalf("cancel = %d", code)
	}
	if answered := <-result; answered.code != 1 || answered.stdout != "" || !strings.Contains(answered.stderr, "gripi sudo: ") || !strings.Contains(answered.stderr, "cancelled") {
		t.Fatalf("gripi sudo = %+v", answered)
	}
	// The first answer wins, and an answered request is gone.
	if code := cancelSudo(request["id"].(string), ""); code != http.StatusConflict {
		t.Fatalf("second cancel = %d", code)
	}
	if requests := sudoRequestsInEvents(t, alpha, ""); len(requests) != 0 {
		t.Fatalf("answered request is still shown: %v", requests)
	}
}

func TestSudoGivesUpWhenNobodyAnswers(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	outsideSessions(t)
	t.Setenv("PI_SESSION_FILE", alpha)
	defer func(timeout time.Duration) { gateway.SudoTimeout = timeout }(gateway.SudoTimeout)
	gateway.SudoTimeout = 300 * time.Millisecond

	if code, stdout, stderr := runCLI("sudo", "--", "true"); code != 1 || stdout != "" || !strings.Contains(stderr, "in time") {
		t.Fatalf("gripi sudo = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if requests := sudoRequestsInEvents(t, alpha, ""); len(requests) != 0 {
		t.Fatalf("expired request is still shown: %v", requests)
	}
}

// sudoUntil asks the gateway to run argv as root until ctx ends, as a gripi sudo that stops would.
func sudoUntil(ctx context.Context, session string, argv ...string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://gripi/sudo", strings.NewReader(url.Values{"session": {session}, "cwd": {"/"}, "argv": argv}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if response, err := socketClient(os.Getenv("GRIPI_SOCKET_PATH")).Do(request); err == nil {
			response.Body.Close()
		}
	}()
	return done
}

func TestSudoForgetsARequestWhoseCommandEnded(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := sudoUntil(ctx, alpha, "true")
	sudoRequestsUntil(t, alpha, "", 1)
	cancel()
	<-done
	sudoRequestsUntil(t, alpha, "", 0)
}

func TestSudoFailsAtOnceOutsideASessionOfTheGateway(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	outsideSessions(t)
	for _, usage := range [][]string{{"sudo"}, {"sudo", "--"}, {"sudo", "apt", "update"}} {
		if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help sudo") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
		}
	}
	if code, _, stderr := runCLI("sudo", "--", "true"); code != 1 || !strings.Contains(stderr, "PI_SESSION_FILE") || !strings.Contains(stderr, "HERMES_SESSION_ID") {
		t.Fatalf("gripi sudo outside a session = %d, stderr %q", code, stderr)
	}
	for variable, value := range map[string]string{"PI_SESSION_FILE": alpha + ".missing", "HERMES_SESSION_ID": "unknown"} {
		outsideSessions(t)
		t.Setenv(variable, value)
		if code, _, stderr := runCLI("sudo", "--", "true"); code != 1 || !strings.Contains(stderr, "no session of yours") {
			t.Fatalf("gripi sudo in an unknown session = %d, stderr %q", code, stderr)
		}
	}
}

func TestSudoAsksInANewSessionBeforeItsFirstReply(t *testing.T) {
	fakePiGateway(t)
	outsideSessions(t)
	_, stdout, _ := runCLI("new", t.TempDir(), "--json")
	started := decodeSession(t, stdout)
	t.Setenv("PI_SESSION_FILE", started.Path)

	result := startCLI("sudo", "--", "true")
	request := sudoRequestsUntil(t, started.Path, "", 1)[0]
	if code := cancelSudo(request["id"].(string), ""); code != http.StatusOK {
		t.Fatalf("cancel = %d", code)
	}
	if answered := <-result; answered.code != 1 {
		t.Fatalf("gripi sudo = %+v", answered)
	}
}

func TestSudoAsksInTheHermesConversationThatRunsIt(t *testing.T) {
	hermesGateway(t, fakeHermesSession{SessionID: "hermes-e2e", CWD: t.TempDir(), Title: "A Hermes session", UpdatedAt: "2026-03-01T10:00:00Z"})
	outsideSessions(t)
	t.Setenv("HERMES_SESSION_ID", "hermes-e2e")

	result := startCLI("sudo", "--", "true")
	request := sudoRequestsUntil(t, "hermes:hermes-e2e", "", 1)[0]
	if code := cancelSudo(request["id"].(string), ""); code != http.StatusOK {
		t.Fatalf("cancel = %d", code)
	}
	if answered := <-result; answered.code != 1 || !strings.Contains(answered.stderr, "cancelled") {
		t.Fatalf("gripi sudo = %+v", answered)
	}
}

func TestOnlyTheOwnerOfTheSessionSeesAndAnswersItsSudoRequest(t *testing.T) {
	record := fakeSudo(t, "password")
	alpha, beta := multiUserGateway(t)
	outsideSessions(t)
	alice, bob := workspaceCookies[aliceToken], workspaceCookies[bobToken]
	t.Setenv("PI_SESSION_FILE", alpha)

	t.Setenv("GRIPI_USER_TOKEN", bobToken)
	if code, _, stderr := runCLI("sudo", "--", "true"); code != 1 || !strings.Contains(stderr, "no session of yours") {
		t.Fatalf("gripi sudo for Bob in Alice's session = %d, stderr %q", code, stderr)
	}

	t.Setenv("GRIPI_USER_TOKEN", aliceToken)
	result := startCLI("sudo", "--", "true")
	request := sudoRequestsUntil(t, alpha, alice, 1)[0]
	if events := browserRequest(http.MethodGet, "/events?session="+url.QueryEscape(alpha), nil, bob); events.Code != http.StatusNotFound {
		t.Fatalf("Bob reads Alice's events: %d", events.Code)
	}
	if requests := sudoRequestsInEvents(t, beta, bob); len(requests) != 0 {
		t.Fatalf("Bob sees %v", requests)
	}
	if code := cancelSudo(request["id"].(string), bob); code != http.StatusConflict {
		t.Fatalf("Bob's cancel = %d", code)
	}
	if code := answerSudo(request["id"].(string), "bob's password", bob); code != http.StatusConflict {
		t.Fatalf("Bob's answer = %d", code)
	}
	if requests := sudoRequestsInEvents(t, alpha, alice); len(requests) != 1 {
		t.Fatalf("Bob changed Alice's requests: %v", requests)
	}
	// sudo's askpass program has no user token, and needs none.
	if code := answerSudo(request["id"].(string), "alice's password", alice); code != http.StatusOK {
		t.Fatalf("Alice's answer = %d", code)
	}
	if answered := <-result; answered.code != 0 {
		t.Fatalf("gripi sudo = %+v", answered)
	}
	if answers := askpassAnswers(t, record, "alice's password"); !slices.Equal(answers, []string{"password"}) {
		t.Fatalf("askpass answered %v", answers)
	}
}

func TestMain(m *testing.M) {
	// The gateway of these tests runs the test binary as sudo's askpass program, as it would run gripi.
	if os.Getenv("GRIPI_SUDO_TOKEN") != "" {
		os.Exit(answerSudoPrompt(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// fakeSudoScript stands in for sudo-rs. Named sudo, it is a parent that gripi's askpass program accepts. It asks
// askpass once for each word of FAKE_SUDO_ASKPASS, with the prompts that sudo-rs would use, and records only a hash
// of each answer. Then it records the command's arguments, environment and input, and runs the command as the user.
const fakeSudoScript = `#!/bin/sh
[ "$1 $2 $3 $5" = "-A -k -p --" ] || { echo "fake sudo: unexpected arguments $*" >&2; exit 99; }
marker=$4
shift 5
echo $$ > "$FAKE_SUDO_RECORD/pid"
for call in $FAKE_SUDO_ASKPASS; do
	case $call in
	password) "$SUDO_ASKPASS" "[sudo: $marker] Password: " ;;
	otp) "$SUDO_ASKPASS" "[sudo: $marker] Verification code: " ;;
	visible) "$SUDO_ASKPASS" "[sudo: input needed Password: " ;;
	unparented) sh -c '"$SUDO_ASKPASS" "$1"; :' sh "[sudo: $marker] Password: " ;;
	esac | sha256sum | cut -d ' ' -f 1 >> "$FAKE_SUDO_RECORD/askpass"
done
printf '%s\n' "$@" > "$FAKE_SUDO_RECORD/argv"
env > "$FAKE_SUDO_RECORD/env"
cat > "$FAKE_SUDO_RECORD/stdin"
exec "$@"
`

// fakeSudo makes the gateway run fakeSudoScript, which calls askpass as askpassCalls lists and records into the
// directory that it returns.
func fakeSudo(t *testing.T, askpassCalls string) (record string) {
	t.Helper()
	path, record := filepath.Join(t.TempDir(), "sudo"), t.TempDir()
	if err := os.WriteFile(path, []byte(fakeSudoScript), 0700); err != nil {
		t.Fatal(err)
	}
	previous := gateway.SudoPath
	t.Cleanup(func() { gateway.SudoPath = previous })
	gateway.SudoPath = path
	t.Setenv("FAKE_SUDO_RECORD", record)
	t.Setenv("FAKE_SUDO_ASKPASS", askpassCalls)
	return record
}

func recorded(t *testing.T, record, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(record, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// askpassAnswers names what each askpass call of the fake sudo printed: the password and a line break, or nothing.
func askpassAnswers(t *testing.T, record, password string) []string {
	t.Helper()
	answers := []string{}
	for _, hash := range strings.Fields(recorded(t, record, "askpass")) {
		switch hash {
		case sha256Hex(password + "\n"):
			answers = append(answers, "password")
		case sha256Hex(""):
			answers = append(answers, "nothing")
		default:
			answers = append(answers, "something else")
		}
	}
	return answers
}

func answerSudo(id, password, cookie string) int {
	return browserRequest(http.MethodPost, "/sudo/answer", url.Values{"id": {id}, "password": {password}}, cookie).Code
}

// sudoWithPassword runs gripi sudo and answers its request in the browser with the password.
func sudoWithPassword(t *testing.T, session, password string, command ...string) cliResult {
	t.Helper()
	result := startCLI(append([]string{"sudo", "--"}, command...)...)
	request := sudoRequestsUntil(t, session, "", 1)[0]
	if code := answerSudo(request["id"].(string), password, ""); code != http.StatusOK {
		t.Fatalf("answer = %d", code)
	}
	return <-result
}

// canaryForms are the canary as text and as any base64 that holds it reads.
func canaryForms(canary string) []string {
	forms := []string{canary}
	for offset := range 3 {
		data := append(make([]byte, offset), canary...)
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			encoded := encoding.EncodeToString(data)
			// The first and the last four characters also depend on the bytes around the canary.
			forms = append(forms, encoded[4:len(encoded)-4])
		}
	}
	return forms
}

func TestSudoRunsTheCommandWithThePasswordFromTheBrowserAndShowsItNowhere(t *testing.T) {
	record := fakeSudo(t, "password password")
	alpha, _ := fakePiGateway(t)
	outsideSessions(t)
	t.Setenv("PI_SESSION_FILE", alpha)
	t.Setenv("TMPDIR", t.TempDir())
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	canary := "canary-" + sha256Hex(t.Name())[:24]

	answered := sudoWithPassword(t, alpha, canary, "sh", "-c", "echo out; echo err >&2; exit 7")
	if answered.code != 7 || answered.stdout != "out\n" || !strings.HasSuffix(answered.stderr, "err\n") {
		t.Fatalf("gripi sudo = %+v", answered)
	}
	// A second prompt, as after a wrong password, gets nothing, so sudo gives up.
	if answers := askpassAnswers(t, record, canary); !slices.Equal(answers, []string{"password", "nothing"}) {
		t.Fatalf("askpass answered %v", answers)
	}
	if argv := recorded(t, record, "argv"); argv != "sh\n-c\necho out; echo err >&2; exit 7\n" {
		t.Fatalf("command arguments = %q", argv)
	}

	page := browserRequest(http.MethodGet, "/?session="+url.QueryEscape(alpha), nil, "").Body.String()
	events := browserRequest(http.MethodGet, "/events?session="+url.QueryEscape(alpha), nil, "").Body.String()
	log.SetOutput(os.Stderr)
	exposed := []string{answered.stdout, answered.stderr, page, events, logged.String()}
	// The fake's records, the gateway's state and TMPDIR are all in the test's temporary directories.
	err := filepath.WalkDir(filepath.Dir(t.TempDir()), func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			contents, readErr := os.ReadFile(path)
			exposed = append(exposed, path+"\n"+string(contents))
			return readErr
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range exposed {
		for _, form := range canaryForms(canary) {
			if strings.Contains(text, form) {
				t.Fatalf("the password shows as %q in %q", form, text)
			}
		}
	}
}

func TestSudoGivesThePasswordOnlyToThePasswordPromptOfSudo(t *testing.T) {
	record := fakeSudo(t, "otp visible unparented password")
	alpha, _ := fakePiGateway(t)
	outsideSessions(t)
	t.Setenv("PI_SESSION_FILE", alpha)

	if answered := sudoWithPassword(t, alpha, "secret", "true"); answered.code != 0 || answered.stdout != "" {
		t.Fatalf("gripi sudo = %+v", answered)
	}
	// The password that the last prompt got shows that the others did not take it.
	if answers := askpassAnswers(t, record, "secret"); !slices.Equal(answers, []string{"nothing", "nothing", "nothing", "password"}) {
		t.Fatalf("askpass answered %v", answers)
	}
}

func TestSudoThatAsksNoPasswordNeverGetsIt(t *testing.T) {
	record := fakeSudo(t, "")
	alpha, _ := fakePiGateway(t)
	outsideSessions(t)
	t.Setenv("PI_SESSION_FILE", alpha)

	if answered := sudoWithPassword(t, alpha, "secret", "cat"); answered.code != 0 || answered.stdout != "" {
		t.Fatalf("gripi sudo = %+v", answered)
	}
	if stdin := recorded(t, record, "stdin"); stdin != "" {
		t.Fatalf("the command read %q", stdin)
	}
	var token string
	for line := range strings.Lines(recorded(t, record, "env")) {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "GRIPI_SUDO_TOKEN="); found {
			token = value
		}
	}
	if token == "" {
		t.Fatal("sudo got no token")
	}
	response, err := socketClient(os.Getenv("GRIPI_SOCKET_PATH")).PostForm("http://gripi/sudo/askpass", url.Values{"token": {token}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if body, _ := io.ReadAll(response.Body); response.StatusCode != http.StatusNotFound || strings.Contains(string(body), "secret") {
		t.Fatalf("token after sudo ended = %d %q", response.StatusCode, body)
	}
}

func TestSudoTakesTheFirstPasswordThatTheBrowserPostsAsAForm(t *testing.T) {
	record := fakeSudo(t, "password")
	alpha, _ := fakePiGateway(t)
	outsideSessions(t)
	t.Setenv("PI_SESSION_FILE", alpha)

	result := startCLI("sudo", "--", "true")
	id := sudoRequestsUntil(t, alpha, "", 1)[0]["id"].(string)
	for _, password := range []string{"", "two\nlines", "carriage\rreturn"} {
		if code := answerSudo(id, password, ""); code != http.StatusBadRequest {
			t.Fatalf("answer %q = %d", password, code)
		}
	}
	if code := browserRequest(http.MethodPost, "/sudo/answer?password=secret", url.Values{"id": {id}}, "").Code; code != http.StatusBadRequest {
		t.Fatalf("answer with the password in its URL = %d", code)
	}
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	writer.WriteField("id", id)
	writer.WriteField("password", "secret")
	writer.Close()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4567/sudo/answer", &multipartBody)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	browser.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("multipart answer = %d", response.Code)
	}

	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	body := &readTracker{Reader: strings.NewReader(url.Values{"id": {id}, "password": {"secret"}}.Encode())}
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4567/sudo/answer", nil)
	request.Body, request.ContentLength, request.TransferEncoding = body, -1, []string{"chunked"}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	browser.ServeHTTP(response, request)
	if entries, _ := os.ReadDir(temporary); response.Code != http.StatusLengthRequired || body.read || len(entries) != 0 {
		t.Fatalf("answer of unknown length = %d, read %v, temporary files %v", response.Code, body.read, entries)
	}
	if requests := sudoRequestsInEvents(t, alpha, ""); len(requests) != 1 {
		t.Fatalf("refused answers ended the request: %v", requests)
	}

	if code := answerSudo(id, "right", ""); code != http.StatusOK {
		t.Fatalf("answer = %d", code)
	}
	if code := answerSudo(id, "wrong", ""); code != http.StatusConflict {
		t.Fatalf("second answer = %d", code)
	}
	if code := cancelSudo(id, ""); code != http.StatusConflict {
		t.Fatalf("cancel after the answer = %d", code)
	}
	if answered := <-result; answered.code != 0 {
		t.Fatalf("gripi sudo = %+v", answered)
	}
	if answers := askpassAnswers(t, record, "right"); !slices.Equal(answers, []string{"password"}) {
		t.Fatalf("askpass answered %v", answers)
	}
}

type readTracker struct {
	io.Reader
	read bool
}

func (tracker *readTracker) Read(data []byte) (int, error) {
	tracker.read = true
	return tracker.Reader.Read(data)
}

func (*readTracker) Close() error { return nil }

func TestSudoStopsTheCommandWhenGripiSudoEnds(t *testing.T) {
	record := fakeSudo(t, "")
	alpha, _ := fakePiGateway(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := sudoUntil(ctx, alpha, "sleep", "30")
	id := sudoRequestsUntil(t, alpha, "", 1)[0]["id"].(string)
	if code := answerSudo(id, "secret", ""); code != http.StatusOK {
		t.Fatalf("answer = %d", code)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(record, "stdin")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the command never started")
		}
	}
	pid, err := strconv.Atoi(strings.TrimSpace(recorded(t, record, "pid")))
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	<-done
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("sudo still runs")
		}
	}
}
