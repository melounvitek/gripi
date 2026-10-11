package sessionfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteSessionFileUsesTrashWhenAvailable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("session"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(bin, "trash")
	script := "#!/bin/sh\n/bin/rm -- \"$1\"\n"
	if err := os.WriteFile(trash, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	method, err := DeleteSessionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if method != "trash" {
		t.Fatalf("method = %q", method)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session remains: %v", err)
	}
}

func TestDeleteSessionFileUsesGIOTrashWhenAvailable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("session"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	gio := filepath.Join(bin, "gio")
	script := "#!/bin/sh\n[ \"$1\" = trash ] && /bin/rm -- \"$3\"\n"
	if err := os.WriteFile(gio, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	method, err := DeleteSessionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if method != "trash" {
		t.Fatalf("method = %q", method)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session remains: %v", err)
	}
}

func TestDeleteSessionFileFallsBackToUnlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("session"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())

	method, err := DeleteSessionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if method != "unlink" {
		t.Fatalf("method = %q", method)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session remains: %v", err)
	}
}
