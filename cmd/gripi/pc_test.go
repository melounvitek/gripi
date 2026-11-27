package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

// The approved browsers: the laptop's and the desk's desktop apps, and a phone, which has no PC access.
const (
	pcBrowserToken = "pc-desktop-browser"
	pcDeskToken    = "pc-desk-browser"
	pcPhoneToken   = "pc-phone-browser"
)

// pcMessage is a message of the gateway to the desktop app.
type pcMessage struct {
	Type        string   `json:"type"`
	ID          string   `json:"id"`
	Session     string   `json:"session"`
	SessionName string   `json:"session_name"`
	Connection  string   `json:"connection"`
	Message     []byte   `json:"message"`
	Argv        []string `json:"argv"`
	CWD         string   `json:"cwd"`
	Root        bool     `json:"root"`
}

type pcLinkAnswer struct {
	Messages []pcMessage `json:"messages"`
	Error    string      `json:"error"`
}

// pcGateway runs fakePiGateway with access approval on, which PC access needs, and the approved browsers. gripi runs
// in alpha, and the desktop apps reach the gateway at address.
func pcGateway(t *testing.T, configure ...func(*config.Config)) (alpha, address string) {
	t.Helper()
	poll, stale, pins := gateway.PCLinkPoll, gateway.PCLinkStale, gateway.PCPins
	t.Cleanup(func() { gateway.PCLinkPoll, gateway.PCLinkStale, gateway.PCPins = poll, stale, pins })
	gateway.PCLinkPoll, gateway.PCLinkStale = 200*time.Millisecond, 500*time.Millisecond
	var browserAccess string
	alpha, _ = fakePiGateway(t, append([]func(*config.Config){func(cfg *config.Config) {
		cfg.AdminPassword, cfg.BrowserAuthDisabled, browserAccess = "secret", false, cfg.BrowserAccessPath
	}}, configure...)...)
	for _, token := range []string{pcBrowserToken, pcDeskToken, pcPhoneToken} {
		if _, err := access.NewBrowserStore(browserAccess).ApproveCurrentBrowser(token, token); err != nil {
			t.Fatal(err)
		}
	}
	outsideSessions(t)
	t.Setenv("PI_SESSION_FILE", alpha)
	return alpha, browserServer(t)
}

// browserServer serves the browser's routes of the gateway that fakePiGateway started last over HTTP on loopback.
func browserServer(t *testing.T) string {
	server := httptest.NewServer(browser)
	t.Cleanup(server.Close)
	return server.URL
}

// pcLinkRequest is a request as the desktop app sends it on its link.
func pcLinkRequest(t *testing.T, target string, body any) *http.Request {
	encoded, _ := json.Marshal(body)
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gripi-PC-Link", "1")
	request.Header.Set("Cookie", "gripi_browser="+pcBrowserToken)
	return request
}

// pcLinkSend sends a request of the desktop app's link, as change leaves it. It fails the test with t.Error, so that a
// desktop app in another goroutine may use it.
func pcLinkSend(t *testing.T, address, route string, body any, change ...func(*http.Request)) (int, pcLinkAnswer) {
	request := pcLinkRequest(t, address+route, body)
	for _, apply := range change {
		apply(request)
	}
	var answer pcLinkAnswer
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Error(err)
		return 0, answer
	}
	defer response.Body.Close()
	json.NewDecoder(response.Body).Decode(&answer)
	return response.StatusCode, answer
}

// asBrowser sends a request with the cookie of the approved browser with token.
func asBrowser(token string) func(*http.Request) {
	return func(request *http.Request) { request.Header.Set("Cookie", "gripi_browser="+token) }
}

// pcPoll polls as link, which a poll with open opens and the others resume.
func pcPoll(t *testing.T, address, link string, open bool, change ...func(*http.Request)) (int, pcLinkAnswer) {
	return pcLinkSend(t, address, "/pc/link", map[string]any{"link": link, "open": open}, change...)
}

// pcDesktop plays the laptop's desktop app, as pcDesktopOn does.
func pcDesktop(t *testing.T, address, link string, answer func(pcMessage) map[string]any) (messages <-chan pcMessage, stop func()) {
	t.Helper()
	return pcDesktopOn(t, address, pcBrowserToken, "laptop", link, answer)
}

// pcDesktopOn plays the desktop app of the browser with token on the computer name, which opens link and polls it until
// stop is called or the test ends. It answers each ssh or run message with what answer returns, unless answer is nil,
// and passes on every message. It returns once the link is open.
func pcDesktopOn(t *testing.T, address, token, name, link string, answer func(pcMessage) map[string]any) (messages <-chan pcMessage, stop func()) {
	t.Helper()
	received := make(chan pcMessage, 100)
	poll := func(open bool) bool {
		body := map[string]any{"link": link}
		if open {
			body["open"], body["name"], body["run"], body["root"] = true, name, true, true
		}
		status, polled := pcLinkSend(t, address, "/pc/link", body, asBrowser(token))
		if status != http.StatusOK {
			t.Errorf("poll of %s = %d %s", link, status, polled.Error)
			return false
		}
		for _, message := range polled.Messages {
			received <- message
			if (message.Type == "ssh" || message.Type == "run") && answer != nil {
				reply := answer(message)
				reply["link"], reply["id"] = link, message.ID
				if status, replied := pcLinkSend(t, address, "/pc/link/reply", reply, asBrowser(token)); status != http.StatusOK {
					t.Errorf("reply of %s = %d %s", link, status, replied.Error)
				}
			}
		}
		return true
	}
	if !poll(true) {
		t.FailNow()
	}
	stopped, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stopped:
				return
			default:
			}
			if !poll(false) {
				return
			}
		}
	}()
	stop = sync.OnceFunc(func() {
		close(stopped)
		<-done
	})
	t.Cleanup(stop)
	return received, stop
}

// answeredBy answers each request with an error that names the computer, which gripi pc prints.
func answeredBy(name string) func(pcMessage) map[string]any {
	return func(pcMessage) map[string]any { return map[string]any{"error": name} }
}

var pcPresenceSequence atomic.Uint64

// pcPresence reports what the page of the browser with token shows, as notification_presence.js does: session, looked
// at, and with used, that the user clicked or typed in it.
func pcPresence(t *testing.T, token, session string, used bool) {
	t.Helper()
	presence := map[string]any{"client_id": "page", "session": session, "focused": true, "sequence": pcPresenceSequence.Add(1)}
	if used {
		presence["used"] = true
	}
	body, _ := json.Marshal(presence)
	request := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:4567/web-push/presence", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	asBrowser(token)(request)
	response := httptest.NewRecorder()
	browser.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("presence of %s = %d %s", session, response.Code, response.Body)
	}
}

// pcPrompt sends message to session from the page of the browser with token, as form changes it, and waits until the
// turn ends.
func pcPrompt(t *testing.T, token, session, message string, change ...func(url.Values)) {
	t.Helper()
	form := url.Values{"session": {session}, "message": {message}}
	for _, apply := range change {
		apply(form)
	}
	if response := browserRequest(http.MethodPost, "/prompt", form, "gripi_browser="+token); response.Code != http.StatusOK {
		t.Fatalf("prompt in %s = %d %s", session, response.Code, response.Body)
	}
	eventually(t, session, func(session gateway.LocalSession) bool { return session.State == "idle" })
}

const pcMessageText = "Show the deterministic browser response"

// pcAnswerer runs gripi pc ssh in the session of PI_SESSION_FILE, and returns what it printed: the name of the computer
// whose desktop app answered, from answeredBy, or why the gateway refused.
func pcAnswerer(t *testing.T) string {
	t.Helper()
	result := pcSSH(t, "ssh-add", "-l")
	for line := range strings.Lines(result.stderr) {
		if answer, found := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "gripi pc: "); found {
			return answer
		}
	}
	t.Fatalf("gripi pc ssh = %+v", result)
	return ""
}

// pcRelay is the relay of gripi pc ssh in session, whose messages tests send themselves.
func pcRelay(t *testing.T, session string) *sshAgentRelay {
	t.Helper()
	client, err := newGatewayClient()
	if err != nil {
		t.Fatal(err)
	}
	return &sshAgentRelay{client: client, session: session, stderr: io.Discard, reported: map[string]bool{}}
}

// pcAgentMessage is a message of agent connection to the gateway, as the relay sends it: the first one with open.
func pcAgentMessage(session, connection string, open bool) url.Values {
	form := url.Values{"session": {session}, "connection": {connection}, "message": {"Cw=="}}
	if open {
		form.Set("open", "1")
	}
	return form
}

// nextPCMessage returns the next message of the type that the desktop app got, and skips the others.
func nextPCMessage(t *testing.T, messages <-chan pcMessage, kind string) pcMessage {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case message := <-messages:
			if message.Type == kind {
				return message
			}
		case <-timeout:
			t.Fatalf("the desktop app got no %s message", kind)
		}
	}
}

// startPCSSH runs gripi pc ssh -- command in the background, as startCLI does, but with files for its output, as in a
// terminal. The command writes to them itself, while gripi prints why the agent fails.
func startPCSSH(t *testing.T, command ...string) <-chan cliResult {
	t.Helper()
	var outputs [2]*os.File
	for index := range outputs {
		file, err := os.CreateTemp(t.TempDir(), "output")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { file.Close() })
		outputs[index] = file
	}
	result := make(chan cliResult, 1)
	go func() {
		code := run(append([]string{"pc", "ssh", "--"}, command...), nil, outputs[0], outputs[1])
		stdout, _ := os.ReadFile(outputs[0].Name())
		stderr, _ := os.ReadFile(outputs[1].Name())
		result <- cliResult{code, string(stdout), string(stderr)}
	}()
	return result
}

func pcSSH(t *testing.T, command ...string) cliResult {
	t.Helper()
	return <-startPCSSH(t, command...)
}

func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is missing", tool)
		}
	}
}

// sshAgentWithKey starts an ssh-agent with a new key, and returns its socket and the key as ssh-add -l lists it.
func sshAgentWithKey(t *testing.T) (socket, listed string) {
	t.Helper()
	requireTools(t, "ssh-agent", "ssh-keygen", "ssh-add")
	directory, err := os.MkdirTemp("", "gripi-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	socket, key := filepath.Join(directory, "agent.sock"), filepath.Join(directory, "key")
	agent := exec.Command("ssh-agent", "-D", "-a", socket)
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		agent.Process.Kill()
		agent.Wait()
	})
	run := func(name string, arguments ...string) string {
		t.Helper()
		command := exec.Command(name, arguments...)
		command.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %q: %v\n%s", name, arguments, err, output)
		}
		return string(output)
	}
	run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "pc-test-key", "-f", key)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ssh-agent never listened")
		}
	}
	run("ssh-add", "-q", key)
	return socket, run("ssh-keygen", "-l", "-f", key+".pub")
}

// forwardToAgent answers each message with the reply of the SSH agent at socket.
func forwardToAgent(t *testing.T, socket string) func(pcMessage) map[string]any {
	return func(message pcMessage) map[string]any {
		connection, err := net.Dial("unix", socket)
		if err != nil {
			t.Error(err)
			return map[string]any{"error": err.Error()}
		}
		defer connection.Close()
		connection.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(message.Message))), message.Message...))
		reply, err := readAgentMessage(connection)
		if err != nil {
			t.Error(err)
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"message": reply}
	}
}

func TestPCSSHUsesTheSSHAgentOfTheDesktopAppsComputer(t *testing.T) {
	agentSocket, listed := sshAgentWithKey(t)
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", forwardToAgent(t, agentSocket))
	pcPresence(t, pcBrowserToken, alpha, true)

	if result := pcSSH(t, "ssh-add", "-l"); result.code != 0 || result.stdout != listed || result.stderr != "" {
		t.Fatalf("gripi pc ssh -- ssh-add -l = %+v, want stdout %q", result, listed)
	}
	request := nextPCMessage(t, messages, "ssh")
	if _, err := hex.DecodeString(request.Connection); len(request.ID) != 64 || request.Connection == "" || err != nil || request.Session != alpha || request.SessionName != "Prompt for 0a1-alpha" || !bytes.Equal(request.Message, []byte{11}) {
		t.Fatalf("the desktop app got %+v", request)
	}
	if closed := nextPCMessage(t, messages, "ssh_close"); !reflect.DeepEqual(closed, pcMessage{Type: "ssh_close", Session: alpha, Connection: request.Connection}) {
		t.Fatalf("the desktop app got %+v after the request %+v", closed, request)
	}
}

func TestPCSSHPrintsTheDesktopAppsRefusalOnceAndFailsTheAgentRequest(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	pcDesktop(t, address, "app", answeredBy("the user denied it"))
	pcPresence(t, pcBrowserToken, alpha, true)

	result := pcSSH(t, "sh", "-c", "ssh-add -l; ssh-add -l")
	if result.code != 1 || result.stdout != "" || strings.Count(result.stderr, "gripi pc: the user denied it\n") != 1 || strings.Count(result.stderr, "agent refused operation") != 2 {
		t.Fatalf("gripi pc ssh = %+v", result)
	}
}

func TestPCSSHFailsAtOnceWithoutALinkOrASession(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, _ := pcGateway(t)
	for _, usage := range [][]string{{"pc"}, {"pc", "exec", "--", "true"}, {"pc", "ssh"}, {"pc", "ssh", "--"}, {"pc", "ssh", "ssh-add", "-l"}} {
		if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help pc") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
		}
	}

	started := time.Now()
	if result := pcSSH(t, "ssh-add", "-l"); result.code != 1 || !strings.HasPrefix(result.stderr, "gripi pc: no computer is connected") || time.Since(started) > 5*time.Second {
		t.Fatalf("gripi pc ssh without a link = %+v after %s", result, time.Since(started))
	}
	if code, stdout, stderr := runCLI("pc", "run", "--", "true"); code != 1 || stdout != "" || !strings.HasPrefix(stderr, "gripi pc: the gateway answered 503: no computer is connected") {
		t.Fatalf("gripi pc run without a link = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	t.Setenv("PI_SESSION_FILE", alpha+".missing")
	if result := pcSSH(t, "ssh-add", "-l"); result.code != 1 || !strings.HasPrefix(result.stderr, "gripi pc: the command runs in no session of yours") {
		t.Fatalf("gripi pc ssh in an unknown session = %+v", result)
	}
	if code, _, stderr := runCLI("pc", "run", "--", "true"); code != 1 || !strings.HasPrefix(stderr, "gripi pc: the gateway answered 404: the command runs in no session of yours") {
		t.Fatalf("gripi pc run in an unknown session = %d, stderr %q", code, stderr)
	}
	outsideSessions(t)
	for _, command := range [][]string{{"pc", "ssh", "--", "true"}, {"pc", "run", "--", "true"}} {
		if code, _, stderr := runCLI(command...); code != 1 || !strings.Contains(stderr, "PI_SESSION_FILE") {
			t.Fatalf("gripi %q outside a session = %d, stderr %q", command, code, stderr)
		}
	}
}

// A link keeps others out while it polls, and its requests and replies never reach another link.
func TestPCSSHFailsWhenTheLinkEndsOrIsReplaced(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	const lost = "gripi pc: the computer's link to Gripi ended before it replied\n"
	messages, stop := pcDesktop(t, address, "first", nil)
	pcPresence(t, pcBrowserToken, alpha, true)
	if status, _ := pcPoll(t, address, "second", true); status != http.StatusConflict {
		t.Fatalf("a second link while the first polls = %d", status)
	}

	result := startPCSSH(t, "ssh-add", "-l")
	nextPCMessage(t, messages, "ssh")
	stop()
	if status, _ := pcLinkSend(t, address, "/pc/link/close", map[string]string{"link": "first"}); status != http.StatusOK {
		t.Fatalf("close = %d", status)
	}
	if answered := <-result; answered.code != 1 || !strings.Contains(answered.stderr, lost) {
		t.Fatalf("gripi pc ssh when the link closed = %+v", answered)
	}
	if status, polled := pcPoll(t, address, "second", true); status != http.StatusOK || len(polled.Messages) != 0 {
		t.Fatalf("the second link after the first closed = %d %+v", status, polled)
	}

	messages, stop = pcDesktop(t, address, "second", nil)
	result = startPCSSH(t, "ssh-add", "-l")
	request := nextPCMessage(t, messages, "ssh")
	stop()
	if answered := <-result; answered.code != 1 || !strings.Contains(answered.stderr, lost) {
		t.Fatalf("gripi pc ssh when the link stopped polling = %+v", answered)
	}
	if status, _ := pcPoll(t, address, "second", false); status != http.StatusConflict {
		t.Fatalf("a poll of the link that stopped polling = %d", status)
	}
	if status, polled := pcPoll(t, address, "first", true); status != http.StatusOK || len(polled.Messages) != 0 {
		t.Fatalf("a new link after the second stopped polling = %d %+v", status, polled)
	}
	reply := map[string]any{"link": "second", "id": request.ID, "message": []byte{12}}
	if status, _ := pcLinkSend(t, address, "/pc/link/reply", reply); status != http.StatusConflict {
		t.Fatalf("the reply of the replaced link = %d", status)
	}
}

func TestPCSSHCancelsTheRequestOfAnAgentConnectionThatEnds(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", nil)
	pcPresence(t, pcBrowserToken, alpha, true)

	result := startPCSSH(t, "sh", "-c", "ssh-add -l & sleep 0.5; kill $!")
	request := nextPCMessage(t, messages, "ssh")
	if cancel := nextPCMessage(t, messages, "cancel"); !reflect.DeepEqual(cancel, pcMessage{Type: "cancel", ID: request.ID}) {
		t.Fatalf("the desktop app got %+v after the request %+v", cancel, request)
	}
	if answered := <-result; answered.code != 0 || strings.Contains(answered.stderr, "gripi pc:") {
		t.Fatalf("gripi pc ssh = %+v", answered)
	}
	reply := map[string]any{"link": "app", "id": request.ID, "message": []byte{12}}
	if status, _ := pcLinkSend(t, address, "/pc/link/reply", reply); status != http.StatusNotFound {
		t.Fatalf("the reply to a cancelled request = %d", status)
	}
}

// Processes that the command leaves running, such as ssh's ControlMaster, may keep their agent connections open.
func TestPCSSHClosesTheAgentConnectionsThatTheCommandLeavesOpen(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", nil)
	pcPresence(t, pcBrowserToken, alpha, true)
	proceed := filepath.Join(t.TempDir(), "proceed")

	result := startPCSSH(t, "sh", "-c", `ssh-add -l & ssh-add -l & until [ -e "$0" ]; do sleep 0.05; done`, proceed)
	open := map[string]bool{}
	for range 2 {
		open[nextPCMessage(t, messages, "ssh").Connection] = true
	}
	if err := os.WriteFile(proceed, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ended := time.Now()
	if answered := <-result; answered.code != 0 || strings.Contains(answered.stderr, "gripi pc:") || time.Since(ended) > 4*time.Second {
		t.Fatalf("gripi pc ssh = %+v after %s", answered, time.Since(ended))
	}
	for range 2 {
		closed := nextPCMessage(t, messages, "ssh_close")
		if !open[closed.Connection] {
			t.Fatalf("the desktop app got %+v, want a close of %v", closed, open)
		}
		delete(open, closed.Connection)
	}
}

// A client of the agent waits for each reply, so a message before it breaks the protocol.
func TestPCSSHEndsAnAgentConnectionThatSendsAMessageBeforeTheReply(t *testing.T) {
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", nil)
	pcPresence(t, pcBrowserToken, alpha, true)
	relay := pcRelay(t, alpha)
	agent, connection := net.Pipe()
	relay.connections.Add(1)
	go relay.serveConnection(context.Background(), connection)

	agent.Write([]byte{0, 0, 0, 1, 11})
	request := nextPCMessage(t, messages, "ssh")
	agent.Write([]byte{0, 0, 0, 1, 11})
	agent.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := agent.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read of the connection after a second message = %v", err)
	}
	// The cancel and the close come from separate requests, in either order.
	ended := map[string]pcMessage{}
	for range 2 {
		select {
		case message := <-messages:
			ended[message.Type] = message
		case <-time.After(5 * time.Second):
			t.Fatalf("the desktop app got only %+v after the request %+v", ended, request)
		}
	}
	want := map[string]pcMessage{
		"cancel":    {Type: "cancel", ID: request.ID},
		"ssh_close": {Type: "ssh_close", Session: request.Session, Connection: request.Connection},
	}
	if !reflect.DeepEqual(ended, want) {
		t.Fatalf("the desktop app got %+v after the request %+v", ended, request)
	}
}

// The gateway ends the link as it stops, so that neither the desktop app's poll nor the requests that wait for its
// replies hold the shutdown.
func TestEndingThePCLinkWakesItsPollAndRequestsAndRefusesNewPolls(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	messages, stop := pcDesktop(t, address, "app", nil)
	pcPresence(t, pcBrowserToken, alpha, true)
	result := startPCSSH(t, "ssh-add", "-l")
	nextPCMessage(t, messages, "ssh")
	stop()
	gateway.PCLinkPoll = time.Minute
	polled := make(chan int, 1)
	go func() {
		status, _ := pcPoll(t, address, "app", false)
		polled <- status
	}()
	time.Sleep(100 * time.Millisecond)

	browser.(interface{ EndPCLink() }).EndPCLink()
	timeout := time.After(5 * time.Second)
	select {
	case status := <-polled:
		if status != http.StatusServiceUnavailable {
			t.Fatalf("the open poll = %d", status)
		}
	case <-timeout:
		t.Fatal("the open poll still waits")
	}
	select {
	case answered := <-result:
		if answered.code != 1 || !strings.Contains(answered.stderr, "gripi pc: the computer's link to Gripi ended before it replied\n") {
			t.Fatalf("gripi pc ssh = %+v", answered)
		}
	case <-timeout:
		t.Fatal("gripi pc ssh still waits")
	}
	if status, _ := pcPoll(t, address, "app", true); status != http.StatusServiceUnavailable {
		t.Fatalf("a poll after the end = %d", status)
	}
}

// A poll that doesn't open its link resumes only the current one, so that the desktop app learns when the gateway
// forgot its link, such as in a restart, and drops what it allowed on it.
func TestPCLinkResumesOnlyTheCurrentLink(t *testing.T) {
	_, address := pcGateway(t)
	for _, poll := range []struct {
		open   bool
		status int
	}{{false, http.StatusConflict}, {true, http.StatusOK}, {false, http.StatusOK}} {
		if status, polled := pcPoll(t, address, "app", poll.open); status != poll.status {
			t.Fatalf("poll with open %v = %d %+v, want %d", poll.open, status, polled, poll.status)
		}
	}
	if status, _ := pcLinkSend(t, address, "/pc/link/close", map[string]string{"link": "app"}); status != http.StatusOK {
		t.Fatalf("close = %d", status)
	}
	if status, _ := pcPoll(t, address, "app", false); status != http.StatusConflict {
		t.Fatalf("a poll of the closed link = %d", status)
	}
}

// No page can play the link: pages send an Origin or Sec-Fetch-* header with every POST, and can't send the link's
// header across origins.
func TestPCLinkRefusesRequestsThatPagesCouldSend(t *testing.T) {
	_, address := pcGateway(t)
	changes := map[string]func(*http.Request){
		"the gateway's own Origin": func(request *http.Request) { request.Header.Set("Origin", address) },
		"Sec-Fetch-Site":           func(request *http.Request) { request.Header.Set("Sec-Fetch-Site", "none") },
		"no X-Gripi-PC-Link":       func(request *http.Request) { request.Header.Del("X-Gripi-PC-Link") },
		"a form":                   func(request *http.Request) { request.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
	}
	for name, change := range changes {
		for _, route := range []string{"/pc/link", "/pc/link/reply", "/pc/link/close"} {
			if status, _ := pcLinkSend(t, address, route, map[string]string{"link": "page", "id": "request", "error": "denied"}, change); status != http.StatusForbidden {
				t.Fatalf("%s with %s = %d", route, name, status)
			}
		}
	}
	if status, _ := pcPoll(t, address, "app", true); status != http.StatusOK {
		t.Fatalf("the desktop app's poll = %d", status)
	}
}

func TestPCAccessIsOffWithoutAccessApprovalAndInMultiUserMode(t *testing.T) {
	requireTools(t, "ssh-add")
	refuses := func(t *testing.T, refusal string, change ...func(*http.Request)) {
		t.Helper()
		status, polled := pcLinkSend(t, browserServer(t), "/pc/link", map[string]string{"link": "app"}, change...)
		if status != http.StatusForbidden || !strings.HasPrefix(polled.Error, refusal) {
			t.Fatalf("poll = %d %+v", status, polled)
		}
		if result := pcSSH(t, "ssh-add", "-l"); result.code != 1 || !strings.HasPrefix(result.stderr, "gripi pc: "+refusal) {
			t.Fatalf("gripi pc ssh = %+v", result)
		}
	}
	t.Run("access approval disabled", func(t *testing.T) {
		alpha, _ := fakePiGateway(t)
		outsideSessions(t)
		t.Setenv("PI_SESSION_FILE", alpha)
		refuses(t, "PC access needs access approval")
	})
	t.Run("multi-user mode", func(t *testing.T) {
		alpha, _ := multiUserGateway(t)
		outsideSessions(t)
		t.Setenv("PI_SESSION_FILE", alpha)
		t.Setenv("GRIPI_USER_TOKEN", aliceToken)
		refuses(t, "PC access is off in multi-user mode", func(request *http.Request) { request.Header.Set("Cookie", workspaceCookies[aliceToken]) })
	})
}

// An attacker on the way of plain HTTP could slip a request into a poll, so only HTTPS or loopback without a proxy
// passes, even where the gateway allows remote HTTP.
func TestPCLinkNeedsHTTPSFromAnywhereButThisMachine(t *testing.T) {
	pcGateway(t, func(cfg *config.Config) { cfg.AllowInsecureRemoteHTTP, cfg.TrustProxyHeaders = true, true })
	for _, peer := range []struct {
		remote, forwardedProto string
		status                 int
	}{
		{"192.0.2.1:40000", "", http.StatusForbidden},
		{"127.0.0.1:40000", "http", http.StatusForbidden},
		{"127.0.0.1:40000", "https", http.StatusOK},
	} {
		request := pcLinkRequest(t, "http://127.0.0.1:4567/pc/link", map[string]any{"link": "app", "open": true})
		request.RemoteAddr = peer.remote
		if peer.forwardedProto != "" {
			request.Header.Set("X-Forwarded-Proto", peer.forwardedProto)
		}
		response := httptest.NewRecorder()
		browser.ServeHTTP(response, request)
		if response.Code != peer.status {
			t.Fatalf("poll from %s with X-Forwarded-Proto %q = %d %s", peer.remote, peer.forwardedProto, response.Code, response.Body)
		}
	}
}

// Each computer's desktop app has a link of its own, and gripi pc goes to the one where the user last sent a message
// in the session, or clicked or typed in it.
func TestPCSSHGoesToTheComputerWhereTheSessionWasLastUsed(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	pcDesktop(t, address, "laptop", answeredBy("laptop"))
	pcDesktopOn(t, address, pcDeskToken, "desk", "desk", answeredBy("desk"))

	pcPrompt(t, pcBrowserToken, alpha, pcMessageText)
	if answerer := pcAnswerer(t); answerer != "laptop" {
		t.Fatalf("after a message from the laptop, gripi pc ssh reached %q", answerer)
	}
	pcPresence(t, pcDeskToken, alpha, true)
	if answerer := pcAnswerer(t); answerer != "desk" {
		t.Fatalf("after a click on the desk, gripi pc ssh reached %q", answerer)
	}
	// Heartbeats look the same as focus reports.
	pcPresence(t, pcBrowserToken, alpha, false)
	if code, _, stderr := runCLI("send", alpha, "Show the deterministic browser response"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	eventually(t, alpha, func(session gateway.LocalSession) bool { return session.State == "idle" })
	if answerer := pcAnswerer(t); answerer != "desk" {
		t.Fatalf("after a focus report from the laptop and gripi send, gripi pc ssh reached %q", answerer)
	}
}

// A request never goes to another computer than the one where the session was last used.
func TestPCSSHRefusesASessionLastUsedWhereNoDesktopAppIsConnected(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	_, stop := pcDesktopOn(t, address, pcDeskToken, "desk", "desk", answeredBy("desk"))
	pcPresence(t, pcDeskToken, alpha, true)

	pcPrompt(t, pcPhoneToken, alpha, pcMessageText)
	if answerer := pcAnswerer(t); !strings.HasPrefix(answerer, "this session was last used in a Gripi window without PC access, such as a phone or a browser; ") {
		t.Fatalf("after a message from the phone, gripi pc ssh = %q", answerer)
	}
	pcPresence(t, pcDeskToken, alpha, true)
	stop()
	if status, _ := pcLinkSend(t, address, "/pc/link/close", map[string]string{"link": "desk"}, asBrowser(pcDeskToken)); status != http.StatusOK {
		t.Fatalf("close = %d", status)
	}
	if answerer := pcAnswerer(t); !strings.HasPrefix(answerer, "this session was last used on desk, where the Gripi desktop app isn't connected for PC access; ") {
		t.Fatalf("after the desk's link ended, gripi pc ssh = %q", answerer)
	}
}

// A sub-session goes to the computer of its parent until it is used itself.
func TestASubSessionUsesItsParentsComputerUntilItIsUsedItself(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	beta := filepath.Join(filepath.Dir(alpha), "beta.jsonl")
	if code, _, stderr := runCLI("link", beta, alpha); code != 0 {
		t.Fatalf("gripi link = %d, stderr %q", code, stderr)
	}
	pcDesktop(t, address, "laptop", answeredBy("laptop"))
	pcDesktopOn(t, address, pcDeskToken, "desk", "desk", answeredBy("desk"))
	answerers := func() [2]string {
		t.Setenv("PI_SESSION_FILE", alpha)
		parent := pcAnswerer(t)
		t.Setenv("PI_SESSION_FILE", beta)
		return [2]string{parent, pcAnswerer(t)}
	}

	pcPresence(t, pcBrowserToken, alpha, true)
	if got := answerers(); got != [2]string{"laptop", "laptop"} {
		t.Fatalf("after a click in the parent on the laptop, gripi pc ssh in the parent and the sub-session reached %q", got)
	}
	pcPresence(t, pcDeskToken, beta, true)
	if got := answerers(); got != [2]string{"laptop", "desk"} {
		t.Fatalf("after a click in the sub-session on the desk, gripi pc ssh in the parent and the sub-session reached %q", got)
	}
	pcPrompt(t, pcPhoneToken, beta, pcMessageText)
	if got := answerers(); got[0] != "laptop" || !strings.HasPrefix(got[1], "this session was last used in a Gripi window without PC access") {
		t.Fatalf("after a message to the sub-session from the phone, gripi pc ssh in the parent and the sub-session reached %q", got)
	}
}

// The desktop app checks each agent connection on its own, from its first message, so the connection stays with the
// computer that got that one.
func TestAnAgentConnectionStaysWithTheComputerOfItsFirstMessage(t *testing.T) {
	alpha, address := pcGateway(t)
	laptop, _ := pcDesktop(t, address, "laptop", answeredBy("laptop"))
	desk, _ := pcDesktopOn(t, address, pcDeskToken, "desk", "desk", answeredBy("desk"))
	relay := pcRelay(t, alpha)
	beta := filepath.Join(filepath.Dir(alpha), "beta.jsonl")
	pcPresence(t, pcBrowserToken, alpha, true)
	pcPresence(t, pcBrowserToken, beta, true)

	if _, err := relay.post(context.Background(), pcAgentMessage(alpha, "connection", true)); err == nil || err.Error() != "laptop" {
		t.Fatalf("the first message = %v", err)
	}
	if _, err := relay.post(context.Background(), pcAgentMessage(alpha, "connection", true)); err == nil || err.Error() != "this SSH agent connection is already open" {
		t.Fatalf("a second first message = %v", err)
	}
	pcPresence(t, pcDeskToken, alpha, true)
	// Also in another session's name, which still goes to the laptop.
	for _, session := range []string{alpha, beta} {
		if _, err := relay.post(context.Background(), pcAgentMessage(session, "connection", false)); err == nil || !strings.HasPrefix(err.Error(), "the session was used on another computer during this SSH agent connection") {
			t.Fatalf("a message in %s after a click on the desk = %v", session, err)
		}
	}
	pcPrompt(t, pcPhoneToken, alpha, pcMessageText)
	if _, err := relay.post(context.Background(), pcAgentMessage(alpha, "connection", false)); err == nil || !strings.HasPrefix(err.Error(), "this session was last used in a Gripi window without PC access") {
		t.Fatalf("a message after a message from the phone = %v", err)
	}
	if _, err := relay.post(context.Background(), url.Values{"session": {alpha}, "connection": {"connection"}, "close": {"1"}}); err != nil {
		t.Fatal(err)
	}
	if closed := nextPCMessage(t, laptop, "ssh_close"); !reflect.DeepEqual(closed, pcMessage{Type: "ssh_close", Session: alpha, Connection: "connection"}) {
		t.Fatalf("the laptop got %+v", closed)
	}
	select {
	case message := <-desk:
		t.Fatalf("the desk got %+v", message)
	case <-time.After(3 * gateway.PCLinkPoll):
	}
}

// An agent connection whose link ended fails, rather than starting again on another link, also once the gateway
// dropped the connection to keep others.
func TestAnAgentConnectionOfAnEndedLinkNeverMovesToAnother(t *testing.T) {
	alpha, address := pcGateway(t)
	gateway.PCPins = 2
	_, stop := pcDesktop(t, address, "first", answeredBy("first"))
	pcPresence(t, pcBrowserToken, alpha, true)
	relay := pcRelay(t, alpha)
	if _, err := relay.post(context.Background(), pcAgentMessage(alpha, "early", true)); err == nil || err.Error() != "first" {
		t.Fatalf("the first message = %v", err)
	}
	stop()
	if status, _ := pcLinkSend(t, address, "/pc/link/close", map[string]string{"link": "first"}); status != http.StatusOK {
		t.Fatalf("close = %d", status)
	}
	messages, _ := pcDesktop(t, address, "second", answeredBy("second"))

	if _, err := relay.post(context.Background(), pcAgentMessage(alpha, "early", false)); err == nil || err.Error() != "the computer's link to Gripi ended before it replied" {
		t.Fatalf("a message after the link ended = %v", err)
	}
	if _, err := relay.post(context.Background(), pcAgentMessage(alpha, "early", true)); err == nil || err.Error() != "this SSH agent connection is already open" {
		t.Fatalf("a first message again after the link ended = %v", err)
	}
	for _, connection := range []string{"later", "latest"} {
		if _, err := relay.post(context.Background(), pcAgentMessage(alpha, connection, true)); err == nil || err.Error() != "second" {
			t.Fatalf("the first message of %s = %v", connection, err)
		}
	}
	if _, err := relay.post(context.Background(), pcAgentMessage(alpha, "early", false)); err == nil || !strings.HasPrefix(err.Error(), "this SSH agent connection didn't start on the computer") {
		t.Fatalf("a message after the gateway dropped the connection = %v", err)
	}
	for range 2 {
		if request := nextPCMessage(t, messages, "ssh"); request.Connection == "early" {
			t.Fatalf("the second link got %+v", request)
		}
	}
	// Open connections may still wait for replies, so without any of ended links to drop, a new one is refused.
	if _, err := relay.post(context.Background(), pcAgentMessage(alpha, "last", true)); err == nil || err.Error() != "too many SSH agent connections are open for PC access" {
		t.Fatalf("the first message of a connection beyond the limit = %v", err)
	}
}

// A message counts from when the gateway began to receive it, before anything can refuse it, and a shell command too. The
// page retries a message that the gateway couldn't take yet, which the first attempt counted, so a retry that comes late
// doesn't take the session back from a newer use elsewhere.
func TestAMessageCountsFromWhenTheGatewayGetsItAndARetryDoesNot(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	pcDesktop(t, address, "laptop", answeredBy("laptop"))
	const phone = "this session was last used in a Gripi window without PC access"

	pcPrompt(t, pcPhoneToken, alpha, "!true")
	if answerer := pcAnswerer(t); !strings.HasPrefix(answerer, phone) {
		t.Fatalf("after a shell command from the phone, gripi pc ssh = %q", answerer)
	}
	pcPresence(t, pcBrowserToken, alpha, true)
	pcPrompt(t, pcPhoneToken, alpha, pcMessageText, func(form url.Values) { form.Set("retry", "1") })
	if answerer := pcAnswerer(t); answerer != "laptop" {
		t.Fatalf("after a retry from the phone, gripi pc ssh reached %q", answerer)
	}
	refused := url.Values{"session": {alpha}, "message": {pcMessageText}, "streaming_behavior": {"later"}}
	if response := browserRequest(http.MethodPost, "/prompt", refused, "gripi_browser="+pcPhoneToken); response.Code != http.StatusBadRequest {
		t.Fatalf("a message that the gateway refuses = %d %s", response.Code, response.Body)
	}
	if answerer := pcAnswerer(t); !strings.HasPrefix(answerer, phone) {
		t.Fatalf("after a message from the phone that the gateway refused, gripi pc ssh = %q", answerer)
	}

	// A click on the laptop while the phone's message still uploads is the later use.
	uploaded := url.Values{"session": {alpha}, "message": {pcMessageText}}.Encode()
	body, upload := io.Pipe()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4567/prompt", body)
	request.ContentLength = int64(len(uploaded))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	asBrowser(pcPhoneToken)(request)
	sent := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		browser.ServeHTTP(response, request)
		sent <- response
	}()
	time.Sleep(100 * time.Millisecond)
	pcPresence(t, pcBrowserToken, alpha, true)
	upload.Write([]byte(uploaded))
	upload.Close()
	if response := <-sent; response.Code != http.StatusOK {
		t.Fatalf("the uploaded message = %d %s", response.Code, response.Body)
	}
	eventually(t, alpha, func(session gateway.LocalSession) bool { return session.State == "idle" })
	if answerer := pcAnswerer(t); answerer != "laptop" {
		t.Fatalf("after a click on the laptop during the phone's upload, gripi pc ssh reached %q", answerer)
	}
}

// Another link of the same browser, such as a copied profile, waits until the first ends. A link belongs to the browser
// that opened it, so a browser whose cookie changed, such as after the admin password was entered again there, opens a
// new one.
func TestEachBrowserHasOneLinkThatOnlyItUses(t *testing.T) {
	_, address := pcGateway(t)
	desk := asBrowser(pcDeskToken)
	if status, _ := pcPoll(t, address, "first", true); status != http.StatusOK {
		t.Fatalf("the first link = %d", status)
	}
	if status, _ := pcPoll(t, address, "second", true); status != http.StatusConflict {
		t.Fatalf("a second link of the same browser = %d", status)
	}
	if status, _ := pcPoll(t, address, "desk", true, desk); status != http.StatusOK {
		t.Fatalf("a link of another browser = %d", status)
	}

	if status, _ := pcPoll(t, address, "first", false, desk); status != http.StatusConflict {
		t.Fatalf("a poll of another browser's link = %d", status)
	}
	if status, _ := pcPoll(t, address, "first", true, desk); status != http.StatusConflict {
		t.Fatalf("an open poll of another browser's link = %d", status)
	}
	if status, _ := pcLinkSend(t, address, "/pc/link/reply", map[string]string{"link": "first", "id": "request", "error": "denied"}, desk); status != http.StatusConflict {
		t.Fatalf("a reply over another browser's link = %d", status)
	}
	pcLinkSend(t, address, "/pc/link/close", map[string]string{"link": "first"}, desk)
	if status, _ := pcPoll(t, address, "first", false); status != http.StatusOK {
		t.Fatalf("a poll after another browser closed the link = %d", status)
	}

	if status, _ := pcLinkSend(t, address, "/pc/link/close", map[string]string{"link": "first"}); status != http.StatusOK {
		t.Fatalf("close = %d", status)
	}
	if status, _ := pcPoll(t, address, "second", true); status != http.StatusOK {
		t.Fatalf("a new link after the first closed = %d", status)
	}
}

// Entering the admin password again gives the browser a new cookie, and ends the links that it opened with the old one,
// rather than leaving them for 35 s with the requests that wait for them.
func TestEnteringTheAdminPasswordAgainEndsTheBrowsersLink(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	gateway.PCLinkStale = time.Minute
	messages, stop := pcDesktop(t, address, "app", nil)
	pcPresence(t, pcBrowserToken, alpha, true)
	result := startPCSSH(t, "ssh-add", "-l")
	nextPCMessage(t, messages, "ssh")
	stop()

	if response := browserRequest(http.MethodPost, "/browser-access/admin-login", url.Values{"password": {"secret"}}, "gripi_browser="+pcBrowserToken); response.Code != http.StatusSeeOther {
		t.Fatalf("admin login = %d %s", response.Code, response.Body)
	}
	select {
	case answered := <-result:
		if answered.code != 1 || !strings.Contains(answered.stderr, "gripi pc: the computer's link to Gripi ended before it replied\n") {
			t.Fatalf("gripi pc ssh = %+v", answered)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gripi pc ssh still waits")
	}
}

// Reading on a phone, or in a browser without PC access, moves nothing, and nor does a session that the gateway doesn't
// know.
func TestOnlyAUseOfAKnownSessionWithPCAccessCounts(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	pcDesktop(t, address, "laptop", answeredBy("laptop"))
	pcPresence(t, pcBrowserToken, alpha, true)

	pcPresence(t, pcPhoneToken, alpha, true)
	if answerer := pcAnswerer(t); answerer != "laptop" {
		t.Fatalf("after a click on the phone, gripi pc ssh reached %q", answerer)
	}

	gamma := filepath.Join(filepath.Dir(alpha), "gamma.jsonl")
	pcPresence(t, pcBrowserToken, gamma, true)
	appendSessionRecords(t, gamma, map[string]any{"type": "session", "version": 3, "id": "0a1-gamma", "timestamp": "2026-01-03T00:00:00Z", "cwd": t.TempDir()})
	eventually(t, gamma, func(gateway.LocalSession) bool { return true })
	t.Setenv("PI_SESSION_FILE", gamma)
	if answerer := pcAnswerer(t); !strings.HasPrefix(answerer, "nobody has used this session in a Gripi desktop app with PC access since the gateway started; ") {
		t.Fatalf("in a session used before the gateway knew it, gripi pc ssh = %q", answerer)
	}
}

// The gateway keeps no uses over a restart, and a message that gripi sends is none.
func TestPCSSHNeedsAUseSinceTheGatewayStarted(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, address := pcGateway(t)
	pcDesktop(t, address, "laptop", answeredBy("laptop"))
	const unused = "nobody has used this session in a Gripi desktop app with PC access since the gateway started; ask the user to open it in the desktop app on the computer that should do this, and click in it"

	if answerer := pcAnswerer(t); answerer != unused {
		t.Fatalf("with a link, but no use, gripi pc ssh = %q", answerer)
	}
	if code, _, stderr := runCLI("send", alpha, "Show the deterministic browser response"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	eventually(t, alpha, func(session gateway.LocalSession) bool { return session.State == "idle" })
	if answerer := pcAnswerer(t); answerer != unused {
		t.Fatalf("after gripi send, gripi pc ssh = %q", answerer)
	}
}

// pcAsk sends the first message of an agent connection in session, as gripi pc ssh does, until cancel is called or
// the test ends, and returns the request that the desktop app got from messages, and what the message got.
func pcAsk(t *testing.T, session, connection string, messages <-chan pcMessage) (request pcMessage, answered <-chan error, cancel func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errors := make(chan error, 1)
	go func() {
		_, err := pcRelay(t, session).post(ctx, pcAgentMessage(session, connection, true))
		errors <- err
	}()
	return nextPCMessage(t, messages, "ssh"), errors, cancel
}

// pcAsking tells the gateway, as the desktop app of the browser with token does over link, that it asks the user to
// approve the request id, or with summary "", that it stopped asking.
func pcAsking(t *testing.T, address, token, link, id, summary string) (int, pcLinkAnswer) {
	body := map[string]any{"link": link, "id": id, "asking": summary != ""}
	if summary != "" {
		body["summary"], body["expires_in"] = summary, 120
	}
	return pcLinkSend(t, address, "/pc/link/status", body, asBrowser(token))
}

// pcApprovals returns the requests that computers ask the user to approve in session, as the phone's live events show
// them.
func pcApprovals(t *testing.T, session string) []map[string]any {
	t.Helper()
	response := browserRequest(http.MethodGet, "/events?session="+url.QueryEscape(session), nil, "gripi_browser="+pcPhoneToken)
	var payload struct {
		Approvals []map[string]any `json:"pc_approvals"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); response.Code != http.StatusOK || err != nil || payload.Approvals == nil {
		t.Fatalf("events of %s = %d %s (%v)", session, response.Code, response.Body, err)
	}
	return payload.Approvals
}

var renderedPCApprovals = regexp.MustCompile(`data-pc-approvals="([^"]*)"`)

// While a computer asks the user to approve a request, every Gripi client of the session shows it, from the page and
// its live events, with the computer's name and the desktop app's summary, escaped and cut, until the app stops asking.
// Then it can't ask again.
func TestASessionShowsTheRequestThatAComputerAsksToApprove(t *testing.T) {
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", nil)
	pcPresence(t, pcBrowserToken, alpha, true)
	request, _, _ := pcAsk(t, alpha, "connection", messages)

	started := time.Now()
	status := map[string]any{"link": "app", "id": request.ID, "asking": true, "summary": "SSH login to github.com as git\u202e\n" + strings.Repeat("x", 300), "expires_in": 3600}
	if status, answer := pcLinkSend(t, address, "/pc/link/status", status); status != http.StatusOK {
		t.Fatalf("status = %d %+v", status, answer)
	}
	approvals := pcApprovals(t, alpha)
	summary := `SSH login to github.com as git\u202e\u000a` + strings.Repeat("x", 157) + "…"
	if len(approvals) != 1 || approvals[0]["id"] != request.ID || approvals[0]["computer"] != "laptop" || approvals[0]["summary"] != summary {
		t.Fatalf("approvals = %+v", approvals)
	}
	if expires, err := time.Parse(time.RFC3339Nano, approvals[0]["expires_at"].(string)); err != nil || expires.Before(started.Add(gateway.PCReplyTimeout)) || expires.After(time.Now().Add(gateway.PCReplyTimeout)) {
		t.Fatalf("an approval for an hour expires at %v (%v)", approvals[0]["expires_at"], err)
	}
	page := browserRequest(http.MethodGet, "/?session="+url.QueryEscape(alpha), nil, "gripi_browser="+pcPhoneToken)
	if match := renderedPCApprovals.FindStringSubmatch(page.Body.String()); page.Code != http.StatusOK || match == nil || !strings.Contains(html.UnescapeString(match[1]), `"id":"`+request.ID+`"`) {
		t.Fatalf("the page = %d, approvals %q", page.Code, match)
	}
	if others := pcApprovals(t, filepath.Join(filepath.Dir(alpha), "beta.jsonl")); len(others) != 0 {
		t.Fatalf("another session shows %+v", others)
	}

	// The stop may also overtake the start.
	overtaken, _, _ := pcAsk(t, alpha, "other", messages)
	for _, id := range []string{request.ID, overtaken.ID} {
		for _, summary := range []string{"", "SSH login again"} {
			if status, answer := pcAsking(t, address, pcBrowserToken, "app", id, summary); status != http.StatusOK {
				t.Fatalf("status with summary %q = %d %+v", summary, status, answer)
			}
			if approvals := pcApprovals(t, alpha); len(approvals) != 0 {
				t.Fatalf("after the app stopped asking, and with summary %q, approvals = %+v", summary, approvals)
			}
		}
	}
}

// Only the desktop app that a request waits for may say that it asks to approve it.
func TestOnlyTheLinkOfARequestMayShowItAsAsking(t *testing.T) {
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", nil)
	pcDesktopOn(t, address, pcDeskToken, "desk", "desk", nil)
	pcPresence(t, pcBrowserToken, alpha, true)
	request, _, _ := pcAsk(t, alpha, "connection", messages)

	for _, refused := range []struct {
		name, token, link, id, summary string
		status                         int
	}{
		{"another browser over the link", pcDeskToken, "app", request.ID, "SSH login", http.StatusConflict},
		{"another link", pcDeskToken, "desk", request.ID, "SSH login", http.StatusNotFound},
		{"an unknown request", pcBrowserToken, "app", "unknown", "SSH login", http.StatusNotFound},
		{"no summary", pcBrowserToken, "app", request.ID, " ", http.StatusBadRequest},
	} {
		if status, answer := pcAsking(t, address, refused.token, refused.link, refused.id, refused.summary); status != refused.status {
			t.Fatalf("status from %s = %d %+v, want %d", refused.name, status, answer, refused.status)
		}
	}
	page := func(request *http.Request) { request.Header.Set("Origin", address) }
	if status, _ := pcLinkSend(t, address, "/pc/link/status", map[string]any{"link": "app", "id": request.ID, "asking": true, "summary": "SSH login", "expires_in": 120}, page); status != http.StatusForbidden {
		t.Fatalf("status from a page = %d", status)
	}
	if approvals := pcApprovals(t, alpha); len(approvals) != 0 {
		t.Fatalf("approvals = %+v", approvals)
	}
}

// A request shows as asking only while it waits: until the desktop app replies, gripi pc stops waiting, or the link
// ends. It can't come back after that.
func TestARequestShowsAsAskingOnlyWhileItWaits(t *testing.T) {
	for name, end := range map[string]func(t *testing.T, address, id string, cancel, stop func()){
		"a reply": func(t *testing.T, address, id string, _, _ func()) {
			if status, _ := pcLinkSend(t, address, "/pc/link/reply", map[string]any{"link": "app", "id": id, "error": "denied on the PC"}); status != http.StatusOK {
				t.Fatalf("reply = %d", status)
			}
		},
		"the end of gripi pc": func(_ *testing.T, _, _ string, cancel, _ func()) { cancel() },
		"the end of the link": func(t *testing.T, address, _ string, _, stop func()) {
			stop()
			if status, _ := pcLinkSend(t, address, "/pc/link/close", map[string]string{"link": "app"}); status != http.StatusOK {
				t.Fatalf("close = %d", status)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			alpha, address := pcGateway(t)
			messages, stop := pcDesktop(t, address, "app", nil)
			pcPresence(t, pcBrowserToken, alpha, true)
			request, answered, cancel := pcAsk(t, alpha, "connection", messages)
			id := request.ID
			if status, _ := pcAsking(t, address, pcBrowserToken, "app", id, "SSH login"); status != http.StatusOK || len(pcApprovals(t, alpha)) != 1 {
				t.Fatalf("status = %d, approvals %+v", status, pcApprovals(t, alpha))
			}

			end(t, address, id, cancel, stop)
			<-answered
			pcAsking(t, address, pcBrowserToken, "app", id, "SSH login")
			if approvals := pcApprovals(t, alpha); len(approvals) != 0 {
				t.Fatalf("approvals after %s = %+v", name, approvals)
			}
		})
	}
}

// Cancel in Gripi refuses a request that a computer asks the user to approve at once, rather than when the computer
// answers, and closes the computer's dialog. As it may only refuse, it acts only while the computer asks: not before,
// nor once the user approved it there.
func TestCancelInGripiRefusesARequestThatAComputerAsksToApprove(t *testing.T) {
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", nil)
	pcPresence(t, pcBrowserToken, alpha, true)
	request, answered, _ := pcAsk(t, alpha, "connection", messages)
	approved, approvedAnswered, _ := pcAsk(t, alpha, "approved", messages)
	cancel := func(id string) int {
		return browserRequest(http.MethodPost, "/pc/cancel", url.Values{"id": {id}}, "gripi_browser="+pcPhoneToken).Code
	}

	pcAsking(t, address, pcBrowserToken, "app", approved.ID, "SSH login")
	pcAsking(t, address, pcBrowserToken, "app", approved.ID, "")
	for _, id := range []string{request.ID, approved.ID, "unknown"} {
		if code := cancel(id); code != http.StatusConflict {
			t.Fatalf("cancel of %s while the computer doesn't ask = %d", id, code)
		}
	}

	pcAsking(t, address, pcBrowserToken, "app", request.ID, "SSH login")
	if code := cancel(request.ID); code != http.StatusOK {
		t.Fatalf("cancel = %d", code)
	}
	select {
	case err := <-answered:
		if err == nil || err.Error() != "cancelled in Gripi" {
			t.Fatalf("the cancelled request got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled request still waits")
	}
	if cancelled := nextPCMessage(t, messages, "cancel"); cancelled.ID != request.ID {
		t.Fatalf("the desktop app got %+v", cancelled)
	}
	if approvals := pcApprovals(t, alpha); len(approvals) != 0 {
		t.Fatalf("approvals after the cancel = %+v", approvals)
	}
	if code := cancel(request.ID); code != http.StatusConflict {
		t.Fatalf("a second cancel = %d", code)
	}
	if status, _ := pcLinkSend(t, address, "/pc/link/reply", map[string]any{"link": "app", "id": request.ID, "message": []byte{12}}); status != http.StatusNotFound {
		t.Fatalf("the reply to the cancelled request = %d", status)
	}
	select {
	case err := <-approvedAnswered:
		t.Fatalf("the approved request got %v", err)
	default:
	}
}

// pcStarting tells the gateway, as the desktop app of the laptop does over link once the user approved the request id,
// that it is about to run the command, and returns whether it may.
func pcStarting(t *testing.T, address, link, id string, change ...func(*http.Request)) int {
	status, _ := pcLinkSend(t, address, "/pc/link/status", map[string]any{"link": link, "id": id, "starting": true}, change...)
	return status
}

// pcResult is the desktop app's reply with how a command ended.
func pcResult(code int, stdout, stderr string, truncated bool) map[string]any {
	return map[string]any{"result": map[string]any{"exit_code": code, "stdout": []byte(stdout), "stderr": []byte(stderr), "truncated": truncated}}
}

// gripi pc run asks the computer where the session was last used to run the command, and passes on its output and
// exit code.
func TestPCRunRunsTheCommandOnTheComputerWhereTheSessionWasLastUsed(t *testing.T) {
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "laptop", func(message pcMessage) map[string]any {
		return pcResult(3, "out\n", "err\n", message.CWD == "")
	})
	pcDesktopOn(t, address, pcDeskToken, "desk", "desk", answeredBy("desk"))
	pcPresence(t, pcBrowserToken, alpha, true)

	if code, stdout, stderr := runCLI("pc", "run", "--cwd", "/srv/app", "--", "systemctl", "--user", "restart", "pipe wire"); code != 3 || stdout != "out\n" || stderr != "err\n" {
		t.Fatalf("gripi pc run = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	request := nextPCMessage(t, messages, "run")
	want := pcMessage{Type: "run", ID: request.ID, Session: alpha, SessionName: "Prompt for 0a1-alpha", Argv: []string{"systemctl", "--user", "restart", "pipe wire"}, CWD: "/srv/app"}
	if len(request.ID) != 64 || !reflect.DeepEqual(request, want) {
		t.Fatalf("the desktop app got %+v, want %+v", request, want)
	}

	// Without --cwd, the computer runs it in the user's home folder there, which the gateway doesn't know.
	if code, _, stderr := runCLI("pc", "run", "--", "true"); code != 3 || stderr != "err\ngripi pc: the command printed more than 16 MiB to an output, and Gripi dropped the rest\n" {
		t.Fatalf("gripi pc run without --cwd = %d, stderr %q", code, stderr)
	}
	if request := nextPCMessage(t, messages, "run"); request.CWD != "" || !reflect.DeepEqual(request.Argv, []string{"true"}) {
		t.Fatalf("the desktop app got %+v", request)
	}

	if code, _, _ := runCLI("pc", "run", "--root", "--cwd", "/etc", "--", "systemctl", "restart", "sshd"); code != 3 {
		t.Fatalf("gripi pc run --root = %d", code)
	}
	if request := nextPCMessage(t, messages, "run"); !request.Root || request.CWD != "/etc" || !reflect.DeepEqual(request.Argv, []string{"systemctl", "restart", "sshd"}) {
		t.Fatalf("the desktop app got %+v for a command as root", request)
	}
}

// gripi pc run refuses at once what the computer couldn't run as given, rather than letting it wait for nothing.
func TestPCRunRefusesACommandThatTheComputerCouldNotRunAsGiven(t *testing.T) {
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "laptop", answeredBy("laptop"))
	pcPresence(t, pcBrowserToken, alpha, true)

	for _, usage := range [][]string{{"pc", "run"}, {"pc", "run", "--"}, {"pc", "run", "true"}, {"pc", "run", "--cwd", "app", "--", "true"}, {"pc", "run", "--cwd", "/srv/app", "true", "--", "true"}, {"pc", "run", "--root=maybe", "--", "true"}} {
		if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help pc") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
		}
	}
	for name, command := range map[string][]string{
		"a NUL":                          {"pc", "run", "--", "printf", "a\x00b"},
		"a NUL in the folder":            {"pc", "run", "--cwd", "/srv/a\x00b", "--", "true"},
		"invalid UTF-8":                  {"pc", "run", "--", "printf", "\xff"},
		"an empty command":               {"pc", "run", "--", "", "true"},
		"more than 256 words":            append([]string{"pc", "run", "--", "echo"}, make([]string, 256)...),
		"a word of more than 4096 bytes": {"pc", "run", "--", "echo", strings.Repeat("x", 4097)},
	} {
		if code, _, stderr := runCLI(command...); code != 1 || !strings.HasPrefix(stderr, "gripi pc: the gateway answered 400: ") {
			t.Fatalf("gripi pc run with %s = %d, stderr %q", name, code, stderr)
		}
	}
	if code, _, stderr := runCLI("pc", "run", "--", "echo", strings.Repeat("x", 4096)); code != 1 || stderr != "gripi pc: the gateway answered 502: laptop\n" {
		t.Fatalf("gripi pc run with a word of 4096 bytes = %d, stderr %q", code, stderr)
	}
	if request := nextPCMessage(t, messages, "run"); len(request.Argv) != 2 {
		t.Fatalf("the desktop app got %+v", request)
	}
}

// A desktop app from before gripi pc run drops its commands, and one from before --root runs them as the user, so the
// gateway refuses them at once rather than letting gripi wait for nothing or the command run as the wrong user.
func TestPCRunNeedsADesktopAppThatRunsCommands(t *testing.T) {
	for _, old := range []struct {
		run     bool
		command []string
		refusal string
	}{
		{false, []string{"pc", "run", "--", "true"}, "can't run commands"},
		{true, []string{"pc", "run", "--root", "--", "true"}, "can't run commands as root"},
	} {
		t.Run(old.refusal, func(t *testing.T) {
			alpha, address := pcGateway(t)
			gateway.PCLinkStale = time.Minute
			if status, _ := pcLinkSend(t, address, "/pc/link", map[string]any{"link": "app", "open": true, "name": "laptop", "run": old.run}); status != http.StatusOK {
				t.Fatalf("open poll = %d", status)
			}
			pcPresence(t, pcBrowserToken, alpha, true)

			if code, _, stderr := runCLI(old.command...); code != 1 || stderr != "gripi pc: the gateway answered 503: the Gripi desktop app on laptop "+old.refusal+"; ask the user to update it\n" {
				t.Fatalf("gripi %q = %d, stderr %q", old.command, code, stderr)
			}
			if status, polled := pcPoll(t, address, "app", false); status != http.StatusOK || len(polled.Messages) != 0 {
				t.Fatalf("the desktop app got %d %+v", status, polled)
			}
		})
	}
}

// The computer confirms that gripi pc run still waits right before it runs the command, so Cancel in Gripi wins until
// then, and never acts once the command runs.
func TestCancelInGripiWinsOverARunOnlyUntilTheComputerStartsIt(t *testing.T) {
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", nil)
	pcPresence(t, pcBrowserToken, alpha, true)
	cancel := func(id string) *httptest.ResponseRecorder {
		return browserRequest(http.MethodPost, "/pc/cancel", url.Values{"id": {id}}, "gripi_browser="+pcPhoneToken)
	}

	result := startCLI("pc", "run", "--", "true")
	request := nextPCMessage(t, messages, "run")
	pcAsking(t, address, pcBrowserToken, "app", request.ID, "/usr/bin/true")
	if response := cancel(request.ID); response.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", response.Code, response.Body)
	}
	if status := pcStarting(t, address, "app", request.ID); status != http.StatusNotFound {
		t.Fatalf("starting after a cancel = %d", status)
	}
	if answered := <-result; answered.code != 1 || answered.stderr != "gripi pc: the gateway answered 502: cancelled in Gripi\n" {
		t.Fatalf("gripi pc run that Gripi cancelled = %+v", answered)
	}

	// Once the command may start, gripi pc run waits for it however long it runs.
	timeout := gateway.PCReplyTimeout
	t.Cleanup(func() { gateway.PCReplyTimeout = timeout })
	gateway.PCReplyTimeout = 200 * time.Millisecond
	result = startCLI("pc", "run", "--", "true")
	request = nextPCMessage(t, messages, "run")
	pcAsking(t, address, pcBrowserToken, "app", request.ID, "/usr/bin/true")
	if status := pcStarting(t, address, "app", request.ID); status != http.StatusOK {
		t.Fatalf("starting = %d", status)
	}
	if response := cancel(request.ID); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"The command already runs."`) {
		t.Fatalf("cancel of a command that runs = %d %s", response.Code, response.Body)
	}
	if approvals := pcApprovals(t, alpha); len(approvals) != 0 {
		t.Fatalf("approvals while the command runs = %+v", approvals)
	}
	time.Sleep(2 * gateway.PCReplyTimeout)
	reply := pcResult(0, "ran\n", "", false)
	reply["link"], reply["id"] = "app", request.ID
	if status, answer := pcLinkSend(t, address, "/pc/link/reply", reply); status != http.StatusOK {
		t.Fatalf("reply = %d %+v", status, answer)
	}
	if answered := <-result; answered.code != 0 || answered.stdout != "ran\n" || answered.stderr != "" {
		t.Fatalf("gripi pc run = %+v", answered)
	}
}

// The computer can't stop a command once it may have started it as root, so Cancel in Gripi says that, and gripi pc run
// doesn't take the end of the link for the end of the command.
func TestARunAsRootIsNeverReportedAsStoppedOnceItMayHaveStarted(t *testing.T) {
	alpha, address := pcGateway(t)
	messages, stop := pcDesktop(t, address, "app", nil)
	pcPresence(t, pcBrowserToken, alpha, true)

	result := startCLI("pc", "run", "--root", "--", "true")
	request := nextPCMessage(t, messages, "run")
	pcAsking(t, address, pcBrowserToken, "app", request.ID, "run a command as root: /usr/bin/true")
	if status := pcStarting(t, address, "app", request.ID); status != http.StatusOK {
		t.Fatalf("starting = %d", status)
	}
	response := browserRequest(http.MethodPost, "/pc/cancel", url.Values{"id": {request.ID}}, "gripi_browser="+pcPhoneToken)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"The command may already run as root, which Gripi can't stop."`) {
		t.Fatalf("cancel of a command that may run as root = %d %s", response.Code, response.Body)
	}

	stop()
	if status, _ := pcLinkSend(t, address, "/pc/link/close", map[string]string{"link": "app"}); status != http.StatusOK {
		t.Fatalf("close = %d", status)
	}
	if answered := <-result; answered.code != 1 || answered.stderr != "gripi pc: the gateway answered 503: the computer's link to Gripi ended before it replied; if the command started as root there, it keeps running\n" {
		t.Fatalf("gripi pc run --root = %+v", answered)
	}
}

// Only the link of a request may start it, and only while gripi pc run waits for it.
func TestOnlyTheLinkOfAWaitingRunMayStartIt(t *testing.T) {
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", nil)
	pcDesktopOn(t, address, pcDeskToken, "desk", "desk", nil)
	pcPresence(t, pcBrowserToken, alpha, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := newGatewayClient()
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() {
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://gripi/pc/run", strings.NewReader(url.Values{"session": {alpha}, "argv": {"true"}}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		_, err := client.http.Do(request)
		ended <- err
	}()
	request := nextPCMessage(t, messages, "run")

	if status := pcStarting(t, address, "app", request.ID, asBrowser(pcDeskToken)); status != http.StatusConflict {
		t.Fatalf("starting from another browser over the link = %d", status)
	}
	if status := pcStarting(t, address, "desk", request.ID, asBrowser(pcDeskToken)); status != http.StatusNotFound {
		t.Fatalf("starting from another link = %d", status)
	}
	cancel()
	<-ended
	if cancelled := nextPCMessage(t, messages, "cancel"); cancelled.ID != request.ID {
		t.Fatalf("the desktop app got %+v", cancelled)
	}
	if status := pcStarting(t, address, "app", request.ID); status != http.StatusNotFound {
		t.Fatalf("starting once gripi pc run ended = %d", status)
	}
}

// The desktop app keeps at most 16 MiB of each output, so the gateway takes no more.
func TestPCRunTakesAtMost16MiBOfEachOutput(t *testing.T) {
	alpha, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", nil)
	pcPresence(t, pcBrowserToken, alpha, true)

	result := startCLI("pc", "run", "--", "yes")
	request := nextPCMessage(t, messages, "run")
	for _, refused := range []map[string]any{pcResult(0, strings.Repeat("y", 16<<20+1), "", true), pcResult(0, "", strings.Repeat("y", 16<<20+1), true), {}} {
		refused["link"], refused["id"] = "app", request.ID
		if status, _ := pcLinkSend(t, address, "/pc/link/reply", refused); status != http.StatusBadRequest {
			t.Fatalf("a reply of a result over 16 MiB, or of nothing = %d", status)
		}
	}
	output := strings.Repeat("y", 16<<20)
	reply := pcResult(0, output, output, true)
	reply["link"], reply["id"] = "app", request.ID
	if status, answer := pcLinkSend(t, address, "/pc/link/reply", reply); status != http.StatusOK {
		t.Fatalf("a reply of 16 MiB of each output = %d %+v", status, answer)
	}
	if answered := <-result; answered.code != 0 || answered.stdout != output || !strings.HasPrefix(answered.stderr, output) {
		t.Fatalf("gripi pc run = %d, %d bytes of stdout, %d of stderr", answered.code, len(answered.stdout), len(answered.stderr))
	}
}

// gripi on a worker reaches the computer for the sessions that run there, routed by main's sessions, so also through a
// parent on main, but not for a session on main.
func TestPCOnAWorkerReachesTheComputerOnlyForSessionsThere(t *testing.T) {
	requireTools(t, "ssh-add")
	// Socket paths have a short system limit.
	state := filepath.Dir(socketPath(t))
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	alpha, address := pcGateway(t, func(configured *config.Config) {
		configured.ReadStatePath = filepath.Join(state, "read.json")
		cfg = *configured
	})
	beta := filepath.Join(filepath.Dir(alpha), "beta.jsonl")
	if code, _, stderr := runCLI("link", alpha, beta); code != 0 {
		t.Fatalf("gripi link = %d, stderr %q", code, stderr)
	}
	pcDesktop(t, address, "laptop", func(message pcMessage) map[string]any {
		if message.Type == "run" {
			return pcResult(0, "ran on the laptop\n", "", false)
		}
		return map[string]any{"error": "laptop"}
	})
	pcPresence(t, pcBrowserToken, beta, true)
	socket, stop, err := startWorkerSocket(cfg, browser, "beelink", fakePlacement{sessions: []string{alpha}})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	t.Setenv("GRIPI_SOCKET_PATH", socket)

	if code, stdout, stderr := runCLI("pc", "run", "--", "true"); code != 0 || stdout != "ran on the laptop\n" {
		t.Fatalf("gripi pc run on the worker = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if answerer := pcAnswerer(t); answerer != "laptop" {
		t.Fatalf("gripi pc ssh on the worker reached %q", answerer)
	}
	t.Setenv("PI_SESSION_FILE", beta)
	if code, _, stderr := runCLI("pc", "run", "--", "true"); code != 1 || !strings.HasPrefix(stderr, "gripi pc: the gateway answered 404: the command runs in no session of yours") {
		t.Fatalf("gripi pc run on the worker in a session on main = %d, stderr %q", code, stderr)
	}
	if answerer := pcAnswerer(t); !strings.HasPrefix(answerer, "the command runs in no session of yours") {
		t.Fatalf("gripi pc ssh on the worker in a session on main = %q", answerer)
	}
}
