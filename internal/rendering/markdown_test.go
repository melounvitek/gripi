package rendering

import (
	"strings"
	"testing"
)

func TestMarkdownRendersBrowserHTMLAndSanitizesUntrustedContent(t *testing.T) {
	rendered := NewMarkdown().Render("## Live\n\n<script>alert('x')</script>\n\n[safe](https://example.test) [bad](javascript:alert(1))")
	for _, expected := range []string{"<h2", ">Live</h2>", `href="https://example.test"`, `target="_blank"`, `rel="nofollow noreferrer noopener"`} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("rendered markdown does not contain %q: %s", expected, rendered)
		}
	}
	for _, unsafe := range []string{"<script", "javascript:"} {
		if strings.Contains(strings.ToLower(rendered), unsafe) {
			t.Errorf("rendered markdown contains %q: %s", unsafe, rendered)
		}
	}
}

func TestMarkdownSanitizerStripsUnsafeLinkTargets(t *testing.T) {
	rendered := NewMarkdown().Render(strings.Join([]string{
		`[mixed](JaVaScRiPt:alert(1))`,
		`[data](data:text/html;base64,PHNjcmlwdD4=)`,
		`<a href="javascript:alert(1)">raw</a>`,
		`<a href="vbscript:msgbox(1)">legacy</a>`,
	}, "\n\n"))
	lower := strings.ToLower(rendered)
	for _, unsafe := range []string{`javascript:`, `href="data:`, `href="vbscript:`} {
		if strings.Contains(lower, unsafe) {
			t.Errorf("rendered markdown contains %q: %s", unsafe, rendered)
		}
	}
}

func TestMarkdownContinuesOrderedListsAcrossCodeBlocks(t *testing.T) {
	rendered := NewMarkdown().Render("1. First\n1. Second\n\n```ruby\nputs :code\n```\n\n1. Third")
	if strings.Count(rendered, "<ol") != 2 || !strings.Contains(rendered, `<ol start="3">`) {
		t.Fatalf("ordered lists were not continued: %s", rendered)
	}
	if !strings.Contains(rendered, `<code class="highlight ruby">`) || !strings.Contains(rendered, `<span class="syntax-function">puts</span>`) {
		t.Fatalf("fenced code highlighting missing: %s", rendered)
	}
}
