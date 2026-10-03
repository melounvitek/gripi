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
			for _, name := range []string{" WORK ", "alpha"} {
				if err := state.SetTag("/parent", name, true); err != nil {
					t.Fatal(err)
				}
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

func TestTagRollbackPreservesNewerDestinationEdit(t *testing.T) {
	root := t.TempDir()
	state := NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pins"), filepath.Join(root, "tags"), "")
	if err := state.SetTag("/parent", "work", true); err != nil {
		t.Fatal(err)
	}
	rollback, err := state.CopyTags("/parent", "/child")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetTag("/child", "newer", true); err != nil {
		t.Fatal(err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	tags, err := state.SessionTags()
	if err != nil || !reflect.DeepEqual(tags, map[string][]string{"/parent": {"work"}, "/child": {"newer", "work"}}) {
		t.Fatalf("newer edit lost=%v %v", tags, err)
	}
}
