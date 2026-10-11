package agent

import (
	"context"
	"errors"
	"fmt"
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
	client := &workflowClient{registryClient: newRegistryClient(), state: State{Key: path}}
	registry := NewRegistry(func(string) (Client, error) { return nil, os.ErrNotExist }, nil)
	pending := NewPendingSessionRegistry(nil)

	result, err := StartNewSession(context.Background(), project, func(cwd string) (Client, error) {
		if cwd != project {
			t.Fatalf("cwd = %q", cwd)
		}
		return client, nil
	}, workflowStore{}, registry, pending, nil)
	if err != nil || result != path || !registry.Active(path) {
		t.Fatalf("result = %q, active=%v, err=%v", result, registry.Active(path), err)
	}
	if cwd, ok := pending.CWD(path); !ok || cwd != project {
		t.Fatalf("pending cwd = %q, %v", cwd, ok)
	}
}

func TestStartNewSessionUsesAPendingKeyUntilTheAgentNamesTheSession(t *testing.T) {
	for _, test := range []struct {
		name, id string
		err      error
	}{{"unnamed", "01a-new", nil}, {"refused", "", &RequestError{Message: "not now"}}} {
		t.Run(test.name, func(t *testing.T) {
			client := &workflowClient{registryClient: newRegistryClient(), state: State{ID: test.id}, stateErr: test.err}
			registry := NewRegistry(func(string) (Client, error) { return nil, os.ErrNotExist }, nil)
			pending := NewPendingSessionRegistry(nil)
			result, err := StartNewSession(context.Background(), "/project", func(string) (Client, error) { return client, nil }, workflowStore{}, registry, pending, nil)
			if err != nil || result != pendingWorkflowKey || registry.Client(result) != client {
				t.Fatalf("result = %q, err = %v", result, err)
			}
			if entries := pending.Entries(); len(entries) != 1 || entries[0].Path != pendingWorkflowKey || entries[0].ID != test.id {
				t.Fatalf("pending = %#v", entries)
			}
		})
	}
}

func TestStartNewSessionKeepsClientWhenDisplacedCloseFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.jsonl")
	old := newRegistryClient()
	old.closeErr = errors.New("close failed")
	client := &workflowClient{registryClient: newRegistryClient(), state: State{Key: path}}
	registry := NewRegistry(func(string) (Client, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(path, old); err != nil {
		t.Fatal(err)
	}

	result, err := StartNewSession(context.Background(), t.TempDir(), func(string) (Client, error) { return client, nil }, workflowStore{}, registry, NewPendingSessionRegistry(nil), nil)
	if err != nil || result != path {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if registry.Client(path) != client || client.closed() || !old.closed() {
		t.Fatalf("registered=%v client_closed=%v old_closed=%v", registry.Client(path) == client, client.closed(), old.closed())
	}
}

func TestStartNewSessionClosesClientWhenOwnershipClaimFails(t *testing.T) {
	client := &workflowClient{registryClient: newRegistryClient(), state: State{Key: "/new"}}
	registry := NewRegistry(func(string) (Client, error) { return nil, os.ErrNotExist }, nil)
	claimErr := errors.New("claim failed")
	_, err := StartNewSession(context.Background(), t.TempDir(), func(string) (Client, error) { return client, nil }, workflowStore{}, registry, NewPendingSessionRegistry(nil), func(string) (func() error, error) {
		return nil, claimErr
	})
	if !errors.Is(err, claimErr) || !client.closed() || registry.Active("/new") {
		t.Fatalf("err=%v closed=%v active=%v", err, client.closed(), registry.Active("/new"))
	}
}

func TestStartNewSessionClosesClientWhenStateFails(t *testing.T) {
	client := &workflowClient{registryClient: newRegistryClient(), stateErr: errors.New("state failed")}
	_, err := StartNewSession(context.Background(), t.TempDir(), func(string) (Client, error) { return client, nil }, workflowStore{}, NewRegistry(func(string) (Client, error) { return nil, nil }, nil), NewPendingSessionRegistry(nil), nil)
	if err == nil || !client.closed() {
		t.Fatalf("err=%v closed=%v", err, client.closed())
	}
}

func TestBranchSessionMovesAndTracksTheClient(t *testing.T) {
	root := t.TempDir()
	previous, next := filepath.Join(root, "previous.jsonl"), filepath.Join(root, "next.jsonl")
	client := &workflowClient{registryClient: newRegistryClient()}
	registry := NewRegistry(func(string) (Client, error) { return nil, os.ErrNotExist }, nil)
	if err := registry.Register(previous, client); err != nil {
		t.Fatal(err)
	}
	pending := NewPendingSessionRegistry(nil)

	result, _, err := BranchSession(context.Background(), previous, root, registry, workflowStore{}, pending, func(Client) (Branch, error) {
		return Branch{Key: next, ID: "01a-next", Moved: true}, nil
	}, nil)
	if err != nil || result != next || registry.Active(previous) || !registry.Active(next) {
		t.Fatalf("result=%q previous=%v next=%v err=%v", result, registry.Active(previous), registry.Active(next), err)
	}
	if entries := pending.Entries(); len(entries) != 1 || entries[0].Path != next || entries[0].CWD != root || entries[0].ID != "01a-next" {
		t.Fatalf("pending = %#v", entries)
	}
}

func TestBranchSessionNormalizesMaterializedAndPendingNativePaths(t *testing.T) {
	for _, materialized := range []bool{false, true} {
		t.Run(fmt.Sprintf("materialized=%v", materialized), func(t *testing.T) {
			root := t.TempDir()
			previous := filepath.Join(root, "previous.jsonl")
			reported := filepath.Join(root, "physical.jsonl")
			configured := filepath.Join(root, "configured.jsonl")
			if materialized {
				if err := os.WriteFile(configured, []byte("session\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			client := &workflowClient{registryClient: newRegistryClient()}
			registry := NewRegistry(func(string) (Client, error) { return nil, os.ErrNotExist }, nil)
			if err := registry.Register(previous, client); err != nil {
				t.Fatal(err)
			}
			pending := NewPendingSessionRegistry(nil)

			result, _, err := BranchSession(context.Background(), previous, root, registry, workflowStore{canonical: map[string]string{reported: configured}}, pending, func(Client) (Branch, error) {
				return Branch{Key: reported, Moved: true}, nil
			}, nil)
			if err != nil || result != configured || registry.Active(previous) || registry.Active(reported) || !registry.Active(configured) {
				t.Fatalf("result=%q previous=%v reported=%v configured=%v err=%v", result, registry.Active(previous), registry.Active(reported), registry.Active(configured), err)
			}
			_, pendingFound := pending.CWD(configured)
			if pendingFound == materialized {
				t.Fatalf("pending = %v, materialized = %v", pendingFound, materialized)
			}
		})
	}
}

func TestBranchSessionKeepsTheClientWhenItStaysWithThePreviousSession(t *testing.T) {
	for _, test := range []struct {
		name    string
		branch  Branch
		err     error
		wantErr bool
	}{
		{"refused", Branch{}, &RequestError{Message: "Entry not found"}, true},
		{"cancelled", Branch{Cancelled: true}, nil, false},
		{"not followed", Branch{Key: "/unexpected"}, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &workflowClient{registryClient: newRegistryClient()}
			registry := NewRegistry(func(string) (Client, error) { return nil, os.ErrNotExist }, nil)
			if err := registry.Register("/previous", client); err != nil {
				t.Fatal(err)
			}
			path, _, err := BranchSession(context.Background(), "/previous", "/project", registry, workflowStore{}, NewPendingSessionRegistry(nil), func(Client) (Branch, error) {
				return test.branch, test.err
			}, nil)
			if path != "/previous" || !registry.Active("/previous") || registry.Active("/unexpected") || client.closed() || (err != nil) != test.wantErr {
				t.Fatalf("path=%q active=%v closed=%v err=%v", path, registry.Active("/previous"), client.closed(), err)
			}
		})
	}
}

func TestBranchSessionFetchesPostSwitchStateInsideTheOriginalLaneAndDoesNotFallBack(t *testing.T) {
	previous, next := "/previous", "/next"
	client := &workflowClient{registryClient: newRegistryClient()}
	registry := NewRegistry(func(string) (Client, error) { return newRegistryClient(), nil }, nil)
	if err := registry.Register(previous, client); err != nil {
		t.Fatal(err)
	}
	switchStarted, releaseSwitch := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, _, err := BranchSession(context.Background(), previous, "/project", registry, workflowStore{}, NewPendingSessionRegistry(nil), func(Client) (Branch, error) {
			close(switchStarted)
			<-releaseSwitch
			return Branch{Key: next, Moved: true}, nil
		}, nil)
		result <- err
	}()
	<-switchStarted
	if err := registry.WithExistingClient(context.Background(), previous, true, func(Client) error { return nil }); !errors.Is(err, ErrOperationPending) {
		t.Fatalf("competing operation = %v", err)
	}
	close(releaseSwitch)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if registry.Active(previous) || !registry.Active(next) {
		t.Fatal("post-switch client was not remapped before releasing the operation lane")
	}

	missing := &workflowClient{registryClient: newRegistryClient()}
	missingRegistry := NewRegistry(func(string) (Client, error) { return nil, os.ErrNotExist }, nil)
	if err := missingRegistry.Register(previous, missing); err != nil {
		t.Fatal(err)
	}
	path, _, err := BranchSession(context.Background(), previous, "/project", missingRegistry, workflowStore{}, NewPendingSessionRegistry(nil), func(Client) (Branch, error) { return Branch{Moved: true}, nil }, nil)
	if err == nil || path != "" || missingRegistry.Active(previous) {
		t.Fatalf("missing native path = %q, %v", path, err)
	}
}

type workflowClient struct {
	*registryClient
	state    State
	stateErr error
}

func (client *workflowClient) State(context.Context) (State, error) {
	return client.state, client.stateErr
}

const pendingWorkflowKey = "/sessions/pending.jsonl"

// workflowStore keys sessions by path, and maps the keys in canonical to others.
type workflowStore struct {
	Store
	canonical map[string]string
}

func (store workflowStore) Exists(key string) bool {
	_, err := os.Stat(key)
	return err == nil
}

func (store workflowStore) Canonical(key string) (string, bool) {
	if canonical, ok := store.canonical[key]; ok {
		return canonical, true
	}
	return key, true
}

func (workflowStore) PendingKey() string { return pendingWorkflowKey }
