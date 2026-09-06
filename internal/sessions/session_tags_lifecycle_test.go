package sessions

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestTagsCopyMigrationAndRollbackPreserveOtherEdits(t *testing.T) {
	for _, move := range []bool{false, true} {
		t.Run(map[bool]string{false: "copy", true: "migrate"}[move], func(t *testing.T) {
			root := t.TempDir()
			state := NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pins"), filepath.Join(root, "tags"), "")
			if _, err := state.SetTags("/parent", []string{" WORK ", "alpha", "work"}); err != nil {
				t.Fatal(err)
			}
			if err := state.SetTag("/child", "existing", true); err != nil {
				t.Fatal(err)
			}
			operation := state.CopyTags
			if move {
				operation = state.MigrateTags
			}
			rollback, err := operation("/parent", "/child")
			if err != nil {
				t.Fatal(err)
			}
			tags, err := state.SessionTags()
			expected := map[string][]string{"/parent": {"alpha", "work"}, "/child": {"alpha", "work"}}
			if move {
				delete(expected, "/parent")
				expected["/child"] = []string{"alpha", "existing", "work"}
			}
			if err != nil || !reflect.DeepEqual(tags, expected) {
				t.Fatalf("tags=%v, %v", tags, err)
			}
			if err := state.SetTag("/other", "newer", true); err != nil {
				t.Fatal(err)
			}
			if err := rollback(); err != nil {
				t.Fatal(err)
			}
			tags, err = state.SessionTags()
			expected = map[string][]string{"/parent": {"alpha", "work"}, "/child": {"existing"}, "/other": {"newer"}}
			if err != nil || !reflect.DeepEqual(tags, expected) {
				t.Fatalf("rollback tags=%v, %v", tags, err)
			}
		})
	}
}

func TestSetTagsIsAtomicAndRollbackPreservesNewerDestinationEdit(t *testing.T) {
	root := t.TempDir()
	state := NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pins"), filepath.Join(root, "tags"), "")
	if _, err := state.SetTags("/session", []string{"work", "bad\n"}); err == nil {
		t.Fatal("accepted invalid batch")
	}
	tags, err := state.SessionTags()
	if err != nil || len(tags) != 0 {
		t.Fatalf("partial batch=%v %v", tags, err)
	}
	rollback, err := state.SetTags("/session", []string{"work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetTag("/session", "work", true); err != nil {
		t.Fatal(err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	tags, err = state.SessionTags()
	if err != nil || !reflect.DeepEqual(tags, map[string][]string{"/session": {"work"}}) {
		t.Fatalf("newer edit lost=%v %v", tags, err)
	}
}
