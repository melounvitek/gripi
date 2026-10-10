package agent

import (
	"context"
	"time"
)

type Client interface {
	Close() error
	Busy() bool
	BusySince() *time.Time
	SettledAt() *time.Time
	AgentRunning() bool
	Compacting() bool
	EventSequence() int64
	EventReplayCursor() int64
	EventsAfter(int64) EventBatch
	LiveSnapshot() LiveSnapshot
	GetState(context.Context) (map[string]any, error)
	GetSessionStats(context.Context) (map[string]any, error)
	GetCommands(context.Context) (map[string]any, error)
}

// CompactionPromptDeferrer is a client that holds prompts back during a compaction and delivers them after it.
type CompactionPromptDeferrer interface {
	DeferringCompactionPrompts() bool
}

// QueueRestorer is a client that hands back its queued messages when a stop retires it.
type QueueRestorer interface {
	QueuedMessagesForStop() (map[string][]string, bool)
}

type QueueClearer interface {
	ClearQueue(context.Context) (map[string]any, error)
}

type PromptImage struct {
	Path     string
	MIMEType string
	Size     int64
}

type EventBatch struct {
	Events  []map[string]any `json:"events"`
	LastSeq int64            `json:"last_seq"`
	Missed  bool             `json:"missed"`
}

type LiveSnapshot struct {
	EventSequence        int64               `json:"event_sequence"`
	EventReplayCursor    int64               `json:"event_replay_cursor"`
	ActiveToolEvents     []map[string]any    `json:"active_tool_events"`
	ActiveAssistantEvent map[string]any      `json:"active_assistant_event,omitempty"`
	Busy                 bool                `json:"busy,omitempty"`
	BusySince            *time.Time          `json:"busy_since,omitempty"`
	AgentBusySince       *time.Time          `json:"agent_busy_since,omitempty"`
	AgentRunning         bool                `json:"agent_running,omitempty"`
	ActiveBash           map[string]any      `json:"active_bash,omitempty"`
	CompletedBashEvents  []map[string]any    `json:"completed_bash_events,omitempty"`
	Compacting           bool                `json:"compacting,omitempty"`
	CompactingSince      *time.Time          `json:"compacting_since,omitempty"`
	QueuedMessages       map[string][]string `json:"queued_messages,omitempty"`
	ExtensionUI          map[string]any      `json:"extension_ui,omitempty"`
}
