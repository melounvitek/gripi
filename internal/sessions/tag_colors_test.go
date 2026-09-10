package sessions

import (
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTagColorState(t *testing.T) *GatewayState {
	t.Helper()
	root := t.TempDir()
	return NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pins.json"), filepath.Join(root, "session-tags.json"), "")
}

func tagColors(t *testing.T, state *GatewayState) map[string]string {
	t.Helper()
	colors, err := state.TagColors()
	if err != nil {
		t.Fatal(err)
	}
	return colors
}

func TestTagColorsDeterministicMigration(t *testing.T) {
	for range 10 {
		state := newTagColorState(t)
		original := []byte(`{"/z":[" Zebra ","ALPHA"],"/a":["alpha"]}`)
		if err := os.WriteFile(state.tagsPath, original, 0600); err != nil {
			t.Fatal(err)
		}
		expected := map[string]string{"alpha": "#e69cff", "zebra": "#5ff5ce"}
		if colors := tagColors(t, state); !maps.Equal(colors, expected) {
			t.Fatalf("migration = %v, want %v", colors, expected)
		}
		assertFileContents(t, state.tagsPath, original)
		path := filepath.Join(filepath.Dir(state.tagsPath), "tag-colors.json")
		var stored map[string]string
		if err := readJSON(path, &stored); err != nil || !maps.Equal(stored, expected) {
			t.Fatalf("stored colors = %v, %v", stored, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("color file permissions = %v, %v", info, err)
		}
	}
}

func TestTagColorsReserveExistingNamesBeforeRemovalAndNewAssignments(t *testing.T) {
	for _, operation := range []string{"remove", "insert", "replace", "forget"} {
		t.Run(operation, func(t *testing.T) {
			state := newTagColorState(t)
			if err := writeJSON(state.tagsPath, map[string][]string{"/session": {"zebra", "work", "zz"}}); err != nil {
				t.Fatal(err)
			}
			var err error
			switch operation {
			case "remove":
				err = state.SetTag("/session", "work", false)
			case "insert":
				err = state.SetTag("/session", "alpha", true)
			case "replace":
				_, err = state.SetTags("/session", []string{"alpha"})
			case "forget":
				err = state.Forget("/session")
			}
			if err != nil {
				t.Fatal(err)
			}
			colors := tagColors(t, state)
			if colors["work"] != "#e69cff" || colors["zebra"] != "#5ff5ce" {
				t.Fatalf("existing reservations lost: %v", colors)
			}
		})
	}
}

func TestTagColorsStabilityAndLifecycleRollback(t *testing.T) {
	state := newTagColorState(t)
	if _, err := state.SetTags("/parent", []string{" Work ", "alpha"}); err != nil {
		t.Fatal(err)
	}
	initial := tagColors(t, state)
	rollback, err := state.SetTags("/parent", []string{"temporary"})
	if err != nil {
		t.Fatal(err)
	}
	reserved := tagColors(t, state)
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func(string, string) (func() error, error){state.CopyTags, state.MigrateTags} {
		rollback, err := operation("/parent", "/child")
		if err != nil {
			t.Fatal(err)
		}
		if err := rollback(); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.Forget("/parent"); err != nil {
		t.Fatal(err)
	}
	if colors := tagColors(t, state); !maps.Equal(colors, reserved) {
		t.Fatalf("reservations changed after rollback/removal: %v, want %v", colors, reserved)
	}
	state = NewGatewayState(state.readPath, state.pinnedPath, state.tagsPath, "")
	if colors := tagColors(t, state); !maps.Equal(colors, reserved) {
		t.Fatalf("reservations changed after reload: %v", colors)
	}
	if _, err := state.SetTags("/new", []string{"WORK", "temporary", "new"}); err != nil {
		t.Fatal(err)
	}
	colors := tagColors(t, state)
	for name, color := range reserved {
		if colors[name] != color {
			t.Fatalf("reservation for %s changed: %v", name, colors)
		}
		if colors["new"] == color {
			t.Fatalf("reused reserved color: %v", colors)
		}
	}
	if colors["work"] != initial["work"] {
		t.Fatal("re-added tag changed color")
	}
	colors["work"] = "mutated"
	if tagColors(t, state)["work"] != initial["work"] {
		t.Fatal("caller mutated stored colors")
	}
}

func TestTagColorsConcurrentAssignment(t *testing.T) {
	state := newTagColorState(t)
	var group sync.WaitGroup
	for i := range 48 {
		group.Go(func() {
			name := fmt.Sprintf("tag-%02d", i)
			if err := state.SetTag("/"+name, name, true); err != nil {
				t.Error(err)
			}
			if _, err := state.TagColors(); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	colors := tagColors(t, state)
	tags, err := state.SessionTags()
	if err != nil || len(tags) != 48 || len(colors) != 48 {
		t.Fatalf("concurrent assignments: %d tags, %d colors, %v", len(tags), len(colors), err)
	}
	for _, names := range tags {
		if colors[names[0]] == "" {
			t.Fatalf("missing reservation for %s", names[0])
		}
	}
}

func TestTagColorsRejectMalformedStateWithoutChangingFiles(t *testing.T) {
	for _, malformed := range []string{
		`{"work":`, `[]`, `{"work":12}`, `{"work":"#123456"} {}`,
		`{" Work":"#123456"}`, `{"WORK":"#123456"}`, `{"":"#123456"}`,
		`{"bad\n":"#123456"}`, `{"` + strings.Repeat("a", 65) + `":"#123456"}`,
		`{"work":"red"}`, `{"work":"#123"}`, `{"work":"#1234567"}`,
		`{"work":"#gggggg"}`, `{"work":"#123456; color:red"}`, `{"work":null}`,
	} {
		t.Run(malformed, func(t *testing.T) {
			state := newTagColorState(t)
			original := []byte(`{"/session":["work"]}`)
			if err := os.WriteFile(state.tagsPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(filepath.Dir(state.tagsPath), "tag-colors.json")
			if err := os.WriteFile(path, []byte(malformed), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := state.TagColors(); err == nil {
				t.Fatal("accepted malformed colors")
			}
			if err := state.SetTag("/session", "new", true); err == nil {
				t.Fatal("assigned tag with malformed colors")
			}
			if _, err := state.SetTags("/session", []string{"new"}); err == nil {
				t.Fatal("assigned batch with malformed colors")
			}
			assertFileContents(t, path, []byte(malformed))
			assertFileContents(t, state.tagsPath, original)
		})
	}
}

func TestTagColorsPreserveValidAllocationsAndChooseDistinctUnusedColor(t *testing.T) {
	// Discover the palette through allocations rather than coupling the test to its size.
	state := newTagColorState(t)
	palette := make(map[string]bool)
	counts := make(map[string]int)
	exhausted := false
	for i := range 100 {
		name := fmt.Sprintf("tag-%03d", i)
		if err := state.SetTag("/"+name, name, true); err != nil {
			t.Fatal(err)
		}
		color := tagColors(t, state)[name]
		if counts[color] > 0 {
			exhausted = true
			if len(palette) < 8 {
				t.Fatalf("palette reused too early: %v", palette)
			}
			for candidate, count := range counts {
				if counts[color] > count {
					t.Fatalf("reused %s (%d) instead of %s (%d)", color, counts[color], candidate, count)
				}
			}
		} else if exhausted {
			t.Fatalf("unused color %s discovered after reuse", color)
		}
		palette[color] = true
		counts[color]++
	}
	if !exhausted {
		t.Fatal("did not reach palette exhaustion")
	}

	state = newTagColorState(t)
	stored := map[string]string{"custom": "#123ABC", "purple": "#E69CFF"}
	path := filepath.Join(filepath.Dir(state.tagsPath), "tag-colors.json")
	original := []byte("{\n \"custom\": \"#123ABC\", \"purple\": \"#E69CFF\"\n}\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(state.tagsPath, map[string][]string{"/session": {"custom", "purple"}}); err != nil {
		t.Fatal(err)
	}
	if colors := tagColors(t, state); !maps.Equal(colors, stored) {
		t.Fatalf("valid reservations changed: %v", colors)
	}
	assertFileContents(t, path, original)
	if err := writeJSON(state.tagsPath, map[string][]string{"/session": {"purple", "new"}}); err != nil {
		t.Fatal(err)
	}
	colors := tagColors(t, state)
	for name, color := range stored {
		if colors[name] != color {
			t.Fatalf("valid allocation overwritten: %v", colors)
		}
	}
	if !palette[colors["new"]] || strings.EqualFold(colors["new"], stored["purple"]) {
		t.Fatalf("not an unused palette color: %v", colors)
	}
	distance := func(color string) int {
		a, err := hex.DecodeString(color[1:])
		if err != nil {
			t.Fatal(err)
		}
		nearest := 3*255*255 + 1
		for _, assigned := range stored {
			b, _ := hex.DecodeString(assigned[1:])
			squared := 0
			for i := range a {
				delta := int(a[i]) - int(b[i])
				squared += delta * delta
			}
			nearest = min(nearest, squared)
		}
		return nearest
	}
	for candidate := range palette {
		if distance(candidate) > distance(colors["new"]) {
			t.Fatalf("%s is more distinct than allocated %s", candidate, colors["new"])
		}
	}
}

func TestTagColorsWriteFailurePreventsAssignments(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	state := newTagColorState(t)
	original := []byte(`{"/session":["work"]}`)
	if err := os.WriteFile(state.tagsPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(state.tagsPath)
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(root, 0700) })
	if _, err := state.TagColors(); err == nil || !strings.Contains(err.Error(), "write tag colors") {
		t.Fatalf("migration write error = %v", err)
	}
	if err := state.SetTag("/session", "new", true); err == nil || !strings.Contains(err.Error(), "write tag colors") {
		t.Fatalf("assignment write error = %v", err)
	}
	assertFileContents(t, state.tagsPath, original)
}

func TestTagColorsReservationSurvivesAssignmentWriteFailure(t *testing.T) {
	state := newTagColorState(t)
	// Force the assignment rename to fail after the reservation has been written.
	if err := os.Mkdir(state.tagsPath, 0700); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	_, err := state.replaceTags(map[string][]string{}, map[string][]string{"/session": {"reserved"}})
	state.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "write session tags") {
		t.Fatalf("assignment error = %v", err)
	}
	var stored map[string]string
	if err := readJSON(filepath.Join(filepath.Dir(state.tagsPath), "tag-colors.json"), &stored); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(stored, map[string]string{"reserved": "#e69cff"}) {
		t.Fatalf("reservation was not persisted before assignment: %v", stored)
	}
	if err := os.Remove(state.tagsPath); err != nil {
		t.Fatal(err)
	}
	if colors := tagColors(t, state); !maps.Equal(colors, stored) {
		t.Fatalf("failed assignment lost reservation: %v", colors)
	}
}

func TestTagColorsMalformedAssignmentsAndEmptyPath(t *testing.T) {
	state := newTagColorState(t)
	if err := os.WriteFile(state.tagsPath, []byte(`{"/session":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.TagColors(); err == nil {
		t.Fatal("accepted malformed assignments")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(state.tagsPath), "tag-colors.json")); !os.IsNotExist(err) {
		t.Fatalf("created reservations for malformed assignments: %v", err)
	}
	t.Chdir(t.TempDir())
	state = NewGatewayState("", "", "", "")
	if colors := tagColors(t, state); len(colors) != 0 {
		t.Fatalf("unconfigured colors = %v", colors)
	}
	if err := state.SetTag("/session", "work", true); err == nil {
		t.Fatal("assignment unexpectedly succeeded without a tags path")
	}
	if _, err := os.Stat("tag-colors.json"); !os.IsNotExist(err) {
		t.Fatalf("created colors in working directory: %v", err)
	}
}
