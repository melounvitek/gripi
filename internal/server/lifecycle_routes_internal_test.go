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

func TestLifecycleDecisionsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name       string
		state      map[string]any
		err        error
		cachedBusy bool
	}{
		{name: "state query error", err: errors.New("state unavailable")},
		{name: "state query rejected", state: map[string]any{"success": false, "error": "state rejected"}},
		{name: "native compaction", state: map[string]any{"success": true, "data": map[string]any{"isStreaming": false, "isCompacting": true}}},
		{name: "cached bash or retry", state: map[string]any{"success": true, "data": map[string]any{"isStreaming": false, "isCompacting": false}}, cachedBusy: true},
	} {
		for _, action := range []string{"delete", "tree"} {
			t.Run(test.name+"/"+action, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "session.jsonl")
				writeSessionRecords(t, path, []map[string]any{
					{"type": "session", "version": 3, "id": "lifecycle", "cwd": root},
					{"type": "message", "id": "before", "parentId": nil, "message": map[string]any{"role": "user", "content": []any{}}},
				})
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				client := &lifecycleStateClient{remapClient: &remapClient{state: test.state, busy: test.cachedBusy}, stateErr: test.err}
				registry := rpc.NewRegistry(nil, nil)
				if err := registry.Register(path, client); err != nil {
					t.Fatal(err)
				}
				cache := sessions.NewCache()
				app := &application{config: config.Config{SessionsRoot: root}, sessionCache: cache, rpcClients: registry, pendingSessions: rpc.NewPendingSessionRegistry(nil)}
				app.synchronizer = sessions.NewSynchronizer(root, root, cache, registry)
				queried := false
				client.onState = func() {
					queried = true
					if err := app.synchronizer.WithExclusiveOperation(path, func() error { return nil }); !errors.Is(err, sessions.ErrSyncBusy) {
						t.Errorf("state query was not serialized with mutations: %v", err)
					}
				}
				request := httptest.NewRequest(http.MethodPost, "/sessions/"+action, strings.NewReader(url.Values{"session": {path}, "entry_id": {"before"}}.Encode()))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				response := httptest.NewRecorder()
				if action == "delete" {
					app.deleteSession(response, request)
				} else {
					app.navigateTree(response, request)
				}
				if !queried && !(action == "delete" && test.cachedBusy) {
					t.Fatalf("did not query native state: %d %s", response.Code, response.Body.String())
				}
				if response.Code < 400 || client.closed || client.aborted || client.navigated {
					t.Fatalf("unsafe lifecycle decision: status=%d closed=%v aborted=%v navigated=%v", response.Code, client.closed, client.aborted, client.navigated)
				}
				if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
					t.Fatalf("session file changed: %v", err)
				}
			})
		}
	}
}

type lifecycleStateClient struct {
	*remapClient
	rpc.ActionClient
	stateErr  error
	onState   func()
	closed    bool
	aborted   bool
	navigated bool
}

func (client *lifecycleStateClient) GetState(context.Context) (map[string]any, error) {
	client.onState()
	return client.state, client.stateErr
}
func (client *lifecycleStateClient) Close() error {
	client.closed = true
	return nil
}
func (client *lifecycleStateClient) Abort(context.Context) (map[string]any, error) {
	client.aborted = true
	return map[string]any{"success": true}, nil
}
func (client *lifecycleStateClient) NavigateTree(context.Context, string, string, string) (map[string]any, error) {
	client.navigated = true
	return map[string]any{"success": true}, nil
}
