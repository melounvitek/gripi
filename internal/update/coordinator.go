package update

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type updaterAPI interface {
	Status(context.Context) Status
	Update(context.Context) Result
}

type Snapshot struct {
	State              string
	Reason             *string
	Message            *string
	CurrentSHA         *string
	TargetSHA          *string
	BehindCount        *int
	Summary            *string
	ActiveSessionCount *int
	Failure            *StepFailure
}

type Coordinator struct {
	updater            updaterAPI
	restarter          func(context.Context) error
	activeSessionCount func() int
	admitRestart       func() bool
	restartDelay       time.Duration
	waitInterval       time.Duration
	statusTimeout      time.Duration
	updateTimeout      time.Duration
	ctx                context.Context
	cancel             context.CancelFunc
	closeDone          chan struct{}
	operationGate      chan struct{}
	mu                 sync.Mutex
	operations         sync.WaitGroup
	snapshot           Snapshot
	statusChecking     bool
	running            bool
	finished           bool
	restartPending     bool
	closed             bool
}

func NewCoordinator(updater updaterAPI, restarter func(context.Context) error, active func() int, admitRestart func() bool) *Coordinator {
	if active == nil {
		active = func() int { return 0 }
	}
	if admitRestart == nil {
		admitRestart = func() bool { return true }
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := &Coordinator{
		updater: updater, restarter: restarter, activeSessionCount: active, admitRestart: admitRestart,
		restartDelay: time.Second, waitInterval: time.Second, statusTimeout: 3 * time.Minute, updateTimeout: 25 * time.Minute,
		ctx: ctx, cancel: cancel, closeDone: make(chan struct{}), operationGate: make(chan struct{}, 1), snapshot: Snapshot{State: "unknown"},
	}
	coordinator.operationGate <- struct{}{}
	return coordinator
}

func (coordinator *Coordinator) CachedStatus() Snapshot {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.snapshot
}

func (coordinator *Coordinator) Status(requestContext context.Context) Snapshot {
	if requestContext == nil {
		requestContext = context.Background()
	}
	coordinator.mu.Lock()
	if coordinator.closed {
		snapshot := coordinator.snapshot
		coordinator.mu.Unlock()
		return snapshot
	}
	if coordinator.running || coordinator.finished || coordinator.statusChecking {
		snapshot := coordinator.snapshot
		coordinator.mu.Unlock()
		return snapshot
	}
	coordinator.statusChecking = true
	coordinator.operations.Add(1)
	lifecycleContext := coordinator.ctx
	coordinator.mu.Unlock()
	defer coordinator.operations.Done()

	ctx, cancel := linkedContext(lifecycleContext, requestContext)
	status := coordinator.checkStatus(ctx)
	cancel()
	result := snapshotFromStatus(status)
	coordinator.mu.Lock()
	coordinator.statusChecking = false
	if requestContext.Err() == nil && !coordinator.running && !coordinator.finished && !coordinator.closed {
		coordinator.snapshot = result
	}
	if requestContext.Err() == nil || coordinator.closed {
		result = coordinator.snapshot
	}
	coordinator.mu.Unlock()
	return result
}

func (coordinator *Coordinator) Start() Snapshot {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return coordinator.snapshot
	}
	if coordinator.running {
		return coordinator.snapshot
	}
	active := coordinator.activeSessionCount()
	coordinator.running = true
	coordinator.finished = false
	restart := coordinator.restartPending
	if active > 0 {
		coordinator.snapshot = waitingSnapshot(coordinator.snapshot, active)
	} else if restart {
		coordinator.snapshot = restartingSnapshot(coordinator.snapshot)
	} else {
		coordinator.snapshot = progressSnapshot(coordinator.snapshot)
	}
	coordinator.operations.Add(1)
	go func(ctx context.Context) {
		defer coordinator.operations.Done()
		coordinator.background(ctx, restart)
	}(coordinator.ctx)
	return coordinator.snapshot
}

func (coordinator *Coordinator) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	coordinator.mu.Lock()
	if !coordinator.closed {
		coordinator.closed = true
		coordinator.snapshot = closedSnapshot(coordinator.snapshot)
		coordinator.cancel()
		go func() {
			coordinator.operations.Wait()
			close(coordinator.closeDone)
		}()
	}
	done := coordinator.closeDone
	coordinator.mu.Unlock()
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (coordinator *Coordinator) background(ctx context.Context, restart bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			reason := "update"
			if restart {
				reason = "restart"
			}
			coordinator.finishFailure(fmt.Errorf("%v", recovered), reason)
		}
	}()
	if restart {
		coordinator.performRestart(ctx)
		return
	}
	coordinator.performUpdate(ctx)
}

func (coordinator *Coordinator) performUpdate(ctx context.Context) {
	if !coordinator.waitForIdle(ctx) {
		coordinator.finishFailure(ctx.Err(), "cancelled")
		return
	}
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return
	}
	coordinator.snapshot = progressSnapshot(coordinator.snapshot)
	coordinator.mu.Unlock()
	result := coordinator.runUpdate(ctx)
	if ctx.Err() != nil {
		coordinator.finishFailure(ctx.Err(), "cancelled")
		return
	}
	if result.State != "updated" {
		coordinator.finish(snapshotFromResult(result))
		return
	}
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return
	}
	coordinator.restartPending = true
	coordinator.snapshot = Snapshot{State: "restarting", Message: stringPointer(nonempty(result.Message, "Restarting gateway…")), CurrentSHA: stringPointerOrNil(result.Status.CurrentSHA), TargetSHA: stringPointerOrNil(result.Status.TargetSHA), BehindCount: intPointer(result.Status.BehindCount), Summary: stringPointerOrNil(result.Status.Summary)}
	coordinator.mu.Unlock()
	coordinator.performRestart(ctx)
}

func (coordinator *Coordinator) checkStatus(ctx context.Context) (status Status) {
	ctx, cancel := context.WithTimeout(ctx, coordinator.statusTimeout)
	defer cancel()
	if !coordinator.acquireOperation(ctx) {
		return statusContextError(ctx.Err())
	}
	defer coordinator.releaseOperation()
	defer func() {
		if recovered := recover(); recovered != nil {
			status = Status{State: "error", Reason: "check", Message: fmt.Sprint(recovered)}
		}
	}()
	status = coordinator.updater.Status(ctx)
	if err := ctx.Err(); err != nil {
		status = statusContextError(err)
	}
	return status
}

func statusContextError(err error) Status {
	reason := "cancelled"
	message := "Gateway update check cancelled: " + err.Error()
	if err == context.DeadlineExceeded {
		reason = "timeout"
		message = "Gateway update check timed out: " + err.Error()
	}
	return Status{State: "error", Reason: reason, Message: message}
}

func (coordinator *Coordinator) runUpdate(ctx context.Context) Result {
	timeout := coordinator.updateTimeout
	if timeout > 2*updateCleanupTimeout {
		timeout -= updateCleanupTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if !coordinator.acquireOperation(ctx) {
		return Result{State: "error", Message: "Gateway update cancelled: " + ctx.Err().Error()}
	}
	defer coordinator.releaseOperation()
	result := coordinator.updater.Update(ctx)
	if err := ctx.Err(); err != nil && result.State != "updated" {
		message := "Gateway update cancelled: " + err.Error()
		if err == context.DeadlineExceeded {
			message = "Gateway update timed out: " + err.Error()
		}
		result = Result{State: "error", Status: result.Status, Message: message}
	}
	return result
}

func (coordinator *Coordinator) acquireOperation(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case <-coordinator.operationGate:
		return true
	case <-ctx.Done():
		return false
	}
}

func (coordinator *Coordinator) releaseOperation() {
	coordinator.operationGate <- struct{}{}
}

func (coordinator *Coordinator) performRestart(ctx context.Context) {
	if !coordinator.waitForIdle(ctx) {
		coordinator.finishFailure(ctx.Err(), "cancelled")
		return
	}
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return
	}
	coordinator.snapshot = restartingSnapshot(coordinator.snapshot)
	coordinator.mu.Unlock()
	if !waitFor(ctx, coordinator.restartDelay) {
		coordinator.finishFailure(ctx.Err(), "cancelled")
		return
	}
	for {
		if !coordinator.waitForIdle(ctx) {
			coordinator.finishFailure(ctx.Err(), "cancelled")
			return
		}
		if coordinator.admitRestart() {
			break
		}
		if !waitFor(ctx, coordinator.waitInterval) {
			coordinator.finishFailure(ctx.Err(), "cancelled")
			return
		}
	}
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return
	}
	if err := ctx.Err(); err != nil {
		coordinator.mu.Unlock()
		coordinator.finishFailure(err, "cancelled")
		return
	}
	coordinator.snapshot = restartingSnapshot(coordinator.snapshot)
	coordinator.mu.Unlock()
	if err := coordinator.restarter(ctx); err != nil {
		coordinator.finishFailure(err, "restart")
	}
}

func (coordinator *Coordinator) waitForIdle(ctx context.Context) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		active := coordinator.activeSessionCount()
		if active <= 0 {
			return true
		}
		coordinator.mu.Lock()
		if coordinator.closed {
			coordinator.mu.Unlock()
			return false
		}
		coordinator.snapshot = waitingSnapshot(coordinator.snapshot, active)
		coordinator.mu.Unlock()
		if !waitFor(ctx, coordinator.waitInterval) {
			return false
		}
	}
}

func waitFor(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func linkedContext(lifecycle, request context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(lifecycle)
	if request.Err() != nil {
		cancel()
		return ctx, cancel
	}
	stop := context.AfterFunc(request, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (coordinator *Coordinator) finish(snapshot Snapshot) {
	coordinator.mu.Lock()
	if !coordinator.closed {
		coordinator.snapshot = snapshot
	}
	coordinator.running = false
	coordinator.finished = true
	coordinator.mu.Unlock()
}

func (coordinator *Coordinator) finishFailure(err error, reason string) {
	coordinator.mu.Lock()
	if !coordinator.closed {
		coordinator.snapshot = failureSnapshot(coordinator.snapshot, err, reason)
	}
	coordinator.running = false
	coordinator.finished = true
	coordinator.mu.Unlock()
}

func progressSnapshot(previous Snapshot) Snapshot {
	previous.State = "updating"
	previous.Reason = nil
	previous.Message = stringPointer("Updating gateway…")
	previous.ActiveSessionCount = nil
	previous.Failure = nil
	return previous
}

func restartingSnapshot(previous Snapshot) Snapshot {
	alreadyRestarting := previous.State == "restarting"
	previous.State = "restarting"
	previous.Reason = nil
	if !alreadyRestarting {
		previous.Message = stringPointer("Restarting gateway…")
	}
	previous.ActiveSessionCount = nil
	return previous
}

func waitingSnapshot(previous Snapshot, active int) Snapshot {
	sessions := "sessions"
	if active == 1 {
		sessions = "session"
	}
	previous.State = "waiting"
	previous.Reason = nil
	previous.Message = stringPointer(fmt.Sprintf("Waiting for %d active Pi %s to finish…", active, sessions))
	previous.ActiveSessionCount = intPointer(active)
	previous.Failure = nil
	return previous
}

func failureSnapshot(previous Snapshot, err error, reason string) Snapshot {
	previous.State = "error"
	previous.Reason = stringPointer(reason)
	previous.Message = stringPointer(err.Error())
	previous.ActiveSessionCount = nil
	return previous
}

func closedSnapshot(previous Snapshot) Snapshot {
	return failureSnapshot(previous, fmt.Errorf("gateway update coordinator is closed"), "closed")
}

func snapshotFromStatus(status Status) Snapshot {
	snapshot := Snapshot{State: status.State, Reason: stringPointerOrNil(status.Reason), Message: stringPointerOrNil(status.Message), CurrentSHA: stringPointerOrNil(status.CurrentSHA), TargetSHA: stringPointerOrNil(status.TargetSHA), Summary: stringPointerOrNil(status.Summary)}
	if status.State == "available" || status.State == "up_to_date" || status.Reason == "ahead" || status.Reason == "diverged" {
		snapshot.BehindCount = intPointer(status.BehindCount)
	}
	return snapshot
}

func snapshotFromResult(result Result) Snapshot {
	snapshot := snapshotFromStatus(result.Status)
	snapshot.State = result.State
	snapshot.Message = stringPointerOrNil(result.Message)
	snapshot.Failure = result.Failure
	if snapshot.Reason == nil {
		snapshot.Reason = stringPointer(result.State)
	}
	return snapshot
}

func stringPointer(value string) *string { return &value }
func stringPointerOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func intPointer(value int) *int { return &value }
func nonempty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
