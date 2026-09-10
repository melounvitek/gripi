package sessions

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var tagColorPalette = []string{
	"#e69cff", "#5ff5ce", "#ff9f43", "#579dff",
	"#f5e663", "#f05a5a", "#52d6ff", "#ff6bb5",
	"#9bd45a", "#c3a35a", "#a275e3", "#38a89d",
}

var validTagColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// TagColors returns persistent reservations, including tags no longer assigned to a session.
func (state *GatewayState) TagColors() (map[string]string, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	tags, err := state.readSessionTags()
	if err != nil {
		return nil, err
	}
	return state.reserveTagColors(tags)
}

// The caller holds state.mu. Seed existing tags before pending changes so removal
// cannot lose a legacy tag's reservation, and new tags cannot reorder migration.
func (state *GatewayState) reserveTagColors(tagSets ...map[string][]string) (map[string]string, error) {
	colors := make(map[string]string)
	if state.tagsPath == "" {
		return colors, nil
	}
	path := filepath.Join(filepath.Dir(state.tagsPath), "tag-colors.json")
	if err := readJSONIfExists(path, &colors); err != nil {
		return nil, fmt.Errorf("read tag colors state: %w", err)
	}
	if colors == nil {
		colors = make(map[string]string)
	}
	for name, color := range colors {
		normalized, err := NormalizeTag(name)
		if err != nil || normalized != name || !validTagColor.MatchString(color) {
			return nil, fmt.Errorf("read tag colors state: invalid allocation for %q", name)
		}
	}
	changed := false
	for _, tags := range tagSets {
		var missing []string
		for _, names := range tags {
			for _, name := range names {
				if _, exists := colors[name]; !exists {
					missing = append(missing, name)
				}
			}
		}
		slices.Sort(missing)
		for _, name := range slices.Compact(missing) {
			colors[name] = nextTagColor(colors)
			changed = true
		}
	}
	if changed {
		if err := writeJSON(path, colors); err != nil {
			return nil, fmt.Errorf("write tag colors state: %w", err)
		}
	}
	return colors, nil
}

func nextTagColor(colors map[string]string) string {
	uses := make(map[string]int)
	for _, color := range colors {
		uses[strings.ToLower(color)]++
	}
	// Preserve the first two seed colors; subsequent choices maximize separation.
	if len(colors) == 1 && uses[tagColorPalette[0]] == 1 {
		return tagColorPalette[1]
	}
	best := tagColorPalette[0]
	bestDistance := -1
	for _, candidate := range tagColorPalette {
		nearest := 3*255*255 + 1
		for _, assigned := range colors {
			nearest = min(nearest, tagColorDistance(candidate, assigned))
		}
		if uses[candidate] < uses[best] || (uses[candidate] == uses[best] && nearest > bestDistance) {
			best, bestDistance = candidate, nearest
		}
	}
	return best
}

func tagColorDistance(a, b string) int {
	// Both inputs have already been validated as six-digit hex colors.
	left, _ := strconv.ParseUint(a[1:], 16, 24)
	right, _ := strconv.ParseUint(b[1:], 16, 24)
	distance := 0
	for shift := 0; shift < 24; shift += 8 {
		delta := int((left>>shift)&255) - int((right>>shift)&255)
		distance += delta * delta
	}
	return distance
}
