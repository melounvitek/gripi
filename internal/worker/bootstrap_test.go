package worker

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/helper"
)

// fakeNode stands in for Node: it says where it is, as process.execPath does, and runs scripts with sh.
const fakeNode = "#!/bin/sh\n[ \"$1\" = -p ] && exec echo \"$0\"\nexec /bin/sh \"$@\"\n"

// fakePiScript stands in for Pi's script; its version is the one in its machine's home folder.
const fakePiScript = "#!/usr/bin/env node\ncat \"$HOME/pi-version\"\n"

// bootstrapRig is a main whose Pi is fakePiScript in a package with dependencies, run by fakeNode through a version
// manager's shim, and a worker that fakeSSH runs on this machine with the helper installed, which serves in this
// process.
type bootstrapRig struct {
	conn                 *Conn
	pi                   Pi
	node, pkg, workerDir string // workerDir is the install folder on the worker
	mainHome, workerHome string
}

func newBootstrapRig(t *testing.T) *bootstrapRig {
	t.Helper()
	gripi, _ := buildExecutables(t) // before HOME changes, which holds Go's caches
	rig := &bootstrapRig{workerHome: fakeSSH(t), mainHome: t.TempDir()}
	t.Setenv("HOME", rig.mainHome)
	ctx := context.Background()
	pool := newTestPool(t)
	dir, _, err := installHelper(ctx, pool, gripi, must(HelperVersion(gripi)), "gripi-worker")
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := shortTempDir(t)
	rig.conn = &Conn{Pool: pool, Control: serveHelper(t, runtimeDir), installDir: dir, options: Options{Destination: "gripi@worker", RuntimeDir: runtimeDir}}
	rig.workerDir = dir

	main := t.TempDir()
	rig.node = filepath.Join(main, "node", "node")
	rig.pkg = filepath.Join(main, "lib", "node_modules", "pi")
	writeTestFile(t, rig.node, fakeNode, 0755)
	writeTestFile(t, filepath.Join(rig.pkg, "package.json"), `{"name": "pi"}`, 0644)
	writeTestFile(t, filepath.Join(rig.pkg, "dist", "cli.js"), fakePiScript, 0755)
	writeTestFile(t, filepath.Join(rig.pkg, "node_modules", "tool", "cli.js"), "tool\n", 0755)
	if err := os.MkdirAll(filepath.Join(rig.pkg, "node_modules", ".bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../tool/cli.js", filepath.Join(rig.pkg, "node_modules", ".bin", "tool")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(main, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../lib/node_modules/pi/dist/cli.js", filepath.Join(main, "bin", "pi")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(main, "bin", "node"), "#!/bin/sh\nexec "+rig.node+" \"$@\"\n", 0755)
	t.Setenv("PATH", filepath.Join(main, "bin")+":"+os.Getenv("PATH"))
	rig.setVersions(t, "1.4.0", "1.4.0")

	rig.pi = Pi{Command: []string{"pi", "--approve"}, AgentDir: filepath.Join(main, "agent"), ExtensionDir: filepath.Join(main, "extensions")}
	writeTestFile(t, filepath.Join(rig.pi.AgentDir, "settings.json"), "{}\n", 0644)
	writeTestFile(t, filepath.Join(rig.pi.ExtensionDir, "gripi-tree.ts"), "tree\n", 0600)
	return rig
}

// serveHelper serves a helper in this process with its sockets in runtimeDir, and returns main's end of its control
// channel.
func serveHelper(t *testing.T, runtimeDir string) *Control {
	t.Helper()
	toHelper, input := io.Pipe()
	output, fromHelper := io.Pipe()
	served := make(chan struct{})
	go func() {
		defer close(served)
		if err := helper.Serve(context.Background(), toHelper, fromHelper, helper.Options{RuntimeDir: runtimeDir}); err != nil {
			t.Error(err)
		}
		fromHelper.Close()
	}()
	control, err := NewControl(context.Background(), output, input, "", helper.DefaultLease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		control.Close()
		<-served
	})
	return control
}

func (rig *bootstrapRig) setVersions(t *testing.T, main, worker string) {
	t.Helper()
	writeTestFile(t, filepath.Join(rig.mainHome, "pi-version"), main+"\n", 0644)
	writeTestFile(t, filepath.Join(rig.workerHome, "pi-version"), worker+"\n", 0644)
}

func writeTestFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

func TestBootstrapInstallsMainsPiOnTheWorker(t *testing.T) {
	rig := newBootstrapRig(t)
	ctx := context.Background()
	for _, command := range [][]string{{"pi", "--approve"}, {rig.node, filepath.Join(rig.pkg, "dist", "cli.js"), "--approve"}} {
		rig.pi.Command = command
		installation, err := rig.conn.Bootstrap(ctx, rig.pi)
		if err != nil {
			t.Fatalf("Bootstrap with %q: %v", command, err)
		}
		if got := installation.Command(slices.Concat(command, []string{"--mode", "rpc"})); !slices.Equal(got, []string{rig.workerDir + "/bin/pi", "--approve", "--mode", "rpc"}) {
			t.Fatalf("the worker's command for %q = %q", command, got)
		}
		if contents, err := os.ReadFile(filepath.Join(rig.workerDir, "bin", "node")); err != nil || string(contents) != fakeNode {
			t.Fatalf("the worker's node for %q = %q, %v", command, contents, err)
		}
		// The helper gives Pi its own HOME.
		output, err := runScript(ctx, rig.conn.Pool, nil, `exec env -i HOME="$HOME" "$@" pi --version`, installation.Environment(nil)...)
		if err != nil || output != "1.4.0\n" {
			t.Fatalf("pi --version in the environment of the worker's Pi = %q, %v", output, err)
		}
	}

	if target, err := os.Readlink(filepath.Join(rig.workerDir, "lib", "node_modules", ".bin", "tool")); err != nil || target != "../tool/cli.js" {
		t.Fatalf("the link in Pi's package on the worker = %q, %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(rig.workerDir, "bin", "gripi")); err != nil {
		t.Fatalf("the helper: %v", err)
	}

	// Another bootstrap sends only what changed.
	unchanged, worker := filepath.Join(rig.workerDir, "bin", "node"), filepath.Join(rig.workerDir, "lib", "dist", "cli.js")
	before := inode(t, unchanged)
	writeTestFile(t, filepath.Join(rig.pkg, "dist", "cli.js"), fakePiScript+"# changed\n", 0755)
	writeTestFile(t, filepath.Join(rig.workerDir, "lib", "left-over.js"), "", 0644)
	if _, err := rig.conn.Bootstrap(ctx, rig.pi); err != nil {
		t.Fatal(err)
	}
	if inode(t, unchanged) != before {
		t.Fatal("the second bootstrap sent node again")
	}
	if contents, _ := os.ReadFile(worker); !strings.HasSuffix(string(contents), "# changed\n") {
		t.Fatalf("Pi's script on the worker = %q", contents)
	}
	if _, err := os.Lstat(filepath.Join(rig.workerDir, "lib", "left-over.js")); err == nil {
		t.Fatal("a file that main's Pi package lacks stays on the worker")
	}
}

func TestBootstrapFailsWhenTheWorkersPiHasAnotherVersion(t *testing.T) {
	rig := newBootstrapRig(t)
	rig.setVersions(t, "1.4.0", "1.3.9")

	_, err := rig.conn.Bootstrap(context.Background(), rig.pi)
	if err == nil || !strings.Contains(err.Error(), "1.3.9") || !strings.Contains(err.Error(), "1.4.0") {
		t.Fatalf("Bootstrap = %v", err)
	}
}

func TestBootstrapCopiesPisAgentFolderWithoutLoginsOrSessions(t *testing.T) {
	rig := newBootstrapRig(t)
	main, worker, outside := rig.pi.AgentDir, filepath.Join(t.TempDir(), "agent"), t.TempDir()
	for _, name := range []string{"auth.json", "auth.json.lock/lock", "mcp-auth.json", "sessions/s.jsonl", "npm/node_modules/p/index.js", "models.json"} {
		writeTestFile(t, filepath.Join(main, name), "main's\n", 0600)
	}
	// The user's own configuration, linked from Pi's folder.
	writeTestFile(t, filepath.Join(outside, "skills", "s", "SKILL.md"), "skill\n", 0644)
	if err := os.Symlink(filepath.Join(outside, "skills"), filepath.Join(main, "skills")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth.json", "sessions/other.jsonl", "stale.json"} {
		writeTestFile(t, filepath.Join(worker, name), "the worker's\n", 0600)
	}

	if err := rig.conn.copyFolder(context.Background(), main, worker, agentExcluded); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"settings.json": "{}\n", "models.json": "main's\n", "npm/node_modules/p/index.js": "main's\n", "skills/s/SKILL.md": "skill\n",
		"auth.json": "the worker's\n", "sessions/other.jsonl": "the worker's\n", "auth.json.lock/lock": "", "mcp-auth.json": "", "sessions/s.jsonl": "", "stale.json": "",
	} {
		if contents, _ := os.ReadFile(filepath.Join(worker, name)); string(contents) != want {
			t.Errorf("%s on the worker = %q, want %q", name, contents, want)
		}
	}
	if info, err := os.Lstat(filepath.Join(worker, "skills")); err != nil || !info.IsDir() {
		t.Fatalf("skills on the worker: %v, %v", info, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(worker)); len(entries) != 1 {
		t.Fatalf("the copy left %v next to the agent folder", entries)
	}
}

// The worker's helper makes the copies, so they see the files as its mirrors and sessions do, also in a private view.
func TestCopiesToTheWorkerGoThroughItsHelper(t *testing.T) {
	rig := newBootstrapRig(t)
	main, worker := t.TempDir(), filepath.Join(t.TempDir(), "copy")
	writeTestFile(t, filepath.Join(main, "notes"), "main's\n", 0600)
	if err := rig.conn.copyFolder(context.Background(), main, worker, nil); err != nil {
		t.Fatal(err)
	}
	if contents, _ := os.ReadFile(filepath.Join(worker, "notes")); string(contents) != "main's\n" {
		t.Fatalf("the copy has %q", contents)
	}

	rig.conn.Control.Close()
	<-rig.conn.Closed()
	writeTestFile(t, filepath.Join(main, "notes"), "changed\n", 0600)
	if err := rig.conn.copyFolder(context.Background(), main, worker, nil); err == nil || !strings.Contains(err.Error(), "the helper does not listen") {
		t.Fatalf("a copy after the helper stopped = %v", err)
	}
	if contents, _ := os.ReadFile(filepath.Join(worker, "notes")); string(contents) != "main's\n" {
		t.Fatalf("the copy has %q after the helper stopped", contents)
	}
}

func TestBootstrapForwardsTheLocalhostPortsOfPisConfiguration(t *testing.T) {
	rig := newBootstrapRig(t)
	writeTestFile(t, filepath.Join(rig.pi.AgentDir, "models.json"), `{
  // Pi allows comments and trailing commas here.
  "providers": {
    "proxy": {"baseUrl": "http://127.0.0.1:8317/v1", "models": [{"id": "m", "baseUrl": "http://localhost:8318"},]},
    "remote": {"baseUrl": "https://api.example.com/v1"},
    "ollama": {"baseUrl": "http://[::1]:11434"},
    "default-port": {"baseUrl": "http://localhost/v1"},
  },
}`, 0600)
	writeTestFile(t, filepath.Join(rig.pi.AgentDir, "mcp.json"), `{"mcpServers": {
  "proxy": {"url": "http://localhost:8317/mcp"},
  "dev": {"url": "http://localhost:3845/mcp"},
  "stdio": {"command": "npx", "args": ["-y", "server", "http://127.0.0.1:9999"]}
}}`, 0600)

	if _, err := rig.conn.Bootstrap(context.Background(), rig.pi); err != nil {
		t.Fatal(err)
	}
	forwards := sshCalls(t, "-O forward")
	// localhost listens on both 127.0.0.1 and ::1 on the worker, and reaches either here.
	for _, want := range []string{"-R localhost:8317:localhost:8317 ", "-R localhost:8318:localhost:8318 ", "-R localhost:11434:localhost:11434 ", "-R localhost:3845:localhost:3845 "} {
		if !slices.ContainsFunc(forwards, func(call string) bool { return strings.Contains(call, want) }) {
			t.Errorf("no forward %q in %q", want, forwards)
		}
	}
	if len(forwards) != 4 {
		t.Fatalf("forwards = %q", forwards)
	}
}

func TestEnvironmentGivesTheWorkersPiMainsVariablesWithoutMainsMachine(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "key")
	t.Setenv("DISPLAY", ":0")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent")
	t.Setenv("TERM", "xterm")
	t.Setenv("GRIPI_ADMIN_PASSWORD", "secret")
	t.Setenv("PI_CODING_AGENT_DIR", "/elsewhere")
	installation := &Installation{dir: "/home/w/.local/share/gripi/worker", path: "/usr/local/bin:/usr/bin", agentDir: "/home/u/.pi/agent"}

	environment := installation.Environment([]string{"SAVED=1", "USER=me"})
	values := map[string][]string{}
	for _, variable := range environment {
		name, value, _ := strings.Cut(variable, "=")
		values[name] = append(values[name], value)
	}
	for name, want := range map[string]string{
		"ANTHROPIC_API_KEY": "key", "SAVED": "1",
		"PATH":                "/home/w/.local/share/gripi/worker/bin:/usr/local/bin:/usr/bin",
		"PI_CODING_AGENT_DIR": "/home/u/.pi/agent",
	} {
		if !slices.Equal(values[name], []string{want}) {
			t.Errorf("%s = %q, want %q", name, values[name], want)
		}
	}
	for _, name := range []string{"HOME", "USER", "DISPLAY", "XDG_RUNTIME_DIR", "SSH_AUTH_SOCK", "TERM", "GRIPI_ADMIN_PASSWORD"} {
		if values[name] != nil {
			t.Errorf("%s = %q on the worker", name, values[name])
		}
	}
}

// TestRealSSHBootstrap copies this machine's Pi to GRIPI_TEST_SSH (see realssh_test.go), with a Pi agent folder under
// /tmp, which can have the same path on both machines. Against another machine, it also checks that a port in
// models.json reaches this one; against this machine, copying to the same paths changes nothing.
func TestRealSSHBootstrap(t *testing.T) {
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("needs pi")
	}
	worker := connectRealWorker(t, 0)
	ctx := context.Background()
	root, err := os.MkdirTemp("/tmp", "gripi-bootstrap-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		worker.direct("rm -rf " + ShellQuote(root))
		os.RemoveAll(root)
	})
	hostname, _ := os.Hostname()
	elsewhere := strings.TrimSpace(worker.direct("uname -n")) != hostname

	agentDir := filepath.Join(root, "agent")
	writeTestFile(t, filepath.Join(agentDir, "settings.json"), "{}\n", 0600)
	writeTestFile(t, filepath.Join(agentDir, "auth.json"), "{}\n", 0600)
	writeTestFile(t, filepath.Join(agentDir, "sessions", "s.jsonl"), "{}\n", 0600)
	writeTestFile(t, filepath.Join(root, "config", "skills", "s", "SKILL.md"), "skill\n", 0644)
	if err := os.Symlink(filepath.Join(root, "config", "skills"), filepath.Join(agentDir, "skills")); err != nil {
		t.Fatal(err)
	}
	baseURL := "http://127.0.0.1:" + strconv.Itoa(freePort(t))
	if elsewhere {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { fmt.Fprint(response, "main") }))
		defer server.Close()
		baseURL = server.URL
	}
	writeTestFile(t, filepath.Join(agentDir, "models.json"), `{"providers": {"proxy": {"baseUrl": "`+baseURL+`/v1"}}}`, 0600)
	extensionDir := filepath.Join(root, "extensions")
	for _, name := range []string{"gripi-tree.ts", "gripi-worker.ts"} {
		contents, err := os.ReadFile(filepath.Join("..", "..", "pi_extensions", name))
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(extensionDir, name), string(contents), 0600)
	}

	for _, attempt := range []string{"first", "repeated"} {
		start := time.Now()
		if _, err := worker.conn.Bootstrap(ctx, Pi{Command: []string{pi, "--approve"}, AgentDir: agentDir, ExtensionDir: extensionDir}); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s bootstrap: %s", attempt, time.Since(start).Round(time.Millisecond))
	}
	check := `test -f "$1/agent/settings.json" && test -f "$1/agent/skills/s/SKILL.md" && test -f "$1/extensions/gripi-worker.ts" && echo copied`
	if elsewhere {
		check += ` && test ! -e "$1/agent/auth.json" && test ! -e "$1/agent/sessions/s.jsonl" && curl -s "$2"`
	}
	if output := worker.direct(ShellQuote("sh", "-c", check, "sh", root, baseURL) + " || true"); !strings.HasPrefix(output, "copied") || (elsewhere && !strings.HasSuffix(output, "main")) {
		t.Fatalf("on the worker: %q", output)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
