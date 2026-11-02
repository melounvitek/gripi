package view_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/melounvitek/gripi/internal/worker/view"
)

func TestMain(m *testing.M) {
	view.Init()
	os.Exit(m.Run())
}

// homes gives this process a home folder of its own, as a worker user has, and returns it and main's home folder,
// which is next to it and doesn't exist.
func homes(t *testing.T) (main, own string) {
	t.Helper()
	root := t.TempDir()
	own = filepath.Join(root, "worker")
	if err := os.Mkdir(own, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", own)
	return filepath.Join(root, "alice"), own
}

// run runs a sh script in the view of main and returns its output, skipping the test where the system blocks the
// view, such as on Ubuntu with AppArmor's default restriction.
func run(t *testing.T, main, script string, arguments ...string) string {
	t.Helper()
	var output strings.Builder
	code, err := view.Run(main, append([]string{"sh", "-c", script, "sh"}, arguments...), nil, &output, os.Stderr)
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, errors.ErrUnsupported) {
		t.Skipf("this system has no private views: %v", err)
	}
	if err != nil || code != 0 {
		t.Fatalf("view.Run = %d, %v; output %q", code, err, output.String())
	}
	return output.String()
}

func TestTheViewShowsTheUsersHomeFolderAtMainsHome(t *testing.T) {
	main, own := homes(t)
	root := filepath.Dir(own)
	if err := os.WriteFile(filepath.Join(own, "notes"), []byte("the worker's\n"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	t.Chdir(outside)

	output := run(t, main, `echo "$HOME"; pwd -P; id -u; id -g; grep CapEff /proc/self/status; cat "$HOME/notes"; ls "$1"; echo written > "$HOME/new"`, root)
	expected := strings.Join([]string{main, outside, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()), "CapEff:\t0000000000000000", "the worker's", "alice", "worker"}, "\n") + "\n"
	if output != expected {
		t.Fatalf("in the view:\n%s\nwant:\n%s", output, expected)
	}
	info, err := os.Stat(filepath.Join(own, "new"))
	if err != nil || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		t.Fatalf("the file written to main's home folder in the view: %v, %v", info, err)
	}

	// A command started in the user's own home folder starts there in the view too.
	t.Chdir(own)
	if folder := run(t, main, `pwd -P; ls`); folder != own+"\nnew\nnotes\n" {
		t.Fatalf("started in %q", folder)
	}
	if _, err := os.Stat(main); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("main's home folder outside the view: %v", err)
	}
}

func TestTheViewShowsTheUsersHomeFolderOverARealFolderAtMainsHome(t *testing.T) {
	main, own := homes(t)
	if err := os.MkdirAll(filepath.Join(main, "project"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "marker"), []byte("main's\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "notes"), []byte("the worker's\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A command started in a folder that the view hides starts in main's home folder.
	t.Chdir(filepath.Join(main, "project"))

	if output := run(t, main, `pwd -P; ls -A; echo written > marker`); output != main+"\nnotes\n" {
		t.Fatalf("in the view: %q", output)
	}
	if marker, err := os.ReadFile(filepath.Join(main, "marker")); err != nil || string(marker) != "main's\n" {
		t.Fatalf("the marker in the real folder at main's home folder: %q, %v", marker, err)
	}
	if written, err := os.ReadFile(filepath.Join(own, "marker")); err != nil || string(written) != "written\n" {
		t.Fatalf("the file written to main's home folder in the view: %q, %v", written, err)
	}
}

func TestTheViewKeepsWhatIsNextToMainsHome(t *testing.T) {
	main, own := homes(t)
	root := filepath.Dir(own)
	if err := os.MkdirAll(filepath.Join(root, "projects", "app"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "shared"), []byte("shared\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("projects", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "notes"), []byte("the worker's\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// What is written next to main's home folder stays after the view, and what the view cannot keep there is refused.
	output := run(t, main, `cd "$1"; ls; readlink link; cat shared worker/notes; echo app > link/app/new; echo more >> shared; mkdir new 2>/dev/null || echo refused`, root)
	expected := strings.Join([]string{"alice", "link", "projects", "shared", "worker", "projects", "shared", "the worker's", "refused"}, "\n") + "\n"
	if output != expected {
		t.Fatalf("in the view:\n%s\nwant:\n%s", output, expected)
	}
	for path, contents := range map[string]string{filepath.Join("projects", "app", "new"): "app\n", "shared": "shared\nmore\n"} {
		if written, err := os.ReadFile(filepath.Join(root, path)); err != nil || string(written) != contents {
			t.Fatalf("%s after the view: %q, %v", path, written, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the folder made next to main's home folder in the view: %v", err)
	}
}

func TestTheViewKeepsTheCommandsExitStatus(t *testing.T) {
	main, own := homes(t)
	run(t, main, "true")
	for _, home := range []string{main, own} {
		if code, err := view.Run(home, []string{"sh", "-c", "exit 3"}, nil, os.Stdout, os.Stderr); err != nil || code != 3 {
			t.Fatalf("view.Run(%q) of exit 3 = %d, %v", home, code, err)
		}
	}
}

// The helper needs no view when its user's home folder is main's, but what main checks there runs in namespaces of its
// own, as each moved session does, where the view then changes nothing.
func TestTheViewOfTheUsersOwnHomeFolderChangesNothing(t *testing.T) {
	_, own := homes(t)
	for _, home := range []string{own, ""} {
		if view.Needed(home) {
			t.Fatalf("Needed(%q)", home)
		}
	}
	namespace, err := os.Readlink("/proc/self/ns/user")
	if err != nil {
		t.Skip(err)
	}
	if err := os.WriteFile(filepath.Join(own, "notes"), []byte("the worker's\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(own)

	output := run(t, own, `echo "$HOME"; pwd -P; id -u; cat notes; readlink /proc/self/ns/user`)
	expected := strings.Join([]string{own, own, strconv.Itoa(os.Getuid()), "the worker's"}, "\n") + "\n"
	if !strings.HasPrefix(output, expected) || strings.HasSuffix(output, "\n"+namespace+"\n") {
		t.Fatalf("in the view of the user's own home folder:\n%s\nwant:\n%s<another user namespace than %s>", output, expected, namespace)
	}
}

// runMove runs a sh script in a move's view and returns its output, skipping the test where the system blocks the
// view.
func runMove(t *testing.T, move view.Move, dir, script string) string {
	t.Helper()
	process, err := move.Command(dir, []string{"sh", "-c", script}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	process.Stdout, process.Stderr = &output, os.Stderr
	if err := process.Start(); errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, errors.ErrUnsupported) {
		t.Skipf("this system has no private views: %v", err)
	} else if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("the move's view: %v; output %q", err, output.String())
	}
	return output.String()
}

// write makes files with contents.
func write(t *testing.T, files map[string]string) {
	t.Helper()
	for path, contents := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func read(path string) string {
	contents, _ := os.ReadFile(path)
	return string(contents)
}

// A moved session's view shows its copy at its private roots, also where the user's home folder is main's.
func TestAMovesViewShowsItsCopyAtItsPrivateRoots(t *testing.T) {
	_, own := homes(t)
	copy := filepath.Join(own, ".local", "copies", "m1")
	write(t, map[string]string{
		filepath.Join(own, "Work", "repo", "README"): "the worker's own\n", filepath.Join(own, "notes"): "shared\n",
		filepath.Join(copy, "Work", "repo", "README"): "the copy's\n",
	})
	move := view.Move{Home: own, Copy: copy, Private: []string{filepath.Join(own, "Work")}}
	repository := filepath.Join(own, "Work", "repo")

	output := runMove(t, move, repository, `echo "$HOME"; pwd -P; id -u; grep CapEff /proc/self/status; cat README "$HOME/notes"; echo written > new; mkdir ../worktree; echo made > ../worktree/file; echo too >> "$HOME/notes"`)
	expected := strings.Join([]string{own, repository, strconv.Itoa(os.Getuid()), "CapEff:\t0000000000000000", "the copy's", "shared"}, "\n") + "\n"
	if output != expected {
		t.Fatalf("in the move's view:\n%s\nwant:\n%s", output, expected)
	}
	for path, want := range map[string]string{
		filepath.Join(copy, "Work", "repo", "new"): "written\n", filepath.Join(copy, "Work", "worktree", "file"): "made\n",
		filepath.Join(own, "Work", "repo", "README"): "the worker's own\n", filepath.Join(own, "Work", "repo", "new"): "",
		filepath.Join(own, "Work", "worktree", "file"): "", filepath.Join(own, "notes"): "shared\ntoo\n",
	} {
		if got := read(path); got != want {
			t.Errorf("%s after the view = %q, want %q", path, got, want)
		}
	}

	// A folder that the view lacks is no place to start.
	process, err := move.Command(filepath.Join(own, "Work", "missing"), []string{"true"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := process.CombinedOutput(); err == nil || !strings.Contains(string(output), "missing is not a folder in the move's view") {
		t.Fatalf("in a missing folder: %v, %s", err, output)
	}
}

// The helper runs in the view of main's home folder when the worker user's home folder differs, so a move's view is
// in that view.
func TestAMovesViewInTheViewOfMainsHomeFolder(t *testing.T) {
	main, own := homes(t)
	copy := filepath.Join(own, ".local", "copies", "m1")
	write(t, map[string]string{filepath.Join(own, "notes"): "the worker's\n", filepath.Join(copy, "Work", "repo", "README"): "the copy's\n"})
	if err := os.Mkdir(filepath.Join(own, "Work"), 0700); err != nil {
		t.Fatal(err)
	}
	nested, err := json.Marshal(nestedMove{
		Move: view.Move{Home: main, Copy: copy, Private: []string{filepath.Join(main, "Work")}}, Dir: filepath.Join(main, "Work", "repo"),
		Script: `{ echo "$HOME"; pwd -P; cat README "$HOME/notes"; } > "$HOME/seen"; echo written > new`,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRIPI_TEST_NESTED_MOVE", string(nested))

	run(t, main, `exec "$1" -test.run='^TestNestedMove$'`, os.Args[0])
	if seen, want := read(filepath.Join(own, "seen")), strings.Join([]string{main, filepath.Join(main, "Work", "repo"), "the copy's", "the worker's"}, "\n")+"\n"; seen != want {
		t.Fatalf("in the move's view:\n%s\nwant:\n%s", seen, want)
	}
	if written := read(filepath.Join(copy, "Work", "repo", "new")); written != "written\n" {
		t.Fatalf("the copy has %q", written)
	}
	if entries, _ := os.ReadDir(filepath.Join(own, "Work")); len(entries) != 0 {
		t.Fatalf("the worker user's own folder has %v", entries)
	}
}

type nestedMove struct {
	view.Move
	Dir, Script string
}

// TestNestedMove runs GRIPI_TEST_NESTED_MOVE for TestAMovesViewInTheViewOfMainsHomeFolder, in the view of main's home.
func TestNestedMove(t *testing.T) {
	encoded := os.Getenv("GRIPI_TEST_NESTED_MOVE")
	if encoded == "" {
		t.Skip("runs in another test's view")
	}
	var nested nestedMove
	if err := json.Unmarshal([]byte(encoded), &nested); err != nil {
		t.Fatal(err)
	}
	runMove(t, nested.Move, nested.Dir, nested.Script)
}

func TestAMovesViewRefusesPrivateRootsOutsideMainsHomeFolder(t *testing.T) {
	for _, private := range [][]string{nil, {"/home/alice"}, {"/home/alice/Work/repo"}, {"/home/bob/Work"}, {"Work"}, {"/home/alice/Work/"}} {
		move := view.Move{Home: "/home/alice", Copy: "/home/gripi/.local/copies/m1", Private: private}
		if _, err := move.Command("/home/alice/Work", []string{"true"}, nil); err == nil {
			t.Errorf("a move's view with the private roots %q", private)
		}
	}
}

func TestTheViewRefusesHomeFoldersThatItCannotShow(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("private views need Linux")
	}
	main, own := homes(t)
	for home, reason := range map[string]string{
		"/alice":                     "is in /",
		filepath.Join(own, "alice"):  "inside each other",
		filepath.Dir(own):            "inside each other",
		filepath.Join(main, "alice"): "needs the folder",
		"alice":                      "absolute",
	} {
		if !view.Needed(home) {
			t.Fatalf("no view is needed for %s", home)
		}
		if _, err := view.Run(home, []string{"true"}, nil, os.Stdout, os.Stderr); err == nil || !strings.Contains(err.Error(), reason) {
			t.Fatalf("view.Run(%q) = %v, want %q", home, err, reason)
		}
	}
}
