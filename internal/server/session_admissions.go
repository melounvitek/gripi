package server

import (
	"context"
	"sync"
)

type sessionPromptLease struct {
	path string
}

type sessionAdmission struct {
	prompts  map[*sessionPromptLease]struct{}
	idleDone chan struct{}
}

// sessionAdmissions keeps prompt handlers and idle cleanup from overlapping,
// including the gaps between their individual RPC operations.
type sessionAdmissions struct {
	mu       sync.Mutex
	sessions map[string]*sessionAdmission
}

// resolve must not call the RPC registry: remap commits hold its mutex before
// taking mu. Resolve aliases under mu so a commit cannot strand a new lease.
func (admissions *sessionAdmissions) prompt(ctx context.Context, resolve func() (string, error)) (string, func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		admissions.mu.Lock()
		path, err := resolve()
		if err != nil {
			admissions.mu.Unlock()
			return "", nil, err
		}
		entry := admissions.sessions[path]
		if entry != nil && entry.idleDone != nil {
			done := entry.idleDone
			admissions.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return "", nil, ctx.Err()
			}
		}
		release := admissions.addPromptLocked(path)
		admissions.mu.Unlock()
		return path, release, nil
	}
}

// tryPrompt protects a remap destination without waiting for idle retirement
// while the caller holds the remap locks.
func (admissions *sessionAdmissions) tryPrompt(path string) (func(), bool) {
	admissions.mu.Lock()
	defer admissions.mu.Unlock()
	if entry := admissions.sessions[path]; entry != nil && entry.idleDone != nil {
		return nil, false
	}
	return admissions.addPromptLocked(path), true
}

func (admissions *sessionAdmissions) addPromptLocked(path string) func() {
	entry := admissions.sessions[path]
	if entry == nil {
		entry = &sessionAdmission{prompts: make(map[*sessionPromptLease]struct{})}
		if admissions.sessions == nil {
			admissions.sessions = make(map[string]*sessionAdmission)
		}
		admissions.sessions[path] = entry
	}
	lease := &sessionPromptLease{path: path}
	entry.prompts[lease] = struct{}{}
	return func() {
		admissions.mu.Lock()
		defer admissions.mu.Unlock()
		entry := admissions.sessions[lease.path]
		delete(entry.prompts, lease)
		if len(entry.prompts) == 0 {
			delete(admissions.sessions, lease.path)
		}
	}
}

// The caller protects to with tryPrompt until the move finishes. Transfer all
// ongoing handlers and publish the alias in the same admission critical section.
func (admissions *sessionAdmissions) remap(from, to string, commit func()) {
	admissions.mu.Lock()
	defer admissions.mu.Unlock()
	if source := admissions.sessions[from]; source != nil && len(source.prompts) > 0 {
		destination := admissions.sessions[to]
		for lease := range source.prompts {
			lease.path = to
			destination.prompts[lease] = struct{}{}
		}
		delete(admissions.sessions, from)
	}
	commit()
}

func (admissions *sessionAdmissions) retireIdle(path string) (func(), bool) {
	admissions.mu.Lock()
	defer admissions.mu.Unlock()
	if admissions.sessions[path] != nil {
		return nil, false
	}
	entry := &sessionAdmission{idleDone: make(chan struct{})}
	if admissions.sessions == nil {
		admissions.sessions = make(map[string]*sessionAdmission)
	}
	admissions.sessions[path] = entry
	return func() {
		admissions.mu.Lock()
		defer admissions.mu.Unlock()
		delete(admissions.sessions, path)
		close(entry.idleDone)
	}, true
}
