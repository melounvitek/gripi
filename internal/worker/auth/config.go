package auth

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
)

// PiCommand returns the words of Gripi's Pi command that run Pi, without the options at its end, such as --approve:
// pi, or Node and Pi's script.
func PiCommand(command []string) []string {
	if option := slices.IndexFunc(command[1:], func(word string) bool { return strings.HasPrefix(word, "-") }); option >= 0 {
		return command[:option+1]
	}
	return command
}

var (
	jsonComment   = regexp.MustCompile(`"(?:\\.|[^"\\])*"|//[^\n]*`)
	trailingComma = regexp.MustCompile(`"(?:\\.|[^"\\])*"|,\s*[}\]]`)
)

// PlainJSON returns one of Pi's JSON configuration files, such as models.json, as plain JSON, without what Pi also
// allows there: a byte order mark, // comments and trailing commas.
func PlainJSON(data []byte) []byte {
	data = jsonComment.ReplaceAllFunc(bytes.TrimPrefix(data, byteOrderMark), func(match []byte) []byte {
		if match[0] == '"' {
			return match
		}
		return nil
	})
	return trailingComma.ReplaceAllFunc(data, func(match []byte) []byte {
		if match[0] == '"' {
			return match
		}
		return match[1:]
	})
}
