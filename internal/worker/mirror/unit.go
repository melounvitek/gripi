package mirror

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Unit is what moves to a worker as a whole: the main folder, its linked
// worktrees and their common Git folder. Paths are absolute and the same on
// both machines.
type Unit struct {
	Main      string   `json:"main"`
	Worktrees []string `json:"worktrees,omitempty"`
	GitDir    string   `json:"git_dir,omitempty"` // empty for a folder outside Git
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
		if len(worktrees) > 0 {
			if _, err := os.Stat(path); err != nil {
				continue // prunable
			}
		}
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
	if u.GitDir != "" && !within(u.GitDir, u.Main) {
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

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/") || dir == "/"
}
