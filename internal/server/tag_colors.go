package server

import (
	"fmt"
	"html/template"

	"github.com/melounvitek/gripi/internal/sessions"
)

// Colors come from validated gateway metadata, never from a tag name or URL.
func tagStyle(color string) template.CSS {
	if color == "" {
		color = "#a0a0a0"
	}
	return template.CSS(fmt.Sprintf("--tag-bg: %s1f; --tag-fg: %s", color, color))
}

func (app *application) visibleTagColors(tags []sessions.TagCount) (map[string]string, error) {
	stored, err := app.gatewayState.TagColors()
	if err != nil {
		return nil, err
	}
	colors := make(map[string]string, len(tags))
	for _, tag := range tags {
		colors[tag.Name] = stored[tag.Name]
	}
	return colors, nil
}
