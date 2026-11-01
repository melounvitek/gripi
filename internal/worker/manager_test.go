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
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
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
	socket := filepath.Join(t.TempDir(), "gripi.sock")
	manager := newTestManager(t, ManagerOptions{
		StateDir: t.TempDir(), Pi: rig.pi, WorkerExtension: filepath.Join(rig.pi.ExtensionDir, "gripi-worker.ts"),
		SocketPath: socket, Store: fakeStore{sessions: map[string]string{key: project}}, SessionsRoot: sessions,
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
	if environment, _ := output.ReadString('\n'); environment != fmt.Sprintf("cwd=%s socket=set gripi=%s agent=%s saved=1\n", project, socket, rig.pi.AgentDir) {
		t.Fatalf("Pi on the worker says %q", environment)
	}
	// The fake worker shares this machine's files, though it hides that, so the worker's logins replaced main's.
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

// localWorker returns the options of a manager whose worker beelink is this machine, through fakeSSH, with main's Pi
// from rig, a helper of its own and a scripted Pi there.
func localWorker(t *testing.T, rig *bootstrapRig) ManagerOptions {
	t.Helper()
	writeTestFile(t, filepath.Join(rig.pkg, "dist", "cli.js"), fakeWorkerPi, 0755)
	writeTestFile(t, filepath.Join(rig.pi.ExtensionDir, "gripi-worker.ts"), "worker\n", 0600)
	gripi, _ := buildExecutables(t)
	unitName := make([]byte, 6)
	rand.Read(unitName)
	return ManagerOptions{
		StateDir: t.TempDir(), Pi: rig.pi, WorkerExtension: filepath.Join(rig.pi.ExtensionDir, "gripi-worker.ts"),
		SocketPath: filepath.Join(t.TempDir(), "gripi.sock"), Store: fakeStore{}, SessionsRoot: t.TempDir(),
		ServeSocket: func(string, WorkerPlacement) (string, func() error, error) {
			return filepath.Join(t.TempDir(), "beelink.sock"), func() error { return nil }, nil
		},
		Connection: Options{Executable: gripi, InstallDir: "gripi-worker", Unit: "gripi-test-" + hex.EncodeToString(unitName), RuntimeDir: shortTempDir(t)},
	}
}

func TestLaunchFailsUnlessBothMirrorsOfTheRepositoryRun(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses folder permissions")
	}
	requireSystemd(t)
	options := localWorker(t, newBootstrapRig(t))
	manager := newTestManager(t, options)
	project := t.TempDir()
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: mirror.Unit{Main: project}}); err != nil {
		t.Fatal(err)
	}
	// The worker cannot make the folder of the session's file, so main would never get the file.
	if err := os.Chmod(options.SessionsRoot, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(options.SessionsRoot, 0700) })

	if command, _, err := manager.Launch(agent.Process{Dir: project, Command: []string{"pi", "--mode", "rpc"}}); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Launch without the mirror of the sessions = %v, %v", command, err)
	}
}

func TestLaunchWaitsUntilMainHasTheRepositoryAndTheSessionsAsTheWorkerHasThem(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	requireSystemd(t)
	options := localWorker(t, newBootstrapRig(t))
	manager := newTestManager(t, options)
	project := t.TempDir()
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: mirror.Unit{Main: project}}); err != nil {
		t.Fatal(err)
	}
	// The worker cannot read a folder among the sessions' files, so main never gets them as they are there.
	sessions := sessionFolder(options.SessionsRoot, project)
	unreadable := filepath.Join(sessions, "private")
	if err := os.MkdirAll(unreadable, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0700) })
	defer func(timeout time.Duration) { firstBarrierTimeout = timeout }(firstBarrierTimeout)
	firstBarrierTimeout = time.Second
	process := agent.Process{Dir: project, Command: []string{"pi", "--mode", "rpc"}}

	if command, _, err := manager.Launch(process); command != nil || err == nil || !strings.Contains(err.Error(), sessions) || !strings.Contains(err.Error(), "no barrier") {
		t.Fatalf("Launch while main lacks the sessions = %v, %v", command, err)
	}
	firstBarrierTimeout = 10 * time.Second
	if err := os.Chmod(unreadable, 0700); err != nil {
		t.Fatal(err)
	}
	command, release, err := manager.Launch(process)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	release()
}

// hangingPi stands in for Pi on the worker, with a background process, whose PID it prints. Once its input ends, it
// hangs, as Pi does in a session_shutdown handler that never finishes.
const hangingPi = `#!/usr/bin/env node
if [ "$1" = --version ]; then exec cat "$HOME/pi-version"; fi
setsid sleep 1000 > /dev/null &
echo $!
while read -r line; do :; done
exec sleep 1000
`

func TestTheNextPiOfASessionStartsAfterAnIdlePiThatHangs(t *testing.T) {
	requireSystemd(t)
	rig := newBootstrapRig(t)
	options := localWorker(t, rig)
	writeTestFile(t, filepath.Join(rig.pkg, "dist", "cli.js"), hangingPi, 0755)
	manager := newTestManager(t, options)
	project := t.TempDir()
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: mirror.Unit{Main: project}}); err != nil {
		t.Fatal(err)
	}
	process := agent.Process{Key: filepath.Join(sessionFolder(options.SessionsRoot, project), "s.jsonl"), Dir: project, Command: []string{"pi", "--mode", "rpc"}}
	start := func() (*exec.Cmd, func(), int) {
		t.Helper()
		command, release, err := manager.Launch(process)
		if err != nil {
			t.Fatal(err)
		}
		stdout, _ := command.StdoutPipe()
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		background, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			t.Fatalf("Pi on the worker said %q", line)
		}
		return command, release, background
	}

	command, release, background := start()
	// Gripi retires the idle Pi: its RPC ends, which the adapter stops when Pi doesn't exit.
	command.Process.Kill()
	command.Wait()
	release()
	command, release, _ = start()
	if err := syscall.Kill(background, 0); err != nil {
		t.Fatalf("the background process of the session's first Pi: %v", err)
	}
	command.Process.Kill()
	command.Wait()
	release()
}

// A worker that is this machine as the same user, or that mounts its home folder, would get main's own files instead
// of copies: the worker's logins would replace main's, and the worker's gripi socket main's.
func TestMainRefusesAWorkerThatSharesItsFiles(t *testing.T) {
	requireSystemd(t)
	rig := newBootstrapRig(t)
	t.Setenv("FAKE_SSH_SHARED", "1")
	logins := `{"anthropic":{"type":"oauth","access":"a","refresh":"main's","expires":1}}`
	writeTestFile(t, filepath.Join(rig.pi.AgentDir, "auth.json"), logins, 0600)
	socket := filepath.Join(shortTempDir(t), "gripi.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	project := t.TempDir()
	options := localWorker(t, rig)
	options.SocketPath = socket
	options.ServeSocket = func(string, WorkerPlacement) (string, func() error, error) {
		t.Error("the worker got a gripi socket")
		return "", nil, errors.New("no socket in this test")
	}
	manager := newTestManager(t, options)
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: mirror.Unit{Main: project}}); err != nil {
		t.Fatal(err)
	}

	_, _, err = manager.Launch(agent.Process{Dir: project, Command: []string{"pi", "--mode", "rpc"}})
	if err == nil || !strings.Contains(err.Error(), "shares") || !strings.Contains(err.Error(), rig.pi.AgentDir) {
		t.Fatalf("Launch on a worker that shares main's files = %v", err)
	}
	if contents, _ := os.ReadFile(filepath.Join(rig.pi.AgentDir, "auth.json")); string(contents) != logins {
		t.Fatalf("main's auth.json = %s", contents)
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("main's gripi socket: %v", err)
	}
	if copies := sshCalls(t, "receive"); len(copies) != 0 {
		t.Fatalf("copies to the worker: %q", copies)
	}
	if markers, _ := filepath.Glob(filepath.Join(rig.pi.AgentDir, ".gripi-worker-check-*")); len(markers) != 0 {
		t.Fatalf("left in Pi's folder: %q", markers)
	}
	if status := manager.Status("beelink"); status.State != Disconnected {
		t.Fatalf("status = %+v", status)
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
	rewritten := make(chan string, 4)
	manager := newTestManager(t, ManagerOptions{Store: fakeStore{}, Placements: placements, Rewritten: func(key string) { rewritten <- key }})
	unit := mirror.Unit{Main: "/work/project", Worktrees: []string{"/work/project-old"}, GitDir: "/work/project/.git"}
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: unit}); err != nil {
		t.Fatal(err)
	}
	// The worker's mirror sends the folders of the unit, which Git there may have changed.
	manager.applied(unit, false)(mirror.Checkpoint{Roots: []string{"/work/project", "/work/project-new"}}, "/state/aside/main/3", []string{"/work/project/README"}, []string{"/work/project/new.jsonl"})
	beelink := manager.Placement("beelink")
	if !beelink.Folder("/work/project-new/src") || beelink.Folder("/work/project-old") {
		t.Fatalf("placement after a worktree was added and one removed: %+v", manager.options.Placements.All())
	}
	reloaded, err := LoadPlacements(path)
	if err != nil || !slices.Equal(reloaded.All()[0].Unit.Worktrees, []string{"/work/project-new"}) {
		t.Fatalf("saved placements: %+v, %v", reloaded.All(), err)
	}
	// The mirror of the sessions' folders has folders of its own. The guard of the sessions' files takes those that
	// it wrote anew for the gateway's own, unless they changed on main, such as in Pi CLI.
	manager.applied(unit, true)(mirror.Checkpoint{Roots: []string{"/sessions/--work-project--"}}, "/state/aside/main/4", []string{"/sessions/--work-project--/s.jsonl"},
		[]string{"/sessions/--work-project--/s.jsonl", "/sessions/--work-project--/rewritten.jsonl"})
	if beelink.Folder("/work/project-new") != true {
		t.Fatal("the sessions' folders changed the placement")
	}
	if key := <-rewritten; key != "/sessions/--work-project--/rewritten.jsonl" {
		t.Fatalf("rewritten %s", key)
	}
	select {
	case key := <-rewritten:
		t.Fatalf("also rewritten %s", key)
	case <-time.After(50 * time.Millisecond):
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

func TestPisAndGripisOwnFoldersNeverMoveToAWorker(t *testing.T) {
	home := t.TempDir()
	agentDir, stateDir, configDir := filepath.Join(home, ".pi", "agent"), filepath.Join(home, ".pi", "gripi"), filepath.Join(home, ".config", "gripi")
	sessions := filepath.Join(home, "sessions")
	for _, folder := range []string{agentDir, stateDir, configDir, sessions, filepath.Join(stateDir, "inside")} {
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, ManagerOptions{Store: fakeStore{}, Pi: Pi{AgentDir: agentDir}, StateDir: stateDir, GatewayPaths: []string{configDir}, SessionsRoot: sessions})
	stop := func(func(string) bool) error {
		t.Error("a refused move stopped the sessions")
		return nil
	}

	// Such as a home folder outside Git, with Pi's logins in it.
	for _, folder := range []string{home, link, filepath.Join(home, ".pi"), filepath.Join(stateDir, "inside"), configDir, sessions} {
		if err := manager.Move(context.Background(), folder, "beelink", stop); !refused(err) || !strings.Contains(err.Error(), "on this machine") {
			t.Errorf("Move(%s) = %v", folder, err)
		}
	}
	if placements := manager.options.Placements.All(); len(placements) != 0 {
		t.Fatalf("placements = %+v", placements)
	}
}

func TestMainsCopiesWriteOnlyInTheFoldersOfTheirUnit(t *testing.T) {
	manager := newTestManager(t, ManagerOptions{
		Store: fakeStore{}, Pi: Pi{AgentDir: "/home/me/.pi/agent"}, StateDir: "/home/me/.pi/gripi", GatewayPaths: []string{"/home/me/.config/gripi"},
		SessionsRoot: "/home/me/.pi/agent/sessions",
	})
	// This machine has a worktree of the unit, and folders of its own where the worker may make worktrees.
	feature, unrelated, empty := t.TempDir(), t.TempDir(), t.TempDir()
	for _, folder := range []string{feature, unrelated} {
		writeTestFile(t, filepath.Join(folder, "README"), "main's\n", 0600)
	}
	unit := mirror.Unit{Main: "/work/project", Worktrees: []string{feature}, GitDir: "/work/project/.git"}
	for _, placement := range []Placement{{Worker: "beelink", Unit: unit}, {Worker: "nuc", Unit: mirror.Unit{Main: "/work/other"}}} {
		if err := manager.options.Placements.Add(placement); err != nil {
			t.Fatal(err)
		}
	}

	// /work/.worktrees/project/new is a worktree made on the worker, and so are those over the folders of this machine,
	// whose files the worker's would replace unless the folder is empty.
	allow := manager.allowRoot(unit, false)
	for root, allowed := range map[string]bool{
		"/work/project": true, feature: true, "/work/project/.git": true, "/work/.worktrees/project/new": true, empty: true, unrelated: false,
		"/home/me": false, "/home/me/.pi/agent/extensions": false, "/home/me/.pi/gripi": false, "/home/me/.config": false,
		"/home/me/.pi/agent/sessions/--work-project--": false, "/work/other": false, "/work/other/src": false, "/work": false,
	} {
		if err := allow(root); (err == nil) != allowed {
			t.Errorf("the copy of the repository allows %s: %v", root, err)
		}
	}

	sessions := mirror.Unit{Main: "/home/me/.pi/agent/sessions/--work-project--", Worktrees: []string{"/home/me/.pi/agent/sessions/--work-project-feature--"}}
	allow = manager.allowRoot(sessions, true)
	for root, allowed := range map[string]bool{
		"/home/me/.pi/agent/sessions/--work-project--": true, "/home/me/.pi/agent/sessions/--work-project-feature--": true,
		"/home/me/.pi/agent/sessions/--work-other--": false, "/home/me/.pi/agent/sessions": false, "/home/me/.pi/agent": false, "/work/project": false,
	} {
		if err := allow(root); (err == nil) != allowed {
			t.Errorf("the copy of the sessions allows %s: %v", root, err)
		}
	}
}

// The sessions that started in a folder of a repository are another's once a worktree of another repository on a
// worker is there, and the worker's copies of them are another's while that repository is on the worker or on its way
// there.
func TestTheSessionFoldersOfAWorktreeOfAnotherRepositoryOnAWorkerAreItsOwn(t *testing.T) {
	manager := newTestManager(t, ManagerOptions{Store: fakeStore{}, StateDir: t.TempDir(), SessionsRoot: "/sessions"})
	project := mirror.Unit{Main: "/work/project"}
	if err := manager.options.Placements.Add(Placement{Worker: "nuc", Unit: mirror.Unit{Main: "/work/other", Worktrees: []string{"/work/project/other"}}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.rememberSessions(project.Main, "beelink", []string{"/work/project/src", "/work/project/other/src"}); err != nil {
		t.Fatal(err)
	}
	if folders, _ := manager.sessionFolders(project, "beelink"); !slices.Equal(folders, []string{"/sessions/--work-project--", "/sessions/--work-project-src--"}) {
		t.Fatalf("session folders = %q", folders)
	}

	manager.copying = append(manager.copying, copying{unit: mirror.Unit{Main: "/work/third"}, worker: "beelink"})
	for name, held := range map[string]bool{"nuc": true, "beelink": false} {
		if manager.heldOn(name, project, "/work/project/other/src") != held {
			t.Errorf("the other repository holds its worktree on %s: %v", name, !held)
		}
	}
	if !manager.heldOn("beelink", project, "/work/third/src") || manager.heldOn("beelink", mirror.Unit{Main: "/work/third"}, "/work/third/src") {
		t.Error("a repository on its way to beelink holds its folders there for others only")
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

// Main's copies are consistent once their mirrors have stopped, which a disconnect waits for after it published that
// the worker is lost.
func TestAReturnOrReconnectWaitsUntilALostConnectionsMirrorsHaveStopped(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	project := t.TempDir()
	manager := newTestManager(t, ManagerOptions{Store: fakeStore{}, StateDir: t.TempDir(), SessionsRoot: t.TempDir(), Clock: func() time.Time { return now }})
	dialed := make(chan struct{}, 1)
	manager.options.Destination = func(string) (string, error) {
		dialed <- struct{}{}
		return "", errors.New("no worker in this test")
	}
	unit := mirror.Unit{Main: project}
	if err := manager.options.Placements.Add(Placement{Worker: "beelink", Unit: unit}); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(manager.mirrorFolder(project), "state")
	writeTestFile(t, filepath.Join(state, "checkpoint.json"), `{"barrier":3}`, 0600)
	conn := connectWorker(t, manager, "beelink")
	// The repository's mirror is applying a barrier.
	applying := make(chan struct{})
	manager.mu.Lock()
	worker := manager.workers["beelink"]
	worker.goneAt = now
	worker.mirrors[project] = &liveMirror{unit: unit, cancel: func() {}, done: applying}
	manager.mu.Unlock()
	now = now.Add(helper.DefaultLease + GoneMargin)

	disconnected := make(chan struct{})
	go func() {
		manager.disconnect(worker, conn, false)
		close(disconnected)
	}()
	for manager.Status("beelink").State != Gone {
		time.Sleep(time.Millisecond)
	}
	returned, reconnected := make(chan error, 1), make(chan error, 1)
	go func() {
		returned <- manager.Return(context.Background(), project, func(func(string) bool) error { return nil })
	}()
	go func() { reconnected <- manager.Reconnect(context.Background(), "beelink") }()
	select {
	case err := <-returned:
		t.Fatalf("returned while the mirror ran: %v", err)
	case <-dialed:
		t.Fatal("connected again while the mirror ran")
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("main's copy while its mirror ran: %v", err)
	}

	close(applying)
	<-disconnected
	if err := <-returned; err != nil {
		t.Fatal(err)
	}
	if err := <-reconnected; err == nil || !strings.Contains(err.Error(), "no worker in this test") {
		t.Fatalf("Reconnect = %v", err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("main's copy after the return: %v", err)
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

// killHelper ends the helper that runs as the systemd unit of a name, and everything that it runs, as when the worker
// reboots.
func killHelper(t *testing.T, unit string) {
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
		SocketPath: filepath.Join(t.TempDir(), "gripi.sock"), Store: fakeStore{sessions: map[string]string{key: project}}, SessionsRoot: sessions,
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
	killHelper(t, unit)
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
	killHelper(t, unit)
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
	// The session's folder can't be read, so the worker's copy of its conversation stays as it was.
	folder := sessionFolder(sessions, project)
	if os.Geteuid() != 0 {
		if err := os.Chmod(folder, 0); err != nil {
			t.Fatal(err)
		}
		if err := manager.Move(ctx, project, "beelink", stop); err == nil {
			t.Fatal("moved without the session's folder")
		}
		if err := os.Chmod(folder, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Move(ctx, project, "beelink", stop); err != nil {
		t.Fatal(err)
	}
	for _, copied := range []string{project, folder} {
		aside := filepath.Join(filepath.Dir(copied), "."+filepath.Base(copied)+".gripi-aside") + "/"
		if calls := sshCalls(t, "'receive' '--aside' '"+aside); len(calls) == 0 || os.Geteuid() != 0 && len(calls) != 2 {
			t.Fatalf("copies of %s that save aside what they replace: %q", copied, calls)
		}
	}
}

// Each worker that main lost keeps what main never got until the next move there saves it aside, also after the
// repository was on another worker that main lost.
func TestEveryLostWorkerKeepsAsideWhatMainNeverGotOnTheNextMoveThere(t *testing.T) {
	requireSystemd(t)
	options := localWorker(t, newBootstrapRig(t))
	var ahead atomic.Int64 // how far the manager's clock is ahead
	options.Clock = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
	manager := newTestManager(t, options)
	// Both workers are this machine, one at a time.
	manager.options.Destination = func(string) (string, error) { return "gripi@worker", nil }
	project := t.TempDir()
	ctx := context.Background()
	stop := func(func(string) bool) error { return nil }
	// lose ends the worker of a name, and the repository continues on main once the worker is gone.
	lose := func(name string) {
		t.Helper()
		killHelper(t, options.Connection.Unit)
		for deadline := time.Now().Add(5 * time.Second); manager.Status(name).State == Connected; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("worker %s is still connected", name)
			}
		}
		ahead.Add(int64(helper.DefaultLease + GoneMargin))
		if err := manager.Return(ctx, project, stop); err != nil {
			t.Fatal(err)
		}
	}
	// movingSavesAside moves the repository to the worker of a name, and reports whether the copies there saved aside
	// what they replaced.
	movingSavesAside := func(name string) bool {
		t.Helper()
		before := len(sshCalls(t, "'receive' '--aside'"))
		if err := manager.Move(ctx, project, name, stop); err != nil {
			t.Fatal(err)
		}
		return len(sshCalls(t, "'receive' '--aside'")) > before
	}
	// returnToMain brings the repository back, then ends the worker, so that the other can run on this machine.
	returnToMain := func() {
		t.Helper()
		if err := manager.Return(ctx, project, stop); err != nil {
			t.Fatal(err)
		}
		killHelper(t, options.Connection.Unit)
	}

	if movingSavesAside("beelink") {
		t.Fatal("the first move to beelink saved aside")
	}
	lose("beelink")
	if movingSavesAside("nuc") {
		t.Fatal("the first move to nuc saved aside")
	}
	lose("nuc")
	if !movingSavesAside("beelink") {
		t.Fatal("the move back to beelink replaced what main never got from it")
	}
	returnToMain()
	if !movingSavesAside("nuc") {
		t.Fatal("the move back to nuc replaced what main never got from it")
	}
	returnToMain()
	if movingSavesAside("beelink") {
		t.Fatal("the move to beelink saved aside again, after main had brought its copies up to date")
	}
}

// A session deleted on main while its repository was back is deleted on the worker too by the next move there, also
// when it was the last one in its folder, which nothing on main names any more.
func TestTheNextMoveToAWorkerDeletesTheSessionsThatMainDeleted(t *testing.T) {
	requireSystemd(t)
	options := localWorker(t, newBootstrapRig(t))
	project := t.TempDir()
	folder := sessionFolder(options.SessionsRoot, filepath.Join(project, "subdir"))
	key := filepath.Join(folder, "s.jsonl")
	writeTestFile(t, key, "{}\n", 0600)
	store := fakeStore{sessions: map[string]string{key: filepath.Join(project, "subdir")}}
	options.Store = store
	manager := newTestManager(t, options)
	ctx := context.Background()
	stop := func(func(string) bool) error { return nil }
	if err := manager.Move(ctx, project, "beelink", stop); err != nil {
		t.Fatal(err)
	}
	if err := manager.Return(ctx, project, stop); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}
	delete(store.sessions, key)
	// The gateway restarts, once the worker has gone away.
	killHelper(t, options.Connection.Unit)
	manager.Close()
	manager = newTestManager(t, options)

	copies := len(sshCalls(t, "'receive'", "'"+folder+"'"))
	if copies == 0 {
		t.Fatal("the first move left out the session's folder")
	}
	if err := manager.Move(ctx, project, "beelink", stop); err != nil {
		t.Fatal(err)
	}
	if len(sshCalls(t, "'receive'", "'"+folder+"'")) == copies {
		t.Fatal("the move left the worker's copy of the deleted session")
	}
}

// A move that fails after it copied a session's folder to the worker leaves the worker's copy to the next move there,
// also when main deleted the session meanwhile.
func TestTheMoveAfterAFailedOneDeletesTheSessionsThatMainDeleted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses folder permissions")
	}
	requireSystemd(t)
	options := localWorker(t, newBootstrapRig(t))
	project := t.TempDir()
	folder := sessionFolder(options.SessionsRoot, filepath.Join(project, "subdir"))
	key := filepath.Join(folder, "s.jsonl")
	writeTestFile(t, key, "{}\n", 0600)
	store := fakeStore{sessions: map[string]string{key: filepath.Join(project, "subdir")}}
	options.Store = store
	manager := newTestManager(t, options)
	ctx := context.Background()
	stop := func(func(string) bool) error { return nil }
	// Main cannot clear its side of the repository's mirror, which the move starts once it copied the sessions' folders.
	stuck := filepath.Join(manager.mirrorFolder(project), "state", "stuck")
	writeTestFile(t, filepath.Join(stuck, "file"), "", 0600)
	if err := os.Chmod(stuck, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(stuck, 0700) })

	if err := manager.Move(ctx, project, "beelink", stop); err == nil {
		t.Fatal("moved without main's side of the repository's mirror")
	}
	copies := len(sshCalls(t, "'receive'", "'"+folder+"'"))
	if copies == 0 {
		t.Fatal("the failed move left out the session's folder")
	}
	if err := os.Chmod(stuck, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}
	delete(store.sessions, key)
	if err := manager.Move(ctx, project, "beelink", stop); err != nil {
		t.Fatal(err)
	}
	if len(sshCalls(t, "'receive'", "'"+folder+"'")) == copies {
		t.Fatal("the move left the worker's copy of the deleted session")
	}
}

// git runs Git in dir, with no configuration of the user's.
func git(t *testing.T, dir string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

// The sessions of a worktree that a repository no longer has are another repository's once that has the worktree's
// folder: the next move of the first repository to a worker neither copies them there nor mirrors them from there,
// and keeps aside what the worker has of them, unless they are on the worker as the other repository's.
func TestAMoveLeavesTheSessionsOfAFolderThatAnotherRepositoryHasNow(t *testing.T) {
	requireSystemd(t)
	for _, other := range []string{"nuc", "beelink"} {
		t.Run("the other repository on "+other, func(t *testing.T) {
			options := localWorker(t, newBootstrapRig(t))
			base := t.TempDir()
			project, worktree := filepath.Join(base, "project"), filepath.Join(base, "feature")
			git(t, base, "init", "-q", project)
			git(t, project, "commit", "-q", "--allow-empty", "-m", "first")
			git(t, project, "worktree", "add", "-q", worktree)
			folder := sessionFolder(options.SessionsRoot, worktree)
			ours, theirs := filepath.Join(folder, "ours.jsonl"), filepath.Join(folder, "theirs.jsonl")
			writeTestFile(t, ours, "{}\n", 0600)
			store := fakeStore{sessions: map[string]string{ours: worktree}}
			options.Store = store
			manager := newTestManager(t, options)
			ctx := context.Background()
			stop := func(func(string) bool) error { return nil }
			if err := manager.Move(ctx, project, "beelink", stop); err != nil {
				t.Fatal(err)
			}
			if err := manager.Return(ctx, project, stop); err != nil {
				t.Fatal(err)
			}
			git(t, project, "worktree", "remove", worktree)
			if err := os.Mkdir(worktree, 0700); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, theirs, "{}\n", 0600)
			store.sessions[theirs] = worktree
			if err := manager.options.Placements.Add(Placement{Worker: other, Unit: mirror.Unit{Main: worktree}}); err != nil {
				t.Fatal(err)
			}
			copies := len(sshCalls(t, "'receive'", "'"+folder+"'"))

			if err := manager.Move(ctx, project, "beelink", stop); err != nil {
				t.Fatal(err)
			}
			if len(sshCalls(t, "'receive'", "'"+folder+"'")) != copies {
				t.Fatal("the move copied the other repository's sessions to the worker")
			}
			manager.mu.Lock()
			live := manager.workers["beelink"].mirrors[manager.sessionsMain(mirror.Unit{Main: project})]
			manager.mu.Unlock()
			if live == nil || live.unit.Contains(folder) {
				t.Fatalf("the mirror of the repository's sessions: %+v", live)
			}
			// The worker is this machine, so its copy of the folder is main's.
			asides, _ := filepath.Glob(filepath.Join(filepath.Dir(folder), "."+filepath.Base(folder)+".gripi-aside", "*", folder, "theirs.jsonl"))
			_, err := os.Stat(theirs)
			if other == "beelink" {
				if len(asides) != 0 || err != nil {
					t.Fatalf("the worker's copy of the other repository's sessions there: %v, kept aside in %q", err, asides)
				}
				return
			}
			if len(asides) != 1 || err == nil {
				t.Fatalf("the worker's copy of the folder: %v, kept aside in %q", err, asides)
			}

			// The worker gets the other repository's sessions there later, which are no longer the first repository's to
			// keep aside.
			if err := manager.Return(ctx, project, stop); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, theirs, "{}\n", 0600)
			if err := manager.Move(ctx, project, "beelink", stop); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(theirs); err != nil {
				t.Fatalf("the worker's copy of the other repository's sessions: %v", err)
			}
		})
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
