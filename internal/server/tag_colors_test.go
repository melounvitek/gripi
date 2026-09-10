package server_test

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestTagColorsRenderAcrossPageAndFragments(t *testing.T) {
	fixture := seedNativeFixture(t)
	handler := fixtureHandler(t, fixture)
	colors := make(map[string][2]string)
	for _, tag := range []string{"color-0", "color-1", "color-é", "color-東京", "color-🧪", "__proto__", "constructor"} {
		response := serve(t, handler, http.MethodPost, "/sessions/tags", url.Values{"session": {fixture.markerPath}, "tag": {tag}, "assigned": {"true"}}.Encode())
		result := readTagResponse(t, response)
		color := result.TagColors[tag]
		if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(color) {
			t.Fatalf("missing saved color for %q: %s", tag, response.Body.String())
		}
		colors[tag] = [2]string{color + "1f", color}
	}
	if colors["color-0"][1] != "#e69cff" || colors["color-1"][1] != "#5ff5ce" {
		t.Fatalf("first colors should be purple and mint: %v", colors)
	}
	handler = fixtureHandler(t, fixture)
	params := url.Values{"session": {fixture.markerPath}, "tag": {"color-🧪"}}
	page := serve(t, handler, http.MethodGet, "/?"+params.Encode(), "")
	sidebar := serve(t, handler, http.MethodGet, "/sidebar?"+params.Encode(), "")
	fragment := serve(t, handler, http.MethodGet, "/session_fragment?"+params.Encode(), "")
	var payload map[string]string
	if err := json.Unmarshal(fragment.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	assertColors := func(t *testing.T, markup, selector, tag string, expected ...string) {
		t.Helper()
		elements := regexp.MustCompile(`<[^>]+`+selector+`[^>]*>`).FindAllString(markup, -1)
		if len(elements) == 0 {
			t.Fatalf("missing %s", selector)
		}
		for _, element := range elements {
			for _, color := range expected {
				if !strings.Contains(element, color) {
					t.Errorf("tag %q missing color %s: %s", tag, color, element)
				}
			}
		}
	}
	for name, markup := range map[string]string{"page": page.Body.String(), "conversation": payload["conversation_html"]} {
		t.Run(name, func(t *testing.T) {
			header := regexp.MustCompile(`<[^>]+class="header-tags session-tags"[^>]*>`).FindString(markup)
			match := regexp.MustCompile(`data-tag-colors="([^"]*)"`).FindStringSubmatch(header)
			if len(match) != 2 {
				t.Fatal("header missing tag color map")
			}
			var saved map[string]string
			if err := json.Unmarshal([]byte(html.UnescapeString(match[1])), &saved); err != nil {
				t.Fatal(err)
			}
			for tag, color := range colors {
				if saved[tag] != color[1] {
					t.Errorf("header color for %q = %q, want %q", tag, saved[tag], color[1])
				}
			}
			for _, tag := range []string{"__proto__", "color-0"} {
				assertColors(t, markup, `data-tag-filter="`+tag+`"`, tag, colors[tag][1])
			}
		})
	}
	for _, markup := range []string{page.Body.String(), sidebar.Body.String(), payload["sidebar_html"]} {
		for _, tag := range []string{"__proto__", "color-0"} {
			assertColors(t, markup, `data-tag-filter="`+tag+`"`, tag, colors[tag][1])
		}
		assertColors(t, markup, `class="compact-tag-filter is-active"`, "color-🧪", colors["color-🧪"][0], colors["color-🧪"][1])
	}
}
