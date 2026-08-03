package rpc

import (
	"sync"
	"time"
)

type PendingSession struct {
	Path      string
	CWD       string
	CreatedAt time.Time
}

type PendingSessionRegistry struct {
	mu         sync.Mutex
	clock      func() time.Time
	entries    map[string]PendingSession
	order      []string
	remaps     map[string]string
	remapOrder []string
}

func NewPendingSessionRegistry(clock func() time.Time) *PendingSessionRegistry {
	if clock == nil {
		clock = time.Now
	}
	return &PendingSessionRegistry{clock: clock, entries: make(map[string]PendingSession), remaps: make(map[string]string)}
}

func (registry *PendingSessionRegistry) Remember(path, cwd string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	delete(registry.remaps, path)
	registry.remapOrder = removePendingOrder(registry.remapOrder, path)
	entry, exists := registry.entries[path]
	if !exists {
		entry = PendingSession{Path: path, CreatedAt: registry.clock()}
		registry.order = append(registry.order, path)
	}
	entry.CWD = cwd
	registry.entries[path] = entry
}

func (registry *PendingSessionRegistry) CWD(path string) (string, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry, exists := registry.entries[path]
	return entry.CWD, exists
}

func (registry *PendingSessionRegistry) Current(path string) (string, string, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current := path
	seen := make(map[string]bool)
	for range 256 {
		if entry, exists := registry.entries[current]; exists {
			return current, entry.CWD, true
		}
		next, remapped := registry.remaps[current]
		if !remapped || seen[current] {
			return current, "", false
		}
		seen[current] = true
		current = next
	}
	return current, "", false
}

func (registry *PendingSessionRegistry) Entries() []PendingSession {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	result := make([]PendingSession, 0, len(registry.entries))
	for _, path := range registry.order {
		if entry, exists := registry.entries[path]; exists {
			result = append(result, entry)
		}
	}
	return result
}

func (registry *PendingSessionRegistry) Remap(from, to string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	delete(registry.entries, from)
	registry.order = removePendingOrder(registry.order, from)
	registry.remaps[from] = to
	registry.remapOrder = append(removePendingOrder(registry.remapOrder, from), from)
	for len(registry.remapOrder) > 256 {
		delete(registry.remaps, registry.remapOrder[0])
		registry.remapOrder = registry.remapOrder[1:]
	}
}

func (registry *PendingSessionRegistry) Resolve(path string) (string, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	resolved, ok := registry.remaps[path]
	return resolved, ok
}

func (registry *PendingSessionRegistry) Forget(path string) {
	registry.mu.Lock()
	delete(registry.entries, path)
	registry.order = removePendingOrder(registry.order, path)
	delete(registry.remaps, path)
	registry.remapOrder = removePendingOrder(registry.remapOrder, path)
	registry.mu.Unlock()
}

func removePendingOrder(order []string, path string) []string {
	for index, candidate := range order {
		if candidate == path {
			return append(order[:index], order[index+1:]...)
		}
	}
	return order
}
