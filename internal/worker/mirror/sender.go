package mirror

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
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
	roots    []string
	cls      map[string]*classifier // per worktree
	skipped  map[string]bool        // excluded worktree files and folders found by walks
	followed map[string]bool        // links that Dereference follows
	c        *conn

	sent       map[string]entry
	dirty      map[string]bool // path → still being written
	firstDirty time.Time
	objDels    map[string]uint64 // object files gone before the barrier with this number
	next       uint64            // the next barrier's number
	writing    []string          // files left out of the current barrier
	buf        []byte

	// watch watches a folder and reports whether it wasn't watched yet; nil
	// for a one-shot sync.
	watch func(dir string) (bool, error)

	sigs    chan *frame
	stopped chan struct{} // closed when the destination's answers stop
	mu      sync.Mutex
	waiters map[uint64][]chan error
}

func newSender(u Unit, opts Options, rw io.ReadWriter) *sender {
	s := &sender{
		opts: opts, main: u.Main, gitDir: u.GitDir, cls: map[string]*classifier{}, skipped: map[string]bool{}, followed: map[string]bool{},
		sent: map[string]entry{}, dirty: map[string]bool{}, objDels: map[string]uint64{},
		sigs: make(chan *frame, sigBatch), stopped: make(chan struct{}), waiters: map[uint64][]chan error{},
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
	if !s.inGit(p) {
		s.skipped[p] = true
	}
}

func (s *sender) mark(p string, writing bool) {
	if s.inGit(p) && gitTemporary(filepath.Base(p)) {
		return
	}
	if len(s.dirty) == 0 {
		s.firstDirty = time.Now()
	}
	s.dirty[p] = writing
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
		if err != nil {
			return nil // gone meanwhile
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
			s.mark(p, false)
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
// returns the mirrored paths.
func (s *sender) scan() (map[string]bool, error) {
	seen := map[string]bool{}
	s.skipped = map[string]bool{}
	for _, r := range s.roots {
		if err := s.walk(r, seen); err != nil {
			return nil, err
		}
	}
	return seen, nil
}

// markMissing marks what the destination has and a scan didn't see.
func (s *sender) markMissing(seen map[string]bool) {
	for p, e := range s.sent {
		if !seen[p] && !s.kept(p, e) {
			s.mark(p, false)
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
	for p, writing := range s.dirty {
		if sel != nil && !sel(p) {
			continue
		}
		info, err := s.lstat(p)
		if err != nil {
			delete(s.dirty, p)
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
			if writing {
				if !barrier {
					continue
				}
				if e.Size > largeFile && time.Since(info.ModTime()) < time.Second {
					s.writing = append(s.writing, p)
					continue
				}
			}
			delete(s.dirty, p)
			files = append(files, p)
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
			s.mark(p, false) // gone, or no longer a file: the next flush sees what it is
			return nil
		}
		ok, e, err := s.stream(f, info, p, plan)
		f.Close()
		if err != nil || ok {
			if ok {
				s.sent[p] = e
			}
			return err
		}
		if e.Size > largeFile {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	s.writing = append(s.writing, p)
	s.mark(p, true)
	return nil
}

func (s *sender) stream(f *os.File, info fs.FileInfo, p string, plan deltaPlan) (bool, entry, error) {
	e := entryOf(p, info)
	e.ino = inode(info)
	w := &putWriter{c: s.c, f: &frame{Kind: kindPut, Path: p, Mode: e.Mode, Mtime: e.Mtime, Size: e.Size, Bases: plan.bases, First: true}}
	counted := &countingReader{r: f}
	var r io.Reader = counted
	var sum hash.Hash
	var err error
	switch {
	case plan.appendAt > 0:
		if _, err := f.Seek(plan.appendAt, io.SeekStart); err != nil {
			return false, e, nil
		}
		counted.n = plan.appendAt
		err = w.add(deltaOp{Base: 0, Off: 0, Len: plan.appendAt})
		if err == nil {
			err = s.copyData(w, r)
		}
	case plan.sigs != nil:
		sum = sha256.New()
		r = io.TeeReader(r, sum)
		err = diff(r, plan.sigs.BlockSize, plan.sigs.Sums, w.add)
	default:
		err = s.copyData(w, r)
	}
	var readErr *readError
	if errors.As(err, &readErr) {
		err = nil
		counted.n = -1 // abort
	}
	if err != nil {
		return false, e, err
	}
	after, statErr := f.Stat()
	ok := statErr == nil && counted.n == e.Size && after.Size() == e.Size && after.ModTime().Equal(info.ModTime())
	var hashed []byte
	if sum != nil && ok {
		hashed = sum.Sum(nil)
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
