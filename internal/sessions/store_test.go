package sessions

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestSessionsCanDeferBusyMetadataRefresh(t *testing.T) {
	root, project, path := sessionFixture(t)
	writeSessionLines(t, path, []string{sessionLine(project), userLine("user", "", "2026-01-01T00:00:01Z", "Initial")})
	store := Store{Root: root, Home: root, Cache: NewCache()}
	initial, err := store.Sessions()
	if err != nil || len(initial) != 1 || initial[0].MessageCount != 1 {
		t.Fatalf("initial sessions = %#v, %v", initial, err)
	}
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
