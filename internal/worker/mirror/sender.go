package mirror

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A file larger than this that is still being written at a barrier is
// reported instead of sent.
const largeFile = 8 << 20

// sigBatch bounds the signature requests waiting for an answer.
const sigBatch = 32

// sender sends the live copy's changes. It knows what the destination has
// (sent) and which paths changed since (dirty).
type sender struct {
	opts     Options
	main     string
	gitDir   string
	within   []string // Unit.Within
	file     string   // Unit.File
	roots    []string
	cls      map[string]*classifier // per worktree
	skipped  map[string]bool        // excluded worktree files and folders found by walks
	followed map[string]bool        // links that Dereference follows
	c        *conn

	sent       map[string]entry
	dirty      map[string]writes
	firstDirty time.Time
	marked     uint64            // changes seen so far
	objDels    map[string]uint64 // object files gone before the barrier with this number
	next       uint64            // the next barrier's number
	writing    []string          // files left out of the current barrier
	settling   map[string]stamp  // CompleteLines files that the barrier found closed, as they were; nil for a one-shot sync
	unread     map[string]error  // what couldn't be read since the barrier started, which fails it
	buf        []byte

	// watch watches a folder and reports whether it wasn't watched yet; nil
	// for a one-shot sync.
	watch func(dir string) (bool, error)

	sigs    chan *frame
	hashes  chan *frame
	stopped chan struct{} // closed when the destination's answers stop
	mu      sync.Mutex
	waiters map[uint64][]chan error
}

// writes is what the source knows of whether a changed file is still being written.
type writes uint8

const (
	writesUnknown writes = iota // as after a scan, which doesn't show whether its writer closed it
	writesClosed                // its writer closed it after the last change
	writesOpen                  // still being written
)

func newSender(u Unit, opts Options, rw io.ReadWriter) *sender {
	s := &sender{
		opts: opts, main: u.Main, gitDir: u.GitDir, within: u.Within, file: u.File, cls: map[string]*classifier{}, skipped: map[string]bool{}, followed: map[string]bool{},
		sent: map[string]entry{}, dirty: map[string]writes{}, objDels: map[string]uint64{}, unread: map[string]error{},
		sigs: make(chan *frame, sigBatch), hashes: make(chan *frame, 1), stopped: make(chan struct{}), waiters: map[uint64][]chan error{},
	}
	if rw != nil {
		s.c = newConn(rw)
	}
	for _, r := range u.roots() {
		s.addRoot(r)
	}
	return s
}

func (s *sender) close() {
	for _, c := range s.cls {
		c.stop()
	}
}

func (s *sender) addRoot(r string) {
	s.roots = append(s.roots, r)
	if s.gitDir != "" && !within(r, s.gitDir) {
		s.cls[r] = newClassifier(r)
	}
}

func (s *sender) removeRoot(r string) {
	s.roots = slices.DeleteFunc(s.roots, func(x string) bool { return x == r })
	if c := s.cls[r]; c != nil {
		c.stop()
		delete(s.cls, r)
	}
}

func (s *sender) isRoot(p string) bool { return slices.Contains(s.roots, p) }

func (s *sender) rootOf(p string) string {
	best := ""
	for _, r := range s.roots {
		if within(p, r) && len(r) > len(best) {
			best = r
		}
	}
	return best
}

func (s *sender) inGit(p string) bool { return s.gitDir != "" && within(p, s.gitDir) }

func (s *sender) inObjects(p string) bool {
	return s.gitDir != "" && within(p, filepath.Join(s.gitDir, "objects"))
}

// same reports whether the destination's entry old matches e. Git names loose
// objects and packs after their contents, and refreshes the time of existing
// ones, so their size is enough.
func (s *sender) same(old, e entry) bool {
	if old.Type == 'f' && e.Type == 'f' && s.inObjects(e.Path) && immutableObject(e.Path) {
		return old.Size == e.Size && old.Mode == e.Mode
	}
	return old.Type == e.Type && old.Mode == e.Mode && old.Size == e.Size && old.Mtime == e.Mtime && old.Target == e.Target
}

func immutableObject(p string) bool {
	dir := filepath.Base(filepath.Dir(p))
	if dir == "pack" {
		return strings.HasPrefix(filepath.Base(p), "pack-") && slices.Contains([]string{".pack", ".idx", ".rev"}, filepath.Ext(p))
	}
	_, err := hex.DecodeString(dir)
	return len(dir) == 2 && err == nil
}

func (s *sender) isMeta(p string) bool { return s.inGit(p) && !s.inObjects(p) }

func (s *sender) inWorktree(p string) bool { return !s.inGit(p) }

// excluded reports whether p is left out of the mirror by itself; see
// excludedAbove for the folders above it.
func (s *sender) excluded(p string, isDir bool, size int64) bool {
	if s.file != "" {
		return p != s.file && p != s.main
	}
	if s.inGit(p) {
		return gitTemporary(filepath.Base(p))
	}
	r := s.rootOf(p)
	if r == "" {
		return true
	}
	if p == r {
		return false
	}
	rel := p[len(r)+1:]
	if matchAny(s.opts.Exclude, rel) {
		return true
	}
	c := s.cls[r]
	if c == nil || matchAny(s.opts.Include, rel) {
		return false
	}
	if isDir {
		return c.ignored(rel+"/") && !c.hasTracked(rel) && !includesBelow(s.opts.Include, rel)
	}
	if !c.ignored(rel) {
		return false
	}
	// In an ignored folder entered only for Include, the rest stays out.
	dir := path.Dir(rel)
	inIgnored := dir != "." && c.ignored(dir+"/") && !c.hasTracked(dir)
	return (size > maxIgnoredSize || inIgnored) && !c.hasTracked(rel)
}

func (s *sender) excludedAbove(p string) bool {
	r := s.rootOf(p)
	for d := filepath.Dir(p); r != "" && len(d) > len(r); d = filepath.Dir(d) {
		if s.excluded(d, true, 0) {
			return true
		}
	}
	return false
}

// kept reports whether the destination keeps its own version of p, which the
// source doesn't mirror: Git's temporary files, or ignored files and folders
// that stay on each machine.
func (s *sender) kept(p string, e entry) bool {
	if s.rootOf(p) == "" {
		return false // a worktree the source no longer has
	}
	return s.excluded(p, e.Type == 'd', e.Size) || s.excludedAbove(p)
}

// skip remembers an excluded worktree path, which Git may track later.
func (s *sender) skip(p string) {
	if !s.inGit(p) && s.file == "" {
		s.skipped[p] = true
	}
}

func (s *sender) mark(p string, w writes) {
	if s.inGit(p) && gitTemporary(filepath.Base(p)) || s.file != "" && p != s.file {
		return
	}
	if len(s.dirty) == 0 {
		s.firstDirty = time.Now()
	}
	s.dirty[p] = w
	s.marked++
	delete(s.settling, p)
}

// cantRead notes that p couldn't be read. Unless it disappeared or changed type meanwhile, which later events or the
// next look at it tell, the barrier fails, rather than the destination losing p or keeping an old version of it.
func (s *sender) cantRead(p string, err error) {
	if !vanished(err) {
		s.unread[p] = err
	}
}

// vanished reports whether err says that a path disappeared or changed type while it was read. ELOOP and ENXIO are
// what opening a symlink with O_NOFOLLOW and a socket return.
func vanished(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENXIO)
}

// retryUnread looks again at what couldn't be read, so that a barrier fails only on what it still can't read.
func (s *sender) retryUnread() error {
	unread := s.unread
	s.unread = map[string]error{}
	for p := range unread {
		s.mark(p, s.dirty[p])
		if info, err := s.lstat(p); err == nil && info.IsDir() && !s.excluded(p, true, 0) && !s.excludedAbove(p) {
			if err := s.walk(p, nil); err != nil { // a folder that couldn't be read or watched
				return err
			}
		}
	}
	return nil
}

// unreadError describes what couldn't be read, if anything.
func (s *sender) unreadError() error {
	if len(s.unread) == 0 {
		return nil
	}
	p := slices.Min(slices.Collect(maps.Keys(s.unread)))
	err := s.unread[p]
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	more := ""
	if len(s.unread) > 1 {
		more = fmt.Sprintf(" and %d more", len(s.unread)-1)
	}
	return fmt.Errorf("mirror: can't read %s%s: %w", p, more, err)
}

func entryOf(p string, info fs.FileInfo) entry {
	e := entry{Path: p}
	switch {
	case info.IsDir():
		e.Type, e.Mode = 'd', uint32(info.Mode().Perm()|0o700) // folders stay writable
	case info.Mode()&fs.ModeSymlink != 0:
		e.Type = 'l'
	default:
		e.Type, e.Mode, e.Size, e.Mtime = 'f', uint32(info.Mode().Perm()), info.Size(), info.ModTime().UnixNano()
	}
	return e
}

func inode(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// walk watches the folders under dir and marks what differs from the
// destination; seen, if set, collects every mirrored path.
func (s *sender) walk(dir string, seen map[string]bool) error {
	visit := func(p string, d fs.DirEntry, err error) error {
		if s.file != "" && p != dir && p != s.file {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err != nil {
			s.cantRead(p, err)
			return nil
		}
		if d.IsDir() && p != dir {
			if s.isRoot(p) {
				return filepath.SkipDir
			}
			if s.excluded(p, true, 0) {
				s.skip(p)
				return filepath.SkipDir
			}
		}
		if d.IsDir() && s.watch != nil {
			if _, err := s.watch(p); err != nil {
				return err
			}
		}
		info, err := d.Info()
		if err != nil {
			s.cantRead(p, err)
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0 {
			return nil // sockets and the like
		}
		if !info.IsDir() && s.excluded(p, false, info.Size()) {
			s.skip(p)
			return nil
		}
		if seen != nil {
			seen[p] = true
		}
		if old, ok := s.sent[p]; !ok || !s.same(old, entryOf(p, info)) || old.Type == 'l' {
			s.mark(p, writesUnknown)
		}
		return nil
	}
	if s.opts.Dereference {
		return fs.WalkDir(dereferencing{s}, dir[1:], func(p string, d fs.DirEntry, err error) error { return visit("/"+p, d, err) })
	}
	return filepath.WalkDir(dir, visit)
}

// dereferencing is the file system as Dereference sees it, where a link that it follows is what it points to, so
// that fs.WalkDir enters links to folders.
type dereferencing struct{ s *sender }

func (fsys dereferencing) Open(name string) (fs.File, error) { return os.Open("/" + name) }

func (fsys dereferencing) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := os.ReadDir("/" + name)
	for i, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 {
			if info := fsys.s.follow(filepath.Join("/"+name, entry.Name())); info != nil {
				entries[i] = fs.FileInfoToDirEntry(info)
			}
		}
	}
	return entries, err
}

// follow returns what the link p points to if Dereference follows it, and nil if it stays a link.
func (s *sender) follow(p string) fs.FileInfo {
	info, err := os.Stat(p)
	if err != nil {
		return nil
	}
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil
	}
	belowFollowed := false
	for dir := filepath.Dir(p); s.rootOf(dir) != ""; dir = filepath.Dir(dir) {
		belowFollowed = belowFollowed || s.followed[dir]
		if real, err := filepath.EvalSymlinks(dir); info.IsDir() && err == nil && within(real, target) {
			return nil // a loop
		}
	}
	inside := slices.ContainsFunc(s.roots, func(root string) bool {
		real, err := filepath.EvalSymlinks(root)
		return err == nil && within(target, real)
	})
	if inside && !belowFollowed {
		return nil
	}
	s.followed[p] = true
	return info
}

// lstat is os.Lstat, except that it follows the links that Dereference follows.
func (s *sender) lstat(p string) (fs.FileInfo, error) {
	if s.followed[p] {
		return os.Stat(p)
	}
	return os.Lstat(p)
}

// scan walks the whole unit, marking what differs from the destination, and
// returns the mirrored paths. It fails, and tells the destination, when one of
// the unit's folders isn't a folder here (see checkRoots).
func (s *sender) scan() (map[string]bool, error) {
	if err := s.checkRoots(); err != nil {
		return nil, s.fail(err)
	}
	seen := map[string]bool{}
	s.skipped = map[string]bool{}
	for _, r := range s.roots {
		if err := s.walk(r, seen); err != nil {
			return nil, err
		}
	}
	return seen, nil
}

// checkRoots fails unless each of the unit's folders is a folder here, so that
// a scan never takes a folder that this process doesn't see for an empty one
// and deletes the destination's copy of it: a process in a private view that
// started before the folder was made doesn't see it, for one. A worktree that
// Git no longer has, such as after git worktree remove, is no longer the
// unit's, and goes on the destination too.
func (s *sender) checkRoots() error {
	var listed []string
	for _, r := range slices.Clone(s.roots) {
		if info, err := os.Lstat(r); err == nil && info.IsDir() {
			continue
		}
		// The main folder of a unit of selected folders may be a linked worktree too.
		if s.gitDir != "" && (r != s.main || s.within != nil) && r != s.gitDir {
			if listed == nil {
				var err error
				if listed, err = gitWorktrees(s.gitFolder()); err != nil {
					return err
				}
			}
			if !slices.Contains(listed, r) {
				s.removeRoot(r)
				continue
			}
		}
		return fmt.Errorf("mirror: %s is not a folder here, so the mirror stops rather than delete the other side's copy of it", r)
	}
	return nil
}

// gitFolder is where Git lists the unit's worktrees: the main folder, unless the session removed that linked worktree.
func (s *sender) gitFolder() string {
	if _, err := os.Stat(s.main); err != nil && s.within != nil {
		return s.gitDir
	}
	return s.main
}

// joinWorktrees adds the existing worktrees that Git lists and that join a unit of selected folders (see Unit.Within),
// and returns every worktree that Git lists.
func (s *sender) joinWorktrees() ([]string, error) {
	listed, err := gitWorktrees(s.gitFolder())
	if err != nil {
		return nil, err
	}
	for i, worktree := range listed {
		if _, err := os.Stat(worktree); err == nil && !s.isRoot(worktree) && joins(s.within, worktree, i == 0) {
			s.addRoot(worktree)
		}
	}
	return listed, nil
}

// fail tells the destination why the source stops, and returns err.
func (s *sender) fail(err error) error {
	if s.c != nil && s.c.send(&frame{Kind: kindFail, Error: err.Error()}) == nil {
		s.c.flush()
	}
	return err
}

// markMissing marks what the destination has and a scan didn't see.
func (s *sender) markMissing(seen map[string]bool) {
	for p, e := range s.sent {
		if !seen[p] && !s.kept(p, e) {
			s.mark(p, writesUnknown)
		}
	}
}

// handshake tells the destination the unit's folders and reads what it has.
func (s *sender) handshake() error {
	skip := make([]string, 0, len(s.skipped))
	for p := range s.skipped {
		skip = append(skip, p)
	}
	if err := s.c.send(&frame{Kind: kindHello, Version: version, Roots: s.roots, Skip: skip}); err != nil {
		return err
	}
	if err := s.c.flush(); err != nil {
		return err
	}
	f, err := s.c.receive()
	if err != nil {
		return fmt.Errorf("mirror handshake: %w", err)
	}
	switch f.Kind {
	case kindManifest:
	case kindFail:
		return fmt.Errorf("mirror destination: %s", f.Error)
	default:
		return fmt.Errorf("mirror handshake: unexpected frame %d", f.Kind)
	}
	for _, e := range f.Entries {
		s.sent[e.Path] = e
	}
	s.next = f.N + 1
	return nil
}

// startReceiving handles the destination's answers in the background; the
// channel gets the reason they stopped.
func (s *sender) startReceiving() <-chan error {
	errc := make(chan error, 1)
	go func() {
		err := s.receive()
		s.failWaiters(err)
		close(s.stopped)
		errc <- err
	}()
	return errc
}

func (s *sender) receive() error {
	for {
		f, err := s.c.receive()
		if err != nil {
			return fmt.Errorf("mirror stream: %w", err)
		}
		switch f.Kind {
		case kindSigs:
			s.sigs <- f
		case kindSum:
			s.hashes <- f
		case kindAck:
			var err error
			if f.Error != "" {
				err = fmt.Errorf("mirror destination: %s", f.Error)
			}
			s.acknowledge(f.N, err)
		case kindFail:
			return fmt.Errorf("mirror destination: %s", f.Error)
		}
	}
}

func (s *sender) expect(n uint64) chan error {
	ch := make(chan error, 1)
	s.mu.Lock()
	s.waiters[n] = append(s.waiters[n], ch)
	s.mu.Unlock()
	return ch
}

func (s *sender) acknowledge(n uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for m, chs := range s.waiters {
		if m <= n {
			for _, ch := range chs {
				ch <- err
			}
			delete(s.waiters, m)
		}
	}
}

func (s *sender) failWaiters(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for m, chs := range s.waiters {
		for _, ch := range chs {
			ch <- err
		}
		delete(s.waiters, m)
	}
}

func (s *sender) sentUnder(p string) bool {
	if _, ok := s.sent[p]; ok {
		return true
	}
	for q := range s.sent {
		if strings.HasPrefix(q, p+"/") {
			return true
		}
	}
	return false
}

func (s *sender) forgetUnder(p string) {
	if e, ok := s.sent[p]; ok && e.Type != 'd' {
		delete(s.sent, p)
		return
	}
	for q := range s.sent {
		if within(q, p) {
			delete(s.sent, q)
		}
	}
}

// flush sends the dirty paths that sel selects. Between barriers, files still
// being written wait; at a barrier they are sent unless large.
func (s *sender) flush(barrier bool, sel func(string) bool) error {
	var dirs, files, links, gone []string
	for p, w := range s.dirty {
		if sel != nil && !sel(p) {
			continue
		}
		info, err := s.lstat(p)
		if err != nil {
			if !vanished(err) {
				s.cantRead(p, err) // and it stays dirty
				continue
			}
			delete(s.dirty, p)
			if s.file != "" {
				// Pi hasn't saved the session yet, or deleted it, which happens on main only.
				delete(s.sent, p)
				continue
			}
			old, had := s.sent[p]
			if (!had && !s.sentUnder(p)) || s.kept(p, old) {
				continue
			}
			if s.inObjects(p) {
				if _, ok := s.objDels[p]; !ok {
					s.objDels[p] = s.next
				}
			} else {
				gone = append(gone, p)
			}
			continue
		}
		e := entryOf(p, info)
		switch e.Type {
		case 'd':
			delete(s.dirty, p)
			if s.excluded(p, true, 0) {
				s.skip(p)
				continue
			}
			if s.excludedAbove(p) {
				continue
			}
			if old, ok := s.sent[p]; !ok || !s.same(old, e) {
				dirs = append(dirs, p)
			}
		case 'l':
			delete(s.dirty, p)
			if s.excludedAbove(p) {
				continue
			}
			if s.excluded(p, false, 0) {
				s.skip(p)
				continue
			}
			links = append(links, p)
		default:
			if !info.Mode().IsRegular() || s.excludedAbove(p) {
				delete(s.dirty, p)
				continue
			}
			if s.excluded(p, false, e.Size) {
				delete(s.dirty, p)
				s.skip(p)
				continue
			}
			if old, ok := s.sent[p]; ok && s.same(old, e) {
				delete(s.dirty, p)
				continue
			}
			if s.settling != nil && s.lineFile(p) {
				// What is read of a rewrite in progress may be only its start, so these go at barriers only: a closed
				// one once the source has made sure that it stayed so, and the rest as held decides.
				if !barrier {
					continue
				}
				if w == writesClosed {
					s.settling[p] = stampOf(p, info)
					continue
				}
			}
			if w == writesOpen {
				if !barrier {
					continue
				}
				if e.Size > largeFile && time.Since(info.ModTime()) < time.Second {
					s.writing = append(s.writing, p)
					continue
				}
			}
			files = append(files, p) // and it stays dirty until it is sent
		}
	}

	// Deletions first: what is gone may be under a folder that is a file now.
	slices.Sort(gone)
	last := ""
	for _, p := range gone {
		if last != "" && within(p, last) {
			continue
		}
		last = p
		if err := s.c.send(&frame{Kind: kindDelete, Path: p}); err != nil {
			return err
		}
		s.forgetUnder(p)
	}
	slices.Sort(dirs)
	for _, p := range dirs {
		info, err := s.lstat(p)
		if err != nil {
			s.cantRead(p, err)
			continue
		}
		if s.watch != nil {
			added, err := s.watch(p)
			if err == nil && added {
				err = s.walk(p, nil) // its creation was missed, and so was what it holds
			}
			if err != nil {
				return err
			}
		}
		e := entryOf(p, info)
		if err := s.c.send(&frame{Kind: kindMkdir, Path: p, Mode: e.Mode}); err != nil {
			return err
		}
		s.sent[p] = e
	}
	if err := s.sendFiles(files); err != nil {
		return err
	}
	for _, p := range links {
		target, err := os.Readlink(p)
		if err != nil {
			continue
		}
		if old, ok := s.sent[p]; ok && old.Type == 'l' && old.Target == target {
			continue
		}
		if err := s.c.send(&frame{Kind: kindSymlink, Path: p, Target: target}); err != nil {
			return err
		}
		s.sent[p] = entry{Path: p, Type: 'l', Target: target}
	}
	return s.c.flush()
}

// priority orders files within a flush: objects, then the rest of .git, then worktrees.
func (s *sender) priority(p string) int {
	switch {
	case s.inObjects(p):
		return 0
	case s.inGit(p):
		return 1
	}
	return 2
}

type deltaPlan struct {
	bases    []string
	appendAt int64 // the destination has the file up to here; nothing before changed
	sigs     *frame
}

func (p deltaPlan) needsSigs() bool { return len(p.bases) > 0 && p.appendAt == 0 }

// plan chooses how to send p: whole, appended to (logs), or as a delta
// against files the destination has (the old version, or other packs).
func (s *sender) plan(p string) deltaPlan {
	info, err := s.lstat(p)
	if err != nil {
		return deltaPlan{}
	}
	old, ok := s.sent[p]
	if ok && old.Type == 'f' && old.ino != 0 && old.ino == inode(info) && info.Size() > old.Size && s.inLogs(p) {
		return deltaPlan{bases: []string{p}, appendAt: old.Size}
	}
	if info.Size() < deltaMinSize {
		return deltaPlan{}
	}
	if ok && old.Type == 'f' && old.Size > 0 {
		return deltaPlan{bases: []string{p}}
	}
	dir := filepath.Dir(p)
	if !s.inGit(p) || dir != filepath.Join(s.gitDir, "objects", "pack") {
		return deltaPlan{}
	}
	var bases []string
	for q, e := range s.sent {
		if e.Type == 'f' && filepath.Dir(q) == dir && filepath.Ext(q) == filepath.Ext(p) {
			bases = append(bases, q)
		}
	}
	slices.SortFunc(bases, func(a, b string) int { return cmp.Compare(s.sent[b].Size, s.sent[a].Size) })
	return deltaPlan{bases: bases[:min(len(bases), 8)]}
}

func (s *sender) inLogs(p string) bool {
	rel := strings.TrimPrefix(p, s.gitDir+"/")
	return s.inGit(p) && (strings.HasPrefix(rel, "logs/") || strings.Contains(rel, "/logs/"))
}

// sendFiles sends files in priority order. Deltas need the destination's
// signatures first; those are asked for in batches while other files go out.
func (s *sender) sendFiles(files []string) error {
	slices.SortFunc(files, func(a, b string) int {
		return cmp.Or(cmp.Compare(s.priority(a), s.priority(b)), strings.Compare(a, b))
	})
	plans := make(map[string]deltaPlan, len(files))
	var deltas []string
	for _, p := range files {
		plan := s.plan(p)
		plans[p] = plan
		if plan.needsSigs() {
			deltas = append(deltas, p)
		}
	}
	sendOthers := func() error {
		for _, p := range files {
			if plan, ok := plans[p]; ok && !plan.needsSigs() {
				if err := s.sendFile(p, plan); err != nil {
					return err
				}
				delete(plans, p)
			}
		}
		return nil
	}
	for len(deltas) > 0 {
		batch := deltas[:min(len(deltas), sigBatch)]
		deltas = deltas[len(batch):]
		for _, p := range batch {
			if err := s.c.send(&frame{Kind: kindSigRequest, Path: p, Bases: plans[p].bases}); err != nil {
				return err
			}
		}
		if err := s.c.flush(); err != nil {
			return err
		}
		if err := sendOthers(); err != nil { // they travel while the destination computes
			return err
		}
		for _, p := range batch {
			var f *frame
			select {
			case f = <-s.sigs:
			case <-s.stopped:
				return errors.New("mirror stream closed")
			}
			if f.Path != p {
				return fmt.Errorf("mirror: signatures for %s arrived instead of %s", f.Path, p)
			}
			plan := plans[p]
			plan.sigs = f
			if err := s.sendFile(p, plan); err != nil {
				return err
			}
		}
	}
	return sendOthers()
}

// sendFile sends one moment of p: if the file changes while it is read, the
// put is aborted and retried, and finally left for later.
func (s *sender) sendFile(p string, plan deltaPlan) error {
	// O_NONBLOCK keeps a FIFO that replaced the file from blocking the open.
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if s.followed[p] {
		flags &^= syscall.O_NOFOLLOW
	}
	for attempt := 0; attempt < 3; attempt++ {
		f, err := os.OpenFile(p, flags, 0)
		var info fs.FileInfo
		if err == nil {
			info, err = f.Stat()
		}
		if err != nil || !info.Mode().IsRegular() {
			if f != nil {
				f.Close()
			}
			if err != nil {
				s.cantRead(p, err)
			}
			s.mark(p, s.dirty[p]) // the next flush sees what it is
			return nil
		}
		if s.settling != nil && s.lineFile(p) {
			if held, err := s.held(f, info, p); held || err != nil {
				f.Close()
				if _, settling := s.settling[p]; !settling { // or sendSettled lists it if it must wait
					s.writing = append(s.writing, p)
				}
				return err
			}
		}
		ok, e, err := s.stream(f, info, p, plan)
		f.Close()
		if err != nil || ok {
			if ok {
				s.sent[p] = e
				delete(s.unread, p)
				delete(s.dirty, p)
			}
			return err
		}
		if e.Size > largeFile {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	s.writing = append(s.writing, p)
	s.mark(p, writesOpen)
	return nil
}

func (s *sender) lineFile(p string) bool {
	return s.opts.CompleteLines && strings.HasSuffix(p, ".jsonl")
}

// held reports whether the CompleteLines file p, open as f, isn't sent now. A file that only gained lines since the
// destination got it goes now, and any other only once its writer has closed it: one that the barrier found closed
// if it is still as it was then. One whose writes the source didn't see, as after a scan, is taken for closed when
// no process has it open for writing, and settles like one found closed until the end of the barrier.
func (s *sender) held(f *os.File, info fs.FileInfo, p string) (bool, error) {
	if st, ok := s.settling[p]; ok {
		return st != stampOf(p, info), nil
	}
	old, ok := s.sent[p]
	if !ok || old.Type != 'f' {
		return false, nil
	}
	if old.linesSum == nil && info.Size() >= old.Size { // the destination had it before this source ran
		main, err := s.destinationHash(p)
		if err != nil {
			return true, err
		}
		old.lines, old.linesSum = main.Size, main.Hash
		s.sent[p] = old
	}
	if old.linesSum != nil {
		sum := sha256.New()
		if _, err := io.Copy(sum, io.NewSectionReader(f, 0, old.lines)); err != nil {
			s.cantRead(p, err)
			return true, nil
		}
		if bytes.Equal(sum.Sum(nil), old.linesSum) {
			return false, nil
		}
	}
	if s.dirty[p] == writesUnknown && !openForWriting(info) {
		s.settling[p] = stampOf(p, info)
	}
	return true, nil
}

// destinationHash asks the destination for the size and SHA-256 of what it has at p; Hash is nil if it has no file
// there.
func (s *sender) destinationHash(p string) (*frame, error) {
	if err := s.c.send(&frame{Kind: kindSumRequest, Path: p}); err != nil {
		return nil, err
	}
	if err := s.c.flush(); err != nil {
		return nil, err
	}
	select {
	case f := <-s.hashes:
		if f.Path != p {
			return nil, fmt.Errorf("mirror: the hash of %s arrived instead of %s", f.Path, p)
		}
		return f, nil
	case <-s.stopped:
		return nil, errors.New("mirror stream closed")
	}
}

// stream sends p as f holds it, or with CompleteLines, its complete lines. The entry it returns is the whole file's,
// so that the rest goes once the file changes again.
func (s *sender) stream(f *os.File, info fs.FileInfo, p string, plan deltaPlan) (bool, entry, error) {
	e := entryOf(p, info)
	e.ino = inode(info)
	size := e.Size
	lines := s.lineFile(p)
	if lines {
		var err error
		if size, err = completeLines(f, size); err != nil {
			s.cantRead(p, err)
			return false, e, nil
		}
	}
	w := &putWriter{c: s.c, f: &frame{Kind: kindPut, Path: p, Mode: e.Mode, Mtime: e.Mtime, Size: size, Bases: plan.bases, First: true}}
	counted := &countingReader{r: io.NewSectionReader(f, 0, size)}
	var r io.Reader = counted
	// Of what is read from the start: what the destination builds from a delta, or the lines that held checks later.
	var sum hash.Hash
	if plan.sigs != nil || lines && plan.appendAt == 0 {
		sum = sha256.New()
		r = io.TeeReader(r, sum)
	}
	var err error
	switch {
	case plan.appendAt > 0:
		counted.r, counted.n = io.NewSectionReader(f, plan.appendAt, size-plan.appendAt), plan.appendAt
		err = w.add(deltaOp{Base: 0, Off: 0, Len: plan.appendAt})
		if err == nil {
			err = s.copyData(w, r)
		}
	case plan.sigs != nil:
		err = diff(r, plan.sigs.BlockSize, plan.sigs.Sums, w.add)
	default:
		err = s.copyData(w, r)
	}
	var readErr *readError
	if errors.As(err, &readErr) {
		s.cantRead(p, readErr.error)
		err = nil
		counted.n = -1 // abort
	}
	if err != nil {
		return false, e, err
	}
	after, statErr := f.Stat()
	ok := statErr == nil && counted.n == size && after.Size() == e.Size && after.ModTime().Equal(info.ModTime())
	var hashed []byte
	if sum != nil && ok {
		hashed = sum.Sum(nil)
	}
	if lines {
		e.lines, e.linesSum = size, hashed
	}
	if plan.sigs == nil {
		hashed = nil // the destination checks only what it builds from a delta
	}
	return ok, e, w.finish(!ok, hashed)
}

func (s *sender) copyData(w *putWriter, r io.Reader) error {
	if s.buf == nil {
		s.buf = make([]byte, chunkSize)
	}
	for {
		n, err := r.Read(s.buf)
		if n > 0 {
			if err := w.add(deltaOp{Data: s.buf[:n]}); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// completeLines returns the length of the first size bytes of f up to their last newline.
func completeLines(f *os.File, size int64) (int64, error) {
	buf := make([]byte, 64<<10)
	for end := size; end > 0; {
		start := max(end-int64(len(buf)), 0)
		// At its end, the file shrank since its size was taken, which the put notices afterwards.
		n, err := f.ReadAt(buf[:end-start], start)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return start + int64(i) + 1, nil
		}
		end = start
	}
	return 0, nil
}

// readError marks failures to read the source file, which abort the put
// rather than the stream.
type readError struct{ error }

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		err = &readError{err}
	}
	return n, err
}

// sendObjectDeletions sends deletions of object files that disappeared before
// the previous barrier: whatever replaced them, such as a new pack, has then
// surely reached the destination, and no ref read since can name them.
func (s *sender) sendObjectDeletions(n uint64) error {
	for p, at := range s.objDels {
		if at >= n {
			continue
		}
		delete(s.objDels, p)
		if _, err := os.Lstat(p); err == nil {
			continue // back again
		}
		if err := s.c.send(&frame{Kind: kindDelete, Path: p}); err != nil {
			return err
		}
		s.forgetUnder(p)
	}
	return nil
}

// sendBarrier ends the barrier with number n and returns a channel that gets
// the destination's answer.
func (s *sender) sendBarrier(n uint64) (chan error, error) {
	if err := s.sendObjectDeletions(n); err != nil {
		return nil, err
	}
	ack := s.expect(n)
	if err := s.c.send(&frame{Kind: kindBarrier, N: n}); err != nil {
		return nil, err
	}
	return ack, s.c.flush()
}
