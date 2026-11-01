package view_test

import (
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
	// Another user's home folder next to main's is hidden in the view, as is a real user at main's path.
	if err := os.Mkdir(filepath.Join(root, "bob"), 0700); err != nil {
		t.Fatal(err)
	}
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

	// A command started in a folder that the view hides starts in main's home folder, and one in the user's own home
	// folder there.
	t.Chdir(filepath.Join(root, "bob"))
	if folder := run(t, main, "pwd -P"); folder != main+"\n" {
		t.Fatalf("started in %q", folder)
	}
	t.Chdir(own)
	if folder := run(t, main, `pwd -P; ls`); folder != own+"\nnew\nnotes\n" {
		t.Fatalf("started in %q", folder)
	}
	if _, err := os.Stat(main); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("main's home folder outside the view: %v", err)
	}
}

func TestTheViewKeepsTheCommandsExitStatus(t *testing.T) {
	main, own := homes(t)
	run(t, main, "true")
	for _, home := range []string{main, own, ""} {
		if code, err := view.Run(home, []string{"sh", "-c", "exit 3"}, nil, os.Stdout, os.Stderr); err != nil || code != 3 {
			t.Fatalf("view.Run(%q) of exit 3 = %d, %v", home, code, err)
		}
	}
}

func TestACommandRunsAsItIsWhenItsHomeFolderIsMains(t *testing.T) {
	_, own := homes(t)
	namespace, err := os.Readlink("/proc/self/ns/user")
	if err != nil {
		t.Skip(err)
	}
	for _, home := range []string{own, ""} {
		var output strings.Builder
		if code, err := view.Run(home, []string{"sh", "-c", `echo "$HOME"; readlink /proc/self/ns/user`}, nil, &output, os.Stderr); err != nil || code != 0 {
			t.Fatalf("view.Run(%q) = %d, %v", home, code, err)
		}
		if output.String() != own+"\n"+namespace+"\n" {
			t.Fatalf("view.Run(%q) printed %q", home, output.String())
		}
		if view.Needed(home) {
			t.Fatalf("Needed(%q)", home)
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
