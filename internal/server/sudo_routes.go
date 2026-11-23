package server

import (
	"bytes"
	"context"
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

// SudoAskpassPrompts are the only prompts that gripi, as sudo's askpass program, answers with the password: the
// password prompt of pam_unix alone, as sudo-rs and classic sudo pass it on. sudo-rs asks askpass
// "[sudo: <the -p prompt>] <PAM's prompt>" for each hidden prompt of PAM, one-time codes too, and
// "[sudo: input needed <PAM's prompt> " for each visible one. Classic sudo asks with the -p prompt in place of PAM's
// "Password: " or its translation, and with PAM's own prompt for any other, unless sudoers sets passprompt_override.
var SudoAskpassPrompts = []string{"[sudo: " + sudoPrompt + "] Password: ", sudoPrompt}

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
	// Worker names the worker where the command runs, for a session there, and User the worker's user, whose password
	// sudo asks for there.
	Worker string `json:"worker,omitempty"`
	User   string `json:"user,omitempty"`
	// Again says that sudo asks for the password again, which it does after it rejected one, such as a wrong one.
	Again bool `json:"again,omitempty"`
}

// sudoRequests holds the pending requests only in memory. A request leaves as it is answered, so it is never shown again.
type sudoRequests struct {
	mu      sync.Mutex
	pending map[string]*sudoRequest
	// runs are the commands that sudo runs on main, by the token that their askpass program gets.
	runs map[string]*sudoRun
}

// sudoRun is a command that sudo runs for an answered request, until it ends.
type sudoRun struct {
	ctx context.Context
	// request is the request that the user last answered for it, whose password sudo's askpass program takes once.
	request   *sudoRequest
	delivered bool
}

// add shows a request, which gets an ID and expires after SudoTimeout.
func (requests *sudoRequests) add(view sudoRequestView) (*sudoRequest, error) {
	id, err := randomBrowserToken()
	if err != nil {
		return nil, err
	}
	view.ID, view.ExpiresAt = id, time.Now().Add(SudoTimeout)
	request := &sudoRequest{view: view, answered: make(chan struct{})}
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

// ask shows a request until the user answers it, which it returns, or until it expires or ctx ends, when it returns nil.
func (requests *sudoRequests) ask(ctx context.Context, view sudoRequestView) (*sudoRequest, error) {
	pending, err := requests.add(view)
	if err != nil {
		return nil, err
	}
	timeout := time.NewTimer(time.Until(pending.view.ExpiresAt))
	defer timeout.Stop()
	select {
	case <-pending.answered:
	case <-timeout.C:
	case <-ctx.Done():
	}
	// The user's answer takes the request away, so it wins even when it comes with the timeout.
	if requests.remove(pending.view.ID) {
		return nil, nil
	}
	return pending, nil
}

// password hands the password of a run to sudo's askpass program, which then owns it, or nil once the user refuses
// it. The first time, it is the password that the user entered for the request. sudo asks again only after it rejected
// the password, such as a wrong one, before the command runs, so then the user is asked again, as sudo would in a
// terminal.
func (requests *sudoRequests) password(ctx context.Context, run *sudoRun) []byte {
	requests.mu.Lock()
	if !run.delivered {
		run.delivered = true
		password := run.request.password
		run.request.password = nil
		requests.mu.Unlock()
		return password
	}
	view := run.request.view
	requests.mu.Unlock()
	view.Again = true
	answer, err := requests.ask(ctx, view)
	if answer == nil || err != nil {
		return nil
	}
	requests.mu.Lock()
	defer requests.mu.Unlock()
	run.request = answer
	password := answer.password
	answer.password = nil
	return password
}

// start lets sudo's askpass program take the password of a run on main with token, until the run ends.
func (requests *sudoRequests) start(token string, run *sudoRun) (end func()) {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	if requests.runs == nil {
		requests.runs = make(map[string]*sudoRun)
	}
	requests.runs[token] = run
	return func() {
		requests.mu.Lock()
		defer requests.mu.Unlock()
		delete(requests.runs, token)
		clear(run.request.password)
	}
}

func (requests *sudoRequests) run(token string) *sudoRun {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	return requests.runs[token]
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
// command as root, and waits for the answer. A worker's gripi sudo runs the command on the worker, with the password of
// the worker's user.
func (app *application) sudo(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	argv, cwd, session := request.PostForm["argv"], request.PostFormValue("cwd"), request.PostFormValue("session")
	if len(argv) == 0 || argv[0] == "" || !filepath.IsAbs(cwd) {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "a sudo request needs a command and an absolute working directory"})
		return
	}
	placement, fromWorker := workerOf(request)
	if !app.validOwnedSessionPath(request, session) || !app.knownOrPendingSession(request, session) {
		refusal := "the command runs in no session of yours in Gripi, so nobody could answer"
		if fromWorker {
			refusal = "the command runs in no session of yours on worker " + placement.Name() + ", so nobody could answer; ask the user to run it on main"
		}
		writeJSONStatus(response, http.StatusNotFound, map[string]string{"error": refusal})
		return
	}
	// The browser's page and live events show the requests of the canonical session, which an alias would miss.
	session, err := app.canonicalRPCSessionPath(request, session)
	if err != nil {
		http.Error(response, "Unable to remap pending session", http.StatusInternalServerError)
		return
	}
	view := sudoRequestView{Session: session, Display: shellWords(argv), CWD: shellWords([]string{cwd})}
	if fromWorker {
		location, _ := app.workers.Location(session)
		view.Worker, view.User = placement.Name(), location.User
	}
	pending, err := app.sudoRequests.ask(request.Context(), view)
	if err != nil {
		writeInternalError(response, "add a sudo request", err)
		return
	}
	if pending == nil {
		if request.Context().Err() == nil {
			writeJSONStatus(response, http.StatusRequestTimeout, map[string]string{"error": "nobody answered the request in Gripi in time"})
		}
		return
	}
	if pending.password == nil {
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": "the user cancelled the request in Gripi"})
		return
	}
	if view.Worker != "" {
		app.runSudoOnWorker(response, request, session, cwd, argv, pending.password)
		return
	}
	app.runSudo(response, request, cwd, argv, &sudoRun{ctx: request.Context(), request: pending})
}

// runSudoOnWorker runs the command with sudo on the worker that runs the session.
func (app *application) runSudoOnWorker(response http.ResponseWriter, request *http.Request, session, cwd string, argv []string, password []byte) {
	defer clear(password)
	var stdout, stderr cappedBuffer
	code, err := app.workers.Sudo(request.Context(), session, cwd, argv, password, &stdout, &stderr)
	if request.Context().Err() != nil {
		return
	}
	if err != nil {
		writeWorkersError(response, "run sudo on a worker", err)
		return
	}
	writeJSON(response, SudoResult{ExitCode: code, Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Truncated: stdout.dropped || stderr.dropped})
}

// runSudo runs the command with sudo, which runs gripi as its askpass program. gripi takes the password from the
// gateway with a token that works only while sudo runs.
func (app *application) runSudo(response http.ResponseWriter, request *http.Request, cwd string, argv []string, run *sudoRun) {
	token, err := randomBrowserToken()
	if err != nil {
		clear(run.request.password)
		writeInternalError(response, "create a sudo token", err)
		return
	}
	defer app.sudoRequests.start(token, run)()
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
	// sudo passes SIGTERM on to the command. Classic sudo 1.9.5p2 to 1.9.12 drops one from its own process group, so
	// sudo gets a group of its own, not gripi's.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	var password []byte
	if run := app.sudoRequests.run(request.PostForm.Get("token")); run != nil {
		// Asking again ends with the run, also when askpass outlives sudo.
		ctx, cancel := context.WithCancel(request.Context())
		defer cancel()
		defer context.AfterFunc(run.ctx, cancel)()
		password = app.sudoRequests.password(ctx, run)
	}
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
