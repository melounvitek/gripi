package sessions

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/melounvitek/gripi/internal/rpc"
)

type SyncMode string

const (
	SyncAvailable      SyncMode = "available"
	SyncManaged        SyncMode = "managed"
	SyncExternalFollow SyncMode = "external_follow"
	SyncConflict       SyncMode = "conflict"
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

type syncState struct {
	Snapshot  *FileSnapshot
	Mode      SyncMode
	RPCLeafID string
	Error     string
}

type Synchronizer struct {
	store    Store
	clients  *rpc.Registry
	locksMu  sync.Mutex
	locks    map[string]*sync.Mutex
	statesMu sync.Mutex
	states   map[string]syncState
}

func NewSynchronizer(root, home string, cache *Cache, clients *rpc.Registry) *Synchronizer {
	return &Synchronizer{store: Store{Root: root, Home: home, Cache: cache}, clients: clients, locks: make(map[string]*sync.Mutex), states: make(map[string]syncState)}
}

func (synchronizer *Synchronizer) Inspect(ctx context.Context, path string, includePosition bool) (SyncResult, error) {
	lock := synchronizer.lockFor(path)
	lock.Lock()
	defer lock.Unlock()
	return synchronizer.inspectRecovering(ctx, path, includePosition)
}

func (synchronizer *Synchronizer) InspectIfAvailable(ctx context.Context, path string, includePosition bool) *SyncResult {
	lock := synchronizer.lockFor(path)
	if !lock.TryLock() {
		return nil
	}
	defer lock.Unlock()
	result := synchronizer.inspectRecoveringAvailable(ctx, path, includePosition)
	return result
}

func (synchronizer *Synchronizer) ReconcileIfAvailable(ctx context.Context, path string, includePosition bool, after func(SyncResult)) *SyncResult {
	result := synchronizer.InspectIfAvailable(ctx, path, includePosition)
	if result != nil && after != nil {
		after(*result)
	}
	return result
}

func (synchronizer *Synchronizer) KnownBlocked(path string) *SyncResult {
	state := synchronizer.state(path)
	if state.Snapshot != nil && (state.Mode == SyncExternalFollow || state.Mode == SyncConflict) {
		result := resultFor(state)
		return &result
	}
	return nil
}
func (synchronizer *Synchronizer) Forget(path string) {
	synchronizer.statesMu.Lock()
	delete(synchronizer.states, path)
	synchronizer.statesMu.Unlock()
}
func (synchronizer *Synchronizer) Message(result SyncResult) string {
	return blockedMessage(result.Mode, result.Error)
}

func (synchronizer *Synchronizer) WithMutableClient(ctx context.Context, path string, call func(rpc.RPCClient) error) error {
	lock := synchronizer.lockFor(path)
	if !lock.TryLock() {
		return ErrSyncBusy
	}
	defer lock.Unlock()
	before, resultErr := synchronizer.verificationSnapshot(ctx, path)
	if resultErr != nil {
		return resultErr
	}
	return synchronizer.clients.WithClient(ctx, path, func(client rpc.RPCClient) error {
		if err := synchronizer.verifyClient(ctx, path, before, client); err != nil {
			return err
		}
		return call(client)
	})
}
func (synchronizer *Synchronizer) WithClientMove(ctx context.Context, path string, touch bool, call func(rpc.RPCClient) (string, error), prepare func(string, string) (func() error, error), commit func(string, string)) (string, error) {
	lock := synchronizer.lockFor(path)
	if !lock.TryLock() {
		return path, ErrSyncBusy
	}
	defer lock.Unlock()
	before, err := synchronizer.verificationSnapshot(ctx, path)
	if err != nil {
		return path, err
	}
	return synchronizer.clients.WithClientMove(ctx, path, touch, func(client rpc.RPCClient) (string, error) {
		if err := synchronizer.verifyClient(ctx, path, before, client); err != nil {
			return path, err
		}
		return call(client)
	}, prepare, commit)
}

func (synchronizer *Synchronizer) WithBashClient(ctx context.Context, path string, call func(rpc.RPCClient) error) error {
	lock := synchronizer.lockFor(path)
	if !lock.TryLock() {
		return ErrSyncBusy
	}
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()
	before, err := synchronizer.verificationSnapshot(ctx, path)
	if err != nil {
		return err
	}
	return synchronizer.clients.WithBashClient(ctx, path, func(client rpc.RPCClient) error {
		if err := synchronizer.verifyClient(ctx, path, before, client); err != nil {
			return err
		}
		lock.Unlock()
		locked = false
		return call(client)
	})
}
func (synchronizer *Synchronizer) WithInterruptClient(ctx context.Context, path string, call func(rpc.RPCClient) error) error {
	lock := synchronizer.lockFor(path)
	if !lock.TryLock() {
		return synchronizer.clients.WithExistingInterruptClient(ctx, path, call)
	}
	defer lock.Unlock()
	if synchronizer.clients.Busy(path) {
		return synchronizer.clients.WithExistingInterruptClient(ctx, path, call)
	}
	before, err := synchronizer.verificationSnapshot(ctx, path)
	if err != nil {
		return err
	}
	err = synchronizer.clients.WithInterruptClient(ctx, path, func(client rpc.RPCClient) error {
		if err := synchronizer.verifyClient(ctx, path, before, client); err != nil {
			return err
		}
		return call(client)
	})
	if errors.Is(err, rpc.ErrOperationPending) {
		return synchronizer.clients.WithExistingInterruptClient(ctx, path, call)
	}
	return err
}

func (synchronizer *Synchronizer) TakeOver(ctx context.Context, path string) (SyncResult, error) {
	lock := synchronizer.lockFor(path)
	lock.Lock()
	defer lock.Unlock()
	if synchronizer.clients.Busy(path) {
		return SyncResult{}, fmt.Errorf("%w: wait for the gateway task to finish before taking over", ErrSyncBusy)
	}
	_, _ = synchronizer.clients.CloseClientIfIdle(path)
	before, err := synchronizer.store.FileSnapshot(path)
	if err != nil {
		return SyncResult{}, err
	}
	if !before.Complete {
		return SyncResult{}, &SyncBlockedError{Mode: SyncConflict, Message: "The session file has an incomplete entry."}
	}
	succeeded := false
	err = synchronizer.clients.WithClient(ctx, path, func(client rpc.RPCClient) error {
		position, err := client.SessionPosition(ctx, before.AppendCursor)
		if err != nil {
			return err
		}
		if err := synchronizer.blockForPosition(path, before, position); err != nil {
			return err
		}
		after, err := synchronizer.store.FileSnapshot(path)
		if err != nil {
			return err
		}
		if before.Revision() != after.Revision() || position.LeafID != before.PersistedLeafID {
			synchronizer.update(path, after, SyncExternalFollow, "", "")
			return &SyncBlockedError{Mode: SyncExternalFollow, Message: "The session changed while the gateway was taking over. Finish using it in Pi CLI and try again."}
		}
		synchronizer.update(path, after, SyncManaged, position.LeafID, "")
		succeeded = true
		return nil
	})
	if !succeeded {
		_, _ = synchronizer.clients.CloseClientIfIdle(path)
	}
	if err != nil {
		return SyncResult{}, err
	}
	return resultFor(synchronizer.state(path)), nil
}

func (synchronizer *Synchronizer) inspectRecovering(ctx context.Context, path string, include bool) (SyncResult, error) {
	result, err := synchronizer.inspectLocked(ctx, path, include)
	if err == nil {
		return result, nil
	}
	if terminalSyncRPCError(err) {
		return synchronizer.recoverRPCExit(path), nil
	}
	if errors.Is(err, rpc.ErrOperationPending) || errors.Is(err, rpc.ErrClientRetiring) || errors.Is(err, rpc.ErrClientStarting) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return SyncResult{}, err
	}
	return synchronizer.conflict(path, "Session file could not be read: "+err.Error()), nil
}
func (synchronizer *Synchronizer) inspectRecoveringAvailable(ctx context.Context, path string, include bool) *SyncResult {
	result, err := synchronizer.inspectLocked(ctx, path, include)
	if err == nil {
		return &result
	}
	if errors.Is(err, rpc.ErrOperationPending) || errors.Is(err, rpc.ErrClientRetiring) || errors.Is(err, rpc.ErrClientStarting) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	if terminalSyncRPCError(err) {
		value := synchronizer.recoverRPCExit(path)
		return &value
	}
	value := synchronizer.conflict(path, "Session file could not be read: "+err.Error())
	return &value
}

func (synchronizer *Synchronizer) inspectLocked(ctx context.Context, path string, includePosition bool) (SyncResult, error) {
	snapshot, err := synchronizer.store.FileSnapshot(path)
	if err != nil {
		return SyncResult{}, err
	}
	state := synchronizer.state(path)
	if !snapshot.Complete {
		synchronizer.update(path, snapshot, SyncConflict, "", "Session file has an incomplete JSONL entry.")
		_, err = synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	if state.Mode == SyncExternalFollow || state.Mode == SyncConflict {
		if state.Snapshot == nil || state.Snapshot.Revision() != snapshot.Revision() {
			synchronizer.update(path, snapshot, state.Mode, "", state.Error)
		}
		_, err = synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	changed := state.Snapshot != nil && state.Snapshot.Revision() != snapshot.Revision()
	if changed {
		mode, message := externalOrConflict(*state.Snapshot, snapshot)
		if mode == SyncConflict {
			synchronizer.update(path, snapshot, mode, "", message)
			_, err = synchronizer.clients.CloseClientIfIdle(path)
			return resultFor(synchronizer.state(path)), err
		}
		appended := snapshot.Size > state.Snapshot.Size || snapshot.AppendCursor != state.Snapshot.AppendCursor
		if appended {
			if !synchronizer.clients.Active(path) {
				synchronizer.update(path, snapshot, SyncExternalFollow, "", "")
				return resultFor(synchronizer.state(path)), nil
			}
			var reconciliation rpc.SessionEntries
			err = synchronizer.clients.WithExistingClient(ctx, path, false, func(client rpc.RPCClient) error {
				var callErr error
				reconciliation, callErr = client.SessionEntriesAfter(ctx, state.Snapshot.AppendCursor)
				return callErr
			})
			if err != nil {
				return SyncResult{}, err
			}
			return synchronizer.applyReconciliation(path, *state.Snapshot, snapshot, reconciliation)
		}
		mode = state.Mode
		if mode == "" {
			mode = SyncAvailable
		}
		synchronizer.update(path, snapshot, mode, state.RPCLeafID, "")
	}
	state = synchronizer.state(path)
	if synchronizer.clients.Active(path) && (includePosition || state.Mode != SyncManaged) {
		if state.Snapshot == nil {
			synchronizer.update(path, snapshot, firstMode(state.Mode), state.RPCLeafID, state.Error)
		}
		var position rpc.SessionEntries
		err = synchronizer.clients.WithExistingClient(ctx, path, false, func(client rpc.RPCClient) error {
			var callErr error
			position, callErr = client.SessionPosition(ctx, snapshot.AppendCursor)
			return callErr
		})
		if err != nil {
			return SyncResult{}, err
		}
		return synchronizer.applyPosition(path, snapshot, position)
	}
	if !synchronizer.clients.Active(path) {
		synchronizer.update(path, snapshot, SyncAvailable, "", "")
	}
	return resultFor(synchronizer.state(path)), nil
}

func (synchronizer *Synchronizer) verificationSnapshot(ctx context.Context, path string) (FileSnapshot, error) {
	result, err := synchronizer.inspectLocked(ctx, path, false)
	if err != nil {
		return FileSnapshot{}, err
	}
	if result.Blocked() {
		return FileSnapshot{}, &SyncBlockedError{Mode: result.Mode, Message: blockedMessage(result.Mode, result.Error)}
	}
	state := synchronizer.state(path)
	if state.Snapshot == nil {
		return FileSnapshot{}, errors.New("session snapshot is unavailable")
	}
	return *state.Snapshot, nil
}
func (synchronizer *Synchronizer) verifyClient(ctx context.Context, path string, before FileSnapshot, client rpc.RPCClient) error {
	position, err := client.SessionPosition(ctx, before.AppendCursor)
	if err != nil {
		return err
	}
	if err = synchronizer.blockForPosition(path, before, position); err != nil {
		return err
	}
	after, err := synchronizer.store.FileSnapshot(path)
	if err != nil {
		return err
	}
	if before.Revision() != after.Revision() {
		reconciliation, err := client.SessionEntriesAfter(ctx, before.AppendCursor)
		if err != nil {
			return err
		}
		result, err := synchronizer.applyReconciliation(path, before, after, reconciliation)
		if err != nil {
			return err
		}
		if result.Blocked() {
			return &SyncBlockedError{Mode: result.Mode, Message: blockedMessage(result.Mode, result.Error)}
		}
		return nil
	}
	synchronizer.update(path, after, SyncManaged, position.LeafID, "")
	return nil
}
func (synchronizer *Synchronizer) blockForPosition(path string, snapshot FileSnapshot, position rpc.SessionEntries) error {
	if position.Error != "" {
		synchronizer.update(path, snapshot, SyncConflict, "", position.Error)
		return &SyncBlockedError{Mode: SyncConflict, Message: blockedMessage(SyncConflict, position.Error)}
	}
	if !position.Known {
		synchronizer.update(path, snapshot, SyncExternalFollow, "", "")
		return &SyncBlockedError{Mode: SyncExternalFollow, Message: blockedMessage(SyncExternalFollow, "")}
	}
	return nil
}
func (synchronizer *Synchronizer) applyReconciliation(path string, previous, current FileSnapshot, reconciliation rpc.SessionEntries) (SyncResult, error) {
	if reconciliation.Error != "" {
		synchronizer.update(path, current, SyncConflict, "", reconciliation.Error)
		_, err := synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	if !reconciliation.Known {
		synchronizer.update(path, current, SyncExternalFollow, "", "")
		_, err := synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	diskIDs, err := synchronizer.store.AppendedEntryIDs(path, previous, current)
	if err != nil {
		message := "Session append reconciliation failed: " + err.Error()
		synchronizer.update(path, current, SyncConflict, "", message)
		_, closeErr := synchronizer.clients.CloseClientIfIdle(path)
		if closeErr != nil {
			return resultFor(synchronizer.state(path)), closeErr
		}
		return resultFor(synchronizer.state(path)), nil
	}
	if len(reconciliation.Entries) < len(diskIDs) {
		synchronizer.update(path, current, SyncExternalFollow, "", "")
		_, err = synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	for index, id := range diskIDs {
		if stringFromMap(reconciliation.Entries[index], "id") != id {
			synchronizer.update(path, current, SyncExternalFollow, "", "")
			_, err = synchronizer.clients.CloseClientIfIdle(path)
			return resultFor(synchronizer.state(path)), err
		}
	}
	synchronizer.update(path, current, SyncManaged, reconciliation.LeafID, "")
	return resultFor(synchronizer.state(path)), nil
}
func (synchronizer *Synchronizer) applyPosition(path string, snapshot FileSnapshot, position rpc.SessionEntries) (SyncResult, error) {
	if position.Error != "" {
		synchronizer.update(path, snapshot, SyncConflict, "", position.Error)
		_, err := synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	if !position.Known {
		synchronizer.update(path, snapshot, SyncExternalFollow, "", "")
		_, err := synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	synchronizer.update(path, snapshot, SyncManaged, position.LeafID, "")
	return resultFor(synchronizer.state(path)), nil
}

func (synchronizer *Synchronizer) recoverRPCExit(path string) SyncResult {
	snapshot, err := synchronizer.store.FileSnapshot(path)
	if err != nil {
		return synchronizer.conflict(path, "Session file could not be read: "+err.Error())
	}
	state := synchronizer.state(path)
	mode := SyncAvailable
	if state.Snapshot != nil && state.Snapshot.Revision() != snapshot.Revision() {
		mode = SyncExternalFollow
	}
	synchronizer.update(path, snapshot, mode, "", "")
	return resultFor(synchronizer.state(path))
}
func (synchronizer *Synchronizer) conflict(path, message string) SyncResult {
	state := synchronizer.state(path)
	state.Mode = SyncConflict
	state.Error = message
	synchronizer.setState(path, state)
	return resultFor(state)
}
func (synchronizer *Synchronizer) state(path string) syncState {
	synchronizer.statesMu.Lock()
	defer synchronizer.statesMu.Unlock()
	return synchronizer.states[path]
}
func (synchronizer *Synchronizer) setState(path string, state syncState) {
	synchronizer.statesMu.Lock()
	synchronizer.states[path] = state
	synchronizer.statesMu.Unlock()
}
func (synchronizer *Synchronizer) update(path string, snapshot FileSnapshot, mode SyncMode, leaf, message string) {
	copy := snapshot
	synchronizer.setState(path, syncState{Snapshot: &copy, Mode: mode, RPCLeafID: leaf, Error: message})
}
func (synchronizer *Synchronizer) lockFor(path string) *sync.Mutex {
	synchronizer.locksMu.Lock()
	defer synchronizer.locksMu.Unlock()
	if synchronizer.locks[path] == nil {
		synchronizer.locks[path] = &sync.Mutex{}
	}
	return synchronizer.locks[path]
}
func resultFor(state syncState) SyncResult {
	result := SyncResult{Mode: state.Mode, RPCLeafID: state.RPCLeafID, Error: state.Error}
	if result.Mode == "" {
		result.Mode = SyncAvailable
	}
	if state.Snapshot != nil {
		result.Revision = state.Snapshot.Revision()
		result.AppendCursor = state.Snapshot.AppendCursor
		result.PersistedLeafID = state.Snapshot.PersistedLeafID
	}
	return result
}
func externalOrConflict(previous, current FileSnapshot) (SyncMode, string) {
	if previous.Device != current.Device || previous.Inode != current.Inode || current.Size < previous.Size {
		return SyncConflict, "Session file was replaced or truncated."
	}
	if current.Size > previous.Size && current.AppendCursor == previous.AppendCursor {
		return SyncConflict, "Session file changed without a complete appended entry."
	}
	if current.Size == previous.Size && current.AppendCursor != previous.AppendCursor {
		return SyncConflict, "Session file changed without an append."
	}
	return SyncExternalFollow, ""
}
func blockedMessage(mode SyncMode, message string) string {
	if mode == SyncConflict {
		return "Session synchronization failed: " + message
	}
	return "This session changed outside the gateway. Finish using it in Pi CLI, then take over in the gateway."
}
func firstMode(mode SyncMode) SyncMode {
	if mode == "" {
		return SyncAvailable
	}
	return mode
}
func stringFromMap(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}
func terminalSyncRPCError(err error) bool {
	var timeout *rpc.RequestTimeoutError
	return errors.As(err, &timeout) || errors.Is(err, rpc.ErrProcessExited)
}
