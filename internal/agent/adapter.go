package agent

// Adapter starts session clients for one agent and answers what the gateway needs to know about it.
type Adapter interface {
	// Start starts a client for an existing session, or for an unsaved one under its key in options.CWD.
	Start(key string, options StartOptions) (Client, error)
	// StartInCWD starts a client for a new session in cwd, optionally copying the session forkFrom.
	StartInCWD(cwd, forkFrom string, options StartOptions) (Client, error)
	// DisplaySettings returns the agent's display preferences for a project.
	DisplaySettings(cwd string) DisplaySettings
	// Composer describes how the agent reads what the user types.
	Composer() Composer
	// Store returns the agent's catalog of saved sessions.
	Store() Store
	// Guard returns the guard for the sessions that clients run. The gateway calls it once, after creating clients.
	Guard(clients *Registry) (Guard, error)
	// Close releases what the adapter holds after its clients have stopped.
	Close() error
}

type StartOptions struct {
	// CWD is the folder of a session that the agent has not saved yet, which starts there empty.
	CWD string
	// Env holds extra NAME=value variables for the agent.
	Env         []string
	Diagnostics *Diagnostics
	// Observe receives the steps of the agent's runs. It must not wait for the client's responses.
	Observe func(Client, Lifecycle)
}

type DisplaySettings struct {
	HideThinkingBlock bool
}
