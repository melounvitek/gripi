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
	if len(names) == 0 {
		delete(tags, path)
	} else {
		tags[path] = names
	}
	if err := writeJSON(state.tagsPath, tags); err != nil {
		return fmt.Errorf("write session tags state: %w", err)
	}
	delete(state.forgotten, path)
	return nil
}
