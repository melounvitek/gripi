// Package reconcile records the base of a session's move to a worker and brings what the session changed there back
// to main's folders, which other sessions kept changing meanwhile.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// Selection is what moves with a session, as main's canonical absolute paths.
type Selection struct {
	Cwd    string   // the session's starting folder
	GitDir string   // the repository's common Git folder
	Roots  []string // the selected worktrees' folders, the main checkout's among them if it is selected
}

// manifest is the base of a move: main as the move's copy started from it.
type manifest struct {
	Move       string           `json:"move"`
	GitDir     string           `json:"git_dir"`
	Cwd        string           `json:"cwd"`
	Registered []string         `json:"registered"` // the folders of every worktree that the repository had
	Worktrees  []worktree       `json:"worktrees"`  // the selected ones
	Refs       map[string]ref   `json:"refs"`
	Stash      []stash          `json:"stash"`
	Files      map[string]entry `json:"files"`     // what the selected worktrees hold
	GitFiles   map[string]entry `json:"git_files"` // the Git folder's config and hooks
}

var moveID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Snapshot copies the selected folders of main and the repository's Git folder into the folder shadow, at
// <shadow>/<absolute path>, with the files that a mirror copies, and records them in the folder base as the move's
// base. The base's files and commits stay in the repository, under refs/gripi/moves/<move>/base, until Discard. The
// repository's other sessions must be idle, as nothing must change the folders meanwhile. Snapshot returns the
// folders that it copied, whose copies the worker gets. The shadow and the base become durable together.
func Snapshot(ctx context.Context, move string, sel Selection, base, shadow string) ([]string, error) {
	if !moveID.MatchString(move) {
		return nil, fmt.Errorf("move ID %q isn't a plain name", move)
	}
	main := repo{gitDir: sel.GitDir}
	registered, err := main.worktrees(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := check(ctx, sel, registered); err != nil {
		return nil, err
	}
	for _, dir := range []string{base, shadow} {
		if err := os.RemoveAll(dir); err != nil {
			return nil, err
		}
	}
	captured := slices.Clone(sel.Roots)
	if !slices.ContainsFunc(sel.Roots, func(root string) bool { return within(sel.GitDir, root) }) {
		captured = append(captured, sel.GitDir)
	}
	paths, err := mirror.List(mirror.Unit{Main: sel.Roots[0], Worktrees: sel.Roots[1:], GitDir: sel.GitDir}, mirror.Options{})
	if err != nil {
		return nil, err
	}
	if err := checkLFS(ctx, sel.GitDir, paths); err != nil {
		return nil, err
	}
	// What unselected worktrees in selected ones have stays out.
	paths = slices.DeleteFunc(paths, func(p string) bool {
		owner := ""
		for _, w := range registered {
			if within(p, w.Root) && len(w.Root) > len(owner) {
				owner = w.Root
			}
		}
		return !within(p, sel.GitDir) && !slices.Contains(sel.Roots, owner)
	})

	m := manifest{Move: move, GitDir: sel.GitDir, Cwd: sel.Cwd, Files: map[string]entry{}, GitFiles: map[string]entry{}}
	for _, root := range captured {
		if err := os.MkdirAll(shadow+filepath.Dir(root), 0o700); err != nil {
			return nil, err
		}
	}
	type recorded struct {
		in  map[string]entry
		key string
	}
	var files []string // to hash, in the shadow
	var hashed []recorded
	for _, p := range paths {
		if err := copyPath(p, shadow+p); err != nil {
			return nil, err
		}
		e, err := lstat(shadow + p)
		if err != nil {
			return nil, err
		}
		r := recorded{m.Files, p}
		if within(p, sel.GitDir) {
			rel := strings.TrimPrefix(p, sel.GitDir+"/")
			if rel != "config" && !within(rel, "hooks") || e.Type == "d" {
				continue
			}
			r = recorded{m.GitFiles, p}
		} else if slices.Contains(sel.Roots, p) || slices.Contains(sel.Roots, filepath.Dir(p)) && filepath.Base(p) == ".git" {
			continue
		}
		r.in[r.key] = e
		if e.Type == "f" {
			files, hashed = append(files, shadow+p), append(hashed, r)
		}
	}
	ids, err := main.hash(ctx, files, true)
	if err != nil {
		return nil, err
	}
	for i, r := range hashed {
		e := r.in[r.key]
		e.OID = ids[i]
		r.in[r.key] = e
	}

	// The base's Git state is what the shadow got.
	copied := repo{gitDir: sel.GitDir, at: shadow}
	if m.Refs, err = copied.refs(ctx); err != nil {
		return nil, err
	}
	all, err := copied.worktrees(ctx, m.Refs)
	if err != nil {
		return nil, err
	}
	pinned := slices.Clone(ids)
	var commits, others []string
	for _, w := range all {
		m.Registered = append(m.Registered, w.Root)
		if !slices.Contains(sel.Roots, w.Root) {
			continue
		}
		if w.Index, err = copied.index(ctx, w); err != nil {
			return nil, err
		}
		if w.Dirty, err = dirty(ctx, w); err != nil {
			return nil, err
		}
		m.Worktrees = append(m.Worktrees, w)
		pinned = append(pinned, blobs(w.Index)...)
		commits = append(commits, w.Head.OID)
	}
	if m.Stash, err = copied.stashes(ctx); err != nil {
		return nil, err
	}
	for _, s := range m.Stash {
		commits = append(commits, s.OID)
	}
	for _, r := range m.Refs {
		commits = append(commits, r.Commit)
		if r.OID != r.Commit {
			others = append(others, r.OID)
		}
	}
	if err := main.pin(ctx, "refs/gripi/moves/"+move+"/base", pinned, commits, others); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	if err := syncFilesystems(shadow, base, sel.GitDir); err != nil {
		return nil, err
	}
	return captured, writeJSON(filepath.Join(base, "manifest.json"), m)
}

// check refuses a selection that isn't the repository's, and what a move can't bring back: alternates, submodules
// and operations in progress.
func check(ctx context.Context, sel Selection, registered []worktree) error {
	if filepath.Base(sel.GitDir) != ".git" {
		return fmt.Errorf("%s isn't a .git folder in a main checkout", sel.GitDir)
	}
	if _, err := os.Stat(filepath.Join(sel.GitDir, "objects", "info", "alternates")); err == nil {
		return errors.New("a repository with alternates can't move")
	}
	if !slices.ContainsFunc(sel.Roots, func(root string) bool { return within(sel.Cwd, root) }) {
		return fmt.Errorf("%s isn't in a selected worktree", sel.Cwd)
	}
	for _, root := range sel.Roots {
		i := slices.IndexFunc(registered, func(w worktree) bool { return w.Root == root })
		if i < 0 {
			return fmt.Errorf("%s isn't a worktree of %s", root, sel.GitDir)
		}
		for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
			if _, err := os.Lstat(filepath.Join(registered[i].GitDir, name)); err == nil {
				return fmt.Errorf("finish the merge, rebase, cherry-pick or revert in %s first", root)
			}
		}
		index, err := repo{}.index(ctx, registered[i])
		if err != nil {
			return err
		}
		if strings.HasPrefix(index, "160000 ") || strings.Contains(index, "\x00160000 ") {
			return errors.New("a repository with submodules can't move")
		}
	}
	return nil
}

var lfsFilter = regexp.MustCompile(`(?m)^[^#\n]*[ \t]filter=lfs\b`)

// checkLFS refuses a repository that uses Git LFS, as a move doesn't bring back the files' contents that it keeps in
// the Git folder: one with LFS's folder there, LFS in its config, or an LFS filter in the attributes among paths.
func checkLFS(ctx context.Context, gitDir string, paths []string) error {
	refused := errors.New("a repository that uses Git LFS can't move")
	if _, err := os.Lstat(filepath.Join(gitDir, "lfs")); err == nil {
		return refused
	}
	// git config fails when nothing matches.
	if out, _ := (repo{}).git(ctx, nil, gitDir, "", "config", "--local", "--name-only", "--get-regexp", `^(lfs|filter\.lfs)\.`); out != "" {
		return refused
	}
	for _, p := range paths {
		if filepath.Base(p) != ".gitattributes" && p != filepath.Join(gitDir, "info", "attributes") {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if lfsFilter.Match(data) {
			return refused
		}
	}
	return nil
}

// copyPath copies the file, link or folder at from to the new path to; a folder may be there already.
func copyPath(from, to string) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if err := os.Mkdir(to, info.Mode().Perm()|0o700); !errors.Is(err, fs.ErrExist) {
			return err
		}
		return os.Chmod(to, info.Mode().Perm()|0o700)
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(from)
		if err != nil {
			return err
		}
		return os.Symlink(target, to)
	}
	return copyFile(from, to)
}
