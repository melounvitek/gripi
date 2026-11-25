package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

const pcBrowserToken = "pc-desktop-browser"

// pcMessage is a message of the gateway to the desktop app.
type pcMessage struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Session     string `json:"session"`
	SessionName string `json:"session_name"`
	Connection  string `json:"connection"`
	Message     []byte `json:"message"`
}

type pcLinkAnswer struct {
	Messages []pcMessage `json:"messages"`
	Error    string      `json:"error"`
}

// pcGateway runs fakePiGateway with access approval on, which PC access needs, and a browser approved for the
// desktop app. gripi runs in alpha, and the desktop app reaches the gateway at address.
func pcGateway(t *testing.T, configure ...func(*config.Config)) (alpha, address string) {
	t.Helper()
	poll, stale := gateway.PCLinkPoll, gateway.PCLinkStale
	t.Cleanup(func() { gateway.PCLinkPoll, gateway.PCLinkStale = poll, stale })
	gateway.PCLinkPoll, gateway.PCLinkStale = 200*time.Millisecond, 500*time.Millisecond
	var browserAccess string
	alpha, _ = fakePiGateway(t, append([]func(*config.Config){func(cfg *config.Config) {
		cfg.AdminPassword, cfg.BrowserAuthDisabled, browserAccess = "secret", false, cfg.BrowserAccessPath
	}}, configure...)...)
	if _, err := access.NewBrowserStore(browserAccess).ApproveCurrentBrowser(pcBrowserToken, "desktop app"); err != nil {
		t.Fatal(err)
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

// pcPoll polls as link, which a poll with open opens and the others resume.
func pcPoll(t *testing.T, address, link string, open bool) (int, pcLinkAnswer) {
	return pcLinkSend(t, address, "/pc/link", map[string]any{"link": link, "open": open})
}

// pcDesktop plays the desktop app, which opens link and polls it until stop is called or the test ends. It answers
// each ssh message with what answer returns, unless answer is nil, and passes on every message. It returns once the
// link is the current one.
func pcDesktop(t *testing.T, address, link string, answer func(pcMessage) map[string]any) (messages <-chan pcMessage, stop func()) {
	t.Helper()
	received := make(chan pcMessage, 100)
	poll := func(open bool) bool {
		status, polled := pcPoll(t, address, link, open)
		if status != http.StatusOK {
			t.Errorf("poll of %s = %d %s", link, status, polled.Error)
			return false
		}
		for _, message := range polled.Messages {
			received <- message
			if message.Type == "ssh" && answer != nil {
				reply := answer(message)
				reply["link"], reply["id"] = link, message.ID
				if status, replied := pcLinkSend(t, address, "/pc/link/reply", reply); status != http.StatusOK {
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
	_, address := pcGateway(t)
	pcDesktop(t, address, "app", func(pcMessage) map[string]any { return map[string]any{"error": "the user denied it"} })

	result := pcSSH(t, "sh", "-c", "ssh-add -l; ssh-add -l")
	if result.code != 1 || result.stdout != "" || strings.Count(result.stderr, "gripi pc: the user denied it\n") != 1 || strings.Count(result.stderr, "agent refused operation") != 2 {
		t.Fatalf("gripi pc ssh = %+v", result)
	}
}

func TestPCSSHFailsAtOnceWithoutALinkOrASession(t *testing.T) {
	requireTools(t, "ssh-add")
	alpha, _ := pcGateway(t)
	for _, usage := range [][]string{{"pc"}, {"pc", "run", "--", "true"}, {"pc", "ssh"}, {"pc", "ssh", "--"}, {"pc", "ssh", "ssh-add", "-l"}} {
		if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help pc") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
		}
	}

	started := time.Now()
	if result := pcSSH(t, "ssh-add", "-l"); result.code != 1 || !strings.HasPrefix(result.stderr, "gripi pc: no computer is connected") || time.Since(started) > 5*time.Second {
		t.Fatalf("gripi pc ssh without a link = %+v after %s", result, time.Since(started))
	}
	t.Setenv("PI_SESSION_FILE", alpha+".missing")
	if result := pcSSH(t, "ssh-add", "-l"); result.code != 1 || !strings.HasPrefix(result.stderr, "gripi pc: the command runs in no session of yours") {
		t.Fatalf("gripi pc ssh in an unknown session = %+v", result)
	}
	outsideSessions(t)
	if code, _, stderr := runCLI("pc", "ssh", "--", "true"); code != 1 || !strings.Contains(stderr, "PI_SESSION_FILE") {
		t.Fatalf("gripi pc ssh outside a session = %d, stderr %q", code, stderr)
	}
}

// A link keeps others out while it polls, and its requests and replies never reach another link.
func TestPCSSHFailsWhenTheLinkEndsOrIsReplaced(t *testing.T) {
	requireTools(t, "ssh-add")
	_, address := pcGateway(t)
	const lost = "gripi pc: the computer's link to Gripi ended before it replied\n"
	messages, stop := pcDesktop(t, address, "first", nil)
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
	_, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", nil)

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
	_, address := pcGateway(t)
	messages, _ := pcDesktop(t, address, "app", nil)
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
	client, err := newGatewayClient()
	if err != nil {
		t.Fatal(err)
	}
	relay := &sshAgentRelay{client: client, session: alpha, stderr: io.Discard, reported: map[string]bool{}}
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
	_, address := pcGateway(t)
	messages, stop := pcDesktop(t, address, "app", nil)
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

// The desktop app drops a link whose poll carries more than 64 messages, so the others wait for the next poll.
func TestPCLinkPollCarriesAtMost64Messages(t *testing.T) {
	alpha, address := pcGateway(t)
	gateway.PCLinkStale = time.Minute
	if status, _ := pcPoll(t, address, "app", true); status != http.StatusOK {
		t.Fatalf("poll = %d", status)
	}
	client, err := newGatewayClient()
	if err != nil {
		t.Fatal(err)
	}
	relay := &sshAgentRelay{client: client}
	for index := range 65 {
		if _, err := relay.post(context.Background(), url.Values{"session": {alpha}, "connection": {strconv.Itoa(index)}, "close": {"1"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []int{64, 1} {
		if status, polled := pcPoll(t, address, "app", false); status != http.StatusOK || len(polled.Messages) != want {
			t.Fatalf("poll = %d with %d messages, want %d", status, len(polled.Messages), want)
		}
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
