package hermes

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

type Options struct {
	// Path runs Hermes, or one of its profiles: `<Path> acp` serves the sessions.
	Path string
	// Home is the folder of a session whose own folder the gateway cannot find.
	Home string
}

// Adapter runs Hermes sessions in one `hermes acp` process, which ACP lets serve many sessions.
type Adapter struct {
	path string
	home string

	mu   sync.Mutex
	conn *connection
	// failure is why Hermes last failed to start, which the adapter answers until retryAfter has passed.
	failure  error
	failedAt time.Time

	listMu   sync.Mutex
	listed   []listedSession
	listedAt time.Time

	sessionsMu sync.Mutex
	sessions   map[string]*session
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
	return &Adapter{path: options.Path, home: options.Home}
}

// Start serves a session that Hermes has saved, or one that the running Hermes started and hasn't saved yet.
func (adapter *Adapter) Start(key string, options agent.StartOptions) (agent.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	if listed, ok := adapter.find(key); ok {
		session, err := adapter.load(ctx, listed)
		if err != nil {
			return nil, err
		}
		return adapter.newClient(session, session.conn, options), nil
	}
	conn, err := adapter.connected(ctx)
	if err != nil {
		return nil, err
	}
	session := adapter.session(strings.TrimPrefix(key, keyPrefix))
	if session.conn != conn {
		return nil, fmt.Errorf("Hermes has no session %s", key)
	}
	return adapter.newClient(session, conn, options), nil
}

// StartInCWD starts a new session in cwd. Hermes saves it once it has a message.
func (adapter *Adapter) StartInCWD(cwd, forkFrom string, options agent.StartOptions) (agent.Client, error) {
	if forkFrom != "" {
		return nil, &agent.RequestError{Message: "Gripi cannot continue a session in Hermes."}
	}
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
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
	session.conn, session.model = conn, answer.Models.CurrentModelID
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

// connected returns the connection to the running Hermes, and starts Hermes when it doesn't run.
func (adapter *Adapter) connected(ctx context.Context) (*connection, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
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
	conn, err := start(adapter.path, environment(os.Environ()), adapter.notified)
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
	adapter.conn, adapter.failure = conn, nil
	return conn, nil
}
