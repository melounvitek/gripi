package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/sessions"
)

type tagResponse struct {
	Session       string              `json:"session"`
	Tags          []string            `json:"tags"`
	AvailableTags []sessions.TagCount `json:"available_tags"`
}

func readTagResponse(t *testing.T, response *httptest.ResponseRecorder) tagResponse {
	t.Helper()
	var result tagResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("tag response = %d %s", response.Code, response.Body.String())
	}
	if result.Tags == nil || result.AvailableTags == nil {
		t.Fatalf("null tag arrays: %s", response.Body.String())
	}
	return result
}

func TestSessionTagsPersistReuseRemoveWithoutChangingPiFiles(t *testing.T) {
	fixture := seedNativeFixture(t)
	cfg := multiUserConfig(fixture.root)
	cfg.MultiUserMode = false
	cfg.SessionsRoot = fixture.sessionsRoot
	handler := multiUserHandler(t, cfg)
	all, err := (sessions.Store{Root: cfg.SessionsRoot, Cache: sessions.NewCache()}).Sessions()
	if err != nil || len(all) < 2 {
		t.Fatalf("sessions = %v, %v", all, err)
	}
	first, second := all[0].Path, all[1].Path
	before := snapshotJSONL(t, cfg.SessionsRoot)
	for _, path := range []string{first, first, second} {
		result := readTagResponse(t, postWorkspaceForm(handler, "/sessions/tags", url.Values{"session": {path}, "tag": {"  WoRK  "}, "assigned": {"true"}}, ""))
		if result.Session != path || !reflect.DeepEqual(result.Tags, []string{"work"}) {
			t.Fatalf("assignment = %+v", result)
		}
	}
	result := readTagResponse(t, postWorkspaceForm(handler, "/sessions/tags", url.Values{"session": {first}, "tag": {"Alpha"}, "assigned": {"true"}}, ""))
	expected := []sessions.TagCount{{Name: "alpha", Count: 1}, {Name: "work", Count: 2}}
	if !reflect.DeepEqual(result.Tags, []string{"alpha", "work"}) || !reflect.DeepEqual(result.AvailableTags, expected) {
		t.Fatalf("tags = %+v", result)
	}
	handler = multiUserHandler(t, cfg)
	result = readTagResponse(t, getWorkspace(handler, "/sessions/tags?session="+url.QueryEscape(first), ""))
	if !reflect.DeepEqual(result.AvailableTags, expected) {
		t.Fatalf("recreated handler = %+v", result)
	}
	catalog := getWorkspace(handler, "/tags?project=missing&session_search=missing&tag=missing", "")
	var listing struct {
		Tags []sessions.TagCount `json:"tags"`
	}
	if catalog.Code != http.StatusOK || json.Unmarshal(catalog.Body.Bytes(), &listing) != nil || !reflect.DeepEqual(listing.Tags, expected) {
		t.Fatalf("catalog = %d %s", catalog.Code, catalog.Body.String())
	}
	for _, path := range []string{first, first, second} {
		readTagResponse(t, postWorkspaceForm(handler, "/sessions/tags", url.Values{"session": {path}, "tag": {"WORK"}, "assigned": {"false"}}, ""))
	}
	result = readTagResponse(t, getWorkspace(handler, "/sessions/tags?session="+url.QueryEscape(second), ""))
	if len(result.Tags) != 0 || !reflect.DeepEqual(result.AvailableTags, expected[:1]) {
		t.Fatalf("removed = %+v", result)
	}
	assertJSONLSnapshotUnchanged(t, before, snapshotJSONL(t, cfg.SessionsRoot))
	if _, err := os.Stat(cfg.ReadStatePath); !os.IsNotExist(err) {
		t.Fatalf("tag requests observed read state: %v", err)
	}
}

func TestSessionTagsRejectInvalidRequestsAndPreserveMalformedState(t *testing.T) {
	fixture := seedNativeFixture(t)
	cfg := multiUserConfig(fixture.root)
	cfg.MultiUserMode = false
	cfg.SessionsRoot = fixture.sessionsRoot
	handler := multiUserHandler(t, cfg)
	valid := url.Values{"session": {fixture.markerPath}, "tag": {"work"}, "assigned": {"true"}}
	for _, path := range []string{"", "relative", "/missing.jsonl", filepath.Join(cfg.SessionsRoot, "missing.jsonl"), fixture.markerPath + "/../contract.jsonl", fixture.markerPath + "\x00"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			form := url.Values{"session": {path}, "tag": {"work"}, "assigned": {"true"}}
			var response *httptest.ResponseRecorder
			if method == http.MethodGet {
				response = getWorkspace(handler, "/sessions/tags?"+form.Encode(), "")
			} else {
				response = postWorkspaceForm(handler, "/sessions/tags", form, "")
			}
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s %q = %d %s", method, path, response.Code, response.Body.String())
			}
		}
	}
	for _, name := range []string{"", "  ", "a\nb", "\twork", "a\x7fb", strings.Repeat("界", 65)} {
		form := url.Values{"session": {fixture.markerPath}, "tag": {name}, "assigned": {"true"}}
		if response := postWorkspaceForm(handler, "/sessions/tags", form, ""); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid tag %q = %d", name, response.Code)
		}
	}
	for _, assigned := range []string{"", "1", "TRUE", "no"} {
		form := url.Values{"session": {fixture.markerPath}, "tag": {"work"}, "assigned": {assigned}}
		if response := postWorkspaceForm(handler, "/sessions/tags", form, ""); response.Code != http.StatusBadRequest {
			t.Fatalf("assigned %q = %d", assigned, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://app.test/sessions/tags", strings.NewReader(valid.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://evil.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "http://app.test/sessions/tags", strings.NewReader(valid.Encode()))
	request.ContentLength = maxRequestBodyBytes + 1
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized = %d", response.Code)
	}
	var tooManyTags []string
	for i := range 33 {
		tooManyTags = append(tooManyTags, fmt.Sprintf("tag-%02d", i))
	}
	overLimitState, err := json.Marshal(map[string][]string{fixture.markerPath: tooManyTags})
	if err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []string{`{"broken":`, `{"broken":[42]}`, `{"broken":[" "]}`, `{} {}`, string(overLimitState)} {
		if err := os.WriteFile(cfg.SessionTagsPath, []byte(malformed), 0600); err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{"/tags", "/sessions/tags?session=" + url.QueryEscape(fixture.markerPath)} {
			response := getWorkspace(handler, target, "")
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("malformed GET = %d %s", response.Code, response.Body.String())
			}
		}
		response := postWorkspaceForm(handler, "/sessions/tags", valid, "")
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("malformed POST = %d %s", response.Code, response.Body.String())
		}
		contents, err := os.ReadFile(cfg.SessionTagsPath)
		if err != nil || string(contents) != malformed {
			t.Fatalf("malformed state changed: %q %v", contents, err)
		}
	}
}

func TestSessionTagAssignmentsAreIndependentUnderConcurrentRequests(t *testing.T) {
	fixture := seedNativeFixture(t)
	cfg := multiUserConfig(fixture.root)
	cfg.MultiUserMode = false
	cfg.SessionsRoot = fixture.sessionsRoot
	handler := multiUserHandler(t, cfg)
	all, err := (sessions.Store{Root: cfg.SessionsRoot, Cache: sessions.NewCache()}).Sessions()
	if err != nil || len(all) < 2 {
		t.Fatalf("sessions = %v, %v", all, err)
	}
	var workers sync.WaitGroup
	for i := range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			response := postWorkspaceForm(handler, "/sessions/tags", url.Values{"session": {all[i%2].Path}, "tag": {fmt.Sprintf("tag-%02d", i)}, "assigned": {"true"}}, "")
			if response.Code != http.StatusOK {
				t.Errorf("assignment = %d %s", response.Code, response.Body.String())
			}
		}()
	}
	workers.Wait()
	for i, session := range all[:2] {
		var expected []string
		for j := i; j < 32; j += 2 {
			expected = append(expected, fmt.Sprintf("tag-%02d", j))
		}
		result := readTagResponse(t, getWorkspace(handler, "/sessions/tags?session="+url.QueryEscape(session.Path), ""))
		if !reflect.DeepEqual(result.Tags, expected) {
			t.Fatalf("lost assignments: %+v", result)
		}
	}
}

func TestSessionTagLimitReturnsBadRequestWithoutChangingAssignments(t *testing.T) {
	fixture := seedNativeFixture(t)
	cfg := multiUserConfig(fixture.root)
	cfg.MultiUserMode = false
	cfg.SessionsRoot = fixture.sessionsRoot
	handler := multiUserHandler(t, cfg)
	var expected []string
	for i := range 32 {
		name := fmt.Sprintf("tag-%02d", i)
		expected = append(expected, name)
		readTagResponse(t, postWorkspaceForm(handler, "/sessions/tags", url.Values{"session": {fixture.markerPath}, "tag": {name}, "assigned": {"true"}}, ""))
	}
	response := postWorkspaceForm(handler, "/sessions/tags", url.Values{"session": {fixture.markerPath}, "tag": {"extra"}, "assigned": {"true"}}, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("33rd tag = %d %s", response.Code, response.Body.String())
	}
	result := readTagResponse(t, getWorkspace(handler, "/sessions/tags?session="+url.QueryEscape(fixture.markerPath), ""))
	if !reflect.DeepEqual(result.Tags, expected) {
		t.Fatalf("assignments changed: %+v", result)
	}
}

func TestMultiUserSessionTagCatalogIsIsolated(t *testing.T) {
	fixture := seedNativeFixture(t)
	cfg := multiUserConfig(fixture.root)
	cfg.SessionsRoot = fixture.sessionsRoot
	handler := multiUserHandler(t, cfg)
	all, err := (sessions.Store{Root: cfg.SessionsRoot, Cache: sessions.NewCache()}).Sessions()
	if err != nil || len(all) < 2 {
		t.Fatalf("sessions = %v, %v", all, err)
	}
	owners := access.NewWorkspaceOwnershipStore(cfg.WorkspaceOwnershipPath, cfg.SessionsRoot)
	for i, workspace := range []string{"workspace-a", "workspace-b"} {
		if err := access.NewWorkspaceStore(cfg.WorkspaceAccessPath).ApproveWorkspace(workspace); err != nil {
			t.Fatal(err)
		}
		if _, err := owners.Claim(all[i].Path, workspace); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"shared", workspace} {
			readTagResponse(t, postWorkspaceForm(handler, "/sessions/tags", url.Values{"session": {all[i].Path}, "tag": {name}, "assigned": {"true"}}, "gripi_workspace="+workspace))
		}
	}
	for i, workspace := range []string{"workspace-a", "workspace-b"} {
		cookie := "gripi_workspace=" + workspace
		expected := []sessions.TagCount{{Name: "shared", Count: 1}, {Name: workspace, Count: 1}}
		result := readTagResponse(t, getWorkspace(handler, "/sessions/tags?session="+url.QueryEscape(all[i].Path), cookie))
		if !reflect.DeepEqual(result.AvailableTags, expected) {
			t.Fatalf("visible tags = %+v", result)
		}
		var catalog struct {
			Tags []sessions.TagCount `json:"tags"`
		}
		response := getWorkspace(handler, "/tags", cookie)
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &catalog) != nil || !reflect.DeepEqual(catalog.Tags, expected) {
			t.Fatalf("isolated catalog = %d %s", response.Code, response.Body.String())
		}
		other := all[1-i].Path
		if response := getWorkspace(handler, "/sessions/tags?session="+url.QueryEscape(other), cookie); response.Code != http.StatusNotFound {
			t.Fatalf("other GET = %d", response.Code)
		}
		if response := postWorkspaceForm(handler, "/sessions/tags", url.Values{"session": {other}, "tag": {"secret"}, "assigned": {"true"}}, cookie); response.Code != http.StatusNotFound {
			t.Fatalf("other POST = %d", response.Code)
		}
	}
}
