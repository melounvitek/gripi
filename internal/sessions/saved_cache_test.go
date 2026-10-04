package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSavedCacheListsUnchangedSessionsWithoutReadingThem(t *testing.T) {
	root, project, path := sessionFixture(t)
	valid := []string{sessionLine(project), userLine("user", "", "2026-01-01T00:00:01Z", "Saved question")}
	writeSessionLines(t, path, valid)
	invalidPath := filepath.Join(root, "invalid.jsonl")
	becomesValid := strings.Join(valid, "\n") + "\n"
	if err := os.WriteFile(invalidPath, []byte(strings.Repeat("x", len(becomesValid))), 0600); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(t.TempDir(), "state", "cache.json")
	first := Store{Root: root, Home: root, Cache: LoadCache(cachePath)}
	before, err := first.Sessions()
	if err != nil || len(before) != 1 || before[0].Path != path {
		t.Fatalf("sessions before saving = %#v, err = %v", before, err)
	}
	if err := first.Cache.Save(); err != nil {
		t.Fatal(err)
	}

	// Swap which file is readable, so any file read again changes the listing.
	replaceKeepingFileSignature(t, path, strings.Repeat("x", len(becomesValid)))
	replaceKeepingFileSignature(t, invalidPath, becomesValid)
	if fresh, err := (Store{Root: root, Home: root, Cache: NewCache()}).Sessions(); err != nil || len(fresh) != 1 || fresh[0].Path != invalidPath {
		t.Fatalf("sessions read again = %#v, err = %v", fresh, err)
	}

	second := Store{Root: root, Home: root, Cache: LoadCache(cachePath)}
	after, err := second.Sessions()
	if err != nil || len(after) != 1 {
		t.Fatalf("sessions after loading = %#v, err = %v", after, err)
	}
	if sessionInUTC(after[0]) != sessionInUTC(before[0]) {
		t.Fatalf("loaded session = %#v, want %#v", after[0], before[0])
	}
}

func TestSavedCacheReadsSessionsChangedAfterTheSaveAgain(t *testing.T) {
	root, project, path := sessionFixture(t)
	writeSessionLines(t, path, []string{sessionLine(project), userLine("first", "", "2026-01-01T00:00:01Z", "Saved question")})
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	first := Store{Root: root, Home: root, Cache: LoadCache(cachePath)}
	if _, err := first.Sessions(); err != nil {
		t.Fatal(err)
	}
	if err := first.Cache.Save(); err != nil {
		t.Fatal(err)
	}
	appendSessionLine(t, path, userLine("second", "first", "2026-01-01T00:00:02Z", "Later question"))

	second := Store{Root: root, Home: root, Cache: LoadCache(cachePath)}
	listed, err := second.Sessions()
	if err != nil || len(listed) != 1 || listed[0].MessageCount != 2 {
		t.Fatalf("sessions after the change = %#v, err = %v", listed, err)
	}
}

func TestUnusableSavedCacheIsIgnoredAndReplaced(t *testing.T) {
	tests := map[string]func(*testing.T, savedCache) []byte{
		"corrupt": func(*testing.T, savedCache) []byte { return []byte("{not json") },
		"other version": func(t *testing.T, saved savedCache) []byte {
			saved.Version++
			data, err := json.Marshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			return data
		},
	}
	for name, unusable := range tests {
		t.Run(name, func(t *testing.T) {
			root, project, path := sessionFixture(t)
			lines := []string{sessionLine(project), userLine("user", "", "2026-01-01T00:00:01Z", "Saved question")}
			writeSessionLines(t, path, lines)
			cachePath := filepath.Join(t.TempDir(), "cache.json")
			first := Store{Root: root, Home: root, Cache: LoadCache(cachePath)}
			if _, err := first.Sessions(); err != nil {
				t.Fatal(err)
			}
			if err := first.Cache.Save(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(cachePath)
			if err != nil {
				t.Fatal(err)
			}
			var saved savedCache
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			for index := range saved.Items {
				stale := *saved.Items[index].Session
				stale.FirstUserMessage = "Stale question"
				saved.Items[index].Session = &stale
			}
			if err := os.WriteFile(cachePath, unusable(t, saved), 0600); err != nil {
				t.Fatal(err)
			}

			second := Store{Root: root, Home: root, Cache: LoadCache(cachePath)}
			listed, err := second.Sessions()
			if err != nil || len(listed) != 1 || listed[0].FirstUserMessage != "Saved question" {
				t.Fatalf("sessions with an unusable saved cache = %#v, err = %v", listed, err)
			}
			if err := second.Cache.Save(); err != nil {
				t.Fatal(err)
			}

			replaceKeepingFileSignature(t, path, strings.Repeat("x", len(strings.Join(lines, "\n"))+1))
			third := Store{Root: root, Home: root, Cache: LoadCache(cachePath)}
			listed, err = third.Sessions()
			if err != nil || len(listed) != 1 || listed[0].FirstUserMessage != "Saved question" {
				t.Fatalf("sessions after replacing the saved cache = %#v, err = %v", listed, err)
			}
		})
	}
}

func replaceKeepingFileSignature(t *testing.T, path, content string) {
	t.Helper()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, time.Time{}, stat.ModTime()); err != nil {
		t.Fatal(err)
	}
}

func sessionInUTC(session *Session) Session {
	result := *session
	result.CreatedAt, result.ModifiedAt, result.ConversationActivityAt = result.CreatedAt.UTC(), result.ModifiedAt.UTC(), result.ConversationActivityAt.UTC()
	return result
}
