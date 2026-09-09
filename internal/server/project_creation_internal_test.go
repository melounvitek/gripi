package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/melounvitek/gripi/internal/rpc"
)

func TestSessionActionsRememberDiscoveredProjectOnlyOnSuccess(t *testing.T) {
	for _, operation := range []string{"new", "clone", "fork"} {
		for _, outcome := range []string{"success", "cancelled"} {
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				app, _, _ := externalSessionTestApplication(t)
				cwd := filepath.Join(app.config.Home, "cli-project")
				if err := os.Mkdir(cwd, 0700); err != nil {
					t.Fatal(err)
				}
				parent := filepath.Join(app.config.SessionsRoot, "parent.jsonl")
				child := filepath.Join(app.config.SessionsRoot, "child.jsonl")
				for _, path := range []string{parent, child} {
					writeSessionRecords(t, path, []map[string]any{
						{"type": "session", "version": 3, "id": filepath.Base(path), "timestamp": "2026-01-01T00:00:00Z", "cwd": cwd},
						{"type": "message", "id": "source", "timestamp": "2026-01-01T00:00:01Z", "message": map[string]any{"role": "user", "content": "hello"}},
					})
				}
				projects, err := app.gatewayState.ProjectCWDs(nil)
				if err != nil || projects[cwd] {
					t.Fatalf("CLI project already known: projects=%v err=%v", projects, err)
				}
				cancelled := outcome == "cancelled"
				client := &projectCreationClient{
					remapClient: &remapClient{
						state:    map[string]any{"success": true, "data": map[string]any{"sessionFile": child}},
						position: rpc.SessionEntries{Known: true, LeafID: "source"},
					},
					cancelled: cancelled,
				}
				if err := app.rpcClients.Register(parent, client); err != nil {
					t.Fatal(err)
				}
				response := httptest.NewRecorder()
				app.replaceSessionFromAction(response, tagLifecycleRequest("/sessions/"+operation, nil), parent, operation, "source")
				wantStatus := http.StatusOK
				if cancelled {
					wantStatus = http.StatusConflict
				}
				if response.Code != wantStatus {
					t.Fatalf("action status=%d, want %d: %s", response.Code, wantStatus, response.Body.String())
				}
				projects, err = app.gatewayState.ProjectCWDs(nil)
				if err != nil {
					t.Fatal(err)
				}
				if projects[cwd] != !cancelled {
					t.Errorf("project remembered=%v, want %v", projects[cwd], !cancelled)
				}
			})
		}
	}
}

func TestNewSessionProjectStorageFailureRollsBackCreation(t *testing.T) {
	for _, failure := range []string{"malformed", "directory"} {
		t.Run(failure, func(t *testing.T) {
			app, _, _ := externalSessionTestApplication(t)
			projectsPath := filepath.Join(app.config.Home, "projects.json")
			if failure == "malformed" {
				if err := os.WriteFile(projectsPath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(projectsPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(projectsPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			newPath := filepath.Join(app.config.SessionsRoot, "pending-new.jsonl")
			app.newRPCClient = func(string) (rpc.RPCClient, error) {
				return &remapClient{state: map[string]any{"data": map[string]any{"sessionFile": newPath}}}, nil
			}
			claimed, released := false, false
			app.claimSession = func(*http.Request, string) (bool, error) {
				claimed = true
				return true, nil
			}
			app.releaseSession = func(*http.Request, string) error {
				released = true
				return nil
			}
			request := tagLifecycleRequest("/sessions/new_at_cwd", url.Values{"tags": {"work"}})
			if _, err := app.startNewSession(request, app.config.Home); err == nil {
				t.Fatal("creation accepted broken project storage")
			}
			if app.rpcClients.Active(newPath) {
				t.Error("failed creation left an active RPC client")
			}
			if _, ok := app.pendingSessions.CWD(newPath); ok {
				t.Error("failed creation left a pending session")
			}
			if claimed && !released {
				t.Error("failed creation did not release ownership")
			}
			tags, err := app.gatewayState.SessionTags()
			if err != nil {
				t.Fatal(err)
			}
			if len(tags[newPath]) != 0 {
				t.Errorf("failed creation left tags: %v", tags[newPath])
			}
		})
	}
}

type projectCreationClient struct {
	*remapClient
	rpc.ActionClient
	cancelled bool
}

func (client *projectCreationClient) NewSession(context.Context, string) (map[string]any, error) {
	return map[string]any{"success": true, "data": map[string]any{"cancelled": client.cancelled}}, nil
}

func (client *projectCreationClient) CloneSession(ctx context.Context) (map[string]any, error) {
	return client.NewSession(ctx, "")
}

func (client *projectCreationClient) Fork(ctx context.Context, _ string) (map[string]any, error) {
	return client.NewSession(ctx, "")
}
