package worker

import (
	"path/filepath"
	"testing"

	"github.com/melounvitek/gripi/internal/worker/mirror"
)

func TestPlacementsFindTheUnitOfAFolderAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-placements.json")
	placements, err := LoadPlacements(path)
	if err != nil {
		t.Fatal(err)
	}
	unit := mirror.Unit{Main: "/work/project", Worktrees: []string{"/work/.worktrees/project/feature"}, GitDir: "/work/project/.git"}
	if err := placements.Add(Placement{Worker: "beelink", Unit: unit}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadPlacements(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"/work/project", "/work/project/src", "/work/.worktrees/project/feature/docs"} {
		if found, ok := reloaded.Find(folder); !ok || found.Worker != "beelink" || found.Unit.Main != unit.Main {
			t.Fatalf("Find(%s) = %+v, %v", folder, found, ok)
		}
	}
	for _, folder := range []string{"/work/project-other", "/work", "/work/.worktrees/project"} {
		if found, ok := reloaded.Find(folder); ok {
			t.Fatalf("Find(%s) = %+v", folder, found)
		}
	}
	if err := reloaded.Add(Placement{Worker: "other", Unit: mirror.Unit{Main: "/work/project/src"}}); err == nil {
		t.Fatal("a folder inside a placed unit was placed again")
	}
	if err := reloaded.Add(Placement{Worker: "other", Unit: mirror.Unit{Main: "/work/.worktrees"}}); !refused(err) {
		t.Fatalf("Add of a folder that holds a placed worktree = %v", err)
	}

	if err := reloaded.Remove(unit.Main); err != nil {
		t.Fatal(err)
	}
	if again, _ := LoadPlacements(path); len(again.All()) != 0 {
		t.Fatalf("placements after removing = %+v", again.All())
	}
}
