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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/sessions"
)

const (
	recentSessionLimit           = 20
	sessionPageSize              = 20
	toolOutputDesktopTailLines   = 18
	toolOutputMobileTailLines    = 12
	toolOutputCollapseCharacters = 500
	toolOutputTailCharacters     = 2000
	cliSessionsCookie            = "gripi_show_cli_sessions"
)

var projectColors = []string{
	"#e6a66f", "#8db9ef", "#c5a0e8", "#ef9aae", "#acd276", "#75c5df",
	"#dfa0d4", "#e0bd65", "#86cb98", "#a5afe9", "#eda18b", "#76cbbf",
}

type sidebarActivity struct{ Busy, Compacting bool }
type sidebarSessionData struct {
	View     *pageView
	Session  *agent.Session
	Shortcut int
}
type messageData struct {
	View    *pageView
	Message *agent.Message
}
type relationData struct {
	View             *pageView
	Session, Current *agent.Session
}

type liveOutputData struct {
	EventAfter               int64
	ActiveToolEventsJSON     string
	ActiveAssistantEventJSON string
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
	Nonce                     string
	ServerOrigin              string
	Params                    url.Values
	Sessions                  []*agent.Session
	Selected                  *agent.Session
	SidebarSessions           []*agent.Session
	PinnedSessions            []*agent.Session
	SeparateCurrent           *agent.Session
	KnownCWDs                 []string
	NewSessionCWDs            []string
	ProjectActivity           map[string]time.Time
	ProjectMonograms          map[string]string
	SelectedProject           string
	SearchQuery               string
	SelectedTag               string
	SessionTags               map[string][]string
	TagColors                 map[string]string
	Unread                    map[string]bool
	ExternalFollow            map[string]bool
	CompletedReplies          map[string]sessions.Completion
	Pinned                    map[string]bool
	UnreadCount               int
	SessionsLimit             int
	SessionPool               []*agent.Session
	SessionPoolLength         int
	SessionTotal              int
	CLISessionsShown          bool
	CLISessionCount           int
	Window                    agent.Window
	Attachments               map[*agent.Message]sessions.AttachmentMatch
	SessionOnly               bool
	GatewayInstanceID         string
	Home                      string
	BrowserAccessEnabled      bool
	WorkspaceAccessEnabled    bool
	ResourceMonitoringEnabled bool
	SessionGeneration         string
	SessionSyncMode           agent.SyncMode
	SessionSyncRevision       string
	SessionSyncError          string
	SessionSyncBlocked        bool
	SessionSyncGatewayBusy    bool
	HideThinkingBlock         bool
	SidebarMetadataDeferred   bool
	SidebarActivity           map[string]sidebarActivity
	LiveOutput                liveOutputData
	// HermesEnabled offers the Hermes panel. HermesPanel is the page that the panel shows.
	HermesEnabled bool
	HermesPanel   bool
	// VoiceEnabled offers Hermes sessions a voice conversation.
	VoiceEnabled bool
}

// voicePrefix starts the messages of a voice conversation, as VOICE_PREFIX in public/assets/formatting.js.
const voicePrefix = "[Voice input — respond concisely and conversationally, 2-3 sentences max. No code blocks or markdown.] "

func (app *application) preparePage(request *http.Request, includeConversation bool) (*pageView, error) {
	// A managed session may still be registered under its synthetic pending path.
	for _, pending := range app.pendingSessions.Entries() {
		if !app.rpcClients.Active(pending.Path) || (app.ownsSession != nil && !app.ownsSession(request, pending.Path)) {
			continue
		}
		if _, err := app.canonicalRPCSessionPath(request, pending.Path); err != nil &&
			!errors.Is(err, agent.ErrOperationPending) && !errors.Is(err, agent.ErrClientRetiring) &&
			!errors.Is(err, agent.ErrClientStarting) && !errors.Is(err, errSessionNavigating) {
			return nil, err
		}
	}
	params := request.URL.Query()
	selectedPath := params.Get("session")
	selectedOwned := app.ownsSession == nil || app.ownsSession(request, selectedPath)
	if selectedOwned && selectedPath != "" {
		resolved, _, err := app.resolveOwnedPendingPath(request, selectedPath)
		if err != nil {
			return nil, err
		}
		if resolved != selectedPath {
			params.Set("session", resolved)
			request.URL.RawQuery = params.Encode()
		}
	}
	all, metadataDeferred, err := app.sessionListing(request).SessionsDeferringMetadata(func(path string) bool {
		return request.URL.Path == "/sidebar" && app.rpcClients.Busy(path)
	})
	if err != nil {
		return nil, err
	}
	hermesPanel := params.Get("session_only") == "1" && app.agentOf(params.Get("session")) == "hermes"
	// Pages don't list Hermes's conversations, so the Hermes panel's page adds its own.
	if hermesPanel {
		if session, saved := app.sessionStore.Session(params.Get("session")); saved {
			all = append(all, session)
		}
	}
	projectCWDs, err := app.gatewayState.ProjectCWDs(all)
	if err != nil {
		return nil, err
	}
	knownPaths := make(map[string]bool, len(all))
	for _, session := range all {
		knownPaths[session.Path] = true
	}
	unresolvedPendingCWDs := make(map[string]bool)
	for _, pending := range app.pendingSessions.Entries() {
		if app.rpcClients.Registered(pending.Path) && (app.ownsSession == nil || app.ownsSession(request, pending.Path)) {
			unresolvedPendingCWDs[pending.CWD] = true
		}
		if knownPaths[pending.Path] {
			continue
		}
		all = append(all, &agent.Session{Path: pending.Path, ID: pending.ID, CWD: pending.CWD, DisplayName: "New session (pending first assistant response)", CreatedAt: pending.CreatedAt, ModifiedAt: pending.CreatedAt, ConversationActivityAt: pending.CreatedAt, Agent: app.agentOf(pending.Path)})
	}
	sort.SliceStable(all, func(left, right int) bool {
		return all[left].ConversationActivityAt.After(all[right].ConversationActivityAt)
	})
	if app.ownsSession != nil {
		ownedPaths := make(map[string]bool)
		if app.ownershipStore != nil {
			var err error
			ownedPaths, err = app.ownershipStore.OwnedPaths(currentWorkspaceID(request))
			if err != nil {
				return nil, err
			}
		}
		owned := all[:0]
		for _, session := range all {
			isOwned := ownedPaths[session.Path]
			if app.ownershipStore == nil {
				isOwned = app.ownsSession(request, session.Path)
			}
			if isOwned {
				owned = append(owned, session)
			}
		}
		all = owned
	}
	app.rememberSessionHashes(all)
	params = request.URL.Query()
	// A page shows Hermes's conversations only in the Hermes panel, the session-only page of one of them.
	if !commandRequest(request) {
		all = slices.DeleteFunc(all, func(session *agent.Session) bool {
			return session.Agent == "hermes" && !(hermesPanel && session.Path == params.Get("session"))
		})
	}
	selectedProject := params.Get("project")
	knownProjects := make(map[string]bool)
	for _, session := range all {
		if projectCWDs[session.CWD] {
			knownProjects[session.CWD] = true
		}
	}
	if !knownProjects[selectedProject] {
		selectedProject = ""
	}
	var selected *agent.Session
	if params.Get("no_session") != "1" {
		for _, session := range all {
			if session.Path == params.Get("session") {
				selected = session
				break
			}
		}
		// The Hermes panel shows no other session in place of its conversation.
		if selected == nil && len(all) > 0 && !hermesPanel {
			excluded := params["session_fallback_excluding"]
			for _, session := range all {
				if !slices.Contains(excluded, session.Path) {
					selected = session
					break
				}
			}
		}
	}
	var selectedSync *agent.SyncResult
	externalFollow := make(map[string]bool)
	if app.guard != nil {
		for _, session := range all {
			// Do not attribute appends while an owned pending client could manage this file.
			if knownPaths[session.Path] && (app.rpcClients.Active(session.Path) || !unresolvedPendingCWDs[session.CWD]) {
				includePosition := includeConversation && session == selected
				state := app.guard.InspectIfAvailable(request.Context(), session.Path, includePosition)
				if includePosition {
					selectedSync = state
				}
			}
			if state := app.guard.KnownBlocked(session.Path); state != nil && state.Mode == agent.SyncExternalFollow {
				externalFollow[session.Path] = true
			}
		}
	}
	// Browsers ask with mark_read=0 while their window has no focus.
	markRead := (request.URL.Path != "/sidebar" || params.Get("session") != "") && params.Get("mark_read") != "0"
	unread, pinned, err := app.gatewayState.ReadAndObservePending(all, selected, markRead, externalFollow, unresolvedPendingCWDs)
	if err != nil {
		return nil, err
	}
	view := &pageView{Request: request, Nonce: requestNonce(request), ServerOrigin: absoluteRedirectURL(request, "", app.config.TrustProxyHeaders), Params: params, Sessions: all, Selected: selected, SelectedProject: selectedProject, SearchQuery: strings.TrimSpace(params.Get("session_search")), Unread: unread, Pinned: pinned, SessionOnly: params.Get("session_only") == "1", GatewayInstanceID: app.instanceID, Home: app.config.Home, BrowserAccessEnabled: !app.config.BrowserAuthDisabled, WorkspaceAccessEnabled: app.config.MultiUserMode, ResourceMonitoringEnabled: app.config.ResourceMonitoringEnabled, SidebarMetadataDeferred: metadataDeferred, SidebarActivity: make(map[string]sidebarActivity), HermesEnabled: app.config.HermesPath != "", HermesPanel: hermesPanel, VoiceEnabled: app.voice != nil}
	view.ExternalFollow = externalFollow
	if notifier := app.completionNotifications; notifier != nil {
		notifier.mu.Lock()
		notifier.pruneClientsLocked()
		notifier.mu.Unlock()
	}
	view.CompletedReplies, err = app.gatewayState.Completions()
	if err != nil {
		return nil, err
	}
	assignments, err := app.gatewayState.SessionTags()
	if err != nil {
		return nil, err
	}
	colors, err := app.gatewayState.TagColors()
	if err != nil {
		return nil, err
	}
	view.SessionTags = make(map[string][]string, len(all))
	view.TagColors = make(map[string]string)
	for _, session := range all {
		view.SessionTags[session.Path] = assignments[session.Path]
		for _, name := range assignments[session.Path] {
			view.TagColors[name] = colors[name]
		}
	}
	view.SelectedTag, _ = sessions.NormalizeTag(params.Get("tag"))
	view.prepareSidebar()
	renderedSessions := make([]*agent.Session, 0, len(view.PinnedSessions)+len(view.SidebarSessions)+1)
	renderedSessions = append(renderedSessions, view.PinnedSessions...)
	renderedSessions = append(renderedSessions, view.SidebarSessions...)
	if view.SeparateCurrent != nil {
		renderedSessions = append(renderedSessions, view.SeparateCurrent)
	}
	for _, session := range renderedSessions {
		activity := sidebarActivity{Busy: app.rpcClients.Busy(session.Path), Compacting: app.rpcClients.Compacting(session.Path)}
		if activity.Busy || activity.Compacting {
			view.SidebarActivity[session.Path] = activity
		}
	}
	view.KnownCWDs, view.ProjectActivity = knownCWDs(all, knownProjects)
	view.NewSessionCWDs = app.newSessionCWDs(view)
	if view.ProjectMonograms, err = app.gatewayState.ProjectMonograms(view.NewSessionCWDs); err != nil {
		return nil, err
	}
	if includeConversation && selected != nil {
		view.HideThinkingBlock = app.adapter.DisplaySettings(selected.CWD).HideThinkingBlock
		leafID, leafSupplied := "", false
		view.SessionSyncMode = agent.SyncAvailable
		saved := app.sessionStore.Exists(selected.Path)
		if state := selectedSync; state != nil {
			view.SessionSyncMode, view.SessionSyncRevision, view.SessionSyncError = state.Mode, state.Revision, state.Error
			view.SessionSyncBlocked = state.Blocked()
			if state.Mode == agent.SyncManaged {
				leafID, leafSupplied = state.RPCLeafID, true
			} else if state.Blocked() {
				leafID, leafSupplied = state.PersistedLeafID, true
			}
		}
		if selected.FolderMissing {
			view.SessionSyncMode, view.SessionSyncBlocked = agent.SyncFolderMissing, true
		}
		view.SessionSyncGatewayBusy = view.SessionSyncBlocked && app.rpcClients.Busy(selected.Path)
		snapshot := app.rpcClients.LiveSnapshot(selected.Path)
		window := agent.Window{}
		if saved {
			// A run that starts or ends during the read would pair its messages with the other run state, so read
			// again until the state holds across a read. An idle agent also must not have had events, which a whole
			// run would leave; a running one has them for every streamed delta. The snapshot stays the earlier one:
			// its event cursor must not skip anything the window lacks.
			for attempt := 1; ; attempt++ {
				var err error
				window, err = app.sessionStore.Window(selected.Path, leafID, leafSupplied, nil, nil)
				if err != nil {
					return nil, err
				}
				latest := app.rpcClients.LiveSnapshot(selected.Path)
				settled := latest.AgentRunning == snapshot.AgentRunning && (snapshot.AgentRunning || latest.EventSequence == snapshot.EventSequence)
				if settled || attempt == 3 {
					break
				}
				snapshot = latest
			}
		}
		view.Window = window
		view.Attachments = make(map[*agent.Message]sessions.AttachmentMatch)
		if saved {
			view.Attachments = (sessions.AttachmentStore{Root: app.config.AttachmentsRoot, Keys: app.sessionStore}).Match(selected.Path, window.Messages)
		}
		view.SessionGeneration = app.sessionStore.Generation(selected.Path)
		activeToolIDs := make([]string, 0, len(snapshot.ActiveTools))
		for _, tool := range snapshot.ActiveTools {
			if tool.ID != "" {
				activeToolIDs = append(activeToolIDs, tool.ID)
			}
		}
		toolContext := map[string]agent.ToolCallContext(nil)
		if saved {
			toolContext = app.sessionStore.SubagentToolCallContext(selected.Path, window.TreeLeafID, activeToolIDs)
		}
		view.LiveOutput = liveOutputFrom(snapshot, window.Messages, app.config.Home, toolContext)
	}
	return view, nil
}

func liveOutputFrom(snapshot agent.LiveSnapshot, messages []*agent.Message, home string, toolContext map[string]agent.ToolCallContext) liveOutputData {
	persistedToolResults := map[string]bool{}
	for _, message := range messages {
		if (message.Role == "toolResult" || message.ToolResultPersisted) && message.ToolCallID != "" {
			persistedToolResults[message.ToolCallID] = true
		}
	}
	for id, details := range toolContext {
		if details.ResultPersisted {
			persistedToolResults[id] = true
		}
	}
	activeTools := make([]json.RawMessage, 0, len(snapshot.ActiveTools))
	for _, tool := range snapshot.ActiveTools {
		if !persistedToolResults[tool.ID] {
			activeTools = append(activeTools, tool.Data)
		}
	}
	extensionUI := any(snapshot.ExtensionUI)
	if snapshot.ExtensionUI == nil {
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
	if shell := snapshot.ActiveShell; shell != nil {
		if activeShellPersisted(*shell, messages, home) {
			persistedBash = append(persistedBash, shell.ID)
		} else {
			activeBash = map[string]any{"bash_id": shell.ID, "command": shell.Command, "exclude_from_context": shell.ExcludeFromContext, "started_at": shell.StartedAt, "output": shell.Output}
		}
	}
	completedBash := make([]json.RawMessage, 0, len(snapshot.CompletedShells))
	for _, shell := range snapshot.CompletedShells {
		if completedShellPersisted(shell, messages, home) {
			persistedBash = append(persistedBash, shell.ID)
		} else {
			completedBash = append(completedBash, shell.Data)
		}
	}
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
		EventAfter:               snapshot.EventSequence,
		ActiveAssistantEventJSON: jsonData(snapshot.ActiveAssistant),
		ActiveToolEventsJSON:     jsonData(activeTools), ActiveToolTimestampsJSON: jsonData(toolTimestamps), ActiveToolPromptsJSON: jsonData(toolPrompts),
		ActiveBashJSON: jsonData(activeBash), CompletedBashEventsJSON: jsonData(completedBash), PersistedBashJSON: jsonData(persistedBash),
		QueuedMessagesJSON: jsonData(queuedMessages(snapshot.Queue)), ExtensionUIJSON: jsonData(extensionUI), ComposerState: composerState,
		ComposerStateSince: millisecondsString(stateSince), ComposerBusySince: millisecondsString(agentBusySince),
		Compacting: snapshot.Compacting, AgentRunning: snapshot.AgentRunning,
	}
}

// queuedMessages is the queue as the browser reads it.
func queuedMessages(queue agent.Queue) map[string][]string {
	return map[string][]string{"steering": append([]string{}, queue.Steering...), "followUp": append([]string{}, queue.FollowUp...)}
}

func activeShellPersisted(shell agent.Shell, messages []*agent.Message, home string) bool {
	if shell.StartedAt.IsZero() {
		return false
	}
	summary := "$ " + agent.DisplayHomePath(shell.Command, home)
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == "bashExecution" && message.Summary == summary && !message.Timestamp.IsZero() && !message.Timestamp.Before(shell.StartedAt) {
			return true
		}
	}
	return false
}

func completedShellPersisted(shell agent.CompletedShell, messages []*agent.Message, home string) bool {
	if !shell.Ended {
		return false
	}
	summary := "$ " + agent.DisplayHomePath(shell.Command, home)
	for _, message := range messages {
		if message.Role != "bashExecution" || message.Summary != summary || message.Text != shell.Output || message.BashCancelled != shell.Cancelled || message.BashTruncated != shell.Truncated || message.BashExcludedFromContext != shell.ExcludeFromContext || message.BashRecordedAt.IsZero() || message.BashRecordedAt.UnixMilli() < shell.StartedAt.UnixMilli() {
			continue
		}
		if (message.BashExitCode == nil) != (shell.ExitCode == nil) {
			continue
		}
		if shell.ExitCode != nil && *message.BashExitCode != *shell.ExitCode {
			continue
		}
		return true
	}
	return false
}

func jsonData(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }
func millisecondsString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(value.UnixMilli(), 10)
}

func (app *application) rememberSessionHashes(all []*agent.Session) {
	values := make(map[string]bool, len(all))
	for _, session := range all {
		aliases := app.sessionStore.Aliases(session.Path)
		if len(aliases) == 0 {
			aliases = []string{session.Path}
		}
		for _, path := range aliases {
			values[sessions.SessionHash(path)] = true
		}
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
	cookie, err := view.Request.Cookie(cliSessionsCookie)
	view.CLISessionsShown = err == nil && cookie.Value == "1"
	var pool []*agent.Session
	query := strings.ToLower(view.SearchQuery)
	for _, session := range view.Sessions {
		if view.Pinned[session.Path] {
			continue
		}
		// Tags, like pins, keep a Pi CLI session listed.
		hideable := view.ExternalFollow[session.Path] && len(view.SessionTags[session.Path]) == 0
		hidden := hideable && !view.CLISessionsShown
		if !hidden {
			view.SessionTotal++
		}
		if view.SelectedTag != "" && !slices.Contains(view.SessionTags[session.Path], view.SelectedTag) {
			continue
		}
		if view.SelectedProject != "" && session.CWD != view.SelectedProject {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(strings.Join([]string{session.DisplayName, session.CWD, filepath.Base(session.CWD), session.FirstUserMessage}, "\n")), query) {
			continue
		}
		if hideable {
			view.CLISessionCount++
		}
		if hidden {
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
	// Unread sessions past the limit show too, in their place, so none waits out of sight.
	for index, session := range pool {
		if index < limit || view.Unread[session.Path] {
			view.SidebarSessions = append(view.SidebarSessions, session)
		}
	}
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

func knownCWDs(all []*agent.Session, projects map[string]bool) ([]string, map[string]time.Time) {
	latest := make(map[string]time.Time)
	for _, session := range all {
		if projects[session.CWD] && session.ConversationActivityAt.After(latest[session.CWD]) {
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
	return result, latest
}

func (app *application) newSessionCWDs(view *pageView) []string {
	var values []string
	for _, cwd := range view.KnownCWDs {
		if stat, err := os.Stat(cwd); err == nil && stat.IsDir() {
			values = append(values, cwd)
		}
	}
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
	if preferred != "" && slices.Contains(unique, preferred) {
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
		"projectColor": projectColor, "projectMonogram": projectMonogram, "tagStyle": tagStyle, "relativeTime": relativeTime, "compactRelativeTime": compactRelativeTime, "activityDay": activityDay, "formatTime": func(value time.Time) string {
			if value.IsZero() {
				return "unknown"
			}
			return value.Local().Format("2006-01-02 15:04")
		},
		"sessionURL": sessionURL, "sessionClasses": sessionClasses, "unreadLabel": unreadLabel, "plural": plural,
		"sessionData": func(view *pageView, session *agent.Session, shortcut int) sidebarSessionData {
			return sidebarSessionData{view, session, shortcut}
		},
		"sessionParent": sessionParent, "sessionChildren": sessionChildren, "sessionRoot": sessionRoot,
		"relationData": func(view *pageView, session, current *agent.Session) relationData {
			return relationData{view, session, current}
		},
		"messageData":    func(view *pageView, message *agent.Message) messageData { return messageData{view, message} },
		"sidebarLoadURL": sidebarLoadURL, "filtersClearURL": filtersClearURL, "agentLabel": agentLabel, "hermesGlyph": hermesGlyph,
		"messageClass": messageClass, "messageRoleLabel": messageRoleLabel, "messageFingerprint": messageFingerprint, "messageTime": func(value time.Time) string { return messageTimeLabel(value, time.Now()) },
		"messageBody": func(message *agent.Message) template.HTML {
			if (message.Role == "assistant" || message.Role == "custom" || message.Thinking) && !message.Compact {
				return template.HTML(markdownRenderer.Render(message.Text))
			}
			if spokenMessage(message) {
				return template.HTML(template.HTMLEscapeString(strings.TrimPrefix(message.Text, voicePrefix)))
			}
			return template.HTML(template.HTMLEscapeString(message.Text))
		},
		"spokenMessage": spokenMessage, "spokenBars": func(message *agent.Message) string { return spokenBars(strings.TrimPrefix(message.Text, voicePrefix)) },
		"compactBody": compactBody, "compactTail": compactTail, "toolSummary": func(value string) template.HTML { return template.HTML(value) },
		"imageSource": imageSource,
		"statusItems": statusItems, "bashStatusItems": bashStatusItems, "attachmentLabel": attachmentLabel,
		"visibleImages": visibleImages, "attachmentCount": attachmentCount, "collapsible": collapsible, "toolOutputHiddenLabel": toolOutputHiddenLabel,
		"terminalOutput": terminalOutput, "terminalSource": terminalSource, "terminalTruncated": func(message *agent.Message) bool { return len(message.Text) > 262144 },
		"conversationSearch": conversationSearch, "tildePath": tildePath,
	}
}

func projectColor(cwd string) string {
	digest := sha256.Sum256([]byte(filepath.Base(cwd)))
	return projectColors[int(digest[0])%len(projectColors)]
}

// Directories outside the project list have no stored letters, so theirs may repeat.
func projectMonogram(view *pageView, cwd string) string {
	if monogram, ok := view.ProjectMonograms[cwd]; ok {
		return monogram
	}
	return sessions.ProjectMonogram(cwd)
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
func activityDay(value time.Time) string {
	local, today := value.Local(), time.Now().Local()
	yesterday := today.AddDate(0, 0, -1)
	switch {
	case local.YearDay() == today.YearDay() && local.Year() == today.Year():
		return "Today"
	case local.YearDay() == yesterday.YearDay() && local.Year() == yesterday.Year():
		return "Yesterday"
	default:
		return "Earlier"
	}
}

// messageTimeLabel matches messageTimeLabel in public/assets/formatting.js.
func messageTimeLabel(value, now time.Time) string {
	local := value.In(now.Location())
	switch {
	case local.Year() != now.Year():
		return local.Format("Jan 2 2006 15:04")
	case local.YearDay() != now.YearDay():
		return local.Format("Jan 2 15:04")
	default:
		return local.Format("15:04")
	}
}
func compactRelativeTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	age := time.Since(value)
	switch {
	case age < time.Minute:
		return "now"
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(age/(24*time.Hour)))
	}
}

func plural(value int) string {
	if value == 1 {
		return ""
	}
	return "s"
}
func sessionURL(view *pageView, session *agent.Session) string {
	values := url.Values{"session": {session.Path}}
	if view.SelectedTag != "" {
		values.Set("tag", view.SelectedTag)
	}
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
func sessionParent(view *pageView, session *agent.Session) *agent.Session {
	if session == nil || session.ParentSessionPath == "" {
		return nil
	}
	for _, candidate := range view.Sessions {
		if candidate.Path == session.ParentSessionPath {
			return candidate
		}
	}
	return nil
}
func sessionChildren(view *pageView, session *agent.Session) []*agent.Session {
	if session == nil {
		return nil
	}
	var result []*agent.Session
	for _, candidate := range view.Sessions {
		if candidate.ParentSessionPath != "" && candidate.ParentSessionPath == session.Path {
			result = append(result, candidate)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ModifiedAt.After(result[j].ModifiedAt) })
	return result
}
func sessionRoot(view *pageView, session *agent.Session) *agent.Session {
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
	if view.SelectedTag != "" {
		values.Set("tag", view.SelectedTag)
	}
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
func retainedSessionsLimit(view *pageView, session *agent.Session) string {
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
func sessionClasses(view *pageView, session *agent.Session) string {
	values := []string{"session", "recent-session"}
	if view.Selected != nil && view.Selected.Path == session.Path {
		values = append(values, "selected")
	}
	if view.Unread[session.Path] && (view.Selected == nil || view.Selected.Path != session.Path) {
		values = append(values, "unread")
	}
	if view.SidebarActivity[session.Path].Compacting {
		values = append(values, "compacting")
	}
	return strings.Join(values, " ")
}
func unreadLabel(count int) string {
	if count > 99 {
		return "99+"
	}
	return strconv.Itoa(count)
}
func conversationSearch(view *pageView, session *agent.Session) string {
	if len(view.SearchQuery) >= 3 && strings.Contains(strings.ToLower(session.FirstUserMessage), strings.ToLower(view.SearchQuery)) {
		return view.SearchQuery
	}
	return ""
}
func tildePath(home, path string) string {
	if rest, ok := strings.CutPrefix(path, home); ok && home != "" && (rest == "" || strings.HasPrefix(rest, string(filepath.Separator))) {
		return "~" + rest
	}
	return path
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
func messageClass(message *agent.Message) string {
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
	if message.ToolPending {
		values = append(values, "message--tool-pending")
	}
	if message.Omitted {
		values = append(values, "message--omitted")
	}
	return strings.Join(values, " ")
}
func messageRoleLabel(message *agent.Message) string {
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
func messageFingerprint(message *agent.Message) string {
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
func spokenMessage(message *agent.Message) bool {
	return message.Role == "user" && strings.HasPrefix(message.Text, voicePrefix)
}

// spokenBars draws what was said in a voice conversation as level bars like the call's, so that its transcript can stay
// hidden: a bar for every two letters of a word, softer at the ends of longer words. The height comes from the word, so
// spokenBars in public/assets/formatting.js draws the same bars for the messages that the browser renders.
func spokenBars(text string) string {
	levels := []rune("▁▂▃▄▅▆▇█")
	var bars []rune
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		hash := uint32(5381)
		for _, r := range word {
			hash = hash*33 + uint32(r)
		}
		count := (utf8.RuneCountInString(word) + 1) / 2
		for index := range count {
			hash = hash*1103515245 + 12345
			level := 2 + int((hash>>16)%6)
			if count > 2 && (index == 0 || index == count-1) {
				level -= 2
			}
			bars = append(bars, levels[level])
		}
		bars = append(bars, ' ')
	}
	return strings.TrimRight(string(bars[:min(len(bars), 64)]), " ")
}
func compactBody(view *pageView, message *agent.Message) template.HTML {
	return renderCompactLines(message, strings.Split(strings.TrimSuffix(message.Text, "\n"), "\n"), 0, view.Home)
}
func compactTail(view *pageView, message *agent.Message) template.HTML {
	lines := compactTailLines(message.Text, !message.ToolTranscript)

	return renderCompactLines(message, lines, max(len(lines)-toolOutputMobileTailLines, 0), view.Home)
}

func compactTailLines(text string, trimCharacters bool) []string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")

	if len(lines) > toolOutputDesktopTailLines {
		lines = lines[len(lines)-toolOutputDesktopTailLines:]
	}

	if !trimCharacters {
		return lines
	}

	characters := []rune(strings.Join(lines, "\n"))

	if len(characters) > toolOutputTailCharacters {
		lines = strings.Split("…"+string(characters[len(characters)-toolOutputTailCharacters+1:]), "\n")
	}

	return lines
}
func renderCompactLines(message *agent.Message, lines []string, desktop int, home string) template.HTML {
	diff := message.ToolTranscript && (message.ToolName == "edit" || message.ToolName == "write")
	var output strings.Builder
	class := "tool-output-content"
	if diff {
		class += " tool-output-content--diff"
	}
	output.WriteString(`<span class="` + class + `">`)
	for index, line := range lines {
		if !message.ToolPreview {
			line = agent.DisplayHomePath(line, home)
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
func statusItems(status agent.Status) [][2]string {
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
func bashStatusItems(message *agent.Message) []string {
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
func imageSource(view *pageView, image agent.Image) string {
	if image.Src != "" {
		return image.Src
	}
	// The extension keeps the image viewer's download filename meaningful.
	return "/session_images/" + url.PathEscape(image.EntryID) + "/" + strconv.Itoa(image.Index) + "." + strings.TrimPrefix(image.MIMEType, "image/") + "?session=" + url.QueryEscape(view.Selected.Path)
}
func visibleImages(view *pageView, message *agent.Message) []agent.Image {
	if len(message.Images) > 0 {
		return message.Images
	}
	return view.Attachments[message].Images
}
func attachmentCount(view *pageView, message *agent.Message) int {
	return view.Attachments[message].Count
}
func collapsible(message *agent.Message) bool {
	lines := strings.Split(strings.TrimSuffix(message.Text, "\n"), "\n")
	longOutput := !message.ToolTranscript && utf8.RuneCountInString(message.Text) > toolOutputCollapseCharacters

	return message.Compact && !message.Thinking && !message.FinalAssistantResponse && (len(lines) > toolOutputDesktopTailLines || longOutput)
}

func toolOutputHiddenLabel(message *agent.Message, tailLines int) string {
	if !message.ToolTranscript && utf8.RuneCountInString(message.Text) > toolOutputCollapseCharacters {
		return "… (earlier output)"
	}

	lines := len(strings.Split(strings.TrimSuffix(message.Text, "\n"), "\n"))

	return fmt.Sprintf("… (%d earlier lines)", lines-tailLines)
}
func terminalOutput(message *agent.Message) bool {
	if !message.Compact || message.ToolName == "read" || message.ToolName == "edit" || message.ToolName == "write" {
		return false
	}
	return strings.ContainsAny(message.Text, "\b\x1b") || strings.Contains(message.Text, "\r")
}
func terminalSource(message *agent.Message) string {
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

// pageStore lists the sessions that pages show. It leaves out Hermes's, so that a page never waits for Hermes to start:
// the Hermes panel opens its conversation itself.
func (app *application) pageStore() agent.Store {
	if router, ok := app.adapter.(*agent.Router); ok {
		return router.StoreUnlisting("hermes")
	}
	return app.sessionStore
}

// sessionListing lists the sessions that a request sees: a page's, or every session for gripi commands.
func (app *application) sessionListing(request *http.Request) agent.Store {
	if commandRequest(request) {
		return app.sessionStore
	}
	return app.pageStore()
}

// agentOf names the agent of a session when the gateway runs several, and is empty otherwise.
func (app *application) agentOf(key string) string {
	if router, ok := app.adapter.(*agent.Router); ok {
		return router.Agent(key)
	}
	return ""
}

// agentLabel names a session's agent for the user, and Pi when the gateway runs Pi alone.
func agentLabel(name string) string {
	if name == "hermes" {
		return "Hermes"
	}
	return "Pi"
}

// hermesGlyph is Hermes's caduceus, which marks the Hermes panel and its button.
func hermesGlyph() template.HTML {
	return `<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M12 3v18"/><path d="M12 5.5C10 3.5 6.5 3.2 3.5 4.5c2.3 1.4 5.4 2 8.5 2.5M12 5.5c2-2 5.5-2.3 8.5-1-2.3 1.4-5.4 2-8.5 2.5"/><path d="M9 10c0 1.8 6 1.4 6 3.5S9 15.7 9 17.5s4.5 1.9 6 1.5"/></svg>`
}
