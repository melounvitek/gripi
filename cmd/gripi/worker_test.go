package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
	remote "github.com/melounvitek/gripi/internal/worker"
	"github.com/melounvitek/gripi/internal/worker/reconcile"
)

// fakeSSHScript stands in for ssh. It records its arguments, then fails as ssh does with FAKE_SSH_ERROR, answers
// FAKE_SSH_ANSWER, or runs the remote command on this machine with FAKE_SSH_HOME as its home. Like another machine,
// it doesn't see the files that main leaves to find a worker that shares its files, unless FAKE_SSH_SHARED is set.
const fakeSSHScript = `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_SSH_ARGUMENTS"
if [ -n "$FAKE_SSH_ERROR" ]; then
	printf '%s\n' "$FAKE_SSH_ERROR" >&2
	exit 255
fi
if [ -n "$FAKE_SSH_ANSWER" ]; then
	printf '%s' "$FAKE_SSH_ANSWER"
	exit 0
fi
for command; do :; done
case $command in *.gripi-worker-check-*) [ -n "$FAKE_SSH_SHARED" ] || command=$(printf '%s\n' "$command" | sed 's/\.gripi-worker-check-/.gripi-worker-elsewhere-/g') ;; esac
HOME=$FAKE_SSH_HOME exec sh -c "$command"
`

// fakeSSH puts fakeSSHScript on PATH as ssh, and a systemd-run that succeeds, gives the test a home that the
// worker shares and a workers file, and returns the file with ssh's arguments.
func fakeSSH(t *testing.T) (arguments string) {
	t.Helper()
	isolateSettings(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "ssh"), []byte(fakeSSHScript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "systemd-run"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GRIPI_WORKERS_PATH", filepath.Join(t.TempDir(), "workers.json"))
	t.Setenv("GRIPI_WORKER_PLACEMENTS_PATH", filepath.Join(t.TempDir(), "worker-placements.json"))
	t.Setenv("FAKE_SSH_HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("FAKE_SSH_ERROR", "")
	t.Setenv("FAKE_SSH_ANSWER", "")
	t.Setenv("FAKE_SSH_SHARED", "")
	arguments = filepath.Join(directory, "arguments")
	t.Setenv("FAKE_SSH_ARGUMENTS", arguments)
	return arguments
}

func listedWorkers(t *testing.T) []map[string]string {
	t.Helper()
	code, stdout, stderr := runCLI("worker", "list", "--json")
	var workers []map[string]string
	if err := json.Unmarshal([]byte(stdout), &workers); code != 0 || stderr != "" || err != nil {
		t.Fatalf("gripi worker list --json = %d, stdout %q, stderr %q (%v)", code, stdout, stderr, err)
	}
	return workers
}

func assertNoWorkersFile(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(os.Getenv("GRIPI_WORKERS_PATH")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("workers file exists: %v", err)
	}
}

// thisSystem is what uname says about this machine, as the worker's would when it matches.
func thisSystem(t *testing.T) (system, machine string) {
	t.Helper()
	uname := func(flag string) string {
		output, err := exec.Command("uname", flag).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(output))
	}
	return uname("-s"), uname("-m")
}

func TestWorkerAddChecksTheWorkerOverSSHAndRemembersIt(t *testing.T) {
	arguments := fakeSSH(t)
	destination := "gripi_worker@beelink-omarchy.tail8fd8b2.ts.net"
	if code, stdout, stderr := runCLI("worker", "list", "--json"); code != 0 || stdout != "[]\n" || stderr != "" {
		t.Fatalf("gripi worker list --json = %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	code, stdout, stderr := runCLI("worker", "add", destination)
	if code != 0 || stderr != "" {
		t.Fatalf("gripi worker add = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, expected := range []string{"beelink-omarchy", "full copy of Pi's configuration, including logins", "secure"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("stdout %q does not contain %q", stdout, expected)
		}
	}
	recorded, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(recorded), "-o\nBatchMode=yes\n") || !strings.Contains(string(recorded), "\n"+destination+"\n") {
		t.Fatalf("ssh arguments = %q", recorded)
	}
	info, err := os.Stat(os.Getenv("GRIPI_WORKERS_PATH"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("workers file = %v, %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".pi", "agent")); err != nil || !info.IsDir() {
		t.Fatalf("Pi's agent folder on the worker = %v, %v", info, err)
	}

	code, stdout, stderr = runCLI("worker", "list")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "beelink-omarchy") || !strings.Contains(stdout, destination) {
		t.Fatalf("gripi worker list = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI("worker", "add", destination)
	if code != 1 || stdout != "" || !strings.Contains(stderr, `"beelink-omarchy"`) || !strings.Contains(stderr, "--name") {
		t.Fatalf("gripi worker add of a taken name = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runCLI("worker", "add", destination, "--name", "beelink"); code != 0 || stderr != "" {
		t.Fatalf("gripi worker add --name = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	expected := []map[string]string{{"name": "beelink-omarchy", "destination": destination}, {"name": "beelink", "destination": destination}}
	if workers := listedWorkers(t); !reflect.DeepEqual(workers, expected) {
		t.Fatalf("workers = %v", workers)
	}

	code, stdout, stderr = runCLI("worker", "remove", "beelink-omarchy")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "beelink-omarchy") || !strings.Contains(stdout, "stay") {
		t.Fatalf("gripi worker remove = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if workers := listedWorkers(t); !reflect.DeepEqual(workers, expected[1:]) {
		t.Fatalf("workers after remove = %v", workers)
	}
	if code, stdout, stderr := runCLI("worker", "remove", "beelink-omarchy"); code != 1 || stdout != "" || !strings.Contains(stderr, "beelink-omarchy") {
		t.Fatalf("gripi worker remove of a missing worker = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestWorkerAddSaysHowToFixSSHFailures(t *testing.T) {
	for _, test := range []struct{ sshError, advice string }{
		{"Host key verification failed.", "Run 'ssh gripi_worker@beelink' once"},
		{"gripi_worker@beelink: Permission denied (publickey).", "Run 'ssh-copy-id gripi_worker@beelink'"},
		{"ssh: Could not resolve hostname beelink: Name or service not known", "ssh: Could not resolve hostname beelink: Name or service not known"},
	} {
		t.Run(test.sshError, func(t *testing.T) {
			fakeSSH(t)
			t.Setenv("FAKE_SSH_ERROR", test.sshError)

			code, stdout, stderr := runCLI("worker", "add", "gripi_worker@beelink")
			if code != 1 || stdout != "" || !strings.Contains(stderr, "gripi worker: ") || !strings.Contains(stderr, test.advice) {
				t.Fatalf("gripi worker add = %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			assertNoWorkersFile(t)
		})
	}
}

func TestWorkerAddRefusesAWorkerThatCannotRunThisMachinesPi(t *testing.T) {
	system, machine := thisSystem(t)
	otherSystem := "Darwin"
	if system == otherSystem {
		otherSystem = "Linux"
	}
	for _, test := range []struct{ name, answer, reason string }{
		{"no Git", system + "\n" + machine + "\ngripi_worker\n/home/gripi_worker\nsystemd\nagent\n", "no Git"},
		{"no systemd", system + "\n" + machine + "\ngripi_worker\n/home/gripi_worker\ngit\nagent\n", "systemd"},
		{"another system", otherSystem + "\n" + machine + "\ngripi_worker\n/home/gripi_worker\ngit\nsystemd\nagent\n", "same operating system and architecture"},
		{"another architecture", system + "\nriscv128\ngripi_worker\n/home/gripi_worker\ngit\nsystemd\nagent\n", "same operating system and architecture"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeSSH(t)
			t.Setenv("FAKE_SSH_ANSWER", test.answer)

			code, stdout, stderr := runCLI("worker", "add", "gripi_worker@beelink")
			if code != 1 || stdout != "" || !strings.Contains(stderr, test.reason) {
				t.Fatalf("gripi worker add = %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			assertNoWorkersFile(t)
		})
	}
}

func TestWorkerAddFailsWhenTheWorkerCannotCreatePisAgentFolder(t *testing.T) {
	fakeSSH(t)
	system, machine := thisSystem(t)
	home := os.Getenv("HOME")
	t.Setenv("FAKE_SSH_ANSWER", system+"\n"+machine+"\ngripi_worker\n"+home+"\ngit\nsystemd\n")

	code, stdout, stderr := runCLI("worker", "add", "gripi_worker@beelink")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "cannot create Pi's folder "+filepath.Join(home, ".pi", "agent")) {
		t.Fatalf("gripi worker add = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	assertNoWorkersFile(t)
}

func TestWorkerAddChecksThatTheWorkerCanWriteToPisAgentFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	fakeSSH(t)
	agentDir := filepath.Join(t.TempDir(), "read-only", "agent")
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(agentDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(agentDir, 0700) })
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)

	code, stdout, stderr := runCLI("worker", "add", "gripi_worker@beelink")
	if code != 1 || stdout != "" || !strings.Contains(stderr, agentDir) || strings.Contains(stderr, "usermod") {
		t.Fatalf("gripi worker add = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	assertNoWorkersFile(t)
}

// A worker that is this machine as the same user, or that mounts its home folder, would get main's own files instead
// of copies, such as Pi's logins.
func TestWorkerAddRefusesAWorkerThatSharesThisMachinesFiles(t *testing.T) {
	fakeSSH(t)
	t.Setenv("FAKE_SSH_SHARED", "1")
	agentDir := filepath.Join(os.Getenv("HOME"), ".pi", "agent")

	code, stdout, stderr := runCLI("worker", "add", "gripi_worker@beelink")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "shares this machine's "+agentDir) {
		t.Fatalf("gripi worker add = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	assertNoWorkersFile(t)
	if left, _ := filepath.Glob(filepath.Join(agentDir, ".gripi-worker-check-*")); len(left) != 0 {
		t.Fatalf("left in Pi's folder: %q", left)
	}
}

// buildGripi builds Gripi as main uploads it to workers. Call it before HOME changes, which holds Go's caches.
func buildGripi(t *testing.T, gripi string) {
	t.Helper()
	build := exec.Command("go", "build", "-o", gripi, "github.com/melounvitek/gripi/cmd/gripi")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
}

// A worker user with another home folder needs no setup: what runs there with main's paths sees the user's home
// folder at main's, in a private view of unprivileged user namespaces, which add checks with the helper.
func TestWorkerAddChecksThePrivateViewOfAWorkerUserWithAnotherHomeFolder(t *testing.T) {
	gripi := filepath.Join(t.TempDir(), "gripi")
	buildGripi(t, gripi)
	fakeSSH(t)
	workerHome := t.TempDir()
	t.Setenv("FAKE_SSH_HOME", workerHome)
	// The worker is this machine as another user, which would see main's files outside the view.
	t.Setenv("FAKE_SSH_SHARED", "1")
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".pi", "agent"), 0700); err != nil {
		t.Fatal(err)
	}
	workerHelper = remote.Options{Executable: gripi}
	t.Cleanup(func() { workerHelper = remote.Options{} })

	code, stdout, stderr := runCLI("worker", "add", "gripi_worker@beelink")
	if code != 0 {
		// This system blocks unprivileged user namespaces, as Ubuntu does by default.
		if code != 1 || !strings.Contains(stderr, workerHome) || !strings.Contains(stderr, "sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0") {
			t.Fatalf("gripi worker add = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		assertNoWorkersFile(t)
		return
	}
	if info, err := os.Stat(filepath.Join(workerHome, ".pi", "agent")); err != nil || !info.IsDir() {
		t.Fatalf("Pi's agent folder in the worker user's home folder = %v, %v", info, err)
	}
	if workers := listedWorkers(t); len(workers) != 1 || workers[0]["name"] != "beelink" {
		t.Fatalf("workers = %v", workers)
	}
}

// Main runs worker commands that use its paths through enter, which runs them as they are when the worker user's
// home folder is main's.
func TestWorkerEnterRunsACommandAsItIsWithMainsHomeFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	code, stdout, stderr := runCLI("worker", "enter", "--home", home, "--", "sh", "-c", `echo "$HOME"; exit 3`)
	if code != 3 || stdout != home+"\n" || stderr != "" {
		t.Fatalf("gripi worker enter = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, _, stderr := runCLI("worker", "enter", "--home", home); code != 2 || !strings.Contains(stderr, "command") {
		t.Fatalf("gripi worker enter without a command = %d, stderr %q", code, stderr)
	}
}

// A worker gets the folder of the extensions at the same path, and its sessions' Pi loads both.
func TestThePiAdapterExtractsBothExtensionsIntoOneFolder(t *testing.T) {
	adapter, err := newPiAdapter(config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tree, worker, err := adapter.ExtensionFiles()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(tree) != filepath.Dir(worker) {
		t.Fatalf("the extensions are in different folders: %s, %s", tree, worker)
	}
	for extracted, source := range map[string]string{tree: "gripi-tree.ts", worker: "gripi-worker.ts"} {
		want, _ := os.ReadFile(filepath.Join("..", "..", "pi_extensions", source))
		if got, err := os.ReadFile(extracted); err != nil || len(want) == 0 || string(got) != string(want) {
			t.Fatalf("%s = %d bytes, %v; want the %d bytes of %s", extracted, len(got), err, len(want), source)
		}
	}
	if again, _, _ := adapter.ExtensionFiles(); again != tree {
		t.Fatalf("ExtensionFiles extracted again: %s, then %s", tree, again)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(tree)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the extensions' folder after Close: %v", err)
	}
}

// Workers keep a copy of the extensions' folder, so a gateway extracts them to the same folder after every restart.
func TestThePiAdapterExtractsTheExtensionsToTheSameFolderAfterARestart(t *testing.T) {
	state := t.TempDir()
	cfg := config.Config{ReadStatePath: filepath.Join(state, "read-state.json")}
	var trees []string
	for range 2 {
		adapter, err := newPiAdapter(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		tree, _, err := adapter.ExtensionFiles()
		if err != nil {
			t.Fatal(err)
		}
		trees = append(trees, tree)
		if err := adapter.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if trees[0] != trees[1] || filepath.Dir(trees[0]) != filepath.Join(state, "pi-extensions") {
		t.Fatalf("the extensions were extracted to %q", trees)
	}
}

// recordedWorkers notes the sessions that the gateway was asked to move.
type recordedWorkers struct{ moves []string }

func (workers *recordedWorkers) Move(_ context.Context, key, _, worker string, _ func(context.Context, func(string) bool) (func(), error)) error {
	workers.moves = append(workers.moves, key+" to "+worker)
	return nil
}

func (workers *recordedWorkers) Return(_ context.Context, key string, _ func(context.Context, func(string) bool) (func(), error)) error {
	workers.moves = append(workers.moves, key+" back")
	return nil
}

func (workers *recordedWorkers) Forward(_ context.Context, key string, port int) (int, error) {
	workers.moves = append(workers.moves, fmt.Sprintf("%s forwards %d", key, port))
	return port + 1, nil
}

func (workers *recordedWorkers) Unforward(key string, port int) error {
	workers.moves = append(workers.moves, fmt.Sprintf("%s unforwards %d", key, port))
	return nil
}

func (workers *recordedWorkers) List() ([]gateway.Worker, error) { return nil, nil }

func (workers *recordedWorkers) Location(string) (gateway.WorkerLocation, bool) {
	return gateway.WorkerLocation{}, false
}

func (workers *recordedWorkers) Placed() map[string]string { return nil }

func (workers *recordedWorkers) Selection(_, cwd string) (string, []string, error) {
	return cwd, []string{cwd}, nil
}

func (workers *recordedWorkers) Notices(string) []gateway.WorkerNotice { return nil }

func (workers *recordedWorkers) DismissNotices(context.Context, string) error { return nil }

func TestWorkerMoveAndReturnTakeASession(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	workers := &recordedWorkers{}
	browser.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)

	if code, stdout, stderr := runCLI("worker", "move", "0a1-alpha", "beelink"); code != 0 || !strings.Contains(stdout, "0a1-alpha runs on beelink") {
		t.Fatalf("worker move = %d %q %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runCLI("worker", "return", alpha); code != 0 || !strings.Contains(stdout, "0a1-alpha runs on this machine") {
		t.Fatalf("worker return = %d %q %q", code, stdout, stderr)
	}
	if expected := []string{alpha + " to beelink", alpha + " back"}; !reflect.DeepEqual(workers.moves, expected) {
		t.Fatalf("moves = %q, want %q", workers.moves, expected)
	}
	if code, _, stderr := runCLI("worker", "move", "no-such-session", "beelink"); code != 1 || !strings.Contains(stderr, "no session matches") {
		t.Fatalf("worker move of nothing = %d %q", code, stderr)
	}
}

func TestWorkerForwardAndUnforwardTakeASessionAndAPort(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	workers := &recordedWorkers{}
	browser.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)

	if code, stdout, stderr := runCLI("worker", "forward", "0a1-alpha", "5173"); code != 0 || stdout != "Port 5173 of the worker is at localhost:5174.\n" {
		t.Fatalf("worker forward = %d %q %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runCLI("worker", "unforward", alpha, "5173"); code != 0 || !strings.Contains(stdout, "5173") {
		t.Fatalf("worker unforward = %d %q %q", code, stdout, stderr)
	}
	if expected := []string{alpha + " forwards 5173", alpha + " unforwards 5173"}; !reflect.DeepEqual(workers.moves, expected) {
		t.Fatalf("asked the gateway: %q, want %q", workers.moves, expected)
	}
	for _, arguments := range [][]string{{"forward", alpha}, {"forward", alpha, "0"}, {"unforward", alpha, "web"}} {
		if code, _, stderr := runCLI(append([]string{"worker"}, arguments...)...); code != 2 {
			t.Fatalf("worker %q = %d %q", arguments, code, stderr)
		}
	}
}

func TestWorkerRemoveRefusesAWorkerThatHasSessions(t *testing.T) {
	fakeSSH(t)
	if code, _, stderr := runCLI("worker", "add", "gripi@beelink", "--name", "beelink"); code != 0 {
		t.Fatalf("worker add: %s", stderr)
	}
	placements, err := remote.LoadPlacements(os.Getenv("GRIPI_WORKER_PLACEMENTS_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	if err := placements.Add(remote.Placement{Session: "/sessions/a.jsonl", Move: "m1", Worker: "beelink"}); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := runCLI("worker", "remove", "beelink"); code != 1 || !strings.Contains(stderr, "/sessions/a.jsonl") || !strings.Contains(stderr, "gripi worker return") {
		t.Fatalf("worker remove of a worker with a session = %d %q", code, stderr)
	}
	if workers := listedWorkers(t); len(workers) != 1 {
		t.Fatalf("workers = %v", workers)
	}
}

func TestTheGatewaySeesTheSavedWorkersAndWhereSessionsRun(t *testing.T) {
	fakeSSH(t)
	if code, _, stderr := runCLI("worker", "add", "gripi@beelink", "--name", "beelink"); code != 0 {
		t.Fatalf("worker add: %s", stderr)
	}
	placements, err := remote.LoadPlacements(os.Getenv("GRIPI_WORKER_PLACEMENTS_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	if err := placements.Add(remote.Placement{Session: "/sessions/a.jsonl", Move: "m1", Worker: "beelink", Repository: "/work/project/.git"}); err != nil {
		t.Fatal(err)
	}
	if err := placements.Add(remote.Placement{Session: "/sessions/b.jsonl", Move: "m2", Worker: "beelink", Repository: "/work/project/.git"}); err != nil {
		t.Fatal(err)
	}
	if err := placements.Finish("/sessions/b.jsonl", &remote.Notice{
		Session: "/sessions/b.jsonl", Worker: "beelink", Repository: "/work/project", Recovery: "/state/returns/m2",
		Result: reconcile.Result{Aside: []reconcile.Item{{Path: "/work/project/README", Reason: "both changed it"}}, Notice: "Main kept its own version."},
	}); err != nil {
		t.Fatal(err)
	}
	workers := gatewayWorkers{remote.NewManager(remote.ManagerOptions{Placements: placements}), os.Getenv("GRIPI_WORKERS_PATH")}

	if list, err := workers.List(); err != nil || !reflect.DeepEqual(list, []gateway.Worker{{Name: "beelink", Destination: "gripi@beelink", State: "disconnected"}}) {
		t.Fatalf("workers = %+v, %v", list, err)
	}
	if location, found := workers.Location("/sessions/a.jsonl"); !found || !reflect.DeepEqual(location, gateway.WorkerLocation{Worker: "beelink", State: "disconnected", Repository: "/work/project", Ports: []gateway.WorkerPort{}}) {
		t.Fatalf("location = %+v, %v", location, found)
	}
	if _, found := workers.Location("/sessions/b.jsonl"); found {
		t.Fatal("a session on main runs on a worker")
	}
	if notices := workers.Notices("/sessions/b.jsonl"); !reflect.DeepEqual(notices, []gateway.WorkerNotice{{
		Repository: "/work/project", Worker: "beelink", Aside: "/state/returns/m2/worker", Paths: []string{"/work/project/README"},
		Items: []gateway.WorkerNoticeItem{{Path: "/work/project/README", Reason: "both changed it"}}, Refs: []string{}, Text: "Main kept its own version.",
	}}) {
		t.Fatalf("notices = %+v", notices)
	}
}
