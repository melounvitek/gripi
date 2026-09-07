package server

import (
	"fmt"
	"hash/fnv"
	"html/template"
)

func tagStyle(tag string) template.CSS {
	// Keep UTF-8 FNV-1a in sync with session_tags_controller.js.
	hash := fnv.New32a()
	hash.Write([]byte(tag))
	colors := projectColors[hash.Sum32()%uint32(len(projectColors))]
	return template.CSS(fmt.Sprintf("--tag-bg: %s; --tag-fg: %s", colors[0], colors[1]))
}
