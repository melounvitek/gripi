package sessionfile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/agent/pi/rpc"
	"github.com/melounvitek/gripi/internal/keyedlock"
)

type syncState struct {
	Snapshot  *FileSnapshot
	Mode      agent.SyncMode
	RPCLeafID string
	Error     string
	// External outlives a conflict, which says nothing about Pi CLI being done with the session.
	External bool
}

type Synchronizer struct {
	clients  *agent.Registry
	locks    keyedlock.Mutexes
	statesMu sync.Mutex
	states   map[string]syncState
	// externalPath is the file that lists the sessions in external follow.
	externalPath  string
	baselinesPath string
}

func NewSynchronizer(clients *agent.Registry) *Synchronizer {
	return &Synchronizer{clients: clients, states: make(map[string]syncState)}
}

// PersistExternalFollow restores the sessions Pi CLI was using when the gateway last ran
// and keeps the file at path current. Without it a restart would let the gateway write to
// a session Pi CLI is still working in, until Pi CLI next appended to it.
func (synchronizer *Synchronizer) PersistExternalFollow(path string) error {
	var external []string
	if err := readJSONIfExists(path, &external); err != nil {
		return fmt.Errorf("read external sessions state %s: %w", path, err)
	}
	synchronizer.statesMu.Lock()
	defer synchronizer.statesMu.Unlock()
	synchronizer.externalPath = path
	for _, sessionPath := range external {
		synchronizer.states[sessionPath] = syncState{Mode: agent.SyncExternalFollow, External: true}
	}
	return nil
}

// RestoreBaselines restores how SaveBaselines last found the session files, so that the
// first look at a session notices what Pi CLI appended while the gateway was down, or
// while it ran with nothing looking at the session. Baselines are only a help, so a file
// that cannot be used is skipped.
func (synchronizer *Synchronizer) RestoreBaselines(path string) {
	synchronizer.statesMu.Lock()
	defer synchronizer.statesMu.Unlock()
	synchronizer.baselinesPath = path
	var baselines map[string]FileSnapshot
	// A gateway killed before it saves must not leave baselines that predate its own Pi's writes.
	if readJSON(path, &baselines) != nil || os.Remove(path) != nil {
		return
	}
	for sessionPath, baseline := range baselines {
		info, err := os.Stat(sessionPath)
		if err != nil {
			continue
		}
		// A reboot can renumber devices, which must not read as a replaced file.
		if device, inode := fileIdentity(info); device == baseline.Device && inode == baseline.Inode {
			synchronizer.states[sessionPath] = syncState{Snapshot: &baseline, Mode: agent.SyncAvailable}
		}
	}
}

// SaveBaselines must run only after the gateway's own Pi processes have stopped.
func (synchronizer *Synchronizer) SaveBaselines() error {
	synchronizer.statesMu.Lock()
	defer synchronizer.statesMu.Unlock()
	if synchronizer.baselinesPath == "" {
		return nil
	}
	baselines := map[string]FileSnapshot{}
	for path, state := range synchronizer.states {
		// The gateway's own Pi may have written past the snapshot of a managed session.
		if state.Mode == agent.SyncAvailable && state.Snapshot != nil {
			baselines[path] = *state.Snapshot
		}
	}
	return writeJSON(synchronizer.baselinesPath, baselines)
}

func (synchronizer *Synchronizer) Inspect(ctx context.Context, path string, includePosition bool) (agent.SyncResult, error) {
	unlock := synchronizer.locks.Lock(path)
	defer unlock()
	return synchronizer.inspectRecovering(ctx, path, includePosition)
}

func (synchronizer *Synchronizer) InspectIfAvailable(ctx context.Context, path string, includePosition bool) *agent.SyncResult {
	unlock, locked := synchronizer.locks.TryLock(path)
	if !locked {
		return nil
	}
	defer unlock()
	result := synchronizer.inspectRecoveringAvailable(ctx, path, includePosition)
	return result
}

func (synchronizer *Synchronizer) ReconcileIfAvailable(ctx context.Context, path string, includePosition bool, after func(agent.SyncResult)) *agent.SyncResult {
	result := synchronizer.InspectIfAvailable(ctx, path, includePosition)
	if result != nil && after != nil {
		after(*result)
	}
	return result
}

func (synchronizer *Synchronizer) KnownBlocked(path string) *agent.SyncResult {
	state := synchronizer.state(path)
	// A session restored after a restart has no snapshot until it is first inspected.
	if state.Mode == agent.SyncExternalFollow || (state.Mode == agent.SyncConflict && state.Snapshot != nil) {
		result := resultFor(state)
		return &result
	}
	return nil
}
func (synchronizer *Synchronizer) Forget(path string) {
	synchronizer.statesMu.Lock()
	defer synchronizer.statesMu.Unlock()
	wasExternal := synchronizer.states[path].External
	delete(synchronizer.states, path)
	if wasExternal {
		synchronizer.saveExternalLocked()
	}
}
func (synchronizer *Synchronizer) RetireManagedClientIfAvailable(path string, retire func() (bool, error)) (bool, error) {
	unlock, locked := synchronizer.locks.TryLock(path)
	if !locked {
		return false, nil
	}
	defer unlock()
	state := synchronizer.state(path)
	blocked := state.Mode == agent.SyncExternalFollow || state.Mode == agent.SyncConflict
	retired, err := retire()
	if retired && !blocked {
		synchronizer.Forget(path)
	}
	return retired, err
}
func (synchronizer *Synchronizer) Message(result agent.SyncResult) string {
	return blockedMessage(result.Mode, result.Error)
}

func (synchronizer *Synchronizer) WithExclusiveOperation(path string, call func() error) error {
	unlock, locked := synchronizer.locks.TryLock(path)
	if !locked {
		return agent.ErrSyncBusy
	}
	defer unlock()
	return call()
}

func (synchronizer *Synchronizer) WithMutableClient(ctx context.Context, path string, call func(agent.Client) error) error {
	unlock, locked := synchronizer.locks.TryLock(path)
	if !locked {
		return agent.ErrSyncBusy
	}
	defer unlock()
	before, resultErr := synchronizer.verificationSnapshot(ctx, path)
	if resultErr != nil {
		return resultErr
	}
	return synchronizer.clients.WithClient(ctx, path, func(client agent.Client) error {
		if err := synchronizer.verifyClient(ctx, path, before, client); err != nil {
			return err
		}
		return call(client)
	})
}
func (synchronizer *Synchronizer) WithClientMove(ctx context.Context, path string, touch bool, call func(agent.Client) (string, error), prepare func(string, string) (func() error, error), commit func(string, string)) (string, error) {
	unlock, locked := synchronizer.locks.TryLock(path)
	if !locked {
		return path, agent.ErrSyncBusy
	}
	defer unlock()
	before, err := synchronizer.verificationSnapshot(ctx, path)
	if err != nil {
		return path, err
	}
	return synchronizer.clients.WithClientMove(ctx, path, touch, func(client agent.Client) (string, error) {
		if err := synchronizer.verifyClient(ctx, path, before, client); err != nil {
			return path, err
		}
		return call(client)
	}, prepare, commit)
}

func (synchronizer *Synchronizer) WithBashClient(ctx context.Context, path string, call func(agent.Client) error) error {
	unlock, locked := synchronizer.locks.TryLock(path)
	if !locked {
		return agent.ErrSyncBusy
	}
	defer func() {
		if locked {
			unlock()
		}
	}()
	before, err := synchronizer.verificationSnapshot(ctx, path)
	if err != nil {
		return err
	}
	return synchronizer.clients.WithBashClient(ctx, path, func(client agent.Client) error {
		if err := synchronizer.verifyClient(ctx, path, before, client); err != nil {
			return err
		}
		unlock()
		locked = false
		return call(client)
	})
}
func (synchronizer *Synchronizer) WithInterruptClient(ctx context.Context, path string, call func(agent.Client) error) error {
	unlock, locked := synchronizer.locks.TryLock(path)
	if !locked {
		return synchronizer.clients.WithExistingInterruptClient(ctx, path, call)
	}
	defer unlock()
	if synchronizer.clients.Busy(path) {
		return synchronizer.clients.WithExistingInterruptClient(ctx, path, call)
	}
	before, err := synchronizer.verificationSnapshot(ctx, path)
	if err != nil {
		return err
	}
	err = synchronizer.clients.WithInterruptClient(ctx, path, func(client agent.Client) error {
		if err := synchronizer.verifyClient(ctx, path, before, client); err != nil {
			return err
		}
		return call(client)
	})
	if errors.Is(err, agent.ErrOperationPending) {
		return synchronizer.clients.WithExistingInterruptClient(ctx, path, call)
	}
	return err
}

func (synchronizer *Synchronizer) TakeOver(ctx context.Context, path string, beforeManaged func() error) (agent.SyncResult, error) {
	unlock := synchronizer.locks.Lock(path)
	defer unlock()
	if synchronizer.clients.Busy(path) {
		return agent.SyncResult{}, fmt.Errorf("%w: wait for the gateway task to finish before taking over", agent.ErrSyncBusy)
	}
	_, _ = synchronizer.clients.CloseClientIfIdle(path)
	before, err := readFileSnapshot(path)
	if err != nil {
		return agent.SyncResult{}, err
	}
	if !before.Complete {
		return agent.SyncResult{}, &agent.SyncBlockedError{Mode: agent.SyncConflict, Message: "The session file has an incomplete entry."}
	}
	succeeded := false
	err = synchronizer.clients.WithClient(ctx, path, func(client agent.Client) error {
		reader, err := entriesReader(client)
		if err != nil {
			return err
		}
		position, err := reader.SessionPosition(ctx, before.AppendCursor)
		if err != nil {
			return err
		}
		if err := synchronizer.blockForPosition(path, before, position); err != nil {
			return err
		}
		after, err := readFileSnapshot(path)
		if err != nil {
			return err
		}
		if before.Revision() != after.Revision() || position.LeafID != before.PersistedLeafID {
			synchronizer.update(path, after, agent.SyncExternalFollow, "", "")
			return &agent.SyncBlockedError{Mode: agent.SyncExternalFollow, Message: "The session was updated again during the takeover. Try again once you’re done in Pi CLI."}
		}
		if beforeManaged != nil {
			if err := beforeManaged(); err != nil {
				return err
			}
		}
		synchronizer.update(path, after, agent.SyncManaged, position.LeafID, "")
		succeeded = true
		return nil
	})
	if !succeeded {
		_, _ = synchronizer.clients.CloseClientIfIdle(path)
	}
	if err != nil {
		return agent.SyncResult{}, err
	}
	return resultFor(synchronizer.state(path)), nil
}

func (synchronizer *Synchronizer) inspectRecovering(ctx context.Context, path string, include bool) (agent.SyncResult, error) {
	result, err := synchronizer.inspectLocked(ctx, path, include)
	if err == nil {
		return result, nil
	}
	if terminalSyncRPCError(err) {
		return synchronizer.recoverRPCExit(path), nil
	}
	if errors.Is(err, agent.ErrOperationPending) || errors.Is(err, agent.ErrClientRetiring) || errors.Is(err, agent.ErrClientStarting) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return agent.SyncResult{}, err
	}
	return synchronizer.conflict(path, "Session file could not be read: "+err.Error()), nil
}
func (synchronizer *Synchronizer) inspectRecoveringAvailable(ctx context.Context, path string, include bool) *agent.SyncResult {
	result, err := synchronizer.inspectLocked(ctx, path, include)
	if err == nil {
		return &result
	}
	if errors.Is(err, agent.ErrOperationPending) || errors.Is(err, agent.ErrClientRetiring) || errors.Is(err, agent.ErrClientStarting) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	if terminalSyncRPCError(err) {
		value := synchronizer.recoverRPCExit(path)
		return &value
	}
	value := synchronizer.conflict(path, "Session file could not be read: "+err.Error())
	return &value
}

func (synchronizer *Synchronizer) inspectLocked(ctx context.Context, path string, includePosition bool) (agent.SyncResult, error) {
	state := synchronizer.state(path)
	info, err := os.Stat(path)
	if err != nil {
		return agent.SyncResult{}, err
	}
	device, inode := fileIdentity(info)
	var snapshot FileSnapshot
	if previous := state.Snapshot; previous != nil && previous.Device == device && previous.Inode == inode && previous.Size == info.Size() && previous.MTimeNS == info.ModTime().UnixNano() {
		// Sidebar polling must not reparse large, unchanged tool-result tails.
		snapshot = *previous
	} else {
		snapshot, err = readFileSnapshot(path)
		if err != nil {
			return agent.SyncResult{}, err
		}
	}
	if !snapshot.Complete {
		synchronizer.update(path, snapshot, agent.SyncConflict, "", "Session file has an incomplete JSONL entry.")
		_, err = synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	if state.Mode == agent.SyncExternalFollow || state.Mode == agent.SyncConflict {
		if state.Snapshot == nil || state.Snapshot.Revision() != snapshot.Revision() {
			synchronizer.update(path, snapshot, state.Mode, "", state.Error)
		}
		_, err = synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	changed := state.Snapshot != nil && state.Snapshot.Revision() != snapshot.Revision()
	if changed {
		mode, message := externalOrConflict(*state.Snapshot, snapshot)
		if mode == agent.SyncConflict {
			synchronizer.update(path, snapshot, mode, "", message)
			_, err = synchronizer.clients.CloseClientIfIdle(path)
			return resultFor(synchronizer.state(path)), err
		}
		appended := snapshot.Size > state.Snapshot.Size || snapshot.AppendCursor != state.Snapshot.AppendCursor
		if appended {
			if !synchronizer.clients.Active(path) {
				// A gateway that is shutting down has stopped its own Pi processes, so what they wrote last would pass for Pi CLI's.
				if synchronizer.clients.Closed() {
					return resultFor(state), nil
				}
				synchronizer.update(path, snapshot, agent.SyncExternalFollow, "", "")
				return resultFor(synchronizer.state(path)), nil
			}
			var reconciliation rpc.SessionEntries
			err = synchronizer.clients.WithExistingClient(ctx, path, false, func(client agent.Client) error {
				reader, callErr := entriesReader(client)
				if callErr != nil {
					return callErr
				}
				reconciliation, callErr = reader.SessionEntriesAfter(ctx, state.Snapshot.AppendCursor)
				return callErr
			})
			if err != nil {
				return agent.SyncResult{}, err
			}
			return synchronizer.applyReconciliation(path, *state.Snapshot, snapshot, reconciliation)
		}
		mode = state.Mode
		if mode == "" {
			mode = agent.SyncAvailable
		}
		synchronizer.update(path, snapshot, mode, state.RPCLeafID, "")
	}
	state = synchronizer.state(path)
	if synchronizer.clients.Active(path) && (includePosition || state.Mode != agent.SyncManaged) {
		if state.Snapshot == nil {
			synchronizer.update(path, snapshot, firstMode(state.Mode), state.RPCLeafID, state.Error)
		}
		var position rpc.SessionEntries
		err = synchronizer.clients.WithExistingClient(ctx, path, false, func(client agent.Client) error {
			reader, callErr := entriesReader(client)
			if callErr != nil {
				return callErr
			}
			position, callErr = reader.SessionPosition(ctx, snapshot.AppendCursor)
			return callErr
		})
		if err != nil {
			return agent.SyncResult{}, err
		}
		return synchronizer.applyPosition(path, snapshot, position)
	}
	if !synchronizer.clients.Active(path) {
		synchronizer.update(path, snapshot, agent.SyncAvailable, "", "")
	}
	return resultFor(synchronizer.state(path)), nil
}

func (synchronizer *Synchronizer) verificationSnapshot(ctx context.Context, path string) (FileSnapshot, error) {
	result, err := synchronizer.inspectLocked(ctx, path, false)
	if err != nil {
		return FileSnapshot{}, err
	}
	if result.Blocked() {
		return FileSnapshot{}, &agent.SyncBlockedError{Mode: result.Mode, Message: blockedMessage(result.Mode, result.Error)}
	}
	state := synchronizer.state(path)
	if state.Snapshot == nil {
		return FileSnapshot{}, errors.New("session snapshot is unavailable")
	}
	return *state.Snapshot, nil
}
func (synchronizer *Synchronizer) verifyClient(ctx context.Context, path string, before FileSnapshot, client agent.Client) error {
	reader, err := entriesReader(client)
	if err != nil {
		return err
	}
	position, err := reader.SessionPosition(ctx, before.AppendCursor)
	if err != nil {
		return err
	}
	if err = synchronizer.blockForPosition(path, before, position); err != nil {
		return err
	}
	after, err := readFileSnapshot(path)
	if err != nil {
		return err
	}
	if before.Revision() != after.Revision() {
		reconciliation, err := reader.SessionEntriesAfter(ctx, before.AppendCursor)
		if err != nil {
			return err
		}
		result, err := synchronizer.applyReconciliation(path, before, after, reconciliation)
		if err != nil {
			return err
		}
		if result.Blocked() {
			return &agent.SyncBlockedError{Mode: result.Mode, Message: blockedMessage(result.Mode, result.Error)}
		}
		return nil
	}
	synchronizer.update(path, after, agent.SyncManaged, position.LeafID, "")
	return nil
}
func (synchronizer *Synchronizer) blockForPosition(path string, snapshot FileSnapshot, position rpc.SessionEntries) error {
	if position.Error != "" {
		synchronizer.update(path, snapshot, agent.SyncConflict, "", position.Error)
		return &agent.SyncBlockedError{Mode: agent.SyncConflict, Message: blockedMessage(agent.SyncConflict, position.Error)}
	}
	if !position.Known {
		synchronizer.update(path, snapshot, agent.SyncExternalFollow, "", "")
		return &agent.SyncBlockedError{Mode: agent.SyncExternalFollow, Message: blockedMessage(agent.SyncExternalFollow, "")}
	}
	return nil
}
func (synchronizer *Synchronizer) applyReconciliation(path string, previous, current FileSnapshot, reconciliation rpc.SessionEntries) (agent.SyncResult, error) {
	if reconciliation.Error != "" {
		synchronizer.update(path, current, agent.SyncConflict, "", reconciliation.Error)
		_, err := synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	if !reconciliation.Known {
		synchronizer.update(path, current, agent.SyncExternalFollow, "", "")
		_, err := synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	diskIDs, err := appendedEntryIDs(path, previous, current)
	if err != nil {
		message := "Session append reconciliation failed: " + err.Error()
		synchronizer.update(path, current, agent.SyncConflict, "", message)
		_, closeErr := synchronizer.clients.CloseClientIfIdle(path)
		if closeErr != nil {
			return resultFor(synchronizer.state(path)), closeErr
		}
		return resultFor(synchronizer.state(path)), nil
	}
	if len(reconciliation.Entries) < len(diskIDs) {
		synchronizer.update(path, current, agent.SyncExternalFollow, "", "")
		_, err = synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	for index, id := range diskIDs {
		if stringFromMap(reconciliation.Entries[index], "id") != id {
			synchronizer.update(path, current, agent.SyncExternalFollow, "", "")
			_, err = synchronizer.clients.CloseClientIfIdle(path)
			return resultFor(synchronizer.state(path)), err
		}
	}
	synchronizer.update(path, current, agent.SyncManaged, reconciliation.LeafID, "")
	return resultFor(synchronizer.state(path)), nil
}
func (synchronizer *Synchronizer) applyPosition(path string, snapshot FileSnapshot, position rpc.SessionEntries) (agent.SyncResult, error) {
	if position.Error != "" {
		synchronizer.update(path, snapshot, agent.SyncConflict, "", position.Error)
		_, err := synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	if !position.Known {
		synchronizer.update(path, snapshot, agent.SyncExternalFollow, "", "")
		_, err := synchronizer.clients.CloseClientIfIdle(path)
		return resultFor(synchronizer.state(path)), err
	}
	synchronizer.update(path, snapshot, agent.SyncManaged, position.LeafID, "")
	return resultFor(synchronizer.state(path)), nil
}

func (synchronizer *Synchronizer) recoverRPCExit(path string) agent.SyncResult {
	snapshot, err := readFileSnapshot(path)
	if err != nil {
		return synchronizer.conflict(path, "Session file could not be read: "+err.Error())
	}
	state := synchronizer.state(path)
	mode := agent.SyncAvailable
	if state.Snapshot != nil && state.Snapshot.Revision() != snapshot.Revision() {
		mode = agent.SyncExternalFollow
	}
	synchronizer.update(path, snapshot, mode, "", "")
	return resultFor(synchronizer.state(path))
}
func (synchronizer *Synchronizer) conflict(path, message string) agent.SyncResult {
	state := synchronizer.state(path)
	state.Mode = agent.SyncConflict
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
	defer synchronizer.statesMu.Unlock()
	wasExternal := synchronizer.states[path].External
	state.External = state.Mode == agent.SyncExternalFollow || (state.Mode == agent.SyncConflict && wasExternal)
	synchronizer.states[path] = state
	if wasExternal != state.External {
		synchronizer.saveExternalLocked()
	}
}
func (synchronizer *Synchronizer) saveExternalLocked() {
	if synchronizer.externalPath == "" {
		return
	}
	external := []string{}
	for path, state := range synchronizer.states {
		if state.External {
			external = append(external, path)
		}
	}
	// The mode still holds in memory, so a failed save only costs it at the next restart.
	if err := writeJSON(synchronizer.externalPath, external); err != nil {
		log.Printf("save external sessions state: %v", err)
	}
}
func (synchronizer *Synchronizer) update(path string, snapshot FileSnapshot, mode agent.SyncMode, leaf, message string) {
	copy := snapshot
	synchronizer.setState(path, syncState{Snapshot: &copy, Mode: mode, RPCLeafID: leaf, Error: message})
}
func resultFor(state syncState) agent.SyncResult {
	result := agent.SyncResult{Mode: state.Mode, RPCLeafID: state.RPCLeafID, Error: state.Error}
	if result.Mode == "" {
		result.Mode = agent.SyncAvailable
	}
	if state.Snapshot != nil {
		result.Revision = state.Snapshot.Revision()
		result.AppendCursor = state.Snapshot.AppendCursor
		result.PersistedLeafID = state.Snapshot.PersistedLeafID
	}
	return result
}
func externalOrConflict(previous, current FileSnapshot) (agent.SyncMode, string) {
	if previous.Device != current.Device || previous.Inode != current.Inode || current.Size < previous.Size {
		return agent.SyncConflict, "Session file was replaced or truncated."
	}
	if current.Size > previous.Size && current.AppendCursor == previous.AppendCursor {
		return agent.SyncConflict, "Session file changed without a complete appended entry."
	}
	if current.Size == previous.Size && current.AppendCursor != previous.AppendCursor {
		return agent.SyncConflict, "Session file changed without an append."
	}
	return agent.SyncExternalFollow, ""
}
func blockedMessage(mode agent.SyncMode, message string) string {
	if mode == agent.SyncConflict {
		return "Session synchronization failed: " + message
	}
	return "This session was updated in Pi CLI. Take over once you’re done there."
}
func firstMode(mode agent.SyncMode) agent.SyncMode {
	if mode == "" {
		return agent.SyncAvailable
	}
	return mode
}
func stringFromMap(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}
func terminalSyncRPCError(err error) bool {
	var timeout *agent.RequestTimeoutError
	return errors.As(err, &timeout) || errors.Is(err, agent.ErrProcessExited)
}

// entriesReader reads Pi's session entries through a client from the registry, which has the core type.
func entriesReader(client agent.Client) (rpc.SessionEntriesReader, error) {
	reader, ok := client.(rpc.SessionEntriesReader)
	if !ok {
		return nil, fmt.Errorf("%T cannot read Pi session entries", client)
	}
	return reader, nil
}
