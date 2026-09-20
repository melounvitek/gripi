package server

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestCompactRelativeTime(t *testing.T) {
	for _, test := range []struct {
		name string
		age  time.Duration
		want string
	}{
		{"future", -time.Minute, "now"},
		{"recent", 30 * time.Second, "now"},
		{"minute", time.Minute, "1m"},
		{"minutes", 59*time.Minute + 30*time.Second, "59m"},
		{"hour", time.Hour, "1h"},
		{"hours", 23*time.Hour + 30*time.Minute, "23h"},
		{"day", 24 * time.Hour, "1d"},
		{"days", 10 * 24 * time.Hour, "10d"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := compactRelativeTime(time.Now().Add(-test.age)); got != test.want {
				t.Errorf("compactRelativeTime = %q, want %q", got, test.want)
			}
		})
	}
	if got := compactRelativeTime(time.Time{}); got != "—" {
		t.Errorf("unknown time = %q, want —", got)
	}
}

func TestMessageTemplateCollapsesLongSingleLineToolOutput(t *testing.T) {
	message := &sessions.Message{
		Role:     "toolResult",
		Text:     "oldest-output-" + strings.Repeat("x", 2500) + "-latest-output",
		Compact:  true,
		Summary:  "bash",
		ToolName: "bash",
	}
	templates, err := template.New("").Funcs(templateFunctions(rendering.NewMarkdown())).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}

	var rendered strings.Builder
	if err := templates.ExecuteTemplate(&rendered, "message", messageData{View: &pageView{}, Message: message}); err != nil {
		t.Fatal(err)
	}

	html := rendered.String()
	fullTemplate := strings.Index(html, `<template data-tool-output-full>`)
	if fullTemplate < 0 || !strings.Contains(html, `data-tool-output-wraps="true"`) || !strings.Contains(html, `data-collapsed="true"`) || !strings.Contains(html, `>Expand</button>`) {
		t.Fatalf("long single-line output is not collapsible: %s", html)
	}
	collapsed := html[:fullTemplate]
	if strings.Contains(collapsed, "oldest-output") || !strings.Contains(collapsed, "latest-output") {
		t.Fatalf("collapsed output does not retain only the latest bounded suffix: %s", collapsed)
	}
	if !strings.Contains(html[fullTemplate:], "oldest-output") {
		t.Fatalf("expanded output does not retain the complete result: %s", html[fullTemplate:])
	}
}

func TestMessageTemplateIdentifiesPersistedPairedToolResults(t *testing.T) {
	message := &sessions.Message{
		Role:                "assistant",
		Text:                "done",
		Compact:             true,
		ToolCallID:          "bash-1",
		ToolName:            "bash",
		ToolResultPersisted: true,
	}
	templates, err := template.New("").Funcs(templateFunctions(rendering.NewMarkdown())).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}

	var rendered strings.Builder
	if err := templates.ExecuteTemplate(&rendered, "message", messageData{View: &pageView{}, Message: message}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(rendered.String(), `data-tool-call-id="bash-1" data-tool-result-persisted="true"`) {
		t.Fatalf("persisted tool identity missing: %s", rendered.String())
	}
}

func TestMessageTemplatePreservesLongDiffTail(t *testing.T) {
	lines := make([]string, 18)
	for index := range lines {
		lines[index] = "context"
	}
	lines = append(lines, "+diff-start-"+strings.Repeat("x", 2500)+"-diff-end")
	message := &sessions.Message{
		Role:           "toolResult",
		Text:           strings.Join(lines, "\n"),
		Compact:        true,
		Summary:        "edit",
		ToolName:       "edit",
		ToolTranscript: true,
	}
	templates, err := template.New("").Funcs(templateFunctions(rendering.NewMarkdown())).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}

	var rendered strings.Builder
	if err := templates.ExecuteTemplate(&rendered, "message", messageData{View: &pageView{}, Message: message}); err != nil {
		t.Fatal(err)
	}

	html := rendered.String()
	fullTemplate := strings.Index(html, `<template data-tool-output-full>`)

	if fullTemplate < 0 {
		t.Fatalf("long diff output is not collapsible: %s", html)
	}

	collapsed := html[:fullTemplate]
	if !strings.Contains(collapsed, `data-tool-output-wraps="false"`) || !strings.Contains(collapsed, `tool-diff-line--add`) || !strings.Contains(collapsed, "+diff-start-") || !strings.Contains(collapsed, "-diff-end") {
		t.Fatalf("collapsed diff does not retain its complete classified tail: %s", collapsed)
	}
}
