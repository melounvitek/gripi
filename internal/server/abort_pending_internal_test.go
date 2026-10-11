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

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

// unidentifiedPendingClient serves a new session whose Pi doesn't say which session file it writes.
type unidentifiedPendingClient struct{ *followUpRaceClient }

func (unidentifiedPendingClient) GetStateForInterrupt(context.Context) (map[string]any, error) {
	return nil, errors.New("get_state timed out")
}

func TestAbortRefusesToGuessWhichPendingSessionToStop(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session.jsonl")
	writeSessionRecords(t, path, []map[string]any{{"type": "session", "version": 3, "id": "requested", "cwd": project}})
	pendingPath := filepath.Join(root, "pending.jsonl")
	client := unidentifiedPendingClient{&followUpRaceClient{Client: (*rpc.Client)(nil)}}
	registry := agent.NewRegistry(func(string) (agent.Client, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(pendingPath, client); err != nil {
		t.Fatal(err)
	}
	pending := agent.NewPendingSessionRegistry(nil)
	pending.Remember(pendingPath, project, "")
	app := &application{config: config.Config{SessionsRoot: root}, sessionStore: NewPiStore(root, ""), rpcClients: registry, pendingSessions: pending}
	app.synchronizer = sessions.NewSynchronizer(root, root, nil, registry)
	request := httptest.NewRequest(http.MethodPost, "http://app.test/abort", strings.NewReader(url.Values{"session": {path}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()

	app.abortSession(response, request)

	expected := `{"error":"Could not identify the active pending Pi session; try stopping it again from its current page"}`
	if response.Code != http.StatusConflict || response.Body.String() != expected || client.aborted {
		t.Fatalf("response = %d %s, aborted=%v", response.Code, response.Body.String(), client.aborted)
	}
}
