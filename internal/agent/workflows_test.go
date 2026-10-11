package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestRestartUnsavedSessionCarriesWhatWasChosenInItToANewClient(t *testing.T) {
	now := time.Unix(1_000, 0)
	old := newChoicesClient("old", "Release plan", LiveStatus{Provider: "e2e", ModelID: "contract-model", ThinkingLevel: "high"})
	old.sequence = 7
	replacement := newChoicesClient("new", "", LiveStatus{Provider: "e2e", ModelID: "fixture-model", ThinkingLevel: "medium"})
	starting, release := make(chan struct{}), make(chan struct{})
	registry := NewRegistry(func(string) (Client, error) { close(starting); <-release; return replacement, nil }, func() time.Time { return now })
	if err := registry.Register("/pending", old); err != nil {
		t.Fatal(err)
	}
	pending := NewPendingSessionRegistry(nil)
	pending.Remember("/pending", "/project", "old")

	result := make(chan error, 1)
	go func() {
		restarted, err := RestartUnsavedSession(context.Background(), "/pending", now, registry, pending)
		if err == nil && !restarted {
			err = errors.New("not restarted")
		}
		result <- err
	}()
	<-starting
	// Until the new client is ready, nothing reaches the session, and the old client's last event is a valid cursor.
	if _, err := registry.EnsureClient("/pending"); !errors.Is(err, ErrClientRetiring) {
		t.Fatalf("client during the restart: %v", err)
	}
	if batch := registry.EventsAfter("/pending", 7); batch.Missed || batch.LastSeq != 7 {
		t.Fatalf("poll during the restart = %#v", batch)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	if registry.Client("/pending") != replacement || !old.closed() || replacement.closed() {
		t.Fatalf("registered new=%v, old closed=%v, new closed=%v", registry.Client("/pending") == replacement, old.closed(), replacement.closed())
	}
	if replacement.status != old.status || replacement.state.Name != "Release plan" {
		t.Fatalf("new client has %#v, named %q", replacement.status, replacement.state.Name)
	}
	if sequence := replacement.EventSequence(); sequence != 7 {
		t.Fatalf("new client numbers its events after %d, want 7", sequence)
	}
	if entries := pending.Entries(); len(entries) != 1 || entries[0].CWD != "/project" || entries[0].ID != "new" {
		t.Fatalf("pending = %#v", entries)
	}
}

func TestRestartUnsavedSessionKeepsTheOldClientWhenTheNewOneRefusesItsModel(t *testing.T) {
	now := time.Unix(1_000, 0)
	old := newChoicesClient("old", "", LiveStatus{Provider: "e2e", ModelID: "contract-model", ThinkingLevel: "high"})
	replacement := newChoicesClient("new", "", LiveStatus{})
	replacement.setModelErr = &RequestError{Message: "No API key for e2e/contract-model"}
	registry := NewRegistry(func(string) (Client, error) { return replacement, nil }, func() time.Time { return now })
	if err := registry.Register("/pending", old); err != nil {
		t.Fatal(err)
	}
	pending := NewPendingSessionRegistry(nil)
	pending.Remember("/pending", "/project", "old")

	restarted, err := RestartUnsavedSession(context.Background(), "/pending", now, registry, pending)

	var refused *RequestError
	if restarted || !errors.As(err, &refused) || refused.Message != "No API key for e2e/contract-model" {
		t.Fatalf("restart = %v, %v", restarted, err)
	}
	if registry.Client("/pending") != old || old.closed() || !replacement.closed() {
		t.Fatalf("registered old=%v, old closed=%v, new closed=%v", registry.Client("/pending") == old, old.closed(), replacement.closed())
	}
	if entries := pending.Entries(); len(entries) != 1 || entries[0].ID != "old" {
		t.Fatalf("pending = %#v", entries)
	}
}

func TestRestartUnsavedSessionLeavesAClientThatStartedAfterTheChangeOrRuns(t *testing.T) {
	now := time.Unix(1_000, 0)
	client := newChoicesClient("old", "", LiveStatus{Provider: "e2e", ModelID: "contract-model"})
	var replacements []*choicesClient
	registry := NewRegistry(func(string) (Client, error) {
		replacement := newChoicesClient("new", "", LiveStatus{})
		replacements = append(replacements, replacement)
		return replacement, nil
	}, func() time.Time { return now })
	if err := registry.Register("/pending", client); err != nil {
		t.Fatal(err)
	}
	pending := NewPendingSessionRegistry(nil)

	if restarted, err := RestartUnsavedSession(context.Background(), "/pending", now.Add(-time.Second), registry, pending); restarted || err != nil {
		t.Fatalf("client that started after the change: %v, %v", restarted, err)
	}
	client.isBusy = true
	if restarted, err := RestartUnsavedSession(context.Background(), "/pending", now, registry, pending); restarted || err != nil {
		t.Fatalf("busy client: %v, %v", restarted, err)
	}
	// Pi can take a prompt before it reports the run that the prompt starts.
	client.isBusy = false
	client.state.Streaming = true
	if restarted, err := RestartUnsavedSession(context.Background(), "/pending", now, registry, pending); restarted || err != nil {
		t.Fatalf("client that took a prompt: %v, %v", restarted, err)
	}
	if registry.Client("/pending") != client || client.closed() || len(replacements) > 1 || (len(replacements) == 1 && !replacements[0].closed()) {
		t.Fatalf("the client was replaced, or a new one left running: %d", len(replacements))
	}
}

func TestRestartUnsavedSessionDoesNotWaitForObservers(t *testing.T) {
	now := time.Unix(1_000, 0)
	old := newChoicesClient("old", "", LiveStatus{})
	replacement := newChoicesClient("new", "", LiveStatus{})
	registry := NewRegistry(func(string) (Client, error) { return replacement, nil }, func() time.Time { return now })
	if err := registry.Register("/pending", old); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	observed := make(chan error, 1)
	go func() {
		observed <- registry.WithObservingClient(context.Background(), "/pending", false, func(Client) error { close(started); <-release; return nil })
	}()
	<-started

	restarted, err := RestartUnsavedSession(context.Background(), "/pending", now, registry, NewPendingSessionRegistry(nil))

	close(release)
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	if !restarted || err != nil || registry.Client("/pending") != replacement {
		t.Fatalf("restart with an observer = %v, %v", restarted, err)
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

// choicesClient serves an unsaved session, which holds the model, thinking level and name chosen in it.
type choicesClient struct {
	*workflowClient
	status      LiveStatus
	setModelErr error
}

func newChoicesClient(id, name string, status LiveStatus) *choicesClient {
	return &choicesClient{workflowClient: &workflowClient{registryClient: newRegistryClient(), state: State{ID: id, Name: name}}, status: status}
}

func (client *choicesClient) LiveStatus(context.Context) (LiveStatus, error) {
	return client.status, nil
}
func (client *choicesClient) ModelSettings(context.Context) (ModelSettings, error) {
	return ModelSettings{}, nil
}
func (client *choicesClient) SetModel(_ context.Context, provider, modelID, thinkingLevel string) (ModelChoice, error) {
	if client.setModelErr != nil {
		return ModelChoice{}, client.setModelErr
	}
	client.status = LiveStatus{Provider: provider, ModelID: modelID, ThinkingLevel: thinkingLevel}
	return ModelChoice{ThinkingLevel: thinkingLevel}, nil
}
func (client *choicesClient) CycleThinkingLevel(context.Context) (string, error) { return "", nil }
func (client *choicesClient) Rename(_ context.Context, name string) error {
	client.state.Name = name
	return nil
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
