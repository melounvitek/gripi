package sessionfile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ToolPaths returns the paths that the tool calls of a saved session named: those of read, edit, write and the other
// tools that take a path, and in shell commands the folders of cd and -C and every absolute path. Relative paths
// resolve against cwd, and those after a cd against its folder; ~ is home. A session that isn't saved named none.
func (store Store) ToolPaths(key, cwd, home string) ([]string, error) {
	file, err := os.Open(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var paths []string
	// add adds a path, resolved in the folder dir, and returns it.
	add := func(dir, path string) string {
		switch {
		case path == "~" || strings.HasPrefix(path, "~/"):
			path = home + path[1:]
		case !filepath.IsAbs(path):
			path = filepath.Join(dir, path)
		}
		if path = filepath.Clean(path); !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
		return path
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"toolCall"`)) {
			var entry struct {
				Message struct {
					Role    string `json:"role"`
					Content []struct {
						Type      string `json:"type"`
						Name      string `json:"name"`
						Arguments struct {
							Path    string `json:"path"`
							Command string `json:"command"`
						} `json:"arguments"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &entry) == nil && entry.Message.Role == "assistant" {
				for _, part := range entry.Message.Content {
					if part.Type != "toolCall" {
						continue
					}
					if part.Arguments.Path != "" {
						add(cwd, part.Arguments.Path)
					}
					if part.Name == "bash" {
						commandPaths(part.Arguments.Command, cwd, add)
					}
				}
			}
		}
		if err != nil {
			return paths, nil
		}
	}
}

// commandPaths passes the paths that a shell command names to add, with the folder that relative ones are in.
func commandPaths(command, cwd string, add func(dir, path string) string) {
	words := shellWords(command)
	dir := cwd
	for i, word := range words {
		next := ""
		if i+1 < len(words) && !strings.HasPrefix(words[i+1], "-") {
			next = words[i+1]
		}
		switch {
		case (word == "cd" || word == "pushd") && next != "":
			dir = add(dir, next)
		case word == "-C" && next != "":
			add(dir, next)
		case len(word) > 1 && (word[0] == '/' || strings.HasPrefix(word, "~/")):
			add(dir, word)
		default:
			if _, value, found := strings.Cut(word, "="); found && len(value) > 1 && value[0] == '/' {
				add(dir, value)
			}
		}
	}
}

// shellWords splits a shell command into its words, without quotes, and its operators, roughly as sh does. A ~ in
// quotes stays literal, so such a word starts with a NUL instead, which no path does.
func shellWords(command string) []string {
	var words []string
	var word strings.Builder
	inWord := false
	end := func() {
		if inWord {
			words = append(words, word.String())
		}
		word.Reset()
		inWord = false
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case c == '\'' || c == '"':
			closing := strings.IndexByte(command[i+1:], c)
			if closing < 0 {
				closing = len(command) - i - 1
			}
			quoted := command[i+1 : i+1+closing]
			if !inWord && strings.HasPrefix(quoted, "~") {
				word.WriteByte(0)
			}
			word.WriteString(quoted)
			inWord = true
			i += closing + 1
		case c == '\\' && i+1 < len(command):
			word.WriteByte(command[i+1])
			inWord = true
			i++
		case strings.IndexByte(" \t\n;&|<>()", c) >= 0:
			end()
			if c != ' ' && c != '\t' && c != '\n' {
				words = append(words, string(c))
			}
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	end()
	return words
}
