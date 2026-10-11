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
