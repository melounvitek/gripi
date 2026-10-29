package server

import (
	"bytes"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

// SudoTimeout is how long a command of 'gripi sudo' waits for the user's answer. Tests shorten it.
var SudoTimeout = 2 * time.Minute

// SudoPath is the sudo that runs the commands. Tests use a fake one.
var SudoPath = "/usr/bin/sudo"

// sudoPrompt is the prompt that Gripi gives sudo, without a %, which sudo would expand.
const sudoPrompt = "gripi"

// SudoAskpassPrompt is the only prompt that gripi, as sudo's askpass program, answers with the password. sudo-rs asks
// askpass "[sudo: <the -p prompt>] <PAM's prompt>" for each hidden prompt of PAM, one-time codes too, and
// "[sudo: input needed <PAM's prompt> " for each visible one, so this is the password prompt of pam_unix alone.
const SudoAskpassPrompt = "[sudo: " + sudoPrompt + "] Password: "

const sudoOutputLimit = 16 << 20

// SudoResult is how a command that 'gripi sudo' asked for ended.
type SudoResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   []byte `json:"stdout"`
	Stderr   []byte `json:"stderr"`
	// Truncated reports that the command printed more than sudoOutputLimit to an output, whose rest the gateway dropped.
	Truncated bool `json:"truncated"`
}

// sudoRequest is a command that an agent asked to run as root, waiting for the user's answer in the browser.
type sudoRequest struct {
	view sudoRequestView
	// answered closes when the user answers, with the password or, when they cancel, without one.
	answered chan struct{}
	password []byte
}

// sudoRequestView is all of a sudo request that the browser gets.
type sudoRequestView struct {
	ID      string `json:"id"`
	Session string `json:"session"`
	// Display and CWD show the command as the user would type it in a shell.
	Display   string    `json:"display"`
	CWD       string    `json:"cwd"`
	ExpiresAt time.Time `json:"expires_at"`
}

// sudoRequests holds the pending requests only in memory. A request leaves as it is answered, so it is never shown again.
type sudoRequests struct {
	mu      sync.Mutex
	pending map[string]*sudoRequest
	// passwords wait for the askpass program of the sudo that runs a request, by the token that it gets.
	passwords map[string][]byte
}

func (requests *sudoRequests) add(session, cwd string, argv []string) (*sudoRequest, error) {
	id, err := randomBrowserToken()
	if err != nil {
		return nil, err
	}
	request := &sudoRequest{
		view:     sudoRequestView{ID: id, Session: session, Display: shellWords(argv), CWD: shellWords([]string{cwd}), ExpiresAt: time.Now().Add(SudoTimeout)},
		answered: make(chan struct{}),
	}
	requests.mu.Lock()
	defer requests.mu.Unlock()
	if requests.pending == nil {
		requests.pending = make(map[string]*sudoRequest)
	}
	requests.pending[id] = request
	return request, nil
}

// remove reports whether the request was still pending, which it is not once the user has answered.
func (requests *sudoRequests) remove(id string) bool {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	_, pending := requests.pending[id]
	delete(requests.pending, id)
	return pending
}

// answer ends the request if it is pending and allowed accepts its session, and reports whether it did. A nil password
// cancels the request.
func (requests *sudoRequests) answer(id string, allowed func(session string) bool, password []byte) bool {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	request, pending := requests.pending[id]
	if !pending || !allowed(request.view.Session) {
		return false
	}
	delete(requests.pending, id)
	request.password = password
	close(request.answered)
	return true
}

func (requests *sudoRequests) offer(token string, password []byte) {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	if requests.passwords == nil {
		requests.passwords = make(map[string][]byte)
	}
	requests.passwords[token] = password
}

// take hands out the password for a token once, to a caller that then owns it.
func (requests *sudoRequests) take(token string) []byte {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	password := requests.passwords[token]
	delete(requests.passwords, token)
	return password
}

func (requests *sudoRequests) of(session string) []sudoRequestView {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	views := []sudoRequestView{}
	for _, request := range requests.pending {
		if request.view.Session == session {
			views = append(views, request.view)
		}
	}
	slices.SortFunc(views, func(left, right sudoRequestView) int { return left.ExpiresAt.Compare(right.ExpiresAt) })
	return views
}

var plainShellWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWords quotes each word that is not plain, which also escapes control and bidirectional characters.
func shellWords(words []string) string {
	shown := make([]string, len(words))
	for index, word := range words {
		shown[index] = word
		if !plainShellWord.MatchString(word) {
			shown[index] = strconv.QuoteToASCII(word)
		}
	}
	return strings.Join(shown, " ")
}

// sudo asks the user, in the conversation of the session that runs 'gripi sudo', to answer a request to run a
// command as root, and waits for the answer.
func (app *application) sudo(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	argv, cwd, session := request.PostForm["argv"], request.PostFormValue("cwd"), request.PostFormValue("session")
	if len(argv) == 0 || argv[0] == "" || !filepath.IsAbs(cwd) {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "a sudo request needs a command and an absolute working directory"})
		return
	}
	if !app.validOwnedSessionPath(request, session) || !app.knownOrPendingSession(request, session) {
		writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": "the command runs in no session of yours in Gripi, so nobody could answer"})
		return
	}
	// The browser's page and live events show the requests of the canonical session, which an alias would miss.
	session, err := app.canonicalRPCSessionPath(request, session)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return
	}
	pending, err := app.sudoRequests.add(session, cwd, argv)
	if err != nil {
		writeInternalError(response, "add a sudo request", err)
		return
	}
	timeout := time.NewTimer(time.Until(pending.view.ExpiresAt))
	defer timeout.Stop()
	select {
	case <-pending.answered:
	case <-timeout.C:
	case <-request.Context().Done():
	}
	// The user's answer takes the request away, so it wins even when it comes with the timeout.
	if app.sudoRequests.remove(pending.view.ID) {
		if request.Context().Err() == nil {
			writeJSONStatus(response, http.StatusRequestTimeout, map[string]string{"error": "nobody answered the request in Gripi in time"})
		}
		return
	}
	if pending.password == nil {
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": "the user cancelled the request in Gripi"})
		return
	}
	app.runSudo(response, request, cwd, argv, pending.password)
}

// runSudo runs the command with sudo, which runs gripi as its askpass program. gripi takes the password from the
// gateway with a token that works once, and only while sudo runs.
func (app *application) runSudo(response http.ResponseWriter, request *http.Request, cwd string, argv []string, password []byte) {
	token, err := randomBrowserToken()
	if err != nil {
		clear(password)
		writeInternalError(response, "create a sudo token", err)
		return
	}
	app.sudoRequests.offer(token, password)
	defer func() { clear(app.sudoRequests.take(token)) }()
	executable, err := os.Executable()
	if err != nil {
		writeInternalError(response, "find gripi for sudo", err)
		return
	}
	command := exec.CommandContext(request.Context(), SudoPath, append([]string{"-A", "-k", "-p", sudoPrompt, "--"}, argv...)...)
	command.Dir = cwd
	// Stdin stays nil, which reads /dev/null.
	command.Env = append(agent.ScrubbedEnvironment(os.Environ()), "SUDO_ASKPASS="+executable, "GRIPI_SUDO_TOKEN="+token)
	var stdout, stderr cappedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	// sudo passes SIGTERM on to the command.
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 10 * time.Second
	err = command.Run()
	if request.Context().Err() != nil {
		return
	}
	if command.ProcessState == nil {
		writeJSONStatus(response, http.StatusInternalServerError, map[string]string{"error": "cannot run sudo: " + err.Error()})
		return
	}
	status := command.ProcessState.Sys().(syscall.WaitStatus)
	code := status.ExitStatus()
	if status.Signaled() {
		code = 128 + int(status.Signal())
	}
	writeJSON(response, SudoResult{ExitCode: code, Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Truncated: stdout.dropped || stderr.dropped})
}

// cappedBuffer keeps the first sudoOutputLimit bytes of an output and drops the rest.
type cappedBuffer struct {
	bytes.Buffer
	dropped bool
}

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	kept := min(len(data), sudoOutputLimit-buffer.Len())
	buffer.dropped = buffer.dropped || kept < len(data)
	buffer.Buffer.Write(data[:kept])
	return len(data), nil
}

// sudoAskpass gives gripi, as the askpass program of a sudo that runSudo started, the password.
func (app *application) sudoAskpass(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	password := app.sudoRequests.take(request.PostForm.Get("token"))
	if password == nil {
		writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": "no password waits for this token"})
		return
	}
	defer clear(password)
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = response.Write(password)
}

const sudoRequestGone = "The sudo request is no longer waiting for an answer."

func (app *application) cancelSudo(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	owned := func(session string) bool { return app.validOwnedSessionPath(request, session) }
	if !app.sudoRequests.answer(request.PostFormValue("id"), owned, nil) {
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": sudoRequestGone})
		return
	}
	writeJSON(response, map[string]bool{"ok": true})
}

// answerSudo takes the user's password for a sudo request. Only a form body may carry it, never a URL, and
// limitRequestBody refuses a body of unknown length, which it would write to a file.
func (app *application) answerSudo(response http.ResponseWriter, request *http.Request) {
	if mediaType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type")); mediaType != "application/x-www-form-urlencoded" {
		writeJSONStatus(response, http.StatusUnsupportedMediaType, map[string]string{"error": "Send the password as a URL-encoded form."})
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, 8<<10)
	if !parseForm(response, request) {
		return
	}
	password := request.PostForm.Get("password")
	if password == "" || strings.ContainsAny(password, "\r\n") {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "Enter the password on one line."})
		return
	}
	owned := func(session string) bool { return app.validOwnedSessionPath(request, session) }
	if !app.sudoRequests.answer(request.PostForm.Get("id"), owned, []byte(password)) {
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": sudoRequestGone})
		return
	}
	writeJSON(response, map[string]bool{"ok": true})
}
