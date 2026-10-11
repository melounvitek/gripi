package agent

import (
	"context"
	"errors"

	"github.com/melounvitek/gripi/internal/keyedlock"
)

// Guard protects sessions against writers outside the gateway, such as the agent's own CLI working on a session
// that the gateway also runs. While someone else writes a session, the gateway follows it read-only until the
// user takes it over. An agent whose sessions nobody else writes would pass every call through to the registry.
type Guard interface {
	// InspectIfAvailable looks for outside writes to a saved session, unless another operation holds the session:
	// then it returns nil. With includePosition it also asks a running client where it is in the session.
	InspectIfAvailable(ctx context.Context, key string, includePosition bool) *SyncResult
	// ReconcileIfAvailable inspects like InspectIfAvailable, and calls after while it still holds the session.
	ReconcileIfAvailable(ctx context.Context, key string, includePosition bool, after func(SyncResult)) *SyncResult
	// KnownBlocked returns the result for a session already known to be blocked, without inspecting it.
	KnownBlocked(key string) *SyncResult
	// Forget drops what the guard knows about a session.
	Forget(key string)
	// TakeOver lets the gateway write a blocked session again. beforeManaged runs just before it may.
	TakeOver(ctx context.Context, key string, beforeManaged func() error) (SyncResult, error)
	// Message explains a blocked result to the user.
	Message(SyncResult) string
	// WithExclusiveOperation runs call while it holds the session, or fails with ErrSyncBusy.
	WithExclusiveOperation(key string, call func() error) error
	// WithMutableClient, WithInterruptClient and WithBashClient run call on the session's client in the registry
	// lane of the same name, once the guard has checked that nobody else wrote the session.
	WithMutableClient(ctx context.Context, key string, call func(Client) error) error
	WithInterruptClient(ctx context.Context, key string, call func(Client) error) error
	WithBashClient(ctx context.Context, key string, call func(Client) error) error
	// WithClientMove is Registry.WithClientMove for a saved session, checked like WithMutableClient.
	WithClientMove(ctx context.Context, key string, touch bool, call func(Client) (string, error), prepare func(string, string) (func() error, error), commit func(string, string)) (string, error)
	// RetireManagedClientIfAvailable calls retire unless another operation holds the session, and forgets the
	// session when retire stopped its client, unless the session is blocked.
	RetireManagedClientIfAvailable(key string, retire func() (bool, error)) (bool, error)
}

// SyncMode says who may write a session. The browser reads the values.
type SyncMode string

const (
	SyncAvailable      SyncMode = "available"
	SyncManaged        SyncMode = "managed"
	SyncExternalFollow SyncMode = "external_follow"
	SyncConflict       SyncMode = "conflict"
	// SyncFolderMissing is reported by the gateway, not the guard: the agent cannot run in a folder that is gone.
	SyncFolderMissing SyncMode = "folder_missing"
)

type SyncResult struct {
	Mode            SyncMode `json:"mode"`
	Revision        string   `json:"revision,omitempty"`
	AppendCursor    string   `json:"append_cursor,omitempty"`
	PersistedLeafID string   `json:"persisted_leaf_id,omitempty"`
	RPCLeafID       string   `json:"rpc_leaf_id,omitempty"`
	Error           string   `json:"error,omitempty"`
}

func (result SyncResult) Blocked() bool {
	return result.Mode == SyncExternalFollow || result.Mode == SyncConflict
}

type SyncBlockedError struct {
	Mode    SyncMode
	Message string
}

func (err *SyncBlockedError) Error() string { return err.Message }

var ErrSyncBusy = errors.New("another session operation is pending")

// OpenGuard is the guard of an agent whose sessions nobody else writes. It only keeps two operations from holding
// a session at once, like the guards that also check for writers outside the gateway.
func OpenGuard(clients *Registry) Guard {
	return &openGuard{clients: clients}
}

type openGuard struct {
	clients *Registry
	locks   keyedlock.Mutexes
}

func (guard *openGuard) result(key string) *SyncResult {
	if guard.clients.Registered(key) {
		return &SyncResult{Mode: SyncManaged}
	}
	return &SyncResult{Mode: SyncAvailable}
}

func (guard *openGuard) InspectIfAvailable(_ context.Context, key string, _ bool) *SyncResult {
	unlock, locked := guard.locks.TryLock(key)
	if !locked {
		return nil
	}
	defer unlock()
	return guard.result(key)
}

func (guard *openGuard) ReconcileIfAvailable(_ context.Context, key string, _ bool, after func(SyncResult)) *SyncResult {
	unlock, locked := guard.locks.TryLock(key)
	if !locked {
		return nil
	}
	defer unlock()
	result := guard.result(key)
	if after != nil {
		after(*result)
	}
	return result
}

func (guard *openGuard) KnownBlocked(string) *SyncResult { return nil }

func (guard *openGuard) Forget(string) {}

func (guard *openGuard) TakeOver(_ context.Context, key string, beforeManaged func() error) (SyncResult, error) {
	unlock := guard.locks.Lock(key)
	defer unlock()
	if beforeManaged != nil {
		if err := beforeManaged(); err != nil {
			return SyncResult{}, err
		}
	}
	return SyncResult{Mode: SyncManaged}, nil
}

func (guard *openGuard) Message(SyncResult) string { return "" }

func (guard *openGuard) WithExclusiveOperation(key string, call func() error) error {
	unlock, locked := guard.locks.TryLock(key)
	if !locked {
		return ErrSyncBusy
	}
	defer unlock()
	return call()
}

func (guard *openGuard) WithMutableClient(ctx context.Context, key string, call func(Client) error) error {
	return guard.WithExclusiveOperation(key, func() error { return guard.clients.WithClient(ctx, key, call) })
}

func (guard *openGuard) WithInterruptClient(ctx context.Context, key string, call func(Client) error) error {
	// A stop must reach a session that another operation holds, such as the prompt it stops.
	return guard.clients.WithInterruptClient(ctx, key, call)
}

func (guard *openGuard) WithBashClient(ctx context.Context, key string, call func(Client) error) error {
	return guard.clients.WithBashClient(ctx, key, call)
}

func (guard *openGuard) WithClientMove(ctx context.Context, key string, touch bool, call func(Client) (string, error), prepare func(string, string) (func() error, error), commit func(string, string)) (string, error) {
	unlock, locked := guard.locks.TryLock(key)
	if !locked {
		return key, ErrSyncBusy
	}
	defer unlock()
	return guard.clients.WithClientMove(ctx, key, touch, call, prepare, commit)
}

func (guard *openGuard) RetireManagedClientIfAvailable(key string, retire func() (bool, error)) (bool, error) {
	unlock, locked := guard.locks.TryLock(key)
	if !locked {
		return false, nil
	}
	defer unlock()
	return retire()
}
