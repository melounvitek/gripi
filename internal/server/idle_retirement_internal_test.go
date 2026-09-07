package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestIdleRetirementPromptWaits(t *testing.T) {
	for _, behavior := range []string{"", "steer", "follow_up"} {
		for _, closeFails := range []bool{false, true} {
			name := behavior
			if name == "" {
				name = "ordinary"
			}
			if closeFails {
				name += "/close_failure"
			} else {
				name += "/replacement"
			}
			t.Run(name, func(t *testing.T) {
				fixture := newIdleRetirementFixture(t)
				closing, releaseClose, blockClose := idleRetirementBarrier()
				defer releaseClose()
				closeErr := errors.New("close failed")
				fixture.old.onClose = func() error {
					blockClose()
					if closeFails {
						return closeErr
					}
					return nil
				}
				var maintenanceErr error
				maintenanceDone := idleRetirementRun(t, func() {
					maintenanceErr = fixture.app.cleanupIdleRPCClients(context.Background())
				})
				idleRetirementWait(t, closing, "old client Close")

				// Register after the sweep selected its candidates, so this session
				// exercises admission independently of the blocked retirement.
				otherPath := fixture.session(t, "other")
				other := &idleRetirementClient{}
				if err := fixture.app.rpcClients.Register(otherPath, other); err != nil {
					t.Fatal(err)
				}
				otherResponse := httptest.NewRecorder()
				otherDone := idleRetirementRun(t, func() {
					fixture.handler.ServeHTTP(otherResponse, idleRetirementRequest(context.Background(), otherPath, ""))
				})
				idleRetirementWait(t, otherDone, "different session prompt during Close")
				idleRetirementAssertSuccess(t, otherResponse, otherPath, "")
				other.assertCalls(t, "", 1)

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				waiting := &idleRetirementWaitContext{Context: ctx, waiting: make(chan struct{})}
				response := httptest.NewRecorder()
				requestDone := idleRetirementRun(t, func() {
					fixture.handler.ServeHTTP(response, idleRetirementRequest(waiting, fixture.path, behavior))
				})
				idleRetirementAwaitAdmission(t, waiting.waiting, requestDone, response)
				fixture.assertUndispatched(t)

				releaseClose()
				idleRetirementWait(t, maintenanceDone, "idle maintenance")
				idleRetirementWait(t, requestDone, "prompt after retirement")
				idleRetirementAssertSuccess(t, response, fixture.path, behavior)
				if closeFails {
					if !errors.Is(maintenanceErr, closeErr) {
						t.Fatalf("maintenance error = %v, want %v", maintenanceErr, closeErr)
					}
					fixture.old.assertCalls(t, behavior, 1)
					fixture.replacement.assertCalls(t, behavior, 0)
					if fixture.created.Load() != 0 || fixture.app.rpcClients.Client(fixture.path) != fixture.old {
						t.Fatal("failed Close did not restore the old client without a factory call")
					}
				} else {
					if maintenanceErr != nil {
						t.Fatal(maintenanceErr)
					}
					fixture.old.assertCalls(t, behavior, 0)
					fixture.replacement.assertCalls(t, behavior, 1)
					if fixture.created.Load() != 1 || fixture.app.rpcClients.Client(fixture.path) != fixture.replacement {
						t.Fatal("prompt did not use exactly one replacement client")
					}
				}
				if fixture.old.closes.Load() != 1 {
					t.Fatalf("old client Close calls = %d, want 1", fixture.old.closes.Load())
				}
			})
		}
	}
}

func TestIdleRetirementPromptCancellation(t *testing.T) {
	for _, behavior := range []string{"", "steer", "follow_up"} {
		name := behavior
		if name == "" {
			name = "ordinary"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newIdleRetirementFixture(t)
			closing, releaseClose, blockClose := idleRetirementBarrier()
			defer releaseClose()
			fixture.old.onClose = func() error { blockClose(); return nil }
			var maintenanceErr error
			maintenanceDone := idleRetirementRun(t, func() {
				maintenanceErr = fixture.app.cleanupIdleRPCClients(context.Background())
			})
			idleRetirementWait(t, closing, "old client Close")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waiting := &idleRetirementWaitContext{Context: ctx, waiting: make(chan struct{})}
			response := httptest.NewRecorder()
			requestDone := idleRetirementRun(t, func() {
				fixture.handler.ServeHTTP(response, idleRetirementRequest(waiting, fixture.path, behavior))
			})
			idleRetirementAwaitAdmission(t, waiting.waiting, requestDone, response)
			cancel()
			idleRetirementWait(t, requestDone, "canceled prompt while Close is still blocked")
			// A recorder defaults to 200 even when cancellation writes nothing.
			// Check the payload and side effects, not that default status.
			if response.Body.Len() != 0 {
				t.Fatalf("canceled prompt wrote a payload: %s", response.Body.String())
			}
			fixture.assertUndispatched(t)

			releaseClose()
			idleRetirementWait(t, maintenanceDone, "idle maintenance after cancellation")
			if maintenanceErr != nil {
				t.Fatal(maintenanceErr)
			}
			fixture.assertUndispatched(t)
			// Cancellation must not leave admission held for later requests.
			response = httptest.NewRecorder()
			nextDone := idleRetirementRun(t, func() {
				fixture.handler.ServeHTTP(response, idleRetirementRequest(context.Background(), fixture.path, behavior))
			})
			idleRetirementWait(t, nextDone, "next prompt after cancellation")
			idleRetirementAssertSuccess(t, response, fixture.path, behavior)
			fixture.replacement.assertCalls(t, behavior, 1)
			if fixture.created.Load() != 1 {
				t.Fatalf("factory calls = %d, want 1", fixture.created.Load())
			}
		})
	}
}

func TestIdleRetirementActivePromptPreventsClose(t *testing.T) {
	for _, behavior := range []string{"", "steer", "follow_up"} {
		name := behavior
		if name == "" {
			name = "ordinary"
		}
		for _, phase := range []string{"dispatch", "response_after_ack"} {
			t.Run(name+"/"+phase, func(t *testing.T) {
				fixture := newIdleRetirementFixture(t)
				entered, release, block := idleRetirementBarrier()
				defer release()
				response := httptest.NewRecorder()
				var writer http.ResponseWriter = response
				if phase == "dispatch" {
					fixture.old.onPrompt = block
				} else {
					// Prompt has acknowledged, but no events have set cached Busy.
					// Registry RPC leases are already released; the handler is not.
					writer = &idleRetirementResponseWriter{ResponseWriter: response, beforeWrite: block}
				}
				requestDone := idleRetirementRun(t, func() {
					fixture.handler.ServeHTTP(writer, idleRetirementRequest(context.Background(), fixture.path, behavior))
				})
				idleRetirementWait(t, entered, phase)
				if fixture.old.Busy() {
					t.Fatal("fixture must remain idle in the cached RPC state")
				}
				var maintenanceErr error
				maintenanceDone := idleRetirementRun(t, func() {
					maintenanceErr = fixture.app.cleanupIdleRPCClients(context.Background())
				})
				idleRetirementWait(t, maintenanceDone, "maintenance during active prompt")
				if maintenanceErr != nil {
					t.Fatal(maintenanceErr)
				}
				if fixture.old.closes.Load() != 0 || fixture.app.rpcClients.Client(fixture.path) != fixture.old {
					t.Fatal("idle maintenance retired a client while its prompt handler was active")
				}
				release()
				idleRetirementWait(t, requestDone, "active prompt completion")
				idleRetirementAssertSuccess(t, response, fixture.path, behavior)
				fixture.old.assertCalls(t, behavior, 1)
				if fixture.created.Load() != 0 {
					t.Fatal("active prompt created a replacement")
				}
				maintenanceDone = idleRetirementRun(t, func() {
					maintenanceErr = fixture.app.cleanupIdleRPCClients(context.Background())
				})
				idleRetirementWait(t, maintenanceDone, "maintenance after handler released admission")
				if maintenanceErr != nil || fixture.old.closes.Load() != 1 || fixture.app.rpcClients.Active(fixture.path) {
					t.Fatalf("retirement after handler: error=%v, closes=%d, active=%v", maintenanceErr, fixture.old.closes.Load(), fixture.app.rpcClients.Active(fixture.path))
				}
			})
		}
	}
}

type idleRetirementFixture struct {
	app              *application
	handler          http.Handler
	path             string
	old, replacement *idleRetirementClient
	created          atomic.Int32
}

func newIdleRetirementFixture(t *testing.T) *idleRetirementFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &idleRetirementFixture{old: &idleRetirementClient{}, replacement: &idleRetirementClient{}}
	// Requests may touch the entry, but it remains expired relative to the
	// real time used by cleanupIdleRPCClients, without sleeps or clock races.
	registry := rpc.NewRegistry(func(path string) (rpc.RPCClient, error) {
		fixture.created.Add(1)
		if path != fixture.path {
			return nil, errors.New("unexpected factory session: " + path)
		}
		return fixture.replacement, nil
	}, func() time.Time { return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC) })
	cache := sessions.NewCache()
	fixture.app = &application{
		config:          config.Config{SessionsRoot: root, Home: root, RPCIdleTimeout: time.Second},
		sessionCache:    cache,
		rpcClients:      registry,
		pendingSessions: rpc.NewPendingSessionRegistry(nil),
		synchronizer:    sessions.NewSynchronizer(root, root, cache, registry),
	}
	fixture.path = fixture.session(t, "idle")
	if err := registry.Register(fixture.path, fixture.old); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fixture.app.registerActionRoutes(mux)
	fixture.handler = mux
	return fixture
}

func (fixture *idleRetirementFixture) session(t *testing.T, name string) string {
	t.Helper()
	root := fixture.app.config.SessionsRoot
	path := filepath.Join(root, name+".jsonl")
	header, err := json.Marshal(map[string]any{"type": "session", "version": 3, "id": name, "cwd": root, "timestamp": "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(header, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (fixture *idleRetirementFixture) assertUndispatched(t *testing.T) {
	t.Helper()
	fixture.old.assertCalls(t, "", 0)
	fixture.replacement.assertCalls(t, "", 0)
	if fixture.created.Load() != 0 || fixture.old.queues.Load() != 0 || fixture.replacement.queues.Load() != 0 {
		t.Fatalf("waiting/canceled request reached RPC: factory=%d, old queues=%d, replacement queues=%d", fixture.created.Load(), fixture.old.queues.Load(), fixture.replacement.queues.Load())
	}
}

type idleRetirementCall struct {
	message, behavior string
	images            int
}

type idleRetirementClient struct {
	remapClient
	rpc.ActionClient // Unexercised actions intentionally have no implementation.
	onClose          func() error
	onPrompt         func()
	closes, queues   atomic.Int32
	mu               sync.Mutex
	calls            []idleRetirementCall
}

var _ rpc.RPCClient = (*idleRetirementClient)(nil)
var _ rpc.ActionClient = (*idleRetirementClient)(nil)

func (client *idleRetirementClient) Close() error {
	client.closes.Add(1)
	if client.onClose != nil {
		return client.onClose()
	}
	return nil
}

func (client *idleRetirementClient) Prompt(_ context.Context, message string, images []rpc.PromptImage) (map[string]any, error) {
	return client.dispatch(message, images, "")
}

func (client *idleRetirementClient) PromptWithBehavior(_ context.Context, message string, images []rpc.PromptImage, behavior string) (map[string]any, error) {
	return client.dispatch(message, images, behavior)
}

func (client *idleRetirementClient) QueueCompactionPrompt(context.Context, string, []rpc.PromptImage, string) (map[string]any, bool, error) {
	client.queues.Add(1)
	return nil, false, nil
}

func (client *idleRetirementClient) dispatch(message string, images []rpc.PromptImage, behavior string) (map[string]any, error) {
	client.mu.Lock()
	client.calls = append(client.calls, idleRetirementCall{message, behavior, len(images)})
	client.mu.Unlock()
	if client.onPrompt != nil {
		client.onPrompt()
	}
	return map[string]any{"success": true}, nil
}

func (client *idleRetirementClient) assertCalls(t *testing.T, behavior string, count int) {
	t.Helper()
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.calls) != count {
		t.Fatalf("prompt calls = %+v, want %d", client.calls, count)
	}
	if behavior == "follow_up" {
		behavior = "followUp"
	}
	for _, call := range client.calls {
		if call != (idleRetirementCall{message: "hello", behavior: behavior}) {
			t.Fatalf("prompt call = %+v, want hello with behavior %q and no images", call, behavior)
		}
	}
}

// Admission checks Err before entering; Done is consulted only when it waits.
// Fake RPC methods deliberately do not consult Done, avoiding false signals.
type idleRetirementWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (ctx *idleRetirementWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func idleRetirementRequest(ctx context.Context, path, behavior string) *http.Request {
	form := url.Values{"session": {path}, "message": {"hello"}, "streaming_behavior": {behavior}}
	request := httptest.NewRequest(http.MethodPost, "/prompt", strings.NewReader(form.Encode())).WithContext(ctx)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	return request
}

func idleRetirementAssertSuccess(t *testing.T, response *httptest.ResponseRecorder, path, behavior string) {
	t.Helper()
	var payload map[string]any
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload["session"] != path || payload["redirect"] == nil || payload["error"] != nil {
		t.Fatalf("prompt response = %d %s", response.Code, response.Body.String())
	}
	if behavior != "" && payload[behavior] != true {
		t.Fatalf("prompt response missing %s: %s", behavior, response.Body.String())
	}
}

func idleRetirementBarrier() (<-chan struct{}, func(), func()) {
	entered, release := make(chan struct{}), make(chan struct{})
	return entered, sync.OnceFunc(func() { close(release) }), func() {
		close(entered)
		<-release
	}
}

func idleRetirementRun(t *testing.T, call func()) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		call()
	}()
	t.Cleanup(func() { idleRetirementWait(t, done, "goroutine cleanup") })
	return done
}

func idleRetirementWait(t *testing.T, done <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func idleRetirementAwaitAdmission(t *testing.T, waiting, done <-chan struct{}, response *httptest.ResponseRecorder) {
	t.Helper()
	select {
	case <-waiting:
	case <-done:
		t.Fatalf("prompt returned before waiting for retirement: %d %s", response.Code, response.Body.String())
	case <-time.After(3 * time.Second):
		t.Fatal("prompt neither waited for retirement nor returned")
	}
}

type idleRetirementResponseWriter struct {
	http.ResponseWriter
	beforeWrite func()
}

func (writer *idleRetirementResponseWriter) Write(data []byte) (int, error) {
	writer.beforeWrite()
	return writer.ResponseWriter.Write(data)
}
