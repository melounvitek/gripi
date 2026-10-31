package helper_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// A worker's copy that may have changes main never got keeps what main's copy replaces or deletes there aside.
func TestReceiveSavesAsideWhatItReplaces(t *testing.T) {
	main, copy, aside := t.TempDir(), filepath.Join(t.TempDir(), "copy"), filepath.Join(t.TempDir(), "aside")
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
	mainEnd, workerEnd := net.Pipe()
	received := make(chan error, 1)
	go func() { received <- helper.Receive(context.Background(), workerEnd, workerEnd, main, copy, aside) }()
	if err := mirror.Sync(context.Background(), mainEnd, mirror.Unit{Main: main}, mirror.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := <-received; err != nil {
		t.Fatal(err)
	}
	// Aside, files keep main's paths, which are the worker's in Gripi, under the barrier that replaced them.
	saved, _ := filepath.Glob(filepath.Join(aside, "main", "*"))
	if len(saved) != 1 {
		t.Fatalf("saved aside: %v", saved)
	}
	for path, want := range map[string]string{
		filepath.Join(copy, "README"): "main's\n", filepath.Join(copy, "same"): "same\n", filepath.Join(copy, "new"): "",
		filepath.Join(saved[0], main, "README"): "the worker's\n", filepath.Join(saved[0], main, "new"): "only the worker's\n", filepath.Join(saved[0], main, "same"): "",
	} {
		if contents, _ := os.ReadFile(path); string(contents) != want {
			t.Errorf("%s = %q, want %q", path, contents, want)
		}
	}
}
