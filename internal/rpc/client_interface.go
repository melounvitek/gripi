package rpc

import (
	"context"
	"fmt"
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

// ActionClient is what the gateway still asks of Pi through raw requests.
type ActionClient interface {
	GetState(context.Context) (map[string]any, error)
	GetAvailableModels(context.Context) (map[string]any, error)
	ScopedModels(context.Context) (map[string]any, error)
	SetModel(context.Context, string, string) (map[string]any, error)
	SetThinkingLevel(context.Context, string) (map[string]any, error)
	CycleThinkingLevel(context.Context) (map[string]any, error)
	AbortBash(context.Context) (map[string]any, error)
	ActiveBashCommand() string
	ExportHTML(context.Context, string) (map[string]any, error)
	Bash(context.Context, string, bool) (map[string]any, error)
	ExtensionUIResponse(context.Context, string, *string, *bool, bool) (map[string]any, error)
	TreeSnapshot(context.Context, string) (map[string]any, error)
	NavigateTree(context.Context, string, string, string) (map[string]any, error)
	SetTreeLabel(context.Context, string, string) (map[string]any, error)
}
