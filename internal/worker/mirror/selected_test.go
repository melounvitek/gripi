//go:build linux

package mirror

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// asACopy makes a repository look as a moved session's copy of it does on a worker: the copy has the worktree that
// the session started in and the common .git, but neither the main worktree's files nor the other worktrees, which
// Git still lists.
func asACopy(t *testing.T, repository Unit) {
	t.Helper()
	entries, err := os.ReadDir(repository.Main)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			os.RemoveAll(filepath.Join(repository.Main, entry.Name()))
		}
	}
	os.RemoveAll(repository.Worktrees[1])
}

func TestAUnitOfSelectedFoldersNeverTakesTheOthers(t *testing.T) {
	repository := newRepo(t)
	base := filepath.Dir(filepath.Dir(repository.Main))
	featA, featB := repository.Worktrees[0], repository.Worktrees[1]
	asACopy(t, repository)
	unit := Unit{Main: featA, GitDir: repository.GitDir, Within: []string{base}}
	dst := t.TempDir()
	// Main's copy has a folder where the worker's copy lacks the unselected worktree.
	writeFile(t, dst+featB, "main's.txt", "main's\n")
	r := &rig{unit: unit, dst: dst, state: filepath.Join(dst, "state")}
	var err error
	if r.d, err = NewMovedDestination(r.state, "/", dst); err != nil {
		t.Fatal(err)
	}
	r.start(t, nil)

	// The session adds a worktree in the moved folders and one elsewhere, and removes the unselected one.
	featC := filepath.Join(filepath.Dir(featA), "feat-c")
	outside := filepath.Join(t.TempDir(), "feat-d")
	git(t, featA, "worktree", "add", "-q", "-b", "feat-c", featC)
	git(t, featA, "worktree", "add", "-q", "-b", "feat-d", outside)
	git(t, featA, "worktree", "prune")
	writeFile(t, featA, "src/d1/f1.txt", "the session's change\n")
	writeFile(t, featC, "new.txt", "in feat-c\n")
	r.barrier(t)

	for path, want := range map[string]string{
		filepath.Join(featA, "src/d1/f1.txt"): "the session's change\n", filepath.Join(featC, "new.txt"): "in feat-c\n",
		filepath.Join(featB, "main's.txt"): "main's\n",
	} {
		if got, _ := os.ReadFile(dst + path); string(got) != want {
			t.Errorf("main's copy of %s = %q, want %q", path, got, want)
		}
	}
	if _, err := os.Stat(dst + filepath.Join(featC, ".git")); err != nil {
		t.Errorf("main's copy of the new worktree: %v", err)
	}
	if entries, err := os.ReadDir(dst + repository.Main); err != nil || len(entries) != 1 || entries[0].Name() != ".git" {
		t.Errorf("main's copy of the main worktree, which holds the common .git: %v, %v", entries, err)
	}
	if _, err := os.Lstat(dst + outside); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("main's copy of a worktree outside the moved folders: %v", err)
	}
	roots := r.d.Checkpoint().Roots
	slices.Sort(roots)
	if want := []string{repository.GitDir, featA, featC}; !slices.Equal(roots, sorted(want)) {
		t.Errorf("the checkpoint's folders = %v, want %v", roots, sorted(want))
	}

	// A worktree that joined the unit and goes on the worker goes on main too.
	git(t, featA, "worktree", "remove", "--force", featC)
	r.barrier(t)
	if _, err := os.Lstat(dst + featC); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("main's copy of the removed worktree: %v", err)
	}
	if got, _ := os.ReadFile(dst + filepath.Join(featB, "main's.txt")); string(got) != "main's\n" {
		t.Errorf("main's folder of the unselected worktree has %q", got)
	}
}

// A mirror that starts again keeps the worktrees that joined the unit, also once the session removed the one that it
// started in.
func TestAUnitOfSelectedFoldersKeepsTheWorktreesThatJoinedIt(t *testing.T) {
	repository := newRepo(t)
	featA := repository.Worktrees[0]
	asACopy(t, repository)
	r := newRig(t, Unit{Main: featA, GitDir: repository.GitDir, Within: []string{filepath.Dir(filepath.Dir(repository.Main))}})
	featC := filepath.Join(filepath.Dir(featA), "feat-c")
	git(t, featA, "worktree", "add", "-q", "-b", "feat-c", featC)
	r.barrier(t)
	restart := func() {
		t.Helper()
		r.conn.Close()
		<-r.served
		<-r.ran
		r.start(t, nil)
	}

	restart()
	writeFile(t, featC, "after.txt", "after the restart\n")
	r.barrier(t)
	git(t, featC, "worktree", "remove", "--force", featA)
	featE := filepath.Join(filepath.Dir(featA), "feat-e")
	git(t, featC, "worktree", "add", "-q", "-b", "feat-e", featE)
	writeFile(t, featE, "new.txt", "in feat-e\n")
	r.barrier(t)
	restart()
	writeFile(t, featC, "last.txt", "after the second restart\n")
	r.barrier(t)

	for path, want := range map[string]string{
		filepath.Join(featC, "after.txt"): "after the restart\n", filepath.Join(featE, "new.txt"): "in feat-e\n",
		filepath.Join(featC, "last.txt"): "after the second restart\n",
	} {
		if got, _ := os.ReadFile(r.dst + path); string(got) != want {
			t.Errorf("main's copy of %s = %q, want %q", path, got, want)
		}
	}
	if _, err := os.Lstat(r.dst + featA); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("main's copy of the removed worktree: %v", err)
	}
}

func TestAUnitOfSelectedFoldersMayBeOutsideGit(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "notes")
	writeFile(t, folder, "todo.txt", "first\n")
	r := newRig(t, Unit{Main: folder, Within: []string{filepath.Dir(folder)}})
	writeFile(t, folder, "todo.txt", "second\n")
	r.barrier(t)
	if got, _ := os.ReadFile(r.dst + filepath.Join(folder, "todo.txt")); string(got) != "second\n" {
		t.Fatalf("main has %q", got)
	}
}

func sorted(paths []string) []string {
	paths = slices.Clone(paths)
	slices.Sort(paths)
	return paths
}

// A moved session's file is mirrored on its own: the folder's other sessions stay as they are on both sides.
func TestAFileUnitKeepsToItsFile(t *testing.T) {
	folder := t.TempDir()
	file := filepath.Join(folder, "session.jsonl")
	writeFile(t, folder, "other.jsonl", "{\"worker's\":1}\n")
	dst := t.TempDir()
	writeFile(t, dst+folder, "mains.jsonl", "{\"main's\":1}\n")
	r := &rig{unit: Unit{Main: folder, File: file}, dst: dst, state: filepath.Join(dst, "state")}
	var err error
	if r.d, err = NewMovedDestination(r.state, "/", dst); err != nil {
		t.Fatal(err)
	}
	r.d.File = file
	r.start(t, sessionMirror)
	onMain := func(name string) string {
		data, _ := os.ReadFile(dst + filepath.Join(folder, name))
		return string(data)
	}
	waitForMain := func(want string) {
		t.Helper()
		waitFor(t, "main's copy of the session", func() bool { return onMain("session.jsonl") == want })
	}
	if _, err := os.Lstat(dst + file); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("main has the session before Pi saved it: %v", err)
	}

	// Pi saves the session once it has something to save, and goes on appending to it.
	writeFile(t, folder, "session.jsonl", "{\"type\":\"session\"}\n{\"id\":")
	waitForMain("{\"type\":\"session\"}\n")
	appendFile(t, folder, "session.jsonl", "\"1\"}\n")
	waitForMain("{\"type\":\"session\"}\n{\"id\":\"1\"}\n")
	// An atomic rewrite replaces it.
	writeFile(t, folder, "session.jsonl.tmp", "{\"type\":\"session\",\"version\":3}\n")
	if err := os.Rename(file+".tmp", file); err != nil {
		t.Fatal(err)
	}
	waitForMain("{\"type\":\"session\",\"version\":3}\n")
	// A session file that goes on the worker stays on main.
	writeFile(t, folder, "other.jsonl", "{\"worker's\":2}\n")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	r.barrier(t)

	if got := onMain("session.jsonl"); got != "{\"type\":\"session\",\"version\":3}\n" {
		t.Errorf("main's copy of the session after it went on the worker = %q", got)
	}
	if got := onMain("mains.jsonl"); got != "{\"main's\":1}\n" {
		t.Errorf("main's other session = %q", got)
	}
	entries, _ := os.ReadDir(dst + folder)
	if names := names(entries); !slices.Equal(names, []string{"mains.jsonl", "session.jsonl"}) {
		t.Errorf("main's session folder has %v", names)
	}
}

func names(entries []fs.DirEntry) []string {
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// Main's copy of a session file refuses anything else, whatever the worker sends.
func TestAFileDestinationRefusesTheFolderAroundTheFile(t *testing.T) {
	folder := t.TempDir()
	writeFile(t, folder, "session.jsonl", "{\"type\":\"session\"}\n")
	writeFile(t, folder, "other.jsonl", "{\"worker's\":1}\n")
	dst := t.TempDir()
	writeFile(t, dst+folder, "mains.jsonl", "{\"main's\":1}\n")
	r := &rig{unit: Unit{Main: folder}, dst: dst, state: filepath.Join(dst, "state")}
	var err error
	if r.d, err = NewMovedDestination(r.state, "/", dst); err != nil {
		t.Fatal(err)
	}
	r.d.File = filepath.Join(folder, "session.jsonl")
	r.launch(t, nil)

	if err := <-r.served; err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("Serve = %v", err)
	}
	entries, _ := os.ReadDir(dst + folder)
	if names := names(entries); !slices.Equal(names, []string{"mains.jsonl"}) {
		t.Errorf("main's session folder has %v", names)
	}
}

// Seeding a moved session's copy and caching what it leaves need the folders that its mirror leaves out because
// Git ignores them, also in worktrees that it doesn't have yet.
func TestWalkMirroredFindsTheFoldersThatGitIgnores(t *testing.T) {
	repository := newRepo(t)
	base := filepath.Dir(filepath.Dir(repository.Main))
	featA, featB := repository.Worktrees[0], repository.Worktrees[1]
	// An ignored folder that holds a worktree is the worktree's.
	git(t, repository.Main, "worktree", "add", "-q", "-b", "inner", filepath.Join(repository.Main, "tmp", "worktrees", "inner"))
	writeFile(t, featA, "node_modules/pkg/index.js", "x\n")
	os.RemoveAll(featB)
	unit := Unit{Main: repository.Main, GitDir: repository.GitDir, Within: []string{base}}

	var walked, ignored []string
	err := WalkMirrored(unit, func(folder string, isIgnored func(string) bool) error {
		walked = append(walked, folder)
		entries, err := os.ReadDir(folder)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if path := filepath.Join(folder, entry.Name()); entry.IsDir() && isIgnored(path) {
				ignored = append(ignored, path)
			}
		}
		if folder == featA && (!isIgnored(filepath.Join(featA, "tmp", "missing")) || isIgnored(filepath.Join(featA, "src", "missing"))) {
			t.Error("ignored tells wrong about folders that a worktree lacks")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(repository.Main, "node_modules"), filepath.Join(repository.Main, "tmp", "cache"), filepath.Join(featA, "node_modules")}
	if slices.Sort(ignored); !slices.Equal(ignored, sorted(want)) {
		t.Errorf("ignored folders = %v, want %v", ignored, sorted(want))
	}
	for _, folder := range walked {
		if within(folder, repository.GitDir) || slices.ContainsFunc(want, func(ignored string) bool { return within(folder, ignored) }) {
			t.Errorf("walked %s", folder)
		}
	}
	if !slices.Contains(walked, filepath.Join(featA, "src", "d1")) || !slices.Contains(walked, filepath.Join(repository.Main, "tmp")) {
		t.Errorf("walked only %v", walked)
	}
}
