package mirror

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Destination keeps a copy of a unit. It stages what a source sends and
// applies it only at barriers, so the copy always matches the last barrier.
//
// Nobody else should change the copy. A file that changed here anyway, since
// the destination last wrote it or since the destination was created, is
// moved aside before a barrier replaces or deletes it. Changes to Git's index
// alone don't count, because a plain git status rewrites it.
type Destination struct {
	// Applied, if set before Serve, is called after each barrier that Serve
	// applies, before the source learns of it, with the new checkpoint and the
	// files that changed here, which are in the folder aside at their paths.
	// A barrier that was finished after a crash, as the destination opened,
	// is reported when Serve starts.
	Applied func(checkpoint Checkpoint, aside string, saved []string)

	state     string
	recovered replayed // what finishing an interrupted barrier saved aside, which Serve reports
	// The source's paths under from are kept under to: both are "/" unless the unit lives elsewhere here, as in
	// tests that run both sides on one machine.
	from, to string
	mu       sync.Mutex
	cp       Checkpoint
	// Owned by the one replaying a journal.
	written map[string]stamp // how the destination left the files it wrote
	lines   int              // in the written file, which has a line for each change of a stamp
	since   int64            // files it never wrote changed here if their ctime is this or later
	all     bool             // every file changed here
}

// stamp is how a file looked once the destination had written it, kept in
// the state's written file; a stamp without an inode forgets the path.
type stamp struct {
	Path  string `json:"path"`
	Ino   uint64 `json:"ino,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Mtime int64  `json:"mtime,omitempty"`
	Ctime int64  `json:"ctime,omitempty"`
}

func stampOf(p string, info fs.FileInfo) stamp {
	st := info.Sys().(*syscall.Stat_t)
	return stamp{Path: p, Ino: st.Ino, Size: info.Size(), Mtime: info.ModTime().UnixNano(), Ctime: changeTime(st)}
}

// Checkpoint is the last barrier the destination applied: its copy has
// matched that barrier since.
type Checkpoint struct {
	Barrier uint64    `json:"barrier"`
	Time    time.Time `json:"time"`
	Roots   []string  `json:"roots"`
}

// op is one change waiting for a barrier, as kept in the journal and in
// changes saved aside.
type op struct {
	Kind   string `json:"kind"` // put, mkdir, symlink, delete or move; empty when superseded
	Path   string `json:"path"`
	To     string `json:"to,omitempty"`
	Target string `json:"target,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
	File   string `json:"file,omitempty"` // the staged contents of a put
}

type journal struct {
	Barrier uint64   `json:"barrier"`
	Roots   []string `json:"roots"`
	Ops     []op     `json:"ops"`
	Aside   string   `json:"aside,omitempty"` // where what changed here goes; empty when that isn't checked
}

// NewDestination opens the destination whose state lives in the folder
// state, which must be on the same filesystem as the unit. If applying a
// barrier was interrupted, it finishes first.
func NewDestination(state string) (*Destination, error) {
	return NewMovedDestination(state, "/", "/")
}

// NewMovedDestination is NewDestination for a unit that this machine keeps elsewhere than the source: what the
// source has under from is kept under to. Sources of units outside from are refused.
func NewMovedDestination(state, from, to string) (*Destination, error) {
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, err
	}
	d := &Destination{state: state, from: from, to: to}
	if err := d.readStamps(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(d.path("checkpoint.json"))
	if err == nil {
		err = json.Unmarshal(data, &d.cp)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("mirror checkpoint: %w", err)
	}
	if err := d.recover(); err != nil {
		return nil, err
	}
	return d, nil
}

// readStamps reads what the destination wrote, and when it was created, which it notes the first time.
func (d *Destination) readStamps() error {
	created, err := os.OpenFile(d.path("since"), os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	info, err := created.Stat()
	created.Close()
	if err != nil {
		return err
	}
	d.since, d.written = stampOf("", info).Ctime, map[string]stamp{}
	file, err := os.Open(d.path("written"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	for lines := bufio.NewScanner(file); lines.Scan(); d.lines++ {
		// A line cut short by a crash only costs its stamp: the file then looks changed here.
		var s stamp
		if json.Unmarshal(lines.Bytes(), &s) != nil {
			continue
		}
		if s.Ino == 0 {
			delete(d.written, s.Path)
		} else {
			d.written[s.Path] = s
		}
	}
	return nil
}

// writeStamps adds the stamps of the paths that a replay changed to the state's written file, or writes the file
// anew once most of its lines are out of date.
func (d *Destination) writeStamps(paths map[string]bool) error {
	if len(paths) == 0 {
		return nil
	}
	d.lines += len(paths)
	anew := d.lines > 2*len(d.written)+1000
	if anew {
		paths, d.lines = map[string]bool{}, len(d.written)
		for p := range d.written {
			paths[p] = true
		}
	}
	var data []byte
	for p := range paths {
		s, ok := d.written[p]
		if !ok {
			s = stamp{Path: p}
		}
		line, _ := json.Marshal(s)
		data = append(append(data, line...), '\n')
	}
	if anew {
		return writeFileAtomic(d.path("written"), data)
	}
	file, err := os.OpenFile(d.path("written"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	return errors.Join(err, file.Close())
}

// SaveAsideReplaced makes every file here count as changed here, so that Serve saves aside whatever it replaces or
// deletes, as for a copy that may have changes that the source never got.
func (d *Destination) SaveAsideReplaced() { d.all = true }

// Checkpoint returns the last applied barrier.
func (d *Destination) Checkpoint() Checkpoint {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cp
}

func (d *Destination) path(names ...string) string {
	return filepath.Join(append([]string{d.state}, names...)...)
}

func (d *Destination) local(p string) string {
	return filepath.Join(d.to, strings.TrimPrefix(p, d.from))
}

// Serve receives one source's changes until stream ends, acknowledging each
// barrier once it is applied durably. Changes received after the last barrier
// are then moved to the folder aside (on the same filesystem), with
// changes.json listing them, and Serve returns an error saying so. Only one
// Serve runs at a time.
func (d *Destination) Serve(ctx context.Context, stream io.ReadWriter, aside string) error {
	if err := d.recover(); err != nil {
		return err
	}
	if len(d.recovered.saved) > 0 && d.Applied != nil {
		d.Applied(d.Checkpoint(), d.recovered.aside, d.recovered.saved)
	}
	d.recovered = replayed{}
	if err := os.RemoveAll(d.path("staging")); err != nil {
		return err
	}
	if err := os.Mkdir(d.path("staging"), 0o700); err != nil {
		return err
	}
	defer closeOnDone(ctx, stream)()
	r := &receiver{d: d, c: newConn(stream), aside: aside, lastPut: map[string]int{}}
	err := r.run()
	if r.cur != nil { // incomplete
		r.cur.close()
		os.Remove(d.path("staging", r.cur.name))
	}
	if ops := r.live(); len(ops) > 0 {
		if serr := r.saveAside(ops, aside); serr != nil {
			return errors.Join(err, serr)
		}
		return fmt.Errorf("mirror: %d changes received after barrier %d were saved in %s: %w", len(ops), d.Checkpoint().Barrier, aside, err)
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (d *Destination) recover() error {
	data, err := os.ReadFile(d.path("journal.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("mirror journal: %w", err)
	}
	// The changes that the interrupted replay applied count as the destination's again before the rest is applied.
	done, err := d.replay(j)
	d.recovered.aside = done.aside
	d.recovered.saved = append(d.recovered.saved, done.saved...)
	return err
}

// commit applies a barrier's changes, and returns the files that changed here,
// which it moved to aside first. The journal, written durably first, and a
// marker per finished change let a restart finish the work after a crash.
func (d *Destination) commit(j journal) (replayed, error) {
	if len(j.Ops) > 0 {
		data, err := json.Marshal(j)
		if err != nil {
			return replayed{}, err
		}
		if err := writeFileAtomic(d.path("journal.json"), data); err != nil {
			return replayed{}, err
		}
		if err := syncFilesystem(d.state); err != nil {
			return replayed{}, err
		}
	}
	return d.replay(j)
}

// replayed is what a replay did besides applying the changes.
type replayed struct {
	aside string   // where files that changed here go; empty when that isn't checked
	saved []string // the files moved or copied there
}

// replay applies a journal. With an aside folder, it first moves there what changed here.
func (d *Destination) replay(j journal) (replayed, error) {
	a := &applier{d: d, roots: j.Roots, opened: map[string]*os.Root{}, dirs: map[string]bool{}, replayed: replayed{aside: j.Aside}, touched: map[string]bool{}}
	defer a.close()
	barrier := strconv.FormatUint(j.Barrier, 10)
	done := d.path("done", barrier)
	if len(j.Ops) > 0 {
		if err := os.MkdirAll(done, 0o700); err != nil {
			return a.replayed, err
		}
	}
	for i, o := range j.Ops {
		// A put's staged file disappears when it is applied; other changes leave a marker.
		marker := filepath.Join(done, strconv.Itoa(i))
		if o.Kind != "put" {
			if _, err := os.Lstat(marker); err == nil {
				a.record(o, true)
				continue
			}
		}
		info, err := os.Lstat(d.local(o.Path))
		if err := a.apply(o); err != nil {
			return a.replayed, fmt.Errorf("mirror: %s %s: %w", o.Kind, o.Path, err)
		}
		a.record(o, err == nil && info.IsDir())
		if o.Kind != "put" {
			if err := os.Mkdir(marker, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return a.replayed, err
			}
		}
	}
	if err := d.writeStamps(a.touched); err != nil {
		return a.replayed, err
	}
	return a.replayed, d.checkpoint(j)
}

// checkpoint notes that the journal is applied, durably.
func (d *Destination) checkpoint(j journal) error {
	barrier := strconv.FormatUint(j.Barrier, 10)
	cp := Checkpoint{Barrier: j.Barrier, Time: time.Now()}
	for _, r := range j.Roots {
		if _, err := os.Lstat(d.local(r)); err == nil {
			cp.Roots = append(cp.Roots, r)
		}
	}
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(d.path("checkpoint.json"), data); err != nil {
		return err
	}
	if err := syncFilesystem(d.state); err != nil {
		return err
	}
	// Earlier barriers' markers can go now that their journals' removal is on
	// disk; this barrier's stay until the next one, after its journal's.
	old, _ := os.ReadDir(d.path("done"))
	for _, e := range old {
		if e.Name() != barrier {
			if err := os.RemoveAll(d.path("done", e.Name())); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(d.path("journal.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	d.mu.Lock()
	d.cp = cp
	d.mu.Unlock()
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// checkFilesystem makes sure staged files can be renamed into the unit.
func (d *Destination) checkFilesystem(roots []string) error {
	var state syscall.Stat_t
	if err := syscall.Stat(d.state, &state); err != nil {
		return err
	}
	for _, r := range roots {
		var st syscall.Stat_t
		p := d.local(r)
		for syscall.Stat(p, &st) != nil && p != filepath.Dir(p) {
			p = filepath.Dir(p)
		}
		if st.Dev != state.Dev {
			return fmt.Errorf("mirror: the state folder %s is on another filesystem than %s", d.state, r)
		}
	}
	return nil
}

func rootFor(roots []string, p string) string {
	best := ""
	for _, r := range roots {
		if within(p, r) && len(r) > len(best) {
			best = r
		}
	}
	return best
}

// applier changes the unit's folders through os.Root, which keeps every
// write inside them, and never writes through a symlink.
type applier struct {
	d       *Destination
	roots   []string
	opened  map[string]*os.Root
	staging *os.File
	dirs    map[string]bool // parent folders known to be real folders
	replayed
	touched map[string]bool // paths whose stamps changed
}

func (a *applier) close() {
	for _, root := range a.opened {
		root.Close()
	}
	if a.staging != nil {
		a.staging.Close()
	}
}

// resolve finds p's worktree folder, which it creates when asked to.
func (a *applier) resolve(p string, create bool) (*os.Root, string, error) {
	r := rootFor(a.roots, p)
	if r == "" {
		return nil, "", fmt.Errorf("%s is outside the unit", p)
	}
	root := a.opened[r]
	if root == nil {
		local := a.d.local(r)
		info, err := os.Lstat(local)
		if errors.Is(err, fs.ErrNotExist) && create {
			// Metadata in .git is sent before the worktree's own folder.
			if err = os.MkdirAll(local, 0o755); err == nil {
				info, err = os.Lstat(local)
			}
		}
		if err == nil && !info.IsDir() {
			err = fmt.Errorf("%s is not a folder", local)
		}
		if err == nil {
			root, err = os.OpenRoot(local)
		}
		if err != nil {
			return nil, "", err
		}
		a.opened[r] = root
	}
	if p == r {
		return root, ".", nil
	}
	return root, p[len(r)+1:], nil
}

// forget drops the handle of a worktree folder that is created or deleted.
func (a *applier) forget(r string) {
	if root := a.opened[r]; root != nil {
		root.Close()
		delete(a.opened, r)
	}
	clear(a.dirs)
}

// parent resolves p and makes the folders above it real folders.
func (a *applier) parent(p string) (*os.Root, string, error) {
	root, rel, err := a.resolve(p, true)
	if err == nil && rel == "." {
		err = fmt.Errorf("%s is a worktree folder", p)
	}
	if err == nil {
		_, err = a.folders(root, rel, true)
	}
	return root, rel, err
}

// existing resolves p if it exists below real folders only, so that changing
// it never goes through a symlink.
func (a *applier) existing(p string) (*os.Root, string, bool, error) {
	root, rel, err := a.resolve(p, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", false, nil // its worktree is gone
	}
	if err != nil {
		return nil, "", false, err
	}
	ok, err := a.folders(root, rel, false)
	if ok {
		_, err = root.Lstat(rel)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", false, nil
	}
	return root, rel, ok && err == nil, err
}

// folders reports whether the folders above rel are real folders; with
// create, it makes them so, replacing files and symlinks.
func (a *applier) folders(root *os.Root, rel string, create bool) (bool, error) {
	dir := filepath.Dir(rel)
	key := root.Name() + "/" + dir
	if dir == "." || a.dirs[key] {
		return true, nil
	}
	cur := ""
	for _, name := range strings.Split(dir, "/") {
		cur = filepath.Join(cur, name)
		info, err := root.Lstat(cur)
		if err == nil && info.IsDir() {
			continue
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		if !create {
			return false, nil
		}
		if err == nil {
			err = root.Remove(cur)
		} else if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		if err == nil {
			err = root.Mkdir(cur, 0o755)
		}
		if err != nil {
			return false, err
		}
	}
	a.dirs[key] = true
	return true, nil
}

func (a *applier) apply(o op) error {
	switch o.Kind {
	case "put":
		return a.put(o)
	case "mkdir":
		if slices.Contains(a.roots, o.Path) {
			a.forget(o.Path)
			root, _, err := a.resolve(o.Path, true)
			if err != nil {
				return err
			}
			return root.Chmod(".", fs.FileMode(o.Mode))
		}
		root, rel, err := a.parent(o.Path)
		if err != nil {
			return err
		}
		if info, err := root.Lstat(rel); err == nil && !info.IsDir() {
			if err := a.keep(root, rel, o.Path); err != nil {
				return err
			}
			if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if err := root.Mkdir(rel, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		return root.Chmod(rel, fs.FileMode(o.Mode))
	case "symlink":
		root, rel, err := a.parent(o.Path)
		if err != nil {
			return err
		}
		defer clear(a.dirs)
		if err := a.keep(root, rel, o.Path); err != nil {
			return err
		}
		if err := root.RemoveAll(rel); err != nil {
			return err
		}
		return root.Symlink(o.Target, rel)
	case "delete":
		if slices.Contains(a.roots, o.Path) {
			if root, _, err := a.resolve(o.Path, false); err == nil {
				if err := a.keep(root, ".", o.Path); err != nil {
					return err
				}
			}
			a.forget(o.Path)
			return os.RemoveAll(a.d.local(o.Path))
		}
		root, rel, ok, err := a.existing(o.Path)
		if !ok {
			return err
		}
		if err := a.keep(root, rel, o.Path); err != nil {
			return err
		}
		defer clear(a.dirs)
		return root.RemoveAll(rel)
	case "move":
		root, rel, ok, err := a.existing(o.Path)
		if !ok {
			return err // nothing there, or moved before a crash
		}
		toRoot, toRel, err := a.parent(o.To)
		if err != nil {
			return err
		}
		if toRoot != root {
			return errors.New("can't move between worktrees")
		}
		defer clear(a.dirs)
		// What moves stays here, at its new path, so a changed file is only copied aside.
		if info, err := root.Lstat(rel); err == nil && !info.IsDir() && a.changed(o.Path, info) {
			if err := a.copyAside(root, rel, o.Path, info); err != nil {
				return err
			}
		}
		if err := a.keep(toRoot, toRel, o.To); err != nil {
			return err
		}
		if err := root.RemoveAll(toRel); err != nil {
			return err
		}
		return root.Rename(rel, toRel)
	}
	return nil
}

// changed reports whether the file at p changed here since the destination
// last wrote it, or since the destination was created if it never did.
func (a *applier) changed(p string, info fs.FileInfo) bool {
	if a.aside == "" || gitIndex(p) {
		return false
	}
	if a.d.all {
		return true
	}
	if written, ok := a.d.written[p]; ok {
		return written != stampOf(p, info)
	}
	return stampOf(p, info).Ctime >= a.d.since
}

// gitIndex reports whether p is the index of a worktree, which a plain git
// status rewrites.
func gitIndex(p string) bool {
	dir := filepath.Dir(p)
	return filepath.Base(p) == "index" && (filepath.Base(dir) == ".git" ||
		filepath.Base(filepath.Dir(dir)) == "worktrees" && filepath.Base(filepath.Dir(filepath.Dir(dir))) == ".git")
}

// keep moves aside what changed here at rel, the path p, or below it, before a
// change replaces or deletes it.
func (a *applier) keep(root *os.Root, rel, p string) error {
	if a.aside == "" {
		return nil
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		if a.changed(p, info) {
			return a.moveAside(root, rel, p)
		}
		return nil
	}
	return fs.WalkDir(root.FS(), rel, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil // what can't be read can't be deleted either, which fails the change
		}
		info, err := entry.Info()
		below, _ := filepath.Rel(rel, name)
		if q := filepath.Join(p, below); err == nil && a.changed(q, info) {
			return a.moveAside(root, name, q)
		}
		return nil
	})
}

func (a *applier) moveAside(root *os.Root, rel, p string) error {
	target := filepath.Join(a.aside, p)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	dir, err := root.Open(filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := unix.Renameat(int(dir.Fd()), filepath.Base(rel), unix.AT_FDCWD, target); err != nil {
		return err
	}
	a.saved = append(a.saved, p)
	a.unstamp(p, false)
	return nil
}

func (a *applier) copyAside(root *os.Root, rel, p string, info fs.FileInfo) error {
	target := filepath.Join(a.aside, p)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		link, err := root.Readlink(rel)
		if err == nil {
			err = os.Symlink(link, target)
		}
		if err != nil {
			return err
		}
	} else {
		from, err := root.Open(rel)
		if err != nil {
			return err
		}
		defer from.Close()
		to, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(to, from)
		if err := errors.Join(err, to.Close()); err != nil {
			return err
		}
	}
	a.saved = append(a.saved, p)
	return nil
}

// record notes how an applied change left the files it wrote. Without
// folder, p wasn't a folder before the change.
func (a *applier) record(o op, folder bool) {
	switch o.Kind {
	case "delete":
		a.unstamp(o.Path, folder)
		return
	case "move":
		a.unstamp(o.To, true)
		moved := map[string]stamp{}
		for p, s := range a.d.written {
			if within(p, o.Path) {
				s.Path = o.To + p[len(o.Path):]
				moved[s.Path] = s
				a.unstamp(p, false)
			}
		}
		for p, s := range moved {
			a.d.written[p], a.touched[p] = s, true
		}
		o.Path = o.To
	case "mkdir":
		a.unstamp(o.Path, false) // what is in a folder stays there
	default:
		a.unstamp(o.Path, folder)
	}
	if info, err := os.Lstat(a.d.local(o.Path)); err == nil && !info.IsDir() {
		a.d.written[o.Path], a.touched[o.Path] = stampOf(o.Path, info), true
	}
}

// unstamp forgets that the destination wrote p, and with folder, below it.
func (a *applier) unstamp(p string, folder bool) {
	if _, ok := a.d.written[p]; ok {
		delete(a.d.written, p)
		a.touched[p] = true
	}
	if !folder {
		return
	}
	for q := range a.d.written {
		if within(q, p) {
			delete(a.d.written, q)
			a.touched[q] = true
		}
	}
}

func (a *applier) put(o op) error {
	if _, err := os.Lstat(a.d.path("staging", o.File)); errors.Is(err, fs.ErrNotExist) {
		return nil // applied before a crash
	}
	root, rel, err := a.parent(o.Path)
	if err != nil {
		return err
	}
	if err := a.keep(root, rel, o.Path); err != nil {
		return err
	}
	if info, err := root.Lstat(rel); err == nil && info.IsDir() {
		if err := root.RemoveAll(rel); err != nil {
			return err
		}
	} else if err == nil && info.Mode().IsRegular() {
		if appended, err := a.appendTo(root, rel, o.File, info); appended || err != nil {
			return err
		}
	}
	if a.staging == nil {
		if a.staging, err = os.Open(a.d.path("staging")); err != nil {
			return err
		}
	}
	dir, err := root.Open(filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer dir.Close()
	// os.Root can't rename from outside the unit; renameat with the parent
	// opened through it keeps the same guarantees.
	return unix.Renameat(int(a.staging.Fd()), o.File, int(dir.Fd()), filepath.Base(rel))
}

// appendTo appends the staged file to the file at rel, if that file is where
// the staged one starts, as when Pi adds entries to a session. Readers that
// follow the file, such as Gripi's guard of Pi's session files, then see it
// grow as its writer made it grow, rather than another file in its place.
func (a *applier) appendTo(root *os.Root, rel, staged string, info fs.FileInfo) (bool, error) {
	if info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return false, nil // a change through another link would surprise
	}
	from, err := os.Open(a.d.path("staging", staged))
	if err != nil {
		return false, err
	}
	defer from.Close()
	stagedInfo, err := from.Stat()
	if err != nil || stagedInfo.Size() <= info.Size() {
		return false, err
	}
	file, err := root.OpenFile(rel, os.O_RDWR, 0)
	if err != nil {
		return false, nil
	}
	defer file.Close()
	if opened, err := file.Stat(); err != nil || !os.SameFile(opened, info) {
		return false, nil
	}
	if same, err := samePrefix(file, from, info.Size()); !same || err != nil {
		return false, err
	}
	tail := make([]byte, stagedInfo.Size()-info.Size())
	if _, err := from.ReadAt(tail, info.Size()); err != nil {
		return false, err
	}
	// One write, as Pi appends an entry, so that readers don't see half of it.
	if _, err := file.WriteAt(tail, info.Size()); err != nil {
		return false, err
	}
	if err := file.Chmod(stagedInfo.Mode().Perm()); err != nil {
		return false, err
	}
	if err := root.Chtimes(rel, stagedInfo.ModTime(), stagedInfo.ModTime()); err != nil {
		return false, err
	}
	return true, os.Remove(a.d.path("staging", staged))
}

// samePrefix reports whether two files have the same first size bytes.
func samePrefix(a, b *os.File, size int64) (bool, error) {
	x, y := make([]byte, min(size, 64<<10)), make([]byte, min(size, 64<<10))
	for offset := int64(0); offset < size; {
		n := int(min(int64(len(x)), size-offset))
		if _, err := a.ReadAt(x[:n], offset); err != nil {
			return false, ignoreEOF(err)
		}
		if _, err := b.ReadAt(y[:n], offset); err != nil {
			return false, ignoreEOF(err)
		}
		if !bytes.Equal(x[:n], y[:n]) {
			return false, nil
		}
		offset += int64(n)
	}
	return true, nil
}

func ignoreEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// receiver is the destination's side of one connection.
type receiver struct {
	d       *Destination
	c       *conn
	aside   string
	roots   []string
	pending []op
	lastPut map[string]int // path → its pending put, which a newer copy replaces
	seq     int
	cur     *incoming
}

type incoming struct {
	path, name string
	file       *os.File
	w          io.Writer
	bases      []*os.File
	hash       hash.Hash
	mode       uint32
	mtime      int64
}

func (in *incoming) close() {
	for _, b := range in.bases {
		if b != nil {
			b.Close()
		}
	}
	in.file.Close()
}

func (r *receiver) run() error {
	f, err := r.c.receive()
	if err != nil {
		return err
	}
	if f.Kind != kindHello || f.Version != version {
		return r.fail(fmt.Errorf("mirror: the source speaks version %d, not %d", f.Version, version))
	}
	if err := r.addRoots(f.Roots); err != nil {
		return r.fail(err)
	}
	if err := r.addRoots(r.d.Checkpoint().Roots); err != nil {
		return r.fail(err)
	}
	if err := r.d.checkFilesystem(r.roots); err != nil {
		return r.fail(err)
	}
	manifest := &frame{Kind: kindManifest, N: r.d.Checkpoint().Barrier, Entries: r.list(f.Skip)}
	if err := r.c.send(manifest); err != nil {
		return err
	}
	if err := r.c.flush(); err != nil {
		return err
	}
	for {
		f, err := r.c.receive()
		if err != nil {
			return err
		}
		if f.Kind == kindBarrier {
			if err := r.barrier(f.N); err != nil {
				return err
			}
			continue
		}
		if err := r.handle(f); err != nil {
			return r.fail(err)
		}
	}
}

func (r *receiver) fail(err error) error {
	if r.c.send(&frame{Kind: kindFail, Error: err.Error()}) == nil {
		r.c.flush()
	}
	return err
}

func (r *receiver) addRoots(roots []string) error {
	for _, root := range roots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || !within(root, r.d.from) {
			return fmt.Errorf("mirror: refusing the folder %q", root)
		}
		if !slices.Contains(r.roots, root) {
			r.roots = append(r.roots, root)
		}
	}
	return nil
}

func (r *receiver) check(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || rootFor(r.roots, p) == "" {
		return fmt.Errorf("mirror: refusing the path %q outside the unit", p)
	}
	return nil
}

// list returns what the destination has, leaving out the source's ignored folders.
func (r *receiver) list(skip []string) []entry {
	var entries []entry
	for _, root := range r.roots {
		local := r.d.local(root)
		filepath.WalkDir(local, func(lp string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			p := root + strings.TrimPrefix(lp, local)
			if de.IsDir() && p != root && (slices.Contains(skip, p) || slices.Contains(r.roots, p)) {
				return filepath.SkipDir
			}
			info, err := de.Info()
			if err != nil || (!info.IsDir() && !info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0) {
				return nil
			}
			e := entryOf(p, info)
			if e.Type == 'l' {
				e.Target, _ = os.Readlink(lp)
			}
			entries = append(entries, e)
			return nil
		})
	}
	return entries
}

func (r *receiver) handle(f *frame) error {
	switch f.Kind {
	case kindRoots:
		return r.addRoots(f.Roots)
	case kindPut:
		return r.put(f)
	case kindSigRequest:
		return r.signatures(f)
	case kindMkdir, kindSymlink, kindDelete:
		if err := r.check(f.Path); err != nil {
			return err
		}
		o := op{Path: f.Path, Mode: f.Mode, Target: f.Target}
		switch f.Kind {
		case kindMkdir:
			o.Kind = "mkdir"
		case kindSymlink:
			o.Kind = "symlink"
		default:
			o.Kind = "delete"
			if i, ok := r.lastPut[f.Path]; ok {
				r.drop(i)
			}
		}
		r.pending = append(r.pending, o)
	case kindMove:
		if err := r.check(f.Path); err != nil {
			return err
		}
		if err := r.check(f.To); err != nil {
			return err
		}
		clear(r.lastPut) // a later copy of the old path is a different file
		r.pending = append(r.pending, op{Kind: "move", Path: f.Path, To: f.To})
	default:
		return fmt.Errorf("mirror: unexpected frame %d", f.Kind)
	}
	return nil
}

func (r *receiver) drop(i int) {
	os.Remove(r.d.path("staging", r.pending[i].File))
	r.pending[i].Kind = ""
	delete(r.lastPut, r.pending[i].Path)
}

func (r *receiver) live() []op {
	var ops []op
	for _, o := range r.pending {
		if o.Kind != "" {
			ops = append(ops, o)
		}
	}
	return ops
}

// view opens what the destination has at p: its newest staged copy, or the file in the unit.
func (r *receiver) view(p string) (*os.File, error) {
	if i, ok := r.lastPut[p]; ok {
		return os.Open(r.d.path("staging", r.pending[i].File))
	}
	a := &applier{d: r.d, roots: r.roots, opened: map[string]*os.Root{}}
	defer a.close()
	root, rel, err := a.resolve(p, false)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a file", p)
	}
	return f, nil
}

func (r *receiver) signatures(f *frame) error {
	files := make([]*os.File, len(f.Bases))
	var largest int64
	for i, b := range f.Bases {
		if err := r.check(b); err != nil {
			return err
		}
		if file, err := r.view(b); err == nil {
			files[i] = file
			if info, err := file.Stat(); err == nil {
				largest = max(largest, info.Size())
			}
		}
	}
	size := blockSize(largest)
	sums := make([][]blockSum, len(files))
	for i, file := range files {
		if file != nil {
			sums[i], _ = blockSums(file, size)
			file.Close()
		}
	}
	if err := r.c.send(&frame{Kind: kindSigs, Path: f.Path, BlockSize: size, Sums: sums}); err != nil {
		return err
	}
	return r.c.flush()
}

func (r *receiver) put(f *frame) error {
	if f.First {
		if r.cur != nil {
			return fmt.Errorf("mirror: %s started before %s ended", f.Path, r.cur.path)
		}
		if err := r.check(f.Path); err != nil {
			return err
		}
		r.seq++
		in := &incoming{path: f.Path, name: strconv.Itoa(r.seq), mode: f.Mode, mtime: f.Mtime}
		file, err := os.OpenFile(r.d.path("staging", in.name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		in.file, in.w = file, file
		r.cur = in
		for _, b := range f.Bases {
			if err := r.check(b); err != nil {
				return err
			}
			base, _ := r.view(b) // a missing base fails only if an op needs it
			in.bases = append(in.bases, base)
		}
		if len(in.bases) > 0 {
			in.hash = sha256.New()
			in.w = io.MultiWriter(file, in.hash)
		}
	}
	in := r.cur
	if in == nil || in.path != f.Path {
		return fmt.Errorf("mirror: unexpected contents of %s", f.Path)
	}
	for _, o := range f.Ops {
		if err := in.write(o); err != nil {
			return err
		}
	}
	if !f.Last {
		return nil
	}
	r.cur = nil
	in.close()
	staged := r.d.path("staging", in.name)
	if f.Abort {
		return os.Remove(staged)
	}
	if len(f.Hash) > 0 && !bytes.Equal(in.hash.Sum(nil), f.Hash) {
		os.Remove(staged)
		return fmt.Errorf("mirror: %s doesn't match after applying its delta", in.path)
	}
	// Mode and time are set while staged, so applying is a rename.
	if err := os.Chmod(staged, fs.FileMode(in.mode)); err != nil {
		return err
	}
	mtime := time.Unix(0, in.mtime)
	if err := os.Chtimes(staged, mtime, mtime); err != nil {
		return err
	}
	if i, ok := r.lastPut[in.path]; ok {
		r.drop(i)
	}
	r.lastPut[in.path] = len(r.pending)
	r.pending = append(r.pending, op{Kind: "put", Path: in.path, File: in.name})
	return nil
}

func (in *incoming) write(o deltaOp) error {
	if len(o.Data) > 0 {
		_, err := in.w.Write(o.Data)
		return err
	}
	if o.Base < 0 || o.Base >= len(in.bases) || in.bases[o.Base] == nil {
		return fmt.Errorf("mirror: %s refers to a file this side doesn't have", in.path)
	}
	n, err := io.Copy(in.w, io.NewSectionReader(in.bases[o.Base], o.Off, o.Len))
	if err == nil && n != o.Len {
		err = fmt.Errorf("mirror: %s refers to data this side doesn't have", in.path)
	}
	return err
}

func (r *receiver) barrier(n uint64) error {
	aside := filepath.Join(r.aside, "main", strconv.FormatUint(n, 10))
	done, err := r.d.commit(journal{Barrier: n, Roots: r.roots, Ops: r.live(), Aside: aside})
	if _, jerr := os.Lstat(r.d.path("journal.json")); err == nil || jerr == nil {
		r.pending = nil // applied, or left to the journal for a restart
		clear(r.lastPut)
	}
	// Before the source hears of it, so that what follows the barrier there finds what Applied changed.
	if err == nil && r.d.Applied != nil {
		r.d.Applied(r.d.Checkpoint(), aside, done.saved)
	}
	ack := &frame{Kind: kindAck, N: n}
	if err != nil {
		ack.Error = err.Error()
	}
	if serr := r.c.send(ack); serr == nil {
		r.c.flush()
	}
	return err
}

func (r *receiver) saveAside(ops []op, aside string) error {
	if err := os.MkdirAll(aside, 0o700); err != nil {
		return err
	}
	if err := os.Rename(r.d.path("staging"), filepath.Join(aside, "files")); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ops, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(aside, "changes.json"), data, 0o600)
}
