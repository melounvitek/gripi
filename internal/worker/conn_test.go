package worker

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
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

func requireSystemd(t *testing.T) {
	t.Helper()
	if exec.Command("systemd-run", "--user", "--pipe", "--quiet", "true").Run() != nil {
		t.Skip("needs a systemd user manager")
	}
}

// localOptions connect to a worker that fakeSSH runs on this machine, with a helper of its own.
func localOptions(t *testing.T) Options {
	t.Helper()
	gripi, _ := buildExecutables(t)
	unit := make([]byte, 6)
	rand.Read(unit)
	return Options{
		Destination: "gripi@worker", SocketDir: t.TempDir(), Executable: gripi, InstallDir: "gripi-worker",
		Unit: "gripi-test-" + hex.EncodeToString(unit), RuntimeDir: filepath.Join(t.TempDir(), "run"),
	}
}

// startFakePi creates a session whose Pi is fakePi, and returns the PIDs of it and of the processes it started.
func startFakePi(t *testing.T, conn *Conn, folder, id string) []int {
	t.Helper()
	pi, pids := filepath.Join(folder, "pi"), filepath.Join(folder, id+".pids")
	if err := os.WriteFile(pi, []byte(fakePi), 0700); err != nil {
		t.Fatal(err)
	}
	session := Session{ID: id, Cwd: folder, Args: []string{pi, "--mode", "rpc"}, Env: []string{"FAKE_PI_PIDS=" + pids}}
	if err := conn.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		contents, _ := os.ReadFile(pids)
		if fields := strings.Fields(string(contents)); len(fields) == 4 {
			var result []int
			for _, field := range fields {
				pid, _ := strconv.Atoi(field)
				result = append(result, pid)
			}
			return result
		}
	}
	t.Fatal("the fake Pi did not start its processes")
	return nil
}

// running reports whether a process exists and is not a zombie.
func running(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err == nil && strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))[0] != "Z"
}

func assertGone(t *testing.T, within time.Duration, pids ...int) {
	t.Helper()
	for deadline := time.Now().Add(within); slices.ContainsFunc(pids, running); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("processes still running: %v", slices.DeleteFunc(slices.Clone(pids), func(pid int) bool { return !running(pid) }))
		}
	}
}

func TestConnectRunsSessionsOnTheWorker(t *testing.T) {
	requireSystemd(t)
	home := fakeSSH(t)
	options := localOptions(t)
	ctx := context.Background()
	conn, err := Connect(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	pids := startFakePi(t, conn, home, "s1")

	attach, release, err := conn.Attach(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	input, _ := attach.StdinPipe()
	stdout, _ := attach.StdoutPipe()
	if err := attach.Start(); err != nil {
		t.Fatal(err)
	}
	output := bufio.NewReader(stdout)
	if where, _ := output.ReadString('\n'); where != fmt.Sprintf("socket=%s cwd=%s args=--mode rpc\n", filepath.Join(options.RuntimeDir, "s1.sock"), home) {
		t.Fatalf("Pi says %q", where)
	}
	fmt.Fprintln(input, `{"type":"get_state"}`)
	if echo, _ := output.ReadString('\n'); echo != `{"type":"get_state"}`+"\n" {
		t.Fatalf("Pi answered %q", echo)
	}
	input.Close()
	if err := attach.Wait(); err != nil {
		t.Fatal(err)
	}
	release()
	assertGone(t, time.Second, pids[0])
	if !running(pids[1]) {
		t.Fatal("closing Pi killed what it started")
	}

	if err := conn.ReleaseSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	assertGone(t, time.Second, pids...)
}

func TestClosingTheConnectionStopsTheWorkersSessions(t *testing.T) {
	requireSystemd(t)
	home := fakeSSH(t)
	options := localOptions(t)
	conn, err := Connect(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	pids := startFakePi(t, conn, home, "s1")

	conn.Close()
	assertGone(t, time.Second, pids...)
	if output, _ := exec.Command("systemctl", "--user", "is-active", options.Unit).Output(); strings.TrimSpace(string(output)) == "active" {
		t.Fatal("the helper still runs")
	}
	if _, err := os.Stat(options.SocketDir); err != nil {
		t.Fatalf("the folder for control sockets: %v", err)
	}
	if entries, _ := os.ReadDir(options.SocketDir); len(entries) != 0 {
		t.Fatalf("control sockets left: %v", entries)
	}
}

func TestConnectRefusesASecondHelper(t *testing.T) {
	requireSystemd(t)
	fakeSSH(t)
	options := localOptions(t)
	conn, err := Connect(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	options.Unit += "-second"
	if second, err := Connect(context.Background(), options); err == nil || !strings.Contains(err.Error(), "another helper") {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second Connect = %v", err)
	}
}
