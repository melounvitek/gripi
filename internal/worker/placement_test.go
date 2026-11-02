package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlacementsFindTheSessionsOnWorkersAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-placements.json")
	placements, err := LoadPlacements(path)
	if err != nil {
		t.Fatal(err)
	}
	placement := Placement{Session: "/sessions/a.jsonl", Move: "m1", Worker: "beelink", Cwd: "/home/me/Work/app", Repository: "/home/me/Work/app/.git", Roots: []string{"/home/me/Work/app"}, Private: []string{"/home/me/Work"}}
	if err := placements.Add(placement); err != nil {
		t.Fatal(err)
	}
	if err := placements.Add(placement); !refused(err) {
		t.Fatalf("Add of a placed session = %v", err)
	}
	if err := placements.SetStage(placement.Session, releasing); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadPlacements(path)
	if err != nil {
		t.Fatal(err)
	}
	if found, ok := reloaded.Find(placement.Session); !ok || found.Move != "m1" || found.Stage != releasing || found.Roots[0] != "/home/me/Work/app" {
		t.Fatalf("Find = %+v, %v", found, ok)
	}
	if _, ok := reloaded.Find("/sessions/b.jsonl"); ok {
		t.Fatal("found a session on main")
	}
	if err := reloaded.Finish(placement.Session, &Notice{Session: placement.Session, Recovery: "/state/returns/m1"}); err != nil {
		t.Fatal(err)
	}
	again, _ := LoadPlacements(path)
	if len(again.All()) != 0 || len(again.Notices(placement.Session)) != 1 || len(again.Stale("beelink")) != 1 {
		t.Fatalf("after the return: placements %+v, notices %+v, stale %q", again.All(), again.Notices(placement.Session), again.Stale("beelink"))
	}
	if err := again.Deleted("beelink", "m1"); err != nil {
		t.Fatal(err)
	}
	if err := again.Dismiss("/state/returns/m1"); err != nil {
		t.Fatal(err)
	}
	if last, _ := LoadPlacements(path); len(last.Notices(placement.Session)) != 0 || len(last.Stale("beelink")) != 0 {
		t.Fatalf("after the dismissal: notices %+v, stale %q", last.Notices(placement.Session), last.Stale("beelink"))
	}
}

// The previous version of Gripi kept the repositories that it placed on workers in the same file, as a list.
func TestPlacementsTakeOverThePreviousVersionsFileOnlyWithoutRepositoriesOnWorkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-placements.json")
	if err := os.WriteFile(path, []byte("[]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	placements, err := LoadPlacements(path)
	if err != nil || len(placements.All()) != 0 {
		t.Fatalf("LoadPlacements of an empty list = %+v, %v", placements.All(), err)
	}
	placement := Placement{Session: "/sessions/a.jsonl", Move: "m1", Worker: "beelink"}
	if err := placements.Add(placement); err != nil {
		t.Fatal(err)
	}
	if reloaded, err := LoadPlacements(path); err != nil || len(reloaded.All()) != 1 {
		t.Fatalf("LoadPlacements after a save = %+v, %v", reloaded.All(), err)
	}

	previous := `[{"worker": "beelink", "unit": {"main": "/home/me/Work/app", "worktrees": ["/home/me/Work/app-fix"], "git_dir": "/home/me/Work/app/.git"}},
 {"worker": "nuc", "unit": {"main": "/home/me/notes"}}]
`
	if err := os.WriteFile(path, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadPlacements(path)
	if !errors.Is(err, ErrPreviousPlacements) {
		t.Fatalf("LoadPlacements of repositories on workers = %v", err)
	}
	for _, named := range []string{path, "/home/me/Work/app on beelink", "/home/me/notes on nuc", "gripi worker return"} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("the error doesn't name %q: %v", named, err)
		}
	}
	if contents, _ := os.ReadFile(path); string(contents) != previous {
		t.Fatalf("the file changed to %s", contents)
	}
}
