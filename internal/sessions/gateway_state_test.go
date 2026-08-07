package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGatewayStatePreservesMalformedReadState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.json")
	malformed := []byte(`{"session":`)
	if err := os.WriteFile(path, malformed, 0600); err != nil {
		t.Fatal(err)
	}
	state := NewGatewayState(path, filepath.Join(t.TempDir(), "pinned.json"), "")
	session := &Session{Path: "/session", AssistantResponseCount: 1}

	if _, _, err := state.ReadAndObserve([]*Session{session}, session, true); err == nil {
		t.Fatal("ReadAndObserve() succeeded")
	}
	if err := state.MarkRead(session.Path, 1); err == nil {
		t.Fatal("MarkRead() succeeded")
	}
	assertFileContents(t, path, malformed)
}

func TestGatewayStatePreservesMalformedPinnedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinned.json")
	malformed := []byte(`[")`)
	if err := os.WriteFile(path, malformed, 0600); err != nil {
		t.Fatal(err)
	}
	state := NewGatewayState(filepath.Join(t.TempDir(), "read.json"), path, "")

	if _, _, err := state.ReadAndObserve(nil, nil, false); err == nil {
		t.Fatal("ReadAndObserve() succeeded")
	}
	if err := state.SetPinned("/session", true); err == nil {
		t.Fatal("SetPinned() succeeded")
	}
	assertFileContents(t, path, malformed)
}

func TestGatewayStateNormalizesPhysicalReadAndPinnedPaths(t *testing.T) {
	root := t.TempDir()
	physicalRoot := filepath.Join(root, "physical-sessions")
	configuredRoot := filepath.Join(root, "configured-sessions")
	if err := os.Mkdir(physicalRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physicalRoot, configuredRoot); err != nil {
		t.Fatal(err)
	}
	physicalPath := filepath.Join(physicalRoot, "session.jsonl")
	configuredPath := filepath.Join(configuredRoot, "session.jsonl")
	readPath := filepath.Join(root, "read.json")
	pinnedPath := filepath.Join(root, "pinned.json")
	counts, _ := json.Marshal(map[string]int{physicalPath: 2})
	pinnedPaths, _ := json.Marshal([]string{physicalPath})
	if err := os.WriteFile(readPath, counts, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pinnedPath, pinnedPaths, 0600); err != nil {
		t.Fatal(err)
	}
	state := NewGatewayState(readPath, pinnedPath, configuredRoot)
	session := &Session{Path: configuredPath, AssistantResponseCount: 2}

	unread, pinned, err := state.ReadAndObserve([]*Session{session}, nil, false)
	if err != nil || unread[configuredPath] || !pinned[configuredPath] {
		t.Fatalf("unread=%v pinned=%v err=%v", unread, pinned, err)
	}
	var persisted map[string]int
	contents, err := os.ReadFile(readPath)
	if err != nil || json.Unmarshal(contents, &persisted) != nil || persisted[configuredPath] != 2 || len(persisted) != 1 {
		t.Fatalf("persisted counts = %#v, %v", persisted, err)
	}
	if err := state.SetPinned(configuredPath, false); err != nil {
		t.Fatal(err)
	}
	var paths []string
	contents, err = os.ReadFile(pinnedPath)
	if err != nil || json.Unmarshal(contents, &paths) != nil || len(paths) != 0 {
		t.Fatalf("persisted pins = %#v, %v", paths, err)
	}
}

func TestGatewayStatePinnedMigrationRollbackPreservesNewerChanges(t *testing.T) {
	root := t.TempDir()
	state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pinned.json"), "")
	if err := state.SetPinned("/pending", true); err != nil {
		t.Fatal(err)
	}
	rollback, err := state.MigratePinned("/pending", "/persisted")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetPinned("/other", true); err != nil {
		t.Fatal(err)
	}

	if err := rollback(); err != nil {
		t.Fatal(err)
	}

	_, pinned, err := state.ReadAndObserve([]*Session{{Path: "/pending"}, {Path: "/persisted"}, {Path: "/other"}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !pinned["/pending"] || pinned["/persisted"] || !pinned["/other"] {
		t.Fatalf("pinned = %v", pinned)
	}
}

func TestGatewayStateTreatsMissingFilesAsEmpty(t *testing.T) {
	root := t.TempDir()
	state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pinned.json"), "")
	session := &Session{Path: "/session", AssistantResponseCount: 1}

	unread, pinned, err := state.ReadAndObserve([]*Session{session}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if unread[session.Path] || pinned[session.Path] {
		t.Fatalf("unread=%v pinned=%v", unread, pinned)
	}
}

func assertFileContents(t *testing.T, path string, expected []byte) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != string(expected) {
		t.Fatalf("contents = %q, want %q", contents, expected)
	}
}
