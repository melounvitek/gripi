//go:build linux

package reconcile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// Run from a Git hook or git rebase --exec, Git's variables would point the tests' git commands at that
	// repository instead of their own.
	for _, variable := range os.Environ() {
		if name, _, _ := strings.Cut(variable, "="); strings.HasPrefix(name, "GIT_") {
			os.Unsetenv(name)
		}
	}
	home, err := os.MkdirTemp("", "gripi-reconcile-home-")
	if err != nil {
		panic(err)
	}
	for k, v := range map[string]string{
		"HOME": home, "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "XDG_CONFIG_HOME": home,
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		os.Setenv(k, v)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

const move = "m1"

// lines is a text file whose lines a test changes one at a time, so that changes to different lines merge.
var lines = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"

// rig is a repository on main with a selected worktree, feat, and an unselected one, other, and the state of one
// move. The worker is simulated by changing the shadow, as the mirror would.
type rig struct {
	t            *testing.T
	main, gitDir string
	feat, other  string
	base, shadow string
	recovery     string
	selected     []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{t: t, main: filepath.Join(dir, "src", "proj"), feat: filepath.Join(dir, "wt", "feat"), other: filepath.Join(dir, "wt", "other")}
	r.gitDir = filepath.Join(r.main, ".git")
	state := filepath.Join(dir, "state")
	r.base, r.shadow, r.recovery = filepath.Join(state, "moves", move, "base"), filepath.Join(state, "moves", move, "shadow"), filepath.Join(state, "returns", move)
	run(t, dir, nil, "git", "init", "-q", "-b", "main", r.main)
	r.git(r.main, "config", "gc.auto", "0")
	r.write(r.main, "a.txt", lines)
	r.write(r.main, ".gitignore", "node_modules/\n*.log\n.env\n")
	r.write(r.main, "dir/b.txt", "b\n")
	r.write(r.main, "bin.dat", "\x00\x01binary\n")
	r.write(r.main, "run.sh", "#!/bin/sh\n")
	os.Chmod(filepath.Join(r.main, "run.sh"), 0o755)
	if err := os.Symlink("a.txt", filepath.Join(r.main, "link")); err != nil {
		t.Fatal(err)
	}
	r.git(r.main, "add", "-A")
	r.git(r.main, "commit", "-qm", "initial")
	r.write(r.main, ".env", "SECRET=dummy\n")
	r.write(r.main, "node_modules/pkg/index.js", "x\n")
	r.git(r.main, "branch", "topic")
	r.git(r.main, "worktree", "add", "-q", "-b", "feat", r.feat)
	r.git(r.main, "worktree", "add", "-q", "-b", "other", r.other)
	return r
}

func (r *rig) snapshot(roots ...string) {
	r.t.Helper()
	r.selected = roots
	captured, err := Snapshot(context.Background(), move, Selection{Cwd: roots[0], GitDir: r.gitDir, Roots: roots}, r.base, r.shadow)
	if err != nil {
		r.t.Fatal(err)
	}
	want := append(slices.Clone(roots), r.gitDir)
	if slices.Contains(roots, r.main) {
		want = want[:len(want)-1]
	}
	slices.Sort(want)
	slices.Sort(captured)
	if !slices.Equal(captured, want) {
		r.t.Fatalf("captured %v, want %v", captured, want)
	}
}

func (r *rig) reconcile() Result {
	r.t.Helper()
	result, err := Reconcile(context.Background(), r.base, r.shadow, r.recovery)
	if err != nil {
		r.t.Fatal(err)
	}
	return result
}

// gitDirOf returns the Git folder of the worktree at root.
func (r *rig) gitDirOf(root string) string {
	if root == r.main {
		return r.gitDir
	}
	return filepath.Join(r.gitDir, "worktrees", filepath.Base(root))
}

// worker runs git in the shadow's copy of the worktree at root, as the session on the worker would.
func (r *rig) worker(root string, args ...string) string {
	r.t.Helper()
	return run(r.t, r.shadow+root, []string{"GIT_DIR=" + r.shadow + r.gitDirOf(root), "GIT_WORK_TREE=" + r.shadow + root}, "git", args...)
}

// workerWorktree makes a worktree on the worker at root, on a new branch, as git worktree add there would.
func (r *rig) workerWorktree(root, branch string) {
	r.t.Helper()
	r.worker(r.main, "worktree", "add", "-q", "-b", branch, r.shadow+root)
	// The worker sees main's paths, so its Git names them.
	registration := filepath.Join(r.gitDir, "worktrees", filepath.Base(root))
	r.write(r.shadow+registration, "gitdir", root+"/.git\n")
	r.write(r.shadow+root, ".git", "gitdir: "+registration+"\n")
}

// commit adds an empty commit to branch with git, which runs git on main or on the worker, and returns it.
func (r *rig) commit(git func(args ...string) string, branch, message string) string {
	r.t.Helper()
	id := strings.TrimSpace(git("commit-tree", "-p", branch, "-m", message, branch+"^{tree}"))
	git("update-ref", "refs/heads/"+branch, id)
	return id
}

func (r *rig) onMain(args ...string) string { return r.git(r.main, args...) }

func (r *rig) git(dir string, args ...string) string {
	r.t.Helper()
	return run(r.t, dir, nil, "git", args...)
}

func (r *rig) write(dir, name, content string) {
	r.t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) read(p string) string {
	r.t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(data)
}

func (r *rig) expectFile(p, want string) {
	r.t.Helper()
	if got := r.read(p); got != want {
		r.t.Errorf("%s = %q, want %q", p, got, want)
	}
}

func (r *rig) expectMissing(p string) {
	r.t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
		r.t.Errorf("%s exists, want it missing (%v)", p, err)
	}
}

// rev returns the object that name names, or "" if there's none.
func (r *rig) rev(dir, name string) string {
	out, _ := exec.Command("git", "-C", dir, "rev-parse", "-q", "--verify", name).Output()
	return strings.TrimSpace(string(out))
}

func run(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			stderr = exit.Stderr
		}
		t.Fatalf("%s %v in %s: %v\n%s%s", name, args, dir, err, out, stderr)
	}
	return string(out)
}

// replace returns lines with the line old replaced.
func replace(old, new string) string { return strings.Replace(lines, old+"\n", new+"\n", 1) }

func asideOf(result Result, path string) (Item, bool) {
	i := slices.IndexFunc(result.Aside, func(item Item) bool { return item.Path == path })
	if i < 0 {
		return Item{}, false
	}
	return result.Aside[i], true
}

func TestSnapshotCapturesTheSelectedFoldersAsTheMirrorWould(t *testing.T) {
	r := newRig(t)
	r.write(r.feat, "untracked.txt", "u\n")
	r.snapshot(r.feat)

	r.expectFile(r.shadow+r.feat+"/untracked.txt", "u\n")
	r.expectFile(r.shadow+r.feat+"/a.txt", lines)
	r.expectFile(r.shadow+r.gitDir+"/HEAD", "ref: refs/heads/main\n")
	r.expectMissing(r.shadow + r.main + "/a.txt") // the main checkout isn't selected
	r.expectMissing(r.shadow + r.other)
	if got := strings.TrimSpace(r.worker(r.feat, "status", "--porcelain")); got != "?? untracked.txt" {
		t.Errorf("git status in the shadow = %q", got)
	}

	r = newRig(t)
	r.snapshot(r.main)
	r.expectFile(r.shadow+r.main+"/.env", "SECRET=dummy\n") // ignored, but small
	r.expectMissing(r.shadow + r.main + "/node_modules")
	if info, err := os.Lstat(r.shadow + r.main + "/run.sh"); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("run.sh in the shadow: %v %v", info, err)
	}
	if target, err := os.Readlink(r.shadow + r.main + "/link"); err != nil || target != "a.txt" {
		t.Errorf("link in the shadow: %q %v", target, err)
	}
}

func TestWorkerOnlyChangesAreApplied(t *testing.T) {
	r := newRig(t)
	r.snapshot(r.main)
	w := r.shadow + r.main
	r.write(w, "a.txt", replace("two", "TWO"))
	r.write(w, "new/c.txt", "c\n")
	os.Remove(w + "/dir/b.txt")
	os.Remove(w + "/dir")
	os.Chmod(w+"/run.sh", 0o644)
	os.Remove(w + "/link")
	os.Symlink("bin.dat", w+"/link")
	r.write(w, ".env", "SECRET=other\n")

	result := r.reconcile()

	r.expectFile(r.main+"/a.txt", replace("two", "TWO"))
	r.expectFile(r.main+"/new/c.txt", "c\n")
	r.expectFile(r.main+"/.env", "SECRET=other\n")
	r.expectMissing(r.main + "/dir")
	if info, _ := os.Lstat(r.main + "/run.sh"); info.Mode().Perm() != 0o644 {
		t.Errorf("run.sh has mode %v", info.Mode())
	}
	if target, _ := os.Readlink(r.main + "/link"); target != "bin.dat" {
		t.Errorf("link points to %q", target)
	}
	r.expectFile(r.main+"/node_modules/pkg/index.js", "x\n")
	if len(result.Aside) > 0 || result.Notice != "" {
		t.Errorf("kept aside %v, notice %q", result.Aside, result.Notice)
	}
	for _, p := range []string{"a.txt", "new", "new/c.txt", ".env", "dir", "dir/b.txt", "run.sh", "link"} {
		if !slices.Contains(result.Applied, filepath.Join(r.main, p)) {
			t.Errorf("applied %v, want %s among them", result.Applied, p)
		}
	}
}

func TestMainOnlyChangesAreKept(t *testing.T) {
	r := newRig(t)
	r.snapshot(r.main)
	r.write(r.main, "a.txt", replace("two", "main"))
	os.Remove(r.main + "/dir/b.txt")
	r.write(r.main, "main.txt", "m\n")

	result := r.reconcile()

	r.expectFile(r.main+"/a.txt", replace("two", "main"))
	r.expectMissing(r.main + "/dir/b.txt")
	r.expectFile(r.main+"/main.txt", "m\n")
	if len(result.Applied) > 0 || len(result.Aside) > 0 {
		t.Errorf("applied %v, kept aside %v", result.Applied, result.Aside)
	}
}

func TestChangesToDifferentLinesMerge(t *testing.T) {
	r := newRig(t)
	r.snapshot(r.main)
	r.write(r.main, "a.txt", replace("two", "main"))
	r.write(r.shadow+r.main, "a.txt", replace("nine", "worker"))

	result := r.reconcile()

	r.expectFile(r.main+"/a.txt", strings.Replace(replace("two", "main"), "nine\n", "worker\n", 1))
	if len(result.Aside) > 0 {
		t.Errorf("kept aside %v", result.Aside)
	}
}

func TestClashesKeepMainsVersionAndTheWorkersAside(t *testing.T) {
	r := newRig(t)
	r.write(r.main, "gone-on-worker.txt", "g\n")
	r.write(r.main, "gone-on-main.txt", "g\n")
	r.snapshot(r.main)
	w := r.shadow + r.main
	r.write(r.main, "a.txt", replace("two", "main"))
	r.write(w, "a.txt", replace("two", "worker"))
	r.write(r.main, "bin.dat", "\x00main\n")
	r.write(w, "bin.dat", "\x00worker\n")
	os.Remove(r.main + "/link")
	r.write(r.main, "link", "a file now\n")
	os.Remove(w + "/link")
	os.Symlink("run.sh", w+"/link")
	os.Remove(w + "/gone-on-worker.txt")
	r.write(r.main, "gone-on-worker.txt", "changed on main\n")
	os.Remove(r.main + "/gone-on-main.txt")
	r.write(w, "gone-on-main.txt", "changed on worker\n")
	r.write(r.main, "both.txt", "main\n")
	r.write(w, "both.txt", "worker\n")

	result := r.reconcile()

	r.expectFile(r.main+"/a.txt", replace("two", "main"))
	r.expectFile(r.main+"/bin.dat", "\x00main\n")
	r.expectFile(r.main+"/link", "a file now\n")
	r.expectFile(r.main+"/gone-on-worker.txt", "changed on main\n")
	r.expectMissing(r.main + "/gone-on-main.txt")
	r.expectFile(r.main+"/both.txt", "main\n")
	for _, name := range []string{"a.txt", "bin.dat", "link", "gone-on-worker.txt", "gone-on-main.txt", "both.txt"} {
		if _, ok := asideOf(result, filepath.Join(r.main, name)); !ok {
			t.Errorf("%s isn't kept aside: %v", name, result.Aside)
		}
		if !strings.Contains(result.Notice, filepath.Join(r.main, name)) {
			t.Errorf("the notice doesn't name %s:\n%s", name, result.Notice)
		}
	}
	if len(result.Applied) > 0 {
		t.Errorf("applied %v", result.Applied)
	}
	r.expectFile(r.recovery+"/worker"+r.main+"/a.txt", replace("two", "worker"))
	r.expectFile(r.recovery+"/main"+r.main+"/a.txt", replace("two", "main"))
	r.expectFile(r.recovery+"/base"+r.main+"/a.txt", lines)
	r.expectFile(r.recovery+"/worker"+r.main+"/gone-on-main.txt", "changed on worker\n")
	r.expectMissing(r.recovery + "/worker" + r.main + "/gone-on-worker.txt")
	r.expectFile(r.recovery+"/base"+r.main+"/gone-on-worker.txt", "g\n")
	if target, _ := os.Readlink(r.recovery + "/worker" + r.main + "/link"); target != "run.sh" {
		t.Errorf("the worker's link is kept aside pointing to %q", target)
	}
	if !strings.Contains(result.Notice, r.recovery) {
		t.Errorf("the notice doesn't say where the worker's versions are:\n%s", result.Notice)
	}
}

func TestRefs(t *testing.T) {
	r := newRig(t)
	for _, branch := range []string{"fast", "diverged", "deleted", "kept"} {
		r.git(r.main, "branch", branch)
	}
	r.snapshot(r.main)
	worker := func(args ...string) string { return r.worker(r.main, args...) }
	fast := r.commit(worker, "fast", "worker")
	diverged := r.commit(worker, "diverged", "worker")
	mainDiverged := r.commit(r.onMain, "diverged", "main")
	worker("update-ref", "refs/heads/new", fast)
	worker("tag", "-a", "-m", "tagged", "v1", fast)
	worker("branch", "-D", "deleted")
	worker("branch", "-D", "kept")
	mainKept := r.commit(r.onMain, "kept", "main")

	result := r.reconcile()

	if got := r.rev(r.main, "fast"); got != fast {
		t.Errorf("fast = %s, want the worker's %s", got, fast)
	}
	if got := r.rev(r.main, "new"); got != fast {
		t.Errorf("new = %s, want %s", got, fast)
	}
	if got := r.rev(r.main, "v1^{commit}"); got != fast {
		t.Errorf("v1 = %s, want %s", got, fast)
	}
	if got := r.rev(r.main, "deleted"); got != "" {
		t.Errorf("deleted = %s, want it gone", got)
	}
	if got := r.rev(r.main, "kept"); got != mainKept {
		t.Errorf("kept = %s, want main's %s", got, mainKept)
	}
	if got := r.rev(r.main, "diverged"); got != mainDiverged {
		t.Errorf("diverged = %s, want main's %s", got, mainDiverged)
	}
	recovery := "refs/gripi/returns/" + move + "/refs/heads/diverged"
	if got := r.rev(r.main, recovery); got != diverged {
		t.Errorf("%s = %s, want the worker's %s", recovery, got, diverged)
	}
	if !slices.Contains(result.Refs, recovery) || !strings.Contains(result.Notice, recovery) {
		t.Errorf("recovery refs %v, notice:\n%s", result.Refs, result.Notice)
	}
	if _, ok := asideOf(result, "refs/heads/kept"); !ok {
		t.Errorf("the worker's deletion of a branch that main changed isn't reported: %v", result.Aside)
	}
	run(t, r.main, nil, "git", "fsck", "--no-progress")
}

func TestACheckedOutBranchFollowsTheWorkerOnlyWhileMainIsClean(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		r := newRig(t)
		r.write(r.feat, "staged.txt", "s\n")
		r.git(r.feat, "add", "staged.txt")
		r.snapshot(r.feat)
		w := r.shadow + r.feat
		r.write(w, "a.txt", replace("two", "worker"))
		r.worker(r.feat, "commit", "-qam", "worker")
		r.write(w, "dir/b.txt", "staged on the worker\n")
		r.worker(r.feat, "add", "dir/b.txt")
		r.write(w, "untracked.txt", "u\n")
		tip := strings.TrimSpace(r.worker(r.feat, "rev-parse", "HEAD"))
		before := r.rev(r.feat, "HEAD")
		if dirty {
			r.write(r.feat, "main.txt", "m\n")
		}

		result := r.reconcile()

		r.expectFile(r.feat+"/a.txt", replace("two", "worker"))
		r.expectFile(r.feat+"/dir/b.txt", "staged on the worker\n")
		r.expectFile(r.feat+"/untracked.txt", "u\n")
		status := r.git(r.feat, "status", "--porcelain", "--untracked-files=all")
		if !dirty {
			if got := r.rev(r.feat, "HEAD"); got != tip {
				t.Errorf("clean: HEAD = %s, want the worker's %s", got, tip)
			}
			if want := "M  dir/b.txt\n?? untracked.txt\n"; status != want {
				t.Errorf("clean: git status =\n%s\nwant\n%s", status, want)
			}
			if len(result.Aside) > 0 {
				t.Errorf("clean: kept aside %v", result.Aside)
			}
			r.git(r.main, "fsck", "--no-progress")
			continue
		}
		if got := r.rev(r.feat, "HEAD"); got != before {
			t.Errorf("dirty: HEAD = %s, want main's %s", got, before)
		}
		if got := r.rev(r.main, "feat"); got != before {
			t.Errorf("dirty: feat = %s, want main's %s", got, before)
		}
		if want := " M a.txt\n M dir/b.txt\nA  staged.txt\n?? main.txt\n?? untracked.txt\n"; status != want {
			t.Errorf("dirty: git status =\n%s\nwant\n%s", status, want)
		}
		ref := "refs/gripi/returns/" + move + "/refs/heads/feat"
		if got := r.rev(r.main, ref); got != tip {
			t.Errorf("dirty: %s = %s, want the worker's %s", ref, got, tip)
		}
		if _, ok := asideOf(result, r.feat); !ok || !strings.Contains(result.Notice, r.feat) {
			t.Errorf("dirty: the worker's HEAD and index aren't reported: %v\n%s", result.Aside, result.Notice)
		}
	}
}

func TestAWorktreeMadeOnTheWorkerIsMadeOnMain(t *testing.T) {
	r := newRig(t)
	r.snapshot(r.main)
	made := filepath.Join(filepath.Dir(r.feat), "made")
	r.workerWorktree(made, "made")
	w := r.shadow + made
	r.write(w, "a.txt", replace("two", "worker"))
	r.write(w, "staged.txt", "s\n")
	run(t, w, []string{"GIT_DIR=" + r.shadow + r.gitDir + "/worktrees/made", "GIT_WORK_TREE=" + w}, "git", "add", "staged.txt")
	r.write(w, "untracked.txt", "u\n")

	result := r.reconcile()

	r.expectFile(made+"/a.txt", replace("two", "worker"))
	r.expectFile(made+"/untracked.txt", "u\n")
	if got := r.git(made, "status", "--porcelain", "--untracked-files=all"); got != " M a.txt\nA  staged.txt\n?? untracked.txt\n" {
		t.Errorf("git status in the new worktree =\n%s", got)
	}
	if got := strings.TrimSpace(r.git(made, "symbolic-ref", "HEAD")); got != "refs/heads/made" {
		t.Errorf("the new worktree is on %s", got)
	}
	if !strings.Contains(r.git(r.main, "worktree", "list"), made) {
		t.Errorf("main doesn't list the worktree")
	}
	r.git(r.main, "fsck", "--no-progress") // with the staged blob, which only the worker had
	if len(result.Aside) > 0 {
		t.Errorf("kept aside %v", result.Aside)
	}
}

func TestAWorktreeMadeOnTheWorkerAtATakenPathIsLeftAlone(t *testing.T) {
	r := newRig(t)
	r.snapshot(r.main)
	made := filepath.Join(filepath.Dir(r.feat), "made")
	r.workerWorktree(made, "made")
	r.write(r.shadow+made, "untracked.txt", "u\n")
	r.write(made, "mine.txt", "main's\n")

	result := r.reconcile()

	if entries, _ := os.ReadDir(made); len(entries) != 1 {
		t.Errorf("the taken folder has %v", entries)
	}
	if strings.Contains(r.git(r.main, "worktree", "list"), made) {
		t.Errorf("main lists the worktree")
	}
	r.expectFile(r.recovery+"/worker"+made+"/untracked.txt", "u\n")
	if _, ok := asideOf(result, made); !ok || !strings.Contains(result.Notice, made) {
		t.Errorf("the worktree isn't reported: %v\n%s", result.Aside, result.Notice)
	}
	if got := r.rev(r.main, "made"); got == "" {
		t.Errorf("the worktree's branch didn't come back")
	}
}

func TestStashesMadeOnTheWorkerAreAdded(t *testing.T) {
	r := newRig(t)
	r.write(r.main, "a.txt", replace("two", "kept"))
	r.git(r.main, "stash", "push", "-q", "-m", "kept")
	r.snapshot(r.main)
	r.write(r.shadow+r.main, "a.txt", replace("two", "stashed"))
	r.worker(r.main, "stash", "push", "-q", "-m", "on the worker")

	result := r.reconcile()

	r.expectFile(r.main+"/a.txt", lines)
	if got := r.git(r.main, "stash", "list", "--format=%gs"); got != "On main: on the worker\nOn main: kept\n" {
		t.Errorf("stash list =\n%s", got)
	}
	if got := r.git(r.main, "stash", "show", "-p", "stash@{0}"); !strings.Contains(got, "+stashed") {
		t.Errorf("stash@{0} =\n%s", got)
	}
	if len(result.Applied) > 0 || len(result.Aside) > 0 {
		t.Errorf("applied %v, kept aside %v", result.Applied, result.Aside)
	}
}

func TestTheBaseOutlivesGarbageCollection(t *testing.T) {
	r := newRig(t)
	r.write(r.main, "untracked.txt", "only in the base\n")
	r.snapshot(r.main)
	r.write(r.main, "untracked.txt", "main\n")
	r.write(r.shadow+r.main, "untracked.txt", "worker\n")
	r.git(r.main, "gc", "-q", "--prune=now")

	result := r.reconcile()

	r.expectFile(r.recovery+"/base"+r.main+"/untracked.txt", "only in the base\n")
	if _, ok := asideOf(result, r.main+"/untracked.txt"); !ok {
		t.Errorf("kept aside %v", result.Aside)
	}
}

func TestAnInterruptedReconcileFinishesWhenRunAgain(t *testing.T) {
	r := newRig(t)
	r.git(r.main, "branch", "fast")
	r.snapshot(r.main)
	w := r.shadow + r.main
	for _, name := range []string{"c.txt", "d.txt", "e.txt"} {
		r.write(w, name, name+"\n")
	}
	os.Remove(w + "/dir/b.txt")
	r.write(r.main, "a.txt", replace("two", "main"))
	r.write(w, "a.txt", replace("two", "worker"))
	tip := r.commit(func(args ...string) string { return r.worker(r.main, args...) }, "fast", "worker")

	steps := 0
	interrupt = func() error {
		if steps++; steps == 3 {
			return errors.New("interrupted")
		}
		return nil
	}
	_, err := Reconcile(context.Background(), r.base, r.shadow, r.recovery)
	interrupt = nil
	if err == nil {
		t.Fatal("the interrupted reconcile succeeded")
	}
	result := r.reconcile()

	for _, name := range []string{"c.txt", "d.txt", "e.txt"} {
		r.expectFile(r.main+"/"+name, name+"\n")
	}
	r.expectMissing(r.main + "/dir/b.txt")
	r.expectFile(r.main+"/a.txt", replace("two", "main"))
	if got := r.rev(r.main, "fast"); got != tip {
		t.Errorf("fast = %s, want %s", got, tip)
	}
	if _, ok := asideOf(result, r.main+"/a.txt"); !ok || len(result.Aside) != 1 {
		t.Errorf("kept aside %v", result.Aside)
	}
	if !slices.Contains(result.Applied, r.main+"/c.txt") || !slices.Contains(result.Applied, r.main+"/e.txt") {
		t.Errorf("applied %v", result.Applied)
	}
	if again := r.reconcile(); !slices.Equal(again.Applied, result.Applied) || again.Notice != result.Notice {
		t.Errorf("a finished reconcile ran again: %v, %q", again, again.Notice)
	}
}

func TestUnselectedWorktreesAreUntouched(t *testing.T) {
	r := newRig(t)
	r.snapshot(r.main)
	tip := r.commit(func(args ...string) string { return r.worker(r.main, args...) }, "other", "worker")
	before := r.rev(r.other, "HEAD")

	result := r.reconcile()

	r.expectFile(r.other+"/a.txt", lines)
	if got := r.rev(r.other, "HEAD"); got != before {
		t.Errorf("the unselected worktree's HEAD = %s, want %s", got, before)
	}
	if !strings.Contains(r.git(r.main, "worktree", "list"), r.other) {
		t.Errorf("main lost the unselected worktree")
	}
	if got := r.rev(r.main, "refs/gripi/returns/"+move+"/refs/heads/other"); got != tip {
		t.Errorf("the worker's other = %s, want %s", got, tip)
	}
	if _, ok := asideOf(result, "refs/heads/other"); !ok {
		t.Errorf("kept aside %v", result.Aside)
	}
}

func TestTheWorkersConfigIsRecordedNotApplied(t *testing.T) {
	r := newRig(t)
	r.snapshot(r.main)
	config := r.read(r.gitDir + "/config")
	r.worker(r.main, "config", "user.name", "worker")

	result := r.reconcile()

	r.expectFile(r.gitDir+"/config", config)
	if got := r.read(r.recovery + "/worker" + r.gitDir + "/config"); !strings.Contains(got, "worker") {
		t.Errorf("the worker's config kept aside:\n%s", got)
	}
	if _, ok := asideOf(result, r.gitDir+"/config"); !ok {
		t.Errorf("kept aside %v", result.Aside)
	}
}

func TestDiscardDropsTheRecoveryRecordAndItsRefs(t *testing.T) {
	r := newRig(t)
	r.git(r.main, "branch", "diverged")
	r.snapshot(r.main)
	r.commit(func(args ...string) string { return r.worker(r.main, args...) }, "diverged", "worker")
	r.commit(r.onMain, "diverged", "main")
	if result := r.reconcile(); len(result.Refs) == 0 {
		t.Fatal("no recovery refs")
	}

	if err := Discard(context.Background(), r.recovery); err != nil {
		t.Fatal(err)
	}

	if got := r.git(r.main, "for-each-ref", "refs/gripi/"); got != "" {
		t.Errorf("refs left:\n%s", got)
	}
	r.expectMissing(r.recovery)
}

func TestSnapshotRefusesWhatAMoveCantBringBack(t *testing.T) {
	r := newRig(t)
	r.write(r.feat, "a.txt", replace("two", "feat"))
	r.git(r.feat, "commit", "-qam", "feat")
	r.write(r.main, "a.txt", replace("two", "main"))
	r.git(r.main, "commit", "-qam", "main")
	if out, err := exec.Command("git", "-C", r.main, "merge", "-q", "feat").CombinedOutput(); err == nil {
		t.Fatalf("the merge didn't stop: %s", out)
	}
	if _, err := Snapshot(context.Background(), move, Selection{Cwd: r.main, GitDir: r.gitDir, Roots: []string{r.main}}, r.base, r.shadow); err == nil || !strings.Contains(err.Error(), "merge") {
		t.Errorf("a merge in progress: %v", err)
	}

	r = newRig(t)
	r.git(r.feat, "update-index", "--add", "--cacheinfo", "160000,"+r.rev(r.main, "HEAD")+",sub")
	if _, err := Snapshot(context.Background(), move, Selection{Cwd: r.feat, GitDir: r.gitDir, Roots: []string{r.feat}}, r.base, r.shadow); err == nil || !strings.Contains(err.Error(), "submodules") {
		t.Errorf("a submodule: %v", err)
	}
	if _, err := Snapshot(context.Background(), move, Selection{Cwd: r.main, GitDir: r.gitDir, Roots: []string{r.feat}}, r.base, r.shadow); err == nil {
		t.Errorf("a cwd outside the selected worktrees was taken")
	}
}

func TestAnUnselectedWorktreeInASelectedOneStaysOut(t *testing.T) {
	r := newRig(t)
	nested := filepath.Join(r.main, "nested")
	r.git(r.main, "worktree", "add", "-q", "-b", "nested", nested)
	r.write(nested, "mine.txt", "m\n")
	r.snapshot(r.main)
	r.expectMissing(r.shadow + nested)
	r.write(r.shadow+r.main, "a.txt", replace("two", "worker"))

	result := r.reconcile()

	r.expectFile(r.main+"/a.txt", replace("two", "worker"))
	r.expectFile(nested+"/mine.txt", "m\n")
	r.expectFile(nested+"/a.txt", lines)
	if len(result.Aside) > 0 {
		t.Errorf("kept aside %v", result.Aside)
	}
}

func TestAWorktreeRemovedOnTheWorkerStaysOnMain(t *testing.T) {
	r := newRig(t)
	r.snapshot(r.main, r.feat)
	// As git worktree remove on the worker.
	os.RemoveAll(r.shadow + r.gitDirOf(r.feat))
	os.RemoveAll(r.shadow + r.feat)

	result := r.reconcile()

	r.expectFile(r.feat+"/a.txt", lines)
	if !strings.Contains(r.git(r.main, "worktree", "list"), r.feat) {
		t.Errorf("main lost the worktree")
	}
	if _, ok := asideOf(result, r.feat); !ok {
		t.Errorf("kept aside %v", result.Aside)
	}
}
