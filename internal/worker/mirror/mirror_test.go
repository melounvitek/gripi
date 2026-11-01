//go:build linux

package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Set GRIPI_MIRROR_STRESS=1 for the long variants.
var stress = os.Getenv("GRIPI_MIRROR_STRESS") != ""

func TestMain(m *testing.M) {
	// Run from a Git hook or git rebase --exec, Git's variables would point the tests' git commands at that
	// repository instead of their own.
	for _, variable := range os.Environ() {
		if name, _, _ := strings.Cut(variable, "="); strings.HasPrefix(name, "GIT_") {
			os.Unsetenv(name)
		}
	}
	home, err := os.MkdirTemp("", "gripi-mirror-home-")
	if err != nil {
		panic(err)
	}
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "XDG_CONFIG_HOME": home,
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		os.Setenv(k, v)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

func TestMirrorsGitOperations(t *testing.T) {
	unit := newRepo(t)
	r := newRig(t, unit)
	main, featA, featB := unit.Main, unit.Worktrees[0], unit.Worktrees[1]
	r.verify(t)
	for _, p := range []string{".env", "log/dev.log", "tmp/.keep", "link1"} {
		if _, err := os.Lstat(r.dst + filepath.Join(main, p)); err != nil {
			t.Errorf("small ignored or tracked file missing on main: %v", err)
		}
	}
	for _, p := range []string{"node_modules", "tmp/cache", "dump.bigdump"} {
		if _, err := os.Lstat(r.dst + filepath.Join(main, p)); err == nil {
			t.Errorf("%s was mirrored", p)
		}
	}

	steps := []struct {
		name string
		do   func(t *testing.T)
	}{
		{"commits in the main folder and a worktree", func(t *testing.T) {
			writeFile(t, main, "src/d1/f1.txt", "main change\n")
			git(t, main, "commit", "-qam", "main change")
			writeFile(t, featA, "src/d2/f2.txt", "feature change\n")
			git(t, featA, "commit", "-qam", "feature change")
		}},
		{"rebase", func(t *testing.T) {
			for i := range 3 {
				writeFile(t, featA, fmt.Sprintf("src/d3/rebase%d.txt", i), "x\n")
				git(t, featA, "add", "-A")
				git(t, featA, "commit", "-qm", "feature "+strconv.Itoa(i))
			}
			writeFile(t, main, "src/d4/f1.txt", "moves main on\n")
			git(t, main, "commit", "-qam", "main moves on")
			git(t, featA, "rebase", "-q", "main")
		}},
		{"gc and repack", func(t *testing.T) {
			git(t, main, "gc", "-q", "--prune=now")
			writeFile(t, main, "src/d5/f1.txt", "after gc\n")
			git(t, main, "commit", "-qam", "after gc")
			git(t, main, "repack", "-q", "-a", "-d", "-f")
		}},
		{"checkout of many files", func(t *testing.T) {
			git(t, featB, "checkout", "-q", "-b", "many")
			for i := range 1500 {
				writeFile(t, featB, fmt.Sprintf("src/many/d%d/f%d.txt", i/50, i), fmt.Sprintf("file %d\n", i))
			}
			git(t, featB, "add", "-A")
			git(t, featB, "commit", "-qm", "many files")
			git(t, featB, "checkout", "-q", "feat-b")
			r.verify(t)
			git(t, featB, "checkout", "-q", "many")
		}},
		{"renames, folder moves and deletes", func(t *testing.T) {
			git(t, main, "mv", "src/d6", "src/d6-moved")
			mustRun(t, main, "mv", "src/d7", "src/d7-plain")
			mustRun(t, main, "mv", "src/d8/f1.txt", "src/d8/renamed.txt")
			mustRun(t, main, "rm", "src/d9/f2.txt")
			mustRun(t, main, "rm", "-rf", "src/d10")
			mustRun(t, main, "mv", "src/d11", "../outside")
			writeFile(t, filepath.Dir(main), "elsewhere/new.txt", "moved in\n")
			mustRun(t, main, "mv", "../elsewhere", "src/moved-in")
			mustRun(t, main, "mv", "src/d12", "node_modules/d12")
			mustRun(t, main, "mv", "node_modules/pkg", "src/pkg-back")
		}},
		{"editor-style atomic saves", func(t *testing.T) {
			for i := range 50 {
				writeFile(t, main, "src/d13/.f1.txt.swp", fmt.Sprintf("save %d\n", i))
				mustRun(t, main, "mv", "src/d13/.f1.txt.swp", "src/d13/f1.txt")
			}
		}},
		{"worktree add and remove", func(t *testing.T) {
			featC := filepath.Join(filepath.Dir(featA), "feat-c")
			git(t, main, "worktree", "add", "-q", "-b", "feat-c", featC)
			writeFile(t, featC, "src/d14/f1.txt", "in feat-c\n")
			git(t, featC, "commit", "-qam", "feat-c change")
			r.verify(t)
			if !r.src.Contains(filepath.Join(featC, "src")) {
				t.Error("the source doesn't count the new worktree as the unit's")
			}
			git(t, main, "worktree", "remove", "--force", featB)
			r.verify(t)
			if r.src.Contains(featB) {
				t.Error("the source counts the removed worktree as the unit's")
			}
			if _, err := os.Lstat(r.dst + featB); err == nil {
				t.Errorf("the removed worktree is still on main")
			}
		}},
		{"ignore rules change", func(t *testing.T) {
			appendFile(t, main, ".gitignore", "ignored-now/\n")
			writeFile(t, main, "ignored-now/a.txt", "ignored\n")
			writeFile(t, main, "tmp/new/cache.bin", "ignored folder\n")
			writeFile(t, main, ".env", "SECRET=changed\n")
			git(t, main, "add", "-f", "node_modules/d12/f1.txt") // a tracked file inside an ignored folder
			git(t, main, "add", "-f", "dump.bigdump")            // a big ignored file, tracked now
		}},
		{"big ignored file created, then tracked", func(t *testing.T) {
			writeFile(t, main, "live.bigdump", strings.Repeat("y", 11<<20))
			r.barrier(t)
			git(t, main, "add", "-f", "live.bigdump")
		}},
		{"pack sidecar edited in place", func(t *testing.T) {
			git(t, main, "repack", "-q", "-a", "-d")
			packs, _ := filepath.Glob(filepath.Join(unit.GitDir, "objects/pack/*.pack"))
			keep := strings.TrimSuffix(packs[0], ".pack") + ".keep"
			writeFile(t, "/", keep, "aaaa\n")
			r.barrier(t)
			writeFile(t, "/", keep, "bbbb\n")
		}},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.do(t)
			r.verify(t)
		})
	}
}

func TestParallelCommitLoops(t *testing.T) {
	unit := newRepo(t)
	r := newRig(t, unit)
	barriers := 8
	if stress {
		barriers = 100
	}
	stop := filepath.Join(t.TempDir(), "stop")
	var loops []*exec.Cmd
	for i, dir := range append([]string{unit.Main}, unit.Worktrees...) {
		cmd := exec.Command("sh", "-c", fmt.Sprintf(
			`i=0; while [ ! -e %s ]; do i=$((i+1)); echo $i > loop%d.txt; git add loop%d.txt && git commit -qm "loop $i" || exit 1; done`, stop, i, i))
		cmd.Dir = dir
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		loops = append(loops, cmd)
	}
	for range barriers {
		r.barrier(t)
		fsck(t, r.dst+unit.GitDir)
	}
	writeFile(t, filepath.Dir(stop), "stop", "")
	for _, cmd := range loops {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("commit loop: %v", err)
		}
	}
	r.verify(t)
}

func TestOverflowRescans(t *testing.T) {
	unit := newRepo(t)
	var stalled atomic.Bool
	release := make(chan struct{})
	r := newRigWith(t, unit, func(s *Source) {
		s.beforeRead = func() {
			if stalled.Load() {
				<-release
			}
		}
	})
	stalled.Store(true)
	writeFile(t, unit.Main, "src/wake.txt", "x") // the reader stops after this event
	time.Sleep(100 * time.Millisecond)
	for i := range 8000 {
		writeFile(t, unit.Main, fmt.Sprintf("src/d%d/flood%d.txt", 3+i%28, i), "flood\n") // watched folders
	}
	mustRun(t, unit.Main, "rm", "-rf", "src/d1")
	mustRun(t, unit.Main, "mv", "src/d2", "src/d2-moved")
	git(t, unit.Main, "worktree", "add", "-q", "-b", "lost", filepath.Join(filepath.Dir(unit.Worktrees[0]), "lost"))
	stalled.Store(false)
	close(release)
	r.verify(t)
	if r.src.overflows.Load() == 0 {
		t.Fatal("the kernel's event queue didn't overflow")
	}
}

func TestReconnectAfterInterruptedStream(t *testing.T) {
	unit := newRepo(t)
	r := newRig(t, unit)
	writeFile(t, unit.Main, "src/d1/f1.txt", "before the barrier\n")
	git(t, unit.Main, "commit", "-qam", "before")
	n := r.barrier(t)
	atBarrier := destinationManifest(t, unit, r.dst)

	writeFile(t, unit.Main, "src/d2/f1.txt", "after the barrier\n")
	git(t, unit.Main, "commit", "-qam", "after")
	waitFor(t, "changes staged on main", func() bool {
		staged, _ := os.ReadDir(filepath.Join(r.state, "staging"))
		return len(staged) >= 3
	})
	r.conn.Close()
	if err := <-r.served; err == nil || !strings.Contains(err.Error(), "saved in") {
		t.Fatalf("Serve after the interruption: %v", err)
	}
	<-r.ran
	if got := destinationManifest(t, unit, r.dst); !slices.Equal(got, atBarrier) {
		t.Fatalf("main changed after its last barrier:\n%s", lineDiff(atBarrier, got))
	}
	fsck(t, r.dst+unit.GitDir)
	if cp := r.d.Checkpoint(); cp.Barrier != n {
		t.Fatalf("checkpoint is barrier %d, want %d", cp.Barrier, n)
	}
	aside := filepath.Join(r.dst, "aside", "1")
	changes, err := os.ReadFile(filepath.Join(aside, "changes.json"))
	if err != nil || !strings.Contains(string(changes), filepath.Join(unit.Main, "src/d2/f1.txt")) {
		t.Fatalf("changes saved aside: %v\n%s", err, changes)
	}

	writeFile(t, unit.Main, "src/d3/f1.txt", "while disconnected\n")
	mustRun(t, unit.Main, "rm", "-r", "src/d4")
	git(t, unit.Main, "commit", "-qam", "disconnected")
	git(t, unit.Main, "worktree", "remove", unit.Worktrees[1])
	mustRun(t, unit.Main, "rm", ".env")
	r.start(t, nil)
	r.verify(t)
	for _, p := range []string{unit.Worktrees[1], filepath.Join(unit.Main, ".env")} {
		if _, err := os.Lstat(r.dst + p); err == nil {
			t.Errorf("%s, deleted while disconnected, is still on main", p)
		}
	}
}

func TestStagingKeepsOnlyTheNewestCopy(t *testing.T) {
	unit := newRepo(t)
	r := newRig(t, unit)
	staged := func() []string {
		var contents []string
		entries, _ := os.ReadDir(filepath.Join(r.state, "staging"))
		for _, e := range entries {
			data, _ := os.ReadFile(filepath.Join(r.state, "staging", e.Name()))
			contents = append(contents, string(data))
		}
		return contents
	}
	for i := range 5 {
		writeFile(t, unit.Main, "src/d1/f1.txt", fmt.Sprintf("version %d\n", i))
		v := fmt.Sprintf("version %d\n", i)
		waitFor(t, "the copy staged", func() bool { return slices.Contains(staged(), v) })
	}
	if got := staged(); len(got) != 1 {
		t.Fatalf("staged copies: %q", got)
	}
	r.barrier(t)
	if data, _ := os.ReadFile(r.dst + filepath.Join(unit.Main, "src/d1/f1.txt")); string(data) != "version 4\n" {
		t.Fatalf("main has %q", data)
	}
}

func TestJournalFinishesAfterCrashMidApply(t *testing.T) {
	unit := newRepo(t)
	r := newRig(t, unit)
	main := unit.Main
	// Replaying the barrier from the start would delete the recreated file.
	mustRun(t, main, "rm", "src/d1/f1.txt")
	time.Sleep(500 * time.Millisecond) // sent once quiet for 50 ms
	writeFile(t, main, "src/d1/f1.txt", "recreated\n")
	for _, d := range []string{"d2", "d5", "d9"} {
		writeFile(t, main, "src/"+d+"/f1.txt", "changed in "+d+"\n")
	}
	git(t, main, "commit", "-qam", "crash")
	waitFor(t, "changes staged", func() bool { return stagedCount(r) >= 5 })

	stuck := r.dst + filepath.Join(main, "src/d5")
	os.Chmod(stuck, 0o500) // applying fails halfway, as if main crashed there
	t.Cleanup(func() { os.Chmod(stuck, 0o755) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, _, err := r.src.Barrier(ctx)
	if err == nil {
		t.Fatalf("barrier %d applied despite the read-only folder", n)
	}
	<-r.served
	<-r.ran
	if _, err := os.Stat(filepath.Join(r.state, "journal.json")); err != nil {
		t.Fatalf("no journal left: %v", err)
	}
	before := r.d.Checkpoint().Barrier

	os.Chmod(stuck, 0o755)
	d, err := NewMovedDestination(r.state, "/", r.dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Checkpoint().Barrier; got != before+1 {
		t.Fatalf("checkpoint after recovery is barrier %d, want %d", got, before+1)
	}
	checkCopy(t, unit, r.dst)

	// The last barrier's markers stay behind, and mustn't skip changes of the next one.
	for i := range 50 {
		os.MkdirAll(filepath.Join(r.state, "done", strconv.FormatUint(before+1, 10), strconv.Itoa(i)), 0o700)
	}
	mustRun(t, main, "rm", "-r", "src/d3")
	writeFile(t, main, "src/d6/new.txt", "after the crash\n")
	r.d = d
	r.start(t, nil)
	r.verify(t)
}

func TestBarrierFailsWhenMainCantDelete(t *testing.T) {
	unit := newRepo(t)
	r := newRig(t, unit)
	writeFile(t, unit.Main, "src/d7/sub/f.txt", "x\n")
	r.barrier(t)
	locked := r.dst + filepath.Join(unit.Main, "src/d7")
	os.Chmod(locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	mustRun(t, unit.Main, "rm", "src/d7/sub/f.txt") // below a folder main can't read
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if n, _, err := r.src.Barrier(ctx); err == nil {
		t.Fatalf("barrier %d applied a deletion main couldn't make", n)
	}
}

// A file the worker can't read hasn't disappeared: main must keep its copy, and barriers must fail until the file
// can be read again.
func TestBarriersFailWhileTheWorkerCantReadAFile(t *testing.T) {
	unit := newRepo(t)
	r := newRig(t, unit)
	file := filepath.Join(unit.Main, "src/d7/f1.txt")
	before, err := os.ReadFile(r.dst + file)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, "/", file, "changed, then unreadable\n")
	os.Chmod(file, 0)
	t.Cleanup(func() { os.Chmod(file, 0o644) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if n, _, err := r.src.Barrier(ctx); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("barrier %d with an unreadable file: %v", n, err)
	}
	if r.src.Healthy() {
		t.Error("the source is healthy while it can't read a file")
	}
	if data, _ := os.ReadFile(r.dst + file); !slices.Equal(data, before) {
		t.Fatalf("main has %q", data)
	}

	os.Chmod(file, 0o644)
	waitFor(t, "the source to recover", r.src.Healthy)
	r.verify(t)
}

func TestAFolderTheWorkerCantReadIsntDeletedOnMain(t *testing.T) {
	unit := newRepo(t)
	r := newRig(t, unit)
	r.conn.Close()
	<-r.served
	<-r.ran
	folder := filepath.Join(unit.Main, "src/d7")
	os.Chmod(folder, 0)
	t.Cleanup(func() { os.Chmod(folder, 0o755) })
	r.launch(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if n, _, err := r.src.Barrier(ctx); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("barrier %d with an unreadable folder: %v", n, err)
	}
	if r.src.Healthy() {
		t.Error("the source is healthy while it can't read a folder")
	}
	if _, err := os.Stat(r.dst + filepath.Join(folder, "f1.txt")); err != nil {
		t.Fatalf("a file in the unreadable folder on main: %v", err)
	}

	os.Chmod(folder, 0o755)
	waitFor(t, "the source to recover", r.src.Healthy)
	r.verify(t)
}

// A folder of the unit that the source doesn't see, such as one that the worker's private view lacks, isn't empty: the
// source stops rather than delete main's copy of it.
func TestASourceStopsOnAFolderOfTheUnitThatIsNotThere(t *testing.T) {
	unit := newRepo(t)
	r := newRig(t, unit)
	r.conn.Close()
	<-r.served
	<-r.ran
	before := destinationManifest(t, unit, r.dst)
	for _, folder := range []string{unit.Main, unit.Worktrees[0]} {
		hidden := folder + ".hidden"
		if err := os.Rename(folder, hidden); err != nil {
			t.Fatal(err)
		}
		if folder != unit.Main {
			// Not a folder either, which Git still has as a worktree.
			writeFile(t, "/", folder, "a file\n")
		}
		r.launch(t, nil)
		select {
		case err := <-r.ran:
			if err == nil || !strings.Contains(err.Error(), folder) {
				t.Fatalf("Run without %s = %v", folder, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("the source runs without %s", folder)
		}
		if err := <-r.served; err == nil || !strings.Contains(err.Error(), folder) {
			t.Fatalf("Serve without %s on the worker = %v", folder, err)
		}
		if _, _, err := r.src.Barrier(context.Background()); err == nil || !strings.Contains(err.Error(), folder) {
			t.Fatalf("Barrier without %s = %v", folder, err)
		}
		if r.src.Healthy() {
			t.Fatalf("the source is healthy without %s", folder)
		}
		os.Remove(folder)
		if err := os.Rename(hidden, folder); err != nil {
			t.Fatal(err)
		}
		if got := destinationManifest(t, unit, r.dst); !slices.Equal(got, before) {
			t.Fatalf("main's copy changed without %s on the worker:\n%s", folder, lineDiff(before, got))
		}
	}
	r.start(t, nil)
	r.verify(t)
}

// A worktree whose folder goes while the source runs goes on main too, also when the source rescans later, though Git
// still lists it until it is pruned.
func TestAWorktreeDeletedWhileMirroringGoesOnMainAlsoAfterARescan(t *testing.T) {
	unit := newRepo(t)
	r := newRig(t, unit)
	featB := unit.Worktrees[1]
	if err := os.RemoveAll(featB); err != nil {
		t.Fatal(err)
	}
	r.verify(t)
	appendFile(t, unit.Main, ".gitignore", "ignored-now/\n") // which makes the source rescan
	r.verify(t)
	if _, err := os.Lstat(r.dst + featB); err == nil {
		t.Error("the deleted worktree is still on main")
	}
}

// A folder outside Git, such as Pi's session folders, has no worktrees, also when it is in another repository.
func TestAFolderInAnotherRepositoryKeepsToItselfWhenTheSourceRescans(t *testing.T) {
	other := newRepo(t)
	folder := filepath.Join(other.Main, "sessions")
	writeFile(t, folder, "s.jsonl", "{}\n")
	r := newRig(t, Unit{Main: folder})
	writeFile(t, folder, ".gitignore", "*.tmp\n") // which makes the source rescan
	r.barrier(t)
	if !r.src.Contains(folder) || r.src.Contains(other.Worktrees[0]) {
		t.Fatal("the source took the other repository's worktrees for its folders")
	}
	for _, p := range []string{filepath.Join(folder, "s.jsonl"), filepath.Join(folder, ".gitignore")} {
		if _, err := os.Lstat(r.dst + p); err != nil {
			t.Errorf("main's copy of %s: %v", p, err)
		}
	}
	if _, err := os.Lstat(r.dst + other.Worktrees[0]); err == nil {
		t.Error("main got a worktree of the other repository")
	}
}

func TestSyncFailsOnAFileItCantRead(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, src, "readable.txt", "x\n")
	writeFile(t, src, "unreadable.txt", "x\n")
	os.Chmod(filepath.Join(src, "unreadable.txt"), 0)
	d, err := NewMovedDestination(filepath.Join(dst, "state"), "/", dst)
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	served := make(chan error, 1)
	go func() { served <- d.Serve(context.Background(), b, filepath.Join(dst, "aside")) }()
	err = Sync(context.Background(), a, Unit{Main: src}, Options{})
	<-served
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Sync with an unreadable file = %v", err)
	}
}

func TestDeltaTransferForRepackedPack(t *testing.T) {
	unit := newRepo(t)
	for i := range 40 {
		writeFile(t, unit.Main, fmt.Sprintf("data/f%d.txt", i), randomText(i+1000, 100_000))
	}
	git(t, unit.Main, "add", "-A")
	git(t, unit.Main, "commit", "-qm", "data")
	git(t, unit.Main, "repack", "-q", "-a", "-d")
	r := newRig(t, unit)

	before := r.written.Load()
	writeFile(t, unit.Main, "data/new.txt", "new\n")
	git(t, unit.Main, "add", "-A")
	git(t, unit.Main, "commit", "-qm", "more")
	git(t, unit.Main, "repack", "-q", "-a", "-d", "-f")
	r.verify(t)
	packs, _ := filepath.Glob(filepath.Join(unit.GitDir, "objects/pack/*.pack"))
	if len(packs) != 1 {
		t.Fatalf("packs: %v", packs)
	}
	info, err := os.Stat(packs[0])
	if err != nil {
		t.Fatal(err)
	}
	if sent := r.written.Load() - before; sent > info.Size()/4 {
		t.Fatalf("sent %d bytes for a repacked %d-byte pack", sent, info.Size())
	}
}

func TestBarrierReportsFilesStillBeingWritten(t *testing.T) {
	unit := newRepo(t)
	r := newRig(t, unit)
	p := filepath.Join(unit.Main, "big.bin")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Write(make([]byte, 9<<20))
	_, writing, err := r.src.Barrier(context.Background())
	if err != nil || !slices.Equal(writing, []string{p}) {
		t.Fatalf("barrier: %v, still being written: %v", err, writing)
	}
	if _, err := os.Lstat(r.dst + p); err == nil {
		t.Fatal("a file still being written reached main")
	}
	f.Write(make([]byte, 1<<20))
	f.Close()
	r.verify(t)
}

func TestAutoBarrierAppliesChangesWithoutBeingAsked(t *testing.T) {
	folder := t.TempDir()
	r := newRigWith(t, Unit{Main: folder}, func(s *Source) { s.opts.AutoBarrier = true })
	first := r.d.Checkpoint().Barrier
	started := time.Now()
	writeFile(t, folder, "session.jsonl", "{\"id\":\"1\"}\n")
	waitFor(t, "main to have the file", func() bool {
		data, _ := os.ReadFile(r.dst + filepath.Join(folder, "session.jsonl"))
		return string(data) == "{\"id\":\"1\"}\n"
	})
	t.Logf("applied after %s", time.Since(started).Round(time.Millisecond))
	if r.d.Checkpoint().Barrier == first {
		t.Fatal("no barrier applied the change")
	}
	time.Sleep(200 * time.Millisecond)
	settled := r.d.Checkpoint().Barrier
	time.Sleep(300 * time.Millisecond)
	if last := r.d.Checkpoint().Barrier; last != settled {
		t.Fatalf("barriers %d to %d followed without changes", settled, last)
	}
}

func TestSyncCopiesTheUnitOnce(t *testing.T) {
	unit := newRepo(t)
	dst := t.TempDir()
	d, err := NewMovedDestination(filepath.Join(dst, "state"), "/", dst)
	if err != nil {
		t.Fatal(err)
	}
	sync := func() int64 {
		a, b := net.Pipe()
		served := make(chan error, 1)
		go func() { served <- d.Serve(context.Background(), b, filepath.Join(dst, "aside")) }()
		var written atomic.Int64
		if err := Sync(context.Background(), &countingConn{a, &written}, unit, Options{}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if err := <-served; err != nil {
			t.Fatalf("Serve: %v", err)
		}
		return written.Load()
	}
	sync()
	checkCopy(t, unit, dst)

	writeFile(t, unit.Main, "src/d1/f1.txt", "changed\n")
	mustRun(t, unit.Main, "rm", "-r", "src/d2")
	git(t, unit.Main, "commit", "-qam", "change")
	if sent := sync(); sent > 200_000 {
		t.Fatalf("the second sync sent %d bytes", sent)
	}
	checkCopy(t, unit, dst)
}

// Pi's agent folder often links to the user's own configuration, which the worker has only if it gets what the
// links point to; links within the folder, such as npm's, must stay links.
func TestSyncCopiesAFolderElsewhereWithWhatItsOutsideLinksPointTo(t *testing.T) {
	src, outside, dst := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "copy")
	writeFile(t, src, "sub/b.txt", "b\n")
	writeFile(t, src, "excluded.txt", "main's\n")
	writeFile(t, outside, "settings.json", "{}\n")
	writeFile(t, outside, "skills/s/SKILL.md", "skill\n")
	for link, target := range map[string]string{
		"inside": "sub/b.txt", "settings.json": filepath.Join(outside, "settings.json"), "skills": filepath.Join(outside, "skills"),
		"dangling": "/nonexistent", "skills/s/loop": "../..",
	} {
		dir := src
		if strings.HasPrefix(link, "skills/") {
			dir = outside
		}
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, dst, "excluded.txt", "the worker's\n")
	writeFile(t, dst, "extra.txt", "x\n")
	d, err := NewMovedDestination(filepath.Join(filepath.Dir(dst), "state"), src, dst)
	if err != nil {
		t.Fatal(err)
	}
	sync := func(unit Unit) (int64, error) {
		a, b := net.Pipe()
		served := make(chan error, 1)
		go func() { served <- d.Serve(context.Background(), b, filepath.Join(t.TempDir(), "aside")) }()
		var written atomic.Int64
		err := Sync(context.Background(), &countingConn{a, &written}, unit, Options{Exclude: []string{"excluded.txt"}, Dereference: true})
		return written.Load(), errors.Join(err, <-served)
	}
	if _, err := sync(Unit{Main: src}); err != nil {
		t.Fatal(err)
	}

	for p, want := range map[string]string{
		"sub/b.txt": "file b\n", "inside": "link sub/b.txt", "settings.json": "file {}\n", "skills/s/SKILL.md": "file skill\n",
		"skills/s/loop": "link ../..", "dangling": "link /nonexistent", "excluded.txt": "file the worker's\n", "extra.txt": "missing",
	} {
		got := "missing"
		if target, err := os.Readlink(filepath.Join(dst, p)); err == nil {
			got = "link " + target
		} else if contents, err := os.ReadFile(filepath.Join(dst, p)); err == nil {
			got = "file " + string(contents)
		}
		if got != want {
			t.Errorf("%s on the worker: %s, want %s", p, got, want)
		}
	}
	if sent, err := sync(Unit{Main: src}); err != nil || sent > 10_000 {
		t.Fatalf("the second sync sent %d bytes: %v", sent, err)
	}
	if _, err := sync(Unit{Main: outside}); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("Sync of a folder outside the mapped one = %v", err)
	}
}

func TestOptionsIncludeAndExcludePaths(t *testing.T) {
	unit := newRepo(t)
	main := unit.Main
	writeFile(t, main, "node_modules/pkg/other.js", "x\n")
	r := newRigWith(t, unit, func(s *Source) {
		s.opts = Options{Include: []string{"*.bigdump", "tmp/cache", "node_modules/*/index.js"}, Exclude: []string{"src/d1"}}
	})
	r.barrier(t)
	for _, p := range []string{"dump.bigdump", "tmp/cache/c", "node_modules/pkg/index.js"} {
		if _, err := os.Lstat(r.dst + filepath.Join(main, p)); err != nil {
			t.Errorf("included path missing: %v", err)
		}
	}
	for _, p := range []string{"src/d1", "node_modules/pkg/other.js"} {
		if _, err := os.Lstat(r.dst + filepath.Join(main, p)); err == nil {
			t.Errorf("%s mirrored", p)
		}
	}

	link := filepath.Join(main, "node_modules/pkg/link")
	if err := os.Symlink("index.js", link); err != nil {
		t.Fatal(err)
	}
	r.barrier(t)
	if _, err := os.Lstat(r.dst + link); err == nil {
		t.Fatal("a symlink that isn't included was mirrored")
	}
	git(t, main, "add", "-f", link)
	r.barrier(t)
	if _, err := os.Lstat(r.dst + link); err != nil {
		t.Fatalf("a symlink Git tracks now is missing: %v", err)
	}
}

func TestDiscover(t *testing.T) {
	unit := newRepo(t)
	got, err := Discover(filepath.Join(unit.Worktrees[0], "src"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Main != unit.Main || got.GitDir != filepath.Join(unit.Main, ".git") || !slices.Equal(got.Worktrees, unit.Worktrees) || len(got.Worktrees) != 2 {
		t.Fatalf("Discover from a worktree = %+v, want %+v", got, unit)
	}
	plain := t.TempDir()
	if got, err := Discover(plain); err != nil || got.Main != plain || got.GitDir != "" || got.Worktrees != nil {
		t.Fatalf("Discover outside Git = %+v, %v", got, err)
	}
}

// rig mirrors a unit to main's copy under dst, in one process.
type rig struct {
	unit    Unit
	dst     string // main has every path p at dst+p
	state   string
	d       *Destination
	src     *Source
	conn    net.Conn
	ran     chan error
	served  chan error
	written atomic.Int64
	starts  int
}

func newRig(t *testing.T, unit Unit) *rig { return newRigWith(t, unit, nil) }

func newRigWith(t *testing.T, unit Unit, configure func(*Source)) *rig {
	dst := t.TempDir()
	r := &rig{unit: unit, dst: dst, state: filepath.Join(dst, "state")}
	var err error
	if r.d, err = NewMovedDestination(r.state, "/", dst); err != nil {
		t.Fatal(err)
	}
	r.start(t, configure)
	return r
}

func (r *rig) start(t *testing.T, configure func(*Source)) {
	t.Helper()
	r.launch(t, configure)
	waitFor(t, "the first barrier", func() bool {
		select {
		case err := <-r.ran:
			t.Fatalf("Run: %v", err)
		default:
		}
		return r.src.Healthy()
	})
}

// launch starts a source without waiting for its first barrier.
func (r *rig) launch(t *testing.T, configure func(*Source)) {
	t.Helper()
	r.starts++
	a, b := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	r.conn = a
	r.src = NewSource(r.unit, Options{})
	if configure != nil {
		configure(r.src)
	}
	// Both are closed after their result, so that they can be read again.
	ran, served := make(chan error, 1), make(chan error, 1)
	r.ran, r.served = ran, served
	go func() {
		served <- r.d.Serve(ctx, b, filepath.Join(r.dst, "aside", strconv.Itoa(r.starts)))
		close(served)
	}()
	go func() {
		ran <- r.src.Run(ctx, &countingConn{a, &r.written})
		close(ran)
	}()
	t.Cleanup(func() {
		cancel()
		<-ran
		<-served
	})
}

func (r *rig) barrier(t *testing.T) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	n, writing, err := r.src.Barrier(ctx)
	if err != nil {
		t.Fatalf("barrier: %v", err)
	}
	if len(writing) > 0 {
		t.Fatalf("barrier %d left out %v", n, writing)
	}
	return n
}

// verify checks main's copy after one barrier, and its files after the next,
// which brings deletions of objects.
func (r *rig) verify(t *testing.T) {
	t.Helper()
	r.barrier(t)
	checkGit(t, r.unit, r.dst)
	r.barrier(t)
	checkFiles(t, r.unit, r.dst)
}

func checkCopy(t *testing.T, unit Unit, dst string) {
	t.Helper()
	checkGit(t, unit, dst)
	checkFiles(t, unit, dst)
}

// checkGit checks that Git finds main's copy sound and that every worktree
// has the same status on both sides.
func checkGit(t *testing.T, unit Unit, dst string) {
	t.Helper()
	unit, err := Discover(unit.Main)
	if err != nil {
		t.Fatal(err)
	}
	fsck(t, dst+unit.GitDir)
	for _, wt := range append([]string{unit.Main}, unit.Worktrees...) {
		want := gitStatus(t, wt, nil)
		gitDir := dst + unit.GitDir
		if wt != unit.Main {
			data, err := os.ReadFile(dst + filepath.Join(wt, ".git"))
			if err != nil {
				t.Fatalf("worktree %s on main: %v", wt, err)
			}
			gitDir = dst + strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir: ")
		}
		got := gitStatus(t, dst+wt, []string{"GIT_DIR=" + gitDir, "GIT_WORK_TREE=" + dst + wt})
		if got != want {
			t.Errorf("git status in %s differs.\nworker:\n%s\nmain:\n%s", wt, want, got)
		}
	}
}

// checkFiles checks that the mirrored files match.
func checkFiles(t *testing.T, unit Unit, dst string) {
	t.Helper()
	unit, err := Discover(unit.Main)
	if err != nil {
		t.Fatal(err)
	}
	want, got := sourceManifest(t, unit), destinationManifest(t, unit, dst)
	if !slices.Equal(want, got) {
		t.Errorf("main's files differ from the worker's (- worker, + main):\n%s", lineDiff(want, got))
	}
}

func fsck(t *testing.T, gitDir string) {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", gitDir, "fsck", "--strict", "--no-dangling", "--no-progress").CombinedOutput()
	if err != nil || len(out) > 0 {
		t.Fatalf("git fsck on main: %v\n%s", err, out)
	}
}

func gitStatus(t *testing.T, dir string, env []string) string {
	t.Helper()
	cmd := exec.Command("git", "--no-optional-locks", "status", "--porcelain=v1", "-uall", "-b")
	cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status in %s: %v\n%s", dir, err, out)
	}
	return string(out)
}

func sourceManifest(t *testing.T, unit Unit) []string {
	s := newSender(unit, Options{}, nil)
	defer s.close()
	seen, err := s.scan()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for p := range seen {
		lines = append(lines, describe(t, p, p))
	}
	slices.Sort(lines)
	return lines
}

func destinationManifest(t *testing.T, unit Unit, dst string) []string {
	unit, err := Discover(unit.Main)
	if err != nil {
		t.Fatal(err)
	}
	roots := unit.roots()
	var lines []string
	for _, root := range roots {
		filepath.WalkDir(dst+root, func(local string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			p := strings.TrimPrefix(local, dst)
			if d.IsDir() && p != root && slices.Contains(roots, p) {
				return filepath.SkipDir
			}
			lines = append(lines, describe(t, local, p))
			return nil
		})
	}
	slices.Sort(lines)
	return lines
}

func describe(t *testing.T, local, p string) string {
	info, err := os.Lstat(local)
	if err != nil {
		return "missing " + p
	}
	switch {
	case info.IsDir():
		return fmt.Sprintf("d %s %o", p, info.Mode().Perm()|0o700)
	case info.Mode()&fs.ModeSymlink != 0:
		target, _ := os.Readlink(local)
		return fmt.Sprintf("l %s -> %s", p, target)
	}
	data, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	mtime := info.ModTime().UnixNano()
	if strings.Contains(p, "/.git/objects/") && immutableObject(p) {
		mtime = 0 // Git refreshes it on one side only
	}
	return fmt.Sprintf("f %s %o %d %d %s", p, info.Mode().Perm(), info.Size(), mtime, hex.EncodeToString(sum[:8]))
}

func lineDiff(want, got []string) string {
	var b strings.Builder
	shown := 0
	for _, l := range want {
		if !slices.Contains(got, l) && shown < 20 {
			fmt.Fprintf(&b, "- %s\n", l)
			shown++
		}
	}
	for _, l := range got {
		if !slices.Contains(want, l) && shown < 40 {
			fmt.Fprintf(&b, "+ %s\n", l)
			shown++
		}
	}
	return b.String()
}

// newRepo makes a repository with two linked worktrees and ignored files and folders.
func newRepo(t *testing.T) Unit {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "src", "proj")
	mustRun(t, base, "git", "init", "-q", "-b", "main", main)
	git(t, main, "config", "gc.auto", "0")
	writeFile(t, main, ".gitignore", "node_modules/\n/tmp/*\n!/tmp/.keep\n*.log\n.env\n*.bigdump\n")
	for i := range 300 {
		writeFile(t, main, fmt.Sprintf("src/d%d/f%d.txt", i/10+1, i%10+1), randomText(i, 200+i*13%4000))
	}
	writeFile(t, main, "tmp/.keep", "")
	writeFile(t, main, "bin/run", "#!/bin/sh\necho hi\n")
	os.Chmod(filepath.Join(main, "bin/run"), 0o755)
	if err := os.Symlink("src/d1/f1.txt", filepath.Join(main, "link1")); err != nil {
		t.Fatal(err)
	}
	git(t, main, "add", "-A")
	git(t, main, "commit", "-qm", "initial")
	writeFile(t, main, ".env", "SECRET=dummy\n")
	writeFile(t, main, "log/dev.log", "log\n")
	writeFile(t, main, "dump.bigdump", strings.Repeat("x", 11<<20))
	writeFile(t, main, "node_modules/pkg/index.js", "x\n")
	writeFile(t, main, "tmp/cache/c", "c\n")
	for _, name := range []string{"feat-a", "feat-b"} {
		git(t, main, "worktree", "add", "-q", "-b", name, filepath.Join(base, ".worktrees", "proj", name))
	}
	unit, err := Discover(main)
	if err != nil {
		t.Fatal(err)
	}
	return unit
}

func randomText(seed, size int) string {
	rng := rand.New(rand.NewPCG(uint64(seed), 1))
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 \n"
	b := make([]byte, size)
	for i := range b {
		b[i] = letters[rng.IntN(len(letters))]
	}
	return string(b)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, dir, name, content string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(content)
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	mustRun(t, dir, "git", args...)
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func stagedCount(r *rig) int {
	staged, _ := os.ReadDir(filepath.Join(r.state, "staging"))
	return len(staged)
}

// countingConn counts the bytes a source sends.
type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.n.Add(int64(n))
	return n, err
}
