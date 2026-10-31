package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	startGateway := func() {
		t.Helper()
		handler, workers, err := newGateway(cfg, remote.Options{Executable: gripi, InstallDir: "gripi-e2e", Unit: "gripi-e2e", RuntimeDir: filepath.Join(realWorkerRoot, "run")})
		if err != nil {
			t.Fatal(err)
		}
		local, err := startLocalServer(handler.(interface{ Local() http.Handler }).Local(), cfg.SocketPath)
		if err != nil {
			t.Fatal(err)
		}
		stopped := false
		stopGateway = func() {
			if !stopped {
				stopped = true
				local.Close()
				handler.(interface{ Close(context.Context) error }).Close(context.Background())
				workers.Close()
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
	// A new gateway connects to the worker once the session needs it.
	stopGateway()
	startGateway()
	// gripi on the worker reaches main and sees the session.
	commit := `echo written on the worker > worker.txt && git add worker.txt && git commit -qm "From the worker" && hostname && gripi list --json | grep -c '"id"'`
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

	started = time.Now()
	cli("worker", "return", project)
	t.Logf("returned in %s", time.Since(started).Round(time.Millisecond))
	cli("send", session.ID, `RUN [[{"name":"bash","arguments":{"command":"hostname && git log -1 --format=%s"}}]]`)
	cli("wait", session.ID, "--timeout", "60")
	localHost, _ := os.Hostname()
	var conversation []map[string]string
	if err := json.Unmarshal([]byte(cli("show", session.ID, "--all", "--json")), &conversation); err != nil {
		t.Fatal(err)
	}
	var replies []string
	for _, message := range conversation {
		if message["role"] == "assistant" {
			replies = append(replies, message["text"])
		}
	}
	if len(replies) != 3 || !strings.Contains(replies[1], `"isError":false,"text":"`+workerHost+`\n1\n"`) || !strings.Contains(replies[2], localHost+`\nFrom the worker`) {
		t.Fatalf("replies = %q", replies)
	}

	stopGateway()
	if output, _ := onWorker("systemctl --user is-active gripi-e2e; pgrep -af " + realWorkerRoot); output != "inactive" {
		t.Fatalf("still running on the worker: %s", output)
	}
}

func quote(text string) string {
	quoted, _ := json.Marshal(text)
	return string(quoted)
}
