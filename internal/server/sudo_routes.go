package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
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

// SudoRememberFor is how long Gripi remembers a password that the user asked it to remember, from when they entered
// it. Tests shorten it.
var SudoRememberFor = 5 * time.Minute

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
	// remembered is the remembered password that the answer took, or that the user asked Gripi to remember with it.
	remembered *rememberedPassword
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
	// Remembered says that Gripi remembers the password, so the user only approves the command.
	Remembered bool `json:"remembered,omitempty"`
}

// sudoTarget is where a request's command runs as root, which takes one password: on main, the session's, or on a
// worker as its user.
type sudoTarget struct{ session, worker, user string }

func (view sudoRequestView) target() sudoTarget {
	return sudoTarget{view.Session, view.Worker, view.User}
}

// rememberedPassword is a password that the user asked Gripi to remember for a target. Their approval of the target's
// requests takes it once a command ran with it, until SudoRememberFor has passed since they entered it.
type rememberedPassword struct {
	target    sudoTarget
	password  []byte
	until     time.Time
	confirmed bool // a command ran with it
}

// sudoRequests holds the pending requests only in memory. A request leaves as it is answered, so it is never shown again.
type sudoRequests struct {
	mu      sync.Mutex
	pending map[string]*sudoRequest
	// runs are the commands that sudo runs on main, by the token that their askpass program gets.
	runs       map[string]*sudoRun
	remembered map[sudoTarget]*rememberedPassword
	// notify tells the user of each request that shows.
	notify func(view sudoRequestView)
}

// sudoRun is a command that sudo runs for an answered request, until it ends.
type sudoRun struct {
	ctx context.Context
	// request is the request that the user last answered for it, whose password sudo's askpass program takes once.
	request   *sudoRequest
	delivered bool
	ended     bool
}

// end clears the password of a run that has ended, unless sudo's askpass program took it. A password that the user
// asked Gripi to remember for the run is remembered only once the command succeeded, which it can't unless sudo took
// the last password that it got.
func (requests *sudoRequests) end(run *sudoRun, succeeded bool) {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	run.ended = true
	clear(run.request.password)
	entry := run.request.remembered
	if entry == nil || entry.confirmed {
		return
	}
	if succeeded && run.delivered && requests.remembered[entry.target] == entry && time.Now().Before(entry.until) {
		entry.confirmed = true
		return
	}
	requests.forget(entry)
}

// remember keeps a copy of a password for a target until SudoRememberFor has passed. Callers hold mu.
func (requests *sudoRequests) remember(target sudoTarget, password []byte) *rememberedPassword {
	if requests.remembered == nil {
		requests.remembered = make(map[sudoTarget]*rememberedPassword)
	}
	if previous := requests.remembered[target]; previous != nil {
		clear(previous.password)
	}
	entry := &rememberedPassword{target: target, password: bytes.Clone(password), until: time.Now().Add(SudoRememberFor)}
	requests.remembered[target] = entry
	time.AfterFunc(SudoRememberFor, func() {
		requests.mu.Lock()
		defer requests.mu.Unlock()
		requests.forget(entry)
	})
	return entry
}

// recall returns the password that Gripi remembers for a target, if a command ran with it. Callers hold mu.
func (requests *sudoRequests) recall(target sudoTarget) *rememberedPassword {
	if entry := requests.remembered[target]; entry != nil && entry.confirmed && time.Now().Before(entry.until) {
		return entry
	}
	return nil
}

// forget clears a remembered password. Callers hold mu.
func (requests *sudoRequests) forget(entry *rememberedPassword) {
	if requests.remembered[entry.target] == entry {
		delete(requests.remembered, entry.target)
	}
	clear(entry.password)
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

// cancel ends the request without a password if it is pending and allowed accepts its session, and reports whether it
// did.
func (requests *sudoRequests) cancel(id string, allowed func(session string) bool) bool {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	request, pending := requests.pending[id]
	if !pending || !allowed(request.view.Session) {
		return false
	}
	delete(requests.pending, id)
	close(request.answered)
	return true
}

// approve answers the request with password if it is pending and allowed accepts its session, or, with an empty one,
// with the password that Gripi remembers for the request's target. With remember, Gripi remembers password for the
// target. It returns http.StatusConflict when the request isn't pending, and http.StatusBadRequest when it has no
// password, which leaves it pending.
func (requests *sudoRequests) approve(id string, allowed func(session string) bool, password []byte, remember bool) int {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	request, pending := requests.pending[id]
	if !pending || !allowed(request.view.Session) {
		return http.StatusConflict
	}
	switch {
	case len(password) == 0:
		if request.remembered = requests.recall(request.view.target()); request.remembered == nil {
			return http.StatusBadRequest
		}
		password = bytes.Clone(request.remembered.password)
	case remember:
		request.remembered = requests.remember(request.view.target(), password)
	}
	delete(requests.pending, id)
	request.password = password
	close(request.answered)
	return http.StatusOK
}

// ask shows a request until the user answers it, which it returns, or until it expires or ctx ends, when it returns nil.
func (requests *sudoRequests) ask(ctx context.Context, view sudoRequestView) (*sudoRequest, error) {
	pending, err := requests.add(view)
	if err != nil {
		return nil, err
	}
	if requests.notify != nil {
		requests.notify(pending.view)
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
	if entry := run.request.remembered; entry != nil {
		requests.forget(entry)
	}
	requests.mu.Unlock()
	view.Again = true
	answer, err := requests.ask(ctx, view)
	if answer == nil || err != nil {
		return nil
	}
	requests.mu.Lock()
	defer requests.mu.Unlock()
	// An answer that comes as the run ends is too late for it.
	if run.ended {
		clear(answer.password)
		if entry := answer.remembered; entry != nil && !entry.confirmed {
			requests.forget(entry)
		}
		return nil
	}
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
			view := request.view
			view.Remembered = requests.recall(view.target()) != nil
			views = append(views, view)
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
	run := &sudoRun{ctx: request.Context(), request: pending}
	if view.Worker != "" {
		app.runSudoOnWorker(response, request, session, view.Worker, cwd, argv, run)
		return
	}
	app.runSudo(response, request, cwd, argv, run)
}

// notifySudo sends the owners of a request's session a notification of it at once, unlike of a reply, unless a
// Gripi client of theirs shows the session, and with it the request.
func (app *application) notifySudo(view sudoRequestView) {
	owners, err := app.completionNotifications.owners(view.Session)
	if err != nil {
		log.Printf("find whom to notify of a sudo request: %v", err)
		return
	}
	title := "current session"
	if session, found := app.sessionStore.Session(view.Session); found && strings.TrimSpace(session.DisplayName) != "" {
		title = agent.NotificationPreview(session.DisplayName)
	}
	digest := sha256.Sum256([]byte(view.ID))
	payload, _ := json.Marshal(map[string]string{
		"type":  "gripi-notification",
		"title": title,
		"body":  agent.NotificationBody("sudo: " + view.Display),
		"tag":   "gripi-sudo:" + hex.EncodeToString(digest[:16]),
		"url":   completedReplyURL(view.Session, app.agentOf(view.Session)),
	})
	for _, owner := range owners {
		presenceOwner := singleUserOwner
		if app.config.MultiUserMode {
			presenceOwner = owner
		}
		if app.notificationPresence.Focused(presenceOwner, view.Session) {
			continue
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			defer cancel()
			if err := app.pushNotifier.Deliver(ctx, owner, payload); err != nil {
				log.Printf("deliver a sudo request's notification: %v", err)
			}
		}()
	}
}

// runSudoOnWorker runs the command with sudo on the worker that runs the session, as the request said.
func (app *application) runSudoOnWorker(response http.ResponseWriter, request *http.Request, session, worker, cwd string, argv []string, run *sudoRun) {
	succeeded := false
	defer func() { app.sudoRequests.end(run, succeeded) }()
	password := func(ctx context.Context) []byte { return app.sudoRequests.password(ctx, run) }
	var stdout, stderr cappedBuffer
	code, err := app.workers.Sudo(request.Context(), session, worker, cwd, argv, password, &stdout, &stderr)
	if request.Context().Err() != nil {
		return
	}
	if err != nil {
		writeWorkersError(response, "run sudo on a worker", err)
		return
	}
	succeeded = code == 0
	writeJSON(response, SudoResult{ExitCode: code, Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Truncated: stdout.dropped || stderr.dropped})
}

// runSudo runs the command with sudo, which runs gripi as its askpass program. gripi takes the password from the
// gateway with a token that works only while sudo runs.
func (app *application) runSudo(response http.ResponseWriter, request *http.Request, cwd string, argv []string, run *sudoRun) {
	succeeded := false
	defer func() { app.sudoRequests.end(run, succeeded) }()
	token, err := randomBrowserToken()
	if err != nil {
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
	succeeded = code == 0
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
	if !app.sudoRequests.cancel(request.PostFormValue("id"), owned) {
		writeJSONStatus(response, http.StatusConflict, map[string]string{"error": sudoRequestGone})
		return
	}
	writeJSON(response, map[string]bool{"ok": true})
}

// answerSudo takes the user's password for a sudo request, or their approval, when Gripi remembers the password. Only
// a form body may carry it, never a URL, and limitRequestBody refuses a body of unknown length, which it would write to
// a file.
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
	refused := map[string]string{"error": "Enter the password on one line."}
	if strings.ContainsAny(password, "\r\n") {
		writeJSONStatus(response, http.StatusBadRequest, refused)
		return
	}
	owned := func(session string) bool { return app.validOwnedSessionPath(request, session) }
	switch status := app.sudoRequests.approve(request.PostForm.Get("id"), owned, []byte(password), request.PostForm.Get("remember") != ""); status {
	case http.StatusConflict:
		writeJSONStatus(response, status, map[string]string{"error": sudoRequestGone})
	case http.StatusBadRequest:
		writeJSONStatus(response, status, refused)
	default:
		writeJSON(response, map[string]bool{"ok": true})
	}
}
