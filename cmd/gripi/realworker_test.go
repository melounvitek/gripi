package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
		workers = manager
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

	started = time.Now()
	cli("worker", "return", project)
	t.Logf("returned in %s", time.Since(started).Round(time.Millisecond))
	cli("send", session.ID, `RUN [[{"name":"bash","arguments":{"command":"hostname && git log -1 --format=%s"}}]]`)
	cli("wait", session.ID, "--timeout", "60")
	localHost, _ := os.Hostname()
	if got := replies(session.ID); len(got) != 3 || !strings.Contains(got[2], localHost+`\nFrom the worker`) {
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
