package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAttachmentMatchReadsMetadataStoredForThePhysicalSessionPath(t *testing.T) {
	root := t.TempDir()
	physicalRoot := filepath.Join(root, "physical-sessions")
	configuredRoot := filepath.Join(root, "configured-sessions")
	attachmentsRoot := filepath.Join(root, "attachments")
	for _, path := range []string{physicalRoot, attachmentsRoot} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(physicalRoot, configuredRoot); err != nil {
		t.Fatal(err)
	}
	physicalPath := filepath.Join(physicalRoot, "session.jsonl")
	configuredPath := filepath.Join(configuredRoot, "session.jsonl")
	physicalHash := SessionHash(physicalPath)
	imageDirectory := filepath.Join(attachmentsRoot, physicalHash)
	if err := os.Mkdir(imageDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(imageDirectory, "image.png")
	if err := os.WriteFile(image, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(Attachment{MessageHash: MessageHash("prompt"), Count: 1, Paths: []string{image}, MIMETypes: []string{"image/png"}})
	metadata := append(append(append([]byte{}, record...), '\n'), record...)
	if err := os.WriteFile(filepath.Join(attachmentsRoot, physicalHash+".jsonl"), append(metadata, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	first := &Message{Role: "user", Text: "prompt"}
	second := &Message{Role: "user", Text: "prompt"}

	matches := (AttachmentStore{Root: attachmentsRoot, SessionsRoot: configuredRoot}).Match(configuredPath, []*Message{first, second})
	for _, message := range []*Message{first, second} {
		match, ok := matches[message]
		if !ok || len(match.Images) != 1 || match.Images[0].Src != "/attachments/"+physicalHash+"/image.png" {
			t.Fatalf("match = %#v, %v", match, ok)
		}
	}
}

func TestAttachmentMigrationMovesImagePathsAndCanRollback(t *testing.T) {
	root := t.TempDir()
	store := AttachmentStore{Root: root}
	from, to := "/pending", "/real"
	fromDirectory := filepath.Join(root, SessionHash(from))
	if err := os.Mkdir(fromDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	fromImage := filepath.Join(fromDirectory, "image.png")
	if err := os.WriteFile(fromImage, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(Attachment{MessageHash: "message", Count: 1, Paths: []string{fromImage}, MIMETypes: []string{"image/png"}})
	if err := os.WriteFile(filepath.Join(root, SessionHash(from)+".jsonl"), append(record, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	rollback, err := store.Migrate(from, to)
	if err != nil {
		t.Fatal(err)
	}
	toImage := filepath.Join(root, SessionHash(to), "image.png")
	metadata := store.read(to)
	if len(metadata) != 1 || len(metadata[0].Paths) != 1 || metadata[0].Paths[0] != toImage {
		t.Fatalf("migrated metadata = %#v", metadata)
	}
	if contents, err := os.ReadFile(toImage); err != nil || string(contents) != "image" {
		t.Fatalf("migrated image = %q, %v", contents, err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(toImage); !os.IsNotExist(err) {
		t.Fatalf("rolled back image error = %v", err)
	}
}

func TestAttachmentMigrationCanRollbackCommittedReplacement(t *testing.T) {
	root := t.TempDir()
	store := AttachmentStore{Root: root}
	from, to := "/pending", "/real"
	fromPath := filepath.Join(root, SessionHash(from)+".jsonl")
	toPath := filepath.Join(root, SessionHash(to)+".jsonl")
	if err := os.WriteFile(fromPath, []byte("pending\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toPath, []byte("existing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rollback, err := store.Migrate(from, to)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := os.ReadFile(toPath)
	if err != nil || string(migrated) != "existing\npending\n" {
		t.Fatalf("migrated = %q, %v", migrated, err)
	}
	secondRollback, err := store.Migrate(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if secondRollback != nil {
		t.Fatal("repeated migration was not idempotent")
	}
	unchanged, err := os.ReadFile(toPath)
	if err != nil || string(unchanged) != "existing\npending\n" {
		t.Fatalf("repeated contents = %q, %v", unchanged, err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(toPath)
	if err != nil || string(restored) != "existing\n" {
		t.Fatalf("restored = %q, %v", restored, err)
	}
}
