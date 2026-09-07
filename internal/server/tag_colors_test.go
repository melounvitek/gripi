package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestTagColorsRenderAcrossPageAndFragments(t *testing.T) {
	fixture := seedNativeFixture(t)
	handler := fixtureHandler(t, fixture)
	// These UTF-8 FNV-1a fixtures cover every project palette entry.
	colors := map[string][2]string{
		"color-7": {"#6a3b1d33", "#e6a66f"}, "color-8": {"#334f7833", "#8db9ef"},
		"color-9": {"#563a7033", "#c5a0e8"}, "color-35": {"#70374633", "#ef9aae"},
		"color-34": {"#4b612b33", "#acd276"}, "color-0": {"#285d7033", "#75c5df"},
		"color-1": {"#67365f33", "#dfa0d4"}, "color-é": {"#66502033", "#e0bd65"},
		"color-3": {"#315d3b33", "#86cb98"}, "color-東京": {"#3f477533", "#a5afe9"},
		"color-5": {"#713f3233", "#eda18b"}, "color-🧪": {"#215f5933", "#76cbbf"},
	}
	for tag := range colors {
		response := serve(t, handler, http.MethodPost, "/sessions/tags", url.Values{"session": {fixture.markerPath}, "tag": {tag}, "assigned": {"true"}}.Encode())
		if response.Code != http.StatusOK {
			t.Fatalf("assign tag: %d %s", response.Code, response.Body.String())
		}
	}
	params := url.Values{"session": {fixture.markerPath}, "tag": {"color-🧪"}}
	page := serve(t, handler, http.MethodGet, "/?"+params.Encode(), "")
	sidebar := serve(t, handler, http.MethodGet, "/sidebar?"+params.Encode(), "")
	fragment := serve(t, handler, http.MethodGet, "/session_fragment?"+params.Encode(), "")
	var payload map[string]string
	if err := json.Unmarshal(fragment.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	assertColors := func(t *testing.T, markup, selector, tag string) {
		t.Helper()
		elements := regexp.MustCompile(`<[^>]+`+selector+`[^>]*>`).FindAllString(markup, -1)
		if len(elements) == 0 {
			t.Fatalf("missing %s", selector)
		}
		for _, element := range elements {
			for _, color := range colors[tag] {
				if !strings.Contains(element, color) {
					t.Errorf("tag %q missing color %s: %s", tag, color, element)
				}
			}
		}
	}
	for name, markup := range map[string]string{"page": page.Body.String(), "conversation": payload["conversation_html"]} {
		t.Run(name, func(t *testing.T) {
			for tag := range colors {
				assertColors(t, markup, `data-tag-filter="`+tag+`"`, tag)
			}
		})
	}
	for _, markup := range []string{page.Body.String(), sidebar.Body.String(), payload["sidebar_html"]} {
		for _, tag := range []string{"color-0", "color-1"} {
			assertColors(t, markup, `data-tag-filter="`+tag+`"`, tag)
		}
		assertColors(t, markup, `class="compact-tag-filter is-active"`, "color-🧪")
	}
	assertColors(t, page.Body.String(), `data-tag-draft-remove="color-🧪"`, "color-🧪")
}
