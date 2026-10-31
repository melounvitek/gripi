package worker

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MaxChannels is how many channels main opens over one master connection. sshd allows 10 sessions per connection
// by default (MaxSessions).
const MaxChannels = 8

// Pool runs the system ssh with connection sharing: every stream is a channel of its own over one of a few master
// connections, which Gripi runs in the foreground and watches.
type Pool struct {
	destination string
	socketDir   string

	mu       sync.Mutex
	masters  []*master
	started  int
	forwards map[Forward]*forward
}

type master struct {
	socket   string
	process  *exec.Cmd
	ended    chan struct{}
	channels int
}

func (master *master) running() bool {
	select {
	case <-master.ended:
		return false
	default:
		return true
	}
}

// Forward makes a port reachable over the master connection, bound to loopback at both ends.
type Forward struct {
	// Remote listens on the worker and connects on main (ssh -R); otherwise it listens on main (ssh -L).
	Remote bool
	// Host is the loopback address at both ends: 127.0.0.1 when empty, or localhost for both 127.0.0.1 and ::1.
	Host string
	// Listen and Connect are the ports.
	Listen, Connect int
}

type forward struct {
	master     *master
	references int
}

// NewPool connects to destination with the user's ssh configuration, keeping the control sockets in socketDir, which
// must be private.
func NewPool(destination, socketDir string) *Pool {
	return &Pool{destination: destination, socketDir: socketDir, forwards: map[Forward]*forward{}}
}

// Command returns ssh that runs remote, a command for the worker user's shell, in a channel of its own. Call release
// once it has ended, to free the channel.
func (pool *Pool) Command(ctx context.Context, remote string) (command *exec.Cmd, release func(), err error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	master, err := pool.master(ctx, func(master *master) bool { return master.channels < MaxChannels })
	if err != nil {
		return nil, nil, err
	}
	master.channels++
	var once sync.Once
	release = func() {
		once.Do(func() {
			pool.mu.Lock()
			defer pool.mu.Unlock()
			master.channels--
		})
	}
	command = exec.Command("ssh", "-S", master.socket, "-o", "ControlMaster=no", "-o", "BatchMode=yes", "-T", "--", pool.destination, remote)
	return command, release, nil
}

// master returns the first working master that fits, or a new one. A channel whose master has gone would open a
// connection of its own, so each master is checked first.
func (pool *Pool) master(ctx context.Context, fits func(*master) bool) (*master, error) {
	pool.masters = slices.DeleteFunc(pool.masters, func(master *master) bool { return !master.running() })
	for _, master := range pool.masters {
		if fits(master) && pool.check(master) == nil {
			return master, nil
		}
	}
	master, err := pool.startMaster(ctx)
	if err != nil {
		return nil, err
	}
	pool.masters = append(pool.masters, master)
	return master, nil
}

func (pool *Pool) check(master *master) error {
	return exec.Command("ssh", "-S", master.socket, "-O", "check", "--", pool.destination).Run()
}

func (pool *Pool) startMaster(ctx context.Context) (*master, error) {
	socket := filepath.Join(pool.socketDir, strconv.Itoa(pool.started))
	pool.started++
	process := exec.Command("ssh", "-MNnT", "-S", socket, "-o", "ControlPersist=no", "-o", "BatchMode=yes",
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "--", pool.destination)
	stderr := &lastLines{}
	process.Stderr = stderr
	if err := process.Start(); err != nil {
		return nil, err
	}
	master := &master{socket: socket, process: process, ended: make(chan struct{})}
	go func() {
		err := process.Wait()
		log.Printf("ssh connection to %s ended: %v %s", pool.destination, err, stderr)
		close(master.ended)
	}()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-master.ended:
			return nil, fmt.Errorf("ssh %s: %s", pool.destination, stderr)
		case <-ctx.Done():
			stop(master)
			return nil, ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(socket); err == nil && pool.check(master) == nil {
				return master, nil
			}
		}
	}
}

// Forward starts a port forward, or shares the one that runs already. Call cancel when it is no longer needed.
func (pool *Pool) Forward(ctx context.Context, port Forward) (cancel func() error, err error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	current := pool.forwards[port]
	if current == nil {
		master, err := pool.master(ctx, func(*master) bool { return true })
		if err != nil {
			return nil, err
		}
		if err := pool.forward(master, "forward", port); err != nil {
			return nil, err
		}
		current = &forward{master: master}
		pool.forwards[port] = current
	}
	current.references++
	var once sync.Once
	return func() (err error) {
		once.Do(func() {
			pool.mu.Lock()
			defer pool.mu.Unlock()
			if current.references--; current.references == 0 {
				delete(pool.forwards, port)
				err = pool.forward(current.master, "cancel", port)
			}
		})
		return err
	}, nil
}

func (pool *Pool) forward(master *master, operation string, port Forward) error {
	direction := "-L"
	if port.Remote {
		direction = "-R"
	}
	host := cmp.Or(port.Host, "127.0.0.1")
	specification := fmt.Sprintf("%s:%d:%s:%d", host, port.Listen, host, port.Connect)
	output, err := exec.Command("ssh", "-S", master.socket, "-O", operation, direction, specification, "--", pool.destination).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh -O %s %s %s: %v %s", operation, direction, specification, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Close ends every master connection, and with them all channels and forwards.
func (pool *Pool) Close() {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for _, master := range pool.masters {
		stop(master)
	}
	pool.masters = nil
}

func stop(master *master) {
	master.process.Process.Signal(syscall.SIGTERM)
	select {
	case <-master.ended:
	case <-time.After(5 * time.Second):
		master.process.Process.Kill()
		<-master.ended
	}
}

// lastLines keeps the last lines written to it, for error messages.
type lastLines struct {
	mu    sync.Mutex
	lines []byte
}

func (last *lastLines) Write(data []byte) (int, error) {
	last.mu.Lock()
	defer last.mu.Unlock()
	last.lines = append(last.lines, data...)
	if excess := len(last.lines) - 2048; excess > 0 {
		last.lines = last.lines[excess:]
	}
	return len(data), nil
}

func (last *lastLines) String() string {
	last.mu.Lock()
	defer last.mu.Unlock()
	return strings.TrimSpace(string(last.lines))
}
