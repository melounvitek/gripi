package worker

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/keyedlock"
	"github.com/melounvitek/gripi/internal/worker/auth"
	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// State is how far main's connection to a worker has got.
type State string

const (
	Disconnected State = "disconnected"
	Connecting   State = "connecting"
	// Copying means a session's copy of its repository is being copied to the worker.
	Copying   State = "copying"
	Connected State = "connected"
	// Unreachable means that main lost its connection to the worker, whose helper may still run sessions until the
	// worker is gone.
	Unreachable State = "unreachable"
	// Gone means that main lost the worker long enough ago for every session there to have stopped, so they can
	// continue on this machine.
	Gone State = "gone"
	// Returning is a session's, not a worker's: it is coming back to main, or a return of it has to be finished.
	Returning State = "returning"
)

// Status is how main's connection to a worker is.
type Status struct {
	State State `json:"state"`
	// GoneAt is when a worker that main lost is gone, unless main has reached it again since.
	GoneAt time.Time `json:"gone_at,omitzero"`
	// User and Docker are what the worker said when main last connected to it (Control's), and empty until then.
	User   string `json:"user,omitempty"`
	Docker string `json:"docker,omitempty"`
}

// refusal is an error that refuses what was asked, such as moving a session that is moving already, rather than
// failing at it.
type refusal struct{ error }

// Refused tells callers, such as the gateway's routes, that the error refuses rather than fails.
func (refusal) Refused() bool { return true }

func (err refusal) Unwrap() error { return err.error }

func refuse(format string, arguments ...any) error {
	return refusal{fmt.Errorf(format, arguments...)}
}

// connectTimeout bounds connecting to a worker, which includes copying Pi there the first time.
const connectTimeout = 5 * time.Minute

// reconnectInterval is how often main tries to connect to a worker whose earlier helper may still run, and refuses
// another.
const reconnectInterval = 2 * time.Second

// firstBarrierTimeout bounds waiting until main's copies of a move have the first barriers of their mirrors, which a
// session's Pi waits for.
var firstBarrierTimeout = time.Minute

// closeSessionTimeout bounds closing a session's Pi on a worker, which the helper kills when it hasn't stopped 5
// seconds after SIGTERM.
const closeSessionTimeout = 10 * time.Second

// ManagerOptions configure NewManager.
type ManagerOptions struct {
	// Destination returns where ssh connects for the worker of a name, as `gripi worker add` saved it.
	Destination func(name string) (string, error)
	// Placements records which sessions are on which worker.
	Placements *Placements
	// StateDir is Gripi's state folder, which holds ssh's control sockets and main's side of each move. It must be on
	// the file system of the repositories and of Pi's sessions.
	StateDir string
	// Pi is main's Pi, which every worker gets a copy of. Its ExtensionDir holds WorkerExtension.
	Pi Pi
	// WorkerExtension is the path of Gripi's worker extension, which the Pi of every session on a worker loads.
	WorkerExtension string
	// SessionsRoot holds Pi's session folders.
	SessionsRoot string
	// GatewayPaths are Gripi's other own files and folders, wherever its settings put them, such as its configuration
	// folder and state files. Like Pi's agent folder, the sessions' root and Gripi's state folder, they never move to a
	// worker.
	GatewayPaths []string
	// SocketPath is the path of the gripi socket, where gripi commands on a worker reach the worker's own socket.
	SocketPath string
	// Store finds saved sessions, and with agent.ToolPathsReader the worktrees that they used.
	Store agent.Store
	// Rewritten tells the guard of the sessions' files that main's copy of a session's file on a worker was written
	// anew, as the worker's Pi rewrote it (agent.Guard's Rewritten).
	Rewritten func(key string)
	// ServeSocket serves the gripi commands of a worker, which reach the sessions that placement says are on the
	// worker, on a socket of its own.
	ServeSocket func(name string, placement WorkerPlacement) (socket string, stop func() error, err error)
	// Connection holds Connect's options but Destination and SocketDir, which the manager sets for each worker. Its
	// Home is main's home folder, which the repositories of moving sessions must be in.
	Connection Options
	// Clock is time.Now when nil.
	Clock func() time.Time

	// open, in tests, replaces connecting to a worker, checking it and giving it main's Pi and gripi socket.
	open func(ctx context.Context, name string) (link, *Installation, func() error, error)
}

// link is main's connection to a worker as the manager uses it: a *Conn, or a fake in tests.
type link interface {
	authApplier
	User() string
	Docker() string
	Lost() <-chan struct{}
	GoneAt() time.Time
	Forward(ctx context.Context, port Forward) (cancel func() error, err error)
	CreateSession(ctx context.Context, session Session) error
	Attach(ctx context.Context, session string) (*exec.Cmd, func(), error)
	CloseSession(ctx context.Context, id string) error
	PrepareMove(ctx context.Context, move Move) (string, error)
	UploadMove(ctx context.Context, id, folder string) error
	UploadSessionFile(ctx context.Context, file string) error
	SeedMove(ctx context.Context, id string) error
	StartMoveMirror(ctx context.Context, id, file string) error
	AttachMoveMirror(ctx context.Context, id, file string) (*exec.Cmd, func(), error)
	ReleaseMove(ctx context.Context, id string) (helper.Barriers, error)
	DeleteMove(ctx context.Context, id string) error
	// hangUp ends every channel at once, also over a link that hangs, before Close.
	hangUp()
	Close() error
}

var _ link = (*Conn)(nil)

func (conn *Conn) hangUp() { conn.Pool.Close() }

// Manager runs the sessions placed on workers there: it connects to a worker when a session needs it, mirrors each
// move's copy and session file to main, and moves sessions between main and workers.
type Manager struct {
	options ManagerOptions
	ctx     context.Context
	cancel  context.CancelFunc
	started time.Time
	logins  *logins
	// repositories is held, by a repository's Git folder, while a move takes its base or a return brings changes to it.
	repositories keyedlock.Mutexes
	// background has what goes on after a move or connection, which Close waits for.
	background sync.WaitGroup

	mu       sync.Mutex
	workers  map[string]*remote
	moving   map[string]*moving  // sessions on their way between main and a worker, by key
	sessions map[string]launched // by the session's ID on its worker
	launches []string            // keys of sessions whose Pi is starting
	launched *sync.Cond          // signalled when a launch ends

	// closing has the sessions whose Pi the helper is closing, by their ID on the worker, which their next Pi waits for.
	closing map[string]chan struct{}
}

// moving is a session on its way between main and a worker.
type moving struct {
	worker     string
	repository string // its main checkout, once the move knows it
	since      time.Time
	returning  bool
}

// remote is main's side of one worker. The manager's mutex guards it, except connecting.
type remote struct {
	name         string
	connecting   sync.Mutex // held while connecting, so that others wait for the connection
	state        State
	goneAt       time.Time // when the worker is gone, after main lost its connection
	user, docker string    // as the worker said when main last connected to it
	conn         link
	installation *Installation
	stopSocket   func() error
	mirrors      map[string]*moveMirrors // by move
	starting     sync.WaitGroup          // mirrors that start on the connection
	forwards     map[int]*forwarded      // by the port on the worker; they end with the connection
	// tornDown is closed once the last disconnect has stopped the mirrors of its connection, whose copies on main
	// nothing else may use until then.
	tornDown chan struct{}
}

// launched is a session whose Pi the manager started on a worker.
type launched struct {
	worker string
	lost   func(message string) // agent.Process.Lost
}

// liveMirror is main's copy of one of a move's mirrors, kept up to date by the mirror on the worker.
type liveMirror struct {
	destination *mirror.Destination
	started     time.Time // before the mirror's first barrier
	cancel      context.CancelFunc
	done        chan struct{}
	err         error // why the mirror stopped, once done is closed
}

func (live *liveMirror) running() bool {
	select {
	case <-live.done:
		return false
	default:
		return true
	}
}

// NewManager returns a manager that connects to no worker until a session or a move needs it. It cleans up after
// the moves that an earlier gateway didn't finish.
func NewManager(options ManagerOptions) *Manager {
	if options.Clock == nil {
		options.Clock = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		options: options, ctx: ctx, cancel: cancel, started: options.Clock(), workers: map[string]*remote{}, sessions: map[string]launched{},
		logins: newLogins(auth.Pi{Command: options.Pi.Command, AgentDir: options.Pi.AgentDir}, options.Clock),
		moving: map[string]*moving{}, closing: map[string]chan struct{}{},
	}
	manager.launched = sync.NewCond(&manager.mu)
	go manager.logins.run(ctx, loginsPoll)
	if options.Placements != nil {
		for _, placement := range options.Placements.Begun() {
			manager.abandon(placement)
		}
	}
	return manager
}

// Status reports the connection to the worker of a name.
func (manager *Manager) Status(name string) Status {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	worker := manager.workers[name]
	if worker == nil {
		return Status{State: Disconnected}
	}
	status := Status{State: worker.state, GoneAt: worker.goneAt, User: worker.user, Docker: worker.docker}
	if worker.state == Disconnected && !worker.goneAt.IsZero() {
		status.State = Gone
		if manager.options.Clock().Before(worker.goneAt) {
			status.State = Unreachable
		}
	}
	return status
}

// Location is where a session runs while it is on a worker, or on its way to one.
type Location struct {
	Worker string
	// Status is the worker's, but Returning while the session comes back.
	Status
	// Repository is the main checkout of the session's repository, once a move knows it.
	Repository string
	// Copying is when the session started moving to the worker, which it is until it runs there.
	Copying time.Time
	// Returning is when the session started coming back, while it does. A return that stopped before it finished
	// leaves the state Returning without it.
	Returning time.Time
	// Forwards are the worker's ports that main forwards for the session, by port.
	Forwards []Forwarded
}

// Location reports the worker that runs a session, or that it is moving to.
func (manager *Manager) Location(key string) (Location, bool) {
	var location Location
	placement, placed := manager.options.Placements.Find(key)
	manager.mu.Lock()
	moving := manager.moving[key]
	if moving != nil {
		location = Location{Worker: moving.worker, Repository: moving.repository}
		if !moving.returning {
			location.Copying = moving.since
		}
	}
	manager.mu.Unlock()
	switch {
	case placed:
		location = Location{Worker: placement.Worker, Repository: filepath.Dir(placement.Repository)}
	case moving == nil:
		return Location{}, false
	}
	location.Status = manager.Status(location.Worker)
	switch {
	case placed && (placement.Stage != "" || moving != nil && moving.returning):
		location.State = Returning
		if moving != nil && moving.returning {
			location.Returning = moving.since
		}
	case location.Copying.IsZero() && location.State == Copying:
		// The worker is getting another session's copy, while it runs this one.
		location.State = Connected
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if worker := manager.workers[location.Worker]; worker != nil {
		for port, forwarded := range worker.forwards {
			if slices.Contains(forwarded.sessions, key) {
				location.Forwards = append(location.Forwards, Forwarded{Port: port, Local: forwarded.local})
			}
		}
	}
	slices.SortFunc(location.Forwards, func(left, right Forwarded) int { return left.Port - right.Port })
	return location, true
}

// Placed returns the folders that the sessions on workers run in, by the sessions' keys.
func (manager *Manager) Placed() map[string]string {
	placed := map[string]string{}
	for _, placement := range manager.options.Placements.All() {
		placed[placement.Session] = placement.Cwd
	}
	return placed
}

// connected reports whether main has a connection to the worker of a name.
func (manager *Manager) connected(name string) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	worker := manager.workers[name]
	return worker != nil && worker.conn != nil
}

func (manager *Manager) setState(worker *remote, state State) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	worker.state = state
}

// connect returns the worker of a name, connected and with main's Pi, and with the mirrors of every move on it
// running, but those whose return has them already or runs. It connects when the worker is not connected yet or lost
// its connection. It fails when the mirrors of the move required don't run.
func (manager *Manager) connect(ctx context.Context, name, required string) (*remote, error) {
	manager.mu.Lock()
	worker := manager.workers[name]
	if worker == nil {
		worker = &remote{name: name, state: Disconnected, mirrors: map[string]*moveMirrors{}}
		manager.workers[name] = worker
	}
	manager.mu.Unlock()
	worker.connecting.Lock()
	defer worker.connecting.Unlock()
	manager.mu.Lock()
	conn := worker.conn
	manager.mu.Unlock()
	if conn != nil {
		select {
		case <-conn.Lost():
			manager.disconnect(worker, conn, true)
			conn = nil
		default:
		}
	}
	if conn == nil {
		if err := manager.waitForTeardown(ctx, name); err != nil {
			return nil, err
		}
		if err := manager.dial(ctx, worker); err != nil {
			return nil, fmt.Errorf("connect to worker %s: %w", name, err)
		}
	}
	for _, placement := range manager.options.Placements.All() {
		manager.mu.Lock()
		moving := manager.moving[placement.Session] != nil
		manager.mu.Unlock()
		// A return that runs has the mirrors of its move, which may have ended for good.
		if placement.Worker != name || placement.Stage == released || moving && placement.Move != required {
			continue
		}
		if err := manager.startMirrors(ctx, worker, placement); err != nil {
			err = fmt.Errorf("mirror session %s from worker %s: %w", placement.Session, name, err)
			if placement.Move == required {
				return nil, err
			}
			log.Print(err)
		}
	}
	return worker, nil
}

// dial connects to a worker, gives it main's Pi and lets its gripi commands reach the gateway, then deletes the copies
// that it keeps of moves that ended.
func (manager *Manager) dial(ctx context.Context, worker *remote) error {
	manager.setState(worker, Connecting)
	open := manager.options.open
	if open == nil {
		open = manager.open
	}
	conn, installation, stopSocket, err := open(ctx, worker.name)
	if err != nil {
		manager.setState(worker, Disconnected)
		return err
	}
	manager.mu.Lock()
	// The new helper runs, so the helper of a lost connection has stopped.
	worker.conn, worker.installation, worker.stopSocket, worker.state, worker.goneAt = conn, installation, stopSocket, Connected, time.Time{}
	worker.user, worker.docker = conn.User(), conn.Docker()
	manager.mu.Unlock()
	if docker := conn.Docker(); docker != "" && docker != "available" {
		log.Printf("Docker: %s", docker)
	}
	manager.logins.add(worker.name, conn)
	go func() {
		<-conn.Lost()
		manager.disconnect(worker, conn, true)
	}()
	manager.background.Go(func() { manager.deleteCopies(worker.name, conn) })
	return nil
}

// open connects to the worker of a name, checks that it doesn't share main's files, gives it main's Pi, and forwards
// its gripi socket to the gateway.
func (manager *Manager) open(ctx context.Context, name string) (link, *Installation, func() error, error) {
	options := manager.options.Connection
	options.Name = name
	var err error
	if options.Destination, err = manager.options.Destination(name); err != nil {
		return nil, nil, nil, err
	}
	options.SocketDir = filepath.Join(manager.options.StateDir, "ssh")
	var conn *Conn
	for {
		if conn, err = Connect(ctx, options); err == nil {
			break
		}
		manager.mu.Lock()
		goneAt := manager.workers[name].goneAt
		manager.mu.Unlock()
		// The helper of a lost connection refuses another until it has stopped, which it has once the worker is gone.
		if !manager.options.Clock().Before(goneAt) {
			return nil, nil, nil, err
		}
		select {
		case <-ctx.Done():
			return nil, nil, nil, fmt.Errorf("%w; worker %s may still run the sessions of a lost connection until %s", err, name, goneAt.Local().Format(time.TimeOnly))
		case <-time.After(reconnectInterval):
		}
	}
	fail := func(err error) (link, *Installation, func() error, error) {
		conn.Close()
		return nil, nil, nil, err
	}
	socketDir := ""
	if manager.options.SocketPath != "" {
		socketDir = filepath.Dir(manager.options.SocketPath)
	}
	if err := CheckSeparate(options.Destination, func(script string, arguments ...string) (string, error) {
		return conn.runInView(ctx, script, arguments...)
	}, manager.options.Pi.AgentDir, socketDir); err != nil {
		return fail(err)
	}
	installation, err := conn.Bootstrap(ctx, manager.options.Pi)
	if err != nil {
		return fail(err)
	}
	socket, stopSocket, err := manager.options.ServeSocket(name, manager.Placement(name))
	if err != nil {
		return fail(err)
	}
	// sshd creates the socket outside the view, where the worker's processes find it at main's path in the view.
	if _, err := conn.Forward(ctx, Forward{Remote: true, ListenSocket: conn.workerPath(manager.options.SocketPath), ConnectSocket: socket}); err != nil {
		stopSocket()
		return fail(err)
	}
	return conn, installation, stopSocket, nil
}

// Reconnect connects to the worker of a name, if main is not connected to it, as the next session there or move to it
// would. After main has lost the worker, it waits while the worker's earlier helper may still run.
func (manager *Manager) Reconnect(ctx context.Context, name string) error {
	_, err := manager.connect(ctx, name, "")
	return err
}

// disconnect forgets a worker's connection and closes it, unless the worker has another one by now. After main lost the
// connection, the worker is unreachable until it is gone, and the steps that its sessions were running end with an
// outcome that nobody knows. The next session there or move to it connects again.
func (manager *Manager) disconnect(worker *remote, conn link, lost bool) {
	manager.mu.Lock()
	if worker.conn != conn {
		manager.mu.Unlock()
		return
	}
	tornDown := make(chan struct{})
	defer close(tornDown)
	mirrors, stopSocket := worker.mirrors, worker.stopSocket
	worker.conn, worker.installation, worker.stopSocket, worker.mirrors, worker.state = nil, nil, nil, map[string]*moveMirrors{}, Disconnected
	worker.tornDown = tornDown
	worker.forwards = nil
	var sessions []launched
	for id, session := range manager.sessions {
		if session.worker == worker.name {
			sessions = append(sessions, session)
			delete(manager.sessions, id)
		}
	}
	if lost {
		worker.goneAt = conn.GoneAt()
		log.Printf("lost the connection to worker %s, which may run sessions until %s", worker.name, worker.goneAt.Local().Format(time.TimeOnly))
	}
	manager.mu.Unlock()
	manager.logins.remove(worker.name, conn)
	conn.hangUp()
	for _, session := range sessions {
		if lost && session.lost != nil {
			session.lost(fmt.Sprintf("Lost the connection to worker %s. The outcome of the running step is unknown.", worker.name))
		}
	}
	// Main's copies are as their last barriers left them once their mirrors have ended.
	for _, live := range mirrors {
		live.stop()
	}
	worker.starting.Wait()
	stopSocket()
	conn.Close()
}

// waitForTeardown waits until the last disconnect from the worker of a name has stopped its mirrors.
func (manager *Manager) waitForTeardown(ctx context.Context, name string) error {
	manager.mu.Lock()
	var tornDown chan struct{}
	if worker := manager.workers[name]; worker != nil {
		tornDown = worker.tornDown
	}
	manager.mu.Unlock()
	if tornDown == nil {
		return nil
	}
	select {
	case <-tornDown:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close closes the connections, which stops every session on the workers, and their mirrors. Call it once the
// sessions' Pi have stopped, whose last steps main's copies have.
func (manager *Manager) Close() {
	manager.mu.Lock()
	workers := make([]*remote, 0, len(manager.workers))
	for _, worker := range manager.workers {
		workers = append(workers, worker)
	}
	manager.mu.Unlock()
	for _, worker := range workers {
		manager.mu.Lock()
		conn := worker.conn
		manager.mu.Unlock()
		if conn != nil {
			manager.disconnect(worker, conn, false)
		}
	}
	manager.cancel()
	manager.background.Wait()
}

// connection returns a worker's connection, or fails when the worker has disconnected.
func (manager *Manager) connection(worker *remote) (link, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if worker.conn == nil {
		return nil, fmt.Errorf("worker %s disconnected", worker.name)
	}
	return worker.conn, nil
}

// Launch runs the Pi of a session on the worker that it moved to, and leaves other sessions' Pi here. It is the Pi
// adapter's launcher.
func (manager *Manager) Launch(process agent.Process) (*exec.Cmd, func(), error) {
	key := process.Key
	if key == "" {
		return nil, nil, nil
	}
	done, err := manager.startLaunch(key)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	placement, placed := manager.options.Placements.Find(key)
	if !placed {
		return nil, nil, nil
	}
	if placement.Stage != "" {
		return nil, nil, refuse("the session is coming back from worker %s; continue on main to finish that", placement.Worker)
	}
	ctx, cancel := context.WithTimeout(manager.ctx, connectTimeout)
	defer cancel()
	// The session's barriers need both mirrors of its move.
	worker, err := manager.connect(ctx, placement.Worker, placement.Move)
	if err != nil {
		return nil, nil, err
	}
	if err := manager.waitForMirrors(ctx, worker, placement.Move); err != nil {
		return nil, nil, err
	}
	conn, err := manager.connection(worker)
	if err != nil {
		return nil, nil, err
	}
	manager.mu.Lock()
	installation := worker.installation
	manager.mu.Unlock()
	// The worker's Pi gets main's latest logins before it starts, without refresh tokens.
	if err := manager.logins.push(ctx, placement.Worker); err != nil {
		return nil, nil, err
	}
	session := Session{
		ID: sessionID(key), Cwd: placement.Cwd, Move: placement.Move,
		Args: installation.Command(append(slices.Clone(process.Command), "--extension", manager.options.WorkerExtension)),
		// gripi commands find the worker's socket where main's is.
		Env: append(installation.Environment(process.Env), "GRIPI_SOCKET_PATH="+manager.options.SocketPath),
	}
	manager.mu.Lock()
	closing := manager.closing[session.ID]
	manager.mu.Unlock()
	if closing != nil {
		select {
		case <-closing:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	if err := conn.CreateSession(ctx, session); err != nil {
		return nil, nil, err
	}
	command, release, err := conn.Attach(ctx, session.ID)
	if err != nil {
		conn.CloseSession(ctx, session.ID)
		return nil, nil, err
	}
	manager.mu.Lock()
	manager.sessions[session.ID] = launched{worker: placement.Worker, lost: process.Lost}
	manager.mu.Unlock()
	return command, func() {
		release()
		manager.closeSession(conn, session.ID)
	}, nil
}

// closeSession stops the Pi of a session on a worker, whose RPC has ended, and keeps the session's other processes,
// as when Gripi stops an idle Pi here. Pi there may hang on its way out, such as in a session_shutdown handler, and
// would keep the session's next Pi from starting. It closes Pi in the background, because the adapter waits for the
// launcher's cleanup while it stops the RPC, and the session's next Pi waits for it.
func (manager *Manager) closeSession(conn link, id string) {
	closed := make(chan struct{})
	manager.mu.Lock()
	manager.closing[id] = closed
	manager.mu.Unlock()
	go func() {
		defer func() {
			manager.mu.Lock()
			if manager.closing[id] == closed {
				delete(manager.closing, id)
			}
			manager.mu.Unlock()
			close(closed)
		}()
		ctx, cancel := context.WithTimeout(manager.ctx, closeSessionTimeout)
		defer cancel()
		if err := conn.CloseSession(ctx, id); err != nil && !errors.Is(err, ErrControlClosed) && manager.ctx.Err() == nil {
			log.Printf("close the Pi of session %s on its worker: %v", id, err)
		}
	}()
}

// sessionID names a session on its worker, the same for every Pi of the session, so they share its cgroup.
func sessionID(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:16])
}

// newMoveID names a new move.
func newMoveID() string {
	random := make([]byte, 8)
	rand.Read(random)
	return hex.EncodeToString(random)
}

// startLaunch notes that the Pi of a session is starting, which a move or return of the session waits for, unless the
// session is moving already.
func (manager *Manager) startLaunch(key string) (done func(), err error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.moving[key] != nil {
		return nil, refuse("the session is moving between this machine and a worker; try again once it has moved")
	}
	manager.launches = append(manager.launches, key)
	return func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		i := slices.Index(manager.launches, key)
		manager.launches = slices.Delete(manager.launches, i, i+1)
		manager.launched.Broadcast()
	}, nil
}

// startMoving marks a session as moving between main and the worker of a name, unless it is moving already, and waits
// until its Pi that is starting has started: a Pi that started on a worker while its session went back would stay
// there.
func (manager *Manager) startMoving(key, worker string, returning bool) (done func(), err error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.moving[key] != nil {
		return nil, refuse("the session is moving already")
	}
	manager.moving[key] = &moving{worker: worker, since: manager.options.Clock(), returning: returning}
	for slices.Contains(manager.launches, key) {
		manager.launched.Wait()
	}
	return func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		delete(manager.moving, key)
	}, nil
}

// goneAt is when the worker of a name must have stopped the sessions of every connection that main lost, including
// those of a gateway before this one, which stopped answering before this one started.
func (manager *Manager) goneAt(name string) time.Time {
	goneAt := manager.started.Add(cmp.Or(manager.options.Connection.Lease, helper.DefaultLease) + GoneMargin)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if worker := manager.workers[name]; worker != nil && worker.goneAt.After(goneAt) {
		goneAt = worker.goneAt
	}
	return goneAt
}

// Placement says what is on the worker of a name, for the gripi commands that run there.
func (manager *Manager) Placement(name string) WorkerPlacement {
	return WorkerPlacement{manager: manager, worker: name}
}

// WorkerPlacement is the server's WorkerPlacement of one worker.
type WorkerPlacement struct {
	manager *Manager
	worker  string
}

// Name names the worker.
func (placement WorkerPlacement) Name() string { return placement.worker }

// Session reports whether a session runs on the worker.
func (placement WorkerPlacement) Session(key string) bool {
	found, placed := placement.manager.options.Placements.Find(key)
	return placed && found.Worker == placement.worker
}
