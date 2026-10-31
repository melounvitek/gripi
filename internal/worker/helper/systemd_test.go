package helper_test

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/helper"
)

// fakePi stands in for Pi. It starts processes the way Pi's tools do, in the background, in a session of their own
// and orphaned, and lists their PIDs after its own in FAKE_PI_PIDS. Then it says where it runs and echoes its input.
const fakePi = `#!/bin/sh
echo $$ > "$FAKE_PI_PIDS"
sleep 1000 &
echo $! >> "$FAKE_PI_PIDS"
setsid sh -c 'echo $$ >> "$FAKE_PI_PIDS"; exec sleep 1000' &
(sh -c 'echo $$ >> "$FAKE_PI_PIDS"; exec sleep 1000' &)
printf 'socket=%s cwd=%s args=%s\n' "$GRIPI_WORKER_SOCKET" "$PWD" "$*"
while read -r line; do printf '%s\n' "$line"; done
`

var gripiBuild struct {
	once           sync.Once
	folder, binary string
	err            error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if gripiBuild.folder != "" {
		os.RemoveAll(gripiBuild.folder)
	}
	os.Exit(code)
}

// buildGripi builds Gripi as main uploads it to workers.
func buildGripi(t *testing.T) string {
	t.Helper()
	gripiBuild.once.Do(func() {
		if gripiBuild.folder, gripiBuild.err = os.MkdirTemp("", "gripi-helper-test-"); gripiBuild.err != nil {
			return
		}
		gripiBuild.binary = filepath.Join(gripiBuild.folder, "gripi")
		command := exec.Command("go", "build", "-o", gripiBuild.binary, "github.com/melounvitek/gripi/cmd/gripi")
		command.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := command.CombinedOutput(); err != nil {
			gripiBuild.err = fmt.Errorf("go build: %v\n%s", err, output)
		}
	})
	if gripiBuild.err != nil {
		t.Fatal(gripiBuild.err)
	}
	return gripiBuild.binary
}

// unitHelper is `gripi worker serve` running under systemd as main runs it on a worker.
type unitHelper struct {
	*fakeMain
	runtimeDir string
	instance   string
	pi         string
}

func startUnitHelper(t *testing.T, lease time.Duration) *unitHelper {
	t.Helper()
	if exec.Command("systemd-run", "--user", "--pipe", "--quiet", "true").Run() != nil {
		t.Skip("needs a systemd user manager")
	}
	gripi := buildGripi(t)
	folder := t.TempDir()
	pi := filepath.Join(folder, "pi")
	if err := os.WriteFile(pi, []byte(fakePi), 0700); err != nil {
		t.Fatal(err)
	}
	unit := make([]byte, 6)
	rand.Read(unit)
	runtimeDir := filepath.Join(folder, "run")
	command := exec.Command("systemd-run", "--user", "--pipe", "--quiet", "--collect", "--unit", "gripi-test-"+hex.EncodeToString(unit),
		"-p", "Delegate=yes", "-p", "KillMode=control-group", "-p", "WatchdogSec=10s", "-p", "WatchdogSignal=SIGKILL",
		gripi, "worker", "serve", "--lease", lease.String(), "--runtime-dir", runtimeDir)
	command.Stderr = os.Stderr
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	main := newFakeMain(t, input, output)
	go func() { main.stopped(command.Wait()) }()
	hello := main.next()
	main.answer(main.nextPing())
	return &unitHelper{fakeMain: main, runtimeDir: runtimeDir, instance: hello.Instance, pi: pi}
}

// createSession starts the fake Pi and returns the PIDs of it and of the processes it started.
func (unit *unitHelper) createSession(id string) []int {
	unit.t.Helper()
	pids := filepath.Join(unit.t.TempDir(), "pids")
	response := unit.request(map[string]any{
		"type": "session.create", "session": id, "cwd": filepath.Dir(unit.pi),
		"args": []string{unit.pi, "--mode", "rpc"}, "env": []string{"FAKE_PI_PIDS=" + pids},
	})
	if !response.OK {
		unit.t.Fatalf("session.create = %+v", response)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		contents, _ := os.ReadFile(pids)
		if lines := strings.Fields(string(contents)); len(lines) == 4 {
			var result []int
			for _, line := range lines {
				pid, _ := strconv.Atoi(line)
				result = append(result, pid)
			}
			return result
		}
	}
	unit.t.Fatal("the fake Pi did not start its processes")
	return nil
}

// attach runs `gripi worker attach` for a session and returns what passes its RPC through.
func (unit *unitHelper) attach(id string) (command *exec.Cmd, input io.WriteCloser, output *bufio.Reader) {
	unit.t.Helper()
	command = exec.Command(buildGripi(unit.t), "worker", "attach", "--runtime-dir", unit.runtimeDir, id, unit.instance)
	command.Stderr = os.Stderr
	input, _ = command.StdinPipe()
	stdout, _ := command.StdoutPipe()
	if err := command.Start(); err != nil {
		unit.t.Fatal(err)
	}
	unit.t.Cleanup(func() { command.Process.Kill(); command.Wait() })
	return command, input, bufio.NewReader(stdout)
}

// running reports whether a process exists and is not a zombie.
func running(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return fields[0] != "Z"
}

func assertRunning(t *testing.T, pids ...int) {
	t.Helper()
	for _, pid := range pids {
		if !running(pid) {
			t.Fatalf("process %d is not running", pid)
		}
	}
}

func assertGone(t *testing.T, within time.Duration, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(within)
	for slices.ContainsFunc(pids, running) {
		if time.Now().After(deadline) {
			t.Fatalf("processes still running: %v", slices.DeleteFunc(slices.Clone(pids), func(pid int) bool { return !running(pid) }))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReleaseKillsEveryProcessOfTheSession(t *testing.T) {
	unit := startUnitHelper(t, helper.DefaultLease)
	pids := unit.createSession("s1")
	assertRunning(t, pids...)
	for _, pid := range pids {
		cgroup, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
		if !strings.HasSuffix(strings.TrimSpace(string(cgroup)), ".service/session-s1") {
			t.Fatalf("process %d runs in %s", pid, cgroup)
		}
	}

	if response := unit.request(map[string]any{"type": "session.release", "session": "s1"}); !response.OK {
		t.Fatalf("session.release = %+v", response)
	}
	assertGone(t, time.Second, pids...)
	if _, err := os.Stat(filepath.Join(unit.runtimeDir, "s1.sock")); !os.IsNotExist(err) {
		t.Fatalf("extension socket after release: %v", err)
	}
}

func TestClosingTheAttachStreamStopsPiAndKeepsWhatItStarted(t *testing.T) {
	unit := startUnitHelper(t, helper.DefaultLease)
	pids := unit.createSession("s1")
	attach, input, output := unit.attach("s1")
	output.ReadString('\n')

	input.Close()
	done := make(chan error, 1)
	go func() { done <- attach.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gripi worker attach: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gripi worker attach did not end with Pi")
	}
	assertGone(t, time.Second, pids[0])
	assertRunning(t, pids[1:]...)

	again := unit.createSession("s1")
	if response := unit.request(map[string]any{"type": "session.release", "session": "s1"}); !response.OK {
		t.Fatalf("session.release = %+v", response)
	}
	assertGone(t, time.Second, append(pids, again...)...)
}

func TestSessionCloseStopsPiAndKeepsWhatItStarted(t *testing.T) {
	unit := startUnitHelper(t, helper.DefaultLease)
	pids := unit.createSession("s1")

	if response := unit.request(map[string]any{"type": "session.close", "session": "s1"}); !response.OK {
		t.Fatalf("session.close = %+v", response)
	}
	assertGone(t, 0, pids[0])
	assertRunning(t, pids[1:]...)
}

func TestAttachPassesPisRPCThroughOnlyForItsHelper(t *testing.T) {
	unit := startUnitHelper(t, helper.DefaultLease)
	unit.createSession("s1")

	stranger := exec.Command(buildGripi(t), "worker", "attach", "--runtime-dir", unit.runtimeDir, "s1", "0123456789abcdef")
	if output, err := stranger.CombinedOutput(); err == nil || !strings.Contains(string(output), "not 0123456789abcdef") {
		t.Fatalf("attach for another helper = %v, %s", err, output)
	}
	_, input, output := unit.attach("s1")
	where, _ := output.ReadString('\n')
	if expected := fmt.Sprintf("socket=%s cwd=%s args=--mode rpc\n", filepath.Join(unit.runtimeDir, "s1.sock"), filepath.Dir(unit.pi)); where != expected {
		t.Fatalf("Pi says %q, want %q", where, expected)
	}
	fmt.Fprintln(input, `{"type":"get_state"}`)
	if echo, _ := output.ReadString('\n'); echo != `{"type":"get_state"}`+"\n" {
		t.Fatalf("Pi answered %q", echo)
	}
	second := exec.Command(buildGripi(t), "worker", "attach", "--runtime-dir", unit.runtimeDir, "s1", unit.instance)
	if output, err := second.CombinedOutput(); err == nil || !strings.Contains(string(output), "attached already") {
		t.Fatalf("second attach = %v, %s", err, output)
	}
}

func TestLeaseExpiryKillsEveryProcessAndStopsTheHelper(t *testing.T) {
	const lease = 2 * time.Second
	unit := startUnitHelper(t, lease)
	unit.answering.Store(true)
	pids := unit.createSession("s1")

	unit.answering.Store(false)
	stoppedAnswering := time.Now()
	select {
	case <-unit.served:
	case <-time.After(lease + 2*time.Second):
		t.Fatal("the helper still runs")
	}
	// The last answered ping may have been sent up to a ping interval before the answer.
	if elapsed := time.Since(stoppedAnswering); elapsed < lease-helper.PingInterval-100*time.Millisecond {
		t.Fatalf("the helper stopped %s after the last answer, before its lease of %s", elapsed, lease)
	}
	assertGone(t, time.Second, pids...)
}

func TestExtensionSocketFailsClosedWithoutAMirror(t *testing.T) {
	unit := startUnitHelper(t, helper.DefaultLease)
	unit.createSession("s1")

	connection, err := net.Dial("unix", filepath.Join(unit.runtimeDir, "s1.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	fmt.Fprint(connection, `{"id":1,"type":"barrier"}`+"\n"+`{"id":2,"type":"health"}`+"\n")
	reader := bufio.NewReader(connection)
	var answers []string
	for range 2 {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		answers = append(answers, strings.TrimSpace(line))
	}
	slices.Sort(answers)
	if answers[0] != `{"id":1,"ok":false,"error":"nothing mirrors this session to main"}` || answers[1] != `{"id":2,"ok":false}` {
		t.Fatalf("answers = %q", answers)
	}
}

func TestAuthApplyWritesTheWorkersLogins(t *testing.T) {
	unit := startUnitHelper(t, helper.DefaultLease)
	path := filepath.Join(t.TempDir(), "auth.json")

	if response := unit.request(map[string]any{"type": "auth.apply", "path": path, "contents": `{"fake":{"type":"api_key","key":"k"}}`}); !response.OK {
		t.Fatalf("auth.apply = %+v", response)
	}
	if contents, _ := os.ReadFile(path); string(contents) != `{"fake":{"type":"api_key","key":"k"}}` {
		t.Fatalf("auth.json = %q", contents)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("Pi's lock after the write: %v", err)
	}
}
