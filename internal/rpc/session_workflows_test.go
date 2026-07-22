package rpc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStartNewSessionRegistersAndTracksAPathBeforeItExists(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "new.jsonl")
	client := &workflowClient{registryClient: newRegistryClient(), state: map[string]any{"data": map[string]any{"sessionFile": path}}}
	registry := NewRegistry(func(string) (RPCClient, error) { return nil, os.ErrNotExist }, nil)
	pending := NewPendingSessionRegistry(nil)

	result, err := StartNewSession(context.Background(), project, root, func(cwd string) (RPCClient, error) {
		if cwd != project {
			t.Fatalf("cwd = %q", cwd)
		}
		return client, nil
	}, registry, pending, nil)
	if err != nil || result != path || !registry.Active(path) {
		t.Fatalf("result = %q, active=%v, err=%v", result, registry.Active(path), err)
	}
	if cwd, ok := pending.CWD(path); !ok || cwd != project {
		t.Fatalf("pending cwd = %q, %v", cwd, ok)
	}
}

func TestStartNewSessionClosesClientWhenOwnershipClaimFails(t *testing.T) {
	client := &workflowClient{registryClient: newRegistryClient(), state: map[string]any{"data": map[string]any{"sessionFile": "/new"}}}
	registry := NewRegistry(func(string) (RPCClient, error) { return nil, os.ErrNotExist }, nil)
	claimErr := errors.New("claim failed")
	_, err := StartNewSession(context.Background(), t.TempDir(), t.TempDir(), func(string) (RPCClient, error) { return client, nil }, registry, NewPendingSessionRegistry(nil), func(string) (func() error, error) {
		return nil, claimErr
	})
	if !errors.Is(err, claimErr) || !client.closed() || registry.Active("/new") {
		t.Fatalf("err=%v closed=%v active=%v", err, client.closed(), registry.Active("/new"))
	}
}

func TestStartNewSessionClosesClientWhenStateFails(t *testing.T) {
	client := &workflowClient{registryClient: newRegistryClient(), stateErr: errors.New("state failed")}
	_, err := StartNewSession(context.Background(), t.TempDir(), t.TempDir(), func(string) (RPCClient, error) { return client, nil }, NewRegistry(func(string) (RPCClient, error) { return nil, nil }, nil), NewPendingSessionRegistry(nil), nil)
	if err == nil || !client.closed() {
		t.Fatalf("err=%v closed=%v", err, client.closed())
	}
}

func TestBranchSessionMovesAndTracksTheClient(t *testing.T) {
	root := t.TempDir()
	previous, next := filepath.Join(root, "previous.jsonl"), filepath.Join(root, "next.jsonl")
	client := &workflowClient{registryClient: newRegistryClient(), state: map[string]any{"success": true, "data": map[string]any{"sessionFile": next}}}
	registry := NewRegistry(func(string) (RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(previous, client); err != nil {
		t.Fatal(err)
	}
	pending := NewPendingSessionRegistry(nil)

	result, response, err := BranchSession(context.Background(), previous, root, registry, pending, func(RPCClient) (map[string]any, error) {
		return map[string]any{"success": true}, nil
	}, nil)
	if err != nil || response["success"] != true || result != next || registry.Active(previous) || !registry.Active(next) {
		t.Fatalf("result=%q previous=%v next=%v err=%v", result, registry.Active(previous), registry.Active(next), err)
	}
	if cwd, ok := pending.CWD(next); !ok || cwd != root {
		t.Fatalf("pending cwd = %q, %v", cwd, ok)
	}
}

func TestBranchSessionPreservesClientWhenNativeSwitchIsRejected(t *testing.T) {
	client := &workflowClient{registryClient: newRegistryClient(), state: map[string]any{"data": map[string]any{"sessionFile": "/unexpected"}}}
	registry := NewRegistry(func(string) (RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register("/previous", client); err != nil {
		t.Fatal(err)
	}
	path, response, err := BranchSession(context.Background(), "/previous", "/project", registry, NewPendingSessionRegistry(nil), func(RPCClient) (map[string]any, error) {
		return map[string]any{"success": false, "error": "Entry not found"}, nil
	}, nil)
	if err != nil || path != "/previous" || response["success"] != false || !registry.Active("/previous") || registry.Active("/unexpected") {
		t.Fatalf("path=%q response=%#v active=%v err=%v", path, response, registry.Active("/previous"), err)
	}
}

func TestBranchSessionFetchesPostSwitchStateInsideTheOriginalLaneAndDoesNotFallBack(t *testing.T) {
	previous, next := "/previous", "/next"
	client := &workflowClient{registryClient: newRegistryClient(), state: map[string]any{"data": map[string]any{"sessionFile": next}}}
	registry := NewRegistry(func(string) (RPCClient, error) { return newRegistryClient(), nil }, nil)
	if err := registry.Register(previous, client); err != nil {
		t.Fatal(err)
	}
	switchStarted, releaseSwitch := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, _, err := BranchSession(context.Background(), previous, "/project", registry, NewPendingSessionRegistry(nil), func(RPCClient) (map[string]any, error) {
			close(switchStarted)
			<-releaseSwitch
			return map[string]any{"success": true}, nil
		}, nil)
		result <- err
	}()
	<-switchStarted
	if err := registry.WithExistingClient(context.Background(), previous, true, func(RPCClient) error { return nil }); !errors.Is(err, ErrOperationPending) {
		t.Fatalf("competing operation = %v", err)
	}
	close(releaseSwitch)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if registry.Active(previous) || !registry.Active(next) {
		t.Fatal("post-switch client was not remapped before releasing the operation lane")
	}

	missing := &workflowClient{registryClient: newRegistryClient(), state: map[string]any{"success": true}}
	missingRegistry := NewRegistry(func(string) (RPCClient, error) { return nil, os.ErrNotExist }, nil)
	if err := missingRegistry.Register(previous, missing); err != nil {
		t.Fatal(err)
	}
	path, _, err := BranchSession(context.Background(), previous, "/project", missingRegistry, NewPendingSessionRegistry(nil), func(RPCClient) (map[string]any, error) { return map[string]any{"success": true}, nil }, nil)
	if err == nil || path != "" || missingRegistry.Active(previous) {
		t.Fatalf("missing native path = %q, %v", path, err)
	}
}

func TestStopResultClassifiesInterruptAndTerminalFailures(t *testing.T) {
	stopping, err := StopResultFor(ErrInterruptPending)
	if err != nil || !stopping.Stopping || stopping.Forced {
		t.Fatalf("stopping = %#v, %v", stopping, err)
	}
	forced, err := StopResultFor(&RequestTimeoutError{Command: "abort"})
	if err != nil || !forced.Forced || forced.Stopping {
		t.Fatalf("forced = %#v, %v", forced, err)
	}
}

type workflowClient struct {
	*registryClient
	state    map[string]any
	stateErr error
}

func (client *workflowClient) GetState(context.Context) (map[string]any, error) {
	return client.state, client.stateErr
}
