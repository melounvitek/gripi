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
	// These UTF-8 FNV-1a fixtures cover every tag palette entry.
	colors := map[string][2]string{
		"color-7": {"#5ff5ce1f", "#5ff5ce"}, "color-8": {"#4df3e51f", "#4df3e5"},
		"color-9": {"#50e3ff1f", "#50e3ff"}, "color-35": {"#68ceff1f", "#68ceff"},
		"color-34": {"#8dbbff1f", "#8dbbff"}, "color-0": {"#b1b6ff1f", "#b1b6ff"},
		"color-1": {"#c2adff1f", "#c2adff"}, "color-é": {"#d3a2ff1f", "#d3a2ff"},
		"color-3": {"#e69cff1f", "#e69cff"}, "color-東京": {"#f59afa1f", "#f59afa"},
		"color-5": {"#ff95dc1f", "#ff95dc"}, "color-🧪": {"#ff9ecb1f", "#ff9ecb"},
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
}
