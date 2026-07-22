package sessions

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type GatewayState struct {
	readPath   string
	pinnedPath string
	mu         sync.Mutex
}

func NewGatewayState(readPath, pinnedPath string) *GatewayState {
	return &GatewayState{readPath: readPath, pinnedPath: pinnedPath}
}

func (state *GatewayState) ReadAndObserve(all []*Session, selected *Session, markSelected bool) (map[string]bool, map[string]bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	counts := map[string]int{}
	_ = readJSON(state.readPath, &counts)
	if counts == nil {
		counts = map[string]int{}
	}
	changed := false
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
		_ = writeJSON(state.readPath, counts)
	}
	unread := make(map[string]bool)
	for _, session := range all {
		unread[session.Path] = counts[session.Path] < session.AssistantResponseCount
	}
	var paths []string
	_ = readJSON(state.pinnedPath, &paths)
	pinned := make(map[string]bool)
	for _, path := range paths {
		pinned[path] = true
	}
	return unread, pinned
}

func (state *GatewayState) SetPinned(path string, pinned bool) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	var paths []string
	_ = readJSON(state.pinnedPath, &paths)
	result := make([]string, 0, len(paths)+1)
	found := false
	seen := make(map[string]bool)
	for _, candidate := range paths {
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
	return writeJSON(state.pinnedPath, result)
}

func (state *GatewayState) MarkRead(path string, count int) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	values := map[string]int{}
	_ = readJSON(state.readPath, &values)
	if values == nil {
		values = map[string]int{}
	}
	if values[path] < count {
		values[path] = count
	}
	return writeJSON(state.readPath, values)
}

func readJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(io.LimitReader(file, 8<<20)).Decode(target)
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
