package agent

import "os/exec"

// Process is an agent's process that an adapter is about to start for a session.
type Process struct {
	// Key names the session, or is empty for a new one that the agent names once it runs.
	Key string
	// Dir is the folder the process starts in, when the adapter knows it.
	Dir string
	// Command is the program and its arguments.
	Command []string
	// Env holds NAME=value variables on top of the gateway's scrubbed environment.
	Env []string
	// Lost is for a launcher that loses the process, such as with the connection to the computer that runs it, to
	// say so with a message for the user. The process's run, if one was going, ends with the message, because nobody
	// knows how it ended.
	Lost func(message string)
}

// Launcher lets the gateway run an agent's process elsewhere, such as on another computer. It returns the command to
// start instead, whose input and output carry the process's, and release, which the adapter calls once that command
// has ended; or no command, and the adapter starts the process here.
type Launcher func(Process) (command *exec.Cmd, release func(), err error)
