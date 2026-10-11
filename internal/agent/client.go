package agent

import (
	"context"
	"encoding/json"
	"time"
)

// Client serves one session of an agent. Close releases the session, which need not stop a process. A request that
// the agent refuses fails with a *RequestError. Features that only some agents have are optional interfaces.
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
	// ContinueEventsAfter numbers the client's events, including those it has already, after the last event of the
	// client it replaces, so that a cursor from that client stays valid. The registry calls it before anyone reads the
	// client's events.
	ContinueEventsAfter(int64)
	LiveSnapshot() LiveSnapshot

	// State asks the agent about the session.
	State(context.Context) (State, error)
	// LiveStatus asks the agent for its model, thinking level and context usage.
	LiveStatus(context.Context) (LiveStatus, error)
	// Commands lists the agent's own commands, without those the gateway runs itself.
	Commands(context.Context) ([]Command, error)
	// Prompt returns once the adapter has accepted the prompt, without waiting for the run it starts. Not every agent
	// confirms a prompt before its run ends, so acceptance need not come from the agent.
	Prompt(context.Context, Prompt) (PromptResult, error)
	// Cancel stops the agent's run.
	Cancel(context.Context) error
	// NewSession starts an empty session in the session's folder.
	NewSession(context.Context) (Branch, error)
}

type State struct {
	// Key is the session the agent says it serves, or empty before the agent names it.
	Key        string
	ID         string
	Name       string
	Streaming  bool
	Compacting bool
}

// LiveStatus leaves empty what the agent doesn't report.
type LiveStatus struct {
	Provider      string
	ModelID       string
	ThinkingLevel string
	Context       *ContextUsage
}

// ContextUsage is how much of the model's context window the session fills.
type ContextUsage struct {
	Tokens    float64
	HasTokens bool
	Window    float64
	HasWindow bool
	Percent   float64
}

type Command struct {
	Name        string
	Description string
	Group       string
}

type Prompt struct {
	Text     string
	Delivery Delivery
	// Images saves the attached images and returns them, and is nil without images. The client calls it only to
	// send them, so that the images of a prompt that takes none are never saved.
	Images func() ([]PromptImage, error)
}

// Delivery says when a prompt sent during a run reaches the agent.
type Delivery int

const (
	// Start is for a session without a run.
	Start Delivery = iota
	// Steer reaches the running agent as soon as it can take it.
	Steer
	// FollowUp waits for the run to end.
	FollowUp
)

type PromptImage struct {
	Path     string
	MIMEType string
	Size     int64
}

type PromptResult struct {
	Disposition Disposition
	// QueuedAfterCompaction says the client holds the prompt until a compaction ends.
	QueuedAfterCompaction bool
	// Activity is what the session did just after a command the agent ran at once during a compaction.
	Activity *Activity
}

// Disposition says how the agent took a prompt: "started" a run, "queued" it behind one, or "handled" it without one.
// It is empty when the agent doesn't say.
type Disposition string

type Activity struct {
	Running    bool
	Compacting bool
}

// Branch is a session that the agent created from the current one.
type Branch struct {
	Key string
	ID  string
	// Moved says the client now serves the new session instead of the current one.
	Moved bool
	// Cancelled says the agent's extensions cancelled the branch.
	Cancelled bool
	// Text is the text of the message a fork starts from, for the user to edit.
	Text *string
}

// EventBatch holds the events after a cursor, in the agent's format for the browser.
type EventBatch struct {
	Events  []json.RawMessage `json:"events"`
	LastSeq int64             `json:"last_seq"`
	Missed  bool              `json:"missed"`
}

// LiveSnapshot is the state of the session's run that a page starts from. What the browser reads in the agent's
// format stays opaque, with the fields that the gateway compares typed.
type LiveSnapshot struct {
	EventSequence     int64
	EventReplayCursor int64
	ActiveTools       []ActiveTool
	// ActiveAssistant is the latest event of the reply that streams, in the agent's format for the browser.
	ActiveAssistant json.RawMessage
	Busy            bool
	BusySince       *time.Time
	AgentBusySince  *time.Time
	AgentRunning    bool
	ActiveShell     *Shell
	// CompletedShells are the shell commands that ended during the run.
	CompletedShells []CompletedShell
	Compacting      bool
	CompactingSince *time.Time
	Queue           Queue
	// PendingRequests counts the questions that the agent waits for the user to answer.
	PendingRequests int
	// ExtensionUI holds the questions, statuses, widgets and title of the agent's extensions, in its format for the
	// browser. It is nil when there are none.
	ExtensionUI json.RawMessage
}

// ActiveTool is a tool that runs, or that ended before the session saved its result.
type ActiveTool struct {
	ID string
	// Data is the tool's latest event, in the agent's format for the browser.
	Data json.RawMessage
}

// Shell is a shell command that the user ran in the session.
type Shell struct {
	ID                 string
	Command            string
	ExcludeFromContext bool
	StartedAt          time.Time
	// Output is what the command printed, or the end of it that the agent keeps while the command runs.
	Output string
}

type CompletedShell struct {
	Shell
	// Ended says the command ran to its end with the result below, which the session saves. A command that failed to
	// run has none.
	Ended     bool
	ExitCode  *int
	Cancelled bool
	Truncated bool
	// Data is the command's end, in the agent's format for the browser.
	Data json.RawMessage
}

// Queue holds the prompts that wait for the run: steered ones reach the agent as soon as it can take them, and
// follow-ups once the run ends.
type Queue struct {
	Steering []string
	FollowUp []string
}
