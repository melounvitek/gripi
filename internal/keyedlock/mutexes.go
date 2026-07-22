package keyedlock

import "sync"

type entry struct {
	mutex sync.Mutex
	refs  int
}

// Mutexes serializes work by key and removes idle keys.
type Mutexes struct {
	mu      sync.Mutex
	entries map[string]*entry
}

func (mutexes *Mutexes) Lock(key string) func() {
	entry := mutexes.reference(key)
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		mutexes.release(key, entry)
	}
}

func (mutexes *Mutexes) TryLock(key string) (func(), bool) {
	entry := mutexes.reference(key)
	if !entry.mutex.TryLock() {
		mutexes.release(key, entry)
		return nil, false
	}
	return func() {
		entry.mutex.Unlock()
		mutexes.release(key, entry)
	}, true
}

func (mutexes *Mutexes) Len() int {
	mutexes.mu.Lock()
	defer mutexes.mu.Unlock()
	return len(mutexes.entries)
}

func (mutexes *Mutexes) reference(key string) *entry {
	mutexes.mu.Lock()
	defer mutexes.mu.Unlock()
	if mutexes.entries == nil {
		mutexes.entries = make(map[string]*entry)
	}
	value := mutexes.entries[key]
	if value == nil {
		value = &entry{}
		mutexes.entries[key] = value
	}
	value.refs++
	return value
}

func (mutexes *Mutexes) release(key string, value *entry) {
	mutexes.mu.Lock()
	defer mutexes.mu.Unlock()
	value.refs--
	if value.refs == 0 && mutexes.entries[key] == value {
		delete(mutexes.entries, key)
	}
}
