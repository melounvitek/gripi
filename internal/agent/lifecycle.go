package agent

// Lifecycle is a step of the agent's runs, which the gateway follows to tell the user about finished replies.
type Lifecycle struct {
	Step LifecycleStep
	// Reply is the reply that ReplyEnded reports, or the last reply of the run that RunEnded reports. It is empty when
	// the run has no reply.
	Reply Reply
	// Error is why the run that RunEnded reports failed, or why the agent refused the prompt that PromptEnded reports.
	Error string
	// Aborted says that the run that RunSettled reports was stopped.
	Aborted bool
}

type LifecycleStep int

const (
	RunStarted LifecycleStep = iota + 1
	ReplyEnded
	RunEnded
	// RunSettled follows the end of a run once the agent has nothing left to do for it, such as a retry.
	RunSettled
	// PromptEnded reports a prompt that started no run, because the agent refused it or handled it itself, when the
	// agent answered only after the gateway had stopped waiting, or answered a prompt that a compaction held back.
	PromptEnded
)

// Reply is an assistant message that ended.
type Reply struct {
	// Text is the reply's final text, without its thinking and tool calls.
	Text string
	// Final says the reply ended normally with text, so that the user can be told about it. Only a final reply has
	// an ID, which is the same in every step that reports the reply.
	Final bool
	ID    string
}
