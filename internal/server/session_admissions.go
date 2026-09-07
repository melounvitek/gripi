package server

import (
	"context"
	"errors"
	"sync"

	"github.com/melounvitek/gripi/internal/rpc"
)

var errSessionNavigating = errors.New("The session tree is changing. Review the selected branch before sending again.")

type sessionPromptLease struct {
	path string
}

type sessionAdmission struct {
	prompts    map[*sessionPromptLease]struct{}
	idleDone   chan struct{}
	navigating bool
}

// sessionAdmissions excludes idle cleanup and tree navigation from prompt
// handlers, including the gaps between their individual RPC operations.
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
		if entry != nil && entry.navigating {
			admissions.mu.Unlock()
			return "", nil, errSessionNavigating
		}
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

// tryPrompt protects a remap path without waiting for idle retirement or
// overlapping navigation while the caller holds the remap locks.
func (admissions *sessionAdmissions) tryPrompt(path string) (func(), error) {
	admissions.mu.Lock()
	defer admissions.mu.Unlock()
	if entry := admissions.sessions[path]; entry != nil {
		if entry.navigating {
			return nil, errSessionNavigating
		}
		if entry.idleDone != nil {
			return nil, rpc.ErrOperationPending
		}
	}
	return admissions.addPromptLocked(path), nil
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

// The caller protects both paths with tryPrompt until the move finishes.
// Transfer handlers and publish the alias in the same critical section.
func (admissions *sessionAdmissions) remap(from, to string, commit func()) {
	admissions.mu.Lock()
	defer admissions.mu.Unlock()
	source, destination := admissions.sessions[from], admissions.sessions[to]
	for lease := range source.prompts {
		lease.path = to
		destination.prompts[lease] = struct{}{}
	}
	delete(admissions.sessions, from)
	commit()
}

// Navigation rejects competing requests rather than waiting and submitting
// their prompts on a different branch. Resolve has the same contract as prompt.
func (admissions *sessionAdmissions) navigate(resolve func() (string, error)) (string, func(), error) {
	admissions.mu.Lock()
	defer admissions.mu.Unlock()
	path, err := resolve()
	if err != nil {
		return "", nil, err
	}
	if entry := admissions.sessions[path]; entry != nil {
		if entry.navigating {
			return "", nil, errSessionNavigating
		}
		return "", nil, rpc.ErrOperationPending
	}
	if admissions.sessions == nil {
		admissions.sessions = make(map[string]*sessionAdmission)
	}
	admissions.sessions[path] = &sessionAdmission{navigating: true}
	return path, func() {
		admissions.mu.Lock()
		defer admissions.mu.Unlock()
		delete(admissions.sessions, path)
	}, nil
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
