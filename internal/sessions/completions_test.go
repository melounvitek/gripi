package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func completionState(t *testing.T) *GatewayState {
	t.Helper()
	root := t.TempDir()
	return NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pins.json"), filepath.Join(root, "tags.json"), "")
}

func TestCompletionMigrationRollbackPreservesNewerReads(t *testing.T) {
	state := completionState(t)
	if err := state.BeginCompletion("pending", 0); err != nil {
		t.Fatal(err)
	}
	completion := Completion{ID: "final", Preview: "Done", ResponseCount: 2}
	if _, err := state.Complete("pending", completion); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkRead("pending", 2); err != nil {
		t.Fatal(err)
	}
	rollback, err := state.MigrateCompletion("pending", "real")
	if err != nil {
		t.Fatal(err)
	}
	if count, err := state.ReadCount("real"); err != nil || count != 2 {
		t.Fatalf("migrated read=%d, %v", count, err)
	}
	if err := state.MarkRead("real", 3); err != nil {
		t.Fatal(err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	if count, err := state.ReadCount("real"); err != nil || count != 3 {
		t.Fatalf("newer read=%d, %v", count, err)
	}
	values, err := state.Completions()
	if err != nil || values["pending"] != completion || len(values) != 1 {
		t.Fatalf("rolled back completions=%v, %v", values, err)
	}
}

func TestCompletionMigrationWriteFailureDoesNotChangeReadState(t *testing.T) {
	state := completionState(t)
	dir := t.TempDir()
	state.completionsPath = filepath.Join(dir, "completions.json")
	if err := state.BeginCompletion("pending", 0); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkRead("pending", 2); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	if _, err := state.MigrateCompletion("pending", "real"); err == nil {
		t.Fatal("expected completion write failure")
	}
	if count, err := state.ReadCount("real"); err != nil || count != 0 {
		t.Fatalf("failed migration changed destination read to %d, %v", count, err)
	}
}

func TestMalformedCompletionsAreNotRewritten(t *testing.T) {
	state := completionState(t)
	original := []byte("{broken")
	if err := os.WriteFile(state.completionsPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := state.BeginCompletion("session", 0); err == nil {
		t.Fatal("begin accepted malformed state")
	}
	if _, err := state.Complete("session", Completion{ID: "reply"}); err == nil {
		t.Fatal("complete accepted malformed state")
	}
	if _, _, err := state.ReadAndObserve([]*Session{{Path: "session", AssistantResponseCount: 1}}, nil, false, nil); err == nil {
		t.Fatal("sidebar accepted malformed state")
	}
	contents, err := os.ReadFile(state.completionsPath)
	if err != nil || string(contents) != string(original) {
		t.Fatalf("malformed state overwritten: %q, %v", contents, err)
	}
}

func TestCompletionBoundaryPreservesExternalTakeoverReadCount(t *testing.T) {
	state := completionState(t)
	if err := state.BeginCompletion("session", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Complete("session", Completion{ID: "old", ResponseCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkExternalRead("session", 4); err != nil {
		t.Fatal(err)
	}
	if err := state.BeginCompletion("session", 4); err != nil {
		t.Fatal(err)
	}
	session := &Session{Path: "session", AssistantResponseCount: 5}
	unread, _, err := state.ReadAndObserve([]*Session{session}, nil, false, nil)
	if err != nil || unread[session.Path] {
		t.Fatalf("progress after takeover unread=%v, %v", unread, err)
	}
	if count, err := state.ReadCount(session.Path); err != nil || count != 4 {
		t.Fatalf("takeover boundary=%d, %v", count, err)
	}
	if _, err := state.Complete("session", Completion{ID: "new", ResponseCount: 5}); err != nil {
		t.Fatal(err)
	}
	unread, _, err = state.ReadAndObserve([]*Session{session}, nil, false, nil)
	if err != nil || !unread[session.Path] {
		t.Fatalf("settled after takeover unread=%v, %v", unread, err)
	}
}

func TestForgetRemovesCompletionAcrossRestart(t *testing.T) {
	state := completionState(t)
	if err := state.BeginCompletion("session", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Complete("session", Completion{ID: "reply", ResponseCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := state.Forget("session"); err != nil {
		t.Fatal(err)
	}
	state = NewGatewayState(state.readPath, state.pinnedPath, state.tagsPath, "")
	values, err := state.Completions()
	if err != nil || len(values) != 0 {
		t.Fatalf("forgotten completion returned: %v, %v", values, err)
	}
}
