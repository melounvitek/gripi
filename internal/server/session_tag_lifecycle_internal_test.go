package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestNewSessionTagsValidateBeforeStartingPi(t *testing.T) {
	tooMany := make([]string, 33)
	for i := range tooMany {
		tooMany[i] = fmt.Sprint(i)
	}
	for _, names := range [][]string{{"work", ""}, {"work", "bad\n"}, tooMany} {
		started := false
		app := &application{newRPCClient: func(string) (rpc.RPCClient, error) { started = true; return nil, fmt.Errorf("unexpected startup") }}
		request := tagLifecycleRequest("/sessions/new_at_cwd", url.Values{"cwd": {t.TempDir()}, "tags": names})
		response := httptest.NewRecorder()
		app.newSessionAtCWD(response, request)
		if started || response.Code != http.StatusBadRequest {
			t.Fatalf("invalid tags started Pi=%v: %d %s", started, response.Code, response.Body.String())
		}
	}
}

func TestInitialTagsPersistForSyntheticAndNativeSessions(t *testing.T) {
	for _, kind := range []string{"synthetic", "native-pending", "native-persisted"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			reported := ""
			if kind != "synthetic" {
				reported = filepath.Join(root, "native.jsonl")
			}
			var before []byte
			if kind == "native-persisted" {
				writeSessionRecords(t, reported, []map[string]any{{"type": "session", "version": 3, "id": "native", "cwd": root}})
				var err error
				before, err = os.ReadFile(reported)
				if err != nil {
					t.Fatal(err)
				}
			}
			state := sessions.NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pins"), filepath.Join(root, "tags"), root)
			registry := rpc.NewRegistry(nil, nil)
			app := &application{config: config.Config{SessionsRoot: root}, gatewayState: state, rpcClients: registry, pendingSessions: rpc.NewPendingSessionRegistry(nil), newRPCClient: func(string) (rpc.RPCClient, error) {
				return &remapClient{state: map[string]any{"data": map[string]any{"sessionFile": reported}}}, nil
			}}
			path, err := app.startNewSession(tagLifecycleRequest("/sessions/new_at_cwd?tag=filter&tags=query", url.Values{"tags": {" Work ", "alpha", "WORK"}}), root)
			if err != nil {
				t.Fatal(err)
			}
			reloaded := sessions.NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pins"), filepath.Join(root, "tags"), root)
			tags, err := reloaded.SessionTags()
			if err != nil || !reflect.DeepEqual(tags, map[string][]string{path: {"alpha", "work"}}) {
				t.Fatalf("initial tags = %v, %v", tags, err)
			}
			if kind == "native-persisted" {
				after, err := os.ReadFile(path)
				if err != nil || string(before) != string(after) {
					t.Fatalf("creation changed native contents: %v", err)
				}
			} else if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("tag assignment materialized a native file: %v", err)
			}
		})
	}
}

func TestNewSessionTagsRollbackRegistrationFailure(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			root := t.TempDir()
			state := sessions.NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pins"), filepath.Join(root, "tags"), root)
			if err := state.SetTag("/unrelated", "keep", true); err != nil {
				t.Fatal(err)
			}
			client := &remapClient{state: map[string]any{"data": map[string]any{}}}
			if native {
				client.state = map[string]any{"data": map[string]any{"sessionFile": filepath.Join(root, "native.jsonl")}}
			}
			registry := rpc.NewRegistry(nil, nil)
			if err := registry.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			owned := map[string]bool{}
			app := &application{config: config.Config{SessionsRoot: root}, gatewayState: state, rpcClients: registry, pendingSessions: rpc.NewPendingSessionRegistry(nil), newRPCClient: func(string) (rpc.RPCClient, error) { return client, nil }, claimSession: func(_ *http.Request, path string) (bool, error) { owned[path] = true; return true, nil }, releaseSession: func(_ *http.Request, path string) error { delete(owned, path); return nil }}
			request := tagLifecycleRequest("/sessions/new_at_cwd", url.Values{"tags": {"draft"}})
			if _, err := app.startNewSession(request, root); err == nil {
				t.Fatal("registration succeeded")
			}
			tags, err := state.SessionTags()
			if err != nil || !reflect.DeepEqual(tags, map[string][]string{"/unrelated": {"keep"}}) || len(owned) != 0 {
				t.Fatalf("failed creation left tags=%v ownership=%v err=%v", tags, owned, err)
			}
		})
	}
}

func TestPendingTagsMaterializeInBackgroundWithoutRequest(t *testing.T) {
	for _, multiUser := range []bool{false, true} {
		t.Run(fmt.Sprint(multiUser), func(t *testing.T) {
			root := t.TempDir()
			actual := writeNotificationSession(t, root, "materialized")
			before, err := os.ReadFile(actual)
			if err != nil {
				t.Fatal(err)
			}
			app := notificationTestApplication(t, root, multiUser, true, &recordingPushNotifier{})
			app.config.AttachmentsRoot = filepath.Join(root, "attachments")
			pending := filepath.Join(root, "pending.jsonl")
			if multiUser {
				if err := app.workspaceStore.ApproveWorkspace("owner"); err != nil {
					t.Fatal(err)
				}
				if _, err := app.ownershipStore.Claim(pending, "owner"); err != nil {
					t.Fatal(err)
				}
			}
			client := &remapClient{state: map[string]any{"data": map[string]any{"sessionFile": actual}}}
			if err := app.rpcClients.Register(pending, client); err != nil {
				t.Fatal(err)
			}
			app.pendingSessions.Remember(pending, root)
			if err := app.gatewayState.SetTag(pending, "work", true); err != nil {
				t.Fatal(err)
			}
			delivered := make(chan struct{}, 1)
			app.pushNotifier = pushNotifierFunc(func(context.Context, string, []byte) error {
				delivered <- struct{}{}
				return nil
			})
			notifier := newCompletionNotifier(app)
			notifier.gracePeriod = 0
			defer notifier.Close(context.Background())
			notifier.schedule(completedReply{client: client, path: pending, text: "done", id: "reply"})
			select {
			case <-delivered:
			case <-time.After(3 * time.Second):
				t.Fatal("background completion did not finish")
			}
			tags, err := app.gatewayState.SessionTags()
			if err != nil || !reflect.DeepEqual(tags, map[string][]string{actual: {"work"}}) {
				t.Fatalf("background tags=%v, %v", tags, err)
			}
			if path, ok := app.pendingSessions.Resolve(pending); !ok || path != actual {
				t.Fatalf("background alias=%s %v", path, ok)
			}
			after, err := os.ReadFile(actual)
			if err != nil || string(before) != string(after) {
				t.Fatalf("native contents changed: %v", err)
			}
		})
	}
}

func TestPendingTagMigrationFailureRollsBackMetadataAndOwnership(t *testing.T) {
	root := t.TempDir()
	from, to := filepath.Join(root, "pending.jsonl"), filepath.Join(root, "native.jsonl")
	state := sessions.NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pins"), filepath.Join(root, "tags"), root)
	for i := range 32 {
		if err := state.SetTag(to, fmt.Sprint(i), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetTag(from, "source", true); err != nil {
		t.Fatal(err)
	}
	if err := state.SetPinned(from, true); err != nil {
		t.Fatal(err)
	}
	before, err := state.SessionTags()
	if err != nil {
		t.Fatal(err)
	}
	registry := rpc.NewRegistry(nil, nil)
	if err := registry.Register(from, &remapClient{}); err != nil {
		t.Fatal(err)
	}
	pending := rpc.NewPendingSessionRegistry(nil)
	pending.Remember(from, root)
	attachments := filepath.Join(root, "attachments")
	if err := os.Mkdir(attachments, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(attachments, sessions.SessionHash(from)+".jsonl")
	metadata := []byte("gateway metadata\n")
	if err := os.WriteFile(source, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	released := ""
	app := &application{config: config.Config{AttachmentsRoot: attachments}, gatewayState: state, rpcClients: registry, pendingSessions: pending,
		claimSession:   func(*http.Request, string) (bool, error) { return true, nil },
		releaseSession: func(_ *http.Request, path string) error { released = path; return nil },
	}
	if err := app.movePendingRPCClient(httptest.NewRequest(http.MethodGet, "/", nil), from, to); err == nil {
		t.Fatal("migration exceeded tag limit")
	}
	tags, err := state.SessionTags()
	if err != nil || !reflect.DeepEqual(tags, before) {
		t.Fatalf("failed migration tags=%v, %v", tags, err)
	}
	_, pins, err := state.ReadAndObserve(nil, nil, false, nil)
	if err != nil || !pins[from] || pins[to] {
		t.Fatalf("failed migration pins=%v, %v", pins, err)
	}
	contents, err := os.ReadFile(source)
	if err != nil || string(contents) != string(metadata) {
		t.Fatalf("failed migration attachments=%q, %v", contents, err)
	}
	if !registry.Active(from) || registry.Active(to) || released != to {
		t.Fatalf("failed migration active source=%v destination=%v released=%s", registry.Active(from), registry.Active(to), released)
	}
	if _, remapped := pending.Resolve(from); remapped {
		t.Fatal("failed migration published an alias")
	}
}

func TestNewSessionTagStorageFailureReleasesOwnership(t *testing.T) {
	root := t.TempDir()
	tagsPath := filepath.Join(root, "tags")
	if err := os.WriteFile(tagsPath, []byte("malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	state := sessions.NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pins"), tagsPath, root)
	path := filepath.Join(root, "native.jsonl")
	registry := rpc.NewRegistry(nil, nil)
	owned := false
	app := &application{config: config.Config{SessionsRoot: root}, gatewayState: state, rpcClients: registry, pendingSessions: rpc.NewPendingSessionRegistry(nil),
		newRPCClient: func(string) (rpc.RPCClient, error) {
			return &remapClient{state: map[string]any{"data": map[string]any{"sessionFile": path}}}, nil
		},
		claimSession:   func(*http.Request, string) (bool, error) { owned = true; return true, nil },
		releaseSession: func(*http.Request, string) error { owned = false; return nil },
	}
	if _, err := app.startNewSession(tagLifecycleRequest("/sessions/new", url.Values{"tags": {"work"}}), root); err == nil {
		t.Fatal("creation accepted malformed tag storage")
	}
	if owned || registry.Active(path) || len(app.pendingSessions.Entries()) != 0 {
		t.Fatal("failed creation left ownership or pending state")
	}
}

func TestBackgroundCompletionDoesNotMigrateTagsAfterConcurrentBranch(t *testing.T) {
	for _, operation := range []string{"clone", "fork", "new"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			child := writeNotificationSession(t, root, "child")
			parent := filepath.Join(root, "pending.jsonl")
			app := notificationTestApplication(t, root, false, true, &recordingPushNotifier{})
			app.config.AttachmentsRoot = filepath.Join(root, "attachments")
			app.synchronizer = sessions.NewSynchronizer(root, root, app.sessionCache, app.rpcClients)
			client := &branchCompletionClient{remapClient: &remapClient{state: map[string]any{"success": true, "data": map[string]any{"sessionFile": child}}}, observed: make(chan struct{}), branched: make(chan struct{})}
			if err := app.rpcClients.Register(parent, client); err != nil {
				t.Fatal(err)
			}
			app.pendingSessions.Remember(parent, root)
			if err := app.gatewayState.SetTag(parent, "parent", true); err != nil {
				t.Fatal(err)
			}
			notifier := newCompletionNotifier(app)
			completed := make(chan error, 1)
			go func() {
				completed <- notifier.deliver(context.Background(), completedReply{client: client, path: parent, text: "done", id: "reply"})
			}()
			<-client.observed
			response := httptest.NewRecorder()
			app.replaceSessionFromAction(response, tagLifecycleRequest("/sessions/"+operation, nil), parent, operation, "entry")
			close(client.branched)
			if response.Code != http.StatusOK {
				t.Fatalf("branch = %d %s", response.Code, response.Body.String())
			}
			select {
			case err := <-completed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("background completion did not finish")
			}
			tags, err := app.gatewayState.SessionTags()
			expected := map[string][]string{parent: {"parent"}, child: {"parent"}}
			if operation == "new" {
				delete(expected, child)
			}
			if err != nil || !reflect.DeepEqual(tags, expected) {
				t.Fatalf("concurrent branch tags = %v, %v", tags, err)
			}
		})
	}
}

type branchCompletionClient struct {
	*remapClient
	rpc.ActionClient
	observed chan struct{}
	branched chan struct{}
	calls    atomic.Int32
}

func (client *branchCompletionClient) GetState(ctx context.Context) (map[string]any, error) {
	if client.calls.Add(1) == 1 {
		close(client.observed)
		<-client.branched
	}
	return client.remapClient.GetState(ctx)
}

func (client *branchCompletionClient) CloneSession(context.Context) (map[string]any, error) {
	return map[string]any{"success": true}, nil
}

func (client *branchCompletionClient) Fork(context.Context, string) (map[string]any, error) {
	return map[string]any{"success": true}, nil
}

func (client *branchCompletionClient) NewSession(context.Context, string) (map[string]any, error) {
	return map[string]any{"success": true}, nil
}

func tagLifecycleRequest(path string, form url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	return request
}
