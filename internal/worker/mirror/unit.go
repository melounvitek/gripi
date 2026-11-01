package mirror

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Unit is what moves to a worker as a whole: the main folder, its linked
// worktrees and their common Git folder. Paths are absolute and the same on
// both machines.
type Unit struct {
	Main      string   `json:"main"`
	Worktrees []string `json:"worktrees,omitempty"`
	GitDir    string   `json:"git_dir,omitempty"` // empty for a folder outside Git
	// Within, when set, makes the unit only the folders that it names, as for a moved session's copy of its
	// repository: Main may be a linked worktree, and of the worktrees that Git lists later, only linked ones that
	// exist in these folders join the unit. So a worktree that wasn't selected is never taken for one that the unit
	// lost, nor is the main worktree that holds the common Git folder.
	Within []string `json:"within,omitempty"`
	// File, when set, makes the unit only this file in the folder Main, as for a moved session's file. It needn't
	// exist: a source that lacks it never deletes it on the destination, and the folder's other files are never
	// listed, sent or changed.
	File string `json:"file,omitempty"`
}

// Discover finds the unit of the folder dir: the repository it belongs to with
// all its worktrees, or the folder alone outside Git.
func Discover(dir string) (Unit, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Unit{}, err
	}
	out, err := exec.Command("git", "--no-optional-locks", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir", "--is-bare-repository").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return Unit{Main: dir}, nil
		}
		return Unit{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return Unit{}, fmt.Errorf("git rev-parse: unexpected output %q", out)
	}
	if lines[1] == "true" {
		return Unit{}, fmt.Errorf("%s is a bare repository, which can't be mirrored", dir)
	}
	worktrees, err := listWorktrees(dir)
	if err != nil {
		return Unit{}, err
	}
	return Unit{Main: worktrees[0], Worktrees: worktrees[1:], GitDir: filepath.Clean(lines[0])}, nil
}

// listWorktrees lists the existing worktrees of the repository at dir, the
// main one first.
func listWorktrees(dir string) ([]string, error) {
	all, err := gitWorktrees(dir)
	if err != nil {
		return nil, err
	}
	worktrees := []string{all[0]}
	for _, path := range all[1:] {
		if _, err := os.Stat(path); err == nil { // or it is prunable
			worktrees = append(worktrees, path)
		}
	}
	return worktrees, nil
}

// gitWorktrees lists every worktree that the repository at dir has, also
// those whose folders are gone, the main one first.
func gitWorktrees(dir string) ([]string, error) {
	out, err := exec.Command("git", "--no-optional-locks", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list in %s: %w", dir, err)
	}
	var worktrees []string
	for _, block := range strings.Split(strings.TrimSpace(string(out)), "\n\n") {
		path, ok := strings.CutPrefix(block, "worktree ")
		if !ok {
			continue
		}
		path, _, _ = strings.Cut(path, "\n")
		worktrees = append(worktrees, filepath.Clean(path))
	}
	if len(worktrees) == 0 {
		return nil, fmt.Errorf("git worktree list in %s: no worktrees", dir)
	}
	return worktrees, nil
}

// roots are the unit's top folders: the worktrees, and the Git folder when it
// isn't inside the main folder.
func (u Unit) roots() []string {
	roots := append([]string{u.Main}, u.Worktrees...)
	if u.GitDir != "" && !slices.ContainsFunc(roots, func(root string) bool { return within(u.GitDir, root) }) {
		roots = append(roots, u.GitDir)
	}
	return roots
}

// Contains reports whether path is in one of the unit's folders.
func (u Unit) Contains(path string) bool {
	for _, root := range u.roots() {
		if within(path, root) {
			return true
		}
	}
	return false
}

// WalkMirrored walks the folders of the unit's worktrees that a mirror of it takes, also of worktrees that joined the
// unit since (see Within), and calls visit with each one. Its ignored reports whether a folder, which needn't exist,
// is one that the mirror leaves out because Git ignores it and that holds none of the repository's worktrees: one
// that stays on each machine, such as node_modules.
func WalkMirrored(unit Unit, visit func(folder string, ignored func(path string) bool) error) error {
	s := newSender(unit, Options{}, nil)
	defer s.close()
	var held []string
	if unit.GitDir != "" {
		// Main may be a worktree that the session removed.
		listed, err := gitWorktrees(unit.GitDir)
		if err != nil {
			return err
		}
		for i, worktree := range listed {
			if _, err := os.Stat(worktree); err == nil && !s.isRoot(worktree) && joins(unit.Within, worktree, i == 0) {
				s.addRoot(worktree)
			}
		}
		held = append(listed, unit.GitDir)
	}
	ignored := func(path string) bool {
		return s.rootOf(path) != "" && !s.inGit(path) && !s.isRoot(path) && s.excluded(path, true, 0) &&
			!slices.ContainsFunc(held, func(folder string) bool { return within(folder, path) })
	}
	for _, root := range slices.Clone(s.roots) {
		if root == unit.GitDir {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || !entry.IsDir() {
				return nil // what can't be read is left out
			}
			if path != root && (s.isRoot(path) || s.inGit(path) || ignored(path)) {
				return filepath.SkipDir
			}
			return visit(path, ignored)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// joins reports whether a worktree that Git lists, the main one if main, becomes the unit's (see Unit.Within).
func joins(selected []string, worktree string, main bool) bool {
	return selected == nil || !main && slices.ContainsFunc(selected, func(folder string) bool { return within(worktree, folder) })
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/") || dir == "/"
}
