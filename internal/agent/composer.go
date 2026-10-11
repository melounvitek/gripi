package agent

// Composer describes the agent's editor: how it reads what the user types, the commands it offers and the values its
// pickers accept. The gateway asks it before a client runs, so that it can refuse input without starting the agent.
type Composer interface {
	// InternalCommand reports whether the message would run one of the commands that carry the adapter's own requests.
	InternalCommand(message string) bool
	// ShellCommand recognizes a command for the user's shell.
	ShellCommand(message string) (ShellCommand, bool)
	// SlashCommand recognizes one of the commands that the gateway runs itself. Its Name is empty for other messages.
	SlashCommand(message string) SlashCommand
	// Commands lists the commands that the gateway runs itself.
	Commands() []Command
	// CommandGroups orders the groups of the command palette. A command in no listed group is counted but not shown.
	CommandGroups() []string
	// ThinkingLevels lists the thinking levels the agent accepts.
	ThinkingLevels() []string
	// TreeFilters lists the filters of the session tree.
	TreeFilters() []string
}

type ShellCommand struct {
	Command            string
	ExcludeFromContext bool
}

type SlashCommand struct {
	Name string
	// Argument is the rest of the command, such as the new name of /name.
	Argument string
	// Unavailable explains why the gateway cannot run the command, such as one that only the agent's own CLI runs.
	Unavailable string
}
