package main

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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
	remote "github.com/melounvitek/gripi/internal/worker"
)

// realWorkerRoot holds everything of the test gateway: its executable, the helper's sockets, which the worker gets at
// the same path, and its home folder, which the worker lacks. Pi's folder and Gripi's state are in that home folder,
// in gripi-e2e, and the repository in gripi-e2e-work, which the worker's sessions see in its user's own home folder.
const realWorkerRoot = "/tmp/gripi-e2e"

// Set GRIPI_TEST_WORKER to a worker that ssh reaches without a prompt, such as gripi@beelink.example.ts.net, to move
// sessions there and back with a scripted model. The test removes /tmp/gripi-e2e on both machines, and ~/gripi-e2e
// and ~/gripi-e2e-work on the worker afterwards, which have the helper, the moves' copies and what the worker got of
// main's home folder.
func TestRealWorkerRunsMovedSessionsOnCopiesOfTheirRepository(t *testing.T) {
	destination := os.Getenv("GRIPI_TEST_WORKER")
	if destination == "" {
		t.Skip("set GRIPI_TEST_WORKER to a worker that ssh reaches without a prompt")
	}
	// Settings inherited from the environment, such as GRIPI_WORKERS_PATH, would reach the user's own state.
	isolateSettings(t)
	onWorker := func(script string) (string, error) {
		output, err := exec.Command("ssh", "-o", "BatchMode=yes", "--", destination, script).CombinedOutput()
		return strings.TrimSpace(string(output)), err
	}
	removeEverything := func() {
		os.RemoveAll(realWorkerRoot)
		if output, err := onWorker("rm -rf " + realWorkerRoot + " ~/gripi-e2e ~/gripi-e2e-work"); err != nil {
			t.Errorf("clean the worker: %v %s", err, output)
		}
	}
	removeEverything()
	t.Cleanup(removeEverything)

	gripi := filepath.Join(realWorkerRoot, "bin", "gripi")
	buildGripi(t, gripi)
	// Main's home folder doesn't exist on the worker, whose user keeps its own; the folder that holds it does, as /home
	// would.
	home := filepath.Join(realWorkerRoot, "home", "vitek")
	if output, err := onWorker("mkdir -p " + filepath.Dir(home) + " && test ! -e " + home); err != nil {
		t.Fatalf("the worker has main's home folder %s: %v %s", home, err, output)
	}
	shared := filepath.Join(home, "gripi-e2e")
	agentDir := filepath.Join(shared, "agent")
	repository, feature := filepath.Join(home, "gripi-e2e-work", "repo"), filepath.Join(home, "gripi-e2e-work", "repo-feature")
	readme := filepath.Join(repository, "README")
	// A fake sudo stands in for the worker's, which needs a password and root, unless the worker user may use sudo with
	// GRIPI_TEST_WORKER_SUDO_PASSWORD.
	sudoPassword, fakeSudo := os.Getenv("GRIPI_TEST_WORKER_SUDO_PASSWORD"), ""
	if sudoPassword == "" {
		sudoPassword, fakeSudo = "the worker's password", filepath.Join(realWorkerRoot, "sudo")
		if output, err := onWorker("mkdir -p " + realWorkerRoot + " && printf %s " + remote.ShellQuote(fakeSudoOnWorker) + " > " + fakeSudo + " && chmod 700 " + fakeSudo); err != nil {
			t.Fatalf("install the fake sudo: %v %s", err, output)
		}
	}
	connection := remote.Options{Executable: gripi, InstallDir: "gripi-e2e", Unit: "gripi-e2e", RuntimeDir: filepath.Join(realWorkerRoot, "run"), Sudo: fakeSudo}
	workerHelper = connection
	t.Cleanup(func() { workerHelper = remote.Options{} })
	// inView runs a sh script on the worker, which sees main's home folder there as the sessions do.
	inView := func(script string) (string, error) {
		return onWorker(remote.ShellQuote("gripi-e2e/bin/gripi", "worker", "enter", "--home", home, "--", "sh", "-c", script))
	}
	for name, value := range map[string]string{
		"HOME": home, "PI_CODING_AGENT_DIR": agentDir, "GRIPI_SESSIONS_ROOT": filepath.Join(agentDir, "sessions"), "GRIPI_ENV_PATH": filepath.Join(home, ".config", "gripi", "env"),
		"GRIPI_READ_STATE_PATH": filepath.Join(shared, "state", "read-state.json"), "GRIPI_SOCKET_PATH": filepath.Join(shared, "gripi.sock"),
		"GRIPI_BROWSER_AUTH_DISABLED": "true", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Gripi test", "GIT_AUTHOR_EMAIL": "test@example.com", "GIT_COMMITTER_NAME": "Gripi test", "GIT_COMMITTER_EMAIL": "test@example.com",
	} {
		t.Setenv(name, value)
	}
	provider, err := os.ReadFile(filepath.Join("..", "..", "test", "helpers", "fake_provider.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(agentDir, "settings.json"):                  `{"defaultProvider": "fake", "defaultModel": "fake-1"}`,
		filepath.Join(agentDir, "extensions", "fake_provider.ts"): string(provider),
		readme: "first line\nsecond line\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitIn := func(dir string, arguments ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", dir}, arguments...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	gitIn(repository, "init", "-q", "-b", "main")
	gitIn(repository, "add", "-A")
	gitIn(repository, "commit", "-qm", "First")

	if code, _, stderr := runCLI("worker", "add", destination, "--name", "beelink"); code != 0 {
		t.Fatalf("worker add: %s", stderr)
	}
	cfg, err := config.Load(os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	var stopGateway func()
	var workers *remote.Manager
	var browser http.Handler
	startGateway := func() {
		t.Helper()
		handler, manager, err := newGateway(cfg, connection)
		if err != nil {
			t.Fatal(err)
		}
		local, err := startLocalServer(handler.(interface{ Local() http.Handler }).Local(), cfg.SocketPath)
		if err != nil {
			t.Fatal(err)
		}
		workers, browser = manager, handler
		stopped := false
		stopGateway = func() {
			if !stopped {
				stopped = true
				local.Close()
				handler.(interface{ Close(context.Context) error }).Close(context.Background())
				manager.Close()
			}
		}
	}
	startGateway()
	t.Cleanup(func() { stopGateway() })

	cli := func(arguments ...string) string {
		t.Helper()
		code, stdout, stderr := runCLI(arguments...)
		if code != 0 {
			t.Fatalf("gripi %v = %d: %s", arguments, code, stderr)
		}
		return stdout
	}
	// replies returns the assistant's replies in a session, as main's copy of the session's file has them.
	replies := func(id string) []string {
		t.Helper()
		var conversation []map[string]string
		if err := json.Unmarshal([]byte(cli("show", id, "--all", "--json")), &conversation); err != nil {
			t.Fatal(err)
		}
		var replies []string
		for _, message := range conversation {
			if message["role"] == "assistant" {
				replies = append(replies, message["text"])
			}
		}
		return replies
	}
	// run has a session run a shell command and returns the reply with its output, once main's copy of the session's
	// file has it.
	run := func(id, command string) string {
		t.Helper()
		before := len(replies(id))
		cli("send", id, `RUN [[{"name":"bash","arguments":{"command":`+quote(command)+`}}]]`)
		cli("wait", id, "--timeout", "60")
		for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if got := replies(id); len(got) > before {
				return got[len(got)-1]
			}
			if time.Now().After(deadline) {
				t.Fatalf("main's copy of %s has no reply to %q", id, command)
			}
		}
	}
	// browse sends a request as the browser does.
	browse := func(method, target string, form url.Values) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "http://127.0.0.1:4567"+target, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Accept", "application/json")
		request.RemoteAddr = "127.0.0.1:40000"
		response := httptest.NewRecorder()
		browser.ServeHTTP(response, request)
		return response
	}
	// noTakeOver fails when the guard of the sessions' files took the mirror for another program.
	noTakeOver := func() {
		t.Helper()
		var listed []gateway.LocalSession
		if err := json.Unmarshal([]byte(cli("list", "--json")), &listed); err != nil {
			t.Fatal(err)
		}
		for _, session := range listed {
			if session.State == "external" || session.State == "conflict" {
				t.Fatalf("session %s in %s is %s", session.ID, session.CWD, session.State)
			}
		}
	}
	// copies lists the worker's copies of moves.
	copies := func() []string {
		t.Helper()
		output, err := onWorker("cd gripi-e2e/copies 2>/dev/null && ls -d -- */ 2>/dev/null | tr -d /")
		if err != nil {
			t.Fatalf("list the worker's copies: %v %s", err, output)
		}
		return strings.Fields(output)
	}
	workerHost, err := onWorker("hostname")
	if err != nil {
		t.Fatal(err)
	}
	localHost, _ := os.Hostname()
	onHost := func(host string) string { return `"text":"` + host + `\n` }

	// Two sessions of the repository on main; one of them moves.
	moved := decodeSession(t, cli("new", repository, "--json"))
	cli("send", moved.ID, "Hello from main")
	cli("wait", moved.ID, "--timeout", "60")
	stays := decodeSession(t, cli("new", repository, "--json"))
	cli("send", stays.ID, "Hello from main too")
	cli("wait", stays.ID, "--timeout", "60")
	started := time.Now()
	cli("worker", "move", moved.ID, "beelink")
	t.Logf("moved in %s", time.Since(started).Round(time.Millisecond))
	if location, found := workers.Location(moved.Path); !found || location.Worker != "beelink" || location.State != remote.Connected {
		t.Fatalf("the moved session's location = %+v, %v", location, found)
	}
	if _, found := workers.Location(stays.Path); found {
		t.Fatal("the other session of the repository moved too")
	}
	// The copy is in the worker user's own home folder, which the view shows at the repository's own path.
	if output, err := onWorker("test -d gripi-e2e/copies/*/gripi-e2e-work/repo/.git && test ! -e gripi-e2e-work/repo && test ! -e " + home + " && echo ok"); err != nil || output != "ok" {
		t.Fatalf("the worker's copy is not in its user's home folder: %q %v", output, err)
	}

	// On the worker, the session makes a worktree with a commit on its own branch, and edits files in the main checkout,
	// which main gets only when it returns; meanwhile, the other session on main edits a file of its own and one of them.
	onWorkerRun := `git worktree add -q -b feature ../repo-feature && cd ../repo-feature && echo made on the worker > feature.txt && git add -A && git commit -qm "On the worker" && ` +
		`cd ../repo && printf 'first line\nsecond line by the worker\n' > README && echo by the worker > worker.txt && hostname`
	if reply := run(moved.ID, onWorkerRun); !strings.Contains(reply, onHost(workerHost)) || !strings.Contains(reply, `"isError":false`) {
		t.Fatalf("the moved session replied %q", reply)
	}
	if reply := run(stays.ID, `printf 'first line\nsecond line by main\n' > README && echo by main > main.txt && hostname`); !strings.Contains(reply, onHost(localHost)) {
		t.Fatalf("the session on main replied %q", reply)
	}
	if _, err := os.Stat(feature); !os.IsNotExist(err) {
		t.Fatalf("main's worktree of the moved session before it returned: %v", err)
	}
	// gripi sudo in the moved session asks in Gripi for the password of the worker's user, and runs the command as root
	// on the worker, in the session's folder, which only the session's view has.
	workerUser, _ := onWorker("id -un")
	if status := workers.Status("beelink"); status.User != workerUser || status.Docker == "" {
		t.Fatalf("the worker's status = %+v", status)
	}
	before := len(replies(moved.ID))
	cli("send", moved.ID, `RUN [[{"name":"bash","arguments":{"command":`+quote("gripi sudo -- sh -c 'pwd; head -n 1 README; id -u'")+`}}]]`)
	var asked struct {
		Requests []map[string]string `json:"sudo_requests"`
	}
	for deadline := time.Now().Add(30 * time.Second); len(asked.Requests) == 0; time.Sleep(100 * time.Millisecond) {
		if err := json.Unmarshal(browse(http.MethodGet, "/events?session="+url.QueryEscape(moved.Path), nil).Body.Bytes(), &asked); err != nil || time.Now().After(deadline) {
			t.Fatalf("gripi sudo asked nothing: %v", err)
		}
	}
	if request := asked.Requests[0]; request["worker"] != "beelink" || request["user"] != workerUser || request["cwd"] != repository {
		t.Fatalf("the sudo request = %v", request)
	}
	if answer := browse(http.MethodPost, "/sudo/answer", url.Values{"id": {asked.Requests[0]["id"]}, "password": {sudoPassword}}); answer.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", answer.Code, answer.Body)
	}
	cli("wait", moved.ID, "--timeout", "60")
	if got := replies(moved.ID); len(got) == before || !strings.Contains(got[len(got)-1], `"text":"`+repository+`\nfirst line\n0\n`) {
		t.Fatalf("gripi sudo replied %q", got[before:])
	}
	if fakeSudo != "" {
		if answers, _ := onWorker("cat " + realWorkerRoot + "/askpass"); !strings.HasPrefix(answers, sha256Hex(sudoPassword+"\n")+"\n") {
			t.Fatalf("askpass answered %q", answers)
		}
	}
	// The worker's logins follow main's while it is connected, without main's refresh tokens.
	authJSON := filepath.Join(agentDir, "auth.json")
	logins := `{"openai": {"type": "api_key", "key": "sk-changed-on-main"}, "anthropic": {"type": "oauth", "access": "access", "refresh": "main's refresh", "expires": 4102444800000}}`
	if err := os.WriteFile(authJSON, []byte(logins), 0600); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		copied, err := inView("cat " + authJSON)
		if err == nil && strings.Contains(copied, "sk-changed-on-main") {
			if strings.Contains(copied, "main's refresh") || !strings.Contains(copied, "gripi:refresh-on-main") {
				t.Fatalf("the worker's auth.json: %s", copied)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker's auth.json never got main's change: %s %v", copied, err)
		}
	}
	// A new gateway connects to the worker once the session needs it, and its copy is as the session left it.
	stopGateway()
	startGateway()
	if reply := run(moved.ID, "hostname && cat worker.txt"); !strings.Contains(reply, onHost(workerHost)+`by the worker\n`) {
		t.Fatalf("after a restart, the moved session replied %q", reply)
	}
	noTakeOver()

	// The return brings the worktree, the branch and the file that only the worker changed, keeps main's changes, and
	// keeps aside the worker's version of the file that both changed.
	started = time.Now()
	cli("worker", "return", moved.ID)
	t.Logf("returned in %s", time.Since(started).Round(time.Millisecond))
	for path, want := range map[string]string{
		filepath.Join(feature, "feature.txt"): "made on the worker\n", filepath.Join(repository, "worker.txt"): "by the worker\n",
		filepath.Join(repository, "main.txt"): "by main\n", readme: "first line\nsecond line by main\n",
	} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Errorf("main's %s = %q, %v; want %q", path, got, err, want)
		}
	}
	if subject := gitIn(repository, "log", "-1", "--format=%s", "feature"); subject != "On the worker" {
		t.Fatalf("main's feature branch is at %q", subject)
	}
	gitIn(repository, "fsck", "--strict", "--no-dangling")
	notices := workers.Notices(moved.Path)
	if len(notices) != 1 || len(notices[0].Result.Aside) != 1 || notices[0].Result.Aside[0].Path != readme || !strings.Contains(notices[0].Result.Notice, notices[0].Recovery) {
		t.Fatalf("notices = %+v", notices)
	}
	if kept, _ := os.ReadFile(filepath.Join(notices[0].Recovery, "worker") + readme); string(kept) != "first line\nsecond line by the worker\n" {
		t.Fatalf("the worker's version kept aside = %q", kept)
	}
	if reply := run(moved.ID, "hostname && git log -1 --format=%s feature"); !strings.Contains(reply, onHost(localHost)+`On the worker\n`) {
		t.Fatalf("the returned session replied %q", reply)
	}
	for deadline := time.Now().Add(30 * time.Second); len(copies()) != 0; time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the worker keeps the copies %q", copies())
		}
	}
	if err := workers.DismissNotices(context.Background(), moved.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(notices[0].Recovery); !os.IsNotExist(err) {
		t.Fatalf("the recovery folder after the dismissal: %v", err)
	}
	if refs := gitIn(repository, "for-each-ref", "refs/gripi"); refs != "" {
		t.Fatalf("refs after the dismissal: %s", refs)
	}
	noTakeOver()

	// Freezing the master connections to the worker stalls every channel, as a link that hangs does.
	signalMasters := func(signal syscall.Signal) {
		t.Helper()
		output, _ := exec.Command("pgrep", "-f", "ssh -MNnT -S "+filepath.Join(stateDir(cfg), "ssh")).Output()
		if len(strings.Fields(string(output))) == 0 {
			t.Fatal("no master connection to the worker")
		}
		for _, pid := range strings.Fields(string(output)) {
			number, _ := strconv.Atoi(pid)
			if err := syscall.Kill(number, signal); err != nil {
				t.Fatal(err)
			}
		}
	}
	// events returns the live events of a session after a cursor, as the browser gets them, and the last event's number.
	events := func(session string, after int64) ([]map[string]any, int64) {
		t.Helper()
		response := browse(http.MethodGet, fmt.Sprintf("/events?after=%d&session=%s", after, url.QueryEscape(session)), nil)
		var batch struct {
			Events  []map[string]any `json:"events"`
			LastSeq int64            `json:"last_seq"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
			t.Fatalf("events: %v %s", err, response.Body)
		}
		return batch.Events, batch.LastSeq
	}
	// The session moves again, with the worktree that it used now.
	cli("worker", "move", moved.ID, "beelink")
	if output, err := onWorker("test -f gripi-e2e/copies/*/gripi-e2e-work/repo-feature/feature.txt && echo ok"); err != nil || output != "ok" {
		t.Fatalf("the second copy lacks the worktree that the session used: %q %v", output, err)
	}
	// A link that hangs for half a lease loses the worker, which ends the running step, whose outcome is unknown.
	cli("send", moved.ID, `RUN [[{"name":"bash","arguments":{"command":"echo before > before.txt"}}],[{"name":"bash","arguments":{"command":"echo during > during.txt && sleep 120"}}]]`)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if shadowed, _ := filepath.Glob(filepath.Join(stateDir(cfg), "moves", "*", "shadow") + filepath.Join(repository, "before.txt")); len(shadowed) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("main never got the file of the first step")
		}
	}
	// The worker writes during.txt and sends it, but no barrier makes main take it.
	time.Sleep(time.Second)
	_, cursor := events(moved.Path, 0)
	signalMasters(syscall.SIGSTOP)
	hung := time.Now()
	cli("wait", moved.ID, "--timeout", "90")
	t.Logf("the running step ended %s after the link hung", time.Since(hung).Round(time.Millisecond))
	if ended, _ := events(moved.Path, cursor); !slices.ContainsFunc(ended, func(event map[string]any) bool {
		return event["type"] == "agent_settled" && event["error"] == "Lost the connection to worker beelink. The outcome of the running step is unknown."
	}) {
		t.Fatalf("the running step ended without saying that its outcome is unknown: %v", ended)
	}
	status := workers.Status("beelink")
	if status.State != remote.Unreachable {
		t.Fatalf("the lost worker is %+v", status)
	}
	if code, _, stderr := runCLI("worker", "return", moved.ID); code == 0 || !strings.Contains(stderr, "unreachable") || !strings.Contains(stderr, status.GoneAt.Local().Format(time.TimeOnly)) {
		t.Fatalf("return before the worker is gone = %d %s", code, stderr)
	}
	time.Sleep(time.Until(status.GoneAt))
	if output, _ := onWorker("systemctl --user is-active gripi-e2e; pgrep -af " + realWorkerRoot); output != "inactive" {
		t.Fatalf("still running on the gone worker: %s", output)
	}
	// The session continues on main with what main got of its changes.
	cli("worker", "return", moved.ID)
	if _, err := os.Stat(filepath.Join(repository, "during.txt")); !os.IsNotExist(err) {
		t.Fatalf("main has the file of the step whose outcome is unknown: %v", err)
	}
	gitIn(repository, "fsck", "--strict", "--no-dangling")
	if reply := run(moved.ID, "hostname && cat before.txt"); !strings.Contains(reply, onHost(localHost)+`before\n`) {
		t.Fatalf("the session continued on main with %q", reply)
	}
	noTakeOver()

	// Two sessions of the repository move at once, each on a copy of its own: one of them new, which Pi has not saved
	// yet, so it starts again on the worker. The worker's stale copy goes once main reaches the worker again.
	blank := decodeSession(t, cli("new", repository, "--json"))
	answers := make(chan string, 2)
	for _, session := range []string{moved.Path, blank.Path} {
		go func() {
			code, _, stderr := runCLI("worker", "move", session, "beelink")
			answers <- fmt.Sprintf("%d %s", code, stderr)
		}()
	}
	for range 2 {
		if answer := <-answers; answer != "0 " {
			t.Fatalf("a move of two at once = %s", answer)
		}
	}
	for deadline := time.Now().Add(30 * time.Second); len(copies()) != 2; time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the worker has the copies %q", copies())
		}
	}
	// A new gateway has no Pi of the new session, which exists only on the worker until Pi saves it.
	stopGateway()
	startGateway()
	if reply := run(blank.Path, "hostname && echo blank > blank.txt"); !strings.Contains(reply, onHost(workerHost)) {
		t.Fatalf("the new session moved to the worker replied %q", reply)
	}
	if reply := run(moved.ID, "hostname && test ! -e blank.txt && echo separate"); !strings.Contains(reply, onHost(workerHost)+`separate\n`) {
		t.Fatalf("the other session on the worker replied %q", reply)
	}
	cli("worker", "return", blank.Path)
	cli("worker", "return", moved.ID)
	if got, _ := os.ReadFile(filepath.Join(repository, "blank.txt")); string(got) != "blank\n" {
		t.Fatalf("main's blank.txt = %q", got)
	}
	for deadline := time.Now().Add(30 * time.Second); len(copies()) != 0; time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the worker keeps the copies %q", copies())
		}
	}
	noTakeOver()

	stopGateway()
	if output, _ := onWorker("systemctl --user is-active gripi-e2e; pgrep -af " + realWorkerRoot); output != "inactive" {
		t.Fatalf("still running on the worker: %s", output)
	}
}

// fakeSudoOnWorker stands in for the worker's sudo, which needs a password and root. Named sudo, it is a parent that
// gripi's askpass program accepts. It asks askpass for the password twice, as classic sudo does after a wrong one,
// with the -p prompt, and records a hash of each answer in askpass next to itself. Then it runs the command as the
// worker's user, who can enter the namespaces of the session's view only through their user namespace, which root
// doesn't need.
const fakeSudoOnWorker = `#!/bin/sh
[ "$1 $2 $3 $4 $5" = "-A -k -p gripi --" ] || { echo "fake sudo: unexpected arguments $*" >&2; exit 99; }
shift 5
for call in 1 2; do
	"$SUDO_ASKPASS" gripi | sha256sum | cut -d ' ' -f 1 >> "${0%/*}/askpass"
done
[ "$1" = nsenter ] && shift && set -- nsenter --user --preserve-credentials "$@"
exec "$@"
`

func quote(text string) string {
	quoted, _ := json.Marshal(text)
	return string(quoted)
}
