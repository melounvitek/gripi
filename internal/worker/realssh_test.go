package worker

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// These tests connect with the real ssh to GRIPI_TEST_SSH, such as localhost, and install the helper in
// GRIPI_TEST_SSH_DIR there (gripi-helper-test in the home folder when unset), which they remove afterwards. The
// worker needs a systemd user manager, and no other helper may run there.

type realWorker struct {
	t           *testing.T
	destination string
	ssh         string // the real ssh, which the stall tests hide behind a relay
	conn        *Conn
	options     Options
}

func connectRealWorker(t *testing.T, lease time.Duration) *realWorker {
	t.Helper()
	destination := os.Getenv("GRIPI_TEST_SSH")
	if destination == "" {
		t.Skip("set GRIPI_TEST_SSH to a worker that ssh reaches, such as localhost")
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	gripi, _ := buildExecutables(t)
	// Control sockets need short paths.
	socketDir, err := os.MkdirTemp("", "gw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	unit := make([]byte, 4)
	rand.Read(unit)
	worker := &realWorker{t: t, destination: destination, ssh: ssh, options: Options{
		Destination: destination, SocketDir: socketDir, Executable: gripi, Lease: lease,
		InstallDir: cmp.Or(os.Getenv("GRIPI_TEST_SSH_DIR"), "gripi-helper-test"), Unit: "gripi-test-" + hex.EncodeToString(unit),
	}}
	t.Cleanup(func() { worker.direct("rm -rf " + ShellQuote(worker.options.InstallDir)) })
	start := time.Now()
	if worker.conn, err = Connect(context.Background(), worker.options); err != nil {
		t.Fatal(err)
	}
	t.Logf("connected in %s", time.Since(start).Round(time.Millisecond))
	t.Cleanup(func() { worker.conn.Close() })
	return worker
}

// direct runs a sh script on the worker over a connection of its own, past any stalled master.
func (worker *realWorker) direct(script string) string {
	worker.t.Helper()
	output, err := exec.Command(worker.ssh, "-o", "BatchMode=yes", "--", worker.destination, ShellQuote("sh", "-c", script)).CombinedOutput()
	if err != nil {
		worker.t.Fatalf("ssh %s %q: %v %s", worker.destination, script, err, output)
	}
	return string(output)
}

// startPi creates a session whose Pi is fakePi in the install folder, and returns the PIDs it lists.
func (worker *realWorker) startPi(id string) []string {
	worker.t.Helper()
	dir := worker.conn.installDir
	if _, err := runScript(context.Background(), worker.conn.Pool, strings.NewReader(fakePi), `cat > "$1/pi" && chmod 700 "$1/pi"`, dir); err != nil {
		worker.t.Fatal(err)
	}
	session := Session{ID: id, Cwd: dir, Args: []string{dir + "/pi", "--mode", "rpc"}, Env: []string{"FAKE_PI_PIDS=" + dir + "/" + id + ".pids"}}
	if err := worker.conn.CreateSession(context.Background(), session); err != nil {
		worker.t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if pids := strings.Fields(worker.direct("cat " + ShellQuote(dir+"/"+id+".pids") + " 2>/dev/null || true")); len(pids) == 4 {
			return pids
		}
	}
	worker.t.Fatal("the fake Pi did not start its processes")
	return nil
}

// running returns those of pids that run on the worker.
func (worker *realWorker) running(pids []string) []string {
	worker.t.Helper()
	return strings.Fields(worker.direct(`for pid in ` + strings.Join(pids, " ") + `; do
		[ -r /proc/$pid/stat ] && [ "$(cut -d' ' -f3 /proc/$pid/stat)" != Z ] && echo $pid
	done; true`))
}

func TestRealSSHAnswersPingsWhileOtherChannelsAreSaturated(t *testing.T) {
	worker := connectRealWorker(t, 0)
	ctx := context.Background()
	var transferred atomic.Int64
	counter := writerFunc(func(data []byte) (int, error) { transferred.Add(int64(len(data))); return len(data), nil })
	var bulk []*exec.Cmd
	for _, channel := range []struct {
		remote         string
		stdin          io.Reader
		stdout         io.Writer
		readNothingYet bool
	}{
		{remote: "cat /dev/zero", stdout: counter},
		{remote: "cat > /dev/null", stdin: zeros{}},
		// A channel that nobody reads fills its window and then waits.
		{remote: "cat /dev/zero", readNothingYet: true},
	} {
		command, release, err := worker.conn.Command(ctx, channel.remote)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		command.Stdin, command.Stdout = channel.stdin, channel.stdout
		if channel.readNothingYet {
			if _, err := command.StdoutPipe(); err != nil {
				t.Fatal(err)
			}
		}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		bulk = append(bulk, command)
	}
	defer func() {
		for _, command := range bulk {
			command.Process.Kill()
			command.Wait()
		}
	}()

	var longest time.Duration
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		longest = max(longest, time.Since(worker.conn.LastAnswer()))
	}
	t.Logf("received %d MB on the bulk channel; longest time without an answer: %s", transferred.Load()>>20, longest.Round(time.Millisecond))
	if longest > 3*time.Second {
		t.Fatalf("main went %s without answering a ping", longest)
	}
	select {
	case <-worker.conn.Closed():
		t.Fatal("the control channel closed")
	default:
	}
}

type zeros struct{}

func (zeros) Read(buffer []byte) (int, error) { clear(buffer); return len(buffer), nil }

type writerFunc func([]byte) (int, error)

func (write writerFunc) Write(data []byte) (int, error) { return write(data) }

// The worker must have stopped its sessions before main treats it as gone, whichever direction of the link stalls.
func TestRealSSHStallStopsTheWorkerBeforeMainTreatsItAsGone(t *testing.T) {
	for _, direction := range []struct {
		name   string
		signal syscall.Signal
	}{{"to main", syscall.SIGUSR1}, {"to the worker", syscall.SIGUSR2}} {
		t.Run(direction.name, func(t *testing.T) {
			const lease = 4 * time.Second
			relayPID := relaySSH(t)
			worker := connectRealWorker(t, lease)
			pids := worker.startPi("s1")
			time.Sleep(1500 * time.Millisecond)

			pid, _ := os.ReadFile(relayPID)
			relay, _ := strconv.Atoi(strings.TrimSpace(string(pid)))
			if err := syscall.Kill(relay, direction.signal); err != nil {
				t.Fatal(err)
			}
			stalled := time.Now()
			// Just before main treats the worker as gone, its sessions must be over.
			select {
			case <-worker.conn.Gone():
				t.Fatalf("main treats the worker as gone %s after the stall", time.Since(stalled))
			case <-time.After(time.Until(worker.conn.LastAnswer().Add(lease + GoneMargin - time.Second))):
			}
			if left := worker.running(pids); len(left) > 0 {
				t.Fatalf("processes still running %s after the stall: %v", time.Since(stalled), left)
			}
			select {
			case <-worker.conn.Gone():
				t.Logf("gone %s after the stall, %s after the last answer", time.Since(stalled).Round(time.Millisecond), time.Since(worker.conn.LastAnswer()).Round(time.Millisecond))
			case <-time.After(lease + GoneMargin + 5*time.Second):
				t.Fatal("main does not treat the stalled worker as gone")
			}
		})
	}
}

func TestRealSSHMasterDeathStopsTheSessionsAtOnce(t *testing.T) {
	worker := connectRealWorker(t, 0)
	pids := worker.startPi("s1")

	worker.conn.Pool.mu.Lock()
	first := worker.conn.Pool.masters[0]
	worker.conn.Pool.mu.Unlock()
	first.process.Process.Kill()
	killed := time.Now()
	select {
	case <-worker.conn.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("the control channel is still open")
	}
	for deadline := time.Now().Add(5 * time.Second); len(worker.running(pids)) > 0; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("processes still running: %v", worker.running(pids))
		}
	}
	t.Logf("sessions stopped within %s", time.Since(killed).Round(time.Millisecond))
	select {
	case <-worker.conn.Gone():
		t.Fatal("main treats the worker as gone before the lease is over")
	default:
	}

	if _, err := runScript(context.Background(), worker.conn.Pool, nil, "true"); err != nil {
		t.Fatalf("channel after the master died: %v", err)
	}
}

// relaySSH makes ssh reach the worker through a relay in this process's executable, which drops what the worker
// sends on SIGUSR1 and what main sends on SIGUSR2. It returns the file that names the relay's PID.
func relaySSH(t *testing.T) string {
	t.Helper()
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	pidFile := filepath.Join(directory, "relay.pid")
	wrapper := fmt.Sprintf(`#!/bin/sh
for argument; do
	case $argument in -MNnT) exec %s -o %s "$@" ;; esac
done
exec %s "$@"
`, ShellQuote(ssh), ShellQuote(fmt.Sprintf("ProxyCommand=env GRIPI_TEST_RELAY=%s %s %%h %%p", pidFile, executable)), ShellQuote(ssh))
	if err := os.WriteFile(filepath.Join(directory, "ssh"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return pidFile
}

// relay is the ProxyCommand of relaySSH. It keeps reading both directions after it drops one, so TCP stays healthy
// and only ssh's own keepalive could notice.
func relay(pidFile, host, port string) {
	connection, err := net.Dial("tcp", net.JoinHostPort(host, port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "relay:", err)
		os.Exit(1)
	}
	os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0600)
	var toMainDropped, toWorkerDropped atomic.Bool
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for received := range signals {
			if received == syscall.SIGUSR1 {
				toMainDropped.Store(true)
			} else {
				toWorkerDropped.Store(true)
			}
		}
	}()
	pump := func(destination io.Writer, source io.Reader, dropped *atomic.Bool) {
		buffer := make([]byte, 32<<10)
		for {
			count, err := source.Read(buffer)
			if count > 0 && !dropped.Load() {
				if _, err := destination.Write(buffer[:count]); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go func() {
		pump(connection, os.Stdin, &toWorkerDropped)
		connection.(*net.TCPConn).CloseWrite()
	}()
	pump(os.Stdout, connection, &toMainDropped)
}
