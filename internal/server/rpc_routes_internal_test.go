package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestCanonicalRPCSessionPathMovesPendingClientAndGatewayAttachments(t *testing.T) {
	root := t.TempDir()
	sessionsRoot := filepath.Join(root, "sessions")
	attachmentsRoot := filepath.Join(root, "attachments")
	project := filepath.Join(root, "project")
	for _, path := range []string{sessionsRoot, attachmentsRoot, project} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	realPath := filepath.Join(sessionsRoot, "real.jsonl")
	header := map[string]any{"type": "session", "version": 3, "id": "real", "cwd": project}
	encoded, _ := json.Marshal(header)
	if err := os.WriteFile(realPath, append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(sessionsRoot, "pending.jsonl")
	client := &remapClient{state: map[string]any{"type": "response", "success": true, "data": map[string]any{"sessionFile": realPath}}}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(pendingPath, client); err != nil {
		t.Fatal(err)
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remember(pendingPath, project)
	metadata := filepath.Join(attachmentsRoot, sessions.SessionHash(pendingPath)+".jsonl")
	if err := os.WriteFile(metadata, []byte("gateway metadata\n"), 0600); err != nil {
		t.Fatal(err)
	}
	app := &application{config: config.Config{SessionsRoot: sessionsRoot, AttachmentsRoot: attachmentsRoot}, sessionCache: sessions.NewCache(), rpcClients: registry, pendingSessions: pending}

	result, err := app.canonicalRPCSessionPath(httptest.NewRequest(http.MethodGet, "http://app.test/", nil), pendingPath)
	if err != nil || result != realPath {
		t.Fatalf("remapped path = %q, %v", result, err)
	}
	if registry.Active(pendingPath) || !registry.Active(realPath) {
		t.Fatal("client was not moved atomically")
	}
	if _, ok := pending.CWD(pendingPath); ok {
		t.Fatal("pending metadata remained")
	}
	if remapped, ok := pending.Resolve(pendingPath); !ok || remapped != realPath {
		t.Fatalf("pending remap alias = %q, %v", remapped, ok)
	}
	migrated, err := os.ReadFile(filepath.Join(attachmentsRoot, sessions.SessionHash(realPath)+".jsonl"))
	if err != nil || string(migrated) != "gateway metadata\n" {
		t.Fatalf("migrated metadata = %q, %v", migrated, err)
	}
}

func TestCanonicalRPCSessionPathFinalizesPendingSessionAtSamePath(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "materialized.jsonl")
	writeSessionRecords(t, path, []map[string]any{{"type": "session", "version": 3, "id": "real", "cwd": project}})
	client := &remapClient{state: map[string]any{"success": true, "data": map[string]any{"sessionFile": path}}}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(path, client); err != nil {
		t.Fatal(err)
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remember(path, project)
	app := &application{config: config.Config{SessionsRoot: root, AttachmentsRoot: t.TempDir()}, sessionCache: sessions.NewCache(), rpcClients: registry, pendingSessions: pending}

	result, err := app.canonicalRPCSessionPath(httptest.NewRequest(http.MethodGet, "http://app.test/", nil), path)
	if err != nil || result != path || !registry.Active(path) {
		t.Fatalf("result=%q active=%v err=%v", result, registry.Active(path), err)
	}
	if _, ok := pending.CWD(path); ok {
		t.Fatal("materialized session remained pending")
	}
}

func TestPreparePageCanonicalizesSelectedPendingSessionBeforeBuildingView(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(root, "real.jsonl")
	writeSessionRecords(t, realPath, []map[string]any{{"type": "session", "version": 3, "id": "real", "timestamp": "2026-01-01T00:00:00Z", "cwd": project}})
	pendingPath := filepath.Join(root, "pending.jsonl")
	client := &remapClient{state: map[string]any{"success": true, "data": map[string]any{"sessionFile": realPath}}}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(pendingPath, client); err != nil {
		t.Fatal(err)
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remember(pendingPath, project)
	cache := sessions.NewCache()
	app := &application{config: config.Config{SessionsRoot: root, Home: root, AttachmentsRoot: filepath.Join(root, "attachments")}, sessionCache: cache, gatewayState: sessions.NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pinned")), rpcClients: registry, pendingSessions: pending}
	app.synchronizer = sessions.NewSynchronizer(root, root, cache, registry)
	request := httptest.NewRequest(http.MethodGet, "http://app.test/?session="+url.QueryEscape(pendingPath), nil)

	view, err := app.preparePage(request, false)
	if err != nil {
		t.Fatal(err)
	}
	if view.Selected == nil || view.Selected.Path != realPath || request.URL.Query().Get("session") != realPath {
		t.Fatalf("selected = %#v, query = %q", view.Selected, request.URL.Query().Get("session"))
	}
}

func TestPendingRemapVerifiesSourceOwnershipAndClaimsDestination(t *testing.T) {
	client := &remapClient{}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register("/pending", client); err != nil {
		t.Fatal(err)
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remember("/pending", "/project")
	request := httptest.NewRequest(http.MethodGet, "http://app.test/", nil)
	claimed := ""
	app := &application{config: config.Config{AttachmentsRoot: t.TempDir()}, rpcClients: registry, pendingSessions: pending,
		ownsSession:  func(_ *http.Request, path string) bool { return path == "/pending" },
		claimSession: func(_ *http.Request, path string) error { claimed = path; return nil },
	}
	if err := app.movePendingRPCClient(request, "/pending", "/real"); err != nil {
		t.Fatal(err)
	}
	if claimed != "/real" || registry.Active("/pending") || !registry.Active("/real") {
		t.Fatalf("claimed=%q pending=%v real=%v", claimed, registry.Active("/pending"), registry.Active("/real"))
	}

	other := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := other.Register("/other-pending", &remapClient{}); err != nil {
		t.Fatal(err)
	}
	otherPending := rpc.NewPendingSessionRegistry(nil)
	otherPending.Remember("/other-pending", "/project")
	app.rpcClients, app.pendingSessions = other, otherPending
	app.ownsSession = func(*http.Request, string) bool { return false }
	if err := app.movePendingRPCClient(request, "/other-pending", "/other-real"); err == nil {
		t.Fatal("unowned pending session was remapped")
	}
	if !other.Active("/other-pending") || other.Active("/other-real") {
		t.Fatal("rejected ownership changed clients")
	}
}

func TestEventsContinuesResolvingACompletedPendingSessionRemap(t *testing.T) {
	client := &remapClient{}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register("/real", client); err != nil {
		t.Fatal(err)
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remap("/pending", "/real")
	app := &application{config: config.Config{}, sessionCache: sessions.NewCache(), rpcClients: registry, pendingSessions: pending}
	for iteration := 0; iteration < 2; iteration++ {
		request := httptest.NewRequest(http.MethodGet, "http://app.test/events?session="+url.QueryEscape("/pending")+"&after=0", nil)
		response := httptest.NewRecorder()
		app.events(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"session_sync":{"error":null,"gateway_busy":false,"mode":"available"`) {
			t.Fatalf("event poll %d = %d %s", iteration, response.Code, response.Body.String())
		}
	}
}

func TestPreparePageUsesManagedRPCLeafAndLiveSnapshot(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session.jsonl")
	records := []map[string]any{
		{"type": "session", "version": 3, "id": "session", "timestamp": "2026-01-01T00:00:00Z", "cwd": project},
		{"type": "message", "id": "branch-a", "parentId": nil, "timestamp": "2026-01-01T00:00:01Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Branch A"}}}},
		{"type": "message", "id": "source", "parentId": "branch-a", "timestamp": "2026-01-01T00:00:02Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "toolCall", "id": "tool-1", "name": "subagent", "arguments": map[string]any{"task": "Review active work"}}}}},
		{"type": "message", "id": "branch-b", "parentId": nil, "timestamp": "2026-01-01T00:00:03Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Branch B"}}}},
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	client := &remapClient{position: rpc.SessionEntries{Known: true, LeafID: "source"}, live: rpc.LiveSnapshot{EventSequence: 9, ActiveToolEvents: []map[string]any{{"type": "tool_execution_update", "toolCallId": "tool-1", "toolName": "subagent"}}, AgentRunning: true}}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return client, nil }, nil)
	if err := registry.Register(path, client); err != nil {
		t.Fatal(err)
	}
	cache := sessions.NewCache()
	app := &application{config: config.Config{SessionsRoot: root, Home: root, AttachmentsRoot: filepath.Join(root, "attachments")}, sessionCache: cache, gatewayState: sessions.NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pinned.json")), rpcClients: registry, pendingSessions: rpc.NewPendingSessionRegistry(nil), instanceID: "test"}
	app.synchronizer = sessions.NewSynchronizer(root, root, cache, registry)
	request := httptest.NewRequest(http.MethodGet, "http://app.test/?session="+url.QueryEscape(path), nil)
	view, err := app.preparePage(request, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.Window.TreeLeafID != "source" || len(view.Window.Messages) < 1 || view.Window.Messages[0].Text != "Branch A" {
		t.Fatalf("managed window = %#v", view.Window)
	}
	if view.LiveOutput.EventAfter != 9 || view.LiveOutput.ComposerState != "running" || !strings.Contains(view.LiveOutput.ActiveToolEventsJSON, "tool-1") || !strings.Contains(view.LiveOutput.ActiveToolPromptsJSON, "Review active work") || !strings.Contains(view.LiveOutput.ActiveToolTimestampsJSON, "tool-1") {
		t.Fatalf("live output = %#v", view.LiveOutput)
	}
	if view.SessionSyncMode != sessions.SyncManaged {
		t.Fatalf("sync mode = %s", view.SessionSyncMode)
	}
	app.templates, err = template.New("").Funcs(templateFunctions(rendering.NewMarkdown())).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var rendered strings.Builder
	if err := app.templates.ExecuteTemplate(&rendered, "conversation", view); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`data-events-after="9"`, `tool-1`, `data-composer-state="running"`, `data-session-sync-mode="managed"`} {
		if !strings.Contains(rendered.String(), expected) {
			t.Errorf("rendered conversation does not contain %q", expected)
		}
	}
}

func TestPreparePageExposesExternalFollowAndUsesPersistedLeaf(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session.jsonl")
	writeSessionRecords(t, path, []map[string]any{{"type": "session", "version": 3, "id": "session", "timestamp": "2026-01-01T00:00:00Z", "cwd": project}, {"type": "message", "id": "old", "parentId": nil, "timestamp": "2026-01-01T00:00:01Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Old"}}}}})
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	cache := sessions.NewCache()
	app := &application{config: config.Config{SessionsRoot: root, Home: root, AttachmentsRoot: filepath.Join(root, "attachments")}, sessionCache: cache, gatewayState: sessions.NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pinned.json")), rpcClients: registry, pendingSessions: rpc.NewPendingSessionRegistry(nil), instanceID: "test"}
	app.synchronizer = sessions.NewSynchronizer(root, root, cache, registry)
	request := httptest.NewRequest(http.MethodGet, "http://app.test/?session="+url.QueryEscape(path), nil)
	if _, err := app.preparePage(request, true); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewEncoder(file).Encode(map[string]any{"type": "message", "id": "external", "parentId": "old", "timestamp": "2026-01-01T00:00:02Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "External"}}}})
	_ = file.Close()
	view, err := app.preparePage(request, true)
	if err != nil {
		t.Fatal(err)
	}
	if !view.SessionSyncBlocked || view.SessionSyncMode != sessions.SyncExternalFollow || view.Window.TreeLeafID != "external" {
		t.Fatalf("external view: blocked=%v mode=%s leaf=%s", view.SessionSyncBlocked, view.SessionSyncMode, view.Window.TreeLeafID)
	}
}

func TestAcceptedPromptImageSurvivesAttachmentMetadataFailure(t *testing.T) {
	path := "/pending-image"
	client := &followUpRaceClient{Client: (*rpc.Client)(nil)}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(path, client); err != nil {
		t.Fatal(err)
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remember(path, "/project")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, sessions.SessionHash(path)+".jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	cache := sessions.NewCache()
	app := &application{config: config.Config{SessionsRoot: t.TempDir(), AttachmentsRoot: root}, sessionCache: cache, rpcClients: registry, pendingSessions: pending}
	app.synchronizer = sessions.NewSynchronizer(app.config.SessionsRoot, t.TempDir(), cache, registry)
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	var invalidBody bytes.Buffer
	invalidWriter := multipart.NewWriter(&invalidBody)
	_ = invalidWriter.WriteField("session", path)
	_ = invalidWriter.WriteField("message", "invalid image")
	invalidHeader := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="images[]"; filename="image.txt"`}, "Content-Type": {"text/plain"}}
	invalidPart, err := invalidWriter.CreatePart(invalidHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = invalidPart.Write([]byte("invalid"))
	_ = invalidWriter.Close()
	invalidRequest := httptest.NewRequest(http.MethodPost, "http://app.test/prompt", &invalidBody)
	invalidRequest.Header.Set("Content-Type", invalidWriter.FormDataContentType())
	invalidResponse := httptest.NewRecorder()
	app.prompt(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid response = %d %s", invalidResponse.Code, invalidResponse.Body.String())
	}
	spools, err := filepath.Glob(filepath.Join(temporary, "multipart-*"))
	if err != nil || len(spools) != 0 {
		t.Fatalf("multipart spool files = %#v, %v", spools, err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("session", path); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("message", "accepted image"); err != nil {
		t.Fatal(err)
	}
	header := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="images[]"; filename="image.png"`}, "Content-Type": {"image/png"}}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("image")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://app.test/prompt", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()

	app.prompt(response, request)
	if response.Code != http.StatusInternalServerError || !client.prompted {
		t.Fatalf("response = %d %s, prompted=%v", response.Code, response.Body.String(), client.prompted)
	}
	images, err := filepath.Glob(filepath.Join(root, sessions.SessionHash(path), "*.png"))
	if err != nil || len(images) != 1 {
		t.Fatalf("persisted images = %#v, %v", images, err)
	}
}

func TestAbortAllowsTrackedSyntheticPendingSession(t *testing.T) {
	path := "/synthetic-pending"
	client := &followUpRaceClient{Client: (*rpc.Client)(nil)}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(path, client); err != nil {
		t.Fatal(err)
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remember(path, "/project")
	app := &application{config: config.Config{SessionsRoot: t.TempDir()}, sessionCache: sessions.NewCache(), rpcClients: registry, pendingSessions: pending}
	request := httptest.NewRequest(http.MethodPost, "http://app.test/abort", strings.NewReader(url.Values{"session": {path}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()

	app.abortSession(response, request)
	if response.Code != http.StatusOK || !client.aborted {
		t.Fatalf("response = %d %s, aborted=%v", response.Code, response.Body.String(), client.aborted)
	}
}

func TestFollowUpFallsBackToOperationLaneWhenCompactionEndsBeforeAtomicQueue(t *testing.T) {
	path := "/pending-follow-up"
	started, release := make(chan struct{}), make(chan struct{})
	client := &followUpRaceClient{Client: (*rpc.Client)(nil)}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(path, client); err != nil {
		t.Fatal(err)
	}
	client.queue = func() {
		go func() {
			_ = registry.WithClient(context.Background(), path, func(rpc.RPCClient) error {
				close(started)
				<-release
				return nil
			})
		}()
		<-started
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remember(path, t.TempDir())
	cache := sessions.NewCache()
	app := &application{config: config.Config{SessionsRoot: t.TempDir(), AttachmentsRoot: t.TempDir()}, sessionCache: cache, rpcClients: registry, pendingSessions: pending}
	app.synchronizer = sessions.NewSynchronizer(app.config.SessionsRoot, t.TempDir(), cache, registry)
	request := httptest.NewRequest(http.MethodPost, "http://app.test/prompt", strings.NewReader(url.Values{"session": {path}, "message": {"after compaction"}, "streaming_behavior": {"follow_up"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()

	app.prompt(response, request)
	close(release)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "session_operation_pending") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if client.followUpCalled {
		t.Fatal("follow-up bypassed the synchronized operation lane")
	}
}

func TestLiveOutputRecognizesNativeIntegerCompletedBashAsPersisted(t *testing.T) {
	recorded := time.UnixMilli(1_750_000_000_500)
	exitCode := 0
	messages := []*sessions.Message{{Role: "bashExecution", Summary: "$ printf done", Text: "done", BashExitCode: &exitCode, BashRecordedAt: recorded}}
	snapshot := rpc.LiveSnapshot{CompletedBashEvents: []map[string]any{{"type": "bash_end", "bashId": "bash-1", "command": "printf done", "startedAt": int64(1_750_000_000_000), "result": map[string]any{"output": "done", "exitCode": int64(0)}}}}
	output := liveOutputFrom(snapshot, messages, "/home/test", nil)
	if output.CompletedBashEventsJSON != "[]" || output.PersistedBashJSON != `["bash-1"]` {
		t.Fatalf("completed=%s persisted=%s", output.CompletedBashEventsJSON, output.PersistedBashJSON)
	}
}

func TestHandlerCloseHonorsContextWhileMaintenanceFinishes(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	maintenance, err := rpc.NewMaintenance(time.Millisecond, func(context.Context) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	maintenance.Start(context.Background())
	<-started
	handler := &Handler{app: &application{rpcClients: rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil), rpcMaintenance: maintenance}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := handler.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error = %v", err)
	}
	close(release)
	if err := handler.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPendingRemapWaitsForPromptAttachmentBoundary(t *testing.T) {
	from, to := "/pending-boundary", "/real-boundary"
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(from, &remapClient{}); err != nil {
		t.Fatal(err)
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remember(from, "/project")
	app := &application{config: config.Config{AttachmentsRoot: t.TempDir()}, rpcClients: registry, pendingSessions: pending}
	boundary := app.imagePromptLock(from)
	boundary.Lock()
	moved := make(chan error, 1)
	go func() {
		moved <- app.movePendingRPCClient(httptest.NewRequest(http.MethodGet, "http://app.test/", nil), from, to)
	}()
	select {
	case err := <-moved:
		t.Fatalf("remap crossed prompt boundary: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	boundary.Unlock()
	if err := <-moved; err != nil {
		t.Fatal(err)
	}
	if remapped, ok := pending.Resolve(from); !ok || remapped != to {
		t.Fatalf("remap = %q, %v", remapped, ok)
	}
}

func TestAttachmentPromptRecordingCannotRacePendingMigration(t *testing.T) {
	root := t.TempDir()
	from, to := "/pending", "/real"
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(from, &remapClient{}); err != nil {
		t.Fatal(err)
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remember(from, "/project")
	app := &application{config: config.Config{AttachmentsRoot: root}, rpcClients: registry, pendingSessions: pending}
	started, record := make(chan struct{}), make(chan struct{})
	recorded := make(chan error, 1)
	go func() {
		recorded <- registry.WithClient(context.Background(), from, func(rpc.RPCClient) error {
			close(started)
			<-record
			return (sessions.AttachmentStore{Root: root}).RecordPrompt(from, "message", 1, time.Now(), []string{"/image"}, []string{"image/png"})
		})
	}()
	<-started
	request := httptest.NewRequest(http.MethodGet, "http://app.test/", nil)
	if err := app.movePendingRPCClient(request, from, to); !errors.Is(err, rpc.ErrOperationPending) {
		t.Fatalf("move during prompt recording = %v", err)
	}
	close(record)
	if err := <-recorded; err != nil {
		t.Fatal(err)
	}
	if err := app.movePendingRPCClient(request, from, to); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(root, sessions.SessionHash(to)+".jsonl"))
	if err != nil || !strings.Contains(string(metadata), sessions.MessageHash("message")) {
		t.Fatalf("migrated metadata = %q, %v", metadata, err)
	}
}

func TestPendingClientMoveRollsBackWhenAttachmentMigrationFails(t *testing.T) {
	root := t.TempDir()
	from, to := "/pending", "/real"
	client := &remapClient{}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(from, client); err != nil {
		t.Fatal(err)
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remember(from, "/project")
	source := filepath.Join(root, sessions.SessionHash(from)+".jsonl")
	if err := os.WriteFile(source, []byte("metadata\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, sessions.SessionHash(to)+".jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	released := ""
	app := &application{config: config.Config{AttachmentsRoot: root}, rpcClients: registry, pendingSessions: pending,
		claimSession:   func(*http.Request, string) error { return nil },
		releaseSession: func(_ *http.Request, path string) error { released = path; return nil },
	}
	if err := app.movePendingRPCClient(httptest.NewRequest(http.MethodGet, "http://app.test/", nil), from, to); err == nil {
		t.Fatal("attachment migration unexpectedly succeeded")
	}
	if !registry.Active(from) || registry.Active(to) {
		t.Fatal("failed migration did not roll back client move")
	}
	if _, ok := pending.CWD(from); !ok {
		t.Fatal("failed migration forgot pending metadata")
	}
	if released != to {
		t.Fatalf("released ownership = %q", released)
	}
}

func writeSessionRecords(t *testing.T, path string, records []map[string]any) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

type followUpRaceClient struct {
	*rpc.Client
	queue          func()
	followUpCalled bool
	aborted        bool
	prompted       bool
}

func (*followUpRaceClient) Close() error { return nil }
func (*followUpRaceClient) GetState(context.Context) (map[string]any, error) {
	return map[string]any{"success": true}, nil
}
func (client *followUpRaceClient) QueueCompactionFollowUp(context.Context, string, []rpc.PromptImage) (map[string]any, bool, error) {
	client.queue()
	return nil, false, nil
}
func (client *followUpRaceClient) FollowUp(context.Context, string, []rpc.PromptImage) (map[string]any, error) {
	client.followUpCalled = true
	return map[string]any{"success": true}, nil
}
func (client *followUpRaceClient) Prompt(context.Context, string, []rpc.PromptImage) (map[string]any, error) {
	client.prompted = true
	return map[string]any{"success": true}, nil
}
func (*followUpRaceClient) ActiveBashCommand() string { return "" }
func (client *followUpRaceClient) Abort(context.Context) (map[string]any, error) {
	client.aborted = true
	return map[string]any{"success": true}, nil
}

type remapClient struct {
	state    map[string]any
	position rpc.SessionEntries
	live     rpc.LiveSnapshot
}

func (client *remapClient) Close() error             { return nil }
func (client *remapClient) Busy() bool               { return false }
func (client *remapClient) BusySince() *time.Time    { return nil }
func (client *remapClient) SettledAt() *time.Time    { return nil }
func (client *remapClient) AgentRunning() bool       { return false }
func (client *remapClient) Compacting() bool         { return false }
func (client *remapClient) EventSequence() int64     { return 0 }
func (client *remapClient) EventReplayCursor() int64 { return 0 }
func (client *remapClient) EventsAfter(int64) rpc.EventBatch {
	return rpc.EventBatch{Events: []map[string]any{}}
}
func (client *remapClient) LiveSnapshot() rpc.LiveSnapshot {
	if client.live.ActiveToolEvents == nil {
		client.live.ActiveToolEvents = []map[string]any{}
	}
	return client.live
}
func (client *remapClient) GetState(context.Context) (map[string]any, error) {
	return client.state, nil
}
func (client *remapClient) GetSessionStats(context.Context) (map[string]any, error) { return nil, nil }
func (client *remapClient) GetCommands(context.Context) (map[string]any, error)     { return nil, nil }
func (client *remapClient) SessionPosition(context.Context, string) (rpc.SessionEntries, error) {
	if !client.position.Known && client.position.Error == "" {
		return rpc.SessionEntries{Known: true}, nil
	}
	return client.position, nil
}
func (client *remapClient) SessionEntriesAfter(context.Context, string) (rpc.SessionEntries, error) {
	return client.SessionPosition(context.Background(), "")
}
