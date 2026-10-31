//go:build linux

package mirror

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
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
	r.d.Applied = func(_ Checkpoint, aside string, paths []string) {
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

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}
