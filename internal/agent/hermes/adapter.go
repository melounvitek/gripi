package hermes

import (
	"context"
	"errors"
	"log"
	"os"
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

var errNotRunnable = errors.New("Hermes sessions cannot run in Gripi yet")

func (adapter *Adapter) Start(string, agent.StartOptions) (agent.Client, error) {
	return nil, errNotRunnable
}

func (adapter *Adapter) StartInCWD(string, string, agent.StartOptions) (agent.Client, error) {
	return nil, errNotRunnable
}

func (adapter *Adapter) DisplaySettings(string) agent.DisplaySettings { return agent.DisplaySettings{} }

func (adapter *Adapter) Composer() agent.Composer { return composer{} }

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
