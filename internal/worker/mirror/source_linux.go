package mirror

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	healthy  atomic.Bool

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

// Healthy reports whether the source runs and the destination has applied
// its first barrier.
func (s *Source) Healthy() bool {
	select {
	case <-s.done:
		return false
	default:
		return s.healthy.Load()
	}
}

// Barrier returns once the destination has durably applied every change made
// before the call, with the barrier's sequence number and the files left out
// because they are still being written.
func (s *Source) Barrier(ctx context.Context) (uint64, []string, error) {
	req := barrierRequest{done: make(chan barrierResult, 1)}
	select {
	case s.requests <- req:
	case <-s.done:
		return 0, nil, errStopped
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
			return 0, nil, errStopped
		}
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

// Run brings the destination at the other end of stream up to date, then
// mirrors changes until ctx ends or the stream fails. It closes stream, if it
// can, before returning. A Source runs only once.
func (s *Source) Run(ctx context.Context, stream io.ReadWriter) error {
	defer close(s.done)
	defer closeOnDone(ctx, stream)()
	s.sender = newSender(s.unit, s.opts, stream)
	defer s.sender.close()
	s.sender.watch = s.addWatch
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
// barrier later.
func (s *Source) runBarrier(reqs []barrierRequest) error {
	if err := s.syncSentinel(); err != nil {
		return err
	}
	if err := s.checkSkipped(); err != nil {
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
		return false, nil // gone meanwhile
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
	if s.wtChanged {
		if err := s.refreshWorktrees(); err != nil {
			return err
		}
	}
	if s.ignChanged {
		s.ignChanged = false
		for _, c := range s.cls {
			c.start()
		}
		s.needRescan = true
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
			s.mark(dir, false)
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
		writing := e.mask&unix.IN_MODIFY != 0
		if e.mask&unix.IN_ATTRIB != 0 {
			writing = s.dirty[p] // a change of mode or time doesn't end a write
		}
		s.mark(p, writing)
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
		s.mark(p, false)
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
	s.mark(p, false)
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
			s.mark(from, false)
		}
		s.mark(to, false) // only checked if it was moved
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
	dirty := map[string]bool{}
	for p, writing := range s.dirty {
		np, _ := rewrite(p, from, to)
		dirty[np] = writing
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
			s.mark(r, false)
		}
	}
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
