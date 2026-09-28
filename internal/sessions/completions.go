package sessions

import (
	"errors"
	"fmt"
)

// Completion is gateway-owned metadata. ResponseCount uses the same raw message
// count as /read, but only advances when a managed run settles successfully.
type Completion struct {
	ID            string `json:"id,omitempty"`
	Preview       string `json:"preview,omitempty"`
	ResponseCount int    `json:"response_count"`
}

func (state *GatewayState) Completions() (map[string]Completion, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.readCompletions()
}

func (state *GatewayState) readCompletions() (map[string]Completion, error) {
	values := map[string]Completion{}
	if err := readJSONIfExists(state.completionsPath, &values); err != nil {
		return nil, fmt.Errorf("read session completions: %w", err)
	}
	result := make(map[string]Completion, len(values))
	for path, value := range values {
		path = state.configuredPath(path)
		if previous, found := result[path]; !found || value.ResponseCount > previous.ResponseCount {
			result[path] = value
		}
	}
	return result, nil
}

// BeginCompletion freezes the unread boundary without marking older replies read.
func (state *GatewayState) BeginCompletion(path string, count int) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	path = state.configuredPath(path)
	if state.sessionForgotten(path) {
		return nil
	}
	values, err := state.readCompletions()
	if err != nil {
		return err
	}
	if _, known := values[path]; !known {
		values[path] = Completion{ResponseCount: count}
		if err := writeJSON(state.completionsPath, values); err != nil {
			return err
		}
	}
	counts := map[string]int{}
	if err := readJSONIfExists(state.readPath, &counts); err != nil {
		return err
	}
	counts, _ = state.normalizedCounts(counts)
	if _, known := counts[path]; !known {
		counts[path] = count
		return writeJSON(state.readPath, counts)
	}
	return nil
}

func (state *GatewayState) Complete(path string, completion Completion) (bool, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	path = state.configuredPath(path)
	if state.sessionForgotten(path) {
		return false, nil
	}
	values, err := state.readCompletions()
	if err != nil {
		return false, err
	}
	if values[path].ID == completion.ID {
		return false, nil
	}
	values[path] = completion
	if err := writeJSON(state.completionsPath, values); err != nil {
		return false, err
	}
	return true, nil
}

// Keep the source read count for notifications already queued under the pending
// path. The destination inherits it so a final reply read before remap stays read.
func (state *GatewayState) MigrateCompletion(from, to string) (func() error, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	from, to = state.configuredPath(from), state.configuredPath(to)
	if from == to {
		return nil, nil
	}
	values, err := state.readCompletions()
	if err != nil {
		return nil, err
	}
	source, found := values[from]
	if !found {
		return nil, nil
	}
	destination, destinationKnown := values[to]
	counts := map[string]int{}
	if err := readJSONIfExists(state.readPath, &counts); err != nil {
		return nil, err
	}
	counts, _ = state.normalizedCounts(counts)
	previousRead, readKnown := counts[to]
	migratedRead := max(previousRead, counts[from])
	counts[to] = migratedRead
	if err := writeJSON(state.readPath, counts); err != nil {
		return nil, err
	}
	delete(values, from)
	values[to] = source
	if err := writeJSON(state.completionsPath, values); err != nil {
		if readKnown {
			counts[to] = previousRead
		} else {
			delete(counts, to)
		}
		return nil, errors.Join(err, writeJSON(state.readPath, counts))
	}
	return func() error {
		state.mu.Lock()
		defer state.mu.Unlock()
		current, err := state.readCompletions()
		if err != nil {
			return err
		}
		// Observe is serialized with pending remaps; still avoid undoing newer state.
		if current[to] == source {
			if _, known := current[from]; !known {
				current[from] = source
			}
			if destinationKnown {
				current[to] = destination
			} else {
				delete(current, to)
			}
			if err := writeJSON(state.completionsPath, current); err != nil {
				return err
			}
		}
		reads := map[string]int{}
		if err := readJSONIfExists(state.readPath, &reads); err != nil {
			return err
		}
		reads, _ = state.normalizedCounts(reads)
		if reads[to] == migratedRead {
			if readKnown {
				reads[to] = previousRead
			} else {
				delete(reads, to)
			}
			return writeJSON(state.readPath, reads)
		}
		return nil
	}, nil
}
