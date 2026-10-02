package sessions

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// ProjectMonogram is the mark of a directory that has no stored one.
func ProjectMonogram(cwd string) string {
	return monogramCandidates(cwd)[0]
}

// ProjectMonograms returns the stored marks, giving each new directory letters
// no other project uses. Marks are kept so that adding a project never renames another.
func (state *GatewayState) ProjectMonograms(cwds []string) (map[string]string, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	path := filepath.Join(filepath.Dir(state.projectsPath), "project-monograms.json")
	monograms := make(map[string]string)
	if err := readJSONIfExists(path, &monograms); err != nil {
		return nil, fmt.Errorf("read project monograms: %w", err)
	}
	taken := make(map[string]bool, len(monograms))
	for _, monogram := range monograms {
		taken[monogram] = true
	}
	changed := false
	for _, cwd := range cwds {
		if _, exists := monograms[cwd]; exists {
			continue
		}
		candidates := monogramCandidates(cwd)
		monogram := candidates[0]
		for _, candidate := range candidates {
			if !taken[candidate] {
				monogram = candidate
				break
			}
		}
		monograms[cwd] = monogram
		taken[monogram] = true
		changed = true
	}
	if changed {
		if err := writeJSON(path, monograms); err != nil {
			return nil, fmt.Errorf("write project monograms: %w", err)
		}
	}
	return monograms, nil
}

// In order of preference: the initials of the first two words, the first two
// letters, then the first letter with each later one.
func monogramCandidates(cwd string) []string {
	words := strings.FieldsFunc(strings.ToLower(filepath.Base(cwd)), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	letters := []rune(strings.Join(words, ""))
	if len(letters) < 2 {
		return []string{string(letters)}
	}
	var candidates []string
	if len(words) > 1 {
		candidates = append(candidates, string([]rune{letters[0], []rune(words[1])[0]}))
	}
	for _, letter := range letters[1:] {
		candidates = append(candidates, string([]rune{letters[0], letter}))
	}
	return candidates
}
