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

func TestMarkdownDoesNotApplyApplicationClasses(t *testing.T) {
	for _, source := range []string{
		`<span class="modal-overlay">Content</span>`,
		`<code class="image-viewer">Content</code>`,
		`<span class="syntax-string modal-overlay">Content</span>`,
		`<span class="modal-overlay syntax-string">Content</span>`,
		`<code class="highlight ruby image-viewer">Content</code>`,
		"```modal-overlay\nContent\n```",
		"```image-viewer\nContent\n```",
	} {
		t.Run(source, func(t *testing.T) {
			rendered := NewMarkdown().Render(source)
			if !strings.Contains(rendered, "Content") {
				t.Fatalf("content was lost: %s", rendered)
			}
			for _, unsafe := range []string{"modal-overlay", "image-viewer"} {
				if strings.Contains(rendered, unsafe) {
					t.Errorf("rendered markdown applies application class %q: %s", unsafe, rendered)
				}
			}
		})
	}
}

func TestMarkdownPreservesSupportedHighlighting(t *testing.T) {
	for language, normalized := range map[string]string{
		"bash": "shell", "sh": "shell", "shell": "shell", "zsh": "shell",
		"js": "javascript", "javascript": "javascript", "ts": "javascript", "typescript": "javascript",
		"json": "json", "rb": "ruby", "ruby": "ruby",
	} {
		t.Run(language, func(t *testing.T) {
			rendered := NewMarkdown().Render("```" + language + "\n\"Content\"\n```")
			if !strings.Contains(rendered, `<code class="highlight `+normalized+`">`) || !strings.Contains(rendered, `<span class="syntax-string">`) {
				t.Fatalf("fenced code highlighting missing: %s", rendered)
			}
		})
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
