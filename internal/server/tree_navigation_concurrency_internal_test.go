package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestTreeNavigationExcludesPrompts(t *testing.T) {
	for _, test := range []struct {
		phase     string
		streaming bool
	}{
		{"preflight", false}, {"navigate", false}, {"response", false},
		{"preflight", true}, {"abort", true}, {"close", true},
		{"factory", true}, {"navigate", true}, {"response", true},
	} {
		name := "idle/"
		if test.streaming {
			name = "streaming/"
		}
		t.Run(name+test.phase, func(t *testing.T) {
			fixture := newTreeNavigationFixture(t)
			fixture.a.state = treeNavigationState(test.streaming)
			if test.streaming {
				fixture.a.live.QueuedMessages = map[string][]string{"steering": {"steer one", "steer two"}, "followUp": {"follow up"}}
			}
			entered, release, block := idleRetirementBarrier()
			defer release()
			// A broken admission gate may let another request reach this method.
			blockOnce := sync.OnceFunc(block)
			response := httptest.NewRecorder()
			var writer http.ResponseWriter = response
			switch test.phase {
			case "preflight":
				fixture.a.onState = func(context.Context) error { blockOnce(); return nil }
			case "abort":
				fixture.a.onAbort = func(context.Context) error { blockOnce(); return nil }
			case "close":
				fixture.old.onClose = func() error { blockOnce(); return nil }
			case "factory":
				fixture.onCreate = func() error { blockOnce(); return nil }
			case "navigate":
				client := fixture.a
				if test.streaming {
					client = fixture.b
				}
				client.onNavigate = func(context.Context) error { blockOnce(); return nil }
			case "response":
				writer = &idleRetirementResponseWriter{ResponseWriter: response, beforeWrite: blockOnce}
			}
			// Resolve aliases before navigation, not only at the RPC dispatch lane.
			alias := filepath.Join(fixture.app.config.SessionsRoot, "pending-alias.jsonl")
			fixture.app.pendingSessions.Remap(alias, fixture.path)
			done := idleRetirementRun(t, func() {
				fixture.handler.ServeHTTP(writer, treeNavigationRequest(context.Background(), alias))
			})
			idleRetirementWait(t, entered, "navigation "+test.phase)
			if fixture.a.Busy() || fixture.b.Busy() {
				t.Fatal("cached busy must remain false, including after native abort")
			}

			for _, behavior := range []string{"", "steer", "follow_up", "bash"} {
				path := fixture.path
				if behavior == "follow_up" {
					path = alias
				}
				promptResponse := treeNavigationServe(t, fixture.handler, treeNavigationPromptRequest(context.Background(), path, behavior))
				treeNavigationAssertConflict(t, promptResponse)
				var payload map[string]any
				if err := json.Unmarshal(promptResponse.Body.Bytes(), &payload); err != nil || payload["retryable"] != false {
					t.Errorf("navigation conflict permits automatic resubmission: %s", promptResponse.Body.String())
				}
			}
			fixture.assertNoPrompts(t)
			created := int32(0)
			if test.streaming && (test.phase == "factory" || test.phase == "navigate" || test.phase == "response") {
				created = 1
			}
			if fixture.created.Load() != created {
				t.Errorf("factory calls during navigation = %d, want %d", fixture.created.Load(), created)
			}

			// The unrelated session must not wait behind a global navigation lock.
			otherPath := fixture.session(t, "other")
			other := &idleRetirementClient{}
			if err := fixture.app.rpcClients.Register(otherPath, other); err != nil {
				t.Fatal(err)
			}
			otherResponse := treeNavigationServe(t, fixture.handler, idleRetirementRequest(context.Background(), otherPath, ""))
			idleRetirementAssertSuccess(t, otherResponse, otherPath, "")
			other.assertCalls(t, "", 1)

			oldCloses, replacementCloses := fixture.old.closes.Load(), fixture.replacement.closes.Load()
			var maintenanceErr error
			maintenanceDone := idleRetirementRun(t, func() {
				maintenanceErr = fixture.app.cleanupIdleRPCClients(context.Background())
			})
			idleRetirementWait(t, maintenanceDone, "maintenance during navigation")
			if maintenanceErr != nil || fixture.old.closes.Load() != oldCloses || fixture.replacement.closes.Load() != replacementCloses {
				t.Errorf("maintenance retired a navigation client: error=%v, old closes=%d -> %d, replacement closes=%d -> %d", maintenanceErr, oldCloses, fixture.old.closes.Load(), replacementCloses, fixture.replacement.closes.Load())
			}
			release()
			idleRetirementWait(t, done, "navigation completion")
			editor := "branch draft"
			wantEvents := []string{"Navigate(A)"}
			if test.streaming {
				editor = "steer one\n\nsteer two\n\nfollow up"
				wantEvents = []string{"Abort(A)", "Close(A)", "Create(B)", "Navigate(B)"}
			}
			treeNavigationAssertSuccess(t, response, fixture.path, editor)
			fixture.assertEvents(t, wantEvents)
			prompt := treeNavigationServe(t, fixture.handler, idleRetirementRequest(context.Background(), alias, ""))
			idleRetirementAssertSuccess(t, prompt, fixture.path, "")
		})
	}
}

func TestTreeNavigationRejectsOverlappingNavigation(t *testing.T) {
	fixture := newTreeNavigationFixture(t)
	entered, release, block := idleRetirementBarrier()
	defer release()
	response := httptest.NewRecorder()
	done := idleRetirementRun(t, func() {
		fixture.handler.ServeHTTP(&idleRetirementResponseWriter{ResponseWriter: response, beforeWrite: block}, treeNavigationRequest(context.Background(), fixture.path))
	})
	idleRetirementWait(t, entered, "navigation response")
	competing := treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), fixture.path))
	treeNavigationAssertConflict(t, competing)
	if fixture.a.getStateCalls.Load() != 1 {
		t.Errorf("overlapping navigation reached preflight: GetState calls=%d, want 1", fixture.a.getStateCalls.Load())
	}
	fixture.assertEvents(t, []string{"Navigate(A)"})
	release()
	idleRetirementWait(t, done, "first navigation completion")
	treeNavigationAssertSuccess(t, response, fixture.path, "branch draft")
	competing = treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), fixture.path))
	treeNavigationAssertSuccess(t, competing, fixture.path, "branch draft")
}

func TestTreeNavigationRejectsActivePromptAfterAck(t *testing.T) {
	for _, behavior := range []string{"", "steer", "follow_up", "bash"} {
		name := behavior
		if name == "" {
			name = "ordinary"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newTreeNavigationFixture(t)
			entered, release, block := idleRetirementBarrier()
			defer release()
			response := httptest.NewRecorder()
			done := idleRetirementRun(t, func() {
				fixture.handler.ServeHTTP(&idleRetirementResponseWriter{ResponseWriter: response, beforeWrite: block}, treeNavigationPromptRequest(context.Background(), fixture.path, behavior))
			})
			idleRetirementWait(t, entered, "prompt response after acknowledgement")
			if fixture.a.Busy() {
				t.Fatal("prompt acknowledgement must not set cached busy")
			}
			// No RPC lease or synchronizer lock is held now, only the handler's admission.
			navigation := treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), fixture.path))
			treeNavigationAssertConflict(t, navigation)
			if fixture.a.getStateCalls.Load() != 0 {
				t.Errorf("conflicting navigation queried native state %d times", fixture.a.getStateCalls.Load())
			}
			fixture.assertEvents(t, nil)
			release()
			idleRetirementWait(t, done, "prompt completion")
			if response.Code != http.StatusOK {
				t.Fatalf("prompt response = %d %s", response.Code, response.Body.String())
			}
			navigation = treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), fixture.path))
			treeNavigationAssertSuccess(t, navigation, fixture.path, "branch draft")
		})
	}
}

func TestTreeNavigationCompactionQueueAllowsConcurrentPrompts(t *testing.T) {
	fixture := newTreeNavigationFixture(t)
	fixture.a.compacting = true
	fixture.a.queued = true
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	fixture.a.onQueue = func() { entered <- struct{}{}; <-release }
	var responses []*httptest.ResponseRecorder
	var done []<-chan struct{}
	for range 2 {
		response := httptest.NewRecorder()
		responses = append(responses, response)
		done = append(done, idleRetirementRun(t, func() {
			fixture.handler.ServeHTTP(response, idleRetirementRequest(context.Background(), fixture.path, "follow_up"))
		}))
	}
	// Both queue calls must enter before either is released. Serializing prompts
	// with each other would break native compaction follow-up admission.
	idleRetirementWait(t, entered, "first compaction queue call")
	idleRetirementWait(t, entered, "second compaction queue call")
	navigation := treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), fixture.path))
	treeNavigationAssertConflict(t, navigation)
	fixture.assertEvents(t, nil)
	if fixture.a.getStateCalls.Load() != 0 {
		t.Error("navigation reached preflight during compaction prompt admission")
	}
	unblock()
	for index, finished := range done {
		idleRetirementWait(t, finished, "compaction follow-up response")
		idleRetirementAssertSuccess(t, responses[index], fixture.path, "follow_up")
		var payload map[string]any
		if err := json.Unmarshal(responses[index].Body.Bytes(), &payload); err != nil || payload["queued_after_compaction"] != true {
			t.Errorf("queue response = %s, error=%v", responses[index].Body.String(), err)
		}
	}
	fixture.old.assertCalls(t, "", 0)
	fixture.replacement.assertCalls(t, "", 0)
	if fixture.old.queues.Load() != 2 || fixture.replacement.queues.Load() != 0 || fixture.created.Load() != 0 {
		t.Fatalf("queue calls: old=%d replacement=%d factory=%d", fixture.old.queues.Load(), fixture.replacement.queues.Load(), fixture.created.Load())
	}
	fixture.a.compacting = false
	navigation = treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), fixture.path))
	treeNavigationAssertSuccess(t, navigation, fixture.path, "branch draft")
}

func TestTreeNavigationReleasesAdmissionOnFailure(t *testing.T) {
	for _, phase := range []string{"preflight", "abort", "close", "factory", "navigate", "cancel_preflight", "cancel_navigate", "native_cancelled"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newTreeNavigationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release, block := idleRetirementBarrier()
			defer release()
			failure := func(ctx context.Context) error {
				block()
				if strings.HasPrefix(phase, "cancel_") {
					return ctx.Err()
				}
				return errors.New("navigation failed")
			}
			switch phase {
			case "preflight", "cancel_preflight":
				fixture.a.onState = failure
			case "abort":
				fixture.a.state = treeNavigationState(true)
				fixture.a.onAbort = failure
			case "close":
				fixture.a.state = treeNavigationState(true)
				fixture.old.onClose = func() error { return failure(ctx) }
			case "factory":
				fixture.a.state = treeNavigationState(true)
				fixture.onCreate = func() error { return failure(ctx) }
			case "navigate", "cancel_navigate":
				fixture.a.onNavigate = failure
			case "native_cancelled":
				fixture.a.cancelled = true
				fixture.a.onNavigate = func(context.Context) error { block(); return nil }
			}
			response := httptest.NewRecorder()
			done := idleRetirementRun(t, func() {
				fixture.handler.ServeHTTP(response, treeNavigationRequest(ctx, fixture.path))
			})
			idleRetirementWait(t, entered, "navigation failure boundary")
			if strings.HasPrefix(phase, "cancel_") {
				cancel()
			}
			release()
			idleRetirementWait(t, done, "failed navigation response")
			switch {
			case strings.HasPrefix(phase, "cancel_"):
				if response.Body.Len() != 0 {
					t.Errorf("canceled navigation wrote %s", response.Body.String())
				}
			case phase == "native_cancelled":
				var payload map[string]any
				if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload["cancelled"] != true {
					t.Errorf("native cancellation response = %d %s", response.Code, response.Body.String())
				}
			default:
				if response.Code != http.StatusInternalServerError {
					t.Errorf("navigation failure response = %d %s", response.Code, response.Body.String())
				}
			}
			fixture.a.onState, fixture.a.onAbort, fixture.a.onNavigate = nil, nil, nil
			fixture.old.onClose, fixture.onCreate = nil, nil
			fixture.a.state = treeNavigationState(false)
			fixture.a.cancelled = false
			// Exercise both admission modes after every exit, not internal lock state.
			navigation := treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), fixture.path))
			treeNavigationAssertSuccess(t, navigation, fixture.path, "branch draft")
			prompt := treeNavigationServe(t, fixture.handler, idleRetirementRequest(context.Background(), fixture.path, ""))
			idleRetirementAssertSuccess(t, prompt, fixture.path, "")
		})
	}
}

// Keep wrapper behavior local: the idle fixture's embedded ActionClient is nil.
type treeNavigationClient struct {
	*idleRetirementClient
	name                         string
	record                       func(string)
	onState, onAbort, onNavigate func(context.Context) error
	onQueue                      func()
	queued, cancelled            bool
	bashes                       atomic.Int32
}

func (client *treeNavigationClient) GetState(ctx context.Context) (map[string]any, error) {
	client.getStateCalls.Add(1)
	if client.onState != nil {
		if err := client.onState(ctx); err != nil {
			return nil, err
		}
	}
	return client.state, nil
}

func (client *treeNavigationClient) Abort(ctx context.Context) (map[string]any, error) {
	client.record("Abort(" + client.name + ")")
	if client.onAbort != nil {
		if err := client.onAbort(ctx); err != nil {
			return nil, err
		}
	}
	return map[string]any{"success": true}, nil
}

func (client *treeNavigationClient) Close() error {
	client.record("Close(" + client.name + ")")
	return client.idleRetirementClient.Close()
}

func (client *treeNavigationClient) NavigateTree(ctx context.Context, entry, summary, instructions string) (map[string]any, error) {
	client.record("Navigate(" + client.name + ")")
	if entry != "target" || summary != "none" || instructions != "" {
		return nil, errors.New("unexpected navigation arguments")
	}
	if client.onNavigate != nil {
		if err := client.onNavigate(ctx); err != nil {
			return nil, err
		}
	}
	return map[string]any{"success": true, "data": map[string]any{"editorText": "branch draft", "cancelled": client.cancelled}}, nil
}

func (client *treeNavigationClient) QueueCompactionPrompt(_ context.Context, message string, images []rpc.PromptImage, behavior string) (map[string]any, bool, error) {
	client.queues.Add(1)
	if message != "hello" || len(images) != 0 || behavior != "steer" && behavior != "followUp" {
		return nil, false, errors.New("unexpected compaction prompt arguments")
	}
	if client.onQueue != nil {
		client.onQueue()
	}
	return map[string]any{"success": true, "compacting": client.queued}, client.queued, nil
}

func (client *treeNavigationClient) Bash(_ context.Context, command string, excluded bool) (map[string]any, error) {
	client.bashes.Add(1)
	if command != "echo hello" || excluded {
		return nil, errors.New("unexpected bash arguments")
	}
	return map[string]any{"success": true, "id": "bash-test"}, nil
}

type treeNavigationFixture struct {
	*idleRetirementFixture
	a, b     *treeNavigationClient
	onCreate func() error
	mu       sync.Mutex
	events   []string
}

func newTreeNavigationFixture(t *testing.T) *treeNavigationFixture {
	t.Helper()
	fixture := &treeNavigationFixture{idleRetirementFixture: newIdleRetirementFixture(t)}
	record := func(event string) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.events = append(fixture.events, event)
	}
	fixture.a = &treeNavigationClient{idleRetirementClient: fixture.old, name: "A", record: record}
	fixture.b = &treeNavigationClient{idleRetirementClient: fixture.replacement, name: "B", record: record}
	fixture.a.state, fixture.b.state = treeNavigationState(false), treeNavigationState(false)
	// LiveSnapshot initializes this slice lazily; make concurrent reads read-only.
	fixture.a.live.ActiveToolEvents, fixture.b.live.ActiveToolEvents = []map[string]any{}, []map[string]any{}
	registry := rpc.NewRegistry(func(path string) (rpc.RPCClient, error) {
		fixture.created.Add(1)
		record("Create(B)")
		if path != fixture.path {
			return nil, errors.New("unexpected factory session: " + path)
		}
		if fixture.onCreate != nil {
			if err := fixture.onCreate(); err != nil {
				return nil, err
			}
		}
		return fixture.b, nil
	}, func() time.Time { return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC) })
	if err := registry.Register(fixture.path, fixture.a); err != nil {
		t.Fatal(err)
	}
	fixture.app.rpcClients = registry
	fixture.app.synchronizer = sessions.NewSynchronizer(fixture.app.config.SessionsRoot, fixture.app.config.Home, fixture.app.sessionCache, registry)
	return fixture
}

func (fixture *treeNavigationFixture) assertNoPrompts(t *testing.T) {
	t.Helper()
	for _, client := range []*treeNavigationClient{fixture.a, fixture.b} {
		client.mu.Lock()
		calls := len(client.calls)
		client.mu.Unlock()
		if calls != 0 || client.queues.Load() != 0 || client.bashes.Load() != 0 {
			t.Errorf("client %s received prompts during navigation: dispatch=%d queue=%d bash=%d", client.name, calls, client.queues.Load(), client.bashes.Load())
		}
	}
}

func (fixture *treeNavigationFixture) assertEvents(t *testing.T, want []string) {
	t.Helper()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !reflect.DeepEqual(fixture.events, want) {
		t.Errorf("lifecycle events = %v, want %v", fixture.events, want)
	}
}

func treeNavigationState(streaming bool) map[string]any {
	return map[string]any{"success": true, "data": map[string]any{"isStreaming": streaming, "isCompacting": false}}
}

func treeNavigationRequest(ctx context.Context, path string) *http.Request {
	form := url.Values{"session": {path}, "entry_id": {"target"}}
	request := httptest.NewRequest(http.MethodPost, "/sessions/tree", strings.NewReader(form.Encode())).WithContext(ctx)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	return request
}

func treeNavigationPromptRequest(ctx context.Context, path, behavior string) *http.Request {
	request := idleRetirementRequest(ctx, path, behavior)
	if behavior == "bash" {
		form := url.Values{"session": {path}, "message": {"!echo hello"}}
		request = httptest.NewRequest(http.MethodPost, "/prompt", strings.NewReader(form.Encode())).WithContext(ctx)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Accept", "application/json")
	}
	return request
}

func treeNavigationServe(t *testing.T, handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	done := idleRetirementRun(t, func() { handler.ServeHTTP(response, request) })
	idleRetirementWait(t, done, request.URL.Path+" response without releasing the competing handler")
	return response
}

func treeNavigationAssertConflict(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var payload map[string]any
	if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload["code"] != "session_operation_pending" {
		t.Errorf("conflicting request = %d %s, want 409 session_operation_pending", response.Code, response.Body.String())
	}
}

func treeNavigationAssertSuccess(t *testing.T, response *httptest.ResponseRecorder, path, editor string) {
	t.Helper()
	var payload map[string]any
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload["session"] != path || payload["editorText"] != editor || payload["cancelled"] != false {
		t.Errorf("navigation response = %d %s, want session=%s editorText=%q", response.Code, response.Body.String(), path, editor)
	}
}
