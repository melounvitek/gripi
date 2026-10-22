package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/state"
)

type Options struct {
	// Path runs Hermes, or one of its profiles: `<Path> acp` serves the sessions.
	Path string
	// Home is the folder of a session whose own folder the gateway cannot find.
	Home string
	// IdleTimeout stops Hermes once it has served nothing for that long, which frees the sessions it loaded.
	// Zero keeps it running.
	IdleTimeout time.Duration
	// StateDir keeps how many replies of each session ended in the gateway, across restarts. Without it the adapter
	// forgets.
	StateDir string
}

// Adapter runs Hermes sessions in one `hermes acp` process, which ACP lets serve many sessions.
type Adapter struct {
	path        string
	home        string
	idleTimeout time.Duration

	mu   sync.Mutex
	conn *connection
	// env holds the user's variables for Hermes, which it gets when it starts.
	env []string
	// clients counts the clients that serve sessions, which keep Hermes running.
	clients int
	usedAt  time.Time
	// version is how `<path> --version` named the installed Hermes when the running one started.
	version string
	// failure is why Hermes last failed to start, which the adapter answers until retryAfter has passed.
	failure  error
	failedAt time.Time

	listMu   sync.Mutex
	listed   []listedSession
	listedAt time.Time

	sessionsMu sync.Mutex
	sessions   map[string]*session

	repliesMu   sync.Mutex
	repliesFile *state.File
	// replies counts the replies of each session that ended in the gateway, which is all a page compares to mark the
	// session read: Hermes doesn't list how many replies a session has.
	replies map[string]int
}

var _ agent.Adapter = (*Adapter)(nil)

const (
	retryAfter = 30 * time.Second
	// listFresh is how long a listing serves the gateway, which lists sessions for every page and sidebar.
	listFresh   = 2 * time.Second
	listTimeout = 15 * time.Second
	// loadTimeout bounds loading a session, for which Hermes builds the session's agent.
	loadTimeout = time.Minute
)

func New(options Options) *Adapter {
	adapter := &Adapter{path: options.Path, home: options.Home, idleTimeout: options.IdleTimeout, replies: make(map[string]int)}
	if options.StateDir != "" {
		adapter.repliesFile = state.NewFile(filepath.Join(options.StateDir, "hermes-replies.json"))
		contents, found, err := adapter.repliesFile.Read()
		if err == nil && found {
			err = json.Unmarshal(contents, &adapter.replies)
		}
		if err != nil {
			log.Printf("read Hermes reply counts: %v", err)
		}
	}
	return adapter
}

// Start serves a session that Hermes has saved, or one that the running Hermes started and hasn't saved yet.
func (adapter *Adapter) Start(key string, options agent.StartOptions) (agent.Client, error) {
	adapter.useVariables(options.Env)
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	if listed, ok := adapter.find(key); ok {
		session, conn, err := adapter.load(ctx, listed)
		if err != nil {
			return nil, err
		}
		return adapter.newClient(session, conn, options), nil
	}
	conn, err := adapter.connected(ctx)
	if err != nil {
		return nil, err
	}
	session := adapter.session(strings.TrimPrefix(key, keyPrefix))
	session.mu.Lock()
	started := session.conn == conn
	session.mu.Unlock()
	if !started {
		return nil, fmt.Errorf("Hermes has no session %s", key)
	}
	return adapter.newClient(session, conn, options), nil
}

// StartInCWD starts a new session in cwd. Hermes saves it once it has a message.
func (adapter *Adapter) StartInCWD(cwd, forkFrom string, options agent.StartOptions) (agent.Client, error) {
	if forkFrom != "" {
		return nil, &agent.RequestError{Message: "Gripi cannot continue a session in Hermes."}
	}
	adapter.useVariables(options.Env)
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	adapter.stopIfUpdated(ctx)
	conn, err := adapter.connected(ctx)
	if err != nil {
		return nil, err
	}
	var answer struct {
		SessionID string `json:"sessionId"`
		Models    struct {
			CurrentModelID string `json:"currentModelId"`
		} `json:"models"`
	}
	if err := conn.request(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}, &answer); err != nil {
		return nil, err
	}
	if answer.SessionID == "" {
		return nil, errors.New("Hermes started no session")
	}
	session := adapter.session(answer.SessionID)
	session.mu.Lock()
	session.conn, session.model, session.cwd = conn, answer.Models.CurrentModelID, cwd
	session.mu.Unlock()
	return adapter.newClient(session, conn, options), nil
}

func (adapter *Adapter) DisplaySettings(string) agent.DisplaySettings { return agent.DisplaySettings{} }

func (adapter *Adapter) Composer(string) agent.Composer { return composer{} }

func (adapter *Adapter) Store() agent.Store { return store{adapter} }

// Guard passes every call through: Gripi writes only the sessions of its own Hermes, which ACP lets serve them alone.
func (adapter *Adapter) Guard(clients *agent.Registry) (agent.Guard, error) {
	return agent.OpenGuard(clients), nil
}

func (adapter *Adapter) Close() error {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.conn != nil {
		_ = adapter.conn.Close()
		adapter.conn = nil
	}
	return nil
}

// useVariables gives Hermes the user's variables. Once they have changed, Hermes restarts with them when no client
// holds a session, which then loads again. Until then, Hermes keeps the variables it started with.
func (adapter *Adapter) useVariables(env []string) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if slices.Equal(env, adapter.env) || adapter.clients > 0 {
		return
	}
	adapter.env = env
	if adapter.conn != nil {
		_ = adapter.conn.Close()
		adapter.conn = nil
	}
}

// connected returns the connection to the running Hermes, and starts Hermes when it doesn't run.
func (adapter *Adapter) connected(ctx context.Context) (*connection, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.usedAt = time.Now()
	if adapter.conn != nil && !adapter.conn.hasExited() {
		return adapter.conn, nil
	}
	if adapter.conn != nil {
		_ = adapter.conn.Close()
		adapter.conn = nil
	}
	if adapter.failure != nil && time.Since(adapter.failedAt) < retryAfter {
		return nil, adapter.failure
	}
	version := installedVersion(ctx, adapter.path)
	conn, err := start(adapter.path, append(environment(os.Environ()), adapter.env...), adapter)
	if err == nil {
		if err = conn.initialize(ctx); err != nil {
			_ = conn.Close()
		}
	}
	if err != nil {
		adapter.failure, adapter.failedAt = err, time.Now()
		log.Printf("Hermes: %v", err)
		return nil, err
	}
	adapter.conn, adapter.version, adapter.failure = conn, version, nil
	if adapter.idleTimeout > 0 {
		go adapter.stopWhenIdle(conn)
	}
	return conn, nil
}

// stopIfUpdated stops a running Hermes that an update has replaced, unless it replies, and reports whether it did, so
// that a session starts or loads in the installed Hermes. The old one loads some of its code only when it first needs
// it, from the update, which may not fit what it loaded before. Clients of other sessions start again with their next
// request.
func (adapter *Adapter) stopIfUpdated(ctx context.Context) bool {
	adapter.mu.Lock()
	conn, version := adapter.conn, adapter.version
	adapter.mu.Unlock()
	if conn == nil || conn.hasExited() || version == "" {
		return false
	}
	if installed := installedVersion(ctx, adapter.path); installed == "" || installed == version || adapter.replying() {
		return false
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.conn == conn {
		_ = conn.Close()
		adapter.conn = nil
	}
	return true
}

// replying is whether Hermes replies in a session that a client serves.
func (adapter *Adapter) replying() bool {
	adapter.sessionsMu.Lock()
	sessions := slices.Collect(maps.Values(adapter.sessions))
	adapter.sessionsMu.Unlock()
	return slices.ContainsFunc(sessions, func(session *session) bool {
		session.mu.Lock()
		client := session.client
		session.mu.Unlock()
		return client != nil && client.Busy()
	})
}

// installedVersion is the first line of what `<path> --version` prints, which names the installed Hermes down to its
// commit, or nothing when Hermes doesn't say. The lines after it change without an update, such as whether one waits.
func installedVersion(ctx context.Context, path string) string {
	command := exec.CommandContext(ctx, path, "--version")
	command.Env = environment(os.Environ())
	output, err := command.Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(output), "\n")
	return strings.TrimSpace(line)
}

func (adapter *Adapter) stopWhenIdle(conn *connection) {
	for {
		select {
		case <-conn.exited:
			return
		case <-time.After(max(adapter.idleTimeout/4, 10*time.Millisecond)):
		}
		adapter.mu.Lock()
		idle := adapter.conn == conn && adapter.clients == 0 && time.Since(adapter.usedAt) >= adapter.idleTimeout
		if idle {
			adapter.conn = nil
		}
		adapter.mu.Unlock()
		if idle {
			_ = conn.Close()
			return
		}
	}
}

func (adapter *Adapter) countReply(id string) {
	adapter.repliesMu.Lock()
	defer adapter.repliesMu.Unlock()
	adapter.replies[id]++
	if adapter.repliesFile == nil {
		return
	}
	encoded, _ := json.Marshal(adapter.replies)
	if err := adapter.repliesFile.Write(encoded); err != nil {
		log.Printf("save Hermes reply counts: %v", err)
	}
}

func (adapter *Adapter) replyCount(id string) int {
	adapter.repliesMu.Lock()
	defer adapter.repliesMu.Unlock()
	return adapter.replies[id]
}
