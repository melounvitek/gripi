//go:build linux

package mirror

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Pi appends entries to a session file, and Gripi's guard of the file on main takes another file in its place for
// another program's.
func TestAFileThatGrowsGrowsInPlace(t *testing.T) {
	folder := t.TempDir()
	unit := Unit{Main: folder}
	writeFile(t, folder, "session.jsonl", "{\"id\":\"1\"}\n")
	r := newRig(t, unit)
	local := r.dst + filepath.Join(folder, "session.jsonl")
	before := inodeOf(t, local)

	appendFile(t, folder, "session.jsonl", "{\"id\":\"2\"}\n")
	r.barrier(t)
	if data, _ := os.ReadFile(local); string(data) != "{\"id\":\"1\"}\n{\"id\":\"2\"}\n" {
		t.Fatalf("main has %q", data)
	}
	if inodeOf(t, local) != before {
		t.Fatal("main got another file instead of the grown one")
	}
	checkFiles(t, unit, r.dst) // the same time and mode too

	writeFile(t, folder, "session.jsonl", "{\"id\":\"rewritten\"}\n")
	r.barrier(t)
	checkFiles(t, unit, r.dst)
}

// Main must never get half of a line that Pi is still writing to a session file, and Gripi's guard of the file on
// main must learn of a rewrite, which it would otherwise take for another program's.
func TestSessionFilesReachMainALineAtATime(t *testing.T) {
	folder := t.TempDir()
	unit := Unit{Main: folder}
	writeFile(t, folder, "session.jsonl", "{\"id\":\"1\"}\n")
	dst := t.TempDir()
	r := &rig{unit: unit, dst: dst, state: filepath.Join(dst, "state")}
	var err error
	if r.d, err = NewMovedDestination(r.state, "/", dst); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var replaced []string
	r.d.Applied = func(_ Checkpoint, _ string, _, paths []string) {
		mu.Lock()
		defer mu.Unlock()
		replaced = append(replaced, paths...)
	}
	takeReplaced := func() []string {
		mu.Lock()
		defer mu.Unlock()
		paths := replaced
		replaced = nil
		return paths
	}
	r.start(t, func(s *Source) { s.opts.CompleteLines = true })
	session := filepath.Join(folder, "session.jsonl")
	takeReplaced()
	before := inodeOf(t, r.dst+session)
	onMain := func() string {
		data, _ := os.ReadFile(r.dst + session)
		return string(data)
	}

	appendFile(t, folder, "session.jsonl", "{\"id\":\"2\"}\n{\"id\":")
	r.barrier(t)
	if got := onMain(); got != "{\"id\":\"1\"}\n{\"id\":\"2\"}\n" {
		t.Fatalf("main has %q", got)
	}
	appendFile(t, folder, "session.jsonl", "\"3\"}\n")
	r.barrier(t)
	checkFiles(t, unit, r.dst)
	if inodeOf(t, r.dst+session) != before {
		t.Fatal("main got another file instead of the grown one")
	}
	if paths := takeReplaced(); len(paths) > 0 {
		t.Fatalf("reported %v as replaced after appends", paths)
	}

	writeFile(t, folder, "session.jsonl", "{\"id\":\"rewritten\"}\n{\"id\":")
	r.barrier(t)
	if got := onMain(); got != "{\"id\":\"rewritten\"}\n" {
		t.Fatalf("main has %q after a rewrite", got)
	}
	if paths := takeReplaced(); !slices.Equal(paths, []string{session}) {
		t.Fatalf("reported %v as replaced after a rewrite", paths)
	}

	writeFile(t, folder, "notes.txt", "no newline")
	r.barrier(t)
	if data, _ := os.ReadFile(r.dst + filepath.Join(folder, "notes.txt")); string(data) != "no newline" {
		t.Fatalf("main has %q in another file", data)
	}
}

// Pi rewrites a session file in place, an entry at a time, and losing the worker in the middle must leave main with
// the whole session it had, rather than the start of the new file.
func TestAnUnfinishedRewriteOfASessionKeepsMainsCopy(t *testing.T) {
	folder := t.TempDir()
	unit := Unit{Main: folder}
	session := filepath.Join(folder, "session.jsonl")
	turns := "{\"type\":\"session\",\"version\":2}\n{\"id\":\"1\",\"role\":\"user\"}\n{\"id\":\"2\",\"role\":\"assistant\"}\n"
	writeFile(t, folder, "session.jsonl", turns)
	r := newRigWith(t, unit, func(s *Source) { s.opts.CompleteLines, s.opts.AutoBarrier = true, true })
	onMain := func() string {
		data, _ := os.ReadFile(r.dst + session)
		return string(data)
	}

	// Lines added to a file that its writer keeps open still reach main.
	f, err := os.OpenFile(session, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	turns += "{\"id\":\"3\",\"role\":\"user\"}\n"
	f.WriteString("{\"id\":\"3\",\"role\":\"user\"}\n")
	r.barrier(t)
	if got := onMain(); got != turns {
		t.Fatalf("main has %q", got)
	}
	f.Close()

	// As Pi's _rewriteFile: truncate, then write the entries one by one.
	if f, err = os.OpenFile(session, os.O_WRONLY|os.O_TRUNC, 0); err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	migrated := []string{"{\"type\":\"session\",\"version\":3}\n", "{\"id\":\"1\",\"role\":\"user\",\"v\":3}\n",
		"{\"id\":\"2\",\"role\":\"assistant\",\"v\":3}\n", "{\"id\":\"3\",\"role\":\"user\",\"v\":3}\n"}
	for _, entry := range migrated[:3] {
		f.WriteString(entry)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		_, writing, err := r.src.Barrier(ctx)
		cancel()
		if err != nil || !slices.Equal(writing, []string{session}) {
			t.Fatalf("barrier during the rewrite: %v, still being written: %v", err, writing)
		}
		if got := onMain(); got != turns {
			t.Fatalf("main has %q during the rewrite", got)
		}
	}
	f.WriteString(migrated[3])
	f.Close()
	r.barrier(t)
	if got := onMain(); got != strings.Join(migrated, "") {
		t.Fatalf("main has %q after the rewrite", got)
	}
	checkFiles(t, unit, r.dst)
}

// Pi's session file before and after a migration rewrote it.
const (
	sessionTurns    = "{\"type\":\"session\",\"version\":2}\n{\"id\":\"1\",\"role\":\"user\"}\n"
	sessionHeader   = "{\"type\":\"session\",\"version\":3}\n"
	sessionMigrated = sessionHeader + "{\"id\":\"1\",\"role\":\"user\",\"v\":3}\n{\"id\":\"2\",\"role\":\"assistant\",\"v\":3}\n"
)

// sessionMirror sets a source up as Gripi's mirror of Pi's session folders.
func sessionMirror(s *Source) { s.opts.CompleteLines, s.opts.AutoBarrier = true, true }

// A restart doesn't show whether Pi still has a session file open, and mustn't take the start of a rewrite in
// progress for the whole file.
func TestARestartDuringARewriteOfASessionKeepsMainsCopy(t *testing.T) {
	folder := t.TempDir()
	session := filepath.Join(folder, "session.jsonl")
	writeFile(t, folder, "session.jsonl", sessionTurns)
	r := newRigWith(t, Unit{Main: folder}, sessionMirror)
	onMain := func() string {
		data, _ := os.ReadFile(r.dst + session)
		return string(data)
	}

	// Lines added while the mirror was away to a file that its writer keeps open still reach main.
	f, err := os.OpenFile(session, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	r.stop()
	turns := sessionTurns + "{\"id\":\"2\",\"role\":\"assistant\"}\n"
	f.WriteString(turns[len(sessionTurns):])
	r.start(t, sessionMirror)
	r.barrier(t)
	if got := onMain(); got != turns {
		t.Fatalf("main has %q", got)
	}
	f.Close()

	// As Pi's _rewriteFile, which has truncated the file and written the new header so far.
	if f, err = os.OpenFile(session, os.O_WRONLY|os.O_TRUNC, 0); err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(sessionHeader)
	r.stop()
	r.start(t, sessionMirror)
	if writing := r.barrierWriting(t); !slices.Equal(writing, []string{session}) {
		t.Fatalf("still being written after a restart during the rewrite: %v", writing)
	}
	if got := onMain(); got != turns {
		t.Fatalf("main has %q after a restart during the rewrite", got)
	}
	f.WriteString(sessionMigrated[len(sessionHeader):])
	f.Close()
	r.barrier(t)
	checkFiles(t, Unit{Main: folder}, r.dst)
}

func TestARewriteOfASessionFinishedWhileTheMirrorWasAwayReachesMain(t *testing.T) {
	folder := t.TempDir()
	writeFile(t, folder, "session.jsonl", sessionTurns)
	r := newRigWith(t, Unit{Main: folder}, sessionMirror)
	r.stop()
	writeFile(t, folder, "session.jsonl", sessionMigrated)
	r.start(t, sessionMirror)
	r.barrier(t)
	checkFiles(t, Unit{Main: folder}, r.dst)
}

// Nor does a rescan after the kernel lost file events.
func TestARescanDuringARewriteOfASessionKeepsMainsCopy(t *testing.T) {
	folder := t.TempDir()
	session := filepath.Join(folder, "session.jsonl")
	writeFile(t, folder, "session.jsonl", sessionTurns)
	var stalled atomic.Bool
	release := make(chan struct{})
	r := newRigWith(t, Unit{Main: folder}, func(s *Source) {
		sessionMirror(s)
		s.beforeRead = func() {
			if stalled.Load() {
				<-release
			}
		}
	})
	stalled.Store(true)
	writeFile(t, folder, "wake.txt", "x") // the reader stops after this event
	time.Sleep(100 * time.Millisecond)

	f, err := os.OpenFile(session, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(sessionHeader)
	// Writes to two files in turn, which the kernel doesn't merge into one event, until its queue overflows.
	data, err := os.ReadFile("/proc/sys/fs/inotify/max_queued_events")
	if err != nil {
		t.Fatal(err)
	}
	limit, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	var flood [2]*os.File
	for i := range flood {
		if flood[i], err = os.Create(filepath.Join(folder, fmt.Sprintf("flood%d.txt", i))); err != nil {
			t.Fatal(err)
		}
		defer flood[i].Close()
	}
	for i := range limit + 100 {
		flood[i%2].WriteString("x")
	}
	stalled.Store(false)
	close(release)

	if writing := r.barrierWriting(t); !slices.Equal(writing, []string{session}) {
		t.Fatalf("still being written after a rescan during the rewrite: %v", writing)
	}
	if r.src.overflows.Load() == 0 {
		t.Fatal("the kernel's event queue didn't overflow")
	}
	if data, _ := os.ReadFile(r.dst + session); string(data) != sessionTurns {
		t.Fatalf("main has %q after a rescan during the rewrite", data)
	}
	f.WriteString(sessionMigrated[len(sessionHeader):])
	f.Close()
	r.barrier(t)
	checkFiles(t, Unit{Main: folder}, r.dst)
}

func TestMainsChangesAreSavedAsideBeforeTheWorkersReplaceThem(t *testing.T) {
	unit := newRepo(t)
	dst := t.TempDir()
	r := &rig{unit: unit, dst: dst, state: filepath.Join(dst, "state")}
	var err error
	if r.d, err = NewMovedDestination(r.state, "/", dst); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var saved []string
	var asides []string
	r.d.Applied = func(_ Checkpoint, aside string, paths, _ []string) {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range paths {
			saved = append(saved, p)
			asides = append(asides, filepath.Join(aside, p))
		}
	}
	r.start(t, nil)
	main := unit.Main
	onMain := func(name string) string { return r.dst + filepath.Join(main, name) }
	takeSaved := func() ([]string, []string) {
		mu.Lock()
		defer mu.Unlock()
		paths, files := saved, asides
		saved, asides = nil, nil
		slices.Sort(paths)
		slices.Sort(files)
		return paths, files
	}

	// One file the mirror wrote, and one it never wrote, change on main.
	writeFile(t, main, "src/d3/f1.txt", "from the worker\n")
	r.barrier(t)
	writeFile(t, filepath.Dir(onMain("src/d1/f1.txt")), "f1.txt", "edited on main\n")
	writeFile(t, filepath.Dir(onMain("src/d3/f1.txt")), "f1.txt", "edited on main too\n")
	// A plain git status on main rewrites the index.
	mustRun(t, onMain("."), "git", "status", "--porcelain")

	// Another barrier comes first, which changes neither.
	writeFile(t, main, "src/d4/f1.txt", "unrelated\n")
	r.barrier(t)
	if paths, _ := takeSaved(); len(paths) > 0 {
		t.Fatalf("saved %v, which the worker didn't replace", paths)
	}

	writeFile(t, main, "src/d1/f1.txt", "changed on the worker\n")
	mustRun(t, main, "rm", "src/d3/f1.txt")
	git(t, main, "commit", "-qam", "replaces main's changes")
	r.verify(t)
	paths, files := takeSaved()
	if want := []string{filepath.Join(main, "src/d1/f1.txt"), filepath.Join(main, "src/d3/f1.txt")}; !slices.Equal(paths, want) {
		t.Fatalf("saved %v, want %v", paths, want)
	}
	for i, want := range []string{"edited on main\n", "edited on main too\n"} {
		if data, err := os.ReadFile(files[i]); err != nil || string(data) != want {
			t.Errorf("main's version of %s: %q, %v", paths[i], data, err)
		}
	}

	// What main changes while the worker is away is saved as the worker's changes come back.
	r.conn.Close()
	<-r.served
	<-r.ran
	writeFile(t, filepath.Dir(onMain("src/d5/f1.txt")), "f1.txt", "edited while away\n")
	writeFile(t, main, "src/d5/f1.txt", "changed on the worker while away\n")
	r.start(t, nil)
	r.verify(t)
	if paths, files := takeSaved(); !slices.Equal(paths, []string{filepath.Join(main, "src/d5/f1.txt")}) {
		t.Fatalf("saved %v after reconnecting", paths)
	} else if data, _ := os.ReadFile(files[0]); string(data) != "edited while away\n" {
		t.Fatalf("main's version: %q", data)
	}
}

// A gateway restart must not take the files the destination wrote for main's changes, and the record of them must
// not grow with every barrier.
func TestTheDestinationRemembersWhatItWroteAcrossRestarts(t *testing.T) {
	folder := t.TempDir()
	r := newRig(t, Unit{Main: folder})
	for round := range 30 {
		for i := range 100 {
			writeFile(t, folder, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("round %d\n", round))
		}
		r.barrier(t)
	}
	if data, _ := os.ReadFile(filepath.Join(r.state, "written")); bytes.Count(data, []byte("\n")) > 1300 {
		t.Fatalf("%d lines record 100 files", bytes.Count(data, []byte("\n")))
	}
	r.conn.Close()
	<-r.served
	<-r.ran

	d, err := NewMovedDestination(r.state, "/", r.dst)
	if err != nil {
		t.Fatal(err)
	}
	var saved []string
	d.Applied = func(_ Checkpoint, _ string, paths, _ []string) { saved = append(saved, paths...) }
	r.d = d
	writeFile(t, r.dst+folder, "f1.txt", "edited on main\n")
	writeFile(t, folder, "f1.txt", "changed on the worker\n")
	writeFile(t, folder, "f2.txt", "changed on the worker\n")
	r.start(t, nil)
	r.barrier(t)
	if !slices.Equal(saved, []string{filepath.Join(folder, "f1.txt")}) {
		t.Fatalf("saved %v", saved)
	}
}

func TestMainsFileInPlaceOfAFolderGoesAsideBeforeTheWorkerWritesIntoIt(t *testing.T) {
	folder := t.TempDir()
	writeFile(t, folder, "file/f.txt", "1\n")
	writeFile(t, folder, "link/f.txt", "1\n")
	r := newRig(t, Unit{Main: folder})
	onMain := func(name string) string { return r.dst + filepath.Join(folder, name) }
	os.RemoveAll(onMain("file"))
	writeFile(t, r.dst+folder, "file", "main's file\n")
	os.RemoveAll(onMain("link"))
	if err := os.Symlink("elsewhere", onMain("link")); err != nil {
		t.Fatal(err)
	}

	writeFile(t, folder, "file/f.txt", "2\n")
	writeFile(t, folder, "link/f.txt", "2\n")
	aside := filepath.Join(r.dst, "aside", "1", "main", strconv.FormatUint(r.barrier(t), 10), folder)
	if data, err := os.ReadFile(filepath.Join(aside, "file")); string(data) != "main's file\n" {
		t.Errorf("main's file aside: %q, %v", data, err)
	}
	if target, err := os.Readlink(filepath.Join(aside, "link")); target != "elsewhere" {
		t.Errorf("main's symlink aside: %q, %v", target, err)
	}
	checkFiles(t, Unit{Main: folder}, r.dst)
}

// Finishing a barrier that main couldn't apply must save main's changes aside like applying it does.
func TestFinishingAnInterruptedBarrierSavesMainsChangesAside(t *testing.T) {
	folder := t.TempDir()
	writeFile(t, folder, "a/f.txt", "a\n")
	writeFile(t, folder, "b/f.txt", "b\n")
	r := newRig(t, Unit{Main: folder})
	writeFile(t, folder, "a/f.txt", "the worker's a\n")
	writeFile(t, folder, "b/f.txt", "the worker's b\n")
	waitFor(t, "changes staged", func() bool { return stagedCount(r) == 2 })
	b := filepath.Join(folder, "b/f.txt")
	writeFile(t, r.dst, b, "main's b\n")
	stuck := r.dst + filepath.Join(folder, "a")
	os.Chmod(stuck, 0o500) // applying fails before it gets to b
	t.Cleanup(func() { os.Chmod(stuck, 0o755) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if n, _, err := r.src.Barrier(ctx); err == nil {
		t.Fatalf("barrier %d applied despite the read-only folder", n)
	}
	<-r.served
	<-r.ran

	os.Chmod(stuck, 0o755)
	d, err := NewMovedDestination(r.state, "/", r.dst)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(r.dst + b); string(data) != "the worker's b\n" {
		t.Fatalf("main has %q", data)
	}
	aside := filepath.Join(r.dst, "aside", "1", "main", strconv.FormatUint(d.Checkpoint().Barrier, 10))
	if data, err := os.ReadFile(filepath.Join(aside, b)); string(data) != "main's b\n" {
		t.Fatalf("main's version aside: %q, %v", data, err)
	}
	var saved []string
	d.Applied = func(_ Checkpoint, folder string, paths, _ []string) {
		if folder == aside {
			saved = append(saved, paths...)
		}
	}
	r.d = d
	r.start(t, nil)
	if !slices.Equal(saved, []string{b}) {
		t.Fatalf("reported %v as saved", saved)
	}
}

// A barrier that main couldn't finish may have rewritten session files already, and finishing it must report those
// too, or Gripi's guard takes them for another program's.
func TestFinishingAnInterruptedBarrierReportsWhatItRewroteBefore(t *testing.T) {
	folder := t.TempDir()
	writeFile(t, folder, "a/session.jsonl", "{\"id\":\"a\"}\n")
	writeFile(t, folder, "b/session.jsonl", "{\"id\":\"b\"}\n")
	sessions := func(s *Source) { s.opts.CompleteLines = true }
	r := newRigWith(t, Unit{Main: folder}, sessions)
	stuck := r.dst + filepath.Join(folder, "b")
	os.Chmod(stuck, 0o500) // the second rewrite fails after the first
	t.Cleanup(func() { os.Chmod(stuck, 0o755) })
	writeFile(t, folder, "a/session.jsonl", "{\"id\":\"A\"}\n")
	writeFile(t, folder, "b/session.jsonl", "{\"id\":\"B\"}\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if n, _, err := r.src.Barrier(ctx); err == nil {
		t.Fatalf("barrier %d applied despite the read-only folder", n)
	}
	<-r.served
	<-r.ran

	os.Chmod(stuck, 0o755)
	d, err := NewMovedDestination(r.state, "/", r.dst)
	if err != nil {
		t.Fatal(err)
	}
	var replaced []string
	d.Applied = func(_ Checkpoint, _ string, _, paths []string) { replaced = append(replaced, paths...) }
	r.d = d
	r.start(t, sessions)
	if want := []string{filepath.Join(folder, "a/session.jsonl"), filepath.Join(folder, "b/session.jsonl")}; !slices.Equal(replaced, want) {
		t.Fatalf("reported %v as replaced, want %v", replaced, want)
	}
}

// stop stops the source, as when the worker's helper restarts or loses main.
func (r *rig) stop() {
	r.conn.Close()
	<-r.served
	<-r.ran
}

// barrierWriting takes a barrier and returns the files it left out as still being written.
func (r *rig) barrierWriting(t *testing.T) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, writing, err := r.src.Barrier(ctx)
	if err != nil {
		t.Fatalf("barrier: %v", err)
	}
	return writing
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}
