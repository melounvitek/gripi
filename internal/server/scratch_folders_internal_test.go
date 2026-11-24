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

// A fresh cache would take Pi most of its start to fill.
func TestScratchSessionsStartWithACopyOfTheGatewaysJitiCache(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "jiti"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "jiti", "extension.mjs"), []byte("compiled"), 0o600); err != nil {
		t.Fatal(err)
	}
	folders, err := openScratchFolders(base)
	if err != nil {
		t.Fatal(err)
	}
	defer folders.Close()
	session, err := folders.create()
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(session, "tmp", "jiti", "extension.mjs")); err != nil || string(data) != "compiled" {
		t.Fatalf("jiti cache = %q, %v", data, err)
	}

	// What the session compiles, such as extensions of its own, stays in its folder.
	if err := os.WriteFile(filepath.Join(session, "tmp", "jiti", "session.mjs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "jiti", "session.mjs")); !os.IsNotExist(err) {
		t.Fatalf("the session's compiled code reached the gateway's cache: %v", err)
	}

	// A cache that links elsewhere could hold anything.
	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(base, "jiti"), filepath.Join(linked, "jiti")); err != nil {
		t.Fatal(err)
	}
	linkedFolders, err := openScratchFolders(linked)
	if err != nil {
		t.Fatal(err)
	}
	defer linkedFolders.Close()
	if session, err = linkedFolders.create(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(session, "tmp", "jiti")); !os.IsNotExist(err) {
		t.Fatalf("a linked jiti cache was copied: %v", err)
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
