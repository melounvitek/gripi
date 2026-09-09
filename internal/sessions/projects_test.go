package sessions

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProjectsPreserveExistingDirectoriesWithoutImportingLaterCLISessions(t *testing.T) {
	for _, test := range []struct {
		name     string
		existing []*Session
	}{
		{name: "empty gateway"},
		{name: "existing projects", existing: []*Session{{CWD: "/existing"}, {CWD: "/existing"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			newState := func() *GatewayState {
				return NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pins.json"), filepath.Join(root, "tags.json"), root)
			}
			existing := test.existing
			state := newState()
			projects, err := state.ProjectCWDs(existing)
			if err != nil || projects["/existing"] != (len(existing) > 0) {
				t.Fatalf("initial projects = %v, %v", projects, err)
			}
			all := append(append([]*Session{}, existing...), &Session{CWD: "/cli-only"})
			projects, err = newState().ProjectCWDs(all)
			if err != nil || projects["/cli-only"] {
				t.Fatalf("restart imported CLI project: %v, %v", projects, err)
			}
			if _, err := state.RememberProject("/gateway"); err != nil {
				t.Fatal(err)
			}
			if _, err := state.RememberProject("/cli-only"); err != nil {
				t.Fatal(err)
			}
			projects, err = newState().ProjectCWDs(all)
			want := map[string]bool{"/gateway": true, "/cli-only": true}
			if len(existing) > 0 {
				want["/existing"] = true
			}
			if err != nil || !reflect.DeepEqual(projects, want) {
				t.Fatalf("adopted projects after restart = %v, %v", projects, err)
			}
		})
	}
}

func TestProjectRollbackPreservesLaterGatewayAdoption(t *testing.T) {
	for _, laterAdoption := range []bool{false, true} {
		root := t.TempDir()
		state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pins.json"), filepath.Join(root, "tags.json"), root)
		rollback, err := state.RememberProject("/new")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := state.RememberProject("/other"); err != nil {
			t.Fatal(err)
		}
		if laterAdoption {
			if _, err := state.RememberProject("/new"); err != nil {
				t.Fatal(err)
			}
		}
		if err := rollback(); err != nil {
			t.Fatal(err)
		}
		projects, err := state.ProjectCWDs(nil)
		if err != nil || !projects["/other"] || projects["/new"] != laterAdoption {
			t.Fatalf("later adoption=%t: projects after rollback = %v, %v", laterAdoption, projects, err)
		}
	}
}

func TestProjectsDoNotReplaceMalformedStateWithDiscoveredDirectories(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "projects.json")
	malformed := []byte("{")
	if err := os.WriteFile(path, malformed, 0600); err != nil {
		t.Fatal(err)
	}
	state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pins.json"), filepath.Join(root, "tags.json"), root)
	if _, err := state.ProjectCWDs([]*Session{{CWD: "/cli-only"}}); err == nil {
		t.Fatal("malformed project state was ignored")
	}
	if _, err := state.RememberProject("/gateway"); err == nil {
		t.Fatal("malformed project state was overwritten")
	}
	assertFileContents(t, path, malformed)
}
