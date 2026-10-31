package worker

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	"github.com/melounvitek/gripi/internal/worker/auth"
	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// State is how far main's connection to a worker has got.
type State string

const (
	Disconnected State = "disconnected"
	Connecting   State = "connecting"
	// Copying means a repository is being copied to the worker.
	Copying   State = "copying"
	Connected State = "connected"
	// Unreachable means that main lost its connection to the worker, whose helper may still run sessions until the
	// worker is gone.
	Unreachable State = "unreachable"
	// Gone means that main lost the worker long enough ago for every session there to have stopped, so the
	// repositories on it can continue on this machine.
	Gone State = "gone"
)

// Status is how main's connection to a worker is.
type Status struct {
	State State `json:"state"`
	// GoneAt is when a worker that main lost is gone, unless main has reached it again since.
	GoneAt time.Time `json:"gone_at,omitzero"`
}

// connectTimeout bounds connecting to a worker, which includes copying Pi there the first time.
const connectTimeout = 5 * time.Minute

// reconnectInterval is how often main tries to connect to a worker whose earlier helper may still run, and refuses
// another.
const reconnectInterval = 2 * time.Second

// ManagerOptions configure NewManager.
type ManagerOptions struct {
	// Destination returns where ssh connects for the worker of a name, as `gripi worker add` saved it.
	Destination func(name string) (string, error)
	// Placements records which repositories are on which worker.
	Placements *Placements
	// StateDir is Gripi's state folder, which holds ssh's control sockets and main's side of each repository's mirror.
	// It must be on the file system of the repositories.
	StateDir string
	// Pi is main's Pi, which every worker gets a copy of. Its ExtensionDir holds WorkerExtension.
	Pi Pi
	// WorkerExtension is the path of Gripi's worker extension, which the Pi of every session on a worker loads.
	WorkerExtension string
	// SessionsRoot holds Pi's session folders, one for each folder that sessions started in.
	SessionsRoot string
	// SocketPath is the path of the gripi socket, where gripi commands on a worker reach the worker's own socket.
	SocketPath string
	// Store finds the folders of saved sessions.
	Store agent.Store
	// ServeSocket serves the gripi commands of a worker, which reach what placement says is on the worker, on a
	// socket of its own.
	ServeSocket func(name string, placement WorkerPlacement) (socket string, stop func() error, err error)
	// Connection holds Connect's options but Destination and SocketDir, which the manager sets for each worker.
	Connection Options
	// Clock is time.Now when nil.
	Clock func() time.Time
}

// Manager runs the sessions of repositories placed on workers there: it connects to a worker when a session needs
// it, mirrors each placed repository and its sessions' folders to main, and moves repositories between main and
// workers.
type Manager struct {
	options ManagerOptions
	ctx     context.Context
	cancel  context.CancelFunc
	started time.Time

	mu          sync.Mutex
	workers     map[string]*remote
	movingUnits []mirror.Unit       // units on their way to or from a worker, whose sessions can't start meanwhile
	sessions    map[string]launched // by the session's ID on its worker
	launches    []string            // folders of sessions whose Pi is starting
	launched    *sync.Cond          // signalled when a launch ends
	notices     []Notice
}

// Notice says that files of a repository on a worker, or of its sessions, changed on this machine, which keeps a
// read-only copy of them: before the worker's versions replaced them, this machine's were saved in Aside, at their
// paths.
type Notice struct {
	Repository mirror.Unit `json:"repository"`
	Aside      string      `json:"aside"`
	Paths      []string    `json:"paths"`
	Time       time.Time   `json:"time"`
}

// remote is main's side of one worker. The manager's mutex guards it, except connecting.
type remote struct {
	name         string
	connecting   sync.Mutex // held while connecting, so that others wait for the connection
	state        State
	goneAt       time.Time // when the worker is gone, after main lost its connection
	conn         *Conn
	installation *Installation
	stopSocket   func() error
	mirrors      map[string]*liveMirror // by the unit's main folder
}

// launched is a session whose Pi the manager started on a worker.
type launched struct {
	worker, cwd string
	lost        func(message string) // agent.Process.Lost
}

// liveMirror is main's copy of a unit, kept up to date by the unit's mirror on the worker.
type liveMirror struct {
	unit        mirror.Unit
	destination *mirror.Destination
	cancel      context.CancelFunc
	done        chan struct{}
}

func (live *liveMirror) running() bool {
	select {
	case <-live.done:
		return false
	default:
		return true
	}
}

// NewManager returns a manager that connects to no worker until a session or a move needs it.
func NewManager(options ManagerOptions) *Manager {
	if options.Clock == nil {
		options.Clock = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{options: options, ctx: ctx, cancel: cancel, started: options.Clock(), workers: map[string]*remote{}, sessions: map[string]launched{}}
	manager.launched = sync.NewCond(&manager.mu)
	return manager
}

// Notices returns the notices of the repository that holds folder, oldest first, until DismissNotices.
func (manager *Manager) Notices(folder string) []Notice {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var notices []Notice
	for _, notice := range manager.notices {
		if notice.Repository.Contains(folder) {
			notices = append(notices, notice)
		}
	}
	return notices
}

// DismissNotices forgets the notices of the repository that holds folder. The files saved aside stay.
func (manager *Manager) DismissNotices(folder string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.notices = slices.DeleteFunc(manager.notices, func(notice Notice) bool { return notice.Repository.Contains(folder) })
}

// Status reports the connection to the worker of a name.
func (manager *Manager) Status(name string) Status {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	worker := manager.workers[name]
	if worker == nil {
		return Status{State: Disconnected}
	}
	status := Status{State: worker.state, GoneAt: worker.goneAt}
	if worker.state == Disconnected && !worker.goneAt.IsZero() {
		status.State = Gone
		if manager.options.Clock().Before(worker.goneAt) {
			status.State = Unreachable
		}
	}
	return status
}

func (manager *Manager) setState(worker *remote, state State) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	worker.state = state
}

// connect returns the worker of a name, connected and with main's Pi, and with the mirrors of every unit placed on it
// running, but those on their way back to main. It connects when the worker is not connected yet or lost its
// connection.
func (manager *Manager) connect(ctx context.Context, name string) (*remote, error) {
	manager.mu.Lock()
	worker := manager.workers[name]
	if worker == nil {
		worker = &remote{name: name, state: Disconnected, mirrors: map[string]*liveMirror{}}
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
		if err := manager.dial(ctx, worker); err != nil {
			return nil, fmt.Errorf("connect to worker %s: %w", name, err)
		}
	}
	for _, placement := range manager.options.Placements.All() {
		manager.mu.Lock()
		moving := slices.ContainsFunc(manager.movingUnits, func(unit mirror.Unit) bool { return unit.Main == placement.Unit.Main })
		manager.mu.Unlock()
		if placement.Worker == name && !moving {
			if err := manager.runMirrors(ctx, worker, placement.Unit); err != nil {
				log.Printf("mirror %s from worker %s: %v", placement.Unit.Main, name, err)
			}
		}
	}
	return worker, nil
}

// runMirrors makes sure that the mirrors of a placed unit run: the unit's, and that of its sessions' folders, with
// every folder that its sessions need.
func (manager *Manager) runMirrors(ctx context.Context, worker *remote, unit mirror.Unit) error {
	manager.mu.Lock()
	live, sessions := worker.mirrors[unit.Main], worker.mirrors[manager.sessionsMain(unit)]
	manager.mu.Unlock()
	if live == nil || !live.running() {
		if err := manager.startMirror(ctx, worker, unit, false); err != nil {
			return err
		}
	}
	if sessions != nil && sessions.running() {
		if !slices.ContainsFunc(manager.sessionFolders(unit), func(folder string) bool { return !sessions.unit.Contains(folder) }) {
			return nil
		}
		// The worker's helper watches only the folders that its mirror started with.
		if err := manager.stopMirror(ctx, worker, sessions.unit.Main); err != nil {
			return err
		}
	}
	return manager.startMirror(ctx, worker, unit, true)
}

// dial connects to a worker, gives it main's Pi and lets its gripi commands reach the gateway.
func (manager *Manager) dial(ctx context.Context, worker *remote) error {
	manager.setState(worker, Connecting)
	options := manager.options.Connection
	var err error
	if options.Destination, err = manager.options.Destination(worker.name); err != nil {
		manager.setState(worker, Disconnected)
		return err
	}
	options.SocketDir = filepath.Join(manager.options.StateDir, "ssh")
	var conn *Conn
	for {
		if conn, err = Connect(ctx, options); err == nil {
			break
		}
		manager.mu.Lock()
		goneAt := worker.goneAt
		manager.mu.Unlock()
		// The helper of a lost connection refuses another until it has stopped, which it has once the worker is gone.
		if !manager.options.Clock().Before(goneAt) {
			manager.setState(worker, Disconnected)
			return err
		}
		select {
		case <-ctx.Done():
			manager.setState(worker, Disconnected)
			return fmt.Errorf("%w; worker %s may still run the sessions of a lost connection until %s", err, worker.name, goneAt.Local().Format(time.TimeOnly))
		case <-time.After(reconnectInterval):
		}
	}
	fail := func(err error) error {
		conn.Close()
		manager.setState(worker, Disconnected)
		return err
	}
	installation, err := conn.Bootstrap(ctx, manager.options.Pi)
	if err != nil {
		return fail(err)
	}
	socket, stopSocket, err := manager.options.ServeSocket(worker.name, manager.Placement(worker.name))
	if err != nil {
		return fail(err)
	}
	if _, err := conn.Forward(ctx, Forward{Remote: true, ListenSocket: manager.options.SocketPath, ConnectSocket: socket}); err != nil {
		stopSocket()
		return fail(err)
	}
	manager.mu.Lock()
	// The new helper runs, so the helper of a lost connection has stopped.
	worker.conn, worker.installation, worker.stopSocket, worker.state, worker.goneAt = conn, installation, stopSocket, Connected, time.Time{}
	manager.mu.Unlock()
	go func() {
		<-conn.Lost()
		manager.disconnect(worker, conn, true)
	}()
	return nil
}

// Reconnect connects to the worker of a name, if main is not connected to it, as the next session there or move to it
// would. After main has lost the worker, it waits while the worker's earlier helper may still run.
func (manager *Manager) Reconnect(ctx context.Context, name string) error {
	_, err := manager.connect(ctx, name)
	return err
}

// disconnect forgets a worker's connection and closes it, unless the worker has another one by now. After main lost the
// connection, the worker is unreachable until it is gone, and the steps that its sessions were running end with an
// outcome that nobody knows. The next session there or move to it connects again.
func (manager *Manager) disconnect(worker *remote, conn *Conn, lost bool) {
	manager.mu.Lock()
	if worker.conn != conn {
		manager.mu.Unlock()
		return
	}
	mirrors, stopSocket := worker.mirrors, worker.stopSocket
	worker.conn, worker.installation, worker.stopSocket, worker.mirrors, worker.state = nil, nil, nil, map[string]*liveMirror{}, Disconnected
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
	// Ending the master connections at once ends every channel, also over a link that hangs.
	conn.Pool.Close()
	for _, session := range sessions {
		if lost && session.lost != nil {
			session.lost(fmt.Sprintf("Lost the connection to worker %s. The outcome of the running step is unknown.", worker.name))
		}
	}
	// Main's copies are as their last barriers left them once their mirrors have ended.
	for _, live := range mirrors {
		live.cancel()
		<-live.done
	}
	stopSocket()
	conn.Close()
}

// Close stops every worker's mirrors after a last barrier, and closes the connections, which stops every session on
// the workers. Call it once the sessions' Pi have stopped.
func (manager *Manager) Close() {
	manager.mu.Lock()
	workers := make([]*remote, 0, len(manager.workers))
	for _, worker := range manager.workers {
		workers = append(workers, worker)
	}
	manager.mu.Unlock()
	for _, worker := range workers {
		manager.mu.Lock()
		conn, mains := worker.conn, slices.Collect(maps.Keys(worker.mirrors))
		manager.mu.Unlock()
		if conn == nil {
			continue
		}
		for _, main := range mains {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := manager.stopMirror(ctx, worker, main); err != nil {
				log.Printf("stop the mirror of %s from worker %s: %v", main, worker.name, err)
			}
			cancel()
		}
		manager.disconnect(worker, conn, false)
	}
	manager.cancel()
}

// connection returns a worker's connection, or fails when the worker has disconnected.
func (manager *Manager) connection(worker *remote) (*Conn, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if worker.conn == nil {
		return nil, fmt.Errorf("worker %s disconnected", worker.name)
	}
	return worker.conn, nil
}

// mirrorFolder holds main's state of the mirror of the unit whose main folder is main, and what main saved aside.
func (manager *Manager) mirrorFolder(main string) string {
	hash := sha256.Sum256([]byte(main))
	return filepath.Join(manager.options.StateDir, "mirrors", hex.EncodeToString(hash[:8]))
}

// sessionsMain names the mirror of a unit's sessions' folders: by the folder of the sessions in the unit's main folder,
// which comes first.
func (manager *Manager) sessionsMain(unit mirror.Unit) string {
	return sessionFolder(manager.options.SessionsRoot, unit.Main)
}

// startMirror starts the mirror of a placed unit on the worker, or with sessions, the mirror of the unit's sessions'
// folders, and connects main's copy to it.
func (manager *Manager) startMirror(ctx context.Context, worker *remote, repository mirror.Unit, sessions bool) error {
	conn, err := manager.connection(worker)
	if err != nil {
		return err
	}
	unit := repository
	if sessions {
		folders := manager.sessionFolders(repository)
		unit = mirror.Unit{Main: folders[0], Worktrees: folders[1:]}
	}
	folder := manager.mirrorFolder(unit.Main)
	destination, err := mirror.NewDestination(filepath.Join(folder, "state"))
	if err != nil {
		return err
	}
	if sessions {
		// Main's copy keeps every folder that it had, whose sessions would go if the worker sent no folder of them.
		for _, root := range destination.Checkpoint().Roots {
			if !unit.Contains(root) {
				unit.Worktrees = append(unit.Worktrees, root)
			}
		}
	}
	destination.Applied = manager.applied(repository, sessions)
	if err := conn.StartMirror(ctx, unit, sessions); err != nil {
		return err
	}
	command, release, err := conn.AttachMirror(ctx, unit.Main)
	if err != nil {
		return err
	}
	input, err := command.StdinPipe()
	if err != nil {
		release()
		return err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		release()
		return err
	}
	stderr := &lastLines{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		release()
		return err
	}
	mirrorCtx, cancel := context.WithCancel(manager.ctx)
	live := &liveMirror{unit: unit, destination: destination, cancel: cancel, done: make(chan struct{})}
	manager.mu.Lock()
	worker.mirrors[unit.Main] = live
	manager.mu.Unlock()
	go func() {
		defer close(live.done)
		aside := filepath.Join(folder, "aside", time.Now().UTC().Format("20060102T150405.000000000Z"))
		err := destination.Serve(mirrorCtx, struct {
			io.Reader
			io.WriteCloser
		}{output, input}, aside)
		command.Wait()
		release()
		if err != nil {
			log.Printf("mirror of %s from worker %s: %v %s", unit.Main, worker.name, err, stderr)
		}
	}()
	return nil
}

// applied takes what main's copy of a placed unit, or with sessions of the unit's sessions' folders, applied: files
// that changed on main make a notice, and the unit's folders, which Git on the worker may have changed, are its
// placement's from then on.
func (manager *Manager) applied(repository mirror.Unit, sessions bool) func(mirror.Checkpoint, string, []string) {
	return func(checkpoint mirror.Checkpoint, aside string, saved []string) {
		placement, placed := manager.options.Placements.Find(repository.Main)
		placed = placed && placement.Unit.Main == repository.Main
		if placed && !sessions {
			worktrees := slices.DeleteFunc(slices.Clone(checkpoint.Roots), func(root string) bool {
				return root == placement.Unit.Main || root == placement.Unit.GitDir
			})
			if !slices.Equal(slices.Sorted(slices.Values(worktrees)), slices.Sorted(slices.Values(placement.Unit.Worktrees))) {
				placement.Unit.Worktrees = worktrees
				if err := manager.options.Placements.Update(placement); err != nil {
					log.Printf("note the worktrees of %s on worker %s: %v", repository.Main, placement.Worker, err)
				}
			}
		}
		if len(saved) == 0 {
			return
		}
		unit := repository
		if placed {
			unit = placement.Unit
		}
		log.Printf("%d files of %s changed on this machine, which keeps a read-only copy; this machine's versions are in %s", len(saved), unit.Main, aside)
		manager.mu.Lock()
		defer manager.mu.Unlock()
		manager.notices = append(manager.notices, Notice{Repository: unit, Aside: aside, Paths: saved, Time: time.Now()})
	}
}

// waitForMirror returns once main's copy of a unit has taken a barrier that started after since.
func waitForMirror(ctx context.Context, live *liveMirror, since time.Time) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !live.destination.Checkpoint().Time.After(since) {
		select {
		case <-live.done:
			return errors.New("the mirror stopped before main had the repository")
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

// stopMirror takes a last barrier of a unit's mirror, so that main has every change, and stops the mirror.
func (manager *Manager) stopMirror(ctx context.Context, worker *remote, main string) error {
	conn, err := manager.connection(worker)
	if err != nil {
		return err
	}
	manager.mu.Lock()
	live := worker.mirrors[main]
	manager.mu.Unlock()
	if live == nil || !live.running() {
		return fmt.Errorf("%s is not mirrored from worker %s, so main may lack its latest changes", main, worker.name)
	}
	if err := conn.StopMirror(ctx, main); err != nil {
		return err
	}
	select {
	case <-live.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	manager.mu.Lock()
	if worker.mirrors[main] == live {
		delete(worker.mirrors, main)
	}
	manager.mu.Unlock()
	return nil
}

// Launch runs the Pi of a session in a repository placed on a worker on that worker, and leaves other sessions'
// Pi here. It is the Pi adapter's launcher.
func (manager *Manager) Launch(process agent.Process) (*exec.Cmd, func(), error) {
	cwd := process.Dir
	if cwd == "" && process.Key != "" {
		if session, found := manager.options.Store.Session(process.Key); found {
			cwd = session.CWD
		}
	}
	if cwd == "" {
		return nil, nil, nil
	}
	done, err := manager.startLaunch(cwd)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	placement, placed := manager.options.Placements.Find(cwd)
	if !placed {
		return nil, nil, nil
	}
	ctx, cancel := context.WithTimeout(manager.ctx, connectTimeout)
	defer cancel()
	worker, err := manager.connect(ctx, placement.Worker)
	if err != nil {
		return nil, nil, err
	}
	conn, err := manager.connection(worker)
	if err != nil {
		return nil, nil, err
	}
	manager.mu.Lock()
	installation := worker.installation
	manager.mu.Unlock()
	// The worker's Pi gets main's logins before it starts, without refresh tokens.
	logins, _, err := auth.Pi{Command: manager.options.Pi.Command, AgentDir: manager.options.Pi.AgentDir}.Build(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := conn.ApplyAuth(ctx, filepath.Join(manager.options.Pi.AgentDir, "auth.json"), string(logins)); err != nil {
		return nil, nil, err
	}
	session := Session{
		ID: sessionID(process.Key), Cwd: cwd,
		Args: installation.Command(append(slices.Clone(process.Command), "--extension", manager.options.WorkerExtension)),
		// gripi commands find the worker's socket where main's is.
		Env: append(installation.Environment(process.Env), "GRIPI_SOCKET_PATH="+manager.options.SocketPath),
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
	manager.sessions[session.ID] = launched{worker: placement.Worker, cwd: cwd, lost: process.Lost}
	manager.mu.Unlock()
	return command, release, nil
}

// sessionID names a session on its worker, the same for every Pi of the session, so they share its cgroup. A new
// session has no key until its Pi names it, so its first Pi gets an ID of its own.
func sessionID(key string) string {
	if key == "" {
		random := make([]byte, 16)
		rand.Read(random)
		return hex.EncodeToString(random)
	}
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:16])
}

// startLaunch notes that the Pi of a session in cwd is starting, which a move of cwd's repository waits for, unless
// the repository is moving already.
func (manager *Manager) startLaunch(cwd string) (done func(), err error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if slices.ContainsFunc(manager.movingUnits, func(unit mirror.Unit) bool { return unit.Contains(cwd) }) {
		return nil, fmt.Errorf("the repository of %s is moving between this machine and a worker; try again once it has moved", cwd)
	}
	manager.launches = append(manager.launches, cwd)
	return func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		i := slices.Index(manager.launches, cwd)
		manager.launches = slices.Delete(manager.launches, i, i+1)
		manager.launched.Broadcast()
	}, nil
}

// startMoving marks a unit as moving, unless it is moving already, and waits until the sessions starting in the unit
// have started: the Pi of a session that started on a worker while its repository went back would stay there.
func (manager *Manager) startMoving(unit mirror.Unit) (done func(), err error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if slices.ContainsFunc(manager.movingUnits, func(moving mirror.Unit) bool { return moving.Contains(unit.Main) || unit.Contains(moving.Main) }) {
		return nil, fmt.Errorf("the repository of %s is moving already", unit.Main)
	}
	manager.movingUnits = append(manager.movingUnits, unit)
	for slices.ContainsFunc(manager.launches, unit.Contains) {
		manager.launched.Wait()
	}
	return func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		manager.movingUnits = slices.DeleteFunc(manager.movingUnits, func(moving mirror.Unit) bool { return moving.Main == unit.Main })
	}, nil
}

// Move moves the repository that holds folder, with all its worktrees and sessions, to the worker of a name: main
// copies it there with the sessions' folders, and the worker's mirror keeps main's copy up to date from then on.
// stop gets the repository's test for a folder before anything is copied, and must stop the Pi of every session
// there, or fail when one is working.
func (manager *Manager) Move(ctx context.Context, folder, name string, stop func(inRepository func(folder string) bool) error) error {
	if _, err := manager.options.Destination(name); err != nil {
		return err
	}
	if placement, placed := manager.options.Placements.Find(folder); placed {
		return fmt.Errorf("%s is on worker %s already", placement.Unit.Main, placement.Worker)
	}
	unit, err := mirror.Discover(folder)
	if err != nil {
		return err
	}
	done, err := manager.startMoving(unit)
	if err != nil {
		return err
	}
	defer done()
	if err := stop(unit.Contains); err != nil {
		return err
	}
	worker, err := manager.connect(ctx, name)
	if err != nil {
		return err
	}
	manager.setState(worker, Copying)
	err = manager.copyToWorker(ctx, worker, unit)
	if err == nil {
		err = manager.options.Placements.Add(Placement{Worker: name, Unit: unit})
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if worker.conn != nil {
		worker.state = Connected
	}
	// Mirrors of a unit that stays here would keep sending what nobody runs there.
	for _, main := range []string{unit.Main, manager.sessionsMain(unit)} {
		if live := worker.mirrors[main]; err != nil && live != nil {
			live.cancel()
			delete(worker.mirrors, main)
		}
	}
	return err
}

// copyToWorker copies a unit and its sessions' folders to the worker, then starts their mirrors and waits until
// main's copies have their first barriers.
func (manager *Manager) copyToWorker(ctx context.Context, worker *remote, unit mirror.Unit) error {
	conn, err := manager.connection(worker)
	if err != nil {
		return err
	}
	if err := conn.CopyUnit(ctx, unit); err != nil {
		return fmt.Errorf("copy %s to worker %s: %w", unit.Main, worker.name, err)
	}
	for _, folder := range manager.sessionFolders(unit) {
		if _, err := os.Stat(folder); err != nil {
			continue
		}
		if err := conn.copyFolder(ctx, folder, folder, nil); err != nil {
			return fmt.Errorf("copy %s to worker %s: %w", folder, worker.name, err)
		}
	}
	started := time.Now()
	for _, sessions := range []bool{false, true} {
		main := unit.Main
		if sessions {
			main = manager.sessionsMain(unit)
		}
		// Main's own changes before the move are not changes to a read-only copy.
		if err := os.RemoveAll(filepath.Join(manager.mirrorFolder(main), "state")); err != nil {
			return err
		}
		if err := manager.startMirror(ctx, worker, unit, sessions); err != nil {
			return err
		}
		manager.mu.Lock()
		live := worker.mirrors[main]
		manager.mu.Unlock()
		if err := waitForMirror(ctx, live, started); err != nil {
			return err
		}
	}
	return nil
}

// Return brings the repository that holds folder back from its worker: it stops everything that the repository's
// sessions run there, and takes the last changes of the repository and of its sessions. stop is as in Move. When main
// has lost the worker, or cannot reach it, the repository continues on this machine as main's copies have it, once the
// worker must have stopped its sessions.
func (manager *Manager) Return(ctx context.Context, folder string, stop func(inRepository func(folder string) bool) error) error {
	placement, placed := manager.options.Placements.Find(folder)
	if !placed {
		return fmt.Errorf("%s is not on a worker", folder)
	}
	unit := placement.Unit
	done, err := manager.startMoving(unit)
	if err != nil {
		return err
	}
	defer done()
	if err := stop(unit.Contains); err != nil {
		return err
	}
	if state := manager.Status(placement.Worker).State; state == Unreachable || state == Gone {
		return manager.continueOnMain(placement, fmt.Errorf("worker %s is unreachable", placement.Worker))
	}
	worker, err := manager.connect(ctx, placement.Worker)
	if err != nil {
		return manager.continueOnMain(placement, err)
	}
	// connect leaves the mirrors of a moving unit alone, so that they don't start again during the return.
	if err := manager.runMirrors(ctx, worker, unit); err != nil {
		return err
	}
	conn, err := manager.connection(worker)
	if err != nil {
		return err
	}
	manager.mu.Lock()
	var ids []string
	for id, session := range manager.sessions {
		if session.worker == worker.name && unit.Contains(session.cwd) {
			ids = append(ids, id)
		}
	}
	manager.mu.Unlock()
	// Nothing that the sessions started may change the repository after the last barrier.
	for _, id := range ids {
		if err := conn.ReleaseSession(ctx, id); err != nil {
			return err
		}
		manager.mu.Lock()
		delete(manager.sessions, id)
		manager.mu.Unlock()
	}
	mains := []string{unit.Main, manager.sessionsMain(unit)}
	for _, main := range mains {
		if err := manager.stopMirror(ctx, worker, main); err != nil {
			return err
		}
	}
	if err := manager.options.Placements.Remove(unit.Main); err != nil {
		return err
	}
	for _, main := range mains {
		if err := os.RemoveAll(filepath.Join(manager.mirrorFolder(main), "state")); err != nil {
			return err
		}
	}
	return nil
}

// continueOnMain brings back the repository of a placement from a worker that main lost, or cannot reach, without the
// worker: main's copies of the repository and of its sessions' folders are as the last barriers left them, which is
// consistent. It refuses with unreachable while the worker may still run the repository's sessions, whose Pi must
// never run again.
func (manager *Manager) continueOnMain(placement Placement, unreachable error) error {
	unit := placement.Unit
	if goneAt, now := manager.goneAt(placement.Worker), manager.options.Clock(); now.Before(goneAt) {
		return fmt.Errorf("%w; it may still run the sessions of %s until %s, so they can continue on this machine from then (in %s)",
			unreachable, unit.Main, goneAt.Local().Format(time.TimeOnly), goneAt.Sub(now).Round(time.Second))
	}
	mains := []string{unit.Main, manager.sessionsMain(unit)}
	for _, main := range mains {
		// Opening main's copy finishes a barrier that a crash interrupted.
		if _, err := mirror.NewDestination(filepath.Join(manager.mirrorFolder(main), "state")); err != nil {
			return err
		}
	}
	if err := manager.options.Placements.Remove(unit.Main); err != nil {
		return err
	}
	for _, main := range mains {
		if err := os.RemoveAll(filepath.Join(manager.mirrorFolder(main), "state")); err != nil {
			return err
		}
	}
	log.Printf("%s continues on this machine without worker %s, as of the last changes that this machine got", unit.Main, placement.Worker)
	return nil
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

// sessionFolders are Pi's folders of the sessions in a unit, the main folder's first: one for each of its worktrees,
// and one for each other folder in it that a session started in or is starting in.
func (manager *Manager) sessionFolders(unit mirror.Unit) []string {
	var folders []string
	add := func(cwd string) {
		if folder := sessionFolder(manager.options.SessionsRoot, cwd); !slices.Contains(folders, folder) {
			folders = append(folders, folder)
		}
	}
	for _, root := range append([]string{unit.Main}, unit.Worktrees...) {
		add(root)
	}
	if sessions, err := manager.options.Store.Sessions(); err == nil {
		for _, session := range sessions {
			if unit.Contains(session.CWD) {
				add(session.CWD)
			}
		}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, session := range manager.sessions {
		if unit.Contains(session.cwd) {
			add(session.cwd)
		}
	}
	for _, cwd := range manager.launches {
		if unit.Contains(cwd) {
			add(cwd)
		}
	}
	return folders
}

// sessionFolder is where Pi keeps the sessions started in cwd, as Pi names it.
func sessionFolder(root, cwd string) string {
	return filepath.Join(root, "--"+strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(strings.TrimPrefix(cwd, "/"))+"--")
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

// Folder reports whether sessions started in a folder run on the worker.
func (placement WorkerPlacement) Folder(path string) bool {
	found, placed := placement.manager.options.Placements.Find(path)
	return placed && found.Worker == placement.worker
}

// Session reports whether a session runs on the worker. A new session has no file on main yet, but its key is a
// path in the session folder of the folder that it started in.
func (placement WorkerPlacement) Session(key string) bool {
	if session, found := placement.manager.options.Store.Session(key); found {
		return placement.Folder(session.CWD)
	}
	folder := filepath.Dir(key)
	for _, found := range placement.manager.options.Placements.All() {
		if found.Worker != placement.worker {
			continue
		}
		for _, root := range append([]string{found.Unit.Main}, found.Unit.Worktrees...) {
			if sessionFolder(placement.manager.options.SessionsRoot, root) == folder {
				return true
			}
		}
	}
	placement.manager.mu.Lock()
	defer placement.manager.mu.Unlock()
	for _, session := range placement.manager.sessions {
		if session.worker == placement.worker && sessionFolder(placement.manager.options.SessionsRoot, session.cwd) == folder {
			return true
		}
	}
	return false
}
