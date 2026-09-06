package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestSessionTagLifecycleWithNativePi(t *testing.T) {
	root := t.TempDir()
	sessionsRoot := filepath.Join(root, "sessions")
	if err := os.Mkdir(sessionsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(sessionsRoot, "parent.jsonl")
	writeActionSession(t, parent, root)
	_, file, _, _ := runtime.Caller(0)
	t.Setenv("GRIPI_E2E_SESSIONS_ROOT", sessionsRoot)
	cfg := config.Config{Environment: "test", Home: root, SessionsRoot: sessionsRoot, AttachmentsRoot: filepath.Join(root, "attachments"), ReadStatePath: filepath.Join(root, "read.json"), PinnedSessionsPath: filepath.Join(root, "pins.json"), SessionTagsPath: filepath.Join(root, "tags.json"), BrowserAuthDisabled: true, PiCommand: []string{"node", filepath.Join(filepath.Dir(file), "..", "..", "e2e", "support", "fake_pi.mjs")}}
	handler, err := gateway.NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closer, ok := handler.(interface{ Close(context.Context) error }); ok {
			_ = closer.Close(context.Background())
		}
	}()
	post := func(route string, form url.Values) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "http://app.test"+route, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Accept", "application/json")
		response := serveAction(handler, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", route, response.Code, response.Body.String())
		}
		var result struct {
			Session  string
			Redirect string
		}
		decodeActionJSON(t, response, &result)
		if form.Get("tag") != "" {
			redirect, err := url.Parse(result.Redirect)
			if err != nil || redirect.Query().Get("tag") != form.Get("tag") {
				t.Fatalf("filter lost in redirect: %s, %v", result.Redirect, err)
			}
		}
		return result.Session
	}
	assertTags := func(path string, expected []string) {
		t.Helper()
		result := readTagResponse(t, getWorkspace(handler, "/sessions/tags?session="+url.QueryEscape(path), ""))
		if !reflect.DeepEqual(result.Tags, expected) {
			t.Fatalf("tags for %s = %v, want %v", path, result.Tags, expected)
		}
	}
	readTagResponse(t, postWorkspaceForm(handler, "/sessions/tags", url.Values{"session": {parent}, "tag": {"work"}, "assigned": {"true"}}, ""))
	for _, operation := range []string{"clone", "fork"} {
		child := post("/sessions/"+operation, url.Values{"session": {parent}, "entry_id": {"u1"}, "tag": {"work"}})
		assertTags(child, []string{"work"})
		assertTags(parent, []string{"work"})
	}
	fresh := post("/prompt", url.Values{"session": {parent}, "message": {"/new"}, "tag": {"work"}})
	assertTags(fresh, []string{})
	assertTags(parent, []string{"work"})
	explicit := post("/sessions/new_at_cwd?tag=filter", url.Values{"cwd": {root}, "tags": {" Alpha ", "WORK", "alpha"}})
	assertTags(explicit, []string{"alpha", "work"})
	untagged := post("/sessions/new_at_cwd?tag=work&tags=query-only", url.Values{"cwd": {root}})
	assertTags(untagged, []string{})
	newFromParent := post("/sessions/new", url.Values{"session": {parent}, "tags": {"fresh"}})
	assertTags(newFromParent, []string{"fresh"})
	for _, operation := range []string{"clone", "fork", "new"} {
		pending := post("/sessions/new_at_cwd", url.Values{"cwd": {root}, "tags": {"pending"}})
		if _, err := os.Stat(pending); !os.IsNotExist(err) {
			t.Fatalf("expected a pending source: %v", err)
		}
		route := "/sessions/" + operation
		if operation == "new" {
			route = "/prompt"
		}
		child := post(route, url.Values{"session": {pending}, "entry_id": {"u1"}, "message": {"/new"}})
		expected := []string{"pending"}
		if operation == "new" {
			expected = []string{}
		}
		assertTags(child, expected)
		tags, err := sessions.NewGatewayState(cfg.ReadStatePath, cfg.PinnedSessionsPath, cfg.SessionTagsPath, sessionsRoot).SessionTags()
		if err != nil || !reflect.DeepEqual(tags[pending], []string{"pending"}) {
			t.Fatalf("branch erased source tags: %v, %v", tags, err)
		}
	}
	post("/sessions/rename", url.Values{"session": {parent}, "name": {"Renamed"}})
	assertTags(parent, []string{"work"})
	readTagResponse(t, postWorkspaceForm(handler, "/sessions/tags", url.Values{"session": {parent}, "tag": {"unique"}, "assigned": {"true"}}, ""))
	post("/sessions/delete", url.Values{"session": {parent}})
	catalog := getWorkspace(handler, "/tags", "")
	var listing struct{ Tags []sessions.TagCount }
	if catalog.Code != http.StatusOK || json.Unmarshal(catalog.Body.Bytes(), &listing) != nil {
		t.Fatalf("catalog = %d %s", catalog.Code, catalog.Body.String())
	}
	for _, tag := range listing.Tags {
		if tag.Name == "unique" {
			t.Fatal("deleted session left an unused suggestion")
		}
	}
	tags, err := sessions.NewGatewayState(cfg.ReadStatePath, cfg.PinnedSessionsPath, cfg.SessionTagsPath, sessionsRoot).SessionTags()
	if err != nil || len(tags[parent]) != 0 {
		t.Fatalf("deleted session tags = %v, %v", tags[parent], err)
	}
}
