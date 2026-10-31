package worker

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// fakeStore knows the folders of some saved sessions.
type fakeStore struct {
	agent.Store
	sessions map[string]string // key → folder
}

func (store fakeStore) Session(key string) (*agent.Session, bool) {
	cwd, found := store.sessions[key]
	return &agent.Session{Path: key, CWD: cwd}, found
}

func (store fakeStore) Sessions() ([]*agent.Session, error) {
	var sessions []*agent.Session
	for key, cwd := range store.sessions {
		sessions = append(sessions, &agent.Session{Path: key, CWD: cwd})
	}
	return sessions, nil
}

// shortTempDir is a temporary folder with a path short enough for the sockets in it.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gripi-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func newTestManager(t *testing.T, options ManagerOptions) *Manager {
	t.Helper()
	if options.Placements == nil {
		placements, err := LoadPlacements(filepath.Join(t.TempDir(), "worker-placements.json"))
		if err != nil {
			t.Fatal(err)
		}
		options.Placements = placements
	}
	options.Destination = func(name string) (string, error) {
		if name != "beelink" {
			return "", fmt.Errorf("no worker is named %q", name)
		}
		return "gripi@worker", nil
	}
	if options.SessionsRoot == "" {
		options.SessionsRoot = "/home/me/.pi/agent/sessions"
	}
	manager := NewManager(options)
	t.Cleanup(manager.Close)
	return manager
}

func TestSessionsInAPlacedRepositoryAreOnItsWorker(t *testing.T) {
	store := fakeStore{sessions: map[string]string{
		"/home/me/.pi/agent/sessions/--work-project-src--/a.jsonl": "/work/project/src",
		"/home/me/.pi/agent/sessions/--work-other--/b.jsonl":       "/work/other",
	}}
	manager := newTestManager(t, ManagerOptions{Store: store})
	unit := mirror.Unit{Main: "/work/project", Worktrees: []string{"/work/.worktrees/project/feature"}, GitDir: "/work/project/.git"}
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: unit}); err != nil {
		t.Fatal(err)
	}
	beelink, other := manager.Placement("beelink"), manager.Placement("other")

	for path, onBeelink := range map[string]bool{"/work/project/src": true, "/work/.worktrees/project/feature": true, "/work/other": false, "/work/project-other": false} {
		if beelink.Folder(path) != onBeelink || other.Folder(path) {
			t.Errorf("Folder(%s) on beelink = %v", path, beelink.Folder(path))
		}
	}
	for key, onBeelink := range map[string]bool{
		"/home/me/.pi/agent/sessions/--work-project-src--/a.jsonl": true,
		"/home/me/.pi/agent/sessions/--work-other--/b.jsonl":       false,
		// New sessions, which Pi has not saved yet:
		"/home/me/.pi/agent/sessions/--work-project--/new.jsonl":                         true,
		"/home/me/.pi/agent/sessions/--work-.worktrees-project-feature--/new.jsonl":      true,
		"/home/me/.pi/agent/sessions/--work-project-other--/new.jsonl":                   false,
		"/home/me/.pi/agent/sessions/pending-0d5b3a3c-6d8e-4a3e-9d43-3f52f9bb9b1f.jsonl": false,
	} {
		if beelink.Session(key) != onBeelink || other.Session(key) {
			t.Errorf("Session(%s) on beelink = %v", key, beelink.Session(key))
		}
	}

	for _, process := range []agent.Process{{Dir: "/work/other"}, {Key: "/home/me/.pi/agent/sessions/--work-other--/b.jsonl"}, {Key: "/unknown.jsonl"}} {
		if command, _, err := manager.Launch(process); command != nil || err != nil {
			t.Fatalf("Launch(%+v) = %v, %v; want Pi here", process, command, err)
		}
	}
	done, err := manager.startMoving(mirror.Unit{Main: "/work/other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Launch(agent.Process{Key: "/home/me/.pi/agent/sessions/--work-other--/b.jsonl"}); err == nil || !strings.Contains(err.Error(), "moving") {
		t.Fatalf("Launch while the repository moves = %v", err)
	}
	done()
}

// fakeWorkerPi stands in for Pi's script on the worker. It says how it was started, then echoes its input.
const fakeWorkerPi = `#!/usr/bin/env node
if [ "$1" = --version ]; then exec cat "$HOME/pi-version"; fi
printf 'args=%s\n' "$*"
printf 'cwd=%s socket=%s gripi=%s agent=%s saved=%s\n' "$PWD" "${GRIPI_WORKER_SOCKET:+set}" "$GRIPI_SOCKET_PATH" "$PI_CODING_AGENT_DIR" "$SAVED"
while read -r line; do printf '%s\n' "$line"; done
`

func TestLaunchRunsTheSessionsOfAPlacedRepositoryOnTheirWorker(t *testing.T) {
	requireSystemd(t)
	rig := newBootstrapRig(t)
	writeTestFile(t, filepath.Join(rig.pkg, "dist", "cli.js"), fakeWorkerPi, 0755)
	writeTestFile(t, filepath.Join(rig.pi.AgentDir, "auth.json"), `{"anthropic":{"type":"oauth","access":"a","refresh":"main's","expires":1}}`, 0600)
	writeTestFile(t, filepath.Join(rig.pi.ExtensionDir, "gripi-worker.ts"), "worker\n", 0600)
	project, sessions := t.TempDir(), t.TempDir()
	key := filepath.Join(sessionFolder(sessions, project), "s.jsonl")
	gripi, _ := buildExecutables(t)
	unitName := make([]byte, 6)
	rand.Read(unitName)
	var socketsStopped int
	manager := newTestManager(t, ManagerOptions{
		StateDir: t.TempDir(), Pi: rig.pi, WorkerExtension: filepath.Join(rig.pi.ExtensionDir, "gripi-worker.ts"),
		SocketPath: "/run/gripi.sock", Store: fakeStore{sessions: map[string]string{key: project}}, SessionsRoot: sessions,
		ServeSocket: func(name string, placement WorkerPlacement) (string, func() error, error) {
			if name != "beelink" || !placement.Folder(project) {
				t.Errorf("socket for %s, whose placement has %s: %v", name, project, placement.Folder(project))
			}
			return filepath.Join(t.TempDir(), "beelink.sock"), func() error { socketsStopped++; return nil }, nil
		},
		Connection: Options{Executable: gripi, InstallDir: "gripi-worker", Unit: "gripi-test-" + hex.EncodeToString(unitName), RuntimeDir: shortTempDir(t)},
	})
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: mirror.Unit{Main: project}}); err != nil {
		t.Fatal(err)
	}

	tree := filepath.Join(rig.pi.ExtensionDir, "gripi-tree.ts")
	command, release, err := manager.Launch(agent.Process{
		Key: key, Command: []string{"pi", "--approve", "--mode", "rpc", "--extension", tree, "--session", key}, Env: []string{"SAVED=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status := manager.Status("beelink"); status != (Status{State: Connected}) {
		t.Fatalf("status = %+v", status)
	}
	// The worker's helper makes the folder of the session's file, so that it can send main the file.
	if _, err := os.Stat(sessionFolder(sessions, project)); err != nil {
		t.Fatal(err)
	}
	input, _ := command.StdinPipe()
	stdout, _ := command.StdoutPipe()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	output := bufio.NewReader(stdout)
	if args, _ := output.ReadString('\n'); args != fmt.Sprintf("args=--approve --mode rpc --extension %s --session %s --extension %s\n", tree, key, manager.options.WorkerExtension) {
		t.Fatalf("Pi on the worker got %q", args)
	}
	if environment, _ := output.ReadString('\n'); environment != fmt.Sprintf("cwd=%s socket=set gripi=/run/gripi.sock agent=%s saved=1\n", project, rig.pi.AgentDir) {
		t.Fatalf("Pi on the worker says %q", environment)
	}
	// The worker and main share this machine's files here, so the worker's logins replaced main's.
	if logins, _ := os.ReadFile(filepath.Join(rig.pi.AgentDir, "auth.json")); !strings.Contains(string(logins), "gripi:refresh-on-main") {
		t.Fatalf("the worker's auth.json = %s", logins)
	}
	fmt.Fprintln(input, `{"type":"get_state"}`)
	if echo, _ := output.ReadString('\n'); echo != `{"type":"get_state"}`+"\n" {
		t.Fatalf("Pi answered %q", echo)
	}
	input.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	release()

	manager.Close()
	if status := manager.Status("beelink"); status != (Status{State: Disconnected}) || socketsStopped != 1 {
		t.Fatalf("after Close: status %+v, %d sockets stopped", status, socketsStopped)
	}
}

func TestReturnWaitsForASessionStartingOnTheWorker(t *testing.T) {
	project := "/work/project"
	manager := newTestManager(t, ManagerOptions{Store: fakeStore{}})
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: mirror.Unit{Main: project}}); err != nil {
		t.Fatal(err)
	}
	connecting, connect := make(chan struct{}), make(chan struct{})
	manager.options.Destination = func(string) (string, error) {
		close(connecting)
		<-connect
		return "", errors.New("no worker in this test")
	}
	launched := make(chan error, 1)
	go func() {
		_, _, err := manager.Launch(agent.Process{Dir: project})
		launched <- err
	}()
	<-connecting
	stopped, returned := make(chan struct{}), make(chan error, 1)
	go func() {
		returned <- manager.Return(context.Background(), project, func(func(string) bool) error {
			close(stopped)
			return errors.New("stopped")
		})
	}()
	select {
	case <-stopped:
		t.Fatal("the return stopped the sessions while one was starting on the worker")
	case <-time.After(200 * time.Millisecond):
	}
	if _, _, err := manager.Launch(agent.Process{Dir: project}); err == nil || !strings.Contains(err.Error(), "moving") {
		t.Fatalf("a session that starts during the return = %v", err)
	}
	close(connect)
	<-launched
	<-stopped
	<-returned
}

func TestWhatTheMirrorAppliesUpdatesThePlacementAndNotices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-placements.json")
	placements, err := LoadPlacements(path)
	if err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, ManagerOptions{Store: fakeStore{}, Placements: placements})
	unit := mirror.Unit{Main: "/work/project", Worktrees: []string{"/work/project-old"}, GitDir: "/work/project/.git"}
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: unit}); err != nil {
		t.Fatal(err)
	}
	// The worker's mirror sends the folders of the unit, which Git there may have changed.
	manager.applied(unit, false)(mirror.Checkpoint{Roots: []string{"/work/project", "/work/project-new"}}, "/state/aside/main/3", []string{"/work/project/README"})
	beelink := manager.Placement("beelink")
	if !beelink.Folder("/work/project-new/src") || beelink.Folder("/work/project-old") {
		t.Fatalf("placement after a worktree was added and one removed: %+v", manager.options.Placements.All())
	}
	reloaded, err := LoadPlacements(path)
	if err != nil || !slices.Equal(reloaded.All()[0].Unit.Worktrees, []string{"/work/project-new"}) {
		t.Fatalf("saved placements: %+v, %v", reloaded.All(), err)
	}
	// The mirror of the sessions' folders has folders of its own.
	manager.applied(unit, true)(mirror.Checkpoint{Roots: []string{"/sessions/--work-project--"}}, "/state/aside/main/4", []string{"/sessions/--work-project--/s.jsonl"})
	if beelink.Folder("/work/project-new") != true {
		t.Fatal("the sessions' folders changed the placement")
	}

	notices := manager.Notices("/work/project-new")
	if len(notices) != 2 || notices[0].Aside != "/state/aside/main/3" || !slices.Equal(notices[0].Paths, []string{"/work/project/README"}) ||
		notices[1].Repository.Main != unit.Main || !slices.Equal(notices[1].Paths, []string{"/sessions/--work-project--/s.jsonl"}) {
		t.Fatalf("notices = %+v", notices)
	}
	if other := manager.Notices("/work/other"); len(other) != 0 {
		t.Fatalf("notices of another folder = %+v", other)
	}
	manager.DismissNotices("/work/project")
	if notices := manager.Notices("/work/project"); len(notices) != 0 {
		t.Fatalf("notices after dismissing = %+v", notices)
	}
}

// loseWorker makes the manager have lost its connection to the worker of a name, which is gone at goneAt.
func loseWorker(manager *Manager, name string, goneAt time.Time) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.workers[name] = &remote{name: name, state: Disconnected, goneAt: goneAt, mirrors: map[string]*liveMirror{}}
}

func TestARepositoryOnALostWorkerContinuesOnMainOnceTheWorkerIsGone(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	project, sessions, stateDir := t.TempDir(), t.TempDir(), t.TempDir()
	manager := newTestManager(t, ManagerOptions{Store: fakeStore{}, StateDir: stateDir, SessionsRoot: sessions, Clock: func() time.Time { return now }})
	contacted := 0
	manager.options.Destination = func(string) (string, error) {
		contacted++
		return "", errors.New("no worker in this test")
	}
	unit := mirror.Unit{Main: project}
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: unit}); err != nil {
		t.Fatal(err)
	}
	// Main's copies of the repository and of its sessions, and what main saved aside while they were on the worker.
	mains := []string{project, manager.sessionsMain(unit)}
	for _, main := range mains {
		writeTestFile(t, filepath.Join(manager.mirrorFolder(main), "state", "checkpoint.json"), `{"barrier":3}`, 0600)
		writeTestFile(t, filepath.Join(manager.mirrorFolder(main), "aside", "20261031T115900Z", "main", "3", main, "README"), "main's\n", 0600)
	}
	goneAt := now.Add(40 * time.Second)
	loseWorker(manager, "beelink", goneAt)
	if status := manager.Status("beelink"); status != (Status{State: Unreachable, GoneAt: goneAt}) {
		t.Fatalf("status of the lost worker = %+v", status)
	}
	stopped := 0
	stop := func(inRepository func(string) bool) error {
		if !inRepository(project) {
			t.Errorf("stopped the sessions of another repository")
		}
		stopped++
		return nil
	}

	err := manager.Return(context.Background(), project, stop)
	if err == nil || !strings.Contains(err.Error(), "unreachable") || !strings.Contains(err.Error(), goneAt.Local().Format(time.TimeOnly)) {
		t.Fatalf("Return before the worker is gone = %v", err)
	}
	if !manager.Placement("beelink").Folder(project) {
		t.Fatalf("the refused return took the repository off the worker: %+v", manager.options.Placements.All())
	}

	now = goneAt
	if status := manager.Status("beelink"); status != (Status{State: Gone, GoneAt: goneAt}) {
		t.Fatalf("status of the gone worker = %+v", status)
	}
	if err := manager.Return(context.Background(), project, stop); err != nil {
		t.Fatal(err)
	}
	if manager.Placement("beelink").Folder(project) || stopped != 2 || contacted != 0 {
		t.Fatalf("after the return: %d sessions stopped, worker contacted %d times, placements %+v", stopped, contacted, manager.options.Placements.All())
	}
	for _, main := range mains {
		if _, err := os.Stat(filepath.Join(manager.mirrorFolder(main), "state")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("main's mirror of %s is left: %v", main, err)
		}
		if _, err := os.Stat(filepath.Join(manager.mirrorFolder(main), "aside", "20261031T115900Z", "main", "3", main, "README")); err != nil {
			t.Fatalf("what main saved aside: %v", err)
		}
	}
	if command, _, err := manager.Launch(agent.Process{Dir: project}); command != nil || err != nil {
		t.Fatalf("a session in the returned repository runs on the worker: %v", err)
	}
}

// A gateway that cannot reach a worker can still bring its repositories back, a lease after it started, when no
// helper that an earlier gateway answered can run sessions there any more.
func TestReturnContinuesOnMainWhenTheWorkerCannotBeReached(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	project := t.TempDir()
	manager := newTestManager(t, ManagerOptions{Store: fakeStore{}, StateDir: t.TempDir(), SessionsRoot: t.TempDir(), Clock: func() time.Time { return now }})
	manager.options.Destination = func(string) (string, error) { return "", errors.New("No route to host") }
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: mirror.Unit{Main: project}}); err != nil {
		t.Fatal(err)
	}
	stop := func(func(string) bool) error { return nil }
	goneAt := now.Add(helper.DefaultLease + GoneMargin)
	if err := manager.Return(context.Background(), project, stop); err == nil || !strings.Contains(err.Error(), "No route to host") || !strings.Contains(err.Error(), goneAt.Local().Format(time.TimeOnly)) {
		t.Fatalf("Return just after the gateway started = %v", err)
	}
	now = goneAt
	if err := manager.Return(context.Background(), project, stop); err != nil {
		t.Fatal(err)
	}
	if manager.Placement("beelink").Folder(project) {
		t.Fatal("the repository is still on the worker")
	}
}

func TestAWorkerThatMainLosesEndsItsRunningStepsAndComesBack(t *testing.T) {
	requireSystemd(t)
	rig := newBootstrapRig(t)
	writeTestFile(t, filepath.Join(rig.pkg, "dist", "cli.js"), fakeWorkerPi, 0755)
	writeTestFile(t, filepath.Join(rig.pi.ExtensionDir, "gripi-worker.ts"), "worker\n", 0600)
	project, sessions := t.TempDir(), t.TempDir()
	key := filepath.Join(sessionFolder(sessions, project), "s.jsonl")
	gripi, _ := buildExecutables(t)
	unitName := make([]byte, 6)
	rand.Read(unitName)
	unit := "gripi-test-" + hex.EncodeToString(unitName)
	var ahead atomic.Int64 // how far the manager's clock is ahead
	var socketsStarted, socketsStopped atomic.Int32
	manager := newTestManager(t, ManagerOptions{
		StateDir: t.TempDir(), Pi: rig.pi, WorkerExtension: filepath.Join(rig.pi.ExtensionDir, "gripi-worker.ts"),
		SocketPath: "/run/gripi.sock", Store: fakeStore{sessions: map[string]string{key: project}}, SessionsRoot: sessions,
		ServeSocket: func(string, WorkerPlacement) (string, func() error, error) {
			socketsStarted.Add(1)
			return filepath.Join(t.TempDir(), "beelink.sock"), func() error { socketsStopped.Add(1); return nil }, nil
		},
		Connection: Options{Executable: gripi, InstallDir: "gripi-worker", Unit: unit, RuntimeDir: shortTempDir(t)},
		Clock:      func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) },
	})
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: mirror.Unit{Main: project}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// The worker's helper and everything that it runs end, as when the worker reboots.
	killHelper := func() {
		t.Helper()
		if output, err := exec.Command("systemctl", "--user", "kill", "--signal=SIGKILL", unit+".service").CombinedOutput(); err != nil {
			t.Fatalf("kill the helper: %v %s", err, output)
		}
		for deadline := time.Now().Add(5 * time.Second); exec.Command("systemctl", "--user", "is-active", "--quiet", unit+".service").Run() == nil; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the helper still runs")
			}
		}
	}

	lost := make(chan string, 2)
	command, release, err := manager.Launch(agent.Process{Key: key, Command: []string{"pi", "--mode", "rpc", "--session", key}, Lost: func(message string) { lost <- message }})
	if err != nil {
		t.Fatal(err)
	}
	stdout, _ := command.StdoutPipe()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if args, _ := bufio.NewReader(stdout).ReadString('\n'); !strings.HasPrefix(args, "args=") {
		t.Fatalf("Pi on the worker said %q", args)
	}
	killHelper()
	select {
	case message := <-lost:
		if !strings.Contains(message, "worker beelink") || !strings.Contains(message, "outcome of the running step is unknown") {
			t.Fatalf("the session was told %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session was not told that its worker is lost")
	}
	command.Wait()
	release()
	if status := manager.Status("beelink"); status.State != Unreachable || time.Until(status.GoneAt) < helper.DefaultLease || time.Until(status.GoneAt) > helper.DefaultLease+GoneMargin {
		t.Fatalf("status of the lost worker = %+v", status)
	}
	if socketsStopped.Load() != 1 {
		t.Fatalf("%d gripi sockets stopped", socketsStopped.Load())
	}

	// The lost helper may still run for all main knows, and holds the unit until it stops.
	if output, err := exec.Command("systemd-run", "--user", "--quiet", "--collect", "--unit", unit, "sleep", "3").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	started := time.Now()
	if err := manager.Reconnect(ctx, "beelink"); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(started); waited < 2*time.Second {
		t.Fatalf("reconnected after %s, while the old helper ran", waited)
	}
	if status := manager.Status("beelink"); status != (Status{State: Connected}) || socketsStarted.Load() != 2 {
		t.Fatalf("after reconnecting: status %+v, %d gripi sockets started", status, socketsStarted.Load())
	}
	manager.mu.Lock()
	live := manager.workers["beelink"].mirrors[project]
	manager.mu.Unlock()
	if live == nil || !live.running() {
		t.Fatal("the mirror of the repository on the worker did not start again")
	}

	// Once the worker is gone, the repository continues here, and the next move there keeps what the worker had aside.
	killHelper()
	for deadline := time.Now().Add(5 * time.Second); manager.Status("beelink").State != Unreachable; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("status of the lost worker = %+v", manager.Status("beelink"))
		}
	}
	ahead.Store(int64(helper.DefaultLease + GoneMargin))
	stop := func(func(string) bool) error { return nil }
	if err := manager.Return(ctx, project, stop); err != nil {
		t.Fatal(err)
	}
	if err := manager.Move(ctx, project, "beelink", stop); err != nil {
		t.Fatal(err)
	}
	aside := filepath.Join(filepath.Dir(project), "."+filepath.Base(project)+".gripi-aside") + "/"
	if calls := sshCalls(t, "'receive' '--aside' '"+aside); len(calls) != 1 {
		t.Fatalf("copies that save aside what they replace: %q", calls)
	}
}

func refused(err error) bool {
	var refusal interface{ Refused() bool }
	return errors.As(err, &refusal) && refusal.Refused()
}

// connectWorker makes the manager have a connection to the worker of a name, over the fake ssh.
func connectWorker(t *testing.T, manager *Manager, name string) *Conn {
	t.Helper()
	conn := &Conn{Pool: newTestPool(t)}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.workers[name] = &remote{name: name, state: Connected, conn: conn, stopSocket: func() error { return nil }, mirrors: map[string]*liveMirror{}}
	return conn
}

func TestForwardMakesAPortOfTheWorkerAvailableOnMainUntilItsConnectionEnds(t *testing.T) {
	fakeSSH(t)
	ctx := context.Background()
	manager := newTestManager(t, ManagerOptions{Store: fakeStore{}})
	project := "/work/project"
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: mirror.Unit{Main: project}}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Forward(ctx, project, 5173); !refused(err) || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("Forward while the worker is not connected = %v", err)
	}
	if _, err := manager.Forward(ctx, "/work/other", 5173); !refused(err) {
		t.Fatalf("Forward in a folder on main = %v", err)
	}
	conn := connectWorker(t, manager, "beelink")
	// Something on main listens at the worker's port already.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	port := taken.Addr().(*net.TCPAddr).Port

	local, err := manager.Forward(ctx, project+"/src", port)
	if err != nil || local <= port {
		t.Fatalf("Forward of a port that main uses = %d, %v", local, err)
	}
	if calls := sshCalls(t, fmt.Sprintf("-O forward -L localhost:%d:localhost:%d", local, port)); len(calls) != 1 {
		t.Fatalf("ssh forwards: %q", sshCalls(t, "-O forward"))
	}
	if again, err := manager.Forward(ctx, project, port); again != local || err != nil {
		t.Fatalf("Forward of a forwarded port = %d, %v", again, err)
	}
	if location, _ := manager.Location(project); !slices.Equal(location.Forwards, []Forwarded{{Port: port, Local: local}}) {
		t.Fatalf("location = %+v", location)
	}

	if err := manager.Unforward(project, port); err != nil {
		t.Fatal(err)
	}
	if calls := sshCalls(t, fmt.Sprintf("-O cancel -L localhost:%d:localhost:%d", local, port)); len(calls) != 1 {
		t.Fatalf("ssh cancels: %q", sshCalls(t, "-O cancel"))
	}
	if err := manager.Unforward(project, port); !refused(err) {
		t.Fatalf("Unforward of a port that is not forwarded = %v", err)
	}

	if _, err := manager.Forward(ctx, project, port); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	worker := manager.workers["beelink"]
	manager.mu.Unlock()
	manager.disconnect(worker, conn, false)
	if location, _ := manager.Location(project); len(location.Forwards) != 0 {
		t.Fatalf("forwards after the connection ended: %+v", location.Forwards)
	}
}

func TestLocationSaysWhichWorkerRunsAFolderOrIsGettingIt(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, ManagerOptions{Store: fakeStore{}, Clock: func() time.Time { return now }})
	placed := mirror.Unit{Main: "/work/project", Worktrees: []string{"/work/project-feature"}}
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: placed}); err != nil {
		t.Fatal(err)
	}
	if location, found := manager.Location("/work/project-feature/src"); !found || location.Worker != "beelink" || location.Repository != "/work/project" || location.State != Disconnected || !location.Copying.IsZero() {
		t.Fatalf("location of a placed folder = %+v, %v", location, found)
	}
	if _, found := manager.Location("/work/other"); found {
		t.Fatal("a folder on main has a worker")
	}
	// The worker gets another repository, while it runs this one's sessions.
	manager.mu.Lock()
	manager.workers["beelink"] = &remote{name: "beelink", state: Copying, mirrors: map[string]*liveMirror{}}
	manager.mu.Unlock()
	if location, _ := manager.Location("/work/project"); location.State != Connected {
		t.Fatalf("location of a placed folder while the worker gets another = %+v", location)
	}

	// A move connects to the worker before it copies, which fails in this test.
	moving := t.TempDir()
	calls := 0
	connecting, connect := make(chan struct{}), make(chan struct{})
	manager.options.Destination = func(string) (string, error) {
		if calls++; calls == 2 {
			close(connecting)
			<-connect
			return "", errors.New("no worker in this test")
		}
		return "gripi@worker", nil
	}
	moved := make(chan error)
	go func() {
		moved <- manager.Move(context.Background(), moving, "beelink", func(func(string) bool) error { return nil })
	}()
	<-connecting
	if location, found := manager.Location(moving); !found || location.Worker != "beelink" || location.Repository != moving || !location.Copying.Equal(now) || location.State != Connecting {
		t.Fatalf("location of a folder that is being copied = %+v, %v", location, found)
	}
	close(connect)
	if err := <-moved; err == nil || refused(err) {
		t.Fatalf("Move = %v", err)
	}
	if location, found := manager.Location(moving); found {
		t.Fatalf("location of a folder that failed to move = %+v", location)
	}
	if err := manager.Move(context.Background(), placed.Main, "beelink", nil); !refused(err) {
		t.Fatalf("Move of a placed repository = %v", err)
	}
	manager.options.Destination = func(name string) (string, error) { return "", fmt.Errorf("no worker is named %q", name) }
	if err := manager.Move(context.Background(), moving, "nuc", nil); !refused(err) {
		t.Fatalf("Move to an unknown worker = %v", err)
	}
	if err := manager.Return(context.Background(), moving, nil); !refused(err) {
		t.Fatalf("Return of a repository on main = %v", err)
	}
}
