package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
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
	startedAt := clients.clock()
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
	if err := clients.registerStartedAt(key, client, startedAt); err != nil {
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

// RestartUnsavedSession restarts the idle client of a session that the agent has not saved yet, if it started no
// later than moment, so that the agent gets what changed since, such as the user's variables. The session exists only
// in the old client, so the new one starts it empty under the same key, with the model, thinking level and name
// chosen in it so far. The old client keeps the session when the new one cannot take them.
func RestartUnsavedSession(ctx context.Context, key string, moment time.Time, clients *Registry, pending *PendingSessionRegistry) (bool, error) {
	var id string
	restarted, err := clients.restartIfStartedBefore(key, moment, func(old, replacement Client) error {
		choices, err := readUnsavedChoices(ctx, old)
		if err != nil {
			return err
		}
		id, err = choices.apply(ctx, replacement)
		return err
	})
	if cwd, ok := pending.CWD(key); restarted && ok {
		pending.Remember(key, cwd, id)
	}
	if errors.Is(err, errRunning) {
		return false, nil
	}
	return restarted, err
}

// StopUnsavedSession closes the idle client of a session that the agent has not saved yet, like RestartUnsavedSession
// but for when no client may run the session for a while, such as while its folder moves to another computer. The
// session keeps its pending key meanwhile, and the function that it returns starts a new client under the key, with
// the model, thinking level and name chosen in the old one. If no client can start with them, the session keeps its
// key without a client, and the function can run again. With a running client, it closes nothing and returns no
// function.
func StopUnsavedSession(ctx context.Context, key string, clients *Registry, pending *PendingSessionRegistry) (func(context.Context) error, error) {
	var choices unsavedChoices
	// The next client continues the old one's events, so a page's cursor stays valid.
	closed, err := clients.closeIdleAfter(key, func(client Client) error {
		var err error
		choices, err = readUnsavedChoices(ctx, client)
		return err
	})
	if errors.Is(err, errRunning) {
		return nil, nil
	}
	if !closed || err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		err := clients.WithClient(ctx, key, func(client Client) error {
			id, err := choices.apply(ctx, client)
			if cwd, ok := pending.CWD(key); err == nil && ok {
				pending.Remember(key, cwd, id)
			}
			return err
		})
		if err != nil {
			// A client without the choices would take their place at the next stop.
			clients.CloseClientWithoutOperations(key)
		}
		return err
	}, nil
}

var errRunning = errors.New("the session is running")

// unsavedChoices are what was chosen in a session that the agent has not saved yet, which only its client has.
type unsavedChoices struct {
	status LiveStatus
	name   string
}

func readUnsavedChoices(ctx context.Context, client Client) (unsavedChoices, error) {
	state, err := client.State(ctx)
	if err != nil {
		return unsavedChoices{}, err
	}
	// The agent can take a prompt before it reports the run that the prompt starts.
	if state.Streaming || state.Compacting {
		return unsavedChoices{}, errRunning
	}
	status, err := client.LiveStatus(ctx)
	return unsavedChoices{status: status, name: state.Name}, err
}

// apply gives the choices to a new client of the session, and returns the session's ID there.
func (choices unsavedChoices) apply(ctx context.Context, client Client) (string, error) {
	if models, ok := client.(ModelSelector); ok && choices.status.Provider != "" && choices.status.ModelID != "" {
		if _, err := models.SetModel(ctx, choices.status.Provider, choices.status.ModelID, choices.status.ThinkingLevel); err != nil {
			return "", err
		}
	}
	if renamer, ok := client.(Renamer); ok && choices.name != "" {
		if err := renamer.Rename(ctx, choices.name); err != nil {
			return "", err
		}
	}
	state, err := client.State(ctx)
	return state.ID, err
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
