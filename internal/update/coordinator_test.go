package update

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeUpdater struct {
	mu                       sync.Mutex
	status                   Status
	result                   Result
	statusCalls, updateCalls int
}

func (fake *fakeUpdater) Status(context.Context) Status {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.statusCalls++
	return fake.status
}
func (fake *fakeUpdater) Update(context.Context) Result {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.updateCalls++
	return fake.result
}

func TestCoordinatorStatusStopsWhenCallerIsCancelled(t *testing.T) {
	updater := &cancellableUpdater{statusStarted: make(chan struct{}), statusCancelled: make(chan struct{})}
	coordinator := NewCoordinator(updater, func(context.Context) error { return nil }, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan Snapshot, 1)
	go func() { result <- coordinator.Status(ctx) }()
	<-updater.statusStarted
	cancel()

	select {
	case snapshot := <-result:
		if snapshot.State != "error" || snapshot.Reason == nil || *snapshot.Reason != "cancelled" {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("status did not stop after cancellation")
	}
	select {
	case <-updater.statusCancelled:
	case <-time.After(time.Second):
		t.Fatal("updater did not receive status cancellation")
	}
	if snapshot := coordinator.CachedStatus(); snapshot.State != "unknown" {
		t.Fatalf("cancelled request changed cached status: %+v", snapshot)
	}
}

func TestCoordinatorDoesNotStartStatusForCancelledRequest(t *testing.T) {
	updater := &fakeUpdater{}
	coordinator := NewCoordinator(updater, func(context.Context) error { return nil }, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	snapshot := coordinator.Status(ctx)
	if snapshot.State != "error" || snapshot.Reason == nil || *snapshot.Reason != "cancelled" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	updater.mu.Lock()
	calls := updater.statusCalls
	updater.mu.Unlock()
	if calls != 0 {
		t.Fatalf("status calls = %d", calls)
	}
}

func TestCoordinatorStatusCancellationDoesNotWaitForOperationGate(t *testing.T) {
	coordinator := NewCoordinator(&fakeUpdater{}, func(context.Context) error { return nil }, nil, nil)
	<-coordinator.operationGate
	defer func() { coordinator.operationGate <- struct{}{} }()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan Snapshot, 1)
	go func() { result <- coordinator.Status(ctx) }()
	deadline := time.Now().Add(time.Second)
	for {
		coordinator.mu.Lock()
		checking := coordinator.statusChecking
		coordinator.mu.Unlock()
		if checking {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("status check did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case snapshot := <-result:
		if snapshot.State != "error" || snapshot.Reason == nil || *snapshot.Reason != "cancelled" {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("status waited for operation gate after cancellation")
	}
}

func TestCoordinatorCloseCancelsUpdateAndPreventsRestart(t *testing.T) {
	updater := &cancellableUpdater{updateStarted: make(chan struct{}), updateCancelled: make(chan struct{})}
	restarted := atomic.Bool{}
	coordinator := NewCoordinator(updater, func(context.Context) error { restarted.Store(true); return nil }, nil, nil)
	coordinator.Start()
	<-updater.updateStarted

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if restarted.Load() {
		t.Fatal("restart ran after coordinator close")
	}
	select {
	case <-updater.updateCancelled:
	case <-time.After(time.Second):
		t.Fatal("updater did not receive update cancellation")
	}
	if snapshot := coordinator.Start(); snapshot.State != "error" || snapshot.Reason == nil || *snapshot.Reason != "closed" {
		t.Fatalf("start after close = %+v", snapshot)
	}
	expired, expire := context.WithCancel(context.Background())
	expire()
	if err := coordinator.Close(expired); err != nil {
		t.Fatalf("completed close = %v", err)
	}
}

func TestCoordinatorCloseWinsAtRestartBoundary(t *testing.T) {
	updater := &fakeUpdater{result: Result{State: "updated"}}
	admissionStarted, releaseAdmission := make(chan struct{}), make(chan struct{})
	restarted := atomic.Bool{}
	coordinator := NewCoordinator(updater, func(context.Context) error { restarted.Store(true); return nil }, nil, func() bool {
		close(admissionStarted)
		<-releaseAdmission
		return true
	})
	coordinator.restartDelay = 0
	coordinator.Start()
	<-admissionStarted
	closed := make(chan error, 1)
	go func() { closed <- coordinator.Close(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot := coordinator.CachedStatus()
		if snapshot.Reason != nil && *snapshot.Reason == "closed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("coordinator did not begin closing")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseAdmission)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if restarted.Load() {
		t.Fatal("restart ran after close won the boundary")
	}
}

func TestCoordinatorCloseInterruptsIdleAndRestartAdmissionWaits(t *testing.T) {
	t.Run("active sessions", func(t *testing.T) {
		updater := &fakeUpdater{}
		coordinator := NewCoordinator(updater, func(context.Context) error { t.Error("unexpected restart"); return nil }, func() int { return 1 }, nil)
		coordinator.waitInterval = time.Hour
		coordinator.Start()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := coordinator.Close(ctx); err != nil {
			t.Fatal(err)
		}
		updater.mu.Lock()
		calls := updater.updateCalls
		updater.mu.Unlock()
		if calls != 0 {
			t.Fatalf("update calls = %d", calls)
		}
	})

	t.Run("restart admission", func(t *testing.T) {
		updater := &fakeUpdater{result: Result{State: "updated"}}
		admissionChecked := make(chan struct{}, 1)
		coordinator := NewCoordinator(updater, func(context.Context) error { t.Error("unexpected restart"); return nil }, nil, func() bool {
			select {
			case admissionChecked <- struct{}{}:
			default:
			}
			return false
		})
		coordinator.restartDelay = 0
		coordinator.waitInterval = time.Hour
		coordinator.Start()
		select {
		case <-admissionChecked:
		case <-time.After(time.Second):
			t.Fatal("restart admission was not checked")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := coordinator.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

type cancellableUpdater struct {
	statusStarted, statusCancelled chan struct{}
	updateStarted, updateCancelled chan struct{}
}

func (updater *cancellableUpdater) Status(ctx context.Context) Status {
	close(updater.statusStarted)
	<-ctx.Done()
	close(updater.statusCancelled)
	return Status{State: "error", Reason: "cancelled", Message: ctx.Err().Error()}
}
func (updater *cancellableUpdater) Update(ctx context.Context) Result {
	close(updater.updateStarted)
	<-ctx.Done()
	close(updater.updateCancelled)
	return Result{State: "updated"}
}

func TestCoordinatorWaitsForActiveSessionsAndRestartsOnce(t *testing.T) {
	updater := &fakeUpdater{result: Result{State: "updated", Status: Status{CurrentSHA: "old", TargetSHA: "new"}, Message: "Updated to new"}}
	var active atomic.Int32
	active.Store(1)
	restarted := make(chan struct{}, 1)
	coordinator := NewCoordinator(updater, func(context.Context) error { restarted <- struct{}{}; return nil }, func() int { return int(active.Load()) }, nil)
	coordinator.restartDelay = 0
	coordinator.waitInterval = time.Millisecond
	snapshot := coordinator.Start()
	if snapshot.State != "waiting" || snapshot.ActiveSessionCount == nil || *snapshot.ActiveSessionCount != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	active.Store(0)
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("restart timed out")
	}
	updater.mu.Lock()
	calls := updater.updateCalls
	updater.mu.Unlock()
	if calls != 1 {
		t.Fatalf("update calls = %d", calls)
	}
}

func TestCoordinatorWaitsForRestartAdmissionAfterTheIdleCheck(t *testing.T) {
	updater := &fakeUpdater{result: Result{State: "updated"}}
	admitted := atomic.Bool{}
	restarted := make(chan struct{}, 1)
	coordinator := NewCoordinator(updater, func(context.Context) error { restarted <- struct{}{}; return nil }, nil, admitted.Load)
	coordinator.restartDelay = 0
	coordinator.waitInterval = time.Millisecond
	coordinator.Start()
	select {
	case <-restarted:
		t.Fatal("restarted before admission closed")
	case <-time.After(25 * time.Millisecond):
	}
	admitted.Store(true)
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("restart did not continue after admission closed")
	}
}

func TestCoordinatorDoesNotWedgeWhenInitialActivityCountPanics(t *testing.T) {
	calls := 0
	coordinator := NewCoordinator(&fakeUpdater{}, func(context.Context) error { return nil }, func() int {
		calls++
		if calls == 1 {
			panic("activity panic")
		}
		return 0
	}, nil)
	func() {
		defer func() { _ = recover() }()
		coordinator.Start()
	}()
	if snapshot := coordinator.Start(); snapshot.State != "updating" {
		t.Fatalf("retry snapshot = %+v", snapshot)
	}
}

func TestCoordinatorSerializesConcurrentStarts(t *testing.T) {
	release := make(chan struct{})
	updater := &fakeUpdater{}
	coordinator := NewCoordinator(updater, func(context.Context) error { return nil }, nil, nil)
	coordinator.updater = &blockingUpdater{fakeUpdater: updater, release: release}
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() { defer wait.Done(); coordinator.Start() }()
	}
	wait.Wait()
	time.Sleep(10 * time.Millisecond)
	updater.mu.Lock()
	calls := updater.updateCalls
	updater.mu.Unlock()
	if calls != 1 {
		t.Fatalf("update calls = %d", calls)
	}
	close(release)
}

type blockingUpdater struct {
	*fakeUpdater
	release chan struct{}
}

func (updater *blockingUpdater) Update(ctx context.Context) Result {
	updater.fakeUpdater.mu.Lock()
	updater.updateCalls++
	updater.fakeUpdater.mu.Unlock()
	select {
	case <-updater.release:
		return Result{State: "blocked"}
	case <-ctx.Done():
		return Result{State: "error", Message: ctx.Err().Error()}
	}
}

type panicUpdater struct {
	panicStatus bool
	panicUpdate bool
}

func (updater *panicUpdater) Status(context.Context) Status {
	if updater.panicStatus {
		updater.panicStatus = false
		panic("status panic")
	}
	return Status{State: "up_to_date"}
}
func (updater *panicUpdater) Update(context.Context) Result {
	if updater.panicUpdate {
		updater.panicUpdate = false
		panic("update panic")
	}
	return Result{State: "blocked"}
}

func TestCoordinatorReleasesOperationLockAfterUpdaterPanics(t *testing.T) {
	updater := &panicUpdater{panicStatus: true, panicUpdate: true}
	coordinator := NewCoordinator(updater, func(context.Context) error { return nil }, nil, nil)
	if snapshot := coordinator.Status(context.Background()); snapshot.State != "error" || snapshot.Message == nil || !strings.Contains(*snapshot.Message, "status panic") {
		t.Fatalf("status panic snapshot = %+v", snapshot)
	}
	if snapshot := coordinator.Status(context.Background()); snapshot.State != "up_to_date" {
		t.Fatalf("status retry snapshot = %+v", snapshot)
	}
	coordinator.Start()
	deadline := time.Now().Add(time.Second)
	for coordinator.CachedStatus().State != "error" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	coordinator.Start()
	deadline = time.Now().Add(time.Second)
	for coordinator.CachedStatus().State == "updating" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := coordinator.CachedStatus(); snapshot.State != "blocked" {
		t.Fatalf("update retry snapshot = %+v", snapshot)
	}
}

func TestCoordinatorLeavesUpdatingStateWithUsefulErrorWhenUpdateTimesOut(t *testing.T) {
	updater := &fakeUpdater{}
	coordinator := NewCoordinator(updater, func(context.Context) error { return nil }, nil, nil)
	coordinator.updater = &blockingUpdater{fakeUpdater: updater, release: make(chan struct{})}
	coordinator.updateTimeout = 20 * time.Millisecond
	coordinator.Start()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := coordinator.CachedStatus()
		if snapshot.State == "error" {
			if snapshot.Message == nil || !strings.Contains(*snapshot.Message, "deadline exceeded") {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("coordinator remained in %+v", coordinator.CachedStatus())
}

func TestCoordinatorReportsTheFailedStepUntilTheUpdateIsRetried(t *testing.T) {
	failure := &StepFailure{Step: "Check with Pi", ExitStatus: 1, Output: "$ gripi check\nPi exited with status 1"}
	updater := &fakeUpdater{result: Result{State: "dependency_failed", Message: "The new version failed its check on this machine.", Failure: failure}}
	coordinator := NewCoordinator(updater, func(context.Context) error { return nil }, nil, nil)
	coordinator.Start()

	deadline := time.Now().Add(time.Second)
	for coordinator.CachedStatus().State != "dependency_failed" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := coordinator.CachedStatus(); snapshot.Failure != failure {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if retry := coordinator.Start(); retry.State != "updating" || retry.Failure != nil {
		t.Fatalf("retry = %+v", retry)
	}
}
