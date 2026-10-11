package pi

import (
	"encoding/json"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/agent/pi/rpc"
)

func (client *Client) LiveSnapshot() agent.LiveSnapshot {
	return LiveSnapshotFrom(client.Client.LiveSnapshot())
}

// LiveSnapshotFrom reads what the gateway compares in the live state of a Pi RPC client, and passes the rest to the
// browser as it is.
func LiveSnapshotFrom(snapshot rpc.LiveSnapshot) agent.LiveSnapshot {
	result := agent.LiveSnapshot{
		EventSequence: snapshot.EventSequence, EventReplayCursor: snapshot.EventReplayCursor,
		Busy: snapshot.Busy, BusySince: snapshot.BusySince, AgentBusySince: snapshot.AgentBusySince, AgentRunning: snapshot.AgentRunning,
		Compacting: snapshot.Compacting, CompactingSince: snapshot.CompactingSince,
		Queue: queueFrom(snapshot.QueuedMessages),
	}
	for _, event := range snapshot.ActiveToolEvents {
		result.ActiveTools = append(result.ActiveTools, agent.ActiveTool{ID: stringValue(event["toolCallId"]), Data: encoded(event)})
	}
	if snapshot.ActiveAssistantEvent != nil {
		result.ActiveAssistant = encoded(snapshot.ActiveAssistantEvent)
	}
	if active := snapshot.ActiveBash; active != nil {
		started, _ := active["started_at"].(time.Time)
		result.ActiveShell = &agent.Shell{ID: stringValue(active["bash_id"]), Command: stringValue(active["command"]), ExcludeFromContext: active["exclude_from_context"] == true, StartedAt: started, Output: stringValue(active["output"])}
	}
	for _, event := range snapshot.CompletedBashEvents {
		result.CompletedShells = append(result.CompletedShells, completedShell(event))
	}
	if snapshot.ExtensionUI != nil {
		dialogs, _ := snapshot.ExtensionUI["pending_dialogs"].([]map[string]any)
		result.PendingRequests = len(dialogs)
		result.ExtensionUI = encoded(snapshot.ExtensionUI)
	}
	return result
}

// completedShell reads the end of a shell command, which carries Pi's result when the command ran.
func completedShell(event map[string]any) agent.CompletedShell {
	shell := agent.CompletedShell{
		Shell: agent.Shell{ID: stringValue(event["bashId"]), Command: stringValue(event["command"]), ExcludeFromContext: event["excludeFromContext"] == true},
		Data:  encoded(event),
	}
	started, startKnown := number(event["startedAt"])
	if startKnown {
		shell.StartedAt = time.UnixMilli(int64(started))
	}
	result, ended := event["result"].(map[string]any)
	if event["type"] != "bash_end" || !ended || !startKnown {
		return shell
	}
	shell.Ended = true
	shell.Output = stringValue(result["output"])
	shell.Cancelled, shell.Truncated = result["cancelled"] == true, result["truncated"] == true
	if exitCode, ok := number(result["exitCode"]); ok {
		code := int(exitCode)
		shell.ExitCode = &code
	}
	return shell
}

func queueFrom(queued map[string][]string) agent.Queue {
	return agent.Queue{Steering: queued["steering"], FollowUp: queued["followUp"]}
}

func (client *Client) QueuedMessagesForStop() (agent.Queue, bool) {
	queued, ok := client.Client.QueuedMessagesForStop()
	return queueFrom(queued), ok
}

// EventsAfter passes Pi's events to the browser as they are.
func (client *Client) EventsAfter(after int64) agent.EventBatch {
	batch := client.Client.EventsAfter(after)
	events := make([]json.RawMessage, len(batch.Events))
	for index, event := range batch.Events {
		events[index] = encoded(event)
	}
	return agent.EventBatch{Events: events, LastSeq: batch.LastSeq, Missed: batch.Missed}
}
