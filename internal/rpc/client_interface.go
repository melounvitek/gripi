package rpc

import (
	"context"
	"fmt"

	"github.com/melounvitek/gripi/internal/agent"
)

// Callers find the optional interfaces by type assertion and fall back without them, so check them at compile time.
var (
	_ agent.Client                   = (*Client)(nil)
	_ agent.CompactionPromptDeferrer = (*Client)(nil)
	_ agent.QueueRestorer            = (*Client)(nil)
	_ agent.QueueClearer             = (*Client)(nil)
	_ SessionEntriesReader           = (*Client)(nil)
)

// SessionEntriesReader reads Pi's session entries, which only the session synchronizer compares with the file.
type SessionEntriesReader interface {
	SessionPosition(context.Context, string) (SessionEntries, error)
	SessionEntriesAfter(context.Context, string) (SessionEntries, error)
}

// SessionPosition and SessionEntriesAfter read the entries through a client from the registry, which has the core type.
func SessionPosition(ctx context.Context, client RPCClient, cursor string) (SessionEntries, error) {
	reader, err := sessionEntriesReader(client)
	if err != nil {
		return SessionEntries{}, err
	}
	return reader.SessionPosition(ctx, cursor)
}

func SessionEntriesAfter(ctx context.Context, client RPCClient, cursor string) (SessionEntries, error) {
	reader, err := sessionEntriesReader(client)
	if err != nil {
		return SessionEntries{}, err
	}
	return reader.SessionEntriesAfter(ctx, cursor)
}

func sessionEntriesReader(client RPCClient) (SessionEntriesReader, error) {
	reader, ok := client.(SessionEntriesReader)
	if !ok {
		return nil, fmt.Errorf("%T cannot read Pi session entries", client)
	}
	return reader, nil
}

type ActionClient interface {
	GetAvailableModels(context.Context) (map[string]any, error)
	ScopedModels(context.Context) (map[string]any, error)
	GetForkMessages(context.Context) (map[string]any, error)
	GetStateForInterrupt(context.Context) (map[string]any, error)
	SetModel(context.Context, string, string) (map[string]any, error)
	SetThinkingLevel(context.Context, string) (map[string]any, error)
	CycleThinkingLevel(context.Context) (map[string]any, error)
	Prompt(context.Context, string, []PromptImage) (map[string]any, error)
	PromptWithBehavior(context.Context, string, []PromptImage, string) (map[string]any, error)
	Steer(context.Context, string, []PromptImage) (map[string]any, error)
	FollowUp(context.Context, string, []PromptImage) (map[string]any, error)
	QueueCompactionFollowUp(context.Context, string, []PromptImage) (map[string]any, bool, error)
	QueueCompactionPrompt(context.Context, string, []PromptImage, string) (map[string]any, bool, error)
	Abort(context.Context) (map[string]any, error)
	AbortBash(context.Context) (map[string]any, error)
	ActiveBashCommand() string
	NewSession(context.Context, string) (map[string]any, error)
	Compact(context.Context, string) (map[string]any, error)
	Fork(context.Context, string) (map[string]any, error)
	CloneSession(context.Context) (map[string]any, error)
	ExportHTML(context.Context, string) (map[string]any, error)
	SetSessionName(context.Context, string) (map[string]any, error)
	Reload(context.Context) (map[string]any, error)
	Bash(context.Context, string, bool) (map[string]any, error)
	ExtensionUIResponse(context.Context, string, *string, *bool, bool) (map[string]any, error)
	TreeSnapshot(context.Context, string) (map[string]any, error)
	NavigateTree(context.Context, string, string, string) (map[string]any, error)
	SetTreeLabel(context.Context, string, string) (map[string]any, error)
}
