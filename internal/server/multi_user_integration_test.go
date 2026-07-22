package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/server"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestWorkspaceAccessRejectsMalformedStateWithoutRewritingIt(t *testing.T) {
	cfg := multiUserConfig(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(cfg.WorkspaceAccessPath), 0700); err != nil {
		t.Fatal(err)
	}
	malformed := []byte(`{"approved_workspaces":`)
	if err := os.WriteFile(cfg.WorkspaceAccessPath, malformed, 0600); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	request.Header.Set("Cookie", "gripi_workspace=workspace")
	response := httptest.NewRecorder()
	multiUserHandler(t, cfg).ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || response.Body.String() != "Internal Server Error" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	persisted, err := os.ReadFile(cfg.WorkspaceAccessPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != string(malformed) {
		t.Fatalf("malformed state was rewritten: %q", persisted)
	}
}

func TestMultiUserTokenApprovalRemainsBrowserBound(t *testing.T) {
	root := t.TempDir()
	cfg := multiUserConfig(root)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	handler := multiUserHandler(t, cfg)
	weak := postWorkspaceForm(handler, "/workspace-key", url.Values{"workspace_key": {"aA1!😀😀"}}, "")
	if weak.Code != http.StatusForbidden || !strings.Contains(weak.Body.String(), "Enter a valid user token") {
		t.Fatalf("short Unicode token = %d %s", weak.Code, weak.Body.String())
	}
	first := postWorkspaceForm(handler, "/workspace-key", url.Values{"workspace_key": {"piu_correct_horse_42"}, "admin_password": {"secret"}}, "")
	if first.Code != http.StatusSeeOther {
		t.Fatalf("bootstrap = %d %s", first.Code, first.Body.String())
	}
	approver := cookieValue(first, "gripi_workspace")
	if approver == "" {
		t.Fatal("bootstrap workspace cookie missing")
	}
	pending := postWorkspaceForm(handler, "/workspace-key", url.Values{"workspace_key": {"piu_different_horse_42"}}, "gripi_browser=requester")
	if pending.Code != http.StatusForbidden || !strings.Contains(pending.Body.String(), "Waiting for workspace approval") {
		t.Fatalf("pending = %d %s", pending.Code, pending.Body.String())
	}
	store := access.NewWorkspaceStore(cfg.WorkspaceAccessPath)
	requests, err := store.PendingRequests()
	if err != nil || len(requests) != 1 {
		t.Fatalf("requests = %+v, %v", requests, err)
	}
	code := requests[0].Code
	approve := postWorkspaceForm(handler, "/workspace-access/approve", url.Values{"code": {code}}, "gripi_workspace="+approver)
	if approve.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", approve.Code, approve.Body.String())
	}
	other := getWorkspace(handler, "/workspace-access/status?code="+url.QueryEscape(code), "gripi_browser=other")
	if cookieValue(other, "gripi_workspace") != "" {
		t.Fatal("approval cookie leaked to another browser")
	}
	requester := getWorkspace(handler, "/workspace-access/status?code="+url.QueryEscape(code), "gripi_browser=requester")
	if cookieValue(requester, "gripi_workspace") == "" {
		t.Fatal("requester did not receive workspace cookie")
	}
}

func TestMultiUserFiltersListingsReadsActionsAndAttachments(t *testing.T) {
	fixture := seedNativeFixture(t)
	cfg := multiUserConfig(fixture.root)
	cfg.Home = fixture.home
	cfg.SessionsRoot = fixture.sessionsRoot
	cfg.AttachmentsRoot = fixture.attachmentsRoot
	cfg.SessionCwdsPath = fixture.configuredCWDs
	cfg.ReadStatePath = filepath.Join(fixture.root, "state", "read.json")
	cfg.PinnedSessionsPath = filepath.Join(fixture.root, "state", "pinned.json")
	handler := multiUserHandler(t, cfg)
	workspace := "workspace-a"
	if err := access.NewWorkspaceStore(cfg.WorkspaceAccessPath).ApproveWorkspace(workspace); err != nil {
		t.Fatal(err)
	}
	var paths []string
	_ = filepath.WalkDir(fixture.sessionsRoot, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && filepath.Ext(path) == ".jsonl" {
			paths = append(paths, path)
		}
		return err
	})
	if len(paths) < 2 {
		t.Fatalf("session paths = %v", paths)
	}
	own, other := paths[0], paths[1]
	owners := access.NewWorkspaceOwnershipStore(cfg.WorkspaceOwnershipPath)
	if _, err := owners.Claim(own, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := owners.Claim(other, "workspace-b"); err != nil {
		t.Fatal(err)
	}
	cookie := "gripi_workspace=" + workspace
	index := getWorkspace(handler, "/", cookie)
	if index.Code != http.StatusOK || !strings.Contains(index.Body.String(), own) || strings.Contains(index.Body.String(), other) {
		t.Fatalf("filtered index = %d %s", index.Code, index.Body.String())
	}
	older := getWorkspace(handler, "/conversation_older?session="+url.QueryEscape(other), cookie)
	if older.Code != http.StatusNotFound {
		t.Fatalf("other history = %d", older.Code)
	}
	events := getWorkspace(handler, "/events?session="+url.QueryEscape(other), cookie)
	if events.Code != http.StatusNotFound {
		t.Fatalf("other events = %d", events.Code)
	}
	pin := postWorkspaceForm(handler, "/sessions/pin", url.Values{"session": {other}, "pinned": {"true"}}, cookie)
	if pin.Code != http.StatusNotFound {
		t.Fatalf("other action = %d", pin.Code)
	}
	for _, target := range []string{
		"/status?session=", "/commands?session=", "/sessions/model_settings?session=",
		"/sessions/fork_messages?session=", "/sessions/tree_entries?session=",
	} {
		result := getWorkspace(handler, target+url.QueryEscape(other), cookie)
		if result.Code != http.StatusNotFound {
			t.Fatalf("other session GET %s = %d", target, result.Code)
		}
	}
	for _, target := range []string{
		"/prompt", "/abort", "/compact", "/sessions/new", "/sessions/model_settings",
		"/sessions/cycle_thinking", "/sessions/tree", "/sessions/tree/label", "/sessions/fork",
		"/sessions/clone", "/extension_ui_response", "/sessions/takeover", "/sessions/mark_read",
		"/composer/path_suggestions",
	} {
		result := postWorkspaceForm(handler, target, url.Values{"session": {other}, "mode": {"fuzzy"}}, cookie)
		if result.Code != http.StatusNotFound {
			t.Fatalf("other session POST %s = %d %s", target, result.Code, result.Body.String())
		}
	}
	hash := sessions.SessionHash(other)
	file := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png"
	if err := os.MkdirAll(filepath.Join(cfg.AttachmentsRoot, hash), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.AttachmentsRoot, hash, file), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	attachment := getWorkspace(handler, "/attachments/"+hash+"/"+file, cookie)
	if attachment.Code != http.StatusNotFound {
		t.Fatalf("other attachment = %d", attachment.Code)
	}
}

func multiUserConfig(root string) config.Config {
	return config.Config{Address: "127.0.0.1:4567", Environment: "test", Home: root, SessionsRoot: filepath.Join(root, "sessions"), AttachmentsRoot: filepath.Join(root, "attachments"), ReadStatePath: filepath.Join(root, "read.json"), PinnedSessionsPath: filepath.Join(root, "pinned.json"), BrowserAccessPath: filepath.Join(root, "browser.json"), WorkspaceSecretPath: filepath.Join(root, "secret"), WorkspaceAccessPath: filepath.Join(root, "workspace-access.json"), WorkspaceOwnershipPath: filepath.Join(root, "owners.json"), RestartPath: filepath.Join(root, "restart"), BrowserAuthDisabled: true, MultiUserMode: true}
}
func multiUserHandler(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	handler, err := server.NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := handler.(interface{ Close(context.Context) error }); ok {
		t.Cleanup(func() { _ = closer.Close(context.Background()) })
	}
	return handler
}
func postWorkspaceForm(handler http.Handler, target string, values url.Values, cookie string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "http://app.test"+target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func getWorkspace(handler http.Handler, target, cookie string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "http://app.test"+target, nil)
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func cookieValue(response *httptest.ResponseRecorder, name string) string {
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}
