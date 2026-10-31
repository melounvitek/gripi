package main

import (
	"context"
	"encoding/json"
	"errors"
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

// realWorkerRoot holds everything of the test gateway: Pi's folder, Gripi's state and the projects. The worker gets
// them at the same paths, whatever its home folder is.
const realWorkerRoot = "/tmp/gripi-e2e"

// Set GRIPI_TEST_WORKER to a worker that ssh reaches without a prompt, such as gripi@beelink.example.ts.net, to move
// a repository there and back with a scripted model. The test removes /tmp/gripi-e2e on both machines and the
// helper's folder on the worker afterwards.
func TestRealWorkerRunsTheSessionsOfAMovedRepository(t *testing.T) {
	destination := os.Getenv("GRIPI_TEST_WORKER")
	if destination == "" {
		t.Skip("set GRIPI_TEST_WORKER to a worker that ssh reaches without a prompt")
	}
	onWorker := func(script string) (string, error) {
		output, err := exec.Command("ssh", "-o", "BatchMode=yes", "--", destination, script).CombinedOutput()
		return strings.TrimSpace(string(output)), err
	}
	removeEverything := func() {
		os.RemoveAll(realWorkerRoot)
		if output, err := onWorker("rm -rf " + realWorkerRoot + " ~/gripi-e2e"); err != nil {
			t.Errorf("clean the worker: %v %s", err, output)
		}
	}
	removeEverything()
	t.Cleanup(removeEverything)

	// Built before HOME changes, which holds Go's caches.
	gripi := filepath.Join(realWorkerRoot, "bin", "gripi")
	build := exec.Command("go", "build", "-o", gripi, "github.com/melounvitek/gripi/cmd/gripi")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	home := filepath.Join(realWorkerRoot, "home")
	agentDir := filepath.Join(home, ".pi", "agent")
	for name, value := range map[string]string{
		"HOME": home, "PI_CODING_AGENT_DIR": agentDir, "GRIPI_SOCKET_PATH": filepath.Join(realWorkerRoot, "gripi.sock"),
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
		filepath.Join(agentDir, "settings.json"):                    `{"defaultProvider": "fake", "defaultModel": "fake-1"}`,
		filepath.Join(agentDir, "extensions", "fake_provider.ts"):   string(provider),
		filepath.Join(realWorkerRoot, "projects", "repo", "README"): "A project\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	project, feature := filepath.Join(realWorkerRoot, "projects", "repo"), filepath.Join(realWorkerRoot, "projects", "repo-feature")
	gitIn := func(dir string, arguments ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", dir}, arguments...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	gitIn(project, "init", "-q", "-b", "main")
	gitIn(project, "add", "-A")
	gitIn(project, "commit", "-qm", "First")
	gitIn(project, "worktree", "add", "-q", "-b", "feature", feature)

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
		handler, manager, err := newGateway(cfg, remote.Options{Executable: gripi, InstallDir: "gripi-e2e", Unit: "gripi-e2e", RuntimeDir: filepath.Join(realWorkerRoot, "run")})
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
	// repliesSoon waits a moment for main's copy of a session on the worker to have count replies.
	repliesSoon := func(id string, count int) []string {
		t.Helper()
		started := time.Now()
		for {
			if got := replies(id); len(got) >= count {
				t.Logf("main had reply %d of %s %s after the turn ended", count, id, time.Since(started).Round(time.Millisecond))
				return got
			}
			if time.Since(started) > 3*time.Second {
				t.Fatalf("main's copy of %s has no reply %d", id, count)
			}
			time.Sleep(50 * time.Millisecond)
		}
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
	session := decodeSession(t, cli("new", feature, "--json"))
	cli("send", session.ID, "Hello from main")
	cli("wait", session.ID, "--timeout", "60")

	started := time.Now()
	cli("worker", "move", session.ID, "beelink")
	t.Logf("moved in %s", time.Since(started).Round(time.Millisecond))
	workerHost, err := onWorker("hostname")
	if err != nil {
		t.Fatal(err)
	}
	// A session that starts on the worker has its file on main, which outlives the gateway.
	fresh := decodeSession(t, cli("new", project, "--json"))
	cli("send", fresh.ID, "Hello on the worker")
	cli("wait", fresh.ID, "--timeout", "60")
	repliesSoon(fresh.ID, 1)
	// The worker's logins follow main's while it is connected, without main's refresh tokens.
	workerLogins := func(key string) {
		t.Helper()
		authJSON := filepath.Join(agentDir, "auth.json")
		logins := `{"openai": {"type": "api_key", "key": "` + key + `"}, "anthropic": {"type": "oauth", "access": "access", "refresh": "main's refresh", "expires": 4102444800000}}`
		if err := os.WriteFile(authJSON, []byte(logins), 0600); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		for {
			copied, err := onWorker("cat " + authJSON)
			if err == nil && strings.Contains(copied, key) {
				if strings.Contains(copied, "main's refresh") || !strings.Contains(copied, "gripi:refresh-on-main") {
					t.Fatalf("the worker's auth.json: %s", copied)
				}
				t.Logf("the worker had main's changed logins after %s", time.Since(started).Round(time.Millisecond))
				return
			}
			if time.Since(started) > 15*time.Second {
				t.Fatalf("the worker's auth.json never got %s: %s %v", key, copied, err)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	workerLogins("sk-changed-on-main")
	// A new gateway connects to the worker once a session needs it.
	stopGateway()
	startGateway()
	if got := replies(fresh.ID); !slices.Equal(got, []string{"plain reply"}) {
		t.Fatalf("after a restart, the replies of the session that started on the worker are %q", got)
	}

	// Main's copy is read-only, so what changes there is saved aside before the worker's version replaces it. Git's
	// index doesn't count, which a plain git status on main rewrites.
	if err := os.WriteFile(filepath.Join(feature, "README"), []byte("edited on main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitIn(feature, "status", "--porcelain")
	// gripi on the worker reaches main and sees the sessions there.
	commit := `echo written on the worker > worker.txt && echo changed on the worker > README && git add -A && git commit -qm "From the worker" && hostname && gripi list --json | grep -c '"id"'`
	started = time.Now()
	cli("send", session.ID, `RUN [[{"name":"bash","arguments":{"command":`+quote(commit)+`}}],[{"name":"bash","arguments":{"command":"sleep 10"}}]]`)
	// The tool result waits until main has the commit, so main has it while the next step still runs.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(feature, "worker.txt")); err == nil {
			t.Logf("main had the commit %s after the prompt", time.Since(started).Round(time.Millisecond))
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("main never got the file written on the worker")
		}
	}
	if subject := gitIn(feature, "log", "-1", "--format=%s"); subject != "From the worker" {
		t.Fatalf("main's latest commit = %q", subject)
	}
	gitIn(project, "fsck", "--strict", "--no-dangling")
	if state := eventually(t, session.ID, func(gateway.LocalSession) bool { return true }).State; state != "working" {
		t.Fatalf("the session is %s, not still working on its next step", state)
	}
	cli("wait", session.ID, "--timeout", "60")
	if got := repliesSoon(session.ID, 2); !strings.Contains(got[1], `"isError":false,"text":"`+workerHost+`\n2\n"`) {
		t.Fatalf("main's copy of the session has the reply %q", got[1])
	}
	notices := workers.Notices(feature)
	if len(notices) != 1 || !slices.Equal(notices[0].Paths, []string{filepath.Join(feature, "README")}) {
		t.Fatalf("notices = %+v", notices)
	}
	if saved, _ := os.ReadFile(filepath.Join(notices[0].Aside, feature, "README")); string(saved) != "edited on main\n" {
		t.Fatalf("main's version saved aside = %q", saved)
	}
	if readme, _ := os.ReadFile(filepath.Join(feature, "README")); string(readme) != "changed on the worker\n" {
		t.Fatalf("main's README = %q", readme)
	}

	// A worktree made on the worker runs its sessions there too.
	second := filepath.Join(realWorkerRoot, "projects", "repo-second")
	cli("send", fresh.ID, `RUN [[{"name":"bash","arguments":{"command":"git worktree add -q -b second ../repo-second"}}]]`)
	cli("wait", fresh.ID, "--timeout", "60")
	if _, err := os.Stat(filepath.Join(second, "README")); err != nil {
		t.Fatalf("the worktree made on the worker: %v", err)
	}
	inSecond := decodeSession(t, cli("new", second, "--json"))
	cli("send", inSecond.ID, `RUN [[{"name":"bash","arguments":{"command":"hostname"}}]]`)
	cli("wait", inSecond.ID, "--timeout", "60")
	if got := repliesSoon(inSecond.ID, 1); !strings.Contains(got[0], `"text":"`+workerHost+`\n"`) {
		t.Fatalf("the session in the new worktree replied %q", got[0])
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
	// A short stall costs nothing.
	cli("send", fresh.ID, `RUN [[{"name":"bash","arguments":{"command":"sleep 4 && echo after the stall > stall.txt"}}]]`)
	time.Sleep(time.Second)
	signalMasters(syscall.SIGSTOP)
	time.Sleep(3 * time.Second)
	signalMasters(syscall.SIGCONT)
	cli("wait", fresh.ID, "--timeout", "60")
	if got := repliesSoon(fresh.ID, 3); !strings.Contains(got[2], `"isError":false`) {
		t.Fatalf("the step that the stall held up replied %q", got[2])
	}
	if _, err := os.Stat(filepath.Join(project, "stall.txt")); err != nil {
		t.Fatalf("main lacks what the worker wrote after the stall: %v", err)
	}
	if status := workers.Status("beelink"); status.State != remote.Connected {
		t.Fatalf("after a short stall, the worker is %+v", status)
	}

	// A link that hangs for half a lease loses the worker, which ends the running step, whose outcome is unknown.
	cli("send", session.ID, `RUN [[{"name":"bash","arguments":{"command":"echo before > before.txt"}}],[{"name":"bash","arguments":{"command":"echo during > during.txt && sleep 120"}}]]`)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(feature, "before.txt")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("main never got the file of the first step")
		}
	}
	// events returns the live events of a session after a cursor, as the browser gets them, and the last event's number.
	events := func(id string, after int64) ([]map[string]any, int64) {
		t.Helper()
		target := fmt.Sprintf("http://127.0.0.1:4567/events?after=%d&session=%s", after, url.QueryEscape(eventually(t, id, func(gateway.LocalSession) bool { return true }).Path))
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Accept", "application/json")
		request.RemoteAddr = "127.0.0.1:40000"
		response := httptest.NewRecorder()
		browser.ServeHTTP(response, request)
		var batch struct {
			Events  []map[string]any `json:"events"`
			LastSeq int64            `json:"last_seq"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
			t.Fatalf("events: %v %s", err, response.Body)
		}
		return batch.Events, batch.LastSeq
	}
	// The worker writes during.txt and sends it, but no barrier makes main take it.
	time.Sleep(time.Second)
	_, cursor := events(session.ID, 0)
	signalMasters(syscall.SIGSTOP)
	hung := time.Now()
	cli("wait", session.ID, "--timeout", "90")
	t.Logf("the running step ended %s after the link hung", time.Since(hung).Round(time.Millisecond))
	if ended, _ := events(session.ID, cursor); !slices.ContainsFunc(ended, func(event map[string]any) bool {
		return event["type"] == "agent_settled" && event["error"] == "Lost the connection to worker beelink. The outcome of the running step is unknown."
	}) {
		t.Fatalf("the running step ended without saying that its outcome is unknown: %v", ended)
	}
	status := workers.Status("beelink")
	if status.State != remote.Unreachable {
		t.Fatalf("the lost worker is %+v", status)
	}
	if code, _, stderr := runCLI("worker", "return", project); code == 0 || !strings.Contains(stderr, "unreachable") || !strings.Contains(stderr, status.GoneAt.Local().Format(time.TimeOnly)) {
		t.Fatalf("return before the worker is gone = %d %s", code, stderr)
	}
	time.Sleep(time.Until(status.GoneAt))
	if output, _ := onWorker("systemctl --user is-active gripi-e2e; pgrep -af " + realWorkerRoot); output != "inactive" {
		t.Fatalf("still running on the gone worker: %s", output)
	}
	// The repository continues here as main's copy was at the last barrier.
	cli("worker", "return", project)
	if _, err := os.Stat(filepath.Join(feature, "during.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("main has the file of the step whose outcome is unknown: %v", err)
	}
	gitIn(project, "fsck", "--strict", "--no-dangling")
	localHost, _ := os.Hostname()
	cli("send", session.ID, `RUN [[{"name":"bash","arguments":{"command":"hostname && cat before.txt"}}]]`)
	cli("wait", session.ID, "--timeout", "60")
	if got := replies(session.ID); !strings.Contains(got[len(got)-1], localHost+`\nbefore\n`) {
		t.Fatalf("the session continued on main with %q", got[len(got)-1])
	}
	noTakeOver()

	// The next move to the worker keeps aside there what the worker had that main never got.
	cli("worker", "move", project, "beelink")
	if output, err := onWorker("cat " + filepath.Join(filepath.Dir(project), ".repo.gripi-aside") + "/*/main/*" + feature + "/during.txt; test ! -e " + feature + "/during.txt"); err != nil || output != "during" {
		t.Fatalf("the worker's file that main never got: %q %v", output, err)
	}

	// A master that dies loses the worker at once, and the next prompt of a session whose Pi ran there connects again
	// and starts its Pi there.
	again := decodeSession(t, cli("new", project, "--json"))
	cli("send", again.ID, `RUN [[{"name":"bash","arguments":{"command":"hostname"}}]]`)
	cli("wait", again.ID, "--timeout", "60")
	signalMasters(syscall.SIGKILL)
	for deadline := time.Now().Add(5 * time.Second); workers.Status("beelink").State != remote.Unreachable; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("after its master died, the worker is %+v", workers.Status("beelink"))
		}
	}
	started = time.Now()
	cli("send", again.ID, `RUN [[{"name":"bash","arguments":{"command":"hostname"}}]]`)
	cli("wait", again.ID, "--timeout", "90")
	t.Logf("reconnected and ran a step in %s", time.Since(started).Round(time.Millisecond))
	if got := repliesSoon(again.ID, 2); !strings.Contains(got[1], `"text":"`+workerHost+`\n"`) {
		t.Fatalf("the session after the reconnect replied %q", got[1])
	}
	workerLogins("sk-changed-after-the-reconnect")

	started = time.Now()
	cli("worker", "return", project)
	t.Logf("returned in %s", time.Since(started).Round(time.Millisecond))
	cli("send", session.ID, `RUN [[{"name":"bash","arguments":{"command":"hostname && git log -1 --format=%s"}}]]`)
	cli("wait", session.ID, "--timeout", "60")
	if got := replies(session.ID); !strings.Contains(got[len(got)-1], localHost+`\nFrom the worker`) {
		t.Fatalf("replies = %q", got)
	}
	noTakeOver()

	stopGateway()
	if output, _ := onWorker("systemctl --user is-active gripi-e2e; pgrep -af " + realWorkerRoot); output != "inactive" {
		t.Fatalf("still running on the worker: %s", output)
	}
}

func quote(text string) string {
	quoted, _ := json.Marshal(text)
	return string(quoted)
}
