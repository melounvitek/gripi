package rendering

import (
	"bytes"
	stdhtml "html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	goldhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var unsafeMarkdownLink = regexp.MustCompile(`(?i)\]\(\s*javascript:[^)]*\)`)
var safeLanguageClass = regexp.MustCompile(`^[A-Za-z0-9_.+#-]+$`)

var languageAliases = map[string]string{"bash": "shell", "sh": "shell", "shell": "shell", "zsh": "shell", "js": "javascript", "javascript": "javascript", "ts": "javascript", "typescript": "javascript", "json": "json", "rb": "ruby", "ruby": "ruby"}

type highlightPattern struct {
	class      string
	expression *regexp.Regexp
}

var highlightPatterns = map[string][]highlightPattern{
	"javascript": {{"comment", regexp.MustCompile(`^(?s://[^\n]*|/\*.*?\*/)`)}, {"string", regexp.MustCompile("^(?:`(?:\\\\.|[^`])*`|\"(?:\\\\.|[^\"])*\"|'(?:\\\\.|[^'])*')")}, {"number", regexp.MustCompile(`(?i)^\b(?:0x[\da-f]+|\d+(?:\.\d+)?)\b`)}, {"keyword", regexp.MustCompile(`^\b(?:async|await|break|case|catch|class|const|continue|default|delete|do|else|export|extends|finally|for|from|function|if|import|in|instanceof|let|new|of|return|switch|throw|try|typeof|var|void|while|yield)\b`)}, {"literal", regexp.MustCompile(`^\b(?:false|null|true|undefined)\b`)}},
	"json":       {{"string", regexp.MustCompile(`^"(?:\\.|[^"])*"`)}, {"number", regexp.MustCompile(`(?i)^-?\b\d+(?:\.\d+)?(?:e[+-]?\d+)?\b`)}, {"literal", regexp.MustCompile(`^\b(?:false|null|true)\b`)}},
	"ruby":       {{"comment", regexp.MustCompile(`^#[^\n]*`)}, {"string", regexp.MustCompile(`^(?:"(?:\\.|[^"])*"|'(?:\\.|[^'])*')`)}, {"symbol", regexp.MustCompile(`^:\w+[!?=]?`)}, {"number", regexp.MustCompile(`^\b\d+(?:\.\d+)?\b`)}, {"keyword", regexp.MustCompile(`^\b(?:alias|and|begin|break|case|class|def|defined\?|do|else|elsif|end|ensure|false|for|if|in|module|next|nil|not|or|redo|rescue|retry|return|self|super|then|true|undef|unless|until|when|while|yield)\b`)}, {"function", regexp.MustCompile(`^\b(?:puts|print|p|require|require_relative|attr_reader|attr_writer|attr_accessor)\b`)}},
	"shell":      {{"comment", regexp.MustCompile(`^#[^\n]*`)}, {"string", regexp.MustCompile(`^(?:"(?:\\.|[^"])*"|'(?:\\.|[^'])*')`)}, {"variable", regexp.MustCompile(`^\$\{?\w+\}?`)}, {"keyword", regexp.MustCompile(`^\b(?:case|do|done|elif|else|esac|fi|for|function|if|in|then|until|while)\b`)}, {"function", regexp.MustCompile(`^\b(?:awk|bundle|cd|cp|curl|echo|find|git|grep|mkdir|npm|rg|ruby|sed|yarn)\b`)}},
}

type Markdown struct {
	engine goldmark.Markdown
	policy *bluemonday.Policy
}

func NewMarkdown() *Markdown {
	policy := bluemonday.UGCPolicy()
	policy.AllowAttrs("class").Matching(bluemonday.SpaceSeparatedTokens).OnElements("code", "span")
	policy.AllowAttrs("start").Matching(bluemonday.Integer).OnElements("ol")
	policy.AllowAttrs("target", "rel").OnElements("a")
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	policy.AddTargetBlankToFullyQualifiedLinks(true)
	return &Markdown{
		engine: goldmark.New(
			goldmark.WithExtensions(extension.GFM),
			goldmark.WithParserOptions(parser.WithAutoHeadingID()),
			goldmark.WithRendererOptions(goldhtml.WithHardWraps(), goldhtml.WithUnsafe(), renderer.WithNodeRenderers(util.Prioritized(fencedCodeRenderer{}, 100))),
		),
		policy: policy,
	}
}

func (markdown *Markdown) Render(source string) string {
	var rendered bytes.Buffer
	source = unsafeMarkdownLink.ReplaceAllString(source, "]()")
	if err := markdown.engine.Convert([]byte(source), &rendered); err != nil {
		return ""
	}
	return markdown.policy.Sanitize(continueOrderedLists(rendered.String()))
}

type fencedCodeRenderer struct{}

func (fencedCodeRenderer) RegisterFuncs(registry renderer.NodeRendererFuncRegisterer) {
	registry.Register(ast.KindFencedCodeBlock, renderFencedCode)
}
func renderFencedCode(writer util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	block := node.(*ast.FencedCodeBlock)
	language := strings.TrimSpace(string(block.Language(source)))
	var code strings.Builder
	for index := 0; index < block.Lines().Len(); index++ {
		line := block.Lines().At(index)
		code.Write(line.Value(source))
	}
	if normalized := languageAliases[strings.ToLower(language)]; normalized != "" {
		_, _ = writer.WriteString(`<pre><code class="highlight ` + normalized + `">` + highlightCode(code.String(), normalized) + "</code></pre>\n")
	} else {
		class := ""
		if safeLanguageClass.MatchString(language) {
			class = ` class="` + stdhtml.EscapeString(language) + `"`
		}
		_, _ = writer.WriteString("<pre><code" + class + ">" + stdhtml.EscapeString(code.String()) + "</code></pre>\n")
	}
	return ast.WalkSkipChildren, nil
}
func highlightCode(code, language string) string {
	var output strings.Builder
	for code != "" {
		matched := false
		for _, pattern := range highlightPatterns[language] {
			value := pattern.expression.FindString(code)
			if value == "" {
				continue
			}
			output.WriteString(`<span class="syntax-` + pattern.class + `">` + stdhtml.EscapeString(value) + `</span>`)
			code = code[len(value):]
			matched = true
			break
		}
		if matched {
			continue
		}
		_, size := utf8.DecodeRuneInString(code)
		output.WriteString(stdhtml.EscapeString(code[:size]))
		code = code[size:]
	}
	return output.String()
}

func continueOrderedLists(fragment string) string {
	context := &nethtml.Node{Type: nethtml.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := nethtml.ParseFragment(strings.NewReader(fragment), context)
	if err != nil {
		return fragment
	}
	nextStart := 0
	for _, node := range nodes {
		if node.Type == nethtml.TextNode && strings.TrimSpace(node.Data) == "" {
			continue
		}
		if node.Type == nethtml.ElementNode && node.DataAtom == atom.Pre {
			continue
		}
		if node.Type != nethtml.ElementNode || node.DataAtom != atom.Ol {
			nextStart = 0
			continue
		}
		count := 0
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == nethtml.ElementNode && child.DataAtom == atom.Li {
				count++
			}
		}
		if nextStart > 0 {
			node.Attr = setAttribute(node.Attr, "start", strconv.Itoa(nextStart))
		}
		if nextStart == 0 {
			nextStart = 1
		}
		nextStart += count
	}
	var output strings.Builder
	for _, node := range nodes {
		if nethtml.Render(&output, node) != nil {
			return fragment
		}
	}
	return output.String()
}

func setAttribute(attributes []nethtml.Attribute, key, value string) []nethtml.Attribute {
	for index := range attributes {
		if attributes[index].Key == key {
			attributes[index].Val = value
			return attributes
		}
	}
	return append(attributes, nethtml.Attribute{Key: key, Val: value})
}
