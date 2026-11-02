package helper_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// upload sends main's folder through the helper, as `gripi worker receive` does, with attach for the kind of copy.
func upload(t *testing.T, unit mirror.Unit, attach func(stdin, stdout net.Conn) error) error {
	t.Helper()
	mainEnd, workerEnd := net.Pipe()
	received := make(chan error, 1)
	go func() {
		received <- attach(workerEnd, workerEnd)
		workerEnd.Close()
	}()
	err := mirror.Sync(context.Background(), mainEnd, unit, mirror.Options{})
	if attached := <-received; attached != nil {
		return attached
	}
	return err
}

func TestAMoveGetsACopyOfItsOwnInTheFoldersThatItNames(t *testing.T) {
	runtimeDir, data := t.TempDir(), t.TempDir()
	home := filepath.Join(t.TempDir(), "alice")
	work := filepath.Join(home, "Work")
	fake, _ := serveHelper(t, helper.Options{RuntimeDir: runtimeDir, Home: home, DataDir: data})
	instance := fake.next().Instance
	prepare := func(move string, roots []string, private ...string) helper.Message {
		return fake.request(map[string]any{
			"type": "move.prepare", "move": move, "repository": filepath.Join(work, "gripi", ".git"), "roots": roots, "private": private,
		})
	}
	roots := []string{filepath.Join(work, ".worktrees", "gripi", "feat"), filepath.Join(work, "gripi")}

	response := prepare("m1", roots, work)
	copy := filepath.Join(data, "copies", "m1")
	if !response.OK || response.Path != copy {
		t.Fatalf("move.prepare = %+v", response)
	}
	for _, folder := range []string{filepath.Join(copy, "Work"), work} {
		if info, err := os.Stat(folder); err != nil || !info.IsDir() {
			t.Fatalf("%s after move.prepare: %v", folder, err)
		}
	}
	if again := prepare("m1", roots, work); !again.OK || again.Path != copy {
		t.Fatalf("move.prepare again = %+v", again)
	}
	// The view would show the copy at the link's target, where Git would name a new worktree outside the private root.
	linked, elsewhere := filepath.Join(home, "Linked"), t.TempDir()
	if err := os.Symlink(elsewhere, linked); err != nil {
		t.Fatal(err)
	}
	for _, refused := range []struct {
		move    string
		roots   []string
		private []string
		reason  string
	}{
		{"m1", roots[:1], []string{work}, "other folders"},
		{"m2", []string{filepath.Join(home, "Other", "gripi")}, []string{work}, "outside"},
		{"m2", roots, []string{filepath.Join(work, "gripi")}, "directly in"},
		{"m2", roots, nil, "private"},
		{"m2", nil, []string{work}, "folders"},
		{"m2", []string{filepath.Join(linked, "gripi")}, []string{work, linked}, "Linked in the worker user's home folder is a symbolic link to " + elsewhere},
		{"../m2", roots, []string{work}, "letters"},
	} {
		if response := prepare(refused.move, refused.roots, refused.private...); response.OK || !strings.Contains(response.Error, refused.reason) {
			t.Errorf("move.prepare of %s with %v in %v = %+v, want an error with %q", refused.move, refused.roots, refused.private, response, refused.reason)
		}
	}

	// Main uploads its shadow of the move's folders, which are in its home folder, as they are.
	shadow := filepath.Join(t.TempDir(), "shadow", "home", "alice")
	files := map[string]string{
		filepath.Join("Work", "gripi", ".git", "HEAD"): "ref: refs/heads/master\n", filepath.Join("Work", "gripi", "node_modules", "x"): "as main had it\n",
		filepath.Join("Work", ".worktrees", "gripi", "feat", ".git"): "gitdir: " + filepath.Join(work, "gripi", ".git", "worktrees", "feat") + "\n",
	}
	for path, contents := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(shadow, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(shadow, path), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := upload(t, mirror.Unit{Main: shadow}, func(stdin, stdout net.Conn) error {
		return helper.AttachReceiveMove(runtimeDir, "m1", shadow, instance, stdin, stdout)
	}); err != nil {
		t.Fatal(err)
	}
	for path, contents := range files {
		if got, _ := os.ReadFile(filepath.Join(copy, path)); string(got) != contents {
			t.Errorf("the copy's %s = %q, want %q", path, got, contents)
		}
	}
	if err := upload(t, mirror.Unit{Main: shadow}, func(stdin, stdout net.Conn) error {
		return helper.AttachReceiveMove(runtimeDir, "m3", shadow, instance, stdin, stdout)
	}); err == nil || !strings.Contains(err.Error(), "m3") {
		t.Fatalf("an upload for a move that wasn't prepared = %v", err)
	}
}
