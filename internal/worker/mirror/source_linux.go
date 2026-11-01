package mirror

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const watchMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MODIFY | unix.IN_CLOSE_WRITE |
	unix.IN_ATTRIB | unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_DELETE_SELF |
	unix.IN_MOVE_SELF | unix.IN_ONLYDIR | unix.IN_DONT_FOLLOW | unix.IN_EXCL_UNLINK

var errStopped = errors.New("mirror stopped")

// Source watches the live copy of a unit and sends its changes to a
// Destination.
type Source struct {
	unit     Unit
	opts     Options
	requests chan barrierRequest
	done     chan struct{} // closed when Run returns
	err      error         // what Run returned, once done is closed
	healthy  atomic.Bool
	failing  atomic.Bool          // the last barrier failed because something couldn't be read
	current  atomic.Pointer[Unit] // the unit's folders as Run last found them

	overflows  atomic.Int64
	beforeRead func() // lets tests stall the reader

	// Owned by Run's goroutine.
	*sender
	fd           int
	events       *os.File
	wdPath       map[int32]string
	pathWd       map[string]int32
	sentinelDir  string
	sentinelWd   int32
	sentinelSeq  uint64
	seenSentinel uint64
	qmu          sync.Mutex
	queue        []rawEvent // unbounded, so the kernel's queue rarely overflows
	qsig         chan struct{}
	moveFrom     *rawMove
	needRescan   bool
	wtChanged    bool
	ignChanged   bool
	indexes      map[string]string // index file → its worktree
	indexChanged map[string]bool   // worktrees
	barriered    uint64            // the changes seen before the last barrier
	unbarriered  time.Time         // when AutoBarrier first saw changes after it
	retryAt      time.Time         // when to retry a barrier that failed because something couldn't be read
}

type rawEvent struct {
	wd     int32
	mask   uint32
	cookie uint32
	name   string
}

type rawMove struct {
	cookie uint32
	path   string
	isDir  bool
}

type barrierRequest struct{ done chan barrierResult }

type barrierResult struct {
	seq     uint64
	writing []string
	err     error
}

func NewSource(unit Unit, opts Options) *Source {
	return &Source{unit: unit, opts: opts, requests: make(chan barrierRequest), done: make(chan struct{})}
}

// Healthy reports whether the source runs, the destination has applied
// its first barrier, and the last barrier didn't fail because something
// couldn't be read. A barrier that failed so is retried every second.
func (s *Source) Healthy() bool {
	select {
	case <-s.done:
		return false
	default:
		return s.healthy.Load() && !s.failing.Load()
	}
}

// Contains reports whether path is in one of the unit's folders, as worktrees
// added and removed since the source started have left them.
func (s *Source) Contains(path string) bool {
	if unit := s.current.Load(); unit != nil {
		return unit.Contains(path)
	}
	return s.unit.Contains(path)
}

// Barrier returns once the destination has durably applied every change made
// before the call, with the barrier's sequence number and the files left out
// because they are still being written.
func (s *Source) Barrier(ctx context.Context) (uint64, []string, error) {
	req := barrierRequest{done: make(chan barrierResult, 1)}
	select {
	case s.requests <- req:
	case <-s.done:
		return 0, nil, s.stopped()
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
	select {
	case r := <-req.done:
		return r.seq, r.writing, r.err
	case <-s.done:
		select {
		case r := <-req.done:
			return r.seq, r.writing, r.err
		default:
			return 0, nil, s.stopped()
		}
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

// stopped says why Run returned.
func (s *Source) stopped() error {
	if s.err == nil {
		return errStopped
	}
	return fmt.Errorf("%w: %w", errStopped, s.err)
}

// Run brings the destination at the other end of stream up to date, then
// mirrors changes until ctx ends or the stream fails. It closes stream, if it
// can, before returning. A Source runs only once.
func (s *Source) Run(ctx context.Context, stream io.ReadWriter) (err error) {
	defer func() {
		s.err = err
		close(s.done)
	}()
	defer closeOnDone(ctx, stream)()
	s.sender = newSender(s.unit, s.opts, stream)
	defer s.sender.close()
	s.sender.watch, s.sender.settling = s.addWatch, map[string]stamp{}
	s.wdPath, s.pathWd, s.indexChanged = map[int32]string{}, map[string]int32{}, map[string]bool{}
	s.qsig = make(chan struct{}, 1)

	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return fmt.Errorf("inotify: %w", err)
	}
	s.fd, s.events = fd, os.NewFile(uintptr(fd), "inotify")
	defer s.events.Close()
	if s.sentinelDir, err = os.MkdirTemp("", "gripi-mirror-"); err != nil {
		return err
	}
	defer os.RemoveAll(s.sentinelDir)
	wd, err := unix.InotifyAddWatch(fd, s.sentinelDir, unix.IN_CREATE)
	if err != nil {
		return fmt.Errorf("inotify: %w", err)
	}
	s.sentinelWd = int32(wd)
	readErr := make(chan error, 1)
	go func() { readErr <- s.read() }()

	s.mapIndexes()
	seen, err := s.scan()
	if err != nil {
		return err
	}
	if err := s.handshake(); err != nil {
		return err
	}
	s.markMissing(seen)
	answers := s.startReceiving()
	if err := s.runBarrier(nil); err != nil {
		return err
	}

	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	lastEvent := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return fmt.Errorf("inotify: %w", err)
		case err := <-answers:
			return err
		case <-s.qsig:
			if err := s.handleBatch(s.takeEvents()); err != nil {
				return err
			}
			lastEvent = time.Now()
		case req := <-s.requests:
			reqs := []barrierRequest{req}
			for more := true; more; {
				select {
				case req := <-s.requests:
					reqs = append(reqs, req)
				default:
					more = false
				}
			}
			if err := s.runBarrier(reqs); err != nil {
				return err
			}
		case <-tick.C:
			if !s.retryAt.IsZero() && time.Now().After(s.retryAt) {
				if err := s.runBarrier(nil); err != nil {
					return err
				}
				continue
			}
			quiet := time.Since(lastEvent) > 50*time.Millisecond
			if s.opts.AutoBarrier && s.marked != s.barriered {
				if s.unbarriered.IsZero() {
					s.unbarriered = time.Now()
				}
				if quiet || time.Since(s.unbarriered) > 500*time.Millisecond {
					if err := s.runBarrier(nil); err != nil {
						return err
					}
				}
				continue
			}
			// Send early, when quiet for a moment or soon after the first change, so barriers are quick.
			if len(s.dirty) > 0 && (quiet || time.Since(s.firstDirty) > 500*time.Millisecond) {
				if err := s.flush(false, nil); err != nil {
					return err
				}
			}
		}
	}
}

// runBarrier follows Git's own order: objects never change and are written
// before the refs that name them. So it drains events up to a sentinel, reads
// .git's metadata, drains up to a second sentinel and then sends every object
// created so far, which covers every ref just sent. Deleted objects go one
// barrier later. If something can't be read, the destination gets no
// barrier, and keeps what it received staged for the next one.
func (s *Source) runBarrier(reqs []barrierRequest) error {
	if err := s.syncSentinel(); err != nil {
		return err
	}
	if err := s.checkSkipped(); err != nil {
		return err
	}
	if err := s.retryUnread(); err != nil {
		return err
	}
	s.writing = nil
	if err := s.flush(true, s.isMeta); err != nil {
		return err
	}
	if err := s.syncSentinel(); err != nil {
		return err
	}
	if err := s.flush(true, s.inObjects); err != nil {
		return err
	}
	if err := s.flush(true, s.inWorktree); err != nil {
		return err
	}
	s.barriered, s.unbarriered = s.marked, time.Time{}
	if err := s.sendSettled(); err != nil {
		return err
	}
	if err := s.unreadError(); err != nil {
		s.failing.Store(true)
		s.retryAt = time.Now().Add(time.Second)
		for _, req := range reqs {
			req.done <- barrierResult{err: err}
		}
		return nil
	}
	s.failing.Store(false)
	s.retryAt = time.Time{}
	n := s.next
	s.next++
	slices.Sort(s.writing)
	writing := slices.Compact(slices.Clone(s.writing))
	ack, err := s.sendBarrier(n)
	if err != nil {
		return err
	}
	go func() {
		err := <-ack
		if err == nil {
			s.healthy.Store(true)
		}
		for _, req := range reqs {
			req.done <- barrierResult{n, writing, err}
		}
	}()
	return nil
}

// sendSettled sends the CompleteLines files that the barrier found or took for closed, once the events up to another
// sentinel show that their writers left them so: what was read of a file that Pi started to rewrite meanwhile may be
// only its start. A file that changed is left out as still being written, and its events bring another barrier.
func (s *Source) sendSettled() error {
	if len(s.settling) == 0 {
		return nil
	}
	paths := slices.Collect(maps.Keys(s.settling))
	if err := s.syncSentinel(); err != nil {
		return err
	}
	var files []string
	for _, p := range paths {
		if _, ok := s.settling[p]; ok {
			files = append(files, p)
		}
	}
	err := s.sendFiles(files)
	clear(s.settling)
	for _, p := range paths {
		if _, left := s.dirty[p]; left {
			s.writing = append(s.writing, p)
		}
	}
	return err
}

func (s *Source) syncSentinel() error {
	s.sentinelSeq++
	id := s.sentinelSeq
	if err := os.WriteFile(filepath.Join(s.sentinelDir, strconv.FormatUint(id, 10)), nil, 0o600); err != nil {
		return err
	}
	timeout := time.After(10 * time.Second)
	for s.seenSentinel < id {
		select {
		case <-s.qsig:
			if err := s.handleBatch(s.takeEvents()); err != nil {
				return err
			}
		case <-timeout:
			return errors.New("mirror: file events stopped arriving")
		}
	}
	return nil
}

// checkSkipped looks for excluded files and folders that Git tracks now,
// through git add -f or a checkout, once an index changed.
func (s *Source) checkSkipped() error {
	for root := range s.indexChanged {
		delete(s.indexChanged, root)
		c := s.cls[root]
		if c == nil {
			continue
		}
		c.indexChanged()
		var paths []string
		for p := range s.skipped {
			if s.rootOf(p) == root {
				paths = append(paths, p[len(root)+1:])
			}
		}
		if len(paths) == 0 {
			continue
		}
		for _, rel := range c.refreshTracked(paths) {
			p := filepath.Join(root, rel)
			delete(s.skipped, p)
			if err := s.walk(p, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Source) addWatch(dir string) (bool, error) {
	wd, err := unix.InotifyAddWatch(s.fd, dir, watchMask)
	if errors.Is(err, unix.ENOSPC) {
		return false, fmt.Errorf("mirror: too many folders to watch at %s; raise the fs.inotify.max_user_watches sysctl", dir)
	}
	if err != nil {
		s.cantRead(dir, err)
		return false, nil
	}
	old, had := s.pathWd[dir]
	if p, ok := s.wdPath[int32(wd)]; ok && p != dir {
		delete(s.pathWd, p)
	}
	s.wdPath[int32(wd)] = dir
	s.pathWd[dir] = int32(wd)
	return !had || old != int32(wd), nil
}

func (s *Source) unwatch(dir string) {
	for wd, p := range s.wdPath {
		if within(p, dir) {
			unix.InotifyRmWatch(s.fd, uint32(wd))
			delete(s.wdPath, wd)
			delete(s.pathWd, p)
		}
	}
}

func (s *Source) read() error {
	buf := make([]byte, 256<<10)
	for {
		if s.beforeRead != nil {
			s.beforeRead()
		}
		n, err := s.events.Read(buf)
		if err != nil {
			return err
		}
		var events []rawEvent
		for off := 0; off+unix.SizeofInotifyEvent <= n; {
			e := rawEvent{
				wd:     int32(binary.NativeEndian.Uint32(buf[off:])),
				mask:   binary.NativeEndian.Uint32(buf[off+4:]),
				cookie: binary.NativeEndian.Uint32(buf[off+8:]),
			}
			length := int(binary.NativeEndian.Uint32(buf[off+12:]))
			off += unix.SizeofInotifyEvent
			e.name = strings.TrimRight(string(buf[off:off+length]), "\x00")
			off += length
			events = append(events, e)
		}
		s.qmu.Lock()
		s.queue = append(s.queue, events...)
		s.qmu.Unlock()
		select {
		case s.qsig <- struct{}{}:
		default:
		}
	}
}

func (s *Source) takeEvents() []rawEvent {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	events := s.queue
	s.queue = nil
	return events
}

func (s *Source) handleBatch(events []rawEvent) error {
	for {
		for _, e := range events {
			if err := s.handleEvent(e); err != nil {
				return err
			}
		}
		if s.moveFrom == nil {
			break
		}
		select { // the move's other half may be on its way
		case <-s.qsig:
			events = s.takeEvents()
			continue
		case <-time.After(5 * time.Millisecond):
			s.resolveMove()
		}
		break
	}
	if s.ignChanged {
		s.ignChanged = false
		for _, c := range s.cls {
			c.start()
		}
		s.needRescan = true
	}
	// Worktrees whose folders went while the source ran are no longer the unit's, which a rescan must know, because it
	// fails on a folder of the unit that isn't there. A folder outside Git has none, also inside another repository.
	if s.gitDir != "" && (s.wtChanged || s.needRescan) {
		if err := s.refreshWorktrees(); err != nil {
			return err
		}
	}
	if s.needRescan {
		return s.rescan()
	}
	return nil
}

func (s *Source) handleEvent(e rawEvent) error {
	if e.mask&unix.IN_Q_OVERFLOW != 0 {
		// Lost events may have registered worktrees or changed ignore files and indexes.
		s.overflows.Add(1)
		s.needRescan, s.wtChanged, s.ignChanged = true, true, true
		return nil
	}
	if e.wd == s.sentinelWd {
		if id, err := strconv.ParseUint(e.name, 10, 64); err == nil {
			s.seenSentinel = max(s.seenSentinel, id)
			os.Remove(filepath.Join(s.sentinelDir, e.name))
		}
		return nil
	}
	dir, ok := s.wdPath[e.wd]
	if !ok {
		return nil
	}
	if e.mask&unix.IN_IGNORED != 0 {
		delete(s.wdPath, e.wd)
		if s.pathWd[dir] == e.wd {
			delete(s.pathWd, dir)
		}
		return nil
	}
	if e.mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0 {
		if s.isRoot(dir) {
			s.mark(dir, writesUnknown)
		}
		return nil
	}
	p := filepath.Join(dir, e.name)
	isDir := e.mask&unix.IN_ISDIR != 0
	if s.moveFrom != nil && (e.mask&unix.IN_MOVED_TO == 0 || e.cookie != s.moveFrom.cookie) {
		s.resolveMove()
	}
	s.noteGitChange(p)
	switch {
	case e.mask&unix.IN_MOVED_FROM != 0:
		s.moveFrom = &rawMove{e.cookie, p, isDir}
	case e.mask&unix.IN_MOVED_TO != 0:
		if s.moveFrom != nil {
			from := s.moveFrom.path
			s.moveFrom = nil
			return s.moved(from, p, isDir)
		}
		return s.created(p, isDir)
	case e.mask&unix.IN_CREATE != 0:
		return s.created(p, isDir)
	default:
		if e.mask&unix.IN_DELETE != 0 && isDir {
			s.unwatch(p)
		}
		w := writesClosed // IN_CLOSE_WRITE, or a deletion
		if e.mask&unix.IN_MODIFY != 0 {
			w = writesOpen
		}
		if e.mask&unix.IN_ATTRIB != 0 {
			w = s.dirty[p] // a change of mode or time doesn't end a write
		}
		s.mark(p, w)
	}
	return nil
}

func (s *Source) noteGitChange(p string) {
	if !s.inGit(p) || p == s.gitDir {
		if filepath.Base(p) == ".gitignore" {
			s.ignChanged = true
		}
		return
	}
	if root, ok := s.indexes[p]; ok {
		s.indexChanged[root] = true
	}
	rel := p[len(s.gitDir)+1:]
	if rel == "info/exclude" {
		s.ignChanged = true
	}
	if rest, ok := strings.CutPrefix(rel, "worktrees/"); ok {
		_, file, _ := strings.Cut(rest, "/")
		if file == "" || file == "gitdir" || file == "locked" {
			s.wtChanged = true
		}
	}
}

func (s *Source) created(p string, isDir bool) error {
	if !isDir {
		s.mark(p, writesUnknown)
		return nil
	}
	if s.excluded(p, true, 0) {
		s.skip(p)
		return nil
	}
	return s.walk(p, nil)
}

func (s *Source) resolveMove() {
	m := s.moveFrom
	s.moveFrom = nil
	s.gone(m.path, m.isDir)
}

func (s *Source) gone(p string, isDir bool) {
	if isDir {
		s.unwatch(p)
	}
	s.mark(p, writesUnknown)
}

func rewrite(p, from, to string) (string, bool) {
	if within(p, from) {
		return to + p[len(from):], true
	}
	return p, false
}

// moved mirrors a rename as a move, so that unchanged contents don't travel again.
func (s *Source) moved(from, to string, isDir bool) error {
	sameArea := s.rootOf(from) == s.rootOf(to) && s.inGit(from) == s.inGit(to)
	if !isDir {
		e, sent := s.sent[from]
		_, dirty := s.dirty[from]
		if sent && !dirty && e.Type == 'f' && sameArea && !s.excluded(from, false, e.Size) && !s.excluded(to, false, e.Size) {
			if err := s.c.send(&frame{Kind: kindMove, Path: from, To: to}); err != nil {
				return err
			}
			delete(s.sent, from)
			e.Path, e.ino = to, 0
			s.sent[to] = e
		} else {
			s.mark(from, writesUnknown)
		}
		s.mark(to, writesUnknown) // only checked if it was moved
		return nil
	}
	outside := func(p string) bool { return s.rootOf(p) == "" || s.excluded(p, true, 0) }
	switch fromOut, toOut := outside(from), outside(to); {
	case fromOut && toOut:
		return nil
	case fromOut:
		return s.created(to, true)
	case toOut || !sameArea:
		s.gone(from, true)
		return s.created(to, true)
	}
	// The folder's watches moved with it.
	for wd, p := range s.wdPath {
		if np, ok := rewrite(p, from, to); ok {
			s.wdPath[wd] = np
			delete(s.pathWd, p)
			s.pathWd[np] = wd
		}
	}
	dirty := map[string]writes{}
	for p, w := range s.dirty {
		np, _ := rewrite(p, from, to)
		dirty[np] = w
	}
	s.dirty = dirty
	if s.sentUnder(from) {
		if err := s.c.send(&frame{Kind: kindMove, Path: from, To: to}); err != nil {
			return err
		}
		sent := map[string]entry{}
		for p, e := range s.sent {
			if np, ok := rewrite(p, from, to); ok {
				e.Path, e.ino = np, 0
				sent[np] = e
			} else if !within(p, to) {
				sent[p] = e
			}
		}
		s.sent = sent
	}
	return s.walk(to, nil) // checks what moved and watches what is new
}

func (s *Source) refreshWorktrees() error {
	s.wtChanged = false
	worktrees, err := listWorktrees(s.main)
	if err != nil {
		return nil // the main folder is gone, which is mirrored as such
	}
	added := false
	for _, w := range worktrees {
		if !s.isRoot(w) {
			s.addRoot(w)
			added = true
		}
		if _, ok := s.pathWd[w]; !ok { // new, or listed before its folder existed
			if err := s.walk(w, nil); err != nil {
				return err
			}
		}
	}
	for _, r := range slices.Clone(s.roots) {
		if !slices.Contains(worktrees, r) && r != s.gitDir {
			s.removeRoot(r)
			s.unwatch(r)
			s.mark(r, writesUnknown)
		}
	}
	s.current.Store(&Unit{Main: s.main, Worktrees: worktrees[1:], GitDir: s.gitDir})
	s.mapIndexes()
	if added {
		return s.c.send(&frame{Kind: kindRoots, Roots: s.roots})
	}
	return nil
}

// rescan stands in for every sentinel written so far, whose events may be
// lost, because it sees every change made before it started.
func (s *Source) rescan() error {
	s.needRescan = false
	s.seenSentinel = s.sentinelSeq
	seen, err := s.scan()
	if err != nil {
		return err
	}
	s.markMissing(seen)
	for wd, dir := range s.wdPath {
		if !seen[dir] { // gone, or excluded now
			unix.InotifyRmWatch(s.fd, uint32(wd))
			delete(s.wdPath, wd)
			delete(s.pathWd, dir)
		}
	}
	return nil
}

// openForWriting reports whether a process has the file open for writing, as /proc shows, or whether that can't be
// told. It leaves out the processes whose files /proc doesn't show: other users', and this user's that are more
// privileged than the source, such as its systemd manager or ssh-agent. Pi, which writes the session files, is
// neither.
func openForWriting(file fs.FileInfo) bool {
	want, ok := file.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	for _, proc := range procs {
		if _, err := strconv.Atoi(proc.Name()); err != nil {
			continue
		}
		dir := "/proc/" + proc.Name()
		// Here and below, ErrPermission is a process that /proc doesn't show, and ErrNotExist one that ended or a file
		// that it closed meanwhile.
		fds, err := os.ReadDir(dir + "/fd")
		if err != nil && !errors.Is(err, fs.ErrPermission) && !errors.Is(err, fs.ErrNotExist) {
			return true
		}
		for _, fd := range fds {
			var st syscall.Stat_t
			if err := syscall.Stat(dir+"/fd/"+fd.Name(), &st); err != nil || st.Dev != want.Dev || st.Ino != want.Ino {
				if err != nil && !errors.Is(err, fs.ErrPermission) && !errors.Is(err, fs.ErrNotExist) {
					return true
				}
				continue
			}
			info, err := os.ReadFile(dir + "/fdinfo/" + fd.Name())
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			_, flags, _ := strings.Cut(string(info), "\nflags:\t")
			flags, _, _ = strings.Cut(flags, "\n")
			mode, perr := strconv.ParseUint(flags, 8, 64)
			if err != nil || perr != nil || mode&syscall.O_ACCMODE != syscall.O_RDONLY {
				return true
			}
		}
	}
	return false
}

func (s *Source) mapIndexes() {
	s.indexes = map[string]string{}
	if s.gitDir == "" {
		return
	}
	s.indexes[filepath.Join(s.gitDir, "index")] = s.main
	for _, r := range s.roots {
		data, err := os.ReadFile(filepath.Join(r, ".git"))
		gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
		if err != nil || !ok {
			continue
		}
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(r, gitdir)
		}
		s.indexes[filepath.Join(gitdir, "index")] = r
	}
}
