package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestDeletePendingSession(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
	}{
		{"idle", http.StatusOK},
		{"retired", http.StatusOK},
		{"persisted", http.StatusOK},
		{"remapped", http.StatusOK},
		{"current", http.StatusConflict},
		{"aliased_current_fileless", http.StatusConflict},
		{"aliased_current_persisted", http.StatusConflict},
		{"unowned_current_alias", http.StatusInternalServerError},
		{"busy", http.StatusConflict},
		{"streaming", http.StatusConflict},
		{"compacting", http.StatusConflict},
		{"state_error", http.StatusInternalServerError},
		{"state_rejected", http.StatusInternalServerError},
		{"unknown", http.StatusNotFound},
		{"unowned", http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "pending.jsonl")
			target := path
			data := map[string]any{"sessionFile": path}
			client := &lifecycleStateClient{
				remapClient: &remapClient{state: map[string]any{"success": true, "data": data}},
				onState:     func() {},
			}
			registry := rpc.NewRegistry(nil, nil)
			pending := rpc.NewPendingSessionRegistry(nil)
			if test.name != "unknown" {
				pending.Remember(path, root)
			}
			if test.name != "retired" && test.name != "unknown" {
				if err := registry.Register(path, client); err != nil {
					t.Fatal(err)
				}
			}
			cache := sessions.NewCache()
			state := sessions.NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pins.json"), filepath.Join(root, "tags.json"), root)
			app := &application{
				config:       config.Config{SessionsRoot: root, Home: root, AttachmentsRoot: filepath.Join(root, "attachments")},
				sessionCache: cache, gatewayState: state, rpcClients: registry, pendingSessions: pending,
				synchronizer: sessions.NewSynchronizer(root, root, cache, registry),
				ownsSession:  func(*http.Request, string) bool { return test.name != "unowned" },
			}
			released := ""
			app.releaseSession = func(_ *http.Request, path string) error { released = path; return nil }
			if err := state.SetTag(path, "pending", true); err != nil {
				t.Fatal(err)
			}
			form := url.Values{"session": {path}}
			switch test.name {
			case "current":
				form.Set("current_session", path)
			case "busy":
				client.busy = true
			case "streaming":
				data["isStreaming"] = true
			case "compacting":
				data["isCompacting"] = true
			case "state_error":
				client.stateErr = errors.New("state unavailable")
			case "state_rejected":
				client.state["success"] = false
			case "persisted", "remapped":
				if test.name == "remapped" {
					target = filepath.Join(root, "actual.jsonl")
					data["sessionFile"] = target
				}
				writeSessionRecords(t, target, []map[string]any{{"type": "session", "version": 3, "id": "pending", "cwd": root}})
			}
			if strings.Contains(test.name, "current_") {
				alias := filepath.Join(root, "old.jsonl")
				pending.Remap(alias, path)
				form.Set("current_session", alias)
				if test.name == "aliased_current_persisted" {
					writeSessionRecords(t, path, []map[string]any{{"type": "session", "version": 3, "id": "pending", "cwd": root}})
				}
				if test.name == "unowned_current_alias" {
					app.ownsSession = func(_ *http.Request, candidate string) bool { return candidate != alias }
				}
			}
			request := httptest.NewRequest(http.MethodPost, "/sessions/delete", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()

			app.deleteSession(response, request)

			if response.Code != test.status {
				t.Fatalf("delete = %d %s; want %d", response.Code, response.Body.String(), test.status)
			}
			if test.status != http.StatusOK {
				if client.closed || client.aborted || released != "" || state.SessionForgotten(path) {
					t.Fatal("rejected deletion changed session lifecycle")
				}
				return
			}
			if !strings.Contains(response.Body.String(), `"deleted":true`) || !strings.Contains(response.Body.String(), target) {
				t.Fatalf("delete response = %s", response.Body.String())
			}
			if registry.Active(path) || registry.Active(target) || len(pending.Entries()) != 0 || released != target {
				t.Fatalf("deleted session retained client, pending entry or ownership: release=%q", released)
			}
			if test.name != "retired" && !client.closed || client.aborted {
				t.Fatal("deletion did not close idle client without aborting")
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("deleted session file remains: %v", err)
			}
			tags, err := state.SessionTags()
			if err != nil || len(tags) != 0 || !state.SessionForgotten(target) {
				t.Fatalf("deleted session retained gateway metadata: %v, %v", tags, err)
			}
		})
	}
}

func TestDeletePendingSessionRejectsActivePromptAdmission(t *testing.T) {
	fixture := newPendingDeleteFixture(t)
	entered, release, block := idleRetirementBarrier()
	defer release()
	prompt := httptest.NewRecorder()
	done := idleRetirementRun(t, func() {
		fixture.handler.ServeHTTP(&idleRetirementResponseWriter{ResponseWriter: prompt, beforeWrite: block}, idleRetirementRequest(context.Background(), fixture.path, ""))
	})
	idleRetirementWait(t, entered, "acknowledged prompt before agent_start")
	response := pendingDeleteRequest(fixture)
	if response.Code != http.StatusConflict || fixture.old.closes.Load() != 0 || fixture.app.gatewayState.SessionForgotten(fixture.path) {
		t.Fatalf("delete during prompt admission = %d %s, closes=%d", response.Code, response.Body.String(), fixture.old.closes.Load())
	}
	release()
	idleRetirementWait(t, done, "prompt response")
	idleRetirementAssertSuccess(t, prompt, fixture.path, "")
	fixture.old.assertCalls(t, "", 1)
	if response := pendingDeleteRequest(fixture); response.Code != http.StatusOK {
		t.Fatalf("delete after admission release = %d %s", response.Code, response.Body.String())
	}
}

func TestDeletePendingSessionExcludesAdmissionBetweenStateAndClose(t *testing.T) {
	fixture := newPendingDeleteFixture(t)
	client := &pendingDeleteGapClient{idleRetirementClient: fixture.old}
	if err := fixture.app.rpcClients.Register(fixture.path, client); err != nil {
		t.Fatal(err)
	}
	checked := false
	client.beforeClose = func() {
		checked = true
		// CloseClientIfIdle has reached its predicate, after the native state
		// RPC lease was released. No cached agent_start is available yet.
		_, release, err := fixture.app.promptAdmissions.prompt(context.Background(), func() (string, error) { return fixture.path, nil })
		if err == nil {
			release()
			t.Error("prompt admitted between delete state check and close")
		}
	}
	response := pendingDeleteRequest(fixture)
	if response.Code != http.StatusOK || !checked {
		t.Fatalf("delete = %d %s; checked gap=%v", response.Code, response.Body.String(), checked)
	}
}

func TestDeletePendingSessionRejectsPrevalidatedPrompt(t *testing.T) {
	fixture := newPendingDeleteFixture(t)
	entered, release, block := idleRetirementBarrier()
	defer release()
	ctx := &pendingAdmissionContext{Context: context.Background(), beforeErr: block}
	prompt := httptest.NewRecorder()
	done := idleRetirementRun(t, func() {
		fixture.handler.ServeHTTP(prompt, idleRetirementRequest(ctx, fixture.path, ""))
	})
	idleRetirementWait(t, entered, "validated prompt before admission")
	if response := pendingDeleteRequest(fixture); response.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", response.Code, response.Body.String())
	}
	release()
	idleRetirementWait(t, done, "prevalidated prompt")
	if prompt.Code != http.StatusNotFound || fixture.created.Load() != 0 || fixture.app.rpcClients.Active(fixture.path) {
		t.Fatalf("deleted session resurrected: response=%d %s, factory=%d", prompt.Code, prompt.Body.String(), fixture.created.Load())
	}
	fixture.replacement.assertCalls(t, "", 0)
}

func newPendingDeleteFixture(t *testing.T) *idleRetirementFixture {
	t.Helper()
	fixture := newIdleRetirementFixture(t)
	if err := os.Remove(fixture.path); err != nil {
		t.Fatal(err)
	}
	root := fixture.app.config.SessionsRoot
	fixture.app.config.AttachmentsRoot = filepath.Join(root, "attachments")
	fixture.app.gatewayState = sessions.NewGatewayState(filepath.Join(root, "read.json"), filepath.Join(root, "pins.json"), filepath.Join(root, "tags.json"), root)
	fixture.app.pendingSessions.Remember(fixture.path, root)
	fixture.old.state = map[string]any{"success": true, "data": map[string]any{"sessionFile": fixture.path}}
	return fixture
}

func pendingDeleteRequest(fixture *idleRetirementFixture) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/sessions/delete", strings.NewReader(url.Values{"session": {fixture.path}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}

type pendingDeleteGapClient struct {
	*idleRetirementClient
	states      int
	beforeClose func()
}

func (client *pendingDeleteGapClient) GetState(ctx context.Context) (map[string]any, error) {
	client.states++
	return client.idleRetirementClient.GetState(ctx)
}

func (client *pendingDeleteGapClient) Busy() bool {
	// The first state query canonicalizes the request; the second is the
	// deletion preflight. Its next Busy call is the close predicate.
	if client.states == 2 {
		client.beforeClose()
	}
	return false
}
