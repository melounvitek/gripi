package server

import (
	"html/template"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/rendering"
	"github.com/melounvitek/gripi/internal/sessions"
)

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
	if fullTemplate < 0 || !strings.Contains(html, `data-collapsed="true"`) || !strings.Contains(html, `>Expand</button>`) {
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
