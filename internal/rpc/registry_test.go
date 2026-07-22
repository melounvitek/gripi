package rpc

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegisterKeepsCommittedReplacementWhenDisplacedCloseFails(t *testing.T) {
	old := newRegistryClient()
	old.closeErr = errors.New("close failed")
	replacement := newRegistryClient()
	var diagnostics bytes.Buffer
	registry := NewRegistry(func(string) (RPCClient, error) { return nil, errors.New("unexpected factory") }, nil)
	registry.SetDiagnostics(&Diagnostics{Enabled: true, Writer: &diagnostics})
	if err := registry.Register("/session", old); err != nil {
		t.Fatal(err)
	}

	if err := registry.Register("/session", replacement); err != nil {
		t.Fatalf("replacement = %v", err)
	}
	if registry.Client("/session") != replacement || replacement.closed() || !old.closed() {
		t.Fatalf("registered=%v replacement_closed=%v old_closed=%v", registry.Client("/session") == replacement, replacement.closed(), old.closed())
	}
	if output := diagnostics.String(); !strings.Contains(output, `"event":"replaced_client_close_failed"`) || !strings.Contains(output, `"session":"/session"`) {
		t.Fatalf("diagnostics = %q", output)
	}
}

func TestRegisterDoesNotReplaceBusyClient(t *testing.T) {
	old := newRegistryClient()
	old.isBusy = true
	replacement := newRegistryClient()
	registry := NewRegistry(func(string) (RPCClient, error) { return nil, errors.New("unexpected factory") }, nil)
	if err := registry.Register("/session", old); err != nil {
		t.Fatal(err)
	}

	if err := registry.Register("/session", replacement); !errors.Is(err, ErrOperationPending) {
		t.Fatalf("replacement = %v", err)
	}
	if registry.Client("/session") != old || old.closed() || replacement.closed() {
		t.Fatalf("old_registered=%v old_closed=%v replacement_closed=%v", registry.Client("/session") == old, old.closed(), replacement.closed())
	}
}

func TestDrainIfIdleAtomicallyStopsNewWork(t *testing.T) {
	registry := NewRegistry(func(string) (RPCClient, error) { return nil, errors.New("unexpected factory") }, nil)
	if err := registry.Register("/session", newRegistryClient()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- registry.WithClient(context.Background(), "/session", func(RPCClient) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	if registry.DrainIfIdle() {
		t.Fatal("registry drained while an operation was admitted")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !registry.DrainIfIdle() {
		t.Fatal("idle registry did not start draining")
	}
	if err := registry.WithClient(context.Background(), "/session", func(RPCClient) error { return nil }); !errors.Is(err, ErrClientRetiring) {
		t.Fatalf("new work after drain = %v", err)
	}
	if !registry.ResumeAfterFailedShutdown() {
		t.Fatal("failed restart did not reopen admission")
	}
	if err := registry.WithClient(context.Background(), "/session", func(RPCClient) error { return nil }); err != nil {
		t.Fatalf("work after failed restart = %v", err)
	}
}

func TestFailedShutdownResumesAdmissionAfterTimedOutCleanupFinishes(t *testing.T) {
	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	client := newRegistryClient()
	client.closeStarted = closeStarted
	client.releaseClose = releaseClose
	registry := NewRegistry(func(string) (RPCClient, error) { return newRegistryClient(), nil }, nil)
	if err := registry.Register("/session", client); err != nil {
		t.Fatal(err)
	}
	if !registry.DrainIfIdle() {
		t.Fatal("idle registry did not start draining")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := registry.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown = %v", err)
	}
	<-closeStarted
	if registry.ResumeAfterFailedShutdown() {
		t.Fatal("cleanup was still running")
	}
	close(releaseClose)
	deadline := time.Now().Add(time.Second)
	for {
		err := registry.WithClient(context.Background(), "/new-session", func(RPCClient) error { return nil })
		if err == nil {
			break
		}
		if !errors.Is(err, ErrClientRetiring) || time.Now().After(deadline) {
			t.Fatalf("admission did not resume: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRegistryCreationLanesAndRetirementAreRaceSafe(t *testing.T) {
	creationStarted := make(chan struct{})
	releaseCreation := make(chan struct{})
	created := newRegistryClient()
	registry := NewRegistry(func(string) (RPCClient, error) { close(creationStarted); <-releaseCreation; return created, nil }, nil)
	createdResult := make(chan error, 1)
	go func() { _, err := registry.EnsureClient("/session"); createdResult <- err }()
	<-creationStarted
	if _, err := registry.EnsureClient("/session"); !errors.Is(err, ErrClientStarting) {
		t.Fatalf("duplicate creation error = %v", err)
	}
	close(releaseCreation)
	if err := <-createdResult; err != nil {
		t.Fatal(err)
	}

	operationStarted := make(chan struct{})
	releaseOperation := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- registry.WithClient(context.Background(), "/session", func(RPCClient) error { close(operationStarted); <-releaseOperation; return nil })
	}()
	<-operationStarted
	if err := registry.WithClient(context.Background(), "/session", func(RPCClient) error { return nil }); !errors.Is(err, ErrOperationPending) {
		t.Fatalf("operation error = %v", err)
	}
	if err := registry.WithInterruptClient(context.Background(), "/session", func(RPCClient) error { return nil }); err != nil {
		t.Fatalf("interrupt lane: %v", err)
	}
	if err := registry.WithBashClient(context.Background(), "/session", func(RPCClient) error { return nil }); err != nil {
		t.Fatalf("bash lane: %v", err)
	}
	close(releaseOperation)
	if err := <-operationDone; err != nil {
		t.Fatal(err)
	}

	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	created.closeStarted = closeStarted
	created.releaseClose = releaseClose
	failureDone := make(chan error, 1)
	go func() {
		failureDone <- registry.WithClient(context.Background(), "/session", func(RPCClient) error { return ErrProcessExited })
	}()
	<-closeStarted
	if _, err := registry.EnsureClient("/session"); !errors.Is(err, ErrClientRetiring) {
		t.Fatalf("retiring error = %v", err)
	}
	close(releaseClose)
	if err := <-failureDone; !errors.Is(err, ErrProcessExited) {
		t.Fatalf("failure = %v", err)
	}
	if registry.Active("/session") {
		t.Fatal("retired client remained active")
	}
}

func TestRegistryCloseAllCancelsPendingInstallation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	client := newRegistryClient()
	registry := NewRegistry(func(string) (RPCClient, error) { close(started); <-release; return client, nil }, nil)
	result := make(chan error, 1)
	go func() { _, err := registry.EnsureClient("/session"); result <- err }()
	<-started
	if err := registry.CloseAll(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, ErrClientStarting) {
		t.Fatalf("creation error = %v", err)
	}
	if !client.closed() {
		t.Fatal("cancelled created client was not closed")
	}
}

func TestRegistryMovePreparationHidesBothPathsAndRollsBack(t *testing.T) {
	client := newRegistryClient()
	registry := NewRegistry(func(string) (RPCClient, error) { return newRegistryClient(), nil }, nil)
	if err := registry.Register("/pending", client); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	moveResult := make(chan error, 1)
	prepareErr := errors.New("migration failed")
	go func() {
		moveResult <- registry.MoveWith("/pending", "/real", func() (func() error, error) { close(started); <-release; return nil, prepareErr })
	}()
	<-started
	if _, err := registry.EnsureClient("/pending"); !errors.Is(err, ErrClientRetiring) {
		t.Fatalf("source during move = %v", err)
	}
	if _, err := registry.EnsureClient("/real"); !errors.Is(err, ErrClientStarting) {
		t.Fatalf("destination during move = %v", err)
	}
	close(release)
	if err := <-moveResult; !errors.Is(err, prepareErr) {
		t.Fatalf("move error = %v", err)
	}
	if !registry.Active("/pending") || registry.Active("/real") {
		t.Fatal("failed move was not rolled back")
	}
}

func TestRegistryMoveCommitsMetadataWhenSourceClientAlreadyDisappeared(t *testing.T) {
	registry := NewRegistry(func(string) (RPCClient, error) { return nil, errors.New("unexpected") }, nil)
	prepared, committed := false, false
	err := registry.MoveWithCommit("/pending", "/real", func() (func() error, error) {
		prepared = true
		return nil, nil
	}, func() { committed = true })
	if err != nil || !prepared || !committed {
		t.Fatalf("prepared=%v committed=%v err=%v", prepared, committed, err)
	}
}

func TestRegistryMoveReplacesAndClosesIdleDestination(t *testing.T) {
	source := newRegistryClient()
	destination := newRegistryClient()
	registry := NewRegistry(func(string) (RPCClient, error) { return nil, errors.New("unexpected") }, nil)
	if err := registry.Register("/pending", source); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("/real", destination); err != nil {
		t.Fatal(err)
	}
	if err := registry.Move("/pending", "/real"); err != nil {
		t.Fatal(err)
	}
	if registry.Active("/pending") || registry.Client("/real") != source || !destination.closed() || source.closed() {
		t.Fatalf("move did not replace idle destination: pending=%v real=%T sourceClosed=%v destinationClosed=%v", registry.Active("/pending"), registry.Client("/real"), source.closed(), destination.closed())
	}
}

func TestRegistryMoveRejectsBusyDestination(t *testing.T) {
	source := newRegistryClient()
	destination := newRegistryClient()
	destination.isBusy = true
	registry := NewRegistry(func(string) (RPCClient, error) { return nil, errors.New("unexpected") }, nil)
	if err := registry.Register("/pending", source); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("/real", destination); err != nil {
		t.Fatal(err)
	}
	if err := registry.Move("/pending", "/real"); !errors.Is(err, ErrOperationPending) {
		t.Fatalf("busy destination move error = %v", err)
	}
	if registry.Client("/pending") != source || registry.Client("/real") != destination || destination.closed() {
		t.Fatal("rejected move changed either client")
	}
}

func TestRegistryMoveRollsBackPreparationWhenShutdownCancelsCommit(t *testing.T) {
	client := newRegistryClient()
	registry := NewRegistry(func(string) (RPCClient, error) { return client, nil }, nil)
	if err := registry.Register("/pending", client); err != nil {
		t.Fatal(err)
	}
	prepared, release, rolledBack := make(chan struct{}), make(chan struct{}), make(chan struct{})
	moveResult := make(chan error, 1)
	go func() {
		moveResult <- registry.MoveWith("/pending", "/real", func() (func() error, error) {
			close(prepared)
			<-release
			return func() error { close(rolledBack); return nil }, nil
		})
	}()
	<-prepared
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- registry.Shutdown(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		registry.mu.Lock()
		closed := registry.closed
		registry.mu.Unlock()
		if closed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-moveResult; !errors.Is(err, ErrClientRetiring) {
		t.Fatalf("move error = %v", err)
	}
	select {
	case <-rolledBack:
	case <-time.After(time.Second):
		t.Fatal("cancelled preparation was not rolled back")
	}
	if err := <-shutdownResult; err != nil {
		t.Fatal(err)
	}
}

func TestRegistryShutdownWaitsForCreationAndPermanentlyRejectsClients(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	client := newRegistryClient()
	registry := NewRegistry(func(string) (RPCClient, error) { close(started); <-release; return client, nil }, nil)
	creation := make(chan error, 1)
	go func() { _, err := registry.EnsureClient("/session"); creation <- err }()
	<-started
	shutdown := make(chan error, 1)
	go func() { shutdown <- registry.Shutdown(context.Background()) }()
	select {
	case err := <-shutdown:
		t.Fatalf("shutdown returned before factory: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-creation; !errors.Is(err, ErrClientStarting) {
		t.Fatalf("creation error = %v", err)
	}
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	if !client.closed() {
		t.Fatal("shutdown did not close cancelled client")
	}
	if _, err := registry.EnsureClient("/later"); !errors.Is(err, ErrClientRetiring) {
		t.Fatalf("post-shutdown creation = %v", err)
	}
}

func TestRegistryShutdownCanBeAwaitedAgainAfterContextTimeout(t *testing.T) {
	client := newRegistryClient()
	client.closeStarted = make(chan struct{})
	client.releaseClose = make(chan struct{})
	registry := NewRegistry(func(string) (RPCClient, error) { return client, nil }, nil)
	if err := registry.Register("/session", client); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- registry.Shutdown(ctx) }()
	<-client.closeStarted
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v", err)
	}
	close(client.releaseClose)
	if err := registry.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryUsesSettlementAndFiveMinuteIdleBoundary(t *testing.T) {
	now := time.Unix(1_000, 0)
	client := newRegistryClient()
	registry := NewRegistry(func(string) (RPCClient, error) { return client, nil }, func() time.Time { return now })
	if _, err := registry.EnsureClient("/session"); err != nil {
		t.Fatal(err)
	}
	settled := time.Unix(1_100, 0)
	client.setSettled(&settled)
	now = settled.Add(5*time.Minute - time.Nanosecond)
	closed, err := registry.CloseIdleClients(5*time.Minute, now, nil, nil)
	if err != nil || len(closed) != 0 {
		t.Fatalf("closed early: %v %v", closed, err)
	}
	now = settled.Add(5 * time.Minute)
	closed, err = registry.CloseIdleClients(5*time.Minute, now, nil, nil)
	if err != nil || len(closed) != 1 || closed[0] != "/session" {
		t.Fatalf("boundary close: %v %v", closed, err)
	}
}

func TestRegistryObserverAllowsMoveButPreventsRetirement(t *testing.T) {
	client := newRegistryClient()
	registry := NewRegistry(func(string) (RPCClient, error) { return nil, errors.New("unexpected") }, nil)
	if err := registry.Register("/pending", client); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- registry.WithObservingClient(context.Background(), "/pending", false, func(RPCClient) error { close(started); <-release; return nil })
	}()
	<-started
	if err := registry.Move("/pending", "/real"); err != nil {
		t.Fatal(err)
	}
	if closed, err := registry.CloseClientIfIdle("/real"); err != nil || closed {
		t.Fatalf("closed observed client: %v %v", closed, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if closed, err := registry.CloseClientIfIdle("/real"); err != nil || !closed {
		t.Fatalf("did not close released client: %v %v", closed, err)
	}
}

type registryClient struct {
	mu           sync.Mutex
	isBusy       bool
	settled      *time.Time
	isClosed     bool
	closeErr     error
	closeStarted chan struct{}
	releaseClose chan struct{}
}

func newRegistryClient() *registryClient { return &registryClient{} }
func (client *registryClient) Close() error {
	client.mu.Lock()
	started, release := client.closeStarted, client.releaseClose
	client.isClosed = true
	client.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	return client.closeErr
}
func (client *registryClient) closed() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.isClosed
}
func (client *registryClient) setSettled(value *time.Time) {
	client.mu.Lock()
	client.settled = value
	client.mu.Unlock()
}
func (client *registryClient) Busy() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.isBusy
}
func (client *registryClient) BusySince() *time.Time { return nil }
func (client *registryClient) SettledAt() *time.Time {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.settled == nil {
		return nil
	}
	copy := *client.settled
	return &copy
}
func (client *registryClient) AgentRunning() bool       { return false }
func (client *registryClient) Compacting() bool         { return false }
func (client *registryClient) EventSequence() int64     { return 0 }
func (client *registryClient) EventReplayCursor() int64 { return 0 }
func (client *registryClient) EventsAfter(int64) EventBatch {
	return EventBatch{Events: []map[string]any{}}
}
func (client *registryClient) LiveSnapshot() LiveSnapshot {
	return LiveSnapshot{ActiveToolEvents: []map[string]any{}}
}
func (client *registryClient) GetState(context.Context) (map[string]any, error) { return nil, nil }
func (client *registryClient) GetSessionStats(context.Context) (map[string]any, error) {
	return nil, nil
}
func (client *registryClient) GetCommands(context.Context) (map[string]any, error) { return nil, nil }
func (client *registryClient) SessionPosition(context.Context, string) (SessionEntries, error) {
	return SessionEntries{Known: true}, nil
}
func (client *registryClient) SessionEntriesAfter(context.Context, string) (SessionEntries, error) {
	return SessionEntries{Known: true}, nil
}
