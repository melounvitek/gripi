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

// QueueClearer is a client that can drop the steered and follow-up prompts waiting for the run.
type QueueClearer interface {
	ClearQueue(context.Context) error
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

// Exporter is a client that can save the session as an HTML page at path.
type Exporter interface {
	ExportHTML(ctx context.Context, path string) error
}

// ShellRunner is a client that runs the user's shell commands in the session, like Pi's `!` and `!!`.
type ShellRunner interface {
	// RunShell waits for the command to end. A command that ran but failed is a result, not an error.
	RunShell(ctx context.Context, command string, excludeFromContext bool) (ShellRun, error)
	// ShellRunning reports whether a command runs, which CancelShell stops rather than Cancel.
	ShellRunning() bool
	CancelShell(context.Context) error
}

type ShellRun struct {
	ID     string
	Failed bool
	Error  string
	// Data is the agent's answer, in its format for the browser.
	Data json.RawMessage
}

// ShellError is a shell command that started, but whose end the agent could not report. The user sees the run
// fail, and the gateway learns why, such as from an agent that exited.
type ShellError struct {
	Run ShellRun
	Err error
}

func (err *ShellError) Error() string { return err.Err.Error() }
func (err *ShellError) Unwrap() error { return err.Err }

// TreeNavigator is a client that can move around the tree of the session's branches.
type TreeNavigator interface {
	// Tree returns the session tree with the filter applied, in the agent's format for the browser. It fails with
	// ErrInvalidResponse when the agent answers something else.
	Tree(ctx context.Context, filter string) (json.RawMessage, error)
	// NavigateTree moves the session to the entry, summarizing the branch it leaves unless summary is "none".
	NavigateTree(ctx context.Context, entryID, summary, instructions string) (Navigation, error)
	// LabelTreeEntry labels an entry, or removes its label when label is empty.
	LabelTreeEntry(ctx context.Context, entryID, label string) error
}

type Navigation struct {
	Cancelled bool
	// EditorText is the text the agent puts back in the editor, if any.
	EditorText *string
}

// ModelSelector is a client whose agent lets the user choose its model and thinking level.
type ModelSelector interface {
	// ModelSettings fails with ErrInvalidResponse when the agent answers something else.
	ModelSettings(context.Context) (ModelSettings, error)
	// SetModel returns the model and thinking level that the agent confirms. It fails with ErrInvalidResponse when
	// the agent doesn't confirm them.
	SetModel(ctx context.Context, provider, modelID, thinkingLevel string) (ModelChoice, error)
	// CycleThinkingLevel returns the next thinking level, or "" when the model has none. It fails with
	// ErrInvalidResponse when the agent reports a level it doesn't accept.
	CycleThinkingLevel(context.Context) (string, error)
}

// ModelSettings is the session's state, the models it can use and those the user scoped the picker to, in the
// agent's format for the browser.
type ModelSettings struct {
	State        json.RawMessage
	Models       json.RawMessage
	ScopedModels json.RawMessage
}

type ModelChoice struct {
	// Model is in the agent's format for the browser.
	Model         json.RawMessage
	ThinkingLevel string
}

// Responder is a client whose agent asks the user questions, such as Pi's extension dialogs.
type Responder interface {
	Answer(ctx context.Context, id string, answer Answer) error
}

type Answer struct {
	Cancelled bool
	Confirmed *bool
	Value     *string
}
