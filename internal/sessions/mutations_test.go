package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDeleteSessionFileUsesTrashWhenAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
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
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
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

func TestAttachmentStoreDeleteRemovesSessionMetadataAndImages(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "session.jsonl")
	store := AttachmentStore{Root: filepath.Join(root, "attachments"), SessionsRoot: filepath.Join(root, "sessions")}
	metadataPath := filepath.Join(store.Root, SessionHash(sessionPath)+".jsonl")
	imagesPath := filepath.Join(store.Root, SessionHash(sessionPath))
	if err := os.MkdirAll(imagesPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, []byte("metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imagesPath, "image.png"), []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(sessionPath); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{metadataPath, imagesPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("attachment path remains at %s: %v", path, err)
		}
	}
}
