package sessions

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const MaxSuggestionQueryBytes = 1024

type Suggestion struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
}

func SuggestPaths(cwd, home, mode, query string) []Suggestion {
	if len(query) > MaxSuggestionQueryBytes || strings.ContainsRune(query, 0) {
		return nil
	}
	stat, err := os.Stat(cwd)
	if err != nil || !stat.IsDir() {
		return nil
	}
	if mode == "path" {
		return pathSuggestions(cwd, home, query)
	}
	if mode == "fuzzy" {
		return fuzzySuggestions(cwd, home, query)
	}
	return nil
}

func pathSuggestions(cwd, home, query string) []Suggestion {
	displayBase, prefix := "", query
	if query == "~" {
		displayBase, prefix = "~/", ""
	} else if query == "" || strings.HasSuffix(query, "/") {
		displayBase, prefix = query, ""
	} else if slash := strings.LastIndex(query, "/"); slash >= 0 {
		displayBase, prefix = query[:slash+1], query[slash+1:]
	}
	searchDir := expandSuggestionPath(cwd, home, displayBase)
	directoryFile, err := os.Open(searchDir)
	if err != nil {
		return nil
	}
	defer directoryFile.Close()
	result := make([]Suggestion, 0, 100)
	for {
		entries, readErr := directoryFile.ReadDir(128)
		for _, entry := range entries {
			if !strings.HasPrefix(strings.ToLower(entry.Name()), strings.ToLower(prefix)) {
				continue
			}
			directory := entry.IsDir()
			path := displayBase + entry.Name()
			if directory {
				path += "/"
			}
			result = append(result, Suggestion{Path: path, Directory: directory})
		}
		if len(result) > 100 {
			sortSuggestions(result)
			result = result[:100]
		}
		if readErr != nil {
			break
		}
	}
	sortSuggestions(result)
	return result
}

func sortSuggestions(values []Suggestion) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Directory != values[j].Directory {
			return values[i].Directory
		}
		return strings.ToLower(values[i].Path) < strings.ToLower(values[j].Path)
	})
}

func fuzzySuggestions(cwd, home, query string) []Suggestion {
	displayBase, needle := "", query
	if slash := strings.LastIndex(query, "/"); slash >= 0 {
		displayBase, needle = query[:slash+1], query[slash+1:]
	}
	base := expandSuggestionPath(cwd, home, displayBase)
	fd := findFD(home)
	if fd == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	arguments := []string{"--base-directory", base, "--max-results", "100", "--type", "f", "--type", "d", "--follow", "--hidden", "--exclude", ".git", "--exclude", ".git/*", "--exclude", ".git/**"}
	if needle != "" {
		arguments = append(arguments, "--", needle)
	}
	command := exec.CommandContext(ctx, fd, arguments...)
	var output bytes.Buffer
	command.Stdout = &limitedWriter{target: &output, remaining: 64 << 10}
	if command.Run() != nil {
		return nil
	}
	type ranked struct {
		score, order int
		suggestion   Suggestion
	}
	var values []ranked
	for order, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		directory := strings.HasSuffix(line, "/")
		path := strings.TrimSuffix(line, "/")
		name, normalized := strings.ToLower(filepath.Base(path)), strings.ToLower(needle)
		score := 0
		if normalized == "" {
			score = 1
		} else if name == normalized {
			score = 100
		} else if strings.HasPrefix(name, normalized) {
			score = 80
		} else if strings.Contains(name, normalized) {
			score = 50
		} else if strings.Contains(strings.ToLower(path), normalized) {
			score = 30
		}
		if directory && score > 0 {
			score += 10
		}
		if score == 0 {
			continue
		}
		display := displayBase + path
		if displayBase == "/" {
			display = "/" + path
		}
		if directory {
			display += "/"
		}
		values = append(values, ranked{score: score, order: order, suggestion: Suggestion{Path: display, Directory: directory}})
	}
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].score != values[j].score {
			return values[i].score > values[j].score
		}
		return values[i].order < values[j].order
	})
	if len(values) > 20 {
		values = values[:20]
	}
	result := make([]Suggestion, len(values))
	for i := range values {
		result[i] = values[i].suggestion
	}
	return result
}

var errFDOutputTooLarge = errors.New("fd output exceeds bound")

type limitedWriter struct {
	target    *bytes.Buffer
	remaining int
}

func (writer *limitedWriter) Write(value []byte) (int, error) {
	if len(value) > writer.remaining {
		return 0, errFDOutputTooLarge
	}
	writer.remaining -= len(value)
	return writer.target.Write(value)
}
func expandSuggestionPath(cwd, home, value string) string {
	if value == "~" || strings.HasPrefix(value, "~/") {
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(value, "~/"), "~"))
	}
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(cwd, value)
}
func findFD(home string) string {
	candidates := []string{filepath.Join(home, ".pi", "agent", "bin", "fd")}
	for _, name := range []string{"fd", "fdfind"} {
		if path, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, path)
		}
	}
	for _, path := range candidates {
		if stat, err := os.Stat(path); err == nil && stat.Mode().IsRegular() && stat.Mode()&0111 != 0 {
			return path
		}
	}
	return ""
}
