package rpc

import (
	"context"
	"time"
)

type RPCClient interface {
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
	SessionPosition(context.Context, string) (SessionEntries, error)
	SessionEntriesAfter(context.Context, string) (SessionEntries, error)
}

type PromptImage struct {
	Path     string
	MIMEType string
	Size     int64
}

type ActionClient interface {
	GetAvailableModels(context.Context) (map[string]any, error)
	GetForkMessages(context.Context) (map[string]any, error)
	GetStateForInterrupt(context.Context) (map[string]any, error)
	SetModel(context.Context, string, string) (map[string]any, error)
	SetThinkingLevel(context.Context, string) (map[string]any, error)
	CycleThinkingLevel(context.Context) (map[string]any, error)
	Prompt(context.Context, string, []PromptImage) (map[string]any, error)
	Steer(context.Context, string, []PromptImage) (map[string]any, error)
	FollowUp(context.Context, string, []PromptImage) (map[string]any, error)
	QueueCompactionFollowUp(context.Context, string, []PromptImage) (map[string]any, bool, error)
	Abort(context.Context) (map[string]any, error)
	AbortBash(context.Context) (map[string]any, error)
	ActiveBashCommand() string
	Compact(context.Context, string) (map[string]any, error)
	Fork(context.Context, string) (map[string]any, error)
	CloneSession(context.Context) (map[string]any, error)
	SetSessionName(context.Context, string) (map[string]any, error)
	Bash(context.Context, string, bool) (map[string]any, error)
	ExtensionUIResponse(context.Context, string, *string, *bool, bool) (map[string]any, error)
	TreeSnapshot(context.Context, string) (map[string]any, error)
	NavigateTree(context.Context, string, string, string) (map[string]any, error)
	SetTreeLabel(context.Context, string, string) (map[string]any, error)
}
