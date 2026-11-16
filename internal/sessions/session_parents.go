package sessions

import "fmt"

// SessionParents maps each session linked under another, such as a worker under the conversation that started it,
// to that session. Pi's parentSession means a fork, so the link is the gateway's own.
func (state *GatewayState) SessionParents() (map[string]string, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.readSessionParents()
}

func (state *GatewayState) readSessionParents() (map[string]string, error) {
	stored := map[string]string{}
	if err := readJSONIfExists(state.parentsPath, &stored); err != nil {
		return nil, fmt.Errorf("read session parents: %w", err)
	}
	result := make(map[string]string, len(stored))
	for path, parent := range stored {
		result[state.configuredPath(path)] = state.configuredPath(parent)
	}
	return result, nil
}

func (state *GatewayState) SetSessionParent(path, parent string) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	parents, err := state.readSessionParents()
	if err != nil {
		return err
	}
	parents[state.configuredPath(path)] = state.configuredPath(parent)
	return state.writeSessionParents(parents)
}

// MigrateSessionParents moves the links of a session that moves to another path: its own, and those of the sessions
// linked under it.
func (state *GatewayState) MigrateSessionParents(from, to string) (func() error, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	from, to = state.configuredPath(from), state.configuredPath(to)
	if from == to {
		return nil, nil
	}
	if changed, err := state.relinkLocked(from, to); err != nil || !changed {
		return nil, err
	}
	return func() error {
		state.mu.Lock()
		defer state.mu.Unlock()
		_, err := state.relinkLocked(to, from)
		return err
	}, nil
}

// relinkLocked puts the session to in the place of from in every link, or drops the links of from when to is empty,
// and reports whether a link changed. The caller holds state.mu.
func (state *GatewayState) relinkLocked(from, to string) (bool, error) {
	parents, err := state.readSessionParents()
	if err != nil {
		return false, err
	}
	result := make(map[string]string, len(parents))
	changed := false
	for path, parent := range parents {
		if path == from {
			path, changed = to, true
		}
		if parent == from {
			parent, changed = to, true
		}
		if path != "" && parent != "" {
			result[path] = parent
		}
	}
	if !changed {
		return false, nil
	}
	return true, state.writeSessionParents(result)
}

func (state *GatewayState) writeSessionParents(parents map[string]string) error {
	if err := writeJSON(state.parentsPath, parents); err != nil {
		return fmt.Errorf("write session parents: %w", err)
	}
	return nil
}
