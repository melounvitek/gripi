package helper_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// receive copies main's folder to the folder copy through the helper, as `gripi worker receive` does for instance.
func receive(main, copy, runtimeDir, instance string) error {
	mainEnd, workerEnd := net.Pipe()
	received := make(chan error, 1)
	go func() {
		received <- helper.AttachReceive(runtimeDir, main, copy, instance, workerEnd, workerEnd)
		workerEnd.Close()
	}()
	err := mirror.Sync(context.Background(), mainEnd, mirror.Unit{Main: main}, mirror.Options{})
	if attached := <-received; attached != nil {
		return attached
	}
	return err
}

// The helper makes the copies of main's folders, so that they see the files as its mirrors do, also in a private
// view.
func TestTheHelperReceivesCopies(t *testing.T) {
	runtimeDir := t.TempDir()
	fake, _ := serveHelper(t, helper.Options{RuntimeDir: runtimeDir})
	instance := fake.next().Instance
	main, copy := t.TempDir(), filepath.Join(t.TempDir(), "copy")
	copied := time.Now().Add(-time.Hour)
	for path, contents := range map[string]string{
		filepath.Join(main, "README"): "main's\n", filepath.Join(main, "same"): "same\n",
		filepath.Join(copy, "README"): "the worker's\n", filepath.Join(copy, "same"): "same\n", filepath.Join(copy, "new"): "only the worker's\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		// As an earlier copy left it.
		if err := os.Chtimes(path, copied, copied); err != nil {
			t.Fatal(err)
		}
	}

	if err := receive(main, copy, runtimeDir, "0123456789abcdef"); err == nil || !strings.Contains(err.Error(), "not 0123456789abcdef") {
		t.Fatalf("a copy for another helper = %v", err)
	}
	if err := receive(main, copy, runtimeDir, instance); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(copy, "README"): "main's\n", filepath.Join(copy, "same"): "same\n", filepath.Join(copy, "new"): "",
	} {
		if contents, _ := os.ReadFile(path); string(contents) != want {
			t.Errorf("%s = %q, want %q", path, contents, want)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(copy), ".*")); len(left) != 0 {
		t.Errorf("the copy left %v next to it", left)
	}

	// Main learns why the helper cannot make a copy.
	if os.Geteuid() != 0 {
		locked := t.TempDir()
		if err := os.Chmod(locked, 0500); err != nil {
			t.Fatal(err)
		}
		if err := receive(main, filepath.Join(locked, "copy"), runtimeDir, instance); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("a copy into a folder that the helper cannot write = %v", err)
		}
	}
}
