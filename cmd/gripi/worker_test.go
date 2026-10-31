package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/config"
)

// fakeSSHScript stands in for ssh. It records its arguments, then fails as ssh does with FAKE_SSH_ERROR, answers
// FAKE_SSH_ANSWER, or runs the remote command on this machine with FAKE_SSH_HOME as its home.
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
	t.Setenv("FAKE_SSH_HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("FAKE_SSH_ERROR", "")
	t.Setenv("FAKE_SSH_ANSWER", "")
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
	t.Setenv("FAKE_SSH_ANSWER", system+"\n"+machine+"\ngripi_worker\n/home/gripi_worker\ngit\nsystemd\n")

	code, stdout, stderr := runCLI("worker", "add", "gripi_worker@beelink")
	if code != 1 || stdout != "" {
		t.Fatalf("gripi worker add = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	home := os.Getenv("HOME")
	for _, expected := range []string{filepath.Join(home, ".pi", "agent"), "/home/gripi_worker", "sudo usermod -m -d " + home + " gripi_worker", "same paths"} {
		if !strings.Contains(stderr, expected) {
			t.Fatalf("stderr %q does not contain %q", stderr, expected)
		}
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

func TestWorkerAddWarnsWhenTheWorkersHomeHasAnotherPath(t *testing.T) {
	fakeSSH(t)
	system, machine := thisSystem(t)
	t.Setenv("FAKE_SSH_ANSWER", system+"\n"+machine+"\ngripi_worker\n/home/gripi_worker\ngit\nsystemd\nagent\n")

	code, stdout, stderr := runCLI("worker", "add", "gripi_worker@beelink")
	if code != 0 || !strings.Contains(stdout, "beelink") {
		t.Fatalf("gripi worker add = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, expected := range []string{"/home/gripi_worker", "sudo usermod -m -d " + os.Getenv("HOME") + " gripi_worker", "same paths"} {
		if !strings.Contains(stderr, expected) {
			t.Fatalf("stderr %q does not contain %q", stderr, expected)
		}
	}
	if workers := listedWorkers(t); len(workers) != 1 || workers[0]["name"] != "beelink" {
		t.Fatalf("workers = %v", workers)
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
