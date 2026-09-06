package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/sessions"
)

func TestTagFilteringCombinesBeforePaginationAndPreservesCurrentAndPins(t *testing.T) {
	fixture := seedNativeFixture(t)
	handler := fixtureHandler(t, fixture)
	all, err := (sessions.Store{Root: fixture.sessionsRoot, Home: fixture.home, Cache: sessions.NewCache()}).Sessions()
	if err != nil {
		t.Fatal(err)
	}
	tag := "review"
	for _, session := range all {
		response := serve(t, handler, http.MethodPost, "/sessions/tags", url.Values{"session": {session.Path}, "tag": {tag}, "assigned": {"true"}}.Encode())
		if response.Code != http.StatusOK {
			t.Fatalf("assign tag: %d %s", response.Code, response.Body.String())
		}
	}
	current := all[0]
	pinned := all[1]
	response := serve(t, handler, http.MethodPost, "/sessions/pin", url.Values{"session": {pinned.Path}, "pinned": {"true"}}.Encode())
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	params := url.Values{"session": {current.Path}, "tag": {" REVIEW "}, "project": {all[len(all)-1].CWD}, "session_search": {all[len(all)-1].DisplayName}}
	response = serve(t, handler, http.MethodGet, "/sidebar?"+params.Encode(), "")
	html := response.Body.String()
	for _, expected := range []string{`data-selected-tag="review"`, `data-tag-filter-count>1</span>`, `data-session-path="` + current.Path + `"`, `data-session-path="` + pinned.Path + `"`, `data-session-path="` + all[len(all)-1].Path + `"`} {
		if !strings.Contains(html, expected) {
			t.Errorf("sidebar missing %s", expected)
		}
	}
	params.Del("project")
	params.Del("session_search")
	response = serve(t, handler, http.MethodGet, "/sidebar?"+params.Encode(), "")
	html = response.Body.String()
	list := strings.Split(strings.Split(html, `<div class="sessions-list">`)[1], `data-sidebar-load-more`)[0]
	if count := len(regexp.MustCompile(`class="session-row`).FindAllString(list, -1)); count != 20 {
		t.Fatalf("first page has %d sessions", count)
	}
	if !strings.Contains(html, "tag=review") {
		t.Fatal("navigation and pagination must retain tag")
	}
}

func TestTagsRenderOnPageAndBothFragments(t *testing.T) {
	fixture := seedNativeFixture(t)
	handler := fixtureHandler(t, fixture)
	tags := []string{"alpha", "beta", "gamma"}
	for _, tag := range tags {
		response := serve(t, handler, http.MethodPost, "/sessions/tags", url.Values{"session": {fixture.markerPath}, "tag": {tag}, "assigned": {"true"}}.Encode())
		if response.Code != http.StatusOK {
			t.Fatalf("assign tag: %d %s", response.Code, response.Body.String())
		}
	}
	params := url.Values{"session": {fixture.markerPath}, "tag": {tags[0]}}
	page := serve(t, handler, http.MethodGet, "/?"+params.Encode(), "")
	sidebar := serve(t, handler, http.MethodGet, "/sidebar?"+params.Encode(), "")
	fragment := serve(t, handler, http.MethodGet, "/session_fragment?"+params.Encode(), "")
	var payload map[string]string
	if err := json.Unmarshal(fragment.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	fragmentURL, err := url.Parse(payload["url"])
	if err != nil || fragmentURL.Query().Get("tag") != tags[0] {
		t.Fatalf("fragment navigation lost tag: %q (%v)", payload["url"], err)
	}
	for name, html := range map[string]string{"page": page.Body.String(), "sidebar": sidebar.Body.String(), "fragment sidebar": payload["sidebar_html"], "conversation": payload["conversation_html"]} {
		if !strings.Contains(html, `data-tag-filter="`+tags[0]+`"`) {
			t.Errorf("%s missing filter chip", name)
		}
		if !strings.Contains(html, `data-tag-edit="`+fixture.markerPath+`"`) {
			t.Errorf("%s missing tag editor", name)
		}
	}
	if !strings.Contains(payload["conversation_html"], `data-tag-filter="`+tags[2]+`"`) {
		t.Fatal("header must show all tags")
	}
	if !strings.Contains(sidebar.Body.String(), ">+1</button>") {
		t.Fatal("sidebar must expose overflowing tags")
	}
}
