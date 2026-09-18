package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestClearQueueUsesOnlyExistingClientAndRecordedAliases(t *testing.T) {
	for _, kind := range []string{"persisted", "pending", "alias_chain"} {
		t.Run(kind, func(t *testing.T) {
			f := newClearQueueFixture(t)
			requested := f.path
			switch kind {
			case "pending":
				pending := filepath.Join(f.app.config.SessionsRoot, "pending.jsonl")
				if err := f.app.rpcClients.Move(f.path, pending); err != nil {
					t.Fatal(err)
				}
				f.app.pendingSessions.Remember(pending, f.app.config.SessionsRoot)
				f.path, requested = pending, pending
			case "alias_chain":
				requested = f.path
				for _, name := range []string{"middle", "final"} {
					next := f.session(t, name)
					if err := f.app.remapPendingRPCClient(f.path, next, pendingAdmissionClaim); err != nil {
						t.Fatal(err)
					}
					f.path = next
				}
			}
			response := f.serve(context.Background(), requested)
			payload := clearQueuePayload(t, response, http.StatusOK)
			if !reflect.DeepEqual(payload, map[string]any{"ok": true, "session": f.path}) {
				t.Fatalf("success payload = %#v", payload)
			}
			f.assertUntouched(t, 1)
		})
	}
}

func TestClearQueueRejectsInvalidUnownedAndUnavailableSessions(t *testing.T) {
	for _, kind := range []string{"empty", "relative", "unclean", "unknown", "unowned", "unowned_alias", "no_client", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			f := newClearQueueFixture(t)
			requested, status := f.path, http.StatusNotFound
			switch kind {
			case "empty":
				requested = ""
			case "relative":
				requested = "session.jsonl"
			case "unclean":
				requested = filepath.Dir(f.path) + "/../session.jsonl"
			case "unknown":
				requested = filepath.Join(f.app.config.SessionsRoot, "unknown.jsonl")
			case "unowned":
				f.app.ownsSession = func(*http.Request, string) bool { return false }
			case "unowned_alias":
				requested = filepath.Join(f.app.config.SessionsRoot, "alias.jsonl")
				if err := f.app.remapPendingRPCClient(requested, f.path, pendingAdmissionClaim); err != nil {
					t.Fatal(err)
				}
				f.app.ownsSession = func(_ *http.Request, path string) bool { return path == requested }
			case "no_client":
				requested = f.session(t, "inactive")
				status = http.StatusConflict
			case "unsupported":
				requested = f.session(t, "unsupported")
				if err := f.app.rpcClients.Register(requested, &remapClient{}); err != nil {
					t.Fatal(err)
				}
				status = http.StatusNotImplemented
			}
			clearQueuePayload(t, f.serve(context.Background(), requested), status)
			f.assertUntouched(t, 0)
		})
	}
}

func TestClearQueueSurfacesFailuresWithoutRetiringOrResettingQueues(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  map[string]any
		err     error
		status  int
		message string
	}{
		{"native", map[string]any{"success": false, "error": "native clear rejected"}, nil, 422, "native clear rejected"},
		{"invalid_response", nil, nil, 422, ""},
		{"timeout", nil, &rpc.RequestTimeoutError{Command: "clear_queue", Accepted: true}, 504, "clear_queue"},
		{"not_accepted_timeout", nil, &rpc.RequestTimeoutError{Command: "clear_queue"}, 504, "clear_queue"},
		{"disconnected", nil, io.ErrClosedPipe, 502, ""},
		{"other", nil, errors.New("native transport failed"), 502, "native transport failed"},
		{"canceled", nil, context.Canceled, 408, ""},
		{"deadline", nil, context.DeadlineExceeded, 504, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClearQueueFixture(t)
			f.client.result, f.client.err = tc.result, tc.err
			payload := clearQueuePayload(t, f.serve(context.Background(), f.path), tc.status)
			if !strings.Contains(payload["error"].(string), tc.message) {
				t.Fatalf("error = %v, want %q", payload["error"], tc.message)
			}
			f.assertUntouched(t, 1)
			// A failed clear must not poison subsequent admission or replace the client.
			f.client.result, f.client.err = map[string]any{"success": true}, nil
			clearQueuePayload(t, f.serve(context.Background(), f.path), http.StatusOK)
			f.assertUntouched(t, 2)
		})
	}
}

func TestClearQueueInterruptsCompactionWithoutTakingOperationOrSyncLane(t *testing.T) {
	f := newClearQueueFixture(t)
	f.client.compacting, f.client.busy = true, true
	entered, release, block := idleRetirementBarrier()
	defer release()
	var operationErr error
	done := idleRetirementRun(t, func() {
		operationErr = f.app.synchronizer.WithMutableClient(context.Background(), f.path, func(rpc.RPCClient) error {
			block()
			return nil
		})
	})
	idleRetirementWait(t, entered, "compaction operation")
	response := httptest.NewRecorder()
	cleared := idleRetirementRun(t, func() { f.handler.ServeHTTP(response, clearQueueRequest(context.Background(), f.path)) })
	idleRetirementWait(t, cleared, "clear during compaction")
	clearQueuePayload(t, response, http.StatusOK)
	f.assertUntouched(t, 1)
	release()
	idleRetirementWait(t, done, "compaction release")
	if operationErr != nil {
		t.Fatal(operationErr)
	}
}

func TestClearQueueHoldsAdmissionThroughResponseAndSerializesInterrupts(t *testing.T) {
	f := newClearQueueFixture(t)
	entered, release, block := idleRetirementBarrier()
	defer release()
	f.client.onClear = block
	responseEntered, releaseResponse, blockResponse := idleRetirementBarrier()
	defer releaseResponse()
	response := httptest.NewRecorder()
	done := idleRetirementRun(t, func() {
		f.handler.ServeHTTP(&idleRetirementResponseWriter{ResponseWriter: response, beforeWrite: blockResponse}, clearQueueRequest(context.Background(), f.path))
	})
	idleRetirementWait(t, entered, "native clear")
	clearQueuePayload(t, f.serve(context.Background(), f.path), http.StatusConflict)
	for _, phase := range []string{"native", "response"} {
		if phase == "response" {
			release()
			idleRetirementWait(t, responseEntered, "clear response")
		}
		_, finish, err := f.app.promptAdmissions.navigate(func() (string, error) { return f.path, nil })
		if err == nil {
			finish()
			t.Fatalf("navigation admitted during %s", phase)
		}
		if err := f.app.cleanupIdleRPCClients(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.assertUntouched(t, 1)
	}
	releaseResponse()
	idleRetirementWait(t, done, "clear response completion")
	clearQueuePayload(t, response, http.StatusOK)
	_, finish, err := f.app.promptAdmissions.navigate(func() (string, error) { return f.path, nil })
	if err != nil {
		t.Fatal(err)
	}
	finish()
}

func TestClearQueueRejectsNavigationAndWaitsForRetirementWithoutStartingClient(t *testing.T) {
	t.Run("navigation", func(t *testing.T) {
		f := newClearQueueFixture(t)
		_, finish, err := f.app.promptAdmissions.navigate(func() (string, error) { return f.path, nil })
		if err != nil {
			t.Fatal(err)
		}
		defer finish()
		clearQueuePayload(t, f.serve(context.Background(), f.path), http.StatusConflict)
		f.assertUntouched(t, 0)
	})
	t.Run("retirement", func(t *testing.T) {
		f := newClearQueueFixture(t)
		entered, release, block := idleRetirementBarrier()
		defer release()
		f.client.onClose = block
		var retirementErr error
		retired := idleRetirementRun(t, func() { retirementErr = f.app.cleanupIdleRPCClients(context.Background()) })
		idleRetirementWait(t, entered, "retirement close")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		waiting := &idleRetirementWaitContext{Context: ctx, waiting: make(chan struct{})}
		response := httptest.NewRecorder()
		done := idleRetirementRun(t, func() { f.handler.ServeHTTP(response, clearQueueRequest(waiting, f.path)) })
		idleRetirementAwaitAdmission(t, waiting.waiting, done, response)
		release()
		idleRetirementWait(t, retired, "retirement completion")
		idleRetirementWait(t, done, "clear after retirement")
		if retirementErr != nil {
			t.Fatal(retirementErr)
		}
		clearQueuePayload(t, response, http.StatusConflict)
		if f.created.Load() != 0 || f.client.clears.Load() != 0 || f.client.closes.Load() != 1 {
			t.Fatal("clear started, replaced, or closed a client after retirement")
		}
	})
}

func TestClearQueueFormAndCancellationErrorsAreJSON(t *testing.T) {
	for _, kind := range []string{"multipart", "malformed", "too_large", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			f := newClearQueueFixture(t)
			request := clearQueueRequest(context.Background(), f.path)
			status, calls := http.StatusOK, int32(1)
			switch kind {
			case "multipart":
				var body bytes.Buffer
				form := multipart.NewWriter(&body)
				if err := form.WriteField("session", f.path); err != nil {
					t.Fatal(err)
				}
				if err := form.Close(); err != nil {
					t.Fatal(err)
				}
				request = httptest.NewRequest(http.MethodPost, "/clear_queue", &body)
				request.Header.Set("Content-Type", form.FormDataContentType())
			case "malformed":
				request.Body = io.NopCloser(strings.NewReader("session=%zz"))
				status, calls = http.StatusBadRequest, 0
			case "too_large":
				request.Body = http.MaxBytesReader(httptest.NewRecorder(), request.Body, 1)
				status, calls = http.StatusRequestEntityTooLarge, 0
			case "canceled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				request = request.WithContext(ctx)
				status, calls = http.StatusRequestTimeout, 0
			}
			response := httptest.NewRecorder()
			f.handler.ServeHTTP(response, request)
			clearQueuePayload(t, response, status)
			f.assertUntouched(t, calls)
		})
	}
}

func TestClearQueueRejectsRegistryRetirementWithoutWaitingOrDispatching(t *testing.T) {
	f := newClearQueueFixture(t)
	entered, release, block := idleRetirementBarrier()
	defer release()
	f.client.onClose = block
	var closeErr error
	done := idleRetirementRun(t, func() { _, closeErr = f.app.rpcClients.CloseClientIfIdle(f.path) })
	idleRetirementWait(t, entered, "registry retirement")
	response := httptest.NewRecorder()
	cleared := idleRetirementRun(t, func() { f.handler.ServeHTTP(response, clearQueueRequest(context.Background(), f.path)) })
	idleRetirementWait(t, cleared, "clear rejection during registry retirement")
	clearQueuePayload(t, response, http.StatusServiceUnavailable)
	if f.created.Load() != 0 || f.client.clears.Load() != 0 || f.client.closes.Load() != 1 {
		t.Fatal("clear dispatched or changed the retiring client")
	}
	release()
	idleRetirementWait(t, done, "registry retirement completion")
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestClearQueueFollowsAliasCommittedBeforeAdmission(t *testing.T) {
	f := newClearQueueFixture(t)
	requested := f.path
	entered, release, block := idleRetirementBarrier()
	defer release()
	ctx := &pendingAdmissionContext{Context: context.Background(), beforeErr: block}
	response := httptest.NewRecorder()
	done := idleRetirementRun(t, func() { f.handler.ServeHTTP(response, clearQueueRequest(ctx, requested)) })
	idleRetirementWait(t, entered, "clear before admission")
	next := f.session(t, "remapped")
	if err := f.app.remapPendingRPCClient(requested, next, pendingAdmissionClaim); err != nil {
		t.Fatal(err)
	}
	f.path = next
	release()
	idleRetirementWait(t, done, "clear after remap")
	payload := clearQueuePayload(t, response, http.StatusOK)
	if payload["session"] != next {
		t.Fatalf("session = %v, want %s", payload["session"], next)
	}
	f.assertUntouched(t, 1)
}

func TestClearQueueRespectsKnownSynchronizationBlock(t *testing.T) {
	for _, mode := range []sessions.SyncMode{sessions.SyncConflict, sessions.SyncExternalFollow} {
		t.Run(string(mode), func(t *testing.T) {
			f := newClearQueueFixture(t)
			// Establish external changes without a client, then register one.
			if _, err := f.app.rpcClients.CloseClientIfIdle(f.path); err != nil {
				t.Fatal(err)
			}
			if _, err := f.app.synchronizer.Inspect(context.Background(), f.path, false); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if mode == sessions.SyncConflict {
				_, err = file.WriteString(`{"type":`)
			} else {
				err = json.NewEncoder(file).Encode(map[string]any{"type": "message", "id": "external", "parentId": nil, "message": map[string]any{"role": "user", "content": "external"}})
			}
			_ = file.Close()
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.app.synchronizer.Inspect(context.Background(), f.path, false)
			if err != nil || result.Mode != mode {
				t.Fatalf("inspect = %+v, %v", result, err)
			}
			f.client.closes.Store(0)
			if err := f.app.rpcClients.Register(f.path, f.client); err != nil {
				t.Fatal(err)
			}
			payload := clearQueuePayload(t, f.serve(context.Background(), f.path), http.StatusConflict)
			if payload["session_sync_mode"] != string(mode) {
				t.Fatalf("payload = %#v", payload)
			}
			f.assertUntouched(t, 0)
		})
	}
}

type clearQueueFixture struct {
	*idleRetirementFixture
	client *clearQueueClient
}

func newClearQueueFixture(t *testing.T) *clearQueueFixture {
	t.Helper()
	f := &clearQueueFixture{idleRetirementFixture: newIdleRetirementFixture(t), client: &clearQueueClient{result: map[string]any{"success": true, "data": map[string]any{"editorText": "must not restore"}}}}
	f.client.live = rpc.LiveSnapshot{QueuedMessages: map[string][]string{"steering": {"steer"}, "followUp": {"follow"}}}
	if err := f.app.rpcClients.Register(f.path, f.client); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *clearQueueFixture) serve(ctx context.Context, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, clearQueueRequest(ctx, path))
	return response
}

func (f *clearQueueFixture) assertUntouched(t *testing.T, calls int32) {
	t.Helper()
	if f.client.clears.Load() != calls || f.client.closes.Load() != 0 || f.client.aborts.Load() != 0 || f.created.Load() != 0 || f.client.getStateCalls.Load() != 0 || f.app.rpcClients.Client(f.path) != f.client {
		t.Fatalf("clear=%d close=%d abort=%d create=%d state=%d client=%T", f.client.clears.Load(), f.client.closes.Load(), f.client.aborts.Load(), f.created.Load(), f.client.getStateCalls.Load(), f.app.rpcClients.Client(f.path))
	}
	if !reflect.DeepEqual(f.client.live.QueuedMessages, map[string][]string{"steering": {"steer"}, "followUp": {"follow"}}) {
		t.Fatal("route optimistically changed the queue snapshot")
	}
}

type clearQueueClient struct {
	remapClient
	clears, closes, aborts atomic.Int32
	onClear, onClose       func()
	result                 map[string]any
	err                    error
}

func (client *clearQueueClient) ClearQueue(context.Context) (map[string]any, error) {
	client.clears.Add(1)
	if client.onClear != nil {
		client.onClear()
	}
	return client.result, client.err
}
func (client *clearQueueClient) Close() error {
	client.closes.Add(1)
	if client.onClose != nil {
		client.onClose()
	}
	return nil
}
func (client *clearQueueClient) Abort(context.Context) (map[string]any, error) {
	client.aborts.Add(1)
	return map[string]any{"success": true}, nil
}
func (client *clearQueueClient) AbortBash(ctx context.Context) (map[string]any, error) {
	return client.Abort(ctx)
}

func clearQueueRequest(ctx context.Context, path string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/clear_queue", strings.NewReader(url.Values{"session": {path}}.Encode())).WithContext(ctx)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	return request
}

func clearQueuePayload(t *testing.T, response *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	var payload map[string]any
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &payload) != nil {
		t.Fatalf("response = %d %s, want JSON status %d", response.Code, response.Body.String(), status)
	}
	if status != http.StatusOK {
		if message, ok := payload["error"].(string); !ok || message == "" || payload["ok"] == true {
			t.Fatalf("failure not visible: %#v", payload)
		}
		if payload["editorText"] != nil {
			t.Fatalf("failure restores queue to editor: %#v", payload)
		}
	}
	return payload
}
