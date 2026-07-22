package rpc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrOperationPending = errors.New("another operation is already pending for this session")
	ErrInterruptPending = errors.New("another interrupt is already pending for this session")
	ErrBashPending      = errors.New("another bash is already pending for this session")
	ErrClientRetiring   = errors.New("Pi RPC client is restarting")
	ErrClientStarting   = errors.New("Pi RPC client is starting")
)

type clientEntry struct {
	client         RPCClient
	lastUsedAt     time.Time
	activeRequests int
	observers      int
	operationLane  chan struct{}
	interruptLane  chan struct{}
	bashLane       chan struct{}
	retiring       bool
}

type Registry struct {
	factory          func(string) (RPCClient, error)
	clock            func() time.Time
	diagnostics      *Diagnostics
	mu               sync.Mutex
	clients          map[string]*clientEntry
	creating         map[string]uint64
	nextCreation     uint64
	closed           bool
	factoryWG        sync.WaitGroup
	moveWG           sync.WaitGroup
	shutdownDone     chan struct{}
	shutdownErr      error
	resumeOnShutdown bool
}

func NewRegistry(factory func(string) (RPCClient, error), clock func() time.Time) *Registry {
	if clock == nil {
		clock = time.Now
	}
	return &Registry{factory: factory, clock: clock, clients: make(map[string]*clientEntry), creating: make(map[string]uint64)}
}

func (registry *Registry) SetDiagnostics(diagnostics *Diagnostics) {
	registry.mu.Lock()
	registry.diagnostics = diagnostics
	registry.mu.Unlock()
}

func newClientEntry(client RPCClient, now time.Time) *clientEntry {
	entry := &clientEntry{client: client, lastUsedAt: now, operationLane: make(chan struct{}, 1), interruptLane: make(chan struct{}, 1), bashLane: make(chan struct{}, 1)}
	entry.operationLane <- struct{}{}
	entry.interruptLane <- struct{}{}
	entry.bashLane <- struct{}{}
	return entry
}

func (registry *Registry) EnsureClient(path string) (RPCClient, error) {
	entry, err := registry.acquire(path, true, true, false)
	if err != nil || entry == nil {
		return nil, err
	}
	defer registry.release(entry, true, false)
	return entry.client, nil
}

func (registry *Registry) Register(path string, client RPCClient) error {
	var old RPCClient
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return ErrClientRetiring
	}
	if registry.creating[path] != 0 {
		registry.mu.Unlock()
		return ErrClientStarting
	}
	current := registry.clients[path]
	if current != nil && current.retiring {
		registry.mu.Unlock()
		return ErrClientRetiring
	}
	if current != nil && current.client != client && (current.activeRequests > 0 || current.client.Busy()) {
		registry.mu.Unlock()
		return ErrOperationPending
	}
	if current == nil || current.client != client {
		if current != nil {
			old = current.client
		}
		registry.clients[path] = newClientEntry(client, registry.clock())
	}
	diagnostics := registry.diagnostics
	registry.mu.Unlock()
	if old != nil {
		if err := old.Close(); err != nil {
			diagnostics.Log("replaced_client_close_failed", map[string]any{"session": path, "error": err.Error()})
		}
	}
	return nil
}

func (registry *Registry) Client(path string) RPCClient {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry := registry.clients[path]
	if entry != nil && !entry.retiring {
		return entry.client
	}
	return nil
}
func (registry *Registry) Active(path string) bool { return registry.Client(path) != nil }
func (registry *Registry) Touch(path string) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry := registry.clients[path]
	if entry != nil {
		entry.lastUsedAt = registry.clock()
		return true
	}
	return false
}
func (registry *Registry) EventSequence(path string) int64 {
	if client := registry.Client(path); client != nil {
		return client.EventSequence()
	}
	return 0
}
func (registry *Registry) EventReplayCursor(path string) int64 {
	if client := registry.Client(path); client != nil {
		return client.EventReplayCursor()
	}
	return 0
}
func (registry *Registry) Busy(path string) bool {
	if client := registry.Client(path); client != nil {
		return client.Busy()
	}
	return false
}
func (registry *Registry) BusySince(path string) *time.Time {
	if client := registry.Client(path); client != nil {
		return client.BusySince()
	}
	return nil
}
func (registry *Registry) AgentRunning(path string) bool {
	if client := registry.Client(path); client != nil {
		return client.AgentRunning()
	}
	return false
}
func (registry *Registry) Compacting(path string) bool {
	if client := registry.Client(path); client != nil {
		return client.Compacting()
	}
	return false
}
func (registry *Registry) LiveSnapshot(path string) LiveSnapshot {
	if client := registry.Client(path); client != nil {
		return client.LiveSnapshot()
	}
	return LiveSnapshot{ActiveToolEvents: []map[string]any{}}
}

func (registry *Registry) BusySessionCount() int {
	registry.mu.Lock()
	clients := make([]RPCClient, 0, len(registry.clients))
	for _, entry := range registry.clients {
		if !entry.retiring {
			clients = append(clients, entry.client)
		}
	}
	registry.mu.Unlock()
	count := 0
	for _, client := range clients {
		if client.Busy() {
			count++
		}
	}
	return count
}

type lane int

const (
	laneNone lane = iota
	laneOperation
	laneInterrupt
	laneBash
)

func (registry *Registry) WithClient(ctx context.Context, path string, call func(RPCClient) error) error {
	return registry.with(ctx, path, laneOperation, true, true, false, call)
}
func (registry *Registry) WithExistingClient(ctx context.Context, path string, touch bool, call func(RPCClient) error) error {
	return registry.with(ctx, path, laneOperation, false, touch, false, call)
}
func (registry *Registry) WithInterruptClient(ctx context.Context, path string, call func(RPCClient) error) error {
	return registry.with(ctx, path, laneInterrupt, true, true, false, call)
}
func (registry *Registry) WithExistingInterruptClient(ctx context.Context, path string, call func(RPCClient) error) error {
	return registry.with(ctx, path, laneInterrupt, false, true, false, call)
}
func (registry *Registry) WithBashClient(ctx context.Context, path string, call func(RPCClient) error) error {
	return registry.with(ctx, path, laneBash, true, true, false, call)
}
func (registry *Registry) WithActiveClient(ctx context.Context, path string, touch bool, call func(RPCClient) error) error {
	return registry.with(ctx, path, laneNone, false, touch, false, call)
}
func (registry *Registry) WithObservingClient(ctx context.Context, path string, touch bool, call func(RPCClient) error) error {
	return registry.with(ctx, path, laneNone, false, touch, true, call)
}

func (registry *Registry) with(ctx context.Context, path string, serial lane, create, touch, observer bool, call func(RPCClient) error) error {
	entry, err := registry.acquire(path, create, touch, observer)
	if err != nil || entry == nil {
		return err
	}
	defer registry.release(entry, touch, observer)
	var token chan struct{}
	var pending error
	switch serial {
	case laneOperation:
		token = entry.operationLane
		pending = ErrOperationPending
	case laneInterrupt:
		token = entry.interruptLane
		pending = ErrInterruptPending
	case laneBash:
		token = entry.bashLane
		pending = ErrBashPending
	}
	if token != nil {
		select {
		case <-token:
			defer func() { token <- struct{}{} }()
		default:
			registry.logRejection(path, serial)
			return pending
		}
	}
	err = call(entry.client)
	if terminalRPCError(err) {
		registry.discard(entry, err)
	}
	return err
}

func (registry *Registry) acquire(path string, create, touch, observer bool) (*clientEntry, error) {
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return nil, ErrClientRetiring
	}
	if existing := registry.clients[path]; existing != nil {
		if existing.retiring {
			registry.mu.Unlock()
			return nil, ErrClientRetiring
		}
		existing.activeRequests++
		if observer {
			existing.observers++
		}
		if touch {
			existing.lastUsedAt = registry.clock()
		}
		registry.mu.Unlock()
		return existing, nil
	}
	if !create {
		registry.mu.Unlock()
		return nil, nil
	}
	if registry.creating[path] != 0 {
		registry.mu.Unlock()
		return nil, ErrClientStarting
	}
	registry.nextCreation++
	token := registry.nextCreation
	registry.creating[path] = token
	registry.factoryWG.Add(1)
	registry.mu.Unlock()
	client, err := registry.factory(path)
	defer registry.factoryWG.Done()
	if err != nil {
		registry.mu.Lock()
		if registry.creating[path] == token {
			delete(registry.creating, path)
		}
		registry.mu.Unlock()
		return nil, err
	}
	registry.mu.Lock()
	if registry.creating[path] != token {
		registry.mu.Unlock()
		_ = client.Close()
		return nil, fmt.Errorf("%w: creation was cancelled", ErrClientStarting)
	}
	delete(registry.creating, path)
	existing := registry.clients[path]
	if existing != nil && existing.retiring {
		registry.mu.Unlock()
		_ = client.Close()
		return nil, ErrClientRetiring
	}
	entry := existing
	unused := RPCClient(nil)
	if entry == nil {
		entry = newClientEntry(client, registry.clock())
		registry.clients[path] = entry
	} else {
		unused = client
	}
	entry.activeRequests++
	if observer {
		entry.observers++
	}
	if touch {
		entry.lastUsedAt = registry.clock()
	}
	registry.mu.Unlock()
	if unused != nil {
		_ = unused.Close()
	}
	return entry, nil
}
func (registry *Registry) release(entry *clientEntry, touch, observer bool) {
	registry.mu.Lock()
	if entry.activeRequests > 0 {
		entry.activeRequests--
	}
	if observer && entry.observers > 0 {
		entry.observers--
	}
	if touch {
		entry.lastUsedAt = registry.clock()
	}
	registry.mu.Unlock()
}

func (registry *Registry) Move(oldPath, newPath string) error {
	return registry.MoveWith(oldPath, newPath, nil)
}

func (registry *Registry) MoveWith(oldPath, newPath string, prepare func() (func() error, error)) error {
	return registry.MoveWithCommit(oldPath, newPath, prepare, nil)
}

func (registry *Registry) MoveWithCommit(oldPath, newPath string, prepare func() (func() error, error), commit func()) error {
	return registry.moveWithCommit(oldPath, newPath, prepare, commit, nil)
}

func (registry *Registry) WithClientMove(ctx context.Context, oldPath string, touch bool, call func(RPCClient) (string, error), prepare func(string, string) (func() error, error), commit func(string, string)) (string, error) {
	entry, err := registry.acquire(oldPath, true, touch, false)
	if err != nil || entry == nil {
		return oldPath, err
	}
	defer registry.release(entry, touch, false)
	select {
	case <-entry.operationLane:
		defer func() { entry.operationLane <- struct{}{} }()
	default:
		registry.logRejection(oldPath, laneOperation)
		return oldPath, ErrOperationPending
	}
	newPath, err := call(entry.client)
	if err != nil {
		registry.discard(entry, err)
		return newPath, err
	}
	if newPath == "" || newPath == oldPath {
		return newPath, nil
	}
	var preparation func() (func() error, error)
	if prepare != nil {
		preparation = func() (func() error, error) { return prepare(oldPath, newPath) }
	}
	var committed func()
	if commit != nil {
		committed = func() { commit(oldPath, newPath) }
	}
	err = registry.moveWithCommit(oldPath, newPath, preparation, committed, entry)
	if err != nil {
		registry.discard(entry, err)
	}
	return newPath, err
}

func (registry *Registry) moveWithCommit(oldPath, newPath string, prepare func() (func() error, error), commit func(), allowedActive *clientEntry) error {
	if oldPath == newPath {
		return nil
	}
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return ErrClientRetiring
	}
	if registry.creating[oldPath] != 0 || registry.creating[newPath] != 0 {
		registry.mu.Unlock()
		return ErrClientStarting
	}
	entry := registry.clients[oldPath]
	if entry == nil {
		if prepare == nil {
			registry.mu.Unlock()
			return nil
		}
		registry.nextCreation++
		token := registry.nextCreation
		registry.creating[oldPath], registry.creating[newPath] = token, token
		registry.moveWG.Add(1)
		registry.mu.Unlock()
		defer registry.moveWG.Done()
		rollback, err := prepare()
		registry.mu.Lock()
		valid := registry.creating[oldPath] == token && registry.creating[newPath] == token
		if registry.creating[oldPath] == token {
			delete(registry.creating, oldPath)
		}
		if registry.creating[newPath] == token {
			delete(registry.creating, newPath)
		}
		committed := err == nil && valid && !registry.closed && registry.clients[oldPath] == nil
		if committed && commit != nil {
			commit()
		}
		if err == nil && !committed {
			if registry.closed {
				err = ErrClientRetiring
			} else {
				err = ErrClientStarting
			}
		}
		registry.mu.Unlock()
		if err != nil && rollback != nil {
			return errors.Join(err, rollback())
		}
		return err
	}
	if entry.retiring {
		registry.mu.Unlock()
		return ErrClientRetiring
	}
	allowedRequests := entry.observers
	if entry == allowedActive {
		allowedRequests++
	}
	if entry.activeRequests > allowedRequests {
		registry.mu.Unlock()
		return fmt.Errorf("%w: source session operation is pending", ErrOperationPending)
	}
	destination := registry.clients[newPath]
	if destination != nil && destination != entry {
		if destination.retiring {
			registry.mu.Unlock()
			return ErrClientRetiring
		}
		if destination.activeRequests > 0 || destination.client.Busy() {
			registry.mu.Unlock()
			return fmt.Errorf("%w: destination session client is busy", ErrOperationPending)
		}
		destination.retiring = true
	}
	registry.nextCreation++
	token := registry.nextCreation
	registry.creating[newPath] = token
	entry.retiring = true
	registry.moveWG.Add(1)
	registry.mu.Unlock()
	defer registry.moveWG.Done()

	var rollback func() error
	var err error
	if prepare != nil {
		rollback, err = prepare()
	}
	registry.mu.Lock()
	reservationValid := registry.creating[newPath] == token
	if reservationValid {
		delete(registry.creating, newPath)
	}
	committed := err == nil && reservationValid && !registry.closed && registry.clients[oldPath] == entry && registry.clients[newPath] == destination
	if committed {
		if commit != nil {
			commit()
		}
		delete(registry.clients, oldPath)
		entry.lastUsedAt = registry.clock()
		entry.retiring = false
		registry.clients[newPath] = entry
	} else {
		if err == nil {
			if registry.closed {
				err = ErrClientRetiring
			} else {
				err = ErrClientStarting
			}
		}
		if registry.clients[oldPath] == entry {
			entry.retiring = false
		}
		if destination != nil && destination != entry && registry.clients[newPath] == destination {
			destination.retiring = false
		}
	}
	registry.mu.Unlock()
	if err != nil && rollback != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
	}
	if committed && destination != nil && destination != entry {
		if closeErr := destination.client.Close(); closeErr != nil {
			registry.mu.Lock()
			diagnostics := registry.diagnostics
			registry.mu.Unlock()
			diagnostics.Log("replaced_client_close_failed", map[string]any{"session": newPath, "error": closeErr.Error()})
		}
	}
	return err
}

func (registry *Registry) EventsAfter(path string, after int64) EventBatch {
	result := EventBatch{Events: []map[string]any{}}
	_ = registry.WithActiveClient(context.Background(), path, false, func(client RPCClient) error { result = client.EventsAfter(after); return nil })
	return result
}

func (registry *Registry) CloseClientIfIdle(path string) (bool, error) {
	return registry.closeWhen(path, func(entry *clientEntry) bool { return entry.activeRequests == 0 && !entry.client.Busy() }, nil)
}
func (registry *Registry) CloseClientIfExpired(path string, idle time.Duration, now time.Time, onClose func(string)) (bool, error) {
	return registry.closeWhen(path, func(entry *clientEntry) bool {
		return entry.activeRequests == 0 && !entry.client.Busy() && !activityAt(entry).Add(idle).After(now)
	}, onClose)
}
func (registry *Registry) IdleClientPaths(idle time.Duration, now time.Time, except map[string]bool) []string {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	result := []string{}
	for path, entry := range registry.clients {
		if !except[path] && !entry.retiring && entry.activeRequests == 0 && !entry.client.Busy() && !activityAt(entry).Add(idle).After(now) {
			result = append(result, path)
		}
	}
	return result
}
func (registry *Registry) CloseIdleClients(idle time.Duration, now time.Time, except map[string]bool, onClose func(string)) ([]string, error) {
	paths := registry.IdleClientPaths(idle, now, except)
	closed := []string{}
	var first error
	for _, path := range paths {
		ok, err := registry.CloseClientIfExpired(path, idle, now, onClose)
		if err != nil && first == nil {
			first = err
		}
		if ok {
			closed = append(closed, path)
		}
	}
	return closed, first
}
func (registry *Registry) closeWhen(path string, predicate func(*clientEntry) bool, onClose func(string)) (bool, error) {
	registry.mu.Lock()
	entry := registry.clients[path]
	if entry == nil || entry.retiring || !predicate(entry) {
		registry.mu.Unlock()
		return false, nil
	}
	entry.retiring = true
	registry.mu.Unlock()
	if err := entry.client.Close(); err != nil {
		registry.mu.Lock()
		if registry.clients[path] == entry {
			entry.retiring = false
		}
		registry.mu.Unlock()
		return false, err
	}
	registry.mu.Lock()
	if registry.clients[path] == entry {
		delete(registry.clients, path)
	}
	registry.mu.Unlock()
	if onClose != nil {
		onClose(path)
	}
	return true, nil
}
func (registry *Registry) CloseAll() error {
	registry.mu.Lock()
	registry.creating = make(map[string]uint64)
	paths := make([]string, 0, len(registry.clients))
	for path := range registry.clients {
		paths = append(paths, path)
	}
	registry.mu.Unlock()
	var first error
	for _, path := range paths {
		_, err := registry.closeWhen(path, func(*clientEntry) bool { return true }, nil)
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (registry *Registry) DrainIfIdle() bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.resumeOnShutdown = false
	if registry.closed {
		return true
	}
	if len(registry.creating) > 0 {
		return false
	}
	for _, entry := range registry.clients {
		if entry.activeRequests > 0 || entry.client.Busy() {
			return false
		}
	}
	registry.closed = true
	return true
}

func (registry *Registry) ResumeAfterFailedShutdown() bool {
	registry.mu.Lock()
	if !registry.closed {
		registry.mu.Unlock()
		return true
	}
	done := registry.shutdownDone
	if done != nil {
		select {
		case <-done:
		default:
			registry.resumeOnShutdown = true
			registry.mu.Unlock()
			go registry.resumeWhenShutdownFinishes(done)
			return false
		}
	}
	registry.resumeLocked()
	registry.mu.Unlock()
	return true
}

func (registry *Registry) resumeWhenShutdownFinishes(done chan struct{}) {
	<-done
	registry.mu.Lock()
	if registry.shutdownDone == done && registry.resumeOnShutdown {
		registry.resumeLocked()
	}
	registry.mu.Unlock()
}

func (registry *Registry) resumeLocked() {
	registry.closed = false
	registry.shutdownDone = nil
	registry.shutdownErr = nil
	registry.resumeOnShutdown = false
}

func (registry *Registry) Shutdown(ctx context.Context) error {
	registry.mu.Lock()
	done := registry.shutdownDone
	if done == nil {
		registry.closed = true
		registry.creating = make(map[string]uint64)
		done = make(chan struct{})
		registry.shutdownDone = done
		go registry.finishShutdown(done)
	}
	registry.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		registry.mu.Lock()
		err := registry.shutdownErr
		registry.mu.Unlock()
		return err
	}
}

func (registry *Registry) finishShutdown(done chan struct{}) {
	registry.factoryWG.Wait()
	registry.moveWG.Wait()
	registry.mu.Lock()
	paths := make([]string, 0, len(registry.clients))
	for path := range registry.clients {
		paths = append(paths, path)
	}
	registry.mu.Unlock()
	errorsChannel := make(chan error, len(paths))
	var clients sync.WaitGroup
	for _, path := range paths {
		clients.Add(1)
		go func() {
			defer clients.Done()
			var err error
			for attempt := 0; attempt < 2; attempt++ {
				_, err = registry.closeWhen(path, func(*clientEntry) bool { return true }, nil)
				if err == nil {
					break
				}
			}
			if err != nil {
				errorsChannel <- err
			}
		}()
	}
	clients.Wait()
	close(errorsChannel)
	var first error
	for err := range errorsChannel {
		if first == nil {
			first = err
		}
	}
	registry.mu.Lock()
	registry.shutdownErr = first
	registry.mu.Unlock()
	close(done)
}

func (registry *Registry) discard(entry *clientEntry, reason error) {
	registry.mu.Lock()
	path := ""
	for candidate, current := range registry.clients {
		if current == entry {
			path = candidate
			break
		}
	}
	if path == "" || entry.retiring {
		registry.mu.Unlock()
		return
	}
	entry.retiring = true
	diagnostics := registry.diagnostics
	registry.mu.Unlock()
	diagnostics.Log("client_evicted", map[string]any{"session": path, "reason": fmt.Sprintf("%T", reason)})
	_ = entry.client.Close()
	registry.mu.Lock()
	for candidate, current := range registry.clients {
		if current == entry {
			delete(registry.clients, candidate)
		}
	}
	registry.mu.Unlock()
}
func terminalRPCError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var timeout *RequestTimeoutError
	return errors.As(err, &timeout) || errors.Is(err, ErrProcessExited)
}
func activityAt(entry *clientEntry) time.Time {
	result := entry.lastUsedAt
	if settled := entry.client.SettledAt(); settled != nil && settled.After(result) {
		result = *settled
	}
	return result
}
func (registry *Registry) logRejection(path string, serial lane) {
	registry.mu.Lock()
	diagnostics := registry.diagnostics
	registry.mu.Unlock()
	name := "operation"
	if serial == laneInterrupt {
		name = "interrupt"
	}
	if serial == laneBash {
		name = "bash"
	}
	diagnostics.Log("operation_rejected", map[string]any{"session": path, "lane": name})
}
