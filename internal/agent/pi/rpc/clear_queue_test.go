package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

type clearQueueResult struct {
	response map[string]any
	err      error
}

func startClearQueue(t *testing.T, client *Client, ctx context.Context) <-chan clearQueueResult {
	t.Helper()
	result := make(chan clearQueueResult, 1)
	go func() {
		response, err := client.ClearQueue(ctx)
		result <- clearQueueResult{response, err}
	}()
	return result
}

func queueTestClient(t *testing.T, timeout time.Duration) (*Client, io.Writer, <-chan map[string]any) {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewClient(stdinWriter, stdoutReader, nil, ClientOptions{RequestTimeout: timeout})
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	commands := make(chan map[string]any, 16)
	go func() {
		defer close(commands)
		decoder := json.NewDecoder(stdinReader)
		for {
			var command map[string]any
			if decoder.Decode(&command) != nil {
				return
			}
			commands <- command
		}
	}()
	return client, stdoutWriter, commands
}

func nextQueueCommand(t *testing.T, commands <-chan map[string]any, kind string) map[string]any {
	t.Helper()
	select {
	case command := <-commands:
		if command["type"] != kind {
			t.Fatalf("command = %#v, want %s", command, kind)
		}
		return command
	case <-time.After(time.Second):
		t.Fatalf("no %s command", kind)
		return nil
	}
}

func awaitClearQueue(t *testing.T, result <-chan clearQueueResult) clearQueueResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(time.Second):
		t.Fatal("clear_queue did not finish")
		return clearQueueResult{}
	}
}

func queueDeferred(t *testing.T, client *Client, message, behavior string) {
	t.Helper()
	response, queued, err := client.QueueCompactionPrompt(context.Background(), message, nil, behavior)
	if err != nil || !queued || response["success"] != true {
		t.Fatalf("queue %q = %#v, %v, %v", message, response, queued, err)
	}
}

func TestClearQueueUsesNativeCommandWithoutAbortingCompaction(t *testing.T) {
	client, stdout, commands := queueTestClient(t, time.Second)
	writeRecord(t, stdout, map[string]any{"type": "agent_start"})
	writeRecord(t, stdout, map[string]any{"type": "compaction_start"})
	waitSequence(t, client, 2)
	queueDeferred(t, client, "old steer", "steer")
	queueDeferred(t, client, "old follow-up", "followUp")
	cursor := client.EventSequence()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := startClearQueue(t, client, ctx)
	command := nextQueueCommand(t, commands, "clear_queue")
	if len(command) != 2 || command["id"] == "" {
		t.Fatalf("native clear command = %#v", command)
	}
	if snapshot := client.LiveSnapshot(); len(snapshot.QueuedMessages) != 0 || !snapshot.Compacting || !snapshot.AgentRunning {
		t.Fatalf("snapshot during clear = %#v", snapshot)
	}
	event := eventOfType(t, client.EventsAfter(cursor).Events, "queue_update")
	if !reflect.DeepEqual(event["steering"], []string{}) || !reflect.DeepEqual(event["followUp"], []string{}) {
		t.Fatalf("clear queue event = %#v", event)
	}
	queueDeferred(t, client, "new follow-up", "followUp")
	cancel() // An accepted request must still reconcile its native response.
	response := map[string]any{"type": "response", "id": command["id"], "command": "clear_queue", "success": true}
	writeRecord(t, stdout, response)
	if got := awaitClearQueue(t, result); got.err != nil || !reflect.DeepEqual(got.response, response) {
		t.Fatalf("clear result = %#v, %v", got.response, got.err)
	}
	writeRecord(t, stdout, map[string]any{"type": "compaction_end"})
	next := nextQueueCommand(t, commands, "prompt")
	if next["message"] != "new follow-up" {
		t.Fatalf("cleared message was sent: %#v", next)
	}
	writeRecord(t, stdout, map[string]any{"type": "response", "id": next["id"], "success": true})
}

func TestClearQueueSerializesWithInFlightCompactionHandoff(t *testing.T) {
	for _, outcome := range []string{"success", "rejected", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			client, stdout, commands := queueTestClient(t, 200*time.Millisecond)
			writeRecord(t, stdout, map[string]any{"type": "compaction_start"})
			waitSequence(t, client, 1)
			queueDeferred(t, client, "in flight", "steer")
			queueDeferred(t, client, "remaining", "followUp")
			writeRecord(t, stdout, map[string]any{"type": "compaction_end"})
			first := nextQueueCommand(t, commands, "prompt")
			// Start clear after part of the flush budget has elapsed, so even a
			// timed-out handoff leaves time for the native clear request.
			time.Sleep(20 * time.Millisecond)
			result := startClearQueue(t, client, context.Background())
			select {
			case command := <-commands:
				t.Fatalf("clear overtook unresolved handoff: %#v", command)
			case <-time.After(20 * time.Millisecond):
			}
			queueDeferred(t, client, "also remaining", "steer")
			if got := client.LiveSnapshot().QueuedMessages; !reflect.DeepEqual(got, map[string][]string{"steering": {"also remaining"}, "followUp": {"remaining"}}) {
				t.Fatalf("remaining queue is not authoritative: %#v", got)
			}
			if outcome != "timeout" {
				writeRecord(t, stdout, map[string]any{"type": "response", "id": first["id"], "success": outcome == "success"})
			}
			clear := nextQueueCommand(t, commands, "clear_queue")
			writeRecord(t, stdout, map[string]any{"type": "response", "id": clear["id"], "success": true})
			if got := awaitClearQueue(t, result); got.err != nil || got.response["success"] != true {
				t.Fatalf("clear result = %#v, %v", got.response, got.err)
			}
			if outcome == "timeout" {
				writeRecord(t, stdout, map[string]any{"type": "response", "id": first["id"], "success": false})
			}
			promptDone := make(chan error, 1)
			go func() { _, err := client.Prompt(context.Background(), "after clear", nil); promptDone <- err }()
			next := nextQueueCommand(t, commands, "prompt")
			if next["message"] != "after clear" {
				t.Fatalf("cleared message resurrected: %#v", next)
			}
			writeRecord(t, stdout, map[string]any{"type": "response", "id": next["id"], "success": true})
			if err := <-promptDone; err != nil {
				t.Fatal(err)
			}
			client.mu.Lock()
			count, bytes := client.compactionFollowUpCount, client.compactionFollowUpBytes
			client.mu.Unlock()
			if count != 0 || bytes != 0 {
				t.Fatalf("queue accounting after clear = %d, %d", count, bytes)
			}
		})
	}
}

func TestClearQueueRefreshesExpiredCompactionFlush(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		willRetry    bool
		timeout      bool
		startup      bool
		clearOutcome string
	}{
		{name: "completed"},
		{name: "auto_retry", willRetry: true},
		{name: "completed_timeout", timeout: true},
		{name: "auto_retry_timeout", willRetry: true, timeout: true},
		{name: "completed_startup", startup: true},
		{name: "auto_retry_startup", willRetry: true, startup: true},
		{name: "clear_rejected", willRetry: true, clearOutcome: "rejected"},
		{name: "clear_timeout", willRetry: true, clearOutcome: "timeout"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				willRetry := scenario.willRetry
				client, stdout, commands := queueTestClient(t, 10*time.Second)
				workerReady, startWorker := make(chan struct{}), make(chan struct{})
				if scenario.startup {
					client.eventObserver = func(_ *Client, event map[string]any) {
						if event["type"] == "compaction_end" {
							close(workerReady)
							<-startWorker
						}
					}
				}
				writeRecord(t, stdout, map[string]any{"type": "compaction_start"})
				synctest.Wait()
				queueDeferred(t, client, "in flight", "steer")
				queueDeferred(t, client, "discard me", "followUp")
				writeRecord(t, stdout, map[string]any{"type": "compaction_end", "willRetry": willRetry})
				firstKind := "prompt"
				if willRetry {
					firstKind = "steer"
				}
				var first map[string]any
				if scenario.startup {
					<-workerReady // The active flag is published, but the worker has not started.
				} else {
					first = nextQueueCommand(t, commands, firstKind)
				}
				synctest.Wait()
				time.Sleep(time.Second)
				result := startClearQueue(t, client, context.Background())
				synctest.Wait() // Clear is waiting behind any in-flight handoff.
				if first != nil {
					writeRecord(t, stdout, map[string]any{"type": "response", "id": first["id"], "success": true})
				}
				clear := nextQueueCommand(t, commands, "clear_queue")
				queueDeferred(t, client, "new steer", "steer")
				queueDeferred(t, client, "new follow-up", "followUp")
				if scenario.startup {
					close(startWorker)
				}
				synctest.Wait()
				time.Sleep(9 * time.Second) // The original flush deadline, but not clear's.
				synctest.Wait()

				cursor := client.EventSequence()
				writeRecord(t, stdout, map[string]any{"type": "queue_update", "steering": []any{}, "followUp": []any{}})
				synctest.Wait()
				if client.EventSequence() != cursor+1 {
					t.Fatal("stdout stalled behind clear")
				}
				if got := client.LiveSnapshot().QueuedMessages; !reflect.DeepEqual(got, map[string][]string{"steering": {"new steer"}, "followUp": {"new follow-up"}}) {
					t.Fatalf("queue during clear = %#v", got)
				}
				if scenario.clearOutcome == "timeout" {
					time.Sleep(time.Second)
					synctest.Wait()
					var timeout *RequestTimeoutError
					if got := awaitClearQueue(t, result); !errors.As(got.err, &timeout) || !timeout.Accepted {
						t.Fatalf("clear timeout = %#v, %v", got.response, got.err)
					}
					writeRecord(t, stdout, map[string]any{"type": "response", "id": clear["id"], "success": false})
				} else {
					success := scenario.clearOutcome != "rejected"
					writeRecord(t, stdout, map[string]any{"type": "response", "id": clear["id"], "success": success})
					if got := awaitClearQueue(t, result); got.err != nil || got.response["success"] != success {
						t.Fatalf("clear result = %#v, %v", got.response, got.err)
					}
				}
				for _, expected := range []struct{ message, behavior, retryCommand string }{
					{"new steer", "steer", "steer"},
					{"new follow-up", "followUp", "follow_up"},
				} {
					kind := "prompt"
					var behavior any = expected.behavior
					if willRetry {
						kind, behavior = expected.retryCommand, nil
					}
					command := nextQueueCommand(t, commands, kind)
					if command["message"] != expected.message || command["streamingBehavior"] != behavior {
						t.Fatalf("handoff = %#v, want %q (%v)", command, expected.message, behavior)
					}
					if scenario.startup && expected.behavior == "steer" {
						// Starting the worker must not overwrite clear's extension.
						time.Sleep(2 * time.Second)
					}
					if scenario.timeout && expected.behavior == "followUp" {
						synctest.Wait()
						time.Sleep(11 * time.Second) // Clear's deadline plus one fresh flush budget.
						synctest.Wait()
						if client.DeferringCompactionPrompts() {
							t.Fatal("refreshed flush exceeded its bounded budget")
						}
						// A late rejection must not resurrect an accepted, timed-out handoff.
						writeRecord(t, stdout, map[string]any{"type": "response", "id": command["id"], "success": false})
					} else {
						writeRecord(t, stdout, map[string]any{"type": "response", "id": command["id"], "success": true})
					}
				}
				synctest.Wait()
				select {
				case command := <-commands:
					t.Fatalf("unexpected extra handoff: %#v", command)
				default:
				}
				client.mu.Lock()
				count, bytes, flushing := client.compactionFollowUpCount, client.compactionFollowUpBytes, client.flushingCompactionFollowUps
				client.mu.Unlock()
				if count != 0 || bytes != 0 || flushing || len(client.LiveSnapshot().QueuedMessages) != 0 {
					t.Fatalf("queue after delivery: count=%d, bytes=%d, flushing=%v", count, bytes, flushing)
				}
			})
		})
	}
}

func TestClearQueuePreservesNativeQueueEventsAndResponseData(t *testing.T) {
	client, stdout, commands := queueTestClient(t, time.Second)
	writeRecord(t, stdout, map[string]any{"type": "agent_start"})
	writeRecord(t, stdout, map[string]any{"type": "queue_update", "steering": []any{"native steer"}, "followUp": []any{"native follow-up"}})
	waitSequence(t, client, 2)
	result := startClearQueue(t, client, context.Background())
	command := nextQueueCommand(t, commands, "clear_queue")
	writeRecord(t, stdout, map[string]any{"type": "queue_update", "steering": []any{}, "followUp": []any{}})
	response := map[string]any{"type": "response", "id": command["id"], "command": "clear_queue", "success": true,
		"data": map[string]any{"steering": []any{"native steer"}, "followUp": []any{"native follow-up"}}}
	writeRecord(t, stdout, response)
	if got := awaitClearQueue(t, result); got.err != nil || !reflect.DeepEqual(got.response, response) {
		t.Fatalf("native result = %#v, %v", got.response, got.err)
	}
	if snapshot := client.LiveSnapshot(); len(snapshot.QueuedMessages) != 0 || !snapshot.AgentRunning || !snapshot.Busy {
		t.Fatalf("snapshot after native clear = %#v", snapshot)
	}
}

func TestClearQueueWaitIsCancellableAndBounded(t *testing.T) {
	for _, outcome := range []string{"cancelled", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			client, stdout, commands := queueTestClient(t, 30*time.Millisecond)
			writeRecord(t, stdout, map[string]any{"type": "compaction_start"})
			waitSequence(t, client, 1)
			queueDeferred(t, client, "not cleared", "followUp")
			// Hold the queue gate longer than the request budget, independently
			// of the timeout of any particular RPC handoff.
			<-client.queueLane
			ctx := context.Background()
			if outcome == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 5*time.Millisecond)
				defer cancel()
			}
			got := awaitClearQueue(t, startClearQueue(t, client, ctx))
			client.queueLane <- struct{}{}
			if outcome == "cancelled" {
				if !errors.Is(got.err, context.DeadlineExceeded) {
					t.Fatalf("cancelled wait = %v", got.err)
				}
			} else {
				var timeout *RequestTimeoutError
				if !errors.As(got.err, &timeout) || timeout.Command != "clear_queue" || timeout.Accepted {
					t.Fatalf("queue gate timeout = %v", got.err)
				}
			}
			if queued := client.LiveSnapshot().QueuedMessages["followUp"]; !reflect.DeepEqual(queued, []string{"not cleared"}) {
				t.Fatalf("unaccepted clear changed queue: %#v", queued)
			}
			result := startClearQueue(t, client, context.Background())
			command := nextQueueCommand(t, commands, "clear_queue")
			writeRecord(t, stdout, map[string]any{"type": "response", "id": command["id"], "success": true})
			if got := awaitClearQueue(t, result); got.err != nil || got.response["success"] != true {
				t.Fatalf("clear after gate wait = %#v, %v", got.response, got.err)
			}
		})
	}
}

func TestClearQueuePreservesNativeFailureAndTimeout(t *testing.T) {
	for _, outcome := range []string{"rejected", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			client, stdout, commands := queueTestClient(t, 50*time.Millisecond)
			writeRecord(t, stdout, map[string]any{"type": "compaction_start"})
			waitSequence(t, client, 1)
			queueDeferred(t, client, "discard locally", "followUp")
			result := startClearQueue(t, client, context.Background())
			command := nextQueueCommand(t, commands, "clear_queue")
			failure := map[string]any{"type": "response", "id": command["id"], "success": false, "error": "native failure"}
			if outcome == "rejected" {
				writeRecord(t, stdout, failure)
			}
			got := awaitClearQueue(t, result)
			if outcome == "rejected" {
				if got.err != nil || !reflect.DeepEqual(got.response, failure) {
					t.Fatalf("failure = %#v, %v", got.response, got.err)
				}
			} else {
				var timeout *RequestTimeoutError
				if !errors.As(got.err, &timeout) || timeout.Command != "clear_queue" || !timeout.Accepted {
					t.Fatalf("timeout = %v", got.err)
				}
				writeRecord(t, stdout, failure)
			}
			if queued := client.LiveSnapshot().QueuedMessages; len(queued) != 0 {
				t.Fatalf("local messages resurrected: %#v", queued)
			}
			result = startClearQueue(t, client, context.Background())
			command = nextQueueCommand(t, commands, "clear_queue")
			writeRecord(t, stdout, map[string]any{"type": "response", "id": command["id"], "success": true})
			if got := awaitClearQueue(t, result); got.err != nil || got.response["success"] != true {
				t.Fatalf("subsequent clear = %#v, %v", got.response, got.err)
			}
		})
	}
}
