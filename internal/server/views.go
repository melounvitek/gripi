package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

const (
	recentSessionLimit         = 20
	sessionPageSize            = 20
	toolOutputDesktopTailLines = 18
	toolOutputMobileTailLines  = 12
)

var projectColors = [][2]string{
	{"#6a3b1d33", "#e6a66f"}, {"#334f7833", "#8db9ef"}, {"#563a7033", "#c5a0e8"}, {"#70374633", "#ef9aae"},
	{"#4b612b33", "#acd276"}, {"#285d7033", "#75c5df"}, {"#67365f33", "#dfa0d4"}, {"#66502033", "#e0bd65"},
	{"#315d3b33", "#86cb98"}, {"#3f477533", "#a5afe9"}, {"#713f3233", "#eda18b"}, {"#215f5933", "#76cbbf"},
}

type projectIdentity struct{ Monogram, Background, Foreground string }
type sidebarSessionData struct {
	View     *pageView
	Session  *sessions.Session
	Shortcut int
}
type messageData struct {
	View    *pageView
	Message *sessions.Message
}
type relationData struct {
	View             *pageView
	Session, Current *sessions.Session
}

type liveOutputData struct {
	EventAfter               int64
	ActiveToolEventsJSON     string
	ActiveToolTimestampsJSON string
	ActiveToolPromptsJSON    string
	ActiveBashJSON           string
	CompletedBashEventsJSON  string
	PersistedBashJSON        string
	QueuedMessagesJSON       string
	ExtensionUIJSON          string
	ComposerState            string
	ComposerStateSince       string
	ComposerBusySince        string
	Compacting               bool
	AgentRunning             bool
}

type pageView struct {
	Request                   *http.Request
	ServerOrigin              string
	Params                    url.Values
	Sessions                  []*sessions.Session
	Selected                  *sessions.Session
	SidebarSessions           []*sessions.Session
	PinnedSessions            []*sessions.Session
	SeparateCurrent           *sessions.Session
	KnownCWDs                 []string
	NewSessionCWDs            []string
	SelectedProject           string
	SearchQuery               string
	Unread                    map[string]bool
	Pinned                    map[string]bool
	UnreadCount               int
	SessionsLimit             int
	SessionPool               []*sessions.Session
	SessionPoolLength         int
	Window                    sessions.Window
	Attachments               map[*sessions.Message]sessions.AttachmentMatch
	SessionOnly               bool
	GatewayInstanceID         string
	Home                      string
	BrowserAccessEnabled      bool
	WorkspaceAccessEnabled    bool
	ResourceMonitoringEnabled bool
	SessionGeneration         string
	SessionSyncMode           sessions.SyncMode
	SessionSyncRevision       string
	SessionSyncError          string
	SessionSyncBlocked        bool
	SidebarMetadataDeferred   bool
	LiveOutput                liveOutputData
}

func (app *application) preparePage(request *http.Request, includeConversation bool) (*pageView, error) {
	params := request.URL.Query()
	selectedPath := params.Get("session")
	if _, pending := app.pendingSessions.CWD(selectedPath); pending {
		canonical, err := app.canonicalRPCSessionPath(request, selectedPath)
		if err != nil {
			return nil, err
		}
		params.Set("session", canonical)
		request.URL.RawQuery = params.Encode()
	} else if _, remapped := app.pendingSessions.Resolve(selectedPath); remapped {
		canonical, err := app.canonicalRPCSessionPath(request, selectedPath)
		if err != nil {
			return nil, err
		}
		params.Set("session", canonical)
		request.URL.RawQuery = params.Encode()
	}
	if remapped, ok := app.pendingSessions.Resolve(params.Get("session")); ok {
		if app.ownsSession != nil && !app.ownsSession(request, params.Get("session")) {
			return nil, errors.New("pending session is not owned by the requester")
		}
		params.Set("session", remapped)
		request.URL.RawQuery = params.Encode()
	}
	store := sessions.Store{Root: app.config.SessionsRoot, Home: app.config.Home, Cache: app.sessionCache}
	all, metadataDeferred, err := store.SessionsDeferringMetadata(func(path string) bool {
		return request.URL.Path == "/sidebar" && app.rpcClients.Busy(path)
	})
	if err != nil {
		return nil, err
	}
	app.rememberSessionHashes(all)
	knownPaths := make(map[string]bool, len(all))
	for _, session := range all {
		knownPaths[session.Path] = true
	}
	for _, pending := range app.pendingSessions.Entries() {
		if knownPaths[pending.Path] {
			continue
		}
		all = append(all, &sessions.Session{Path: pending.Path, CWD: pending.CWD, DisplayName: "New session (pending first assistant response)", CreatedAt: pending.CreatedAt, ModifiedAt: pending.CreatedAt, ConversationActivityAt: pending.CreatedAt})
	}
	params = request.URL.Query()
	selectedProject := params.Get("project")
	knownProjects := make(map[string]bool)
	for _, session := range all {
		knownProjects[session.CWD] = true
	}
	if !knownProjects[selectedProject] {
		selectedProject = ""
	}
	var selected *sessions.Session
	if params.Get("no_session") != "1" {
		for _, session := range all {
			if session.Path == params.Get("session") {
				selected = session
				break
			}
		}
		if selected == nil && len(all) > 0 {
			excluded := params.Get("session_fallback_excluding")
			for _, session := range all {
				if session.Path != excluded {
					selected = session
					break
				}
			}
		}
	}
	markRead := request.URL.Path != "/sidebar" || params.Get("session") != ""
	unread, pinned := app.gatewayState.ReadAndObserve(all, selected, markRead)
	view := &pageView{Request: request, ServerOrigin: absoluteRedirectURL(request, "", app.config.TrustProxyHeaders), Params: params, Sessions: all, Selected: selected, SelectedProject: selectedProject, SearchQuery: strings.TrimSpace(params.Get("session_search")), Unread: unread, Pinned: pinned, SessionOnly: params.Get("session_only") == "1", GatewayInstanceID: app.instanceID, Home: app.config.Home, BrowserAccessEnabled: !app.config.BrowserAuthDisabled, WorkspaceAccessEnabled: app.config.MultiUserMode, ResourceMonitoringEnabled: app.config.ResourceMonitoringEnabled, SidebarMetadataDeferred: metadataDeferred}
	view.prepareSidebar()
	view.KnownCWDs = knownCWDs(all)
	view.NewSessionCWDs = app.newSessionCWDs(view)
	if includeConversation && selected != nil {
		leafID, leafSupplied := "", false
		view.SessionSyncMode = sessions.SyncAvailable
		_, statErr := os.Stat(selected.Path)
		if statErr == nil {
			if state := app.synchronizer.InspectIfAvailable(request.Context(), selected.Path, true); state != nil {
				view.SessionSyncMode, view.SessionSyncRevision, view.SessionSyncError = state.Mode, state.Revision, state.Error
				view.SessionSyncBlocked = state.Blocked()
				if state.Mode == sessions.SyncManaged {
					leafID, leafSupplied = state.RPCLeafID, true
				} else if state.Blocked() {
					leafID, leafSupplied = state.PersistedLeafID, true
				}
			}
		}
		snapshot := app.rpcClients.LiveSnapshot(selected.Path)
		window := sessions.Window{}
		if statErr == nil {
			var err error
			window, err = store.Window(selected.Path, leafID, leafSupplied, nil, nil)
			if err != nil {
				return nil, err
			}
		}
		view.Window = window
		view.Attachments = make(map[*sessions.Message]sessions.AttachmentMatch)
		if statErr == nil {
			view.Attachments = (sessions.AttachmentStore{Root: app.config.AttachmentsRoot}).Match(selected.Path, window.Messages)
			view.SessionGeneration = store.Generation(selected.Path)
		}
		activeToolIDs := make([]string, 0, len(snapshot.ActiveToolEvents))
		for _, event := range snapshot.ActiveToolEvents {
			if id := stringFromAny(event["toolCallId"]); id != "" {
				activeToolIDs = append(activeToolIDs, id)
			}
		}
		toolContext := map[string]sessions.ToolCallContext(nil)
		if statErr == nil {
			toolContext = store.SubagentToolCallContext(selected.Path, activeToolIDs)
		}
		view.LiveOutput = liveOutputFrom(snapshot, window.Messages, app.config.Home, toolContext)
	}
	return view, nil
}

func liveOutputFrom(snapshot rpc.LiveSnapshot, messages []*sessions.Message, home string, toolContext map[string]sessions.ToolCallContext) liveOutputData {
	activeTools := snapshot.ActiveToolEvents
	if activeTools == nil {
		activeTools = []map[string]any{}
	}
	completedBash := snapshot.CompletedBashEvents
	if completedBash == nil {
		completedBash = []map[string]any{}
	}
	queued := snapshot.QueuedMessages
	if queued == nil {
		queued = map[string][]string{"steering": {}, "followUp": {}}
	}
	extensionUI := snapshot.ExtensionUI
	if extensionUI == nil {
		extensionUI = map[string]any{"pending_dialogs": []map[string]any{}, "statuses": []map[string]any{}, "widgets": []map[string]any{}, "title": nil}
	}
	toolTimestamps, toolPrompts := map[string]time.Time{}, map[string]string{}
	for id, details := range toolContext {
		if !details.Timestamp.IsZero() {
			toolTimestamps[id] = details.Timestamp
		}
		if details.Prompt != "" {
			toolPrompts[id] = details.Prompt
		}
	}
	activeBash := any(nil)
	persistedBash := []string{}
	if snapshot.ActiveBash != nil {
		if activeBashPersisted(snapshot.ActiveBash, messages, home) {
			persistedBash = append(persistedBash, stringFromAny(snapshot.ActiveBash["bash_id"]))
		} else {
			activeBash = snapshot.ActiveBash
		}
	}
	remainingCompleted := completedBash[:0]
	for _, event := range completedBash {
		if completedBashPersisted(event, messages, home) {
			persistedBash = append(persistedBash, stringFromAny(event["bashId"]))
		} else {
			remainingCompleted = append(remainingCompleted, event)
		}
	}
	completedBash = remainingCompleted
	agentBusy := snapshot.AgentRunning || snapshot.Compacting
	composerState := "idle"
	if agentBusy {
		composerState = "running"
	} else if activeBash != nil {
		composerState = "bash"
	}
	agentBusySince := snapshot.AgentBusySince
	if agentBusySince == nil && activeBash == nil {
		agentBusySince = snapshot.BusySince
	}
	stateSince := agentBusySince
	if snapshot.CompactingSince != nil {
		stateSince = snapshot.CompactingSince
	}
	return liveOutputData{
		EventAfter:           snapshot.EventSequence,
		ActiveToolEventsJSON: jsonData(activeTools), ActiveToolTimestampsJSON: jsonData(toolTimestamps), ActiveToolPromptsJSON: jsonData(toolPrompts),
		ActiveBashJSON: jsonData(activeBash), CompletedBashEventsJSON: jsonData(completedBash), PersistedBashJSON: jsonData(persistedBash),
		QueuedMessagesJSON: jsonData(queued), ExtensionUIJSON: jsonData(extensionUI), ComposerState: composerState,
		ComposerStateSince: millisecondsString(stateSince), ComposerBusySince: millisecondsString(agentBusySince),
		Compacting: snapshot.Compacting, AgentRunning: snapshot.AgentRunning,
	}
}

func activeBashPersisted(active map[string]any, messages []*sessions.Message, home string) bool {
	started, ok := timeFromAny(active["started_at"])
	if !ok {
		return false
	}
	summary := "$ " + sessions.DisplayHomePath(stringFromAny(active["command"]), home)
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == "bashExecution" && message.Summary == summary && !message.Timestamp.IsZero() && !message.Timestamp.Before(started) {
			return true
		}
	}
	return false
}

func completedBashPersisted(event map[string]any, messages []*sessions.Message, home string) bool {
	if event["type"] != "bash_end" {
		return false
	}
	result, ok := event["result"].(map[string]any)
	if !ok {
		return false
	}
	startedMilliseconds, ok := numericValue(event["startedAt"])
	if !ok {
		return false
	}
	summary := "$ " + sessions.DisplayHomePath(stringFromAny(event["command"]), home)
	for _, message := range messages {
		if message.Role != "bashExecution" || message.Summary != summary || message.Text != stringFromAny(result["output"]) || message.BashCancelled != (result["cancelled"] == true) || message.BashTruncated != (result["truncated"] == true) || message.BashExcludedFromContext != (event["excludeFromContext"] == true) || message.BashRecordedAt.IsZero() || message.BashRecordedAt.UnixMilli() < int64(startedMilliseconds) {
			continue
		}
		resultExit, hasExit := numericValue(result["exitCode"])
		if (message.BashExitCode == nil) != !hasExit {
			continue
		}
		if hasExit && *message.BashExitCode != int(resultExit) {
			continue
		}
		return true
	}
	return false
}

func timeFromAny(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, true
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		return parsed, err == nil
	default:
		return time.Time{}, false
	}
}

func jsonData(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }
func millisecondsString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(value.UnixMilli(), 10)
}

func (app *application) rememberSessionHashes(all []*sessions.Session) {
	values := make(map[string]bool, len(all))
	for _, session := range all {
		values[sessions.SessionHash(session.Path)] = true
	}
	app.sessionHashesMu.Lock()
	app.knownSessionHashes = values
	app.sessionHashesAt = time.Now()
	app.sessionHashesMu.Unlock()
}

func (view *pageView) prepareSidebar() {
	for _, session := range view.Sessions {
		if view.Unread[session.Path] && (view.Selected == nil || view.Selected.Path != session.Path) {
			view.UnreadCount++
		}
	}
	for _, session := range view.Sessions {
		if view.Pinned[session.Path] {
			view.PinnedSessions = append(view.PinnedSessions, session)
		}
	}
	var pool []*sessions.Session
	query := strings.ToLower(view.SearchQuery)
	for _, session := range view.Sessions {
		if view.Pinned[session.Path] {
			continue
		}
		if view.SelectedProject != "" && session.CWD != view.SelectedProject {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(strings.Join([]string{session.DisplayName, session.CWD, filepath.Base(session.CWD), session.FirstUserMessage}, "\n")), query) {
			continue
		}
		pool = append(pool, session)
	}
	limit, _ := strconv.Atoi(view.Params.Get("sidebar_sessions_limit"))
	if limit < recentSessionLimit {
		limit = recentSessionLimit
	}
	if view.Params.Get("show_all_sessions") == "1" {
		limit = len(pool)
	}
	view.SessionsLimit, view.SessionPool, view.SessionPoolLength = limit, pool, len(pool)
	if limit > len(pool) {
		limit = len(pool)
	}
	view.SidebarSessions = pool[:limit]
	if view.Selected != nil && !view.Pinned[view.Selected.Path] {
		found := false
		for _, session := range view.SidebarSessions {
			found = found || session.Path == view.Selected.Path
		}
		if !found {
			view.SeparateCurrent = view.Selected
		}
	}
}

func knownCWDs(all []*sessions.Session) []string {
	latest := make(map[string]time.Time)
	for _, session := range all {
		if session.ConversationActivityAt.After(latest[session.CWD]) {
			latest[session.CWD] = session.ConversationActivityAt
		}
	}
	result := make([]string, 0, len(latest))
	for cwd := range latest {
		result = append(result, cwd)
	}
	sort.Slice(result, func(i, j int) bool {
		if !latest[result[i]].Equal(latest[result[j]]) {
			return latest[result[i]].After(latest[result[j]])
		}
		return strings.ToLower(filepath.Base(result[i])) < strings.ToLower(filepath.Base(result[j]))
	})
	return result
}

func (app *application) newSessionCWDs(view *pageView) []string {
	values := append([]string{}, view.KnownCWDs...)
	file, err := os.Open(app.config.SessionCwdsPath)
	if err == nil {
		defer file.Close()
		data, _ := io.ReadAll(io.LimitReader(file, 1<<20))
		for _, line := range strings.Split(string(data), "\n") {
			line, _, _ = strings.Cut(line, " #")
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if line == "~" || strings.HasPrefix(line, "~/") {
				line = filepath.Join(app.config.Home, strings.TrimPrefix(strings.TrimPrefix(line, "~/"), "~"))
			}
			if path, err := filepath.EvalSymlinks(line); err == nil {
				if stat, err := os.Stat(path); err == nil && stat.IsDir() {
					values = append(values, path)
				}
			}
		}
	}
	seen := make(map[string]bool)
	unique := values[:0]
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			unique = append(unique, value)
		}
	}
	preferred := view.SelectedProject
	if preferred == "" && view.Selected != nil {
		preferred = view.Selected.CWD
	}
	if preferred != "" {
		result := []string{preferred}
		for _, value := range unique {
			if value != preferred {
				result = append(result, value)
			}
		}
		return result
	}
	return unique
}

func templateFunctions(markdownRenderer interface{ Render(string) string }) template.FuncMap {
	return template.FuncMap{
		"add": func(a, b int) int { return a + b }, "sub": func(a, b int) int { return a - b }, "min": func(a, b int) int {
			if a < b {
				return a
			}
			return b
		},
		"base": filepath.Base, "urlquery": url.QueryEscape, "json": func(value any) string { data, _ := json.Marshal(value); return string(data) },
		"projectIdentity": identityFor, "relativeTime": relativeTime, "formatTime": func(value time.Time) string {
			if value.IsZero() {
				return "unknown"
			}
			return value.Local().Format("2006-01-02 15:04")
		},
		"sessionURL": sessionURL, "sessionClasses": sessionClasses, "unreadLabel": unreadLabel,
		"sessionData": func(view *pageView, session *sessions.Session, shortcut int) sidebarSessionData {
			return sidebarSessionData{view, session, shortcut}
		},
		"sessionParent": sessionParent, "sessionChildren": sessionChildren, "sessionRoot": sessionRoot,
		"relationData": func(view *pageView, session, current *sessions.Session) relationData {
			return relationData{view, session, current}
		},
		"messageData":    func(view *pageView, message *sessions.Message) messageData { return messageData{view, message} },
		"sidebarLoadURL": sidebarLoadURL, "filtersClearURL": filtersClearURL,
		"messageClass": messageClass, "messageRoleLabel": messageRoleLabel, "messageFingerprint": messageFingerprint,
		"messageBody": func(message *sessions.Message) template.HTML {
			if (message.Role == "assistant" || message.Role == "custom" || message.Thinking) && !message.Compact {
				return template.HTML(markdownRenderer.Render(message.Text))
			}
			return template.HTML(template.HTMLEscapeString(message.Text))
		},
		"compactBody": compactBody, "compactTail": compactTail, "toolSummary": func(value string) template.HTML { return template.HTML(value) },
		"imageSource": imageSource,
		"statusItems": statusItems, "bashStatusItems": bashStatusItems, "attachmentLabel": attachmentLabel,
		"visibleImages": visibleImages, "attachmentCount": attachmentCount, "collapsible": collapsible,
		"lineCount": func(message *sessions.Message) int {
			if message.Text == "" {
				return 0
			}
			return len(strings.Split(strings.TrimSuffix(message.Text, "\n"), "\n"))
		},
		"terminalOutput": terminalOutput, "terminalSource": terminalSource, "terminalTruncated": func(message *sessions.Message) bool { return len(message.Text) > 262144 },
		"conversationSearch": conversationSearch, "newCWDLabel": newCWDLabel,
	}
}

func identityFor(cwd string) projectIdentity {
	label := filepath.Base(cwd)
	words := strings.FieldsFunc(label, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') })
	monogram := ""
	if len(words) > 1 {
		monogram = firstRunes(strings.ToUpper(words[0]), 1) + firstRunes(strings.ToUpper(words[1]), 1)
	} else if len(words) == 1 {
		monogram = firstRunes(strings.ToUpper(words[0]), 2)
	} else {
		monogram = firstRunes(strings.ToUpper(label), 2)
	}
	digest := sha256.Sum256([]byte(label))
	colors := projectColors[int(digest[0])%len(projectColors)]
	return projectIdentity{Monogram: monogram, Background: colors[0], Foreground: colors[1]}
}
func firstRunes(value string, count int) string {
	chars := []rune(value)
	if len(chars) > count {
		chars = chars[:count]
	}
	return string(chars)
}
func relativeTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	now := time.Now()
	seconds := int(now.Sub(value).Seconds())
	if seconds < 60 {
		return "just now"
	}
	local, today := value.Local(), now.Local()
	if local.YearDay() == today.YearDay() && local.Year() == today.Year() {
		if seconds < 3600 {
			return fmt.Sprintf("%d minute%s ago", seconds/60, plural(seconds/60))
		}
		return fmt.Sprintf("%d hour%s ago", seconds/3600, plural(seconds/3600))
	}
	yesterday := today.AddDate(0, 0, -1)
	if local.YearDay() == yesterday.YearDay() && local.Year() == yesterday.Year() {
		return "yesterday"
	}
	return local.Format("2006-01-02")
}
func plural(value int) string {
	if value == 1 {
		return ""
	}
	return "s"
}
func sessionURL(view *pageView, session *sessions.Session) string {
	values := url.Values{"session": {session.Path}}
	if view.SessionOnly {
		values.Set("session_only", "1")
	} else {
		if view.SelectedProject != "" {
			values.Set("project", view.SelectedProject)
		}
		if view.SearchQuery != "" {
			values.Set("session_search", view.SearchQuery)
		}
		if retained := retainedSessionsLimit(view, session); retained != "" {
			values.Set("sidebar_sessions_limit", retained)
		}
	}
	return "/?" + values.Encode()
}
func sessionParent(view *pageView, session *sessions.Session) *sessions.Session {
	if session == nil || session.ParentSessionPath == "" {
		return nil
	}
	target := filepath.Clean(session.ParentSessionPath)
	for _, candidate := range view.Sessions {
		if filepath.Clean(candidate.Path) == target {
			return candidate
		}
	}
	return nil
}
func sessionChildren(view *pageView, session *sessions.Session) []*sessions.Session {
	if session == nil {
		return nil
	}
	var result []*sessions.Session
	target := filepath.Clean(session.Path)
	for _, candidate := range view.Sessions {
		if candidate.ParentSessionPath != "" && filepath.Clean(candidate.ParentSessionPath) == target {
			result = append(result, candidate)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ModifiedAt.After(result[j].ModifiedAt) })
	return result
}
func sessionRoot(view *pageView, session *sessions.Session) *sessions.Session {
	seen := make(map[string]bool)
	current := session
	for current != nil && !seen[current.Path] {
		seen[current.Path] = true
		parent := sessionParent(view, current)
		if parent == nil {
			return current
		}
		current = parent
	}
	return current
}
func sidebarLoadURL(view *pageView) string {
	values := url.Values{}
	if view.Selected != nil {
		values.Set("session", view.Selected.Path)
	}
	if view.SelectedProject != "" {
		values.Set("project", view.SelectedProject)
	}
	if view.SearchQuery != "" {
		values.Set("session_search", view.SearchQuery)
	}
	values.Set("sidebar_sessions_limit", strconv.Itoa(min(view.SessionsLimit+sessionPageSize, view.SessionPoolLength)))
	return "/?" + values.Encode()
}
func filtersClearURL(view *pageView) string {
	values := url.Values{}
	if view.Selected != nil {
		values.Set("session", view.Selected.Path)
	}
	return "/?" + values.Encode()
}
func retainedSessionsLimit(view *pageView, session *sessions.Session) string {
	if view.SessionsLimit <= recentSessionLimit || session == nil {
		return ""
	}
	for index, candidate := range view.SessionPool {
		if candidate.Path == session.Path && index >= recentSessionLimit {
			return strconv.Itoa(view.SessionsLimit)
		}
	}
	return ""
}
func sessionClasses(view *pageView, session *sessions.Session) string {
	values := []string{"session", "recent-session"}
	if view.Selected != nil && view.Selected.Path == session.Path {
		values = append(values, "selected")
	}
	if view.Unread[session.Path] && (view.Selected == nil || view.Selected.Path != session.Path) {
		values = append(values, "unread")
	}
	return strings.Join(values, " ")
}
func unreadLabel(count int) string {
	if count > 99 {
		return "99+"
	}
	return strconv.Itoa(count)
}
func conversationSearch(view *pageView, session *sessions.Session) string {
	if len(view.SearchQuery) >= 3 && strings.Contains(strings.ToLower(session.FirstUserMessage), strings.ToLower(view.SearchQuery)) {
		return view.SearchQuery
	}
	return ""
}
func newCWDLabel(view *pageView, cwd string) string {
	name := filepath.Base(cwd)
	count := 0
	for _, value := range view.NewSessionCWDs {
		if filepath.Base(value) == name {
			count++
		}
	}
	if count > 1 {
		return name + " — " + cwd
	}
	return name
}

func messageRoleKey(role string) string {
	switch role {
	case "assistant":
		return "assistant"
	case "user":
		return "user"
	case "tool", "toolResult", "bashExecution":
		return "tool"
	case "error":
		return "error"
	default:
		return "status"
	}
}
func messageClass(message *sessions.Message) string {
	values := []string{"message", "message--" + messageRoleKey(message.Role)}
	if message.Compact {
		values = append(values, "message--compact")
	}
	if message.Compaction {
		values = append(values, "message--compaction")
	}
	if message.Role == "bashExecution" {
		values = append(values, "message--bash-execution")
	}
	if message.BashExcludedFromContext {
		values = append(values, "message--bash-excluded")
	}
	if message.BashCancelled {
		values = append(values, "message--bash-cancelled")
	}
	if message.BashTruncated {
		values = append(values, "message--bash-truncated")
	}
	if message.Thinking {
		values = append(values, "message--thinking")
	}
	if message.Compact && message.Role == "assistant" && message.ToolName != "" {
		values = append(values, "message--tool-call")
	}
	if message.ToolTranscript {
		values = append(values, "message--tool-transcript")
	}
	if message.Error {
		values = append(values, "message--tool-error")
	}
	return strings.Join(values, " ")
}
func messageRoleLabel(message *sessions.Message) string {
	if message.Compact && message.Role == "assistant" && message.ToolName != "" {
		return "tool"
	}
	if message.Role == "custom" && message.CustomType != "" {
		return "[" + message.CustomType + "]"
	}
	switch message.Role {
	case "assistant":
		return "pi"
	case "toolResult":
		return "tool result"
	case "bashExecution":
		return "shell"
	case "":
		return "status"
	default:
		return message.Role
	}
}
func messageFingerprint(message *sessions.Message) string {
	if message.Timestamp.IsZero() {
		return ""
	}
	text := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(message.Text, "\r\n", "\n"), "\r", "\n"))
	hash := uint32(5381)
	for _, value := range []byte(text) {
		hash = ((hash << 5) + hash) + uint32(value)
	}
	return fmt.Sprintf("%s:%d:%x", messageRoleKey(message.Role), message.Timestamp.Unix(), hash)
}
func compactBody(view *pageView, message *sessions.Message) template.HTML {
	return renderCompactLines(message, strings.Split(strings.TrimSuffix(message.Text, "\n"), "\n"), 0, view.Home)
}
func compactTail(view *pageView, message *sessions.Message) template.HTML {
	lines := strings.Split(strings.TrimSuffix(message.Text, "\n"), "\n")
	if len(lines) > toolOutputDesktopTailLines {
		lines = lines[len(lines)-toolOutputDesktopTailLines:]
	}
	return renderCompactLines(message, lines, max(len(lines)-toolOutputMobileTailLines, 0), view.Home)
}
func renderCompactLines(message *sessions.Message, lines []string, desktop int, home string) template.HTML {
	diff := message.ToolTranscript && (message.ToolName == "edit" || message.ToolName == "write")
	var output strings.Builder
	class := "tool-output-content"
	if diff {
		class += " tool-output-content--diff"
	}
	output.WriteString(`<span class="` + class + `">`)
	for index, line := range lines {
		if !message.ToolPreview {
			line = sessions.DisplayHomePath(line, home)
		}
		lineClass := "tool-output-line"
		if diff {
			lineClass = "tool-diff-line tool-diff-line--context"
			if strings.HasPrefix(line, "+") {
				lineClass = "tool-diff-line tool-diff-line--add"
			}
			if strings.HasPrefix(line, "-") {
				lineClass = "tool-diff-line tool-diff-line--remove"
			}
			if strings.HasPrefix(line, "Edit ") {
				lineClass = "tool-diff-line tool-diff-line--meta"
			}
		}
		if index < desktop {
			lineClass += " tool-output-tail-desktop-extra"
		}
		output.WriteString(`<span class="` + lineClass + `">` + template.HTMLEscapeString(line) + `</span>`)
	}
	output.WriteString(`</span>`)
	return template.HTML(output.String())
}
func statusItems(status sessions.Status) [][2]string {
	var result [][2]string
	if status.HasContextTokens || status.HasContextLimit {
		value := ""
		if status.HasContextLimit {
			percent := status.ContextPercent
			if percent == 0 && status.HasContextTokens {
				percent = status.ContextTokens / status.ContextLimit * 100
			}
			value = fmt.Sprintf("%.1f%%/%s", percent, compactNumber(status.ContextLimit))
		} else {
			value = compactNumber(status.ContextTokens)
		}
		if status.ContextEstimated {
			value = "≈" + value
		}
		result = append(result, [2]string{"CTX", value})
	}
	model := strings.Trim(strings.Join([]string{status.Provider, status.ModelID}, "/"), "/")
	if model != "" {
		if status.ThinkingLevel != "" {
			model += " (" + status.ThinkingLevel + ")"
		}
		result = append(result, [2]string{"Model", model})
	}
	return result
}
func compactNumber(value float64) string {
	if value < 1000 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	if value < 1e6 {
		return strconv.FormatFloat(value/1000, 'f', 1, 64) + "k"
	}
	return strconv.FormatFloat(value/1e6, 'f', 1, 64) + "M"
}
func bashStatusItems(message *sessions.Message) []string {
	var values []string
	if message.BashExcludedFromContext {
		values = append(values, "excluded from model context")
	}
	if message.BashExitCode != nil && *message.BashExitCode != 0 {
		values = append(values, fmt.Sprintf("exit %d", *message.BashExitCode))
	}
	if message.BashCancelled {
		values = append(values, "cancelled")
	}
	if message.BashTruncated {
		values = append(values, "output truncated")
		if message.BashFullOutputPath != "" {
			values = append(values, "full output: "+message.BashFullOutputPath)
		}
	}
	return values
}
func attachmentLabel(count int) string {
	return fmt.Sprintf("📎 %d image attachment%s", count, plural(count))
}
func imageSource(image sessions.Image) template.URL {
	if strings.HasPrefix(image.Src, "/attachments/") {
		return template.URL(image.Src)
	}
	if len(image.Data) == 0 || len(image.Data) > 32<<20 {
		return ""
	}
	switch image.MIMEType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return ""
	}
	decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(image.Data))
	if _, err := io.Copy(io.Discard, decoder); err != nil {
		return ""
	}
	return template.URL("data:" + image.MIMEType + ";base64," + image.Data)
}
func visibleImages(view *pageView, message *sessions.Message) []sessions.Image {
	if len(message.Images) > 0 {
		return message.Images
	}
	return view.Attachments[message].Images
}
func attachmentCount(view *pageView, message *sessions.Message) int {
	return view.Attachments[message].Count
}
func collapsible(message *sessions.Message) bool {
	return message.Compact && !message.Thinking && !message.FinalAssistantResponse && len(strings.Split(strings.TrimSuffix(message.Text, "\n"), "\n")) > toolOutputDesktopTailLines
}
func terminalOutput(message *sessions.Message) bool {
	if !message.Compact || message.ToolName == "read" || message.ToolName == "edit" || message.ToolName == "write" {
		return false
	}
	return strings.ContainsAny(message.Text, "\b\x1b") || strings.Contains(message.Text, "\r")
}
func terminalSource(message *sessions.Message) string {
	value := message.Text
	if len(value) > 262144 {
		if latest := strings.LastIndex(value, "\x1b[2J"); latest >= 0 && len(value)-latest <= 262144 {
			value = value[latest:]
		} else {
			value = value[len(value)-262144:]
		}
		value = strings.ToValidUTF8(value, "�")
	}
	return base64Text(value)
}
func base64Text(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }
