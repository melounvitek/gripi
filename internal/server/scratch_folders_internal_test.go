package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScratchFoldersBelongToTheirGateway(t *testing.T) {
	base := t.TempDir()
	first, err := openScratchFolders(base)
	if err != nil {
		t.Fatal(err)
	}
	session, err := first.create()
	if err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"work", "tmp"} {
		if info, err := os.Stat(filepath.Join(session, folder)); err != nil || !info.IsDir() {
			t.Fatalf("%s: %v", folder, err)
		}
	}

	// Another gateway leaves the folders of a running one alone.
	second, err := openScratchFolders(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(session); err != nil {
		t.Fatalf("a running gateway's scratch folder was removed: %v", err)
	}

	// A gateway that ended without removing its folders, such as after a crash, no longer holds its lock.
	if err := first.lock.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := openScratchFolders(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.dir); !os.IsNotExist(err) {
		t.Fatalf("an ended gateway's scratch folders were kept: %v", err)
	}

	for _, folders := range []*scratchFolders{second, third} {
		dir := folders.dir
		if err := folders.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("closing kept %s: %v", dir, err)
		}
	}
}

func TestScratchFoldersNeedAPrivateFolder(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, scratchRootName()), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openScratchFolders(base); err == nil {
		t.Fatal("scratch folders opened in a folder that other users can read")
	}

	linked := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(linked, scratchRootName())); err != nil {
		t.Fatal(err)
	}
	if _, err := openScratchFolders(linked); err == nil {
		t.Fatal("scratch folders opened through a symbolic link")
	}
}
