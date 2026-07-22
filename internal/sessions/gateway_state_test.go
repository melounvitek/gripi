package sessions

import (
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
	state := NewGatewayState(path, filepath.Join(t.TempDir(), "pinned.json"))
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
	state := NewGatewayState(filepath.Join(t.TempDir(), "read.json"), path)

	if _, _, err := state.ReadAndObserve(nil, nil, false); err == nil {
		t.Fatal("ReadAndObserve() succeeded")
	}
	if err := state.SetPinned("/session", true); err == nil {
		t.Fatal("SetPinned() succeeded")
	}
	assertFileContents(t, path, malformed)
}

func TestGatewayStateTreatsMissingFilesAsEmpty(t *testing.T) {
	root := t.TempDir()
	state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pinned.json"))
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
