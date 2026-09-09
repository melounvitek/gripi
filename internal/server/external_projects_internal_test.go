package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestNewCLIProjectsStayOutOfSelectorsUntilTakeoverAcrossRestarts(t *testing.T) {
	root := t.TempDir()
	writeNotificationSession(t, root, "Existing project")
	cfg := config.Config{Home: root, SessionsRoot: root, ReadStatePath: filepath.Join(root, "read.json"), PinnedSessionsPath: filepath.Join(root, "pins.json"), SessionTagsPath: filepath.Join(root, "tags.json"), BrowserAuthDisabled: true}
	start := func() *Handler {
		handler, err := NewHandler(cfg, gripi.WebFiles)
		if err != nil {
			t.Fatal(err)
		}
		result := handler.(*Handler)
		t.Cleanup(func() { _ = result.Close(context.Background()) })
		return result
	}
	handler := start()
	// The CLI session starts after gateway startup, before any page was requested.
	cwd := filepath.Join(root, "cli-project")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "cli-session.jsonl")
	writeSessionRecords(t, path, []map[string]any{{"type": "session", "version": 3, "id": "cli", "timestamp": "2026-01-01T00:00:00Z", "cwd": cwd}})
	check := func(app *application, adopted bool) {
		t.Helper()
		for _, target := range []string{"/sidebar?no_session=1", "/?session=" + url.QueryEscape(path)} {
			view, err := app.preparePage(httptest.NewRequest(http.MethodGet, "http://app.test"+target, nil), strings.HasPrefix(target, "/?"))
			if err != nil {
				t.Fatal(err)
			}
			if len(view.Sessions) != 2 {
				t.Fatalf("CLI session disappeared from All projects: %d sessions", len(view.Sessions))
			}
			for _, projects := range [][]string{view.KnownCWDs, view.NewSessionCWDs} {
				if !slices.Contains(projects, root) || slices.Contains(projects, cwd) != adopted {
					t.Fatalf("adopted=%t projects=%v", adopted, projects)
				}
			}
		}
	}
	check(handler.app, false)
	if err := handler.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler = start()
	check(handler.app, false)

	app := handler.app
	client := &remapClient{position: rpc.SessionEntries{Known: true, LeafID: "external"}}
	app.rpcClients = rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return client, nil }, nil)
	app.synchronizer = sessions.NewSynchronizer(root, root, app.sessionCache, app.rpcClients)
	if _, err := app.synchronizer.Inspect(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	appendExternalSessionReply(t, path, "external", "")
	check(app, false)
	request := httptest.NewRequest(http.MethodPost, "http://app.test/sessions/takeover", strings.NewReader(url.Values{"session": {path}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.takeOverSession(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("takeover = %d: %s", response.Code, response.Body.String())
	}
	check(app, true)
	if err := handler.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	check(start().app, true)
}

func TestCreatingSessionInGripiAddsItsProject(t *testing.T) {
	app, _, _ := externalSessionTestApplication(t)
	cwd := filepath.Join(app.config.Home, "new-project")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(app.config.SessionsRoot, "gateway-session.jsonl")
	app.newRPCClient = func(string) (rpc.RPCClient, error) {
		writeSessionRecords(t, path, []map[string]any{{"type": "session", "version": 3, "id": "gateway", "timestamp": "2026-01-01T00:00:00Z", "cwd": cwd}})
		return &remapClient{state: map[string]any{"data": map[string]any{"sessionFile": path}}}, nil
	}
	request := httptest.NewRequest(http.MethodPost, "http://app.test/sessions/new_at_cwd", strings.NewReader(url.Values{"cwd": {cwd}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.newSessionAtCWD(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create session = %d: %s", response.Code, response.Body.String())
	}
	view, err := app.preparePage(httptest.NewRequest(http.MethodGet, "http://app.test/sidebar?no_session=1", nil), false)
	if err != nil || !slices.Contains(view.KnownCWDs, cwd) || !slices.Contains(view.NewSessionCWDs, cwd) {
		t.Fatalf("new gateway project missing: view=%#v, %v", view, err)
	}
	projects, err := app.gatewayState.ProjectCWDs(nil)
	if err != nil || !projects[cwd] {
		t.Fatalf("gateway project not persisted: %v, %v", projects, err)
	}
}
