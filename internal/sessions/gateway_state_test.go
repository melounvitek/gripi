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
	state := NewGatewayState(path, filepath.Join(t.TempDir(), "pinned.json"), filepath.Join(t.TempDir(), "tags.json"), "")
	session := &Session{Path: "/session", AssistantResponseCount: 1}

	if _, _, err := state.ReadAndObserve([]*Session{session}, session, true, nil); err == nil {
		t.Fatal("ReadAndObserve() succeeded")
	}
	if err := state.MarkRead(session.Path, 1); err == nil {
		t.Fatal("MarkRead() succeeded")
	}
	if _, err := state.ReadCount(session.Path); err == nil {
		t.Fatal("ReadCount() succeeded")
	}
	assertFileContents(t, path, malformed)
}

func TestGatewayStateReportsThePersistedReadCount(t *testing.T) {
	root := t.TempDir()
	state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pinned.json"), filepath.Join(t.TempDir(), "tags.json"), root)
	path := filepath.Join(root, "session.jsonl")
	if err := state.MarkRead(path, 3); err != nil {
		t.Fatal(err)
	}

	count, err := state.ReadCount(path)
	if err != nil || count != 3 {
		t.Fatalf("ReadCount() = %d, %v", count, err)
	}
}

func TestGatewayStatePreservesMalformedPinnedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinned.json")
	malformed := []byte(`[")`)
	if err := os.WriteFile(path, malformed, 0600); err != nil {
		t.Fatal(err)
	}
	state := NewGatewayState(filepath.Join(t.TempDir(), "read.json"), path, filepath.Join(t.TempDir(), "tags.json"), "")

	if _, _, err := state.ReadAndObserve(nil, nil, false, nil); err == nil {
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
	state := NewGatewayState(readPath, pinnedPath, filepath.Join(t.TempDir(), "tags.json"), configuredRoot)
	session := &Session{Path: configuredPath, AssistantResponseCount: 2}

	unread, pinned, err := state.ReadAndObserve([]*Session{session}, nil, false, nil)
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
	state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pinned.json"), filepath.Join(t.TempDir(), "tags.json"), "")
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

	_, pinned, err := state.ReadAndObserve([]*Session{{Path: "/pending"}, {Path: "/persisted"}, {Path: "/other"}}, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !pinned["/pending"] || pinned["/persisted"] || !pinned["/other"] {
		t.Fatalf("pinned = %v", pinned)
	}
}

func TestGatewayStatePinnedMigrationRollbackPreservesNewerDestinationChange(t *testing.T) {
	for _, test := range []struct {
		name                    string
		pinned                  bool
		expectSourcePinned      bool
		expectDestinationPinned bool
	}{
		{name: "pin", pinned: true, expectSourcePinned: true, expectDestinationPinned: true},
		{name: "unpin", pinned: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pinned.json"), filepath.Join(t.TempDir(), "tags.json"), "")
			if err := state.SetPinned("/pending", true); err != nil {
				t.Fatal(err)
			}
			rollback, err := state.MigratePinned("/pending", "/persisted")
			if err != nil {
				t.Fatal(err)
			}
			if err := state.SetPinned("/persisted", test.pinned); err != nil {
				t.Fatal(err)
			}

			if err := rollback(); err != nil {
				t.Fatal(err)
			}

			_, pinned, err := state.ReadAndObserve([]*Session{{Path: "/pending"}, {Path: "/persisted"}}, nil, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if pinned["/pending"] != test.expectSourcePinned || pinned["/persisted"] != test.expectDestinationPinned {
				t.Fatalf("pinned = %v", pinned)
			}
		})
	}
}

func TestGatewayStateForgetRemovesReadAndPinnedState(t *testing.T) {
	root := t.TempDir()
	readPath := filepath.Join(root, "read.json")
	pinnedPath := filepath.Join(root, "pinned.json")
	sessionPath := filepath.Join(root, "sessions", "session.jsonl")
	state := NewGatewayState(readPath, pinnedPath, filepath.Join(t.TempDir(), "tags.json"), filepath.Join(root, "sessions"))

	if err := state.MarkRead(sessionPath, 3); err != nil {
		t.Fatal(err)
	}
	if err := state.SetPinned(sessionPath, true); err != nil {
		t.Fatal(err)
	}
	if err := state.Forget(sessionPath); err != nil {
		t.Fatal(err)
	}
	if count, err := state.ReadCount(sessionPath); err != nil || count != 0 {
		t.Fatalf("read count = %d, %v", count, err)
	}
	if !state.SessionForgotten(sessionPath) {
		t.Fatal("forgotten session was not tracked")
	}
	stale := &Session{Path: sessionPath, AssistantResponseCount: 5}
	unread, pinned, err := state.ReadAndObserve([]*Session{stale}, stale, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if unread[sessionPath] || pinned[sessionPath] {
		t.Fatalf("forgotten session state was recreated: unread=%v pinned=%v", unread, pinned)
	}
	if count, err := state.ReadCount(sessionPath); err != nil || count != 0 {
		t.Fatalf("read count after stale observation = %d, %v", count, err)
	}
}

func TestGatewayStateTracksForgottenSessionWhenCleanupStateIsMalformed(t *testing.T) {
	root := t.TempDir()
	readPath := filepath.Join(root, "read.json")
	state := NewGatewayState(readPath, filepath.Join(root, "pinned.json"), filepath.Join(t.TempDir(), "tags.json"), "")
	if err := os.WriteFile(readPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := state.Forget("/deleted.jsonl"); err == nil {
		t.Fatal("malformed state cleanup succeeded")
	}
	if !state.SessionForgotten("/deleted.jsonl") {
		t.Fatal("failed cleanup did not retain the deletion tombstone")
	}
}

func TestGatewayStateTreatsMissingFilesAsEmpty(t *testing.T) {
	root := t.TempDir()
	state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pinned.json"), filepath.Join(t.TempDir(), "tags.json"), "")
	session := &Session{Path: "/session", AssistantResponseCount: 1}

	unread, pinned, err := state.ReadAndObserve([]*Session{session}, nil, false, nil)
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
