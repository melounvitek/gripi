package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

type GatewayState struct {
	readPath     string
	pinnedPath   string
	sessionsRoot string
	mu           sync.Mutex
}

func NewGatewayState(readPath, pinnedPath, sessionsRoot string) *GatewayState {
	return &GatewayState{readPath: readPath, pinnedPath: pinnedPath, sessionsRoot: sessionsRoot}
}

func (state *GatewayState) ReadAndObserve(all []*Session, selected *Session, markSelected bool) (map[string]bool, map[string]bool, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	counts := map[string]int{}
	if err := readJSONIfExists(state.readPath, &counts); err != nil {
		return nil, nil, fmt.Errorf("read session read state: %w", err)
	}
	if counts == nil {
		counts = map[string]int{}
	}
	counts, changed := state.normalizedCounts(counts)
	var paths []string
	if err := readJSONIfExists(state.pinnedPath, &paths); err != nil {
		return nil, nil, fmt.Errorf("read pinned sessions state: %w", err)
	}
	for _, session := range all {
		value, known := counts[session.Path]
		if !known || value > session.AssistantResponseCount {
			counts[session.Path] = session.AssistantResponseCount
			changed = true
		}
	}
	if selected != nil && markSelected && counts[selected.Path] != selected.AssistantResponseCount {
		counts[selected.Path] = selected.AssistantResponseCount
		changed = true
	}
	if changed {
		if err := writeJSON(state.readPath, counts); err != nil {
			return nil, nil, fmt.Errorf("write session read state: %w", err)
		}
	}
	unread := make(map[string]bool)
	for _, session := range all {
		unread[session.Path] = counts[session.Path] < session.AssistantResponseCount
	}
	pinned := make(map[string]bool)
	for _, path := range paths {
		pinned[state.configuredPath(path)] = true
	}
	return unread, pinned, nil
}

func (state *GatewayState) SetPinned(path string, pinned bool) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	var paths []string
	if err := readJSONIfExists(state.pinnedPath, &paths); err != nil {
		return fmt.Errorf("read pinned sessions state: %w", err)
	}
	path = state.configuredPath(path)
	result := make([]string, 0, len(paths)+1)
	found := false
	seen := make(map[string]bool)
	for _, candidate := range paths {
		candidate = state.configuredPath(candidate)
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		if candidate == path {
			found = true
			if !pinned {
				continue
			}
		}
		result = append(result, candidate)
	}
	if pinned && !found {
		result = append(result, path)
	}
	if err := writeJSON(state.pinnedPath, result); err != nil {
		return fmt.Errorf("write pinned sessions state: %w", err)
	}
	return nil
}

func (state *GatewayState) MarkRead(path string, count int) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	values := map[string]int{}
	if err := readJSONIfExists(state.readPath, &values); err != nil {
		return fmt.Errorf("read session read state: %w", err)
	}
	if values == nil {
		values = map[string]int{}
	}
	values, _ = state.normalizedCounts(values)
	path = state.configuredPath(path)
	if values[path] < count {
		values[path] = count
	}
	if err := writeJSON(state.readPath, values); err != nil {
		return fmt.Errorf("write session read state: %w", err)
	}
	return nil
}

func (state *GatewayState) configuredPath(path string) string {
	if state.sessionsRoot != "" {
		if configured, ok := ConfiguredSessionPath(state.sessionsRoot, path); ok {
			return configured
		}
	}
	return path
}

func (state *GatewayState) normalizedCounts(values map[string]int) (map[string]int, bool) {
	result := make(map[string]int, len(values))
	changed := false
	for path, count := range values {
		configured := state.configuredPath(path)
		if configured != path {
			changed = true
		}
		if existing, found := result[configured]; !found || count > existing {
			result[configured] = count
		} else {
			changed = true
		}
	}
	return result, changed
}

func readJSONIfExists(path string, target any) error {
	err := readJSON(path, target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func readJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("state contains multiple JSON values")
		}
		return err
	}
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".gripi-state-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
