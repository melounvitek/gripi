package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/rpc"
)

func TestSynchronizerReconcilesRPCAppendAndDetectsForeignEntries(t *testing.T) {
	root, path := synchronizerSession(t)
	appendSyncEntry(t, path, map[string]any{"type": "message", "id": "old", "parentId": nil, "message": map[string]any{"role": "user", "content": []any{}}})
	client := newSyncClient()
	client.positions["old"] = rpc.SessionEntries{Known: true, LeafID: "old"}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return nil, errors.New("unexpected start") }, nil)
	if err := registry.Register(path, client); err != nil {
		t.Fatal(err)
	}
	synchronizer := NewSynchronizer(root, "", NewCache(), registry)
	if result := inspectSync(t, synchronizer, path, false); result.Mode != SyncManaged {
		t.Fatalf("initial result = %#v", result)
	}

	appendSyncEntry(t, path, map[string]any{"type": "message", "id": "gateway", "parentId": "old", "message": map[string]any{"role": "assistant", "content": []any{}}})
	client.mu.Lock()
	client.positions["old"] = rpc.SessionEntries{Known: true, LeafID: "gateway", Entries: []map[string]any{{"id": "gateway"}}}
	client.mu.Unlock()
	if result := inspectSync(t, synchronizer, path, false); result.Mode != SyncManaged || result.RPCLeafID != "gateway" {
		t.Fatalf("gateway append = %#v", result)
	}

	appendSyncEntry(t, path, map[string]any{"type": "message", "id": "pi-cli", "parentId": "gateway", "message": map[string]any{"role": "user", "content": []any{}}})
	appendSyncEntry(t, path, map[string]any{"type": "message", "id": "known-tail", "parentId": "gateway", "message": map[string]any{"role": "assistant", "content": []any{}}})
	client.mu.Lock()
	client.positions["gateway"] = rpc.SessionEntries{Known: true, LeafID: "known-tail", Entries: []map[string]any{{"id": "known-tail"}}}
	client.mu.Unlock()
	result := inspectSync(t, synchronizer, path, false)
	if result.Mode != SyncExternalFollow || !client.closedValue() {
		t.Fatalf("foreign append = %#v, closed=%v", result, client.closedValue())
	}
}

func TestFileSnapshotStreamsLargeFinalEntryMetadata(t *testing.T) {
	root, path := synchronizerSession(t)
	appendSyncEntry(t, path, map[string]any{"type": "message", "id": "large", "parentId": nil, "message": map[string]any{"role": "toolResult", "toolCallId": "tool", "toolName": "read", "content": []any{map[string]any{"type": "text", "text": strings.Repeat("x", 1<<20)}}}})
	snapshot, err := (Store{Root: root, Cache: NewCache()}).FileSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AppendCursor != "large" || snapshot.PersistedLeafID != "large" || !snapshot.Complete {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestSynchronizerRejectsSameSizeInPlaceRewrite(t *testing.T) {
	root, path := synchronizerSession(t)
	appendSyncEntry(t, path, map[string]any{"type": "message", "id": "old", "parentId": nil, "message": map[string]any{"role": "user", "content": []any{}}})
	client := newSyncClient()
	client.positions["old"] = rpc.SessionEntries{Known: true, LeafID: "old"}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return client, nil }, nil)
	if err := registry.Register(path, client); err != nil {
		t.Fatal(err)
	}
	synchronizer := NewSynchronizer(root, "", NewCache(), registry)
	_ = inspectSync(t, synchronizer, path, false)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := bytes.Replace(contents, []byte(`"id":"old"`), []byte(`"id":"new"`), 1)
	if len(rewritten) != len(contents) {
		t.Fatal("rewrite changed size")
	}
	if err := os.WriteFile(path, rewritten, 0600); err != nil {
		t.Fatal(err)
	}
	result := inspectSync(t, synchronizer, path, false)
	if result.Mode != SyncConflict || !strings.Contains(result.Error, "without an append") {
		t.Fatalf("result = %#v", result)
	}
}

func TestSynchronizerFailsClosedForAppendedEntryWithoutID(t *testing.T) {
	root, path := synchronizerSession(t)
	appendSyncEntry(t, path, map[string]any{"type": "message", "id": "old", "parentId": nil, "message": map[string]any{"role": "user", "content": []any{}}})
	client := newSyncClient()
	client.positions["old"] = rpc.SessionEntries{Known: true, LeafID: "old"}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return client, nil }, nil)
	if err := registry.Register(path, client); err != nil {
		t.Fatal(err)
	}
	synchronizer := NewSynchronizer(root, "", NewCache(), registry)
	_ = inspectSync(t, synchronizer, path, false)
	appendSyncEntry(t, path, map[string]any{"type": "custom"})
	appendSyncEntry(t, path, map[string]any{"type": "message", "id": "gateway", "parentId": "old", "message": map[string]any{"role": "assistant", "content": []any{}}})
	client.mu.Lock()
	client.positions["old"] = rpc.SessionEntries{Known: true, LeafID: "gateway", Entries: []map[string]any{{"id": "gateway"}}}
	client.mu.Unlock()
	result := inspectSync(t, synchronizer, path, false)
	if result.Mode != SyncConflict || !strings.Contains(result.Error, "missing a string id") {
		t.Fatalf("result = %#v", result)
	}
}

func TestSynchronizerTakeoverDetectsConcurrentPiCLIWrite(t *testing.T) {
	root, path := synchronizerSession(t)
	appendSyncEntry(t, path, map[string]any{"type": "message", "id": "external", "parentId": nil, "message": map[string]any{"role": "user", "content": []any{}}})
	stale := newSyncClient()
	stale.positions["external"] = rpc.SessionEntries{Known: false}
	fresh := newSyncClient()
	fresh.positionHook = func() {
		appendSyncEntry(t, path, map[string]any{"type": "message", "id": "later", "parentId": "external", "message": map[string]any{"role": "assistant", "content": []any{}}})
	}
	fresh.positions["external"] = rpc.SessionEntries{Known: true, LeafID: "external"}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return fresh, nil }, nil)
	if err := registry.Register(path, stale); err != nil {
		t.Fatal(err)
	}
	synchronizer := NewSynchronizer(root, "", NewCache(), registry)
	if result := inspectSync(t, synchronizer, path, false); result.Mode != SyncExternalFollow {
		t.Fatalf("stale result = %#v", result)
	}
	_, err := synchronizer.TakeOver(context.Background(), path)
	var blocked *SyncBlockedError
	if !errors.As(err, &blocked) || blocked.Mode != SyncExternalFollow {
		t.Fatalf("takeover error = %v", err)
	}
	if !fresh.closedValue() {
		t.Fatal("fresh takeover client was not retired")
	}
}

func TestSynchronizerDoesNotTurnRequestCancellationIntoConflict(t *testing.T) {
	root, path := synchronizerSession(t)
	appendSyncEntry(t, path, map[string]any{"type": "message", "id": "old", "parentId": nil, "message": map[string]any{"role": "user", "content": []any{}}})
	client := newSyncClient()
	client.positionErr = context.Canceled
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return client, nil }, nil)
	if err := registry.Register(path, client); err != nil {
		t.Fatal(err)
	}
	synchronizer := NewSynchronizer(root, "", NewCache(), registry)
	if _, err := synchronizer.Inspect(context.Background(), path, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("inspect error = %v", err)
	}
	if blocked := synchronizer.KnownBlocked(path); blocked != nil {
		t.Fatalf("cancellation poisoned state: %#v", blocked)
	}
	if result := synchronizer.InspectIfAvailable(context.Background(), path, true); result != nil {
		t.Fatalf("cancelled available inspection = %#v", result)
	}
}

func TestSynchronizerLifecycleIsSafeDuringConcurrentObservation(t *testing.T) {
	root, path := synchronizerSession(t)
	appendSyncEntry(t, path, map[string]any{"type": "message", "id": "old", "parentId": nil, "message": map[string]any{"role": "user", "content": []any{}}})
	client := newSyncClient()
	client.positions["old"] = rpc.SessionEntries{Known: true, LeafID: "old"}
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { return client, nil }, nil)
	if err := registry.Register(path, client); err != nil {
		t.Fatal(err)
	}
	synchronizer := NewSynchronizer(root, "", NewCache(), registry)
	const workers = 12
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 100; iteration++ {
				_ = synchronizer.InspectIfAvailable(context.Background(), path, iteration%3 == 0)
				if iteration%7 == 0 {
					synchronizer.Forget(path)
				}
			}
		}()
	}
	group.Wait()
	if synchronizer.locks.Len() != 0 {
		t.Fatalf("session lock entries after concurrent observation = %d", synchronizer.locks.Len())
	}
	result := inspectSync(t, synchronizer, path, true)
	if result.Mode != SyncManaged {
		t.Fatalf("final result = %#v", result)
	}
	if synchronizer.locks.Len() != 0 {
		t.Fatalf("session lock entries after final inspection = %d", synchronizer.locks.Len())
	}
	if err := registry.CloseAll(); err != nil {
		t.Fatal(err)
	}
}

func inspectSync(t *testing.T, synchronizer *Synchronizer, path string, includePosition bool) SyncResult {
	t.Helper()
	result, err := synchronizer.Inspect(context.Background(), path, includePosition)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func synchronizerSession(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session.jsonl")
	appendSyncEntry(t, path, map[string]any{"type": "session", "version": 3, "id": "session", "cwd": project})
	return root, path
}
func appendSyncEntry(t *testing.T, path string, entry map[string]any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(entry)
	if err == nil {
		_, err = file.Write(append(encoded, '\n'))
	}
	closeErr := file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

type syncClient struct {
	mu           sync.Mutex
	positions    map[string]rpc.SessionEntries
	positionHook func()
	positionErr  error
	closed       bool
}

func newSyncClient() *syncClient { return &syncClient{positions: make(map[string]rpc.SessionEntries)} }
func (client *syncClient) Close() error {
	client.mu.Lock()
	client.closed = true
	client.mu.Unlock()
	return nil
}
func (client *syncClient) closedValue() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.closed
}
func (client *syncClient) Busy() bool               { return false }
func (client *syncClient) BusySince() *time.Time    { return nil }
func (client *syncClient) SettledAt() *time.Time    { return nil }
func (client *syncClient) AgentRunning() bool       { return false }
func (client *syncClient) Compacting() bool         { return false }
func (client *syncClient) EventSequence() int64     { return 0 }
func (client *syncClient) EventReplayCursor() int64 { return 0 }
func (client *syncClient) EventsAfter(int64) rpc.EventBatch {
	return rpc.EventBatch{Events: []map[string]any{}}
}
func (client *syncClient) LiveSnapshot() rpc.LiveSnapshot {
	return rpc.LiveSnapshot{ActiveToolEvents: []map[string]any{}}
}
func (client *syncClient) GetState(context.Context) (map[string]any, error)        { return nil, nil }
func (client *syncClient) GetSessionStats(context.Context) (map[string]any, error) { return nil, nil }
func (client *syncClient) GetCommands(context.Context) (map[string]any, error)     { return nil, nil }
func (client *syncClient) SessionPosition(_ context.Context, cursor string) (rpc.SessionEntries, error) {
	client.mu.Lock()
	hook := client.positionHook
	client.positionHook = nil
	position := client.positions[cursor]
	err := client.positionErr
	client.mu.Unlock()
	if hook != nil {
		hook()
	}
	return position, err
}
func (client *syncClient) SessionEntriesAfter(ctx context.Context, cursor string) (rpc.SessionEntries, error) {
	return client.SessionPosition(ctx, cursor)
}
