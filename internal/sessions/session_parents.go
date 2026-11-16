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
	if err := writeJSON(state.parentsPath, parents); err != nil {
		return fmt.Errorf("write session parents: %w", err)
	}
	return nil
}
