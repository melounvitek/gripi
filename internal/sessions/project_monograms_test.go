package sessions

import (
	"maps"
	"path/filepath"
	"testing"
)

func TestProjectMonogramsAreUniqueAndKeptAcrossRestarts(t *testing.T) {
	root := t.TempDir()
	monograms := func(cwds ...string) map[string]string {
		t.Helper()
		state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pins.json"), filepath.Join(root, "tags.json"), root)
		result, err := state.ProjectMonograms(cwds)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	cwds := []string{"/work/gripi", "/work/mixit-admin", "/work/mixit-production-tool", "/work/mixit_api", "/work/mixit-front", "/work/mpt-front", "/work/Čaj", "/work/x"}
	want := map[string]string{
		"/work/gripi":                 "gr", // one word: its first two letters
		"/work/mixit-admin":           "ma", // several words: initials
		"/work/mixit-production-tool": "mp",
		"/work/mixit_api":             "mi", // "ma" is taken: first two letters
		"/work/mixit-front":           "mf",
		"/work/mpt-front":             "mt", // "mf" and "mp" are taken: first letter with the next free one
		"/work/Čaj":                   "ča",
		"/work/x":                     "x",
	}
	if got := monograms(cwds...); !maps.Equal(got, want) {
		t.Fatalf("monograms = %v, want %v", got, want)
	}

	// A project added later takes free letters, even when it is listed first.
	want["/work/mixit-archive"] = "mx"
	if got := monograms(append([]string{"/work/mixit-archive"}, cwds...)...); !maps.Equal(got, want) {
		t.Fatalf("monograms after restart = %v, want %v", got, want)
	}
}

func TestProjectMonogramsRepeatWhenANameHasNoFreeLetters(t *testing.T) {
	root := t.TempDir()
	state := NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pins.json"), filepath.Join(root, "tags.json"), root)
	got, err := state.ProjectMonograms([]string{"/a/ab", "/b/ab"})
	want := map[string]string{"/a/ab": "ab", "/b/ab": "ab"}
	if err != nil || !maps.Equal(got, want) {
		t.Fatalf("monograms = %v, %v, want %v", got, err, want)
	}
}

func TestProjectMonogramOfADirectoryOutsideTheProjectListIsItsInitials(t *testing.T) {
	if got := ProjectMonogram("/elsewhere/mixit-admin"); got != "ma" {
		t.Fatalf("monogram = %q, want \"ma\"", got)
	}
}
