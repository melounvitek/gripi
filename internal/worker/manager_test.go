package worker

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	var socketsStarted, socketsStopped atomic.Int32
	manager := newTestManager(t, ManagerOptions{
		StateDir: t.TempDir(), Pi: rig.pi, WorkerExtension: filepath.Join(rig.pi.ExtensionDir, "gripi-worker.ts"),
		SocketPath: "/run/gripi.sock", Store: fakeStore{sessions: map[string]string{key: project}}, SessionsRoot: sessions,
		ServeSocket: func(string, WorkerPlacement) (string, func() error, error) {
			socketsStarted.Add(1)
			return filepath.Join(t.TempDir(), "beelink.sock"), func() error { socketsStopped.Add(1); return nil }, nil
		},
		Connection: Options{Executable: gripi, InstallDir: "gripi-worker", Unit: unit, RuntimeDir: shortTempDir(t)},
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
}
