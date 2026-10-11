package agent

import (
	"context"
	"errors"
	"fmt"
)

// ClientMover runs call on a session's client and moves the client to the key that call returns, like
// Registry.WithClientMove. The guard does the same for a saved session after checking it.
type ClientMover interface {
	WithClientMove(ctx context.Context, key string, touch bool, call func(Client) (string, error), prepare func(string, string) (func() error, error), commit func(string, string)) (string, error)
}

var errOutsideStore = errors.New("the agent reported a session outside its store")

// StartNewSession starts a client for a new session in cwd and registers it under the key the agent reports, or a
// pending key until the agent names the session. prepare readies the gateway's state for the key and returns how to
// undo that if the client cannot be registered.
func StartNewSession(ctx context.Context, cwd string, start func(cwd string) (Client, error), store Store, clients *Registry, pending *PendingSessionRegistry, prepare func(key string) (func() error, error)) (string, error) {
	startedAt := clients.Now()
	client, err := start(cwd)
	if err != nil {
		return "", err
	}
	registered := false
	defer func() {
		if !registered {
			_ = client.Close()
		}
	}()
	state, err := client.State(ctx)
	// An agent that won't describe its new session still gets one, under a pending key.
	var refused *RequestError
	if errors.As(err, &refused) {
		state, err = State{}, nil
	}
	if errors.Is(err, ErrProcessExited) {
		return "", fmt.Errorf("%w: it exited before the session was ready", ErrStartFailed)
	}
	if err != nil {
		return "", err
	}
	key := state.Key
	if key == "" {
		key = store.PendingKey()
	}
	key, ok := store.Canonical(key)
	if !ok {
		return "", errOutsideStore
	}
	var rollback func() error
	if prepare != nil {
		rollback, err = prepare(key)
		if err != nil {
			return "", err
		}
	}
	if err := clients.RegisterStartedAt(key, client, startedAt); err != nil {
		if rollback != nil {
			return "", errors.Join(err, rollback())
		}
		return "", err
	}
	registered = true
	if !store.Exists(key) {
		pending.Remember(key, cwd, state.ID)
	}
	return key, nil
}

// BranchSession runs branch on the client of the session previous and moves the client to the session it creates.
// It returns the key that the client serves afterwards, which stays previous when the agent refused or cancelled the
// branch. prepare readies the gateway's state for the new key during the move.
func BranchSession(ctx context.Context, previous, cwd string, clients ClientMover, store Store, pending *PendingSessionRegistry, branch func(Client) (Branch, error), prepare func(string, string) (func() error, error)) (string, Branch, error) {
	_, wasPending := pending.CWD(previous)
	var result Branch
	// The mover closes the client on an error, so an error after which the client still serves previous waits.
	var unmoved error
	key, err := clients.WithClientMove(ctx, previous, true, func(client Client) (string, error) {
		var err error
		result, err = branch(client)
		var refused *RequestError
		if errors.As(err, &refused) {
			unmoved = err
			return previous, nil
		}
		if err != nil || result.Cancelled {
			return previous, err
		}
		// A session that the client does not serve needs a client of its own, which the gateway cannot start yet.
		if !result.Moved {
			unmoved = errors.New("the agent created a session that this client does not serve")
			return previous, nil
		}
		if result.Key == "" {
			return "", errors.New("the agent did not report the session it switched to")
		}
		key, ok := store.Canonical(result.Key)
		if !ok {
			return "", errOutsideStore
		}
		if key == previous {
			return "", errors.New("the agent did not report the session it switched to")
		}
		return key, nil
	}, prepare, func(from, to string) {
		if wasPending {
			pending.Remap(from, to)
		}
		if !store.Exists(to) {
			pending.Remember(to, cwd, result.ID)
		}
	})
	if err == nil {
		err = unmoved
	}
	return key, result, err
}
