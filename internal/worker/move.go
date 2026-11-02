package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/worker/mirror"
	"github.com/melounvitek/gripi/internal/worker/reconcile"
)

// A session moves to a worker alone, on a private copy of its repository there: the common Git folder and the
// worktrees that the session uses. Main keeps the move's base and a shadow of the copy, which the copy's mirror keeps
// up to date, in moves/<move> in its state folder, and the session's file at its path. A return brings what the
// session changed to main's folders, which the repository's other sessions kept changing meanwhile, and keeps aside
// in returns/<move> what both changed.

// releaseTimeout bounds waiting for main's copies of a move to end after the worker released the move.
const releaseTimeout = 30 * time.Second

func (manager *Manager) moveFolder(id string) string {
	return filepath.Join(manager.options.StateDir, "moves", id)
}

func (manager *Manager) recoveryFolder(id string) string {
	return filepath.Join(manager.options.StateDir, "returns", id)
}

func (manager *Manager) home() (string, error) {
	if home := manager.options.Connection.Home; home != "" {
		return home, nil
	}
	return os.UserHomeDir()
}

// Selection returns what moves with the session whose key is key, in the folder cwd: the main checkout of its
// repository, and the worktrees that its copy gets, the session's own first. It refuses what can't move.
func (manager *Manager) Selection(key, cwd string) (repository string, roots []string, err error) {
	selection, _, err := manager.selection(context.Background(), key, cwd)
	return filepath.Dir(selection.GitDir), selection.Roots, err
}

// selection finds what moves with a session in cwd, as main's canonical paths: the common Git folder of its
// repository, the worktree that holds cwd and those whose files the session's tool calls named, and the private
// roots, the folders directly in main's home folder that hold them. It refuses what can't move: a folder outside
// Git or main's home folder, and a private root that holds Pi's or Gripi's own folders or the session's file.
func (manager *Manager) selection(ctx context.Context, key, cwd string) (reconcile.Selection, []string, error) {
	home, err := manager.home()
	if err != nil {
		return reconcile.Selection{}, nil, err
	}
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	gitDir, worktrees, err := registeredWorktrees(ctx, cwd)
	if err != nil {
		return reconcile.Selection{}, nil, err
	}
	own := worktreeOf(worktrees, cwd)
	if own == "" {
		return reconcile.Selection{}, nil, refuse("%s is in none of the worktrees of %s", cwd, filepath.Dir(gitDir))
	}
	selection := reconcile.Selection{Cwd: cwd, GitDir: gitDir, Roots: []string{own}}
	if reader, ok := manager.options.Store.(agent.ToolPathsReader); ok {
		paths, err := reader.ToolPaths(key, cwd, home)
		if err != nil {
			return reconcile.Selection{}, nil, err
		}
		for _, path := range paths {
			if real, err := filepath.EvalSymlinks(path); err == nil {
				path = real
			}
			if root := worktreeOf(worktrees, path); root != "" && !slices.Contains(selection.Roots, root) {
				selection.Roots = append(selection.Roots, root)
			}
		}
	}
	var private []string
	for _, folder := range append([]string{gitDir}, selection.Roots...) {
		relative, err := filepath.Rel(home, folder)
		if err != nil || relative == "." || !filepath.IsLocal(relative) {
			return reconcile.Selection{}, nil, refuse("%s is outside the home folder %s, so it can't move to a worker yet", folder, home)
		}
		root := filepath.Join(home, strings.Split(relative, string(filepath.Separator))[0])
		if !slices.Contains(private, root) {
			private = append(private, root)
		}
	}
	// The move's processes see the private roots from its copy, but need Pi's and Gripi's folders, and the session's
	// file, as they are.
	for _, root := range private {
		if own := manager.ownFolder(root, filepath.Dir(key)); own != "" {
			return reconcile.Selection{}, nil, refuse("the session can't move to a worker, because %s holds %s, which the worker's copy would hide", root, own)
		}
	}
	slices.Sort(private)
	return selection, private, nil
}

// registeredWorktrees returns the common Git folder of the repository that holds folder, which must be in its main
// checkout, and the folders of the repository's worktrees that exist, the main checkout first.
func registeredWorktrees(ctx context.Context, folder string) (gitDir string, worktrees []string, err error) {
	output, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", folder, "rev-parse", "--path-format=absolute", "--git-common-dir", "--is-bare-repository").Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", nil, refuse("%s is not in a Git repository; only sessions in one can move to a worker", folder)
	}
	if err != nil {
		return "", nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 || lines[1] == "true" || filepath.Base(lines[0]) != ".git" {
		return "", nil, refuse("the repository of %s has no main checkout with its Git folder in it, so its sessions can't move to a worker", folder)
	}
	gitDir = filepath.Clean(lines[0])
	if real, err := filepath.EvalSymlinks(gitDir); err == nil {
		gitDir = real
	}
	return gitDir, existingWorktrees(ctx, gitDir), nil
}

// existingWorktrees returns the folders of the worktrees of the repository whose common Git folder is gitDir that
// exist, the main checkout first.
func existingWorktrees(ctx context.Context, gitDir string) []string {
	output, _ := exec.CommandContext(ctx, "git", "--no-optional-locks", "--git-dir", gitDir, "worktree", "list", "--porcelain").Output()
	worktrees := []string{filepath.Dir(gitDir)}
	for _, line := range strings.Split(string(output), "\n") {
		if path, found := strings.CutPrefix(line, "worktree "); found && !slices.Contains(worktrees, path) {
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				worktrees = append(worktrees, path)
			}
		}
	}
	return worktrees
}

// worktreeOf returns the worktree among worktrees that holds path, the innermost one, if any.
func worktreeOf(worktrees []string, path string) string {
	found := ""
	for _, root := range worktrees {
		if holds(root, path) && len(root) > len(found) {
			found = root
		}
	}
	return found
}

// inRepository reports whether a folder is in a worktree or the Git folder of the repository whose common Git folder
// is gitDir, also in a worktree made since, which it learns of within a moment.
func inRepository(ctx context.Context, gitDir string) func(folder string) bool {
	var mu sync.Mutex
	var folders []string
	var read time.Time
	return func(folder string) bool {
		if real, err := filepath.EvalSymlinks(folder); err == nil {
			folder = real
		}
		mu.Lock()
		if time.Since(read) > 100*time.Millisecond {
			folders, read = append(existingWorktrees(ctx, gitDir), gitDir), time.Now()
		}
		current := folders
		mu.Unlock()
		return slices.ContainsFunc(current, func(root string) bool { return holds(root, folder) })
	}
}

// ownFolder returns Pi's or Gripi's own file or folder, or one of others, that folder holds or is in, if any: Pi's
// agent folder, with its logins, the sessions' root, Gripi's state folder, its other own paths and its socket.
func (manager *Manager) ownFolder(folder string, others ...string) string {
	paths := func(path string) []string {
		if real, err := filepath.EvalSymlinks(path); err == nil && real != path {
			return []string{path, real}
		}
		return []string{path}
	}
	own := append([]string{manager.options.Pi.AgentDir, manager.options.SessionsRoot, manager.options.StateDir}, manager.options.GatewayPaths...)
	if manager.options.SocketPath != "" {
		own = append(own, filepath.Dir(manager.options.SocketPath))
	}
	for _, own := range append(own, others...) {
		if own == "" {
			continue
		}
		for _, ownPath := range paths(own) {
			for _, path := range paths(folder) {
				if holds(ownPath, path) || holds(path, ownPath) {
					return own
				}
			}
		}
	}
	return ""
}

// holds reports whether path is folder or in it.
func holds(folder, path string) bool {
	return path == folder || strings.HasPrefix(path, folder+"/")
}

// Move moves the session whose key is key, in the folder cwd, to the worker of a name: main takes the base of the
// session's repository, the worker gets a copy of it, and the session runs there on the copy from then on. Before
// main takes the base, prepare must stop the session's own Pi, hold the new prompts of the repository's other sessions
// on main, whose folders inRepository passes, until release, and return once none of them works.
func (manager *Manager) Move(ctx context.Context, key, cwd, name string, prepare func(ctx context.Context, inRepository func(folder string) bool) (release func(), err error)) error {
	if _, err := manager.options.Destination(name); err != nil {
		return refusal{err}
	}
	if placement, placed := manager.options.Placements.Find(key); placed {
		return refuse("the session is on worker %s already", placement.Worker)
	}
	done, err := manager.startMoving(key, name, false)
	if err != nil {
		return err
	}
	defer done()
	selection, private, err := manager.selection(ctx, key, cwd)
	if err != nil {
		return err
	}
	manager.mu.Lock()
	manager.moving[key].repository = filepath.Dir(selection.GitDir)
	manager.mu.Unlock()
	placement := Placement{Session: key, Move: newMoveID(), Worker: name, Cwd: selection.Cwd, Repository: selection.GitDir, Roots: selection.Roots, Private: private}
	unlock := manager.repositories.Lock(selection.GitDir)
	release, err := prepare(ctx, inRepository(ctx, selection.GitDir))
	if err != nil {
		unlock()
		return err
	}
	// A move that the gateway didn't finish is cleaned up when it starts again.
	err = manager.options.Placements.Begin(placement)
	if err == nil {
		folder := manager.moveFolder(placement.Move)
		_, err = reconcile.Snapshot(ctx, placement.Move, selection, filepath.Join(folder, "base"), filepath.Join(folder, "shadow"))
	}
	release()
	unlock()
	if err == nil {
		err = manager.copyToWorker(ctx, placement)
	}
	if err != nil {
		manager.abandon(placement)
	}
	return err
}

// copyToWorker gives the worker the copy of a move, from main's shadow, and the session's file, starts the move's
// mirrors, and places the session on the worker once main's copies have their first barriers.
func (manager *Manager) copyToWorker(ctx context.Context, placement Placement) error {
	worker, err := manager.connect(ctx, placement.Worker, "")
	if err != nil {
		return err
	}
	conn, err := manager.connection(worker)
	if err != nil {
		return err
	}
	home, err := manager.home()
	if err != nil {
		return err
	}
	manager.setState(worker, Copying)
	err = func() error {
		if _, err := conn.PrepareMove(ctx, placement.move()); err != nil {
			return err
		}
		if err := conn.UploadMove(ctx, placement.Move, filepath.Join(manager.moveFolder(placement.Move), "shadow")+home); err != nil {
			return fmt.Errorf("copy the repository to worker %s: %w", placement.Worker, err)
		}
		if err := conn.UploadSessionFile(ctx, placement.Session); err != nil {
			return fmt.Errorf("copy the session to worker %s: %w", placement.Worker, err)
		}
		return conn.SeedMove(ctx, placement.Move)
	}()
	manager.mu.Lock()
	if worker.conn == conn {
		worker.state = Connected
	}
	manager.mu.Unlock()
	if err == nil {
		err = manager.startMirrors(ctx, worker, placement)
	}
	if err == nil {
		err = manager.waitForMirrors(ctx, worker, placement.Move)
	}
	if err == nil {
		err = manager.options.Placements.Add(placement)
	}
	if err != nil {
		manager.mu.Lock()
		mirrors := worker.mirrors[placement.Move]
		delete(worker.mirrors, placement.Move)
		manager.mu.Unlock()
		if mirrors != nil {
			mirrors.stop()
		}
	}
	return err
}

// abandon cleans up after a move that failed: main's side of it, the refs that keep its base, and the worker's copy,
// which it may have.
func (manager *Manager) abandon(placement Placement) {
	if err := os.RemoveAll(manager.moveFolder(placement.Move)); err != nil {
		log.Printf("remove what main kept of move %s: %v", placement.Move, err)
	}
	refs, err := exec.Command("git", "--git-dir", placement.Repository, "for-each-ref", "--format=delete %(refname)", "refs/gripi/moves/"+placement.Move+"/").Output()
	if err == nil {
		update := exec.Command("git", "--git-dir", placement.Repository, "update-ref", "--stdin")
		update.Stdin = bytes.NewReader(refs)
		var output []byte
		if output, err = update.CombinedOutput(); err != nil {
			err = fmt.Errorf("%w: %s", err, output)
		}
	}
	if err != nil {
		log.Printf("delete the refs of move %s in %s: %v", placement.Move, placement.Repository, err)
	}
	if err := manager.options.Placements.Abandon(placement); err != nil {
		log.Printf("forget move %s: %v", placement.Move, err)
		return
	}
	manager.mu.Lock()
	worker := manager.workers[placement.Worker]
	var conn link
	if worker != nil {
		conn = worker.conn
	}
	manager.mu.Unlock()
	if conn != nil {
		manager.background.Go(func() { manager.deleteCopies(placement.Worker, conn) })
	}
}

// Return brings the session whose key is key back from its worker, which releases the move, and brings what the session
// changed there to main's folders, keeping aside the worker's version of what main changed too. prepare is as in Move,
// and runs before the worker releases the move. When main has lost the worker, or cannot reach it, the session's
// changes come from main's copies as their last barriers left them, once the worker must have stopped the session. A
// return that failed or was interrupted finishes when it runs again.
func (manager *Manager) Return(ctx context.Context, key string, prepare func(ctx context.Context, inRepository func(folder string) bool) (release func(), err error)) error {
	placement, placed := manager.options.Placements.Find(key)
	if !placed {
		return refuse("the session is not on a worker")
	}
	done, err := manager.startMoving(key, placement.Worker, true)
	if err != nil {
		return err
	}
	defer done()
	unlock := manager.repositories.Lock(placement.Repository)
	defer unlock()
	var worker *remote
	if placement.Stage != released {
		if state := manager.Status(placement.Worker).State; state == Unreachable || state == Gone {
			err = manager.gone(placement.Worker, fmt.Errorf("worker %s is unreachable", placement.Worker))
		} else if worker, err = manager.connect(ctx, placement.Worker, placement.Move); err != nil && !manager.connected(placement.Worker) {
			// Only a worker that main can't reach stops the session by itself.
			err = manager.gone(placement.Worker, err)
		}
		if err != nil {
			return err
		}
	}
	release, err := prepare(ctx, inRepository(ctx, placement.Repository))
	if err != nil {
		return err
	}
	defer release()
	folder := manager.moveFolder(placement.Move)
	switch {
	case placement.Stage == released:
	case worker == nil:
		if err := manager.waitForTeardown(ctx, placement.Worker); err != nil {
			return err
		}
		// Opening main's copies finishes a barrier that a crash interrupted.
		if _, err := mirror.NewMovedDestination(filepath.Join(folder, "repository"), "/", filepath.Join(folder, "shadow")); err != nil {
			return err
		}
		if _, err := mirror.NewDestination(filepath.Join(folder, "session")); err != nil {
			return err
		}
		log.Printf("session %s continues on this machine without worker %s, as of the last changes that this machine got", key, placement.Worker)
	default:
		if err := manager.options.Placements.SetStage(key, releasing); err != nil {
			return err
		}
		if err := manager.release(ctx, worker, placement); err != nil {
			return err
		}
	}
	if err := manager.options.Placements.SetStage(key, released); err != nil {
		return err
	}
	recovery := manager.recoveryFolder(placement.Move)
	result, err := reconcile.Reconcile(ctx, filepath.Join(folder, "base"), filepath.Join(folder, "shadow"), recovery)
	if err != nil {
		return fmt.Errorf("bring the session's changes to this machine: %w", err)
	}
	var notice *Notice
	if len(result.Aside) > 0 {
		notice = &Notice{Session: key, Worker: placement.Worker, Repository: filepath.Dir(placement.Repository), Recovery: recovery, Result: result, Time: manager.options.Clock()}
		log.Printf("the return of session %s from worker %s kept %d changes as this machine had them; the worker's are in %s", key, placement.Worker, len(result.Aside), recovery)
	}
	if err := manager.options.Placements.Finish(key, notice); err != nil {
		return err
	}
	if _, err := manager.unforward(placement.Worker, key); err != nil {
		log.Printf("stop forwarding the ports of session %s on worker %s: %v", key, placement.Worker, err)
	}
	if err := os.RemoveAll(folder); err != nil {
		log.Printf("remove what main kept of move %s: %v", placement.Move, err)
	}
	if notice == nil {
		if err := reconcile.Discard(ctx, recovery); err != nil {
			log.Printf("drop the record of the return of session %s: %v", key, err)
		}
	}
	if worker != nil {
		if conn, err := manager.connection(worker); err == nil {
			manager.background.Go(func() { manager.deleteCopies(placement.Worker, conn) })
		}
	}
	return nil
}

// gone refuses with cause while the worker of a name may still run its sessions, whose Pi must never run again.
func (manager *Manager) gone(name string, cause error) error {
	if goneAt, now := manager.goneAt(name), manager.options.Clock(); now.Before(goneAt) {
		return refuse("%w; it may still run the session until %s, so it can continue on this machine from then (in %s)",
			cause, goneAt.Local().Format(time.TimeOnly), goneAt.Sub(now).Round(time.Second))
	}
	return nil
}

// release has the worker release a move: it kills the move's processes, and main's copies take the last changes of
// its mirrors, which then end.
func (manager *Manager) release(ctx context.Context, worker *remote, placement Placement) error {
	conn, err := manager.connection(worker)
	if err != nil {
		return err
	}
	barriers, err := conn.ReleaseMove(ctx, placement.Move)
	if err != nil {
		return fmt.Errorf("release the session on worker %s: %w", placement.Worker, err)
	}
	manager.mu.Lock()
	mirrors := worker.mirrors[placement.Move]
	delete(worker.mirrors, placement.Move)
	manager.mu.Unlock()
	if mirrors == nil {
		return fmt.Errorf("main's copies of the session on worker %s were not connected; try again", placement.Worker)
	}
	timeout := time.After(releaseTimeout)
	for _, last := range []struct {
		live    *liveMirror
		barrier uint64
	}{{mirrors.repository, barriers.Repository}, {mirrors.session, barriers.Session}} {
		select {
		case <-last.live.done:
		case <-timeout:
			mirrors.stop()
			return fmt.Errorf("main's copy of the session on worker %s did not end after its last barrier", placement.Worker)
		}
		if got := last.live.destination.Checkpoint().Barrier; got < last.barrier {
			return fmt.Errorf("main's copy of the session on worker %s has barrier %d, not its last one %d: %v", placement.Worker, got, last.barrier, last.live.err)
		}
	}
	return nil
}

// deleteCopies deletes the copies that the worker of a name keeps of moves that main no longer needs, and tries again
// a while later while one fails, as long as conn is connected.
func (manager *Manager) deleteCopies(name string, conn link) {
	for wait := time.Second; ; wait = min(2*wait, time.Minute) {
		failed := false
		for _, id := range manager.options.Placements.Stale(name) {
			ctx, cancel := context.WithTimeout(manager.ctx, time.Minute)
			err := conn.DeleteMove(ctx, id)
			cancel()
			if err == nil {
				err = manager.options.Placements.Deleted(name, id)
			}
			if err != nil {
				failed = true
				log.Printf("delete the copy of move %s on worker %s: %v", id, name, err)
			}
		}
		if !failed {
			return
		}
		select {
		case <-conn.Lost():
			return
		case <-manager.ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Notices returns the notices of the returns of a session that kept changes aside, oldest first.
func (manager *Manager) Notices(key string) []Notice {
	return manager.options.Placements.Notices(key)
}

// DismissNotices forgets the notices of a session, with the recovery records of their returns: the worker's versions
// and the refs that keep its commits.
func (manager *Manager) DismissNotices(ctx context.Context, key string) error {
	for _, notice := range manager.options.Placements.Notices(key) {
		if err := reconcile.Discard(ctx, notice.Recovery); err != nil {
			return err
		}
		if err := manager.options.Placements.Dismiss(notice.Recovery); err != nil {
			return err
		}
	}
	return nil
}

// moveMirrors are main's copies of a move's mirrors: the shadow of its copy and the session's file. They don't change
// once a worker has them.
type moveMirrors struct {
	repository, session *liveMirror
}

func (mirrors *moveMirrors) stop() {
	for _, live := range []*liveMirror{mirrors.repository, mirrors.session} {
		if live != nil {
			live.cancel()
			<-live.done
		}
	}
}

// startMirrors makes sure that both mirrors of a move run, the copy's and the session file's, with main's copies
// connected to them, on a worker that has prepared the move once more.
func (manager *Manager) startMirrors(ctx context.Context, worker *remote, placement Placement) error {
	manager.mu.Lock()
	mirrors := moveMirrors{}
	if current := worker.mirrors[placement.Move]; current != nil {
		mirrors = *current
	}
	manager.mu.Unlock()
	running := func(live *liveMirror) bool { return live != nil && live.running() }
	if running(mirrors.repository) && running(mirrors.session) {
		return nil
	}
	manager.mu.Lock()
	conn := worker.conn
	if conn == nil {
		manager.mu.Unlock()
		return fmt.Errorf("worker %s disconnected", worker.name)
	}
	// A disconnect waits until main's copies that start here are as their last barriers left them.
	worker.starting.Add(1)
	manager.mu.Unlock()
	defer worker.starting.Done()
	// After a reconnect, the helper is new.
	_, err := conn.PrepareMove(ctx, placement.move())
	folder := manager.moveFolder(placement.Move)
	if err == nil && !running(mirrors.repository) {
		var destination *mirror.Destination
		if destination, err = mirror.NewMovedDestination(filepath.Join(folder, "repository"), "/", filepath.Join(folder, "shadow")); err == nil {
			mirrors.repository, err = manager.attachMirror(ctx, worker.name, conn, placement, "", destination)
		}
	}
	if err == nil && !running(mirrors.session) {
		var destination *mirror.Destination
		if destination, err = mirror.NewDestination(filepath.Join(folder, "session")); err == nil {
			destination.File = placement.Session
			destination.Applied = func(_ mirror.Checkpoint, aside string, saved, replaced []string) {
				if len(saved) > 0 {
					log.Printf("session %s changed on this machine while it ran on worker %s; this machine's version is in %s", placement.Session, worker.name, aside)
				}
				// The guard waits for the session, which an operation may hold while it waits for this barrier.
				if manager.options.Rewritten != nil && slices.Contains(replaced, placement.Session) && !slices.Contains(saved, placement.Session) {
					go manager.options.Rewritten(placement.Session)
				}
			}
			mirrors.session, err = manager.attachMirror(ctx, worker.name, conn, placement, placement.Session, destination)
		}
	}
	manager.mu.Lock()
	if worker.conn != conn {
		manager.mu.Unlock()
		// The disconnect has stopped the mirrors that it knew of, and waits for these.
		mirrors.stop()
		return fmt.Errorf("worker %s disconnected", worker.name)
	}
	worker.mirrors[placement.Move] = &mirrors
	manager.mu.Unlock()
	return err
}

// attachMirror starts one of a move's mirrors on the worker, that of the session's file with file, and connects main's
// copy, destination, to it.
func (manager *Manager) attachMirror(ctx context.Context, name string, conn link, placement Placement, file string, destination *mirror.Destination) (*liveMirror, error) {
	if err := conn.StartMoveMirror(ctx, placement.Move, file); err != nil {
		return nil, err
	}
	command, release, err := conn.AttachMoveMirror(ctx, placement.Move, file)
	if err != nil {
		return nil, err
	}
	input, err := command.StdinPipe()
	if err != nil {
		release()
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		release()
		return nil, err
	}
	stderr := &lastLines{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		release()
		return nil, err
	}
	mirrorCtx, cancel := context.WithCancel(manager.ctx)
	live := &liveMirror{destination: destination, started: time.Now(), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(live.done)
		aside := filepath.Join(manager.moveFolder(placement.Move), "aside", time.Now().UTC().Format("20060102T150405.000000000Z"))
		live.err = destination.Serve(mirrorCtx, struct {
			io.Reader
			io.WriteCloser
		}{output, input}, aside)
		command.Wait()
		release()
		if live.err != nil && mirrorCtx.Err() == nil {
			log.Printf("mirror of session %s from worker %s: %v %s", placement.Session, name, live.err, stderr)
		}
	}()
	return live, nil
}

// waitForMirrors returns once main's copies of a move have the first barriers of their running mirrors, which a
// failed apply, a file that the worker cannot read or a stalled channel hold up.
func (manager *Manager) waitForMirrors(ctx context.Context, worker *remote, id string) error {
	ctx, cancel := context.WithTimeoutCause(ctx, firstBarrierTimeout,
		fmt.Errorf("main got no barrier from it within %s; the worker may be unable to read some of its files", firstBarrierTimeout))
	defer cancel()
	manager.mu.Lock()
	mirrors := worker.mirrors[id]
	manager.mu.Unlock()
	if mirrors == nil || mirrors.repository == nil || mirrors.session == nil {
		return fmt.Errorf("the session is not mirrored from worker %s", worker.name)
	}
	for _, live := range []*liveMirror{mirrors.repository, mirrors.session} {
		if err := waitForMirror(ctx, live); err != nil {
			return fmt.Errorf("mirror the session from worker %s: %w", worker.name, err)
		}
	}
	return nil
}

// waitForMirror returns once main's copy has applied the first barrier of its running mirror.
func waitForMirror(ctx context.Context, live *liveMirror) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !live.destination.Checkpoint().Time.After(live.started) {
		select {
		case <-live.done:
			return fmt.Errorf("the mirror stopped before its first barrier: %v", live.err)
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
		}
	}
	return nil
}
