package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeTag(t *testing.T) {
	for _, test := range []struct{ input, expected string }{{"  Work  ", "work"}, {"ÉCOLE", "école"}, {strings.Repeat("界", 64), strings.Repeat("界", 64)}, {"two words", "two words"}} {
		actual, err := NormalizeTag(test.input)
		if err != nil || actual != test.expected {
			t.Fatalf("NormalizeTag(%q) = %q, %v", test.input, actual, err)
		}
	}
	for _, input := range []string{"", "  ", "a\nb", "\twork", "work\r", "a\x00b", "a\x7fb", strings.Repeat("界", 65)} {
		if _, err := NormalizeTag(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestSessionTagsNormalizePathsAndForgetOnlyTarget(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	configured := filepath.Join(root, "configured")
	if err := os.Mkdir(physical, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, configured); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configured, "session.jsonl")
	tagsPath := filepath.Join(root, "tags.json")
	state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pinned.json"), tagsPath, configured)
	for _, assignment := range []struct{ path, name string }{{filepath.Join(physical, "session.jsonl"), " Work "}, {path, "WORK"}, {path, "alpha"}, {"/other", "work"}} {
		if err := state.SetTag(assignment.path, assignment.name, true); err != nil {
			t.Fatal(err)
		}
	}
	tags, err := state.SessionTags()
	expected := map[string][]string{path: {"alpha", "work"}, "/other": {"work"}}
	if err != nil || !reflect.DeepEqual(tags, expected) {
		t.Fatalf("tags = %v, %v", tags, err)
	}
	tags[path][0] = "changed"
	tags, err = state.SessionTags()
	if err != nil || !reflect.DeepEqual(tags, expected) {
		t.Fatalf("mutable snapshot = %v, %v", tags, err)
	}
	if err := state.Forget(path); err != nil {
		t.Fatal(err)
	}
	tags, err = state.SessionTags()
	if err != nil || !reflect.DeepEqual(tags, map[string][]string{"/other": {"work"}}) {
		t.Fatalf("forgotten tags = %v, %v", tags, err)
	}
	info, err := os.Stat(tagsPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions = %v, %v", info, err)
	}
}

func TestSessionTagsLimitAllowsIdempotentAssignmentsAndRemoval(t *testing.T) {
	root := t.TempDir()
	state := NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pins"), filepath.Join(root, "tags"), "")
	for i := range 32 {
		if err := state.SetTag("/session", fmt.Sprint(i), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetTag("/session", "extra", true); err == nil {
		t.Fatal("accepted 33 tags")
	}
	if err := state.SetTag("/session", "0", true); err != nil {
		t.Fatal(err)
	}
	if err := state.SetTag("/session", "0", false); err != nil {
		t.Fatal(err)
	}
	if err := state.SetTag("/session", "extra", true); err != nil {
		t.Fatal(err)
	}
}

func TestSessionTagsPreserveMalformedStateDuringForget(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tags.json")
	malformed := []byte(`{"session":`)
	if err := os.WriteFile(path, malformed, 0600); err != nil {
		t.Fatal(err)
	}
	state := NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pins"), path, "")
	if _, err := state.SessionTags(); err == nil {
		t.Fatal("read malformed tags")
	}
	if err := state.SetTag("/session", "work", true); err == nil {
		t.Fatal("updated malformed tags")
	}
	if err := state.Forget("/session"); err == nil {
		t.Fatal("forgot malformed tags")
	}
	assertFileContents(t, path, malformed)
}
