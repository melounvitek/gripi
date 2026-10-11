package agent

import (
	"context"
	"encoding/json"
)

// CompactionPromptDeferrer is a client that holds prompts back during a compaction and delivers them after it.
type CompactionPromptDeferrer interface {
	DeferringCompactionPrompts() bool
	// DeferPrompt takes a steered or follow-up prompt while the operation lane may be busy with the compaction, and
	// reports false when the session doesn't hold prompts back, so that the prompt goes to Prompt.
	DeferPrompt(context.Context, Prompt) (PromptResult, bool, error)
}

// QueueRestorer is a client that hands back its queued messages when a stop retires it.
type QueueRestorer interface {
	QueuedMessagesForStop() (map[string][]string, bool)
}

type QueueClearer interface {
	ClearQueue(context.Context) (map[string]any, error)
}

// InterruptStater is a client that can answer State within the shorter wait of a stop.
type InterruptStater interface {
	InterruptState(context.Context) (State, error)
}

// Forker is a client that can fork the session from one of its user messages.
type Forker interface {
	// ForkMessages lists the messages a fork can start from, in the agent's format for the browser.
	ForkMessages(context.Context) (json.RawMessage, error)
	Fork(ctx context.Context, entryID string) (Branch, error)
}

type Cloner interface {
	Clone(context.Context) (Branch, error)
}

type Compacter interface {
	Compact(ctx context.Context, instructions string) error
}

type Renamer interface {
	Rename(ctx context.Context, name string) error
}

// Reloader is a client whose agent can reload its extensions, skills, prompts and context files.
type Reloader interface {
	Reload(context.Context) error
}
