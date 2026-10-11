package agent

import (
	"context"
	"errors"
)

// Guard protects sessions against writers outside the gateway, such as the agent's own CLI working on a session
// that the gateway also runs. While someone else writes a session, the gateway follows it read-only until the
// user takes it over. An agent whose sessions nobody else writes would pass every call through to the registry.
type Guard interface {
	// Inspect looks for outside writes to a saved session. With includePosition it also asks a running client
	// where it is in the session.
	Inspect(ctx context.Context, key string, includePosition bool) (SyncResult, error)
	// InspectIfAvailable inspects like Inspect, unless another operation holds the session: then it returns nil.
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
