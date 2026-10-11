package rpc

import "context"

// SessionEntriesReader reads Pi's session entries, which only the session synchronizer compares with the file.
type SessionEntriesReader interface {
	SessionPosition(context.Context, string) (SessionEntries, error)
	SessionEntriesAfter(context.Context, string) (SessionEntries, error)
}
