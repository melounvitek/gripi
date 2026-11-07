package sessionfile

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

func searchFixture(t *testing.T, lines ...string) (Store, string) {
	t.Helper()
	root, project, path := sessionFixture(t)
	writeSessionLines(t, path, append([]string{sessionLine(project)}, lines...))
	return Store{Root: root, Home: root, Cache: NewCache()}, path
}

func searchRoles(t *testing.T, store Store, path, query string, roles ...string) []string {
	t.Helper()
	result, err := store.Search(path, query, roles, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := []string{}
	for _, match := range result.Matches {
		found = append(found, match.Role)
	}
	return found
}

func TestSearchFindsTextInEveryKindOfMessageButThinkingAndImages(t *testing.T) {
	store, path := searchFixture(t,
		userLine("user", "", "2026-01-01T00:00:01Z", "Fix Asana task 1209876 please"),
		`{"type":"message","id":"call","parentId":"user","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"thinking","thinking":"Task 1209876 again"},{"type":"text","text":"Looking at 1209876."},{"type":"toolCall","id":"tool-1","name":"bash","arguments":{"command":"gh pr list --search 1209876&state=open"}}]}}`,
		`{"type":"message","id":"result","parentId":"call","timestamp":"2026-01-01T00:00:03Z","message":{"role":"toolResult","toolCallId":"tool-1","toolName":"bash","isError":false,"content":[{"type":"text","text":"https://github.com/o/r/pull/1209876"},{"type":"image","mimeType":"image/png","data":"QUJD1209876"}]}}`,
		`{"type":"message","id":"shell","parentId":"result","timestamp":"2026-01-01T00:00:04Z","message":{"role":"bashExecution","command":"echo 1209876","output":"1209876\n","exitCode":0}}`,
		`{"type":"custom_message","id":"note","parentId":"shell","timestamp":"2026-01-01T00:00:05Z","customType":"note","content":"Reminder about 1209876","display":true}`,
		`{"type":"message","id":"image","parentId":"note","timestamp":"2026-01-01T00:00:06Z","message":{"role":"user","content":[{"type":"text","text":"Look"},{"type":"image","mimeType":"image/png","data":"1209876"}]}}`,
	)
	if roles := searchRoles(t, store, path, "1209876"); !reflect.DeepEqual(roles, []string{"user", "assistant", "tool", "tool", "tool", "custom"}) {
		t.Fatalf("roles of the matches = %q", roles)
	}
	if roles := searchRoles(t, store, path, "1209876", "user", "custom"); !reflect.DeepEqual(roles, []string{"user", "custom"}) {
		t.Fatalf("roles of the matches in user and custom messages = %q", roles)
	}
	// Tool arguments read as Pi wrote them.
	if roles := searchRoles(t, store, path, "1209876&state"); !reflect.DeepEqual(roles, []string{"tool"}) {
		t.Fatalf("roles of the matches of a URL query = %q", roles)
	}
	if roles := searchRoles(t, store, path, "again"); len(roles) != 0 {
		t.Fatalf("thinking matched: %q", roles)
	}
}

func TestSearchIgnoresCaseAndFindsWhatJSONEscapes(t *testing.T) {
	store, path := searchFixture(t,
		userLine("user", "", "2026-01-01T00:00:01Z", "Překlad do ČEŠTINY for branch FEAT/Login"),
		// userLine writes the text as it is, so the test escapes it as JSON does.
		userLine("quote", "user", "2026-01-01T00:00:02Z", `He said \"ship it\"`),
	)
	for _, query := range []string{"feat/login", "češtiny", `said "ship`, "\"ship it\""} {
		if roles := searchRoles(t, store, path, query); len(roles) != 1 {
			t.Fatalf("%q found %d matches", query, len(roles))
		}
	}
}

func TestSearchReadsLinesLargerThanItsBuffer(t *testing.T) {
	// The query spans the end of the first 64 KB that the scanner reads of the line.
	before := strings.Repeat("x", 64<<10-len(`{"type":"message","id":"user","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"`)-3)
	store, path := searchFixture(t, userLine("user", "", "2026-01-01T00:00:01Z", before+"needle-in-a-haystack"+strings.Repeat("y", 200<<10)))
	result, err := store.Search(path, "NEEDLE-in", nil, 3)
	if err != nil || result.Count != 1 {
		t.Fatalf("search across the buffer's end = %+v, %v", result, err)
	}
	if snippet := result.Matches[0].Snippet; !strings.HasPrefix(snippet, "…xxx") || !strings.HasSuffix(snippet, "yyy…") || !strings.Contains(snippet, "needle-in-a-haystack") || len(snippet) > 200 {
		t.Fatalf("snippet = %q", snippet)
	}
}

func TestSearchTakesALongQueryInLinearTime(t *testing.T) {
	store, path := searchFixture(t, userLine("user", "", "2026-01-01T00:00:01Z", "short"))
	started := time.Now()
	if result, err := store.Search(path, strings.Repeat("long query ", 20_000), nil, 3); err != nil || result.Count != 0 {
		t.Fatalf("search for a long query = %+v, %v", result, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("a 220 KB query took %s", elapsed)
	}
}

func TestSearchCountsEveryMatchAndKeepsTheLatest(t *testing.T) {
	var lines []string
	parent := ""
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		lines = append(lines, userLine(id, parent, "2026-01-01T00:00:01Z", "deploy "+id))
		parent = id
	}
	store, path := searchFixture(t, lines...)
	result, err := store.Search(path, "deploy", nil, 2)
	snippets := []string{}
	for _, match := range result.Matches {
		snippets = append(snippets, match.Snippet)
	}
	if err != nil || result.Count != 5 || !reflect.DeepEqual(snippets, []string{"deploy d", "deploy e"}) {
		t.Fatalf("search = %d matches, %q, %v", result.Count, snippets, err)
	}
	if _, err := store.Search(t.TempDir()+"/elsewhere.jsonl", "deploy", nil, 2); err == nil {
		t.Fatal("searched a file outside the sessions")
	}
	var _ agent.Searcher = store
}
