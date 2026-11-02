package worker

import (
	"path/filepath"
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
