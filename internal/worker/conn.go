package worker

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/worker/helper"
)

// DefaultInstallDir is where Gripi keeps its files on a worker, relative to the worker's home.
const DefaultInstallDir = ".local/share/gripi/worker"

// DefaultUnit is the systemd user unit that the helper runs as.
const DefaultUnit = "gripi-worker"

// Options configure Connect.
type Options struct {
	// Destination is where ssh connects, as `gripi worker add` saved it.
	Destination string
	// SocketDir holds ssh's control sockets. It must be a private folder under Gripi's state folder.
	SocketDir string
	// Executable is the Gripi executable that the worker runs as its helper; os.Executable() when empty.
	Executable string
	// InstallDir is Gripi's folder on the worker, relative to the worker's home unless it is absolute;
	// DefaultInstallDir when empty.
	InstallDir string
	// Unit is DefaultUnit when empty.
	Unit string
	// RuntimeDir is the helper's folder for its sockets; the helper's default, $XDG_RUNTIME_DIR/gripi-worker, when
	// empty.
	RuntimeDir string
	// Lease is helper.DefaultLease when zero.
	Lease time.Duration
	// Home is main's home folder. When the worker user's home folder has another path, what runs on the worker with
	// main's paths, such as the helper and the copies, runs in a private view in which Home shows the worker user's
	// home folder (package view). Without it, they see the worker's files as they are.
	Home string
}

// Conn is main's connection to a worker: ssh channels and forwards from its Pool, and the helper's Control.
type Conn struct {
	*Pool
	*Control
	options    Options
	socketDir  string
	installDir string // absolute, on the worker
	home       string // the worker user's home folder, outside the view
	serving    chan struct{}
}

// Connect connects to a worker, replaces its helper if the helper differs from this Gripi, and starts the helper.
func Connect(ctx context.Context, options Options) (*Conn, error) {
	options.InstallDir = cmp.Or(options.InstallDir, DefaultInstallDir)
	options.Unit = cmp.Or(options.Unit, DefaultUnit)
	options.Lease = cmp.Or(options.Lease, helper.DefaultLease)
	if options.Executable == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		options.Executable = executable
	}
	version, err := HelperVersion(options.Executable)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(options.SocketDir, 0700); err != nil {
		return nil, err
	}
	// Each connection gets a folder of its own, because ssh can't reuse the socket of a master that crashed.
	socketDir, err := os.MkdirTemp(options.SocketDir, "")
	if err != nil {
		return nil, err
	}
	conn := &Conn{Pool: NewPool(options.Destination, socketDir), options: options, socketDir: socketDir, serving: make(chan struct{})}
	if err := conn.start(ctx, version); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (conn *Conn) start(ctx context.Context, version string) error {
	var err error
	if conn.installDir, conn.home, err = installHelper(ctx, conn.Pool, conn.options.Executable, version, conn.options.InstallDir); err != nil {
		return err
	}
	// In the view, the helper is not the unit's main process, so NotifyAccess=all lets it feed systemd's watchdog.
	serve := []string{"systemd-run", "--user", "--pipe", "--quiet", "--collect", "--unit", conn.options.Unit,
		"-p", "Delegate=yes", "-p", "KillMode=control-group", "-p", "WatchdogSec=10s", "-p", "WatchdogSignal=SIGKILL", "-p", "NotifyAccess=all",
		conn.installDir + "/bin/gripi", "worker", "serve", "--lease", conn.options.Lease.String()}
	if conn.options.RuntimeDir != "" {
		serve = append(serve, "--runtime-dir", conn.options.RuntimeDir)
	}
	if conn.options.Home != "" {
		serve = append(serve, "--home", conn.options.Home)
	}
	command, release, err := conn.Command(ctx, ShellQuote(serve...))
	if err != nil {
		return err
	}
	input, err := command.StdinPipe()
	if err != nil {
		release()
		return err
	}
	// exec's StdoutPipe could close before Control has read the helper's last words; this pipe ends after them.
	output, outputWriter := io.Pipe()
	stderr := &helperLog{destination: conn.options.Destination}
	command.Stdout, command.Stderr = outputWriter, stderr
	if err := command.Start(); err != nil {
		release()
		return err
	}
	go func() {
		command.Wait()
		outputWriter.Close()
		release()
		close(conn.serving)
	}()
	if conn.Control, err = NewControl(ctx, output, input, version, conn.options.Lease); err != nil {
		input.Close()
		return fmt.Errorf("start the helper on %s: %w: %s", conn.options.Destination, err, stderr.lastLines.String())
	}
	return nil
}

// Attach returns ssh that passes the RPC of a session's Pi through, to run instead of Pi. Call release once it has
// ended. When its input ends, Pi's input ends.
func (conn *Conn) Attach(ctx context.Context, session string) (command *exec.Cmd, release func(), err error) {
	return conn.stream(ctx, "attach", session)
}

// AttachMirror returns ssh that connects main's copy of a unit to the helper's mirror of it, which StartMirror
// prepared, for a mirror.Destination. Call release once it has ended.
func (conn *Conn) AttachMirror(ctx context.Context, main string) (command *exec.Cmd, release func(), err error) {
	return conn.stream(ctx, "mirror", main)
}

// stream returns ssh that runs `gripi worker <subcommand> <name> <instance>` on the worker.
func (conn *Conn) stream(ctx context.Context, subcommand, name string) (*exec.Cmd, func(), error) {
	words := []string{conn.installDir + "/bin/gripi", "worker", subcommand}
	if conn.options.RuntimeDir != "" {
		words = append(words, "--runtime-dir", conn.options.RuntimeDir)
	}
	return conn.Command(ctx, ShellQuote(append(words, name, conn.Instance())...))
}

// inView returns the words that run a command on the worker in the view of main's home folder.
func (conn *Conn) inView(words ...string) []string {
	return viewWords(conn.installDir, conn.options.Home, words...)
}

func viewWords(installDir, home string, words ...string) []string {
	if home == "" {
		return words
	}
	return append([]string{installDir + "/bin/gripi", "worker", "enter", "--home", home, "--"}, words...)
}

// runInView runs a sh script on the worker in the view of main's home folder, and returns its output.
func (conn *Conn) runInView(ctx context.Context, script string, arguments ...string) (string, error) {
	return runWords(ctx, conn.Pool, nil, conn.inView(append([]string{"sh", "-c", script, "sh"}, arguments...)...)...)
}

// workerPath returns where a path of main's is on the worker for what runs outside the view, such as sshd: in the
// worker user's home folder when it is in main's.
func (conn *Conn) workerPath(path string) string {
	if home := conn.options.Home; home != "" && (path == home || strings.HasPrefix(path, home+"/")) {
		return conn.home + strings.TrimPrefix(path, home)
	}
	return path
}

// Close stops the helper, which kills every session's processes, and closes the connection.
func (conn *Conn) Close() error {
	if conn.Control != nil {
		conn.Control.Close()
		select {
		case <-conn.serving:
		case <-time.After(5 * time.Second):
		}
	}
	conn.Pool.Close()
	return os.RemoveAll(conn.socketDir)
}

// helperLog logs what the helper writes to standard error, a line at a time, and keeps the end for error messages.
type helperLog struct {
	destination string
	lastLines
	mu      sync.Mutex
	partial []byte
}

func (stderr *helperLog) Write(data []byte) (int, error) {
	stderr.lastLines.Write(data)
	stderr.mu.Lock()
	defer stderr.mu.Unlock()
	stderr.partial = append(stderr.partial, data...)
	for {
		line, rest, found := bytes.Cut(stderr.partial, []byte("\n"))
		if !found {
			return len(data), nil
		}
		log.Printf("worker %s: %s", stderr.destination, line)
		stderr.partial = rest
	}
}
