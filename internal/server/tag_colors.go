package server

import (
	"fmt"
	"hash/fnv"
	"html/template"
)

// Keep this tag-only palette in sync with session_tags_controller.js.
var tagColors = [][2]string{
	{"#5ff5ce1f", "#5ff5ce"}, {"#4df3e51f", "#4df3e5"}, {"#50e3ff1f", "#50e3ff"}, {"#68ceff1f", "#68ceff"},
	{"#8dbbff1f", "#8dbbff"}, {"#b1b6ff1f", "#b1b6ff"}, {"#c2adff1f", "#c2adff"}, {"#d3a2ff1f", "#d3a2ff"},
	{"#e69cff1f", "#e69cff"}, {"#f59afa1f", "#f59afa"}, {"#ff95dc1f", "#ff95dc"}, {"#ff9ecb1f", "#ff9ecb"},
}

func tagStyle(tag string) template.CSS {
	// Keep UTF-8 FNV-1a in sync with session_tags_controller.js.
	hash := fnv.New32a()
	hash.Write([]byte(tag))
	colors := tagColors[hash.Sum32()%uint32(len(tagColors))]
	return template.CSS(fmt.Sprintf("--tag-bg: %s; --tag-fg: %s", colors[0], colors[1]))
}
