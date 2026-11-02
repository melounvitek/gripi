package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// fakeStore knows the folders of some saved sessions, and the paths that their tool calls named.
type fakeStore struct {
	agent.Store
	sessions map[string]string   // key → folder
	paths    map[string][]string // key → paths
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

func (store fakeStore) ToolPaths(key, _, _ string) ([]string, error) { return store.paths[key], nil }

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

func refused(err error) bool {
	var refusal interface{ Refused() bool }
	return errors.As(err, &refusal) && refusal.Refused()
}

// git runs Git in dir.
func git(t *testing.T, dir string, arguments ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", dir}, arguments...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

// runFakeMirrorSource runs in a process of its own, which a fakeLink starts as a mirror's channel: a mirror source of
// an empty folder, or of a session's file on main, which tests leave alone, that takes a last barrier on SIGUSR1,
// writes its number to a file and ends. It makes the file empty once it takes the signal.
func runFakeMirrorSource(folder string) {
	release := make(chan os.Signal, 1)
	signal.Notify(release, syscall.SIGUSR1)
	barrierFile := os.Getenv("GRIPI_TEST_MIRROR_BARRIER")
	os.WriteFile(barrierFile, nil, 0600)
	unit := mirror.Unit{Main: folder}
	if file := os.Getenv("GRIPI_TEST_MIRROR_FILE"); file != "" {
		unit = mirror.Unit{Main: filepath.Dir(file), File: file}
	}
	source := mirror.NewSource(unit, mirror.Options{AutoBarrier: unit.File != "", CompleteLines: unit.File != ""})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-release
		barrier, _, err := source.Barrier(ctx)
		if err == nil {
			os.WriteFile(barrierFile, []byte(strconv.FormatUint(barrier, 10)), 0600)
		}
		cancel()
	}()
	source.Run(ctx, struct {
		io.Reader
		io.Writer
	}{os.Stdin, os.Stdout})
}

// fakeLink is a connection to a worker that keeps a log of what main asked of it. Its mirrors are sources of an empty
// folder and of the session's file on main, so main's copies of a move change only where a test changes them, as if
// the worker had.
type fakeLink struct {
	rig     *moveRig
	lost    chan struct{}
	goneAt  time.Time
	mu      sync.Mutex
	mirrors map[string]fakeMirror // by move and file
}

// fakeMirror is the channel of one of a move's mirrors, and the file where it writes its last barrier.
type fakeMirror struct {
	command *exec.Cmd
	barrier string
}

func (link *fakeLink) call(format string, arguments ...any) error {
	call := fmt.Sprintf(format, arguments...)
	link.rig.log(call)
	link.rig.mu.Lock()
	defer link.rig.mu.Unlock()
	for prefix, err := range link.rig.failing {
		if strings.HasPrefix(call, prefix) {
			return err
		}
	}
	return nil
}

func (link *fakeLink) User() string                                    { return "gripi" }
func (link *fakeLink) Docker() string                                  { return link.rig.docker }
func (link *fakeLink) ApplyAuth(context.Context, string, string) error { return nil }
func (link *fakeLink) Lost() <-chan struct{}                           { return link.lost }
func (link *fakeLink) GoneAt() time.Time                               { return link.goneAt }
func (link *fakeLink) hangUp()                                         {}
func (link *fakeLink) Close() error                                    { return nil }

func (link *fakeLink) Forward(_ context.Context, port Forward) (func() error, error) {
	return func() error { return link.call("unforward %d", port.Connect) }, link.call("forward %d to %d", port.Connect, port.Listen)
}

func (link *fakeLink) CreateSession(_ context.Context, session Session) error {
	link.rig.mu.Lock()
	link.rig.created = append(link.rig.created, session)
	link.rig.mu.Unlock()
	return link.call("create %s", session.ID)
}

func (link *fakeLink) Attach(context.Context, string) (*exec.Cmd, func(), error) {
	return exec.Command("cat"), func() {}, nil
}

func (link *fakeLink) CloseSession(_ context.Context, id string) error {
	return link.call("close %s", id)
}

func (link *fakeLink) PrepareMove(_ context.Context, move Move) (string, error) {
	link.rig.mu.Lock()
	link.rig.prepared = append(link.rig.prepared, move)
	link.rig.mu.Unlock()
	return "/worker/copies/" + move.ID, link.call("prepare %s", move.ID)
}

func (link *fakeLink) UploadMove(_ context.Context, id, folder string) error {
	return link.call("upload %s %s", id, folder)
}

func (link *fakeLink) UploadSessionFile(_ context.Context, file string) error {
	return link.call("upload %s", file)
}

func (link *fakeLink) SeedMove(ctx context.Context, id string) error {
	if err := link.call("seed %s", id); err != nil || link.rig.seeding == nil {
		return err
	}
	close(link.rig.seeding)
	<-ctx.Done()
	return ctx.Err()
}

func (link *fakeLink) StartMoveMirror(_ context.Context, id, file string) error {
	return link.call("mirror %s %s", id, file)
}

func (link *fakeLink) AttachMoveMirror(_ context.Context, id, file string) (*exec.Cmd, func(), error) {
	folder := filepath.Join(link.rig.sources, id+map[bool]string{false: "-copy", true: "-file"}[file != ""])
	for _, folder := range []string{folder, filepath.Dir(file)} {
		if err := os.MkdirAll(folder, 0700); err != nil {
			return nil, nil, err
		}
	}
	fake := fakeMirror{command: exec.Command(os.Args[0], "-test.run=^$"), barrier: folder + ".barrier"}
	fake.command.Env = append(os.Environ(), "GRIPI_TEST_MIRROR_SOURCE="+folder, "GRIPI_TEST_MIRROR_BARRIER="+fake.barrier)
	if file != "" {
		fake.command.Env = append(fake.command.Env, "GRIPI_TEST_MIRROR_FILE="+file)
	}
	os.Remove(fake.barrier)
	link.mu.Lock()
	link.mirrors[id+" "+file] = fake
	link.mu.Unlock()
	return fake.command, func() {}, nil
}

// ReleaseMove has the move's mirrors take their last barriers, as the helper does once it has killed the move's
// processes.
func (link *fakeLink) ReleaseMove(_ context.Context, id string) (helper.Barriers, error) {
	if err := link.call("release %s", id); err != nil {
		return helper.Barriers{}, err
	}
	var barriers []uint64
	for _, file := range []string{"", link.rig.key} {
		link.mu.Lock()
		fake, found := link.mirrors[id+" "+file]
		link.mu.Unlock()
		if !found || fake.command.Process == nil {
			barriers = append(barriers, 0)
			continue
		}
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(fake.barrier); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return helper.Barriers{}, errors.New("a mirror never took the signal for its last barrier")
			}
		}
		fake.command.Process.Signal(syscall.SIGUSR1)
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if contents, err := os.ReadFile(fake.barrier); err == nil && len(contents) > 0 {
				barrier, _ := strconv.ParseUint(string(contents), 10, 64)
				barriers = append(barriers, barrier)
				break
			}
			if time.Now().After(deadline) {
				return helper.Barriers{}, errors.New("a mirror took no last barrier")
			}
		}
	}
	return helper.Barriers{Repository: barriers[0], Session: barriers[1]}, nil
}

func (link *fakeLink) DeleteMove(_ context.Context, id string) error {
	return link.call("delete %s", id)
}

func (link *fakeLink) Sudo(_ context.Context, session, cwd string, argv []string, password []byte, stdout, _ io.Writer) (int, error) {
	io.WriteString(stdout, "ran\n")
	return 3, link.call("sudo %s in %s: %s with %s", session, cwd, strings.Join(argv, " "), password)
}

// moveRig is main with a repository in its home folder, which has a linked worktree that a session used and another
// one, and a manager whose worker beelink is a fakeLink. The worker's changes to a move's copy are simulated by
// changing main's shadow of it, as the copy's mirror would.
type moveRig struct {
	t                         *testing.T
	manager                   *Manager
	home, repository, gitDir  string
	feature, other, elsewhere string
	key, sessions, sources    string
	now                       time.Time

	mu       sync.Mutex
	events   []string
	failing  map[string]error // what fakeLink's calls that start with a key fail with
	seeding  chan struct{}    // if set, closed when a move seeds its copy, which then waits for the move's end
	docker   string           // what the worker says of Docker when main connects
	links    []*fakeLink
	prepared []Move
	created  []Session
}

func newMoveRig(t *testing.T) *moveRig {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home", "me")
	rig := &moveRig{
		t: t, home: home, repository: filepath.Join(home, "Work", "app"), feature: filepath.Join(home, "Work", ".worktrees", "app", "feature"),
		other: filepath.Join(home, "Work", ".worktrees", "app", "other"), elsewhere: filepath.Join(home, "Work", "elsewhere"),
		sessions: filepath.Join(home, ".pi", "agent", "sessions"), sources: t.TempDir(), failing: map[string]error{},
		now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
	}
	rig.gitDir = filepath.Join(rig.repository, ".git")
	rig.key = filepath.Join(rig.sessions, "--app--", "session.jsonl")
	for path, contents := range map[string]string{
		filepath.Join(rig.repository, "README"): "one\ntwo\nthree\nfour\nfive\nsix\n", filepath.Join(rig.repository, "notes.txt"): "notes\n",
		filepath.Join(rig.elsewhere, "file"): "elsewhere\n",
	} {
		writeTestFile(t, path, contents, 0600)
	}
	git(t, rig.repository, "init", "-q", "-b", "main")
	git(t, rig.repository, "add", "-A")
	git(t, rig.repository, "commit", "-qm", "First")
	git(t, rig.repository, "tag", "-a", "v1", "-m", "The first version")
	git(t, rig.repository, "worktree", "add", "-q", "-b", "feature", rig.feature)
	git(t, rig.repository, "worktree", "add", "-q", "-b", "other", rig.other)
	placements, err := LoadPlacements(filepath.Join(home, ".pi", "gripi", "worker-placements.json"))
	if err != nil {
		t.Fatal(err)
	}
	rig.manager = NewManager(ManagerOptions{
		Destination: func(name string) (string, error) {
			if name != "beelink" {
				return "", fmt.Errorf("no worker is named %q", name)
			}
			return "gripi@beelink", nil
		},
		Placements: placements, StateDir: filepath.Join(home, ".pi", "gripi"), SessionsRoot: rig.sessions,
		Pi:              Pi{Command: []string{"pi"}, AgentDir: filepath.Join(home, ".pi", "agent")},
		WorkerExtension: "/extensions/gripi-worker.ts", SocketPath: filepath.Join(home, ".pi", "gripi", "gripi.sock"),
		Store:      fakeStore{paths: map[string][]string{rig.key: {filepath.Join(rig.feature, "README"), filepath.Join(rig.elsewhere, "file"), "/etc/hosts"}}},
		Connection: Options{Home: home},
		Clock: func() time.Time {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			return rig.now
		},
		open: func(context.Context, string) (link, *Installation, func() error, error) {
			link := &fakeLink{rig: rig, lost: make(chan struct{}), mirrors: map[string]fakeMirror{}}
			if err := link.call("connect"); err != nil {
				return nil, nil, nil, err
			}
			rig.mu.Lock()
			rig.links = append(rig.links, link)
			rig.mu.Unlock()
			return link, &Installation{dir: "/worker", path: "/usr/bin", agentDir: filepath.Join(home, ".pi", "agent")}, func() error { return nil }, nil
		},
	})
	t.Cleanup(rig.manager.Close)
	return rig
}

func (rig *moveRig) log(event string) {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	rig.events = append(rig.events, event)
}

// took returns the events since the last call.
func (rig *moveRig) took() []string {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	events := rig.events
	rig.events = nil
	return events
}

func (rig *moveRig) fail(call string, err error) {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if err == nil {
		delete(rig.failing, call)
	} else {
		rig.failing[call] = err
	}
}

// restart replaces the manager with one of a gateway that starts again, over the same state.
func (rig *moveRig) restart() {
	rig.t.Helper()
	options := rig.manager.options
	placements, err := LoadPlacements(filepath.Join(rig.home, ".pi", "gripi", "worker-placements.json"))
	if err != nil {
		rig.t.Fatal(err)
	}
	options.Placements = placements
	rig.manager = NewManager(options)
	rig.t.Cleanup(rig.manager.Close)
}

// prepare stands in for the gateway's: it checks the repository's folders, and logs when it starts and releases.
func (rig *moveRig) prepare(_ context.Context, inRepository func(string) bool) (func(), error) {
	for folder, in := range map[string]bool{rig.repository: true, filepath.Join(rig.other, "src"): true, rig.gitDir: true, rig.elsewhere: false, rig.home: false} {
		if inRepository(folder) != in {
			rig.t.Errorf("inRepository(%s) = %v", folder, !in)
		}
	}
	rig.log("prepare")
	return func() { rig.log("release others") }, nil
}

func (rig *moveRig) move() Placement {
	rig.t.Helper()
	if err := rig.manager.Move(context.Background(), rig.key, rig.repository, "beelink", rig.prepare); err != nil {
		rig.t.Fatal(err)
	}
	placement, found := rig.manager.options.Placements.Find(rig.key)
	if !found {
		rig.t.Fatal("the moved session has no placement")
	}
	return placement
}

// shadow returns where main's shadow of a move has a path of main's.
func (rig *moveRig) shadow(placement Placement, path string) string {
	return filepath.Join(rig.manager.moveFolder(placement.Move), "shadow") + path
}

func (rig *moveRig) link() *fakeLink {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	return rig.links[len(rig.links)-1]
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestASessionMovesAloneOnACopyOfTheWorktreesThatItUses(t *testing.T) {
	rig := newMoveRig(t)
	other := filepath.Join(rig.sessions, "--app--", "other.jsonl")

	placement := rig.move()
	id := placement.Move
	if events, want := rig.took(), []string{
		"prepare", "release others", "connect", "prepare " + id, "upload " + id + " " + filepath.Join(rig.manager.moveFolder(id), "shadow") + rig.home,
		"upload " + rig.key, "seed " + id, "prepare " + id, "mirror " + id + " ", "mirror " + id + " " + rig.key,
	}; !slices.Equal(events, want) {
		t.Fatalf("the move did\n%q\nwant\n%q", events, want)
	}
	want := Move{ID: id, Repository: rig.gitDir, Roots: []string{rig.repository, rig.feature}, Private: []string{filepath.Join(rig.home, "Work")}}
	if prepared := rig.prepared[0]; prepared.ID != want.ID || prepared.Repository != want.Repository || !slices.Equal(prepared.Roots, want.Roots) || !slices.Equal(prepared.Private, want.Private) {
		t.Fatalf("the worker prepared %+v, want %+v", prepared, want)
	}
	// The shadow, which the worker's copy starts from, has the session's worktrees but not the other one.
	if readme := readFile(t, rig.shadow(placement, filepath.Join(rig.feature, "README"))); readme != "one\ntwo\nthree\nfour\nfive\nsix\n" {
		t.Fatalf("the shadow's README = %q", readme)
	}
	if _, err := os.Stat(rig.shadow(placement, rig.other)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the shadow of the worktree that the session didn't use: %v", err)
	}
	if location, found := rig.manager.Location(rig.key); !found || location.Worker != "beelink" || location.State != Connected || location.Repository != rig.repository || !location.Copying.IsZero() {
		t.Fatalf("location = %+v, %v", location, found)
	}
	if _, found := rig.manager.Location(other); found {
		t.Fatal("another session of the repository is on the worker")
	}
	beelink := rig.manager.Placement("beelink")
	if !beelink.Session(rig.key) || beelink.Session(other) || rig.manager.Placement("nuc").Session(rig.key) {
		t.Fatal("the worker's gripi commands reach the wrong sessions")
	}

	// The session's Pi runs in the move's view on the worker; the repository's other sessions run on main.
	command, release, err := rig.manager.Launch(agent.Process{Key: rig.key, Command: []string{"pi", "--mode", "rpc"}, Env: []string{"SAVED=1"}})
	if err != nil || command == nil {
		t.Fatalf("Launch = %v, %v", command, err)
	}
	release()
	if session := rig.created[0]; session.ID != sessionID(rig.key) || session.Move != id || session.Cwd != rig.repository ||
		!slices.Equal(session.Args, []string{"/worker/bin/pi", "--mode", "rpc", "--extension", "/extensions/gripi-worker.ts"}) || !slices.Contains(session.Env, "SAVED=1") {
		t.Fatalf("the worker started %+v", session)
	}
	for _, process := range []agent.Process{{Key: other}, {Dir: rig.repository}} {
		if command, _, err := rig.manager.Launch(process); command != nil || err != nil {
			t.Fatalf("Launch(%+v) = %v, %v; want Pi on main", process, command, err)
		}
	}
	if err := rig.manager.Move(context.Background(), rig.key, rig.repository, "beelink", rig.prepare); !refused(err) {
		t.Fatalf("a second move of the session = %v", err)
	}

	// Another session of the repository moves on a copy of its own.
	key := filepath.Join(rig.sessions, "--app-feature--", "session.jsonl")
	if err := rig.manager.Move(context.Background(), key, rig.feature, "beelink", rig.prepare); err != nil {
		t.Fatal(err)
	}
	second, _ := rig.manager.options.Placements.Find(key)
	if second.Move == id || !slices.Equal(second.Roots, []string{rig.feature}) {
		t.Fatalf("the second move = %+v", second)
	}
}

func TestAMoveRefusesWhatCannotMove(t *testing.T) {
	rig := newMoveRig(t)
	ctx := context.Background()
	outside := filepath.Join(filepath.Dir(rig.home), "outside")
	writeTestFile(t, filepath.Join(outside, "file"), "outside\n", 0600)
	git(t, outside, "init", "-q")
	for _, refusedMove := range []struct{ cwd, worker, says string }{
		{rig.repository, "nuc", "no worker"},
		{rig.elsewhere, "beelink", "not in a Git repository"},
		{outside, "beelink", "outside the home folder"},
	} {
		if err := rig.manager.Move(ctx, rig.key, refusedMove.cwd, refusedMove.worker, rig.prepare); !refused(err) || !strings.Contains(err.Error(), refusedMove.says) {
			t.Errorf("Move from %s to %s = %v", refusedMove.cwd, refusedMove.worker, err)
		}
	}
	// Pi's agent folder, which the worker's sessions need as they are, is in the folder that the copy would hide.
	rig.manager.options.Pi.AgentDir = filepath.Join(rig.home, "Work", "pi-agent")
	if err := rig.manager.Move(ctx, rig.key, rig.repository, "beelink", rig.prepare); !refused(err) || !strings.Contains(err.Error(), "pi-agent") {
		t.Errorf("Move that would hide Pi's agent folder = %v", err)
	}
	rig.manager.options.Pi.AgentDir = filepath.Join(rig.home, ".pi", "agent")
	inWork := filepath.Join(rig.home, "Work", "sessions", "session.jsonl")
	if err := rig.manager.Move(ctx, inWork, rig.repository, "beelink", rig.prepare); !refused(err) {
		t.Errorf("Move of a session whose file is in the copy = %v", err)
	}
	// What the base can't record, such as Git LFS, is refused once the other sessions are idle.
	git(t, rig.repository, "config", "lfs.repositoryformatversion", "0")
	if err := rig.manager.Move(ctx, rig.key, rig.repository, "beelink", rig.prepare); err == nil || !strings.Contains(err.Error(), "LFS") {
		t.Errorf("Move of a repository with Git LFS = %v", err)
	}
	if events := rig.took(); !slices.Equal(events, []string{"prepare", "release others"}) {
		t.Errorf("the refused moves did %q", events)
	}
	if entries, _ := os.ReadDir(filepath.Join(rig.manager.options.StateDir, "moves")); len(entries) != 0 {
		t.Errorf("the refused moves left %v", entries)
	}
	if refs := git(t, rig.repository, "for-each-ref", "refs/gripi"); refs != "" {
		t.Errorf("the refused moves left the refs %s", refs)
	}
	if _, found := rig.manager.Location(rig.key); found || len(rig.manager.options.Placements.All()) != 0 {
		t.Error("a refused move placed the session")
	}
}

func TestAFailedMoveLeavesTheSessionOnMainAndDeletesTheWorkersCopy(t *testing.T) {
	rig := newMoveRig(t)
	rig.fail("seed", errors.New("cp: No space left on device"))

	err := rig.manager.Move(context.Background(), rig.key, rig.repository, "beelink", rig.prepare)
	if err == nil || !strings.Contains(err.Error(), "No space left") {
		t.Fatalf("Move = %v", err)
	}
	id := rig.prepared[0].ID
	for deadline := time.Now().Add(5 * time.Second); !slices.Contains(rig.took(), "delete "+id); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the worker's copy of the failed move was not deleted")
		}
	}
	if _, found := rig.manager.Location(rig.key); found {
		t.Fatal("the session of the failed move is on the worker")
	}
	if _, err := os.Stat(rig.manager.moveFolder(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("main's side of the failed move: %v", err)
	}
	if refs := git(t, rig.repository, "for-each-ref", "refs/gripi"); refs != "" {
		t.Fatalf("the failed move left the refs %s", refs)
	}
	if stale := rig.manager.options.Placements.Stale("beelink"); len(stale) != 0 {
		t.Fatalf("copies left to delete: %q", stale)
	}
}

func TestAReturnBringsTheSessionsChangesAndKeepsAsideWhatMainChangedToo(t *testing.T) {
	rig := newMoveRig(t)
	placement := rig.move()
	rig.took()
	// Meanwhile on the worker: a commit on the session's branch, a new file, and changes to README in both worktrees.
	git(t, rig.feature, "--git-dir", rig.shadow(placement, filepath.Join(rig.gitDir, "worktrees", "feature")), "--work-tree", rig.shadow(placement, rig.feature), "commit", "-q", "--allow-empty", "-m", "On the worker")
	writeTestFile(t, rig.shadow(placement, filepath.Join(rig.feature, "new.txt")), "made on the worker\n", 0600)
	writeTestFile(t, rig.shadow(placement, filepath.Join(rig.repository, "README")), "one\ntwo\nthree\nfour\nfive\nsix on the worker\n", 0600)
	writeTestFile(t, rig.shadow(placement, filepath.Join(rig.repository, "notes.txt")), "the worker's notes\n", 0600)
	// And on main: README in another place, and notes.txt too.
	writeTestFile(t, filepath.Join(rig.repository, "README"), "one on main\ntwo\nthree\nfour\nfive\nsix\n", 0600)
	writeTestFile(t, filepath.Join(rig.repository, "notes.txt"), "main's notes\n", 0600)

	if err := rig.manager.Return(context.Background(), rig.key, rig.prepare); err != nil {
		t.Fatal(err)
	}
	if events := rig.took(); len(events) < 2 || !slices.Equal(events[:2], []string{"prepare", "release " + placement.Move}) || !slices.Contains(events, "release others") {
		t.Fatalf("the return did %q", events)
	}
	for path, want := range map[string]string{
		filepath.Join(rig.feature, "new.txt"): "made on the worker\n", filepath.Join(rig.repository, "README"): "one on main\ntwo\nthree\nfour\nfive\nsix on the worker\n",
		filepath.Join(rig.repository, "notes.txt"): "main's notes\n",
	} {
		if got := readFile(t, path); got != want {
			t.Errorf("main's %s = %q, want %q", path, got, want)
		}
	}
	if subject := git(t, rig.feature, "log", "-1", "--format=%s"); subject != "On the worker" {
		t.Errorf("main's feature branch is at %q", subject)
	}
	notices := rig.manager.Notices(rig.key)
	if len(notices) != 1 || len(notices[0].Result.Aside) != 1 || notices[0].Result.Aside[0].Path != filepath.Join(rig.repository, "notes.txt") || notices[0].Worker != "beelink" || notices[0].Repository != rig.repository {
		t.Fatalf("notices = %+v", notices)
	}
	if _, found := rig.manager.Location(rig.key); found {
		t.Fatal("the returned session is still on the worker")
	}
	if command, _, err := rig.manager.Launch(agent.Process{Key: rig.key}); command != nil || err != nil {
		t.Fatalf("the returned session's Pi = %v, %v; want it on main", command, err)
	}
	if _, err := os.Stat(rig.manager.moveFolder(placement.Move)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("main's side of the move: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); len(rig.manager.options.Placements.Stale("beelink")) != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the worker's copy was not deleted")
		}
	}

	// Dismissing the notice drops what the return kept aside.
	recovery := notices[0].Recovery
	if _, err := os.Stat(recovery); err != nil {
		t.Fatal(err)
	}
	if err := rig.manager.DismissNotices(context.Background(), rig.key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(recovery); !errors.Is(err, os.ErrNotExist) || len(rig.manager.Notices(rig.key)) != 0 {
		t.Fatalf("after the dismissal, the recovery folder: %v, notices %+v", err, rig.manager.Notices(rig.key))
	}

	// A return that keeps nothing aside keeps no record.
	placement = rig.move()
	writeTestFile(t, rig.shadow(placement, filepath.Join(rig.feature, "second.txt")), "second\n", 0600)
	if err := rig.manager.Return(context.Background(), rig.key, rig.prepare); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(rig.feature, "second.txt")); got != "second\n" || len(rig.manager.Notices(rig.key)) != 0 {
		t.Fatalf("second.txt = %q, notices %+v", got, rig.manager.Notices(rig.key))
	}
	if _, err := os.Stat(rig.manager.recoveryFolder(placement.Move)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the record of a return that kept nothing aside: %v", err)
	}
	if refs := git(t, rig.repository, "for-each-ref", "refs/gripi"); refs != "" {
		t.Fatalf("refs after the returns: %s", refs)
	}
}

func TestAFailedReleaseFinishesWhenTheReturnRunsAgain(t *testing.T) {
	rig := newMoveRig(t)
	placement := rig.move()
	writeTestFile(t, rig.shadow(placement, filepath.Join(rig.feature, "new.txt")), "new\n", 0600)
	rig.fail("release", errors.New("worker: main's copy isn't connected"))

	if err := rig.manager.Return(context.Background(), rig.key, rig.prepare); err == nil || !strings.Contains(err.Error(), "isn't connected") {
		t.Fatalf("Return = %v", err)
	}
	// The worker released the move, which takes no new sessions, and nothing brings the session back now.
	if location, _ := rig.manager.Location(rig.key); location.State != Returning || !location.Returning.IsZero() {
		t.Fatalf("location = %+v", location)
	}
	if _, _, err := rig.manager.Launch(agent.Process{Key: rig.key}); !refused(err) || !strings.Contains(err.Error(), "coming back") {
		t.Fatalf("Launch of a session that is coming back = %v", err)
	}
	rig.fail("release", nil)
	if err := rig.manager.Return(context.Background(), rig.key, func(ctx context.Context, inRepository func(string) bool) (func(), error) {
		if location, _ := rig.manager.Location(rig.key); location.State != Returning || !location.Returning.Equal(rig.now) {
			t.Errorf("location of a session coming back = %+v", location)
		}
		return rig.prepare(ctx, inRepository)
	}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(rig.feature, "new.txt")); got != "new\n" {
		t.Fatalf("new.txt = %q", got)
	}
}

func TestASessionOnALostWorkerContinuesOnMainOnceTheWorkerIsGone(t *testing.T) {
	rig := newMoveRig(t)
	placement := rig.move()
	lost := make(chan string, 1)
	command, release, err := rig.manager.Launch(agent.Process{Key: rig.key, Command: []string{"pi"}, Lost: func(message string) { lost <- message }})
	if err != nil || command == nil {
		t.Fatal(err)
	}
	defer release()
	writeTestFile(t, rig.shadow(placement, filepath.Join(rig.feature, "durable.txt")), "the last barrier\n", 0600)
	goneAt := rig.now.Add(helper.DefaultLease + GoneMargin)
	link := rig.link()
	link.goneAt = goneAt
	close(link.lost)
	if message := <-lost; !strings.Contains(message, "outcome of the running step is unknown") {
		t.Fatalf("the session was told %q", message)
	}
	for rig.manager.Status("beelink").State != Unreachable {
		time.Sleep(time.Millisecond)
	}
	rig.took()

	err = rig.manager.Return(context.Background(), rig.key, rig.prepare)
	if !refused(err) || !strings.Contains(err.Error(), "unreachable") || !strings.Contains(err.Error(), goneAt.Local().Format(time.TimeOnly)) {
		t.Fatalf("Return before the worker is gone = %v", err)
	}
	rig.mu.Lock()
	rig.now = goneAt
	rig.mu.Unlock()
	if err := rig.manager.Return(context.Background(), rig.key, rig.prepare); err != nil {
		t.Fatal(err)
	}
	if events := rig.took(); !slices.Equal(events, []string{"prepare", "release others"}) {
		t.Fatalf("the return without the worker did %q", events)
	}
	if got := readFile(t, filepath.Join(rig.feature, "durable.txt")); got != "the last barrier\n" {
		t.Fatalf("durable.txt = %q", got)
	}
	// The worker's copy may have changes that main never got, and goes once main reaches the worker again.
	if err := rig.manager.Reconnect(context.Background(), "beelink"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); !slices.Contains(rig.took(), "delete "+placement.Move); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the stale copy was not deleted")
		}
	}
}

// gripi sudo in a session on a worker runs the command there, but not once the session is on its way back.
func TestSudoRunsTheCommandOnTheWorkerOfTheSession(t *testing.T) {
	rig := newMoveRig(t)
	ctx := context.Background()
	var output strings.Builder
	if _, err := rig.manager.Sudo(ctx, rig.key, rig.feature, []string{"id"}, []byte("secret"), &output, io.Discard); !refused(err) {
		t.Fatalf("Sudo in a session on main = %v", err)
	}
	rig.move()
	rig.took()

	code, err := rig.manager.Sudo(ctx, rig.key, rig.feature, []string{"id", "-u"}, []byte("secret"), &output, io.Discard)
	if err != nil || code != 3 || output.String() != "ran\n" {
		t.Fatalf("Sudo = %d, %v, printed %q", code, err, output.String())
	}
	if events, want := rig.took(), []string{"sudo " + sessionID(rig.key) + " in " + rig.feature + ": id -u with secret"}; !slices.Equal(events, want) {
		t.Fatalf("the worker did %q, want %q", events, want)
	}
	if err := rig.manager.options.Placements.SetStage(rig.key, releasing); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.manager.Sudo(ctx, rig.key, rig.feature, []string{"id"}, []byte("secret"), &output, io.Discard); !refused(err) {
		t.Fatalf("Sudo in a session that is coming back = %v", err)
	}
}

// Main remembers whom the sessions on a worker run as and whether they can use Docker, as the worker said when main
// last connected to it, and logs why they can't.
func TestTheStatusOfAWorkerSaysWhetherItsSessionsCanUseDocker(t *testing.T) {
	rig := newMoveRig(t)
	if status := rig.manager.Status("beelink"); status.User != "" || status.Docker != "" {
		t.Fatalf("before main connected, the status is %+v", status)
	}
	rig.docker = "sessions on worker beelink run as gripi, who can't use Docker there (permission denied)"
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	if err := rig.manager.Reconnect(context.Background(), "beelink"); err != nil {
		t.Fatal(err)
	}
	link := rig.link()
	link.goneAt = rig.now
	close(link.lost)
	for rig.manager.Status("beelink").State == Connected {
		time.Sleep(time.Millisecond)
	}
	if status := rig.manager.Status("beelink"); status.User != "gripi" || status.Docker != rig.docker {
		t.Fatalf("the status = %+v", status)
	}
	if !strings.Contains(logged.String(), "Docker: "+rig.docker) {
		t.Fatalf("the log says %q", logged.String())
	}
}

func TestAReconnectPreparesTheMovesAgainAndRestartsTheirMirrors(t *testing.T) {
	rig := newMoveRig(t)
	placement := rig.move()
	link := rig.link()
	link.goneAt = rig.now
	close(link.lost)
	for rig.manager.Status("beelink").State == Connected {
		time.Sleep(time.Millisecond)
	}
	rig.took()

	command, release, err := rig.manager.Launch(agent.Process{Key: rig.key, Command: []string{"pi"}})
	if err != nil || command == nil {
		t.Fatal(err)
	}
	release()
	id := placement.Move
	if events := rig.took(); !slices.Equal(events[:4], []string{"connect", "prepare " + id, "mirror " + id + " ", "mirror " + id + " " + rig.key}) || !slices.Contains(events, "create "+sessionID(rig.key)) {
		t.Fatalf("the launch after the reconnect did %q", events)
	}
}

func TestForwardMakesAPortOfTheWorkerAvailableOnMainForTheSessionsThatAskedForIt(t *testing.T) {
	rig := newMoveRig(t)
	ctx := context.Background()
	if _, err := rig.manager.Forward(ctx, rig.key, 5173); !refused(err) {
		t.Fatalf("Forward of a session on main = %v", err)
	}
	rig.move()
	other := filepath.Join(rig.sessions, "--app-feature--", "session.jsonl")
	if err := rig.manager.Move(ctx, other, rig.feature, "beelink", rig.prepare); err != nil {
		t.Fatal(err)
	}
	// Something on main listens at the worker's port already.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	port := taken.Addr().(*net.TCPAddr).Port
	forwards := func(key string) []Forwarded {
		location, _ := rig.manager.Location(key)
		return location.Forwards
	}
	rig.took()

	local, err := rig.manager.Forward(ctx, rig.key, port)
	if err != nil || local <= port {
		t.Fatalf("Forward of a port that main uses = %d, %v", local, err)
	}
	if again, err := rig.manager.Forward(ctx, other, port); again != local || err != nil {
		t.Fatalf("Forward of a forwarded port for another session = %d, %v", again, err)
	}
	if events := rig.took(); !slices.Equal(events, []string{fmt.Sprintf("forward %d to %d", port, local)}) {
		t.Fatalf("forwards: %q", events)
	}
	for _, key := range []string{rig.key, other} {
		if got := forwards(key); !slices.Equal(got, []Forwarded{{Port: port, Local: local}}) {
			t.Fatalf("the forwards of %s = %+v", key, got)
		}
	}
	// Sessions claim and drop forwards at once.
	var claims sync.WaitGroup
	for range 4 {
		claims.Go(func() {
			for range 20 {
				rig.manager.Forward(ctx, other, port)
				rig.manager.Unforward(other, port)
				rig.manager.Location(rig.key)
			}
		})
	}
	claims.Wait()
	if err := rig.manager.Unforward(other, port); !refused(err) {
		t.Fatalf("Unforward of a port that the session doesn't forward = %v", err)
	}
	if got := forwards(other); len(got) != 0 || len(rig.took()) != 0 || len(forwards(rig.key)) != 1 {
		t.Fatalf("after the other session's unforward, its forwards are %+v, the session's %+v", got, forwards(rig.key))
	}
	// A forward ends with the return of the last session that asked for it, or with the connection.
	if err := rig.manager.Return(ctx, rig.key, rig.prepare); err != nil {
		t.Fatal(err)
	}
	if events := rig.took(); !slices.Contains(events, fmt.Sprintf("unforward %d", port)) {
		t.Fatalf("the return did %q", events)
	}
	if _, err := rig.manager.Forward(ctx, other, port); err != nil {
		t.Fatal(err)
	}
	close(rig.link().lost)
	for len(forwards(other)) != 0 {
		time.Sleep(time.Millisecond)
	}
}

func TestLocationSaysThatASessionIsMovingToAWorker(t *testing.T) {
	rig := newMoveRig(t)
	preparing, prepared := make(chan struct{}), make(chan struct{})
	moved := make(chan error, 1)
	go func() {
		moved <- rig.manager.Move(context.Background(), rig.key, rig.repository, "beelink", func(context.Context, func(string) bool) (func(), error) {
			close(preparing)
			<-prepared
			return func() {}, nil
		})
	}()
	<-preparing
	if location, found := rig.manager.Location(rig.key); !found || location.Worker != "beelink" || !location.Copying.Equal(rig.now) || location.Repository != rig.repository {
		t.Fatalf("location of a moving session = %+v, %v", location, found)
	}
	if _, _, err := rig.manager.Launch(agent.Process{Key: rig.key}); !refused(err) || !strings.Contains(err.Error(), "moving") {
		t.Fatalf("Launch of a moving session = %v", err)
	}
	close(prepared)
	if err := <-moved; err != nil {
		t.Fatal(err)
	}
	if location, _ := rig.manager.Location(rig.key); !location.Copying.IsZero() || location.State != Connected {
		t.Fatalf("location of the moved session = %+v", location)
	}
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
	gripi, _ := buildExecutables(t)
	manager := NewManager(ManagerOptions{
		Destination: func(string) (string, error) { return "gripi@worker", nil },
		StateDir:    t.TempDir(), Pi: rig.pi, SocketPath: socket, Store: fakeStore{}, SessionsRoot: t.TempDir(),
		ServeSocket: func(string, WorkerPlacement) (string, func() error, error) {
			t.Error("the worker got a gripi socket")
			return "", nil, errors.New("no socket in this test")
		},
		Connection: Options{Executable: gripi, InstallDir: "gripi-worker", Unit: "gripi-test-" + newMoveID(), RuntimeDir: shortTempDir(t)},
	})
	t.Cleanup(manager.Close)

	err = manager.Reconnect(context.Background(), "beelink")
	if err == nil || !strings.Contains(err.Error(), "shares") || !strings.Contains(err.Error(), rig.pi.AgentDir) {
		t.Fatalf("Reconnect to a worker that shares main's files = %v", err)
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

// A worker that main reaches has the session until it releases it, also when the session's mirrors don't start.
func TestAReturnFromAWorkerThatMainReachesNeedsTheWorker(t *testing.T) {
	rig := newMoveRig(t)
	placement := rig.move()
	rig.manager.mu.Lock()
	mirrors := rig.manager.workers["beelink"].mirrors[placement.Move]
	rig.manager.mu.Unlock()
	mirrors.stop()
	rig.fail("prepare", errors.New("worker: the copy is busy"))
	// Long after the gateway started, when a worker that main can't reach would be gone, and while another session's
	// copy goes to the worker.
	rig.mu.Lock()
	rig.now = rig.now.Add(time.Hour)
	rig.mu.Unlock()
	rig.manager.setState(rig.manager.workers["beelink"], Copying)

	if err := rig.manager.Return(context.Background(), rig.key, rig.prepare); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("Return = %v", err)
	}
	if found, placed := rig.manager.options.Placements.Find(rig.key); !placed || found.Stage != "" {
		t.Fatalf("the session after the failed return: %+v, %v", found, placed)
	}
	rig.fail("prepare", nil)
	if err := rig.manager.Return(context.Background(), rig.key, rig.prepare); err != nil {
		t.Fatal(err)
	}
}

// The sessions that a move waits for are those in the repository's worktrees, also in one made while it waits.
func TestTheFoldersOfARepositoryAreAlsoThoseOfAWorktreeMadeSince(t *testing.T) {
	rig := newMoveRig(t)
	in := inRepository(context.Background(), rig.gitDir)
	made := filepath.Join(rig.home, "Work", ".worktrees", "app", "made")
	if in(made) || !in(rig.other) {
		t.Fatal("the repository's folders are wrong")
	}
	git(t, rig.repository, "worktree", "add", "-q", "-b", "made", made)
	for deadline := time.Now().Add(time.Second); !in(filepath.Join(made, "src")); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a worktree made since is not the repository's")
		}
	}
}

// A gateway that stopped while a session moved cleans up after the move when it starts again.
func TestAMoveThatTheGatewayDidntFinishIsCleanedUpWhenItStartsAgain(t *testing.T) {
	rig := newMoveRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	rig.seeding = make(chan struct{})
	moved := make(chan error, 1)
	go func() { moved <- rig.manager.Move(ctx, rig.key, rig.repository, "beelink", rig.prepare) }()
	<-rig.seeding
	id := rig.prepared[0].ID
	defer func() {
		cancel()
		<-moved
	}()

	rig.restart()
	if _, err := os.Stat(rig.manager.moveFolder(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("main's side of the unfinished move: %v", err)
	}
	if refs := git(t, rig.repository, "for-each-ref", "refs/gripi"); refs != "" {
		t.Fatalf("the unfinished move left the refs %s", refs)
	}
	if _, found := rig.manager.Location(rig.key); found {
		t.Fatal("the session of the unfinished move is on the worker")
	}
	rig.took()
	if err := rig.manager.Reconnect(context.Background(), "beelink"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); !slices.Contains(rig.took(), "delete "+id); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the worker's copy of the unfinished move was not deleted")
		}
	}
}
