package sessions

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

func TestWindowIndexesLargeNativeEntriesWithoutMaterializingThemIntoTheConversation(t *testing.T) {
	root, project, path := sessionFixture(t)
	largeOutput := strings.Repeat("x", MaxIndexedEntryBytes+1024)
	lines := []string{
		sessionLine(project),
		`{"type":"message","id":"call","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"tool-1","name":"bash","arguments":{"command":"old"}}]}}`,
		`{"type":"message","id":"result","parentId":"call","timestamp":"2026-01-01T00:00:02Z","message":{"role":"toolResult","toolCallId":"tool-1","toolName":"bash","content":[{"type":"text","text":"` + largeOutput + `"}],"isError":false}}`,
		userLine("user", "result", "2026-01-01T00:00:03Z", "Current question"),
		`{"type":"message","id":"answer","parentId":"user","timestamp":"2026-01-01T00:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"Current answer"}],"api":"test","provider":"test","model":"test","usage":{"totalTokens":10},"stopReason":"stop","timestamp":0}}`,
	}
	writeSessionLines(t, path, lines)

	window, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Messages) != 2 || window.Messages[0].Text != "Current question" || window.Messages[1].Text != "Current answer" {
		t.Fatalf("messages = %#v", window.Messages)
	}
}

func TestOversizedMultipartMessagesPreserveSegmentsWindowsAndSessionMetadata(t *testing.T) {
	root, project, path := sessionFixture(t)
	large := strings.Repeat("x", MaxIndexedEntryBytes+1024)
	assistant := `{"type":"message","id":"assistant-large","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"Inspecting"},` +
		`{"type":"text","text":"First answer"},{"type":"text","text":"continued"},` +
		`{"type":"toolCall","id":"inspect-1","name":"inspect","arguments":{"payload":"` + large + `"}},` +
		`{"type":"thinking","thinking":"More thought"},` +
		`{"type":"toolCall","id":"bash-1","name":"bash","arguments":{"command":"echo ok"}},` +
		`{"type":"text","text":"Final answer"}],` +
		`"api":"responses","provider":"openai-codex","model":"gpt-5.5","usage":{"totalTokens":321,"cost":{"total":1.25}},"stopReason":"stop","timestamp":1}}`
	user := `{"type":"message","id":"user-large","parentId":"assistant-large","timestamp":"2026-01-01T00:00:02Z","message":{"role":"user","content":[` +
		`"` + large + `",{"type":"text","text":"question text"},{"type":"thinking","thinking":"user notes"},{"type":"image","data":"cG5n","mimeType":"image/png"}]}}`
	lines := []string{sessionLine(project), assistant, user}
	parent := "user-large"
	for index := 0; index < 25; index++ {
		id := fmt.Sprintf("user-%d", index)
		lines = append(lines, userLine(id, parent, "2026-01-01T00:01:00Z", fmt.Sprintf("Message %d", index)))
		parent = id
	}
	writeSessionLines(t, path, lines)
	store := Store{Root: root, Home: root, Cache: NewCache()}

	window, err := store.Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if window.TotalMessageCount != 32 || window.StartIndex != 7 {
		t.Fatalf("window bounds = start %d of %d", window.StartIndex, window.TotalMessageCount)
	}
	if len(window.Messages) != 25 || window.Messages[0].Text != "Message 0" || window.Messages[24].Text != "Message 24" {
		t.Fatalf("messages = %#v", window.Messages)
	}
	session, ok := store.Session(path)
	if !ok {
		t.Fatal("session was not discovered")
	}
	if session.MessageCount != 27 || session.AssistantResponseCount != 1 || session.LatestAssistantResponsePreview != "First answer continued Final answer" {
		t.Fatalf("session metadata = %#v", session)
	}
	if !strings.HasPrefix(session.FirstUserMessage, strings.Repeat("x", 100)) || len(session.FirstUserMessage) > indexCaptureBytes+30 {
		t.Fatalf("bounded first user message has length %d", len(session.FirstUserMessage))
	}
	indexed, err := store.Cache.Index(path)
	if err != nil {
		t.Fatal(err)
	}
	if indexed.bytes > 1<<20 {
		t.Fatalf("oversized content was retained in the index: %d bytes", indexed.bytes)
	}
}

func TestSelectedOversizedToolCallsAndResultsRenderWithCorrectPairing(t *testing.T) {
	root, project, path := sessionFixture(t)
	large := strings.Repeat("z", MaxIndexedEntryBytes+1024)
	lines := []string{
		sessionLine(project),
		`{"type":"message","id":"assistant","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"Plan"},` +
			`{"type":"toolCall","id":"read-1","name":"read","arguments":{"path":"/tmp/` + large + `"}},` +
			`{"type":"text","text":"Between tools"},` +
			`{"type":"toolCall","id":"write-1","name":"write","arguments":{"path":"/tmp/out","content":"ok"}}]}}`,
		`{"type":"message","id":"read-result","parentId":"assistant","timestamp":"2026-01-01T00:00:02Z","message":{"role":"toolResult","toolCallId":"read-1","toolName":"read","content":[{"type":"text","text":"` + large + `"}],"isError":false}}`,
		`{"type":"message","id":"write-result","parentId":"read-result","timestamp":"2026-01-01T00:00:03Z","message":{"role":"toolResult","toolCallId":"write-1","toolName":"write","content":[{"type":"text","text":"saved ` + large + `"}],"isError":false}}`,
	}
	writeSessionLines(t, path, lines)

	window, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if window.TotalMessageCount != 4 || window.StartIndex != 0 || len(window.Messages) != 4 {
		t.Fatalf("window = start %d of %d, messages %#v", window.StartIndex, window.TotalMessageCount, window.Messages)
	}
	if !window.Messages[0].Thinking || window.Messages[1].ToolName != "read" || window.Messages[1].Text != "" || window.Messages[2].Text != "Between tools" || window.Messages[3].ToolName != "write" || !strings.HasPrefix(window.Messages[3].Text, "+ ok\n\nsaved ") {
		t.Fatalf("paired messages = %#v", window.Messages)
	}
}

func TestOversizedAssistantStatusAndCompactionMetadataRemainUsable(t *testing.T) {
	root, project, path := sessionFixture(t)
	answer := strings.Repeat("a", MaxIndexedEntryBytes+1024)
	summary := strings.Repeat("s", MaxIndexedEntryBytes+2048)
	lines := []string{
		sessionLine(project),
		`{"type":"model_change","id":"model","parentId":null,"timestamp":"2026-01-01T00:00:01Z","provider":"fallback","modelId":"fallback-model"}`,
		`{"type":"message","id":"assistant","parentId":"model","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"` + answer + `"}],"api":"responses","provider":"openai-codex","model":"gpt-5.5","usage":{"totalTokens":1234,"contextWindow":200000,"cost":{"total":2.5}},"stopReason":"stop","timestamp":1}}`,
		userLine("kept", "assistant", "2026-01-01T00:00:03Z", "keep"),
		`{"type":"compaction","id":"compact","parentId":"kept","timestamp":"2026-01-01T00:00:04Z","summary":"` + summary + `","firstKeptEntryId":"kept","tokensBefore":1234,"details":{"ignored":"` + strings.Repeat("x", 1024) + `"},"fromHook":false}`,
	}
	writeSessionLines(t, path, lines)
	store := Store{Root: root, Home: root, Cache: NewCache()}

	status, err := store.Status(path)
	if err != nil {
		t.Fatal(err)
	}
	expectedTokens := float64((len(summary) + len("keep") + 1 + 3) / 4)
	if status.Provider != "openai-codex" || status.ModelID != "gpt-5.5" || !status.ContextEstimated || status.ContextTokens != expectedTokens || status.HasContextLimit {
		t.Fatalf("status = %#v, expected tokens %.0f", status, expectedTokens)
	}
	window, err := store.Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if window.StartIndex != 1 || len(window.Messages) != 2 || window.Messages[0].Text != "keep" || len(window.Messages[1].Text) != len(summary) || !window.Messages[1].Compaction {
		t.Fatalf("window = start %d, messages %#v", window.StartIndex, window.Messages)
	}
}

func TestOversizedUnicodeThinkingAndNativeBashEntriesRemainIndexable(t *testing.T) {
	root, project, path := sessionFixture(t)
	unicodeThinking := strings.Repeat("😀", 70_000)
	largeSignature := strings.Repeat("s", MaxIndexedEntryBytes+1024)
	largeOutput := strings.Repeat("o", MaxIndexedEntryBytes+1024)
	lines := []string{
		sessionLine(project),
		`{"type":"message","id":"thinking","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"` + unicodeThinking + `"}]}}`,
		`{"type":"message","id":"whitespace-thinking","parentId":"thinking","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"**Heading**\n\n\n\t","thinkingSignature":"` + largeSignature + `"}]}}`,
		`{"type":"message","id":"bash","parentId":"whitespace-thinking","timestamp":"2026-01-01T00:00:03Z","message":{"role":"bashExecution","command":"generate","output":"` + largeOutput + `","exitCode":0,"cancelled":false,"truncated":false,"timestamp":1}}`,
	}
	parent := "bash"
	for index := 0; index < 25; index++ {
		id := fmt.Sprintf("later-%d", index)
		lines = append(lines, userLine(id, parent, "2026-01-01T00:01:00Z", fmt.Sprintf("Later %d", index)))
		parent = id
	}
	writeSessionLines(t, path, lines)

	window, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if window.StartIndex != 3 || window.TotalMessageCount != 28 || len(window.Messages) != 25 {
		t.Fatalf("window = start %d of %d, messages %d", window.StartIndex, window.TotalMessageCount, len(window.Messages))
	}
}

func TestOversizedHiddenSubagentCallDoesNotPushItsSmallResultOutOfTheWindow(t *testing.T) {
	root, project, path := sessionFixture(t)
	prompt := strings.Repeat("review", MaxIndexedEntryBytes/6+1024)
	lines := []string{
		sessionLine(project),
		`{"type":"message","id":"call","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"subagent-1","name":"subagent","arguments":{"task":"` + prompt + `"}}]}}`,
		`{"type":"message","id":"result","parentId":"call","timestamp":"2026-01-01T00:00:02Z","message":{"role":"toolResult","toolCallId":"subagent-1","toolName":"subagent","content":[{"type":"text","text":"No findings"}],"isError":false}}`,
	}
	writeSessionLines(t, path, lines)

	window, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if window.StartIndex != 0 || window.TotalMessageCount != 1 || len(window.Messages) != 1 || window.Messages[0].Text != "No findings" || window.Messages[0].ToolPrompt != prompt {
		t.Fatalf("window = start %d of %d, messages %#v", window.StartIndex, window.TotalMessageCount, window.Messages)
	}
}

func TestIndexUsesABoundedSnapshotWhilePiAppends(t *testing.T) {
	_, project, path := sessionFixture(t)
	writeSessionLines(t, path, []string{sessionLine(project), userLine("initial", "", "2026-01-01T00:00:01Z", "Initial")})
	appends := 0
	indexed, err := buildIndexWithValidationHook(path, func(_ int) {
		appends++
		appendSessionLine(t, path, userLine(fmt.Sprintf("appended-%d", appends), "initial", "2026-01-01T00:00:02Z", "Appended"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if appends == 0 || appends > 3 || indexed.latestLeafID() != "initial" {
		t.Fatalf("appends = %d, latest leaf = %q", appends, indexed.latestLeafID())
	}
	if indexed.size >= fileSize(t, path) {
		t.Fatalf("index did not retain the stable pre-append snapshot: %d >= %d", indexed.size, fileSize(t, path))
	}
}

func TestWindowRendersAStableSnapshotWhilePiAppends(t *testing.T) {
	root, project, path := sessionFixture(t)
	writeSessionLines(t, path, []string{sessionLine(project), userLine("initial", "", "2026-01-01T00:00:01Z", "Initial")})
	store := Store{Root: root, Home: root, Cache: NewCache()}
	appends := 0
	window, err := store.windowWithValidationHook(path, "", false, nil, nil, func(_ int) {
		appends++
		appendSessionLine(t, path, userLine(fmt.Sprintf("appended-%d", appends), "initial", "2026-01-01T00:00:02Z", "Appended"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if appends == 0 || appends > 3 || len(window.Messages) != 1 || window.Messages[0].Text != "Initial" {
		t.Fatalf("appends = %d, window = %#v", appends, window)
	}
	fresh, err := store.Window(path, "", false, nil, nil)
	if err != nil || len(fresh.Messages) <= len(window.Messages) {
		t.Fatalf("fresh window = %#v, err = %v", fresh, err)
	}
}

func TestMismatchedPairedToolNamesRejectTheIndexedProjection(t *testing.T) {
	root, project, path := sessionFixture(t)
	writeSessionLines(t, path, []string{
		sessionLine(project),
		`{"type":"message","id":"call","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"tool-1","name":"bash","arguments":{"command":"true"}}]}}`,
		`{"type":"message","id":"result","parentId":"call","timestamp":"2026-01-01T00:00:02Z","message":{"role":"toolResult","toolCallId":"tool-1","toolName":"read","content":[{"type":"text","text":"unexpected"}],"isError":false}}`,
	})
	if _, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil); err == nil {
		t.Fatal("mismatched tool metadata was accepted")
	}
}

func TestOversizedBashExecutionContributesToCompactionStatusEstimate(t *testing.T) {
	root, project, path := sessionFixture(t)
	output := strings.Repeat("o", MaxIndexedEntryBytes+1024)
	lines := []string{
		sessionLine(project),
		`{"type":"message","id":"old","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"old"}],"api":"responses","provider":"test","model":"model","usage":{"totalTokens":999},"stopReason":"stop","timestamp":1}}`,
		`{"type":"message","id":"bash","parentId":"old","timestamp":"2026-01-01T00:00:02Z","message":{"role":"bashExecution","command":"generate","output":"` + output + `","exitCode":7,"cancelled":false,"truncated":false,"timestamp":1}}`,
		`{"type":"compaction","id":"compact","parentId":"bash","timestamp":"2026-01-01T00:00:03Z","summary":"Summary","firstKeptEntryId":"bash","tokensBefore":999}`,
	}
	writeSessionLines(t, path, lines)

	status, err := (Store{Root: root, Home: root, Cache: NewCache()}).Status(path)
	if err != nil {
		t.Fatal(err)
	}
	exitCode := 7
	bashCharacters := bashExecutionCharacterLength(len("generate"), len(output), false, &exitCode, false, false, nil)
	expected := float64((len("Summary") + bashCharacters + 1 + 3) / 4)
	if !status.ContextEstimated || status.ContextTokens != expected {
		t.Fatalf("status = %#v, expected %.0f tokens", status, expected)
	}
}

func TestOversizedTrailingWhitespaceCompactionUsesItsSmallRenderedSize(t *testing.T) {
	root, project, path := sessionFixture(t)
	summary := "x" + strings.Repeat(" ", MaxIndexedEntryBytes+1024)
	lines := []string{
		sessionLine(project),
		`{"type":"compaction","id":"compact","parentId":null,"timestamp":"2026-01-01T00:00:01Z","summary":"` + summary + `","firstKeptEntryId":"","tokensBefore":100}`,
	}
	parent := "compact"
	for index := 0; index < 25; index++ {
		id := fmt.Sprintf("after-compact-%d", index)
		lines = append(lines, userLine(id, parent, "2026-01-01T00:01:00Z", fmt.Sprintf("After %d", index)))
		parent = id
	}
	writeSessionLines(t, path, lines)

	window, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if window.StartIndex != 0 || window.TotalMessageCount != 26 || len(window.Messages) != 26 || window.Messages[0].Text != "x" {
		t.Fatalf("window = start %d of %d, messages %#v", window.StartIndex, window.TotalMessageCount, window.Messages)
	}
}

func TestOversizedAssistantSessionMetadataExcludesCommentaryAndUnicodeWhitespace(t *testing.T) {
	root, project, path := sessionFixture(t)
	commentarySignature := `{\"v\":1.0,\"id\":\"progress\",\"phase\":\"commentary\"}`
	ignoredSignature := strings.Repeat("s", MaxIndexedEntryBytes+1024)
	unicodeWhitespace := strings.Repeat("\u2003", MaxIndexedEntryBytes/3+1024)
	lines := []string{
		sessionLine(project),
		`{"type":"message","id":"commentary","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"Internal note","textSignature":"` + commentarySignature + `"},{"type":"thinking","thinking":"","thinkingSignature":"` + ignoredSignature + `"}]}}`,
		`{"type":"message","id":"whitespace","parentId":"commentary","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"` + unicodeWhitespace + `"}]}}`,
		userLine("user", "whitespace", "2026-01-01T00:00:03Z", unicodeWhitespace+"Question"),
	}
	writeSessionLines(t, path, lines)
	store := Store{Root: root, Home: root, Cache: NewCache()}

	session, ok := store.Session(path)
	if !ok {
		t.Fatal("session was not discovered")
	}
	if session.AssistantResponseCount != 0 || session.LatestAssistantResponsePreview != "" || session.FirstUserMessage != "Question" {
		t.Fatalf("session metadata = %#v", session)
	}
}

func TestOversizedIgnoredToolResultDetailsDoNotPushSmallPairedOutputOutOfTheWindow(t *testing.T) {
	root, project, path := sessionFixture(t)
	ignored := strings.Repeat("i", MaxIndexedEntryBytes+1024)
	lines := []string{
		sessionLine(project),
		`{"type":"message","id":"call","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"bash-1","name":"bash","arguments":{"command":"echo ok"}}]}}`,
		`{"type":"message","id":"result","parentId":"call","timestamp":"2026-01-01T00:00:02Z","message":{"role":"toolResult","toolCallId":"bash-1","toolName":"bash","content":[{"type":"text","text":"ok"}],"details":{"ignored":"` + ignored + `"},"isError":false}}`,
		userLine("user", "result", "2026-01-01T00:00:03Z", "Continue"),
	}
	writeSessionLines(t, path, lines)

	window, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if window.StartIndex != 0 || window.TotalMessageCount != 2 || len(window.Messages) != 2 || window.Messages[0].Text != "ok" || window.Messages[1].Text != "Continue" {
		t.Fatalf("window = start %d of %d, messages %#v", window.StartIndex, window.TotalMessageCount, window.Messages)
	}
}

func TestOversizedCanonicalCompactionBranchAndCustomMessageShapesRender(t *testing.T) {
	root, project, path := sessionFixture(t)
	ignored := strings.Repeat("i", MaxIndexedEntryBytes+1024)
	lines := []string{
		sessionLine(project),
		`{"type":"compaction","id":"compact","parentId":null,"timestamp":"2026-01-01T00:00:01Z","summary":"Compacted","firstKeptEntryId":"","tokensBefore":100,"details":{"payload":"` + ignored + `"},"fromHook":false}`,
		`{"type":"branch_summary","id":"branch","parentId":"compact","timestamp":"2026-01-01T00:00:02Z","fromId":"compact","summary":"Branch summary","details":{"payload":"` + ignored + `"},"fromHook":false}`,
		`{"type":"custom_message","customType":"notice","content":[{"type":"text","text":"Visible notice"}],"display":true,"details":{"payload":"` + ignored + `"},"id":"custom","parentId":"branch","timestamp":"2026-01-01T00:00:03Z"}`,
	}
	writeSessionLines(t, path, lines)

	window, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if window.TotalMessageCount != 2 || len(window.Messages) != 2 || window.Messages[0].Text != "Compacted" || window.Messages[1].Text != "Visible notice" {
		t.Fatalf("messages = %#v", window.Messages)
	}
}

func TestOversizedEmptyEditDiffDoesNotCreateAnUnrenderableUnit(t *testing.T) {
	root, project, path := sessionFixture(t)
	ignored := strings.Repeat("i", MaxIndexedEntryBytes+1024)
	line := `{"type":"message","id":"result","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"toolResult","toolCallId":"edit-1","toolName":"edit","content":[{"type":"text","text":""}],"details":{"diff":"","ignored":"` + ignored + `"},"isError":false}}`
	writeSessionLines(t, path, []string{sessionLine(project), line})

	window, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if window.TotalMessageCount != 0 || len(window.Messages) != 0 {
		t.Fatalf("window = %#v", window)
	}
}

func TestOversizedEmptyTextPartsStillProduceTheirJoinedNewline(t *testing.T) {
	root, project, path := sessionFixture(t)
	ignoredSignature := strings.Repeat("i", MaxIndexedEntryBytes+1024)
	line := `{"type":"message","id":"user","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"","textSignature":"` + ignoredSignature + `"},{"type":"text","text":""}],"timestamp":1}}`
	writeSessionLines(t, path, []string{sessionLine(project), line})

	window, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Messages) != 1 || window.Messages[0].Text != "\n" {
		t.Fatalf("messages = %#v", window.Messages)
	}
}

func TestIndexAcceptsAWellFormedEntryAtThe64MiBCapWithoutCachingItsContent(t *testing.T) {
	if testing.Short() {
		t.Skip("constructs a 64 MiB native JSONL entry")
	}
	root, project, path := sessionFixture(t)
	prefix := `{"type":"message","id":"huge","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"`
	suffix := `"}]}}`
	payloadBytes := MaxRenderedEntryBytes - len(prefix) - len(suffix) - 1
	if payloadBytes <= MaxIndexedEntryBytes {
		t.Fatal("invalid cap fixture")
	}
	var following []string
	parent := "huge"
	for index := 0; index < 25; index++ {
		id := fmt.Sprintf("after-%d", index)
		following = append(following, userLine(id, parent, "2026-01-01T00:01:00Z", fmt.Sprintf("After %d", index)))
		parent = id
	}
	writeRepeatedSessionLine(t, path, sessionLine(project), prefix, 'h', payloadBytes, suffix, following)
	store := Store{Root: root, Home: root, Cache: NewCache()}

	window, err := store.Window(path, "", false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if window.StartIndex != 1 || window.TotalMessageCount != 26 || len(window.Messages) != 25 {
		t.Fatalf("window = start %d of %d (%d rendered)", window.StartIndex, window.TotalMessageCount, len(window.Messages))
	}
	indexed, err := store.Cache.Index(path)
	if err != nil {
		t.Fatal(err)
	}
	if indexed.bytes > 1<<20 {
		t.Fatalf("64 MiB content was retained in the index: %d bytes", indexed.bytes)
	}
}

func TestUnsupportedOversizedEntriesRejectStatusAndSessionMetadata(t *testing.T) {
	root, project, path := sessionFixture(t)
	writeSessionLines(t, path, []string{
		sessionLine(project),
		`{"type":"unknown","payload":"` + strings.Repeat("x", MaxIndexedEntryBytes+1) + `"}`,
	})
	store := Store{Root: root, Home: root, Cache: NewCache()}
	if _, err := store.Status(path); err == nil {
		t.Fatal("status returned partial metadata")
	}
	if _, ok := store.Session(path); ok {
		t.Fatal("session returned partial metadata")
	}
	sessions, err := store.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("discovery returned partial sessions: %#v", sessions)
	}
}

func TestWindowRejectsMalformedUnsupportedAndOverCapOversizedEntries(t *testing.T) {
	root, project, path := sessionFixture(t)
	tests := []struct {
		name string
		line string
	}{
		{"malformed", `{"type":"message","id":"bad","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("x", MaxIndexedEntryBytes+1) + `"}],}}`},
		{"unsupported", `{"type":"unknown","payload":"` + strings.Repeat("x", MaxIndexedEntryBytes+1) + `"}`},
		{"excessive structure", `{"type":"compaction","id":"compact","parentId":null,"timestamp":"2026-01-01T00:00:01Z","summary":"ok","firstKeptEntryId":"","tokensBefore":1,"details":{"payload":"` + strings.Repeat("x", MaxIndexedEntryBytes+1) + `","items":[` + strings.Repeat(`{},`, indexMaxValues) + `{ }]}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writeSessionLines(t, path, []string{sessionLine(project), test.line})
			_, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil)
			if err == nil {
				t.Fatal("unsupported oversized entry was accepted")
			}
		})
	}
	t.Run("over cap", func(t *testing.T) {
		prefix := `{"type":"message","id":"too-large","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"`
		writeRepeatedSessionLine(t, path, sessionLine(project), prefix, 'x', MaxRenderedEntryBytes, `"}}`, nil)
		if _, err := (Store{Root: root, Home: root, Cache: NewCache()}).Window(path, "", false, nil, nil); err == nil {
			t.Fatal("over-cap entry was accepted")
		}
	})
}

func sessionFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	return root, project, filepath.Join(root, "session.jsonl")
}

func sessionLine(project string) string {
	return `{"type":"session","version":3,"id":"session","timestamp":"2026-01-01T00:00:00Z","cwd":"` + project + `"}`
}

func userLine(id, parent, timestamp, text string) string {
	parentValue := `"` + parent + `"`
	if parent == "" {
		parentValue = "null"
	}
	return `{"type":"message","id":"` + id + `","parentId":` + parentValue + `,"timestamp":"` + timestamp + `","message":{"role":"user","content":[{"type":"text","text":"` + text + `"}]}}`
}

func TestSessionsRetainMetadataBeyondTheConversationIndexLimit(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, maxCacheEntries+1)
	for number := range paths {
		path := filepath.Join(root, fmt.Sprintf("session-%03d.jsonl", number))
		paths[number] = path
		writeSessionLines(t, path, []string{
			sessionLine(project),
			userLine(fmt.Sprintf("user-%d", number), "", "2026-01-01T00:00:01Z", fmt.Sprintf("Message %d", number)),
		})
	}
	store := Store{Root: root, Home: root, Cache: NewCache()}

	initial, err := store.Sessions()
	if err != nil || len(initial) != len(paths) {
		t.Fatalf("initial sessions = %d, err = %v", len(initial), err)
	}
	store.Cache.mu.Lock()
	if len(store.Cache.items) != maxCacheEntries || len(store.Cache.metadataItems) != len(paths) {
		indexCount, metadataCount := len(store.Cache.items), len(store.Cache.metadataItems)
		store.Cache.mu.Unlock()
		t.Fatalf("cache counts = indexes %d, metadata %d", indexCount, metadataCount)
	}
	indexesBefore := make(map[string]*index, len(store.Cache.items))
	for path, item := range store.Cache.items {
		indexesBefore[path] = item.index
	}
	metadataBefore := make(map[string]*metadataCacheItem, len(store.Cache.metadataItems))
	for path, item := range store.Cache.metadataItems {
		metadataBefore[path] = item
	}
	store.Cache.mu.Unlock()

	repeated, err := store.Sessions()
	if err != nil || len(repeated) != len(paths) {
		t.Fatalf("repeated sessions = %d, err = %v", len(repeated), err)
	}
	store.Cache.mu.Lock()
	for path, before := range indexesBefore {
		if store.Cache.items[path] == nil || store.Cache.items[path].index != before {
			store.Cache.mu.Unlock()
			t.Fatalf("unchanged conversation index was rebuilt for %s", path)
		}
	}
	for path, before := range metadataBefore {
		if store.Cache.metadataItems[path] != before {
			store.Cache.mu.Unlock()
			t.Fatalf("unchanged metadata was rebuilt for %s", path)
		}
	}
	store.Cache.mu.Unlock()

	changedPath := paths[0]
	appendSessionLine(t, changedPath, userLine("changed", "user-0", "2026-01-01T00:00:02Z", "Changed"))
	stale, deferred, err := store.SessionsDeferringMetadata(func(path string) bool { return path == changedPath })
	if err != nil || !deferred || len(stale) != len(paths) {
		t.Fatalf("deferred sessions = %d, deferred = %v, err = %v", len(stale), deferred, err)
	}
	for _, session := range stale {
		if session.Path == changedPath && session.MessageCount != 1 {
			t.Fatalf("deferred message count = %d", session.MessageCount)
		}
	}
	store.Cache.mu.Lock()
	for path, before := range metadataBefore {
		if store.Cache.metadataItems[path] != before {
			store.Cache.mu.Unlock()
			t.Fatalf("deferred metadata was refreshed for %s", path)
		}
	}
	store.Cache.mu.Unlock()

	refreshed, err := store.Sessions()
	if err != nil || len(refreshed) != len(paths) {
		t.Fatalf("refreshed sessions = %d, err = %v", len(refreshed), err)
	}
	for _, session := range refreshed {
		expected := 1
		if session.Path == changedPath {
			expected = 2
		}
		if session.MessageCount != expected {
			t.Fatalf("message count for %s = %d, want %d", session.Path, session.MessageCount, expected)
		}
	}
	store.Cache.mu.Lock()
	for path, before := range metadataBefore {
		if changed := store.Cache.metadataItems[path] != before; changed != (path == changedPath) {
			store.Cache.mu.Unlock()
			t.Fatalf("metadata refresh for %s = %v", path, changed)
		}
	}
	store.Cache.mu.Unlock()
}

func TestSessionMetadataCacheRequiresAnExactFileSignature(t *testing.T) {
	_, project, path := sessionFixture(t)
	writeSessionLines(t, path, []string{sessionLine(project), userLine("user", "", "2026-01-01T00:00:01Z", "Message")})
	cache := NewCache()
	if _, err := cache.Index(path); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	cache.mu.Lock()
	original := *cache.metadataItems[path]
	cache.mu.Unlock()
	tests := []struct {
		name   string
		change func(*metadataCacheItem)
	}{
		{"device", func(item *metadataCacheItem) { item.device++ }},
		{"inode", func(item *metadataCacheItem) { item.inode++ }},
		{"size", func(item *metadataCacheItem) { item.size++ }},
		{"mtime", func(item *metadataCacheItem) { item.mtime = item.mtime.Add(time.Nanosecond) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := original
			test.change(&changed)
			cache.mu.Lock()
			cache.metadataItems[path] = &changed
			cache.mu.Unlock()
			if _, found := cache.sessionMetadata(path, stat, false); found {
				t.Fatal("metadata cache accepted an inexact signature")
			}
			if metadata, found := cache.sessionMetadata(path, stat, true); !found || metadata == nil {
				t.Fatal("stale metadata was unavailable for busy-session deferral")
			}
		})
	}
}

func TestSessionMetadataCacheRetainsKnownInvalidOutcomes(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	validPath := filepath.Join(root, "valid.jsonl")
	malformedPath := filepath.Join(root, "malformed.jsonl")
	unsupportedPath := filepath.Join(root, "unsupported.jsonl")
	writeSessionLines(t, validPath, []string{sessionLine(project)})
	writeSessionLines(t, malformedPath, []string{"not json"})
	writeSessionLines(t, unsupportedPath, []string{sessionLine(project), `{"type":"unknown","payload":"` + strings.Repeat("x", MaxIndexedEntryBytes+1) + `"}`})
	store := Store{Root: root, Home: root, Cache: NewCache()}

	for scan := 0; scan < 2; scan++ {
		listed, err := store.Sessions()
		if err != nil || len(listed) != 1 || listed[0].Path != validPath {
			t.Fatalf("scan %d sessions = %#v, err = %v", scan, listed, err)
		}
	}
	store.Cache.mu.Lock()
	defer store.Cache.mu.Unlock()
	if len(store.Cache.metadataItems) != 3 {
		t.Fatalf("metadata count = %d", len(store.Cache.metadataItems))
	}
	if item := store.Cache.metadataItems[malformedPath]; item == nil || item.session != nil {
		t.Fatalf("malformed metadata outcome = %#v", item)
	}
	if item := store.Cache.metadataItems[unsupportedPath]; item == nil || item.session != nil {
		t.Fatalf("unsupported metadata outcome = %#v", item)
	}
}

func TestSessionMetadataCacheIsBounded(t *testing.T) {
	cache := NewCache()
	for number := 0; number < 10_000; number++ {
		path := fmt.Sprintf("/sessions/session-%05d.jsonl", number)
		indexed := &index{path: path, device: 1, inode: uint64(number + 1), size: 100, mtime: time.Unix(1, 0), supported: true, sessionMetadataSupported: true, session: &Session{Path: path, CWD: "/project", ID: fmt.Sprintf("session-%d", number), DisplayName: "Ordinary session"}}
		cache.mu.Lock()
		cache.cacheSessionMetadataLocked(path, indexed)
		cache.mu.Unlock()
	}
	cache.mu.Lock()
	ordinaryCount := len(cache.metadataItems)
	cache.mu.Unlock()
	if ordinaryCount != 10_000 {
		t.Fatalf("ordinary metadata count = %d", ordinaryCount)
	}

	for number := 10_000; number <= maxMetadataCacheEntries; number++ {
		path := fmt.Sprintf("/sessions/session-%05d.jsonl", number)
		indexed := &index{path: path, device: 1, inode: uint64(number + 1), size: 100, mtime: time.Unix(1, 0), supported: true, sessionMetadataSupported: true, session: &Session{Path: path, CWD: "/project", ID: fmt.Sprintf("session-%d", number), DisplayName: "Ordinary session"}}
		cache.mu.Lock()
		cache.cacheSessionMetadataLocked(path, indexed)
		cache.mu.Unlock()
	}
	cache.mu.Lock()
	metadataCount, metadataBytes := len(cache.metadataItems), cache.metadataBytes
	cache.mu.Unlock()
	if metadataCount > maxMetadataCacheEntries || metadataBytes > maxMetadataCacheBytes {
		t.Fatalf("metadata cache exceeds bounds: count %d, bytes %d", metadataCount, metadataBytes)
	}

	path := "/sessions/over-budget.jsonl"
	indexed := &index{path: path, device: 1, inode: 99_999, size: 100, mtime: time.Unix(1, 0), supported: true, sessionMetadataSupported: true, session: &Session{Path: path, CWD: "/project", DisplayName: strings.Repeat("x", maxMetadataCacheBytes)}}
	cache.mu.Lock()
	cache.cacheSessionMetadataLocked(path, indexed)
	_, cached := cache.metadataItems[path]
	cache.mu.Unlock()
	if cached {
		t.Fatal("individually over-budget metadata was cached")
	}
}

func TestMetadataIsCachedWhenTheConversationIndexExceedsItsCacheBudget(t *testing.T) {
	cache := NewCache()
	path := "/sessions/large-index.jsonl"
	indexed := &index{path: path, device: 1, inode: 2, size: 100, mtime: time.Unix(1, 0), bytes: maxCacheBytes + 1, supported: true, sessionMetadataSupported: true, session: &Session{Path: path, CWD: "/project", DisplayName: "Large conversation"}}
	cache.cacheBuiltIndex(path, indexed)
	cache.mu.Lock()
	metadata := cache.metadataItems[path]
	fullIndex := cache.items[path]
	cache.mu.Unlock()
	if metadata == nil || metadata.session == nil || metadata.session.DisplayName != "Large conversation" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if fullIndex != nil {
		t.Fatal("over-budget conversation index was cached")
	}
}

func TestSessionMetadataCacheDetachesRetainedStrings(t *testing.T) {
	paddedName := strings.Repeat(" ", MaxIndexedEntryBytes) + "name" + strings.Repeat(" ", MaxIndexedEntryBytes)
	largeResponse := strings.Repeat("x", MaxIndexedEntryBytes)
	indexed := &index{
		path: "/sessions/detached.jsonl", device: 1, inode: 2, size: 100, mtime: time.Unix(1, 0),
		supported: true, sessionMetadataSupported: true,
		session: &Session{DisplayName: strings.TrimSpace(paddedName), LatestAssistantResponsePreview: largeResponse[len(largeResponse)-16:]},
	}
	cached := sessionFromIndex(indexed)
	if cached == nil || cached.DisplayName != "name" || cached.LatestAssistantResponsePreview != strings.Repeat("x", 16) {
		t.Fatalf("cached session = %#v", cached)
	}
	if unsafe.StringData(cached.DisplayName) == unsafe.StringData(indexed.session.DisplayName) {
		t.Fatal("cached display name retains its padded source allocation")
	}
	if unsafe.StringData(cached.LatestAssistantResponsePreview) == unsafe.StringData(indexed.session.LatestAssistantResponsePreview) {
		t.Fatal("cached response preview retains its large source allocation")
	}
}

func TestSessionMetadataCacheSupportsConcurrentScans(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 32)
	for number := range paths {
		paths[number] = filepath.Join(root, fmt.Sprintf("session-%02d.jsonl", number))
		writeSessionLines(t, paths[number], []string{sessionLine(project), userLine(fmt.Sprintf("user-%d", number), "", "2026-01-01T00:00:01Z", "Message")})
	}
	store := Store{Root: root, Home: root, Cache: NewCache()}
	if sessions, err := store.Sessions(); err != nil || len(sessions) != len(paths) {
		t.Fatalf("initial sessions = %d, err = %v", len(sessions), err)
	}

	errors := make(chan error, 8)
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for iteration := 0; iteration < 10; iteration++ {
				sessions, err := store.Sessions()
				if err != nil || len(sessions) != len(paths) {
					errors <- fmt.Errorf("sessions = %d, err = %v", len(sessions), err)
					return
				}
				if _, err := store.Cache.Index(paths[(worker+iteration)%len(paths)]); err != nil {
					errors <- err
					return
				}
			}
		}(worker)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestSessionsCanDeferBusyMetadataRefresh(t *testing.T) {
	root, project, path := sessionFixture(t)
	writeSessionLines(t, path, []string{sessionLine(project), userLine("user", "", "2026-01-01T00:00:01Z", "Initial")})
	store := Store{Root: root, Home: root, Cache: NewCache()}
	initial, err := store.Sessions()
	if err != nil || len(initial) != 1 || initial[0].MessageCount != 1 {
		t.Fatalf("initial sessions = %#v, %v", initial, err)
	}
	store.Cache.mu.Lock()
	metadata := store.Cache.metadataItems[path]
	if metadata == nil {
		store.Cache.mu.Unlock()
		t.Fatal("metadata was not cached")
	}
	store.Cache.metadataBytes -= metadata.bytes
	delete(store.Cache.metadataItems, path)
	store.Cache.metadataOrder.Remove(metadata.element)
	if store.Cache.items[path] == nil {
		store.Cache.mu.Unlock()
		t.Fatal("full index was not cached")
	}
	store.Cache.mu.Unlock()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(file, userLine("new", "user", "2026-01-01T00:00:02Z", "New")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	deferredSessions, deferred, err := store.SessionsDeferringMetadata(func(candidate string) bool { return candidate == path })
	if err != nil || !deferred || deferredSessions[0].MessageCount != 1 {
		t.Fatalf("deferred sessions = %#v, deferred=%v, err=%v", deferredSessions, deferred, err)
	}
	refreshed, deferred, err := store.SessionsDeferringMetadata(nil)
	if err != nil || deferred || refreshed[0].MessageCount != 2 {
		t.Fatalf("refreshed sessions = %#v, deferred=%v, err=%v", refreshed, deferred, err)
	}
}

func writeSessionLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func appendSessionLine(t *testing.T, path, line string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString(line + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return stat.Size()
}

func writeRepeatedSessionLine(t *testing.T, path, header, prefix string, repeated byte, count int, suffix string, following []string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = file.WriteString(header + "\n" + prefix); err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte{repeated}, 64<<10)
	for count > 0 {
		written := min(count, len(chunk))
		if _, err = file.Write(chunk[:written]); err != nil {
			t.Fatal(err)
		}
		count -= written
	}
	if _, err = file.WriteString(suffix + "\n"); err != nil {
		t.Fatal(err)
	}
	for _, line := range following {
		if _, err = file.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}
