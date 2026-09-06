package sessions

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type TagCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

var ErrInvalidTag = errors.New("tags must contain 1–64 characters without control characters")
var ErrTooManyTags = errors.New("a session may have at most 32 tags")

func NormalizeTag(name string) (string, error) {
	if !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
		return "", ErrInvalidTag
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return "", ErrInvalidTag
	}
	return name, nil
}

func (state *GatewayState) SessionTags() (map[string][]string, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.readSessionTags()
}

func (state *GatewayState) readSessionTags() (map[string][]string, error) {
	var stored map[string][]string
	if err := readJSONIfExists(state.tagsPath, &stored); err != nil {
		return nil, fmt.Errorf("read session tags state: %w", err)
	}
	result := make(map[string][]string, len(stored))
	for path, names := range stored {
		path = state.configuredPath(path)
		for _, name := range names {
			normalized, err := NormalizeTag(name)
			if err != nil {
				return nil, fmt.Errorf("read session tags state: %w", err)
			}
			result[path] = append(result[path], normalized)
		}
	}
	for path, names := range result {
		slices.Sort(names)
		result[path] = slices.Compact(names)
		if len(result[path]) > 32 {
			return nil, fmt.Errorf("read session tags state: %w", ErrTooManyTags)
		}
	}
	return result, nil
}

func (state *GatewayState) SetTag(path, name string, assigned bool) error {
	name, err := NormalizeTag(name)
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	tags, err := state.readSessionTags()
	if err != nil {
		return err
	}
	path = state.configuredPath(path)
	names := tags[path]
	index, found := slices.BinarySearch(names, name)
	if assigned && !found {
		if len(names) >= 32 {
			return ErrTooManyTags
		}
		names = slices.Insert(names, index, name)
	} else if !assigned && found {
		names = slices.Delete(names, index, index+1)
	}
	_, err = state.replaceTags(tags, map[string][]string{path: names})
	if err == nil {
		delete(state.forgotten, path)
	}
	return err
}

func NormalizeTags(names []string) ([]string, error) {
	result := make([]string, 0, len(names))
	for _, name := range names {
		normalized, err := NormalizeTag(name)
		if err != nil {
			return nil, err
		}
		result = append(result, normalized)
	}
	slices.Sort(result)
	result = slices.Compact(result)
	if len(result) > 32 {
		return nil, ErrTooManyTags
	}
	return result, nil
}

func (state *GatewayState) SetTags(path string, names []string) (func() error, error) {
	names, err := NormalizeTags(names)
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	tags, err := state.readSessionTags()
	if err != nil {
		return nil, err
	}
	return state.replaceTags(tags, map[string][]string{state.configuredPath(path): names})
}

func (state *GatewayState) CopyTags(from, to string) (func() error, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	tags, err := state.readSessionTags()
	if err != nil {
		return nil, err
	}
	from, to = state.configuredPath(from), state.configuredPath(to)
	if from == to {
		return nil, nil
	}
	return state.replaceTags(tags, map[string][]string{to: tags[from]})
}

func (state *GatewayState) MigrateTags(from, to string) (func() error, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	tags, err := state.readSessionTags()
	if err != nil {
		return nil, err
	}
	from, to = state.configuredPath(from), state.configuredPath(to)
	if from == to || len(tags[from]) == 0 {
		return nil, nil
	}
	names, err := NormalizeTags(append(slices.Clone(tags[to]), tags[from]...))
	if err != nil {
		return nil, err
	}
	return state.replaceTags(tags, map[string][]string{from: nil, to: names})
}

// The caller holds state.mu. Rollback touches only paths that have not been edited since.
func (state *GatewayState) replaceTags(tags, changes map[string][]string) (func() error, error) {
	previous := make(map[string][]string, len(changes))
	for path, names := range changes {
		previous[path] = slices.Clone(tags[path])
		if len(names) == 0 {
			delete(tags, path)
		} else {
			tags[path] = names
		}
	}
	if err := writeJSON(state.tagsPath, tags); err != nil {
		return nil, fmt.Errorf("write session tags state: %w", err)
	}
	state.tagRevision++
	revision := state.tagRevision
	if state.tagChanges == nil {
		state.tagChanges = make(map[string]uint64)
	}
	for path := range changes {
		state.tagChanges[path] = revision
	}
	return func() error {
		state.mu.Lock()
		defer state.mu.Unlock()
		current, err := state.readSessionTags()
		if err != nil {
			return err
		}
		restore := make(map[string][]string)
		for path, names := range previous {
			if state.tagChanges[path] == revision {
				restore[path] = names
			}
		}
		_, err = state.replaceTags(current, restore)
		return err
	}, nil
}
