package auth

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func assertContents(t *testing.T, path, want string) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Fatalf("%s = %q, %v, want %q", path, got, err, want)
	}
}

func assertNoLock(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lock after the write: %v", err)
	}
}

func TestWriteLockedReplacesTheFileWithAPrivateOne(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "auth.json")
	writeFile(t, path, "{}")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := WriteLocked(t.Context(), path, []byte(`{"openai": {"type": "api_key", "key": "sk"}}`)); err != nil {
		t.Fatal(err)
	}

	assertContents(t, path, `{"openai": {"type": "api_key", "key": "sk"}}`)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", after.Mode())
	}
	// A new file rather than the old one rewritten, so that no reader sees it half-written.
	if after.Sys().(*syscall.Stat_t).Ino == before.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("auth.json was rewritten in place")
	}
	assertNoLock(t, path)
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 1 {
		t.Fatalf("folder after the write = %v, %v, want only auth.json", entries, err)
	}
}

func TestWriteLockedWaitsWhilePiHoldsTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeFile(t, path, "{}")
	if err := os.Mkdir(path+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	// A Pi that holds the lock renews it every 15 seconds.
	renewed := time.Now().Add(-20 * time.Second)
	if err := os.Chtimes(path+".lock", renewed, renewed); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := WriteLocked(ctx, path, []byte(`{"a": 1}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WriteLocked while Pi holds the lock = %v, want it to wait until the deadline", err)
	}
	assertContents(t, path, "{}")

	done := make(chan error, 1)
	go func() { done <- WriteLocked(t.Context(), path, []byte(`{"a": 1}`)) }()
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteLocked still waits after Pi released the lock")
	}
	assertContents(t, path, `{"a": 1}`)
	assertNoLock(t, path)
}

func TestWriteLockedBreaksAStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeFile(t, path, "{}")
	if err := os.Mkdir(path+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	// Left by a Pi that died: Pi itself takes over a lock not renewed for 30 seconds.
	abandoned := time.Now().Add(-31 * time.Second)
	if err := os.Chtimes(path+".lock", abandoned, abandoned); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := WriteLocked(ctx, path, []byte(`{"a": 1}`)); err != nil {
		t.Fatal(err)
	}

	assertContents(t, path, `{"a": 1}`)
	assertNoLock(t, path)
}
