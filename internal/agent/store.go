package agent

// Store is the agent's catalog of saved sessions. The gateway keys a session by an opaque string that only the
// store interprets: the gateway never parses a key, and asks the store which keys name the same session.
type Store interface {
	// Sessions lists the saved sessions, most recently active first, each under its canonical key.
	Sessions() ([]*Session, error)
	// SessionsDeferringMetadata lists like Sessions, but may serve the last known metadata of the sessions that
	// deferFor selects instead of reading them again, and reports whether it did.
	SessionsDeferringMetadata(deferFor func(key string) bool) ([]*Session, bool, error)
	// Session finds a saved session by any of its keys.
	Session(key string) (*Session, bool)
	// Window returns a page of the messages on the branch that ends at leafID, or at the latest entry unless
	// leafSupplied: the newest ones before the cursor-th message, or with after the oldest from the after-th on.
	// The error matches fs.ErrNotExist when the session is not saved.
	Window(key, leafID string, leafSupplied bool, cursor, after *int) (Window, error)
	// Status returns the model and context usage the session last saved.
	Status(key string) (Status, error)
	// Generation identifies the saved session, so a page notices when its key comes to name another one.
	// It is empty when the session is not saved.
	Generation(key string) string
	// Image returns an image of a saved message and its MIME type.
	Image(key, entryID string, index int) ([]byte, string, error)
	// SubagentToolCallContext returns what the saved branch ending at leafID records about the given tool calls.
	SubagentToolCallContext(key, leafID string, ids []string) map[string]ToolCallContext

	// Exists reports whether the session is saved. A new session can have a key before the agent saves it.
	Exists(key string) bool
	// Canonical returns the key that gateway state is kept under, and false when key cannot name one of the
	// agent's sessions.
	Canonical(key string) (string, bool)
	// Aliases returns every key that names the same session, the canonical one first, or nil when Canonical fails.
	Aliases(key string) []string
	// ValidKey reports whether key has the form of the agent's keys. It need not name a saved session.
	ValidKey(key string) bool
	// FallbackCWD returns a working directory for a session whose own the gateway cannot find.
	FallbackCWD(key string) string
	// Delete removes the saved session and what the store cached about it, and says how: Pi answers "trash" or "unlink".
	Delete(key string) (method string, err error)
	// SaveCache keeps what the store has learnt about sessions, so the next start can list them without reading
	// each again.
	SaveCache() error
}
