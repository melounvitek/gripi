package reconcile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// entry is what a path holds. Folders compare by type alone, as Git doesn't know their modes.
type entry struct {
	Type   string `json:"type,omitempty"` // "f", "l" or "d", "x" for anything else; empty when missing
	Mode   uint32 `json:"mode,omitempty"` // a file's permissions
	OID    string `json:"oid,omitempty"`  // a file's blob
	Target string `json:"target,omitempty"`
	Stamp  stamp  `json:"stamp,omitzero"` // how the shadow's copy of a file looked when it was recorded
}

func (e entry) same(o entry) bool {
	return e.Type == o.Type && e.Mode == o.Mode && e.OID == o.OID && e.Target == o.Target && e.Type != "x"
}

// stamp tells whether the shadow's copy of a file is still the one recorded: the mirror replaces files with new
// ones, or appends to them.
type stamp struct {
	Size, Mtime int64
	Ino         uint64
}

// lstat reads what p holds, except a file's blob ID.
func lstat(p string) (entry, error) {
	info, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return entry{}, nil
	}
	if err != nil {
		return entry{}, err
	}
	switch {
	case info.IsDir():
		return entry{Type: "d"}, nil
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		return entry{Type: "l", Target: target}, err
	case info.Mode().IsRegular():
		ino := info.Sys().(*syscall.Stat_t).Ino
		return entry{Type: "f", Mode: uint32(info.Mode().Perm()), Stamp: stamp{info.Size(), info.ModTime().UnixNano(), uint64(ino)}}, nil
	}
	return entry{Type: "x"}, nil
}

// entries reads what the repository's folders have at paths in the worktree at root, with the files' blob IDs. A path
// below anything but a folder, such as a link, holds nothing.
func (r repo) entries(ctx context.Context, root string, paths []string) (map[string]entry, error) {
	found := make(map[string]entry, len(paths))
	var files []string
	for _, p := range paths {
		var e entry
		if !blocked(r.path(root), r.path(p)) {
			var err error
			if e, err = lstat(r.path(p)); err != nil {
				return nil, err
			}
		}
		found[p] = e
		if e.Type == "f" {
			files = append(files, p)
		}
	}
	return found, r.hashInto(ctx, found, files)
}

// hashInto fills in the blob IDs of the files at paths, as the repository hashes them.
func (r repo) hashInto(ctx context.Context, found map[string]entry, paths []string) error {
	local := make([]string, len(paths))
	for i, p := range paths {
		local[i] = r.path(p)
	}
	ids, err := r.hash(ctx, local, false)
	for i, id := range ids {
		e := found[paths[i]]
		e.OID = id
		found[paths[i]] = e
	}
	return err
}

// walk lists what the shadow has in the worktree at root, leaving out the worktree's .git and other worktrees in it.
// Files whose stamps match base get base's blob IDs; the rest are hashed.
func (r repo) walk(ctx context.Context, root string, others []string, base map[string]entry) (map[string]entry, error) {
	found := map[string]entry{}
	var files []string
	err := filepath.WalkDir(r.path(root), func(local string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		p := strings.TrimPrefix(local, r.at)
		if p == root {
			return nil
		}
		if p == root+"/.git" || slices.Contains(others, p) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		e, err := lstat(local)
		if err != nil || e.Type == "x" {
			return err
		}
		if b := base[p]; e.Type == "f" && b.Type == "f" && b.Stamp == e.Stamp {
			e.OID = b.OID
		} else if e.Type == "f" {
			files = append(files, p)
		}
		found[p] = e
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, r.hashInto(ctx, found, files)
}

func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/") // dir may be /
}

// blocked reports whether something other than a folder is at root or between it and p, so that p can't hold
// anything, and a change to it would go through a link.
func blocked(root, p string) bool {
	for dir := filepath.Dir(p); within(dir, root); dir = filepath.Dir(dir) {
		if e, err := lstat(dir); err != nil || e.Type != "d" && e.Type != "" {
			return true
		}
		if dir == root {
			break
		}
	}
	return false
}

// copyFile copies the file from to the new file to, with its permissions and modification time.
func copyFile(from, to string) error {
	in, err := os.OpenFile(from, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(info.Mode().Perm())
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chtimes(to, info.ModTime(), info.ModTime())
	}
	return err
}

// place makes p hold e, with the contents of the file from, through a temporary file next to it, so that p is
// never half-written. A folder in the way must be empty.
func place(p string, e entry, from string) error {
	if e.Type == "d" {
		if err := os.Mkdir(p, 0o777); err == nil || !errors.Is(err, fs.ErrExist) {
			return err
		}
		if current, err := lstat(p); err != nil || current.Type == "d" {
			return err
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		return os.Mkdir(p, 0o777)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(p), ".gripi-"+randomName())
	var err error
	if e.Type == "l" {
		err = os.Symlink(e.Target, tmp)
	} else {
		err = copyFile(from, tmp)
		if err == nil {
			err = os.Chmod(tmp, fs.FileMode(e.Mode))
		}
	}
	if err == nil {
		if current, lerr := lstat(p); lerr == nil && current.Type == "d" {
			err = os.Remove(p)
		}
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// keep writes a version of p, something kept aside, at <tree>/<p>, where tree holds such versions of many paths.
// Nothing there marks a missing version. A version never goes through or replaces another that isn't a folder, and a
// folder keeps the versions in it.
func (r repo) keep(ctx context.Context, tree, p string, e entry, from string) error {
	dst := filepath.Join(tree, p)
	if blocked(tree, dst) {
		return nil
	}
	current, err := lstat(dst)
	if err != nil || current.Type == "d" {
		return err
	}
	if current.Type != "" {
		if err := os.Remove(dst); err != nil {
			return err
		}
	}
	return r.write(ctx, dst, e, from)
}

// write writes e to dst: a file with the contents of from, or else of its blob, a link or a folder.
func (r repo) write(ctx context.Context, dst string, e entry, from string) error {
	if e.Type == "" || e.Type == "x" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if e.Type == "d" {
		return os.Mkdir(dst, 0o700)
	}
	if e.Type == "l" {
		return os.Symlink(e.Target, dst)
	}
	if from != "" {
		return copyFile(from, dst)
	}
	data, err := r.git(ctx, nil, r.gitDir, "", "cat-file", "blob", e.OID)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(data), fs.FileMode(e.Mode))
}

// keepTree copies the folder from with everything in it to <tree>/<p>, in place of what was there, as keep would.
func keepTree(from, tree, p string) error {
	dst := filepath.Join(tree, p)
	if blocked(tree, dst) {
		return nil
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		to := filepath.Join(dst, strings.TrimPrefix(p, from))
		e, err := lstat(p)
		switch {
		case err != nil:
			return err
		case e.Type == "d":
			return os.MkdirAll(to, 0o700)
		case e.Type == "l":
			return os.Symlink(e.Target, to)
		case e.Type == "f":
			return copyFile(p, to)
		}
		return nil
	})
}

func randomName() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// writeJSON writes v to path durably, all at once.
func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.Create(path + ".tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(path+".tmp", path)
	}
	if err == nil {
		err = syncDir(filepath.Dir(path))
	}
	return err
}

// readJSON reads path into v; it returns fs.ErrNotExist if there's no such file.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// syncFilesystems makes everything written to the file systems of the folders durable.
func syncFilesystems(dirs ...string) error {
	for _, dir := range dirs {
		if err := syncFilesystem(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
