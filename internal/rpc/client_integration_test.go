package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientUsesNativeFakePiRPCWithoutRewritingSessionOnReads(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required")
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session.jsonl")
	contents := `{"type":"session","version":3,"id":"rpc-test","timestamp":"2026-01-01T00:00:00Z","cwd":` + quoted(project) + `}` + "\n" +
		`{"type":"message","id":"entry-1","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"before"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "fake.log")
	t.Setenv("GRIPI_E2E_FAKE_PI_LOG", logPath)
	t.Setenv("GRIPI_E2E_SESSIONS_ROOT", root)
	client, err := Start(path, []string{node, filepath.Join(repo, "e2e", "support", "fake_pi.mjs")}, filepath.Join(repo, "pi_extensions", "gripi-tree.ts"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	state, err := client.GetState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := state["data"].(map[string]any)
	if data["sessionFile"] != path || data["thinkingLevel"] != "medium" {
		t.Fatalf("state = %#v", state)
	}
	entries, err := client.SessionEntriesAfter(ctx, "entry-1")
	if err != nil || !entries.Known || entries.LeafID != "entry-1" || len(entries.Entries) != 0 {
		t.Fatalf("entries = %#v, err = %v", entries, err)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != contents {
		t.Fatalf("read-only RPC changed Pi JSONL: %v", err)
	}

	bashCursor := client.EventSequence()
	bashResponse, err := client.Bash(ctx, "printf fake", true)
	if err != nil || bashResponse["success"] != true {
		t.Fatalf("bash = %#v, %v", bashResponse, err)
	}
	bashEvents := client.EventsAfter(bashCursor).Events
	if !containsEvent(bashEvents, "bash_start") || !containsEvent(bashEvents, "bash_end") {
		t.Fatalf("bash events = %#v", bashEvents)
	}

	response, err := client.Prompt(ctx, "integration prompt", nil)
	if err != nil || response["success"] != true {
		t.Fatalf("prompt = %#v, %v", response, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var batch EventBatch
	for time.Now().Before(deadline) {
		batch = client.EventsAfter(0)
		if containsEvent(batch.Events, "agent_settled") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, eventType := range []string{"agent_start", "message_start", "turn_start", "tool_execution_start", "tool_execution_end", "agent_end", "agent_settled"} {
		if !containsEvent(batch.Events, eventType) {
			t.Errorf("missing %s in %#v", eventType, eventTypes(batch.Events))
		}
	}
	if client.Busy() || client.AgentRunning() {
		t.Fatal("client remained busy after agent_settled")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == contents || !strings.Contains(string(after), `"role":"assistant"`) {
		t.Fatal("fake Pi did not perform its native append")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(after)), "\n") {
		var value map[string]any
		if json.Unmarshal([]byte(line), &value) != nil {
			t.Fatalf("invalid native JSONL: %q", line)
		}
	}
}

func TestProcessClientPreservesFinalResponseBeforeImmediateExit(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required")
	}
	root := t.TempDir()
	script := filepath.Join(root, "exit-after-response.mjs")
	contents := `import readline from "node:readline"; const lines = readline.createInterface({ input: process.stdin }); lines.once("line", line => { const request = JSON.parse(line); process.stdout.end(JSON.stringify({ type: "response", id: request.id, command: request.type, success: true, data: { final: true } }) + "\n"); }); process.stdout.once("finish", () => process.exit(0));`
	if err := os.WriteFile(script, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 20; iteration++ {
		client, err := Start(filepath.Join(root, "unused.jsonl"), []string{node, script}, script, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, requestErr := client.GetState(context.Background())
		closeErr := client.Close()
		if requestErr != nil || response["success"] != true || closeErr != nil {
			t.Fatalf("iteration %d: response=%#v request=%v close=%v", iteration, response, requestErr, closeErr)
		}
	}
}

func TestQueueCompactionFollowUpAtomicallyReportsWhetherItQueued(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	defer stdinReader.Close()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{})
	t.Cleanup(func() { _ = client.Close() })

	response, queued, err := client.QueueCompactionFollowUp(context.Background(), "before", nil)
	if err != nil || queued || response != nil {
		t.Fatalf("queue before compaction = %#v, %v, %v", response, queued, err)
	}
	writeRecord(t, stdoutWriter, map[string]any{"type": "compaction_start"})
	waitSequence(t, client, 1)
	response, queued, err = client.QueueCompactionFollowUp(context.Background(), "during", nil)
	if err != nil || !queued || response["queued"] != true {
		t.Fatalf("queue during compaction = %#v, %v, %v", response, queued, err)
	}
	writeRecord(t, stdoutWriter, map[string]any{"type": "compaction_end"})
	waitSequence(t, client, 2)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		client.mu.Lock()
		flushing := client.flushingCompactionFollowUps
		client.mu.Unlock()
		if flushing {
			break
		}
		time.Sleep(time.Millisecond)
	}
	response, queued, err = client.QueueCompactionFollowUp(context.Background(), "while flushing", nil)
	if err != nil || !queued || response["queued"] != true {
		t.Fatalf("queue while flushing = %#v, %v, %v", response, queued, err)
	}
	_ = stdoutWriter.Close()
}

func TestImageCommandStreamsPersistedBytesWithoutLargeWrites(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "image.png")
	contents := bytes.Repeat([]byte("image-data"), 1<<17)
	if err := os.WriteFile(imagePath, contents, 0600); err != nil {
		t.Fatal(err)
	}
	writer := &boundedWriteCloser{}
	client := NewClient(writer, io.NopCloser(strings.NewReader("")), nil, ClientOptions{})
	<-client.readerDone
	value := map[string]any{"id": "prompt-1", "type": "prompt", "message": "hello", "images": []PromptImage{{Path: imagePath, MIMEType: "image/png", Size: int64(len(contents))}}}
	if err := client.writeCommandUnlocked(value, "prompt", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if writer.maximum > RPCReadChunkBytes {
		t.Fatalf("largest write = %d", writer.maximum)
	}
	var payload map[string]any
	if err := json.Unmarshal(writer.bytes, &payload); err != nil {
		t.Fatal(err)
	}
	images := payload["images"].([]any)
	image := images[0].(map[string]any)
	if image["type"] != "image" || image["mimeType"] != "image/png" || image["data"] != base64.StdEncoding.EncodeToString(contents) {
		t.Fatal("streamed payload changed native Pi image shape")
	}

	if err := os.WriteFile(imagePath, append(contents, 'x'), 0600); err != nil {
		t.Fatal(err)
	}
	changedWriter := &boundedWriteCloser{}
	client.stdin = changedWriter
	if err := client.writeCommandUnlocked(value, "prompt", time.Time{}); err == nil || len(changedWriter.bytes) != 0 {
		t.Fatalf("changed persisted image was streamed: bytes=%d err=%v", len(changedWriter.bytes), err)
	}
}

type boundedWriteCloser struct {
	bytes   []byte
	maximum int
}

func (writer *boundedWriteCloser) Write(value []byte) (int, error) {
	writer.maximum = max(writer.maximum, len(value))
	writer.bytes = append(writer.bytes, value...)
	return len(value), nil
}
func (*boundedWriteCloser) Close() error { return nil }

func TestClientCompactionFlushDoesNotBlockStdoutAndTimesOut(t *testing.T) {
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{RequestTimeout: 30 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	writeRecord(t, stdoutWriter, map[string]any{"type": "compaction_start"})
	waitSequence(t, client, 1)
	response, err := client.FollowUp(context.Background(), strings.Repeat("blocked", 1<<20), nil)
	if err != nil || response["queued"] != true {
		t.Fatalf("queued follow-up = %#v, %v", response, err)
	}
	writeRecord(t, stdoutWriter, map[string]any{"type": "compaction_end"})
	written := make(chan struct{})
	go func() {
		writeRecord(t, stdoutWriter, map[string]any{"type": "turn_start"})
		close(written)
	}()
	waitSequence(t, client, 3)
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("stdout remained blocked behind compaction follow-up flush")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		client.mu.Lock()
		flushing := client.flushingCompactionFollowUps
		client.mu.Unlock()
		if !flushing {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed-out compaction flush remained active")
}

func TestClientBoundsFollowUpsWaitingForCompaction(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	defer stdinReader.Close()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{})
	t.Cleanup(func() { _ = client.Close() })
	writeRecord(t, stdoutWriter, map[string]any{"type": "compaction_start"})
	waitSequence(t, client, 1)
	for index := 0; index < MaxCompactionFollowUps; index++ {
		response, err := client.FollowUp(context.Background(), fmt.Sprintf("follow-up %d", index), nil)
		if err != nil || response["success"] != true {
			t.Fatalf("follow-up %d = %#v, %v", index, response, err)
		}
	}
	response, err := client.FollowUp(context.Background(), "one too many", nil)
	if err != nil || response["success"] != false {
		t.Fatalf("unbounded follow-up = %#v, %v", response, err)
	}
	client.mu.Lock()
	count, bytes := client.compactionFollowUpCount, client.compactionFollowUpBytes
	client.mu.Unlock()
	if count != MaxCompactionFollowUps || bytes > MaxCompactionFollowUpBytes {
		t.Fatalf("bounded follow-ups: count=%d bytes=%d", count, bytes)
	}
	_ = stdoutWriter.Close()
}

func TestClientBoundsQueuedMessagesAndExtensionUISnapshots(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	defer stdinReader.Close()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{})
	t.Cleanup(func() { _ = client.Close() })
	queued := make([]any, MaxQueuedMessageCount*2)
	for index := range queued {
		queued[index] = strings.Repeat("q", MaxSnapshotStringBytes)
	}
	writeRecord(t, stdoutWriter, map[string]any{"type": "queue_update", "steering": queued, "followUp": queued})
	sequence := int64(1)
	for index := 0; index < MaxExtensionUIItems*2; index++ {
		writeRecord(t, stdoutWriter, map[string]any{"type": "extension_ui_request", "id": fmt.Sprintf("dialog-%03d", index), "method": "input", "title": strings.Repeat("d", MaxExtensionUIItemBytes)})
		writeRecord(t, stdoutWriter, map[string]any{"type": "extension_ui_request", "method": "setStatus", "statusKey": fmt.Sprintf("status-%03d", index), "statusText": strings.Repeat("s", MaxExtensionUIItemBytes)})
		writeRecord(t, stdoutWriter, map[string]any{"type": "extension_ui_request", "method": "setWidget", "widgetKey": fmt.Sprintf("widget-%03d", index), "widgetLines": []any{strings.Repeat("w", MaxExtensionUIItemBytes)}})
		sequence += 3
	}
	waitSequence(t, client, sequence)
	snapshot := client.LiveSnapshot()
	if len(snapshot.QueuedMessages["steering"])+len(snapshot.QueuedMessages["followUp"]) > MaxQueuedMessageCount || jsonSize(snapshot.QueuedMessages) > MaxQueuedMessageBytes {
		t.Fatalf("queued snapshot is unbounded: count=%d bytes=%d", len(snapshot.QueuedMessages["steering"])+len(snapshot.QueuedMessages["followUp"]), jsonSize(snapshot.QueuedMessages))
	}
	dialogs := snapshot.ExtensionUI["pending_dialogs"].([]map[string]any)
	statuses := snapshot.ExtensionUI["statuses"].([]map[string]any)
	widgets := snapshot.ExtensionUI["widgets"].([]map[string]any)
	if len(dialogs) > MaxExtensionUIItems || len(statuses) > MaxExtensionUIItems || len(widgets) > MaxExtensionUIItems || jsonSize(snapshot.ExtensionUI) > MaxExtensionUISnapshotBytes {
		t.Fatalf("extension snapshot is unbounded: dialogs=%d statuses=%d widgets=%d bytes=%d", len(dialogs), len(statuses), len(widgets), jsonSize(snapshot.ExtensionUI))
	}
	if dialogs[len(dialogs)-1]["id"] != fmt.Sprintf("dialog-%03d", MaxExtensionUIItems*2-1) {
		t.Fatalf("extension eviction did not preserve latest dialog: %#v", dialogs[len(dialogs)-1])
	}
	_ = stdoutWriter.Close()
}

func TestClientFailsExplicitlyOnOversizedFallbackLine(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{FallbackRPCLineBytes: RPCReadChunkBytes + 1024, RequestTimeout: time.Second})
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	result := make(chan error, 1)
	go func() {
		_, err := client.GetState(context.Background())
		result <- err
	}()
	var command map[string]any
	if err := json.NewDecoder(stdinReader).Decode(&command); err != nil {
		t.Fatal(err)
	}
	oversized := map[string]any{"type": "response", "id": command["id"], "success": true, "data": strings.Repeat("x", RPCReadChunkBytes+2048)}
	writeRecord(t, stdoutWriter, oversized)
	if err := <-result; !errors.Is(err, ErrRPCLineTooLarge) {
		t.Fatalf("oversized fallback error = %v", err)
	}
}

func TestClientAcceptsFallbackResponseNearConfiguredLimit(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	limit := RPCReadChunkBytes + 32<<10
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{FallbackRPCLineBytes: limit, RequestTimeout: time.Second})
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	result := make(chan error, 1)
	go func() {
		response, err := client.GetState(context.Background())
		if err == nil && response["success"] != true {
			err = errors.New("response was unsuccessful")
		}
		result <- err
	}()
	var command map[string]any
	if err := json.NewDecoder(stdinReader).Decode(&command); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, stdoutWriter, map[string]any{"type": "response", "id": command["id"], "success": true, "data": strings.Repeat("x", limit-(4<<10))})
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestClientBoundsReplayAndCoalescesLiveUpdates(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	defer stdinReader.Close()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{EventBufferLimit: 2, EventBufferBytes: 1 << 20})
	t.Cleanup(func() { _ = client.Close() })
	writeRecord(t, stdoutWriter, map[string]any{"type": "message_update", "message": map[string]any{"content": "one"}})
	writeRecord(t, stdoutWriter, map[string]any{"type": "message_update", "message": map[string]any{"content": "two"}})
	writeRecord(t, stdoutWriter, map[string]any{"type": "turn_start"})
	writeRecord(t, stdoutWriter, map[string]any{"type": "turn_end"})
	waitSequence(t, client, 4)
	batch := client.EventsAfter(0)
	if !batch.Missed || batch.LastSeq != 4 || len(batch.Events) != 0 {
		t.Fatalf("bounded batch = %#v", batch)
	}
	batch = client.EventsAfter(client.EventReplayCursor())
	if len(batch.Events) != 2 || batch.Events[0]["type"] != "turn_start" || batch.Events[1]["type"] != "turn_end" {
		t.Fatalf("replay = %#v", batch)
	}
	_ = stdoutWriter.Close()
}

func TestClientSamplesOversizedNativeToolUpdatesAndBoundsActiveSnapshots(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	defer stdinReader.Close()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{OversizedToolUpdateSampleInterval: time.Hour})
	t.Cleanup(func() { _ = client.Close() })
	payload := strings.Repeat("x", RPCReadChunkBytes)
	writeNativeToolUpdate(t, stdoutWriter, "sampled", "bash", map[string]any{"content": []any{map[string]any{"type": "text", "text": payload}}})
	waitSequence(t, client, 1)
	writeNativeToolUpdate(t, stdoutWriter, "sampled", "bash", map[string]any{"content": []any{map[string]any{"type": "text", "text": payload + "second"}}})
	time.Sleep(20 * time.Millisecond)
	if client.EventSequence() != 1 {
		t.Fatalf("immediate oversized update was not coalesced at read time: sequence=%d", client.EventSequence())
	}
	writeRecord(t, stdoutWriter, map[string]any{"type": "tool_execution_end", "toolCallId": "sampled", "toolName": "bash"})
	writeNativeToolUpdate(t, stdoutWriter, "subagent", "subagent", map[string]any{"content": []any{map[string]any{"type": "text", "text": payload}}, "details": map[string]any{"task": payload, "tools": []any{map[string]any{"name": "read", "output": payload}}, "usage": map[string]any{"turns": 2}}})
	deadline := time.Now().Add(2 * time.Second)
	for len(client.LiveSnapshot().ActiveToolEvents) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	batch := client.EventsAfter(0)
	if countEvent(batch.Events, "tool_execution_update") != 1 {
		t.Fatalf("oversized replay = %#v", eventTypes(batch.Events))
	}
	snapshot := client.LiveSnapshot()
	if len(snapshot.ActiveToolEvents) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	encoded, _ := json.Marshal(snapshot.ActiveToolEvents[0])
	if len(encoded) > MaxActiveToolSnapshotBytes {
		t.Fatalf("snapshot bytes = %d", len(encoded))
	}
	_ = stdoutWriter.Close()
}

func TestClientAllowsOnlyOneExtensionUIAnswer(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{})
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	writeRecord(t, stdoutWriter, map[string]any{"type": "extension_ui_request", "id": "confirm-1", "method": "confirm"})
	waitSequence(t, client, 1)
	confirmed := true
	firstDone := make(chan error, 1)
	go func() {
		_, err := client.ExtensionUIResponse(context.Background(), "confirm-1", nil, &confirmed, false)
		firstDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := client.LiveSnapshot()
		dialogs := snapshot.ExtensionUI["pending_dialogs"].([]map[string]any)
		if len(dialogs) == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	second, err := client.ExtensionUIResponse(context.Background(), "confirm-1", nil, &confirmed, false)
	if err != nil || second["success"] != false {
		t.Fatalf("second response = %#v, %v", second, err)
	}
	var command map[string]any
	if err := json.NewDecoder(stdinReader).Decode(&command); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestClientTimesOutResponsesAndDiscardsLateRPCReplies(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{RequestTimeout: 30 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	commandRead := make(chan map[string]any, 1)
	go func() {
		var command map[string]any
		_ = json.NewDecoder(stdinReader).Decode(&command)
		commandRead <- command
	}()
	_, err := client.GetState(context.Background())
	var timeout *RequestTimeoutError
	if !errors.As(err, &timeout) || timeout.Command != "get_state" {
		t.Fatalf("timeout = %v", err)
	}
	command := <-commandRead
	writeRecord(t, stdoutWriter, map[string]any{"id": command["id"], "type": "response", "command": "get_state", "success": true})
	writeRecord(t, stdoutWriter, map[string]any{"type": "turn_start"})
	waitSequence(t, client, 1)
	batch := client.EventsAfter(0)
	if len(batch.Events) != 1 || batch.Events[0]["type"] != "turn_start" {
		t.Fatalf("late response became event: %#v", batch)
	}
}

func TestClientReconcilesAcceptedCommandAfterCallerCancellation(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{RequestTimeout: time.Second})
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		response, err := client.GetState(ctx)
		if err == nil && response["success"] != true {
			err = errors.New("unsuccessful response")
		}
		result <- err
	}()
	var command map[string]any
	if err := json.NewDecoder(stdinReader).Decode(&command); err != nil {
		t.Fatal(err)
	}
	cancel()
	writeRecord(t, stdoutWriter, map[string]any{"id": command["id"], "type": "response", "command": "get_state", "success": true})
	if err := <-result; err != nil {
		t.Fatalf("accepted command was abandoned: %v", err)
	}
}

func TestClientTimesOutBlockedPipeWritesWithoutLeakingRequest(t *testing.T) {
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{RequestTimeout: 20 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	started := time.Now()
	_, err = client.Prompt(context.Background(), strings.Repeat("blocked", 1<<20), nil)
	var timeout *RequestTimeoutError
	if !errors.As(err, &timeout) || time.Since(started) > time.Second {
		t.Fatalf("blocked write = %v after %s", err, time.Since(started))
	}
}

func TestClientSnapshotsAndAnswersExtensionUIState(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{})
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	writeRecord(t, stdoutWriter, map[string]any{"type": "queue_update", "steering": []any{"adjust"}, "followUp": []any{"next"}})
	writeRecord(t, stdoutWriter, map[string]any{"type": "extension_ui_request", "id": "confirm-1", "method": "confirm", "title": "Approve?", "timeout": 10000})
	writeRecord(t, stdoutWriter, map[string]any{"type": "extension_ui_request", "method": "setStatus", "statusKey": "release", "statusText": "waiting"})
	writeRecord(t, stdoutWriter, map[string]any{"type": "extension_ui_request", "method": "setWidget", "widgetKey": "release", "widgetLines": []any{"one"}})
	writeRecord(t, stdoutWriter, map[string]any{"type": "extension_ui_request", "method": "setTitle", "title": "Release"})
	waitSequence(t, client, 5)
	snapshot := client.LiveSnapshot()
	if snapshot.QueuedMessages["steering"][0] != "adjust" || snapshot.ExtensionUI == nil {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	pending, _ := snapshot.ExtensionUI["pending_dialogs"].([]map[string]any)
	if len(pending) != 1 || pending[0]["id"] != "confirm-1" {
		t.Fatalf("pending dialogs = %#v", snapshot.ExtensionUI["pending_dialogs"])
	}
	commandRead := make(chan map[string]any, 1)
	go func() {
		decoder := json.NewDecoder(stdinReader)
		var command map[string]any
		_ = decoder.Decode(&command)
		commandRead <- command
	}()
	confirmed := true
	response, err := client.ExtensionUIResponse(context.Background(), "confirm-1", nil, &confirmed, false)
	if err != nil || response["success"] != true {
		t.Fatalf("extension response = %#v, %v", response, err)
	}
	command := <-commandRead
	if command["type"] != "extension_ui_response" || command["id"] != "confirm-1" || command["confirmed"] != true {
		t.Fatalf("command = %#v", command)
	}
	if dialogs := client.LiveSnapshot().ExtensionUI["pending_dialogs"].([]map[string]any); len(dialogs) != 0 {
		t.Fatalf("answered dialog remained: %#v", dialogs)
	}
}

func TestScrubbedEnvironmentRemovesGatewayPasswordAndPreservesRuntimeVariables(t *testing.T) {
	environment := ScrubbedEnvironment([]string{"PATH=/bin", "GRIPI_ADMIN_PASSWORD=secret", "TOOL_RUNTIME_PATH=kept", "GRIPI_E2E_SESSIONS_ROOT=kept"})
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "GRIPI_ADMIN_PASSWORD") {
		t.Fatalf("environment retained gateway password: %q", joined)
	}
	for _, expected := range []string{"TOOL_RUNTIME_PATH=kept", "GRIPI_E2E_SESSIONS_ROOT=kept"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("environment = %q; missing %q", joined, expected)
		}
	}
}

func writeNativeToolUpdate(t *testing.T, writer io.Writer, id, toolName string, partialResult map[string]any) {
	t.Helper()
	partial, err := json.Marshal(partialResult)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"type":"tool_execution_update","toolCallId":` + quoted(id) + `,"toolName":` + quoted(toolName) + `,"partialResult":` + string(partial) + "}\n"
	if _, err := io.WriteString(writer, line); err != nil {
		t.Fatal(err)
	}
}

func writeRecord(t *testing.T, writer io.Writer, value map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if _, err = writer.Write(encoded); err != nil {
		t.Fatal(err)
	}
}
func waitSequence(t *testing.T, client *Client, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if client.EventSequence() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("event sequence = %d, want %d", client.EventSequence(), want)
}
func containsEvent(events []map[string]any, want string) bool { return countEvent(events, want) > 0 }
func countEvent(events []map[string]any, want string) int {
	count := 0
	for _, event := range events {
		if event["type"] == want {
			count++
		}
	}
	return count
}
func eventTypes(events []map[string]any) []any {
	result := make([]any, 0, len(events))
	for _, event := range events {
		result = append(result, event["type"])
	}
	return result
}
func quoted(value string) string { encoded, _ := json.Marshal(value); return string(encoded) }
