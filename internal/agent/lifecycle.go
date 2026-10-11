package agent

// Lifecycle is a step of the agent's runs, which the gateway follows to tell the user about finished replies.
type Lifecycle struct {
	Step LifecycleStep
	// Reply is the reply that ReplyEnded reports, or the last reply of the run that RunEnded reports. It is empty when
	// the run has no reply.
	Reply Reply
}

type LifecycleStep int

const (
	RunStarted LifecycleStep = iota + 1
	ReplyEnded
	RunEnded
	// RunSettled follows the end of a run once the agent has nothing left to do for it, such as a retry.
	RunSettled
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
