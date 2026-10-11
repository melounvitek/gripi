// Package hermes runs Hermes Agent sessions through its Agent Client Protocol (ACP) server, `hermes acp`.
package hermes

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

// protocolVersion is the ACP version that Gripi speaks.
const protocolVersion = 1

// connection speaks ACP, JSON-RPC 2.0 with one message per line, to a `hermes acp` process.
type connection struct {
	process *exec.Cmd
	stdin   io.WriteCloser
	writing sync.Mutex
	// grace is how long Close lets Hermes end its sessions after its input ends, before it stops the process group.
	grace     time.Duration
	closeOnce sync.Once

	mu      sync.Mutex
	nextID  int64
	waiting map[int64]chan message
	stderr  tail

	// exited closes when the process has exited and its output has been read. exitErr says why, for requests that
	// were still waiting.
	exited  chan struct{}
	exitErr error
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (err *rpcError) Error() string {
	return fmt.Sprintf("Hermes answered: %s (%d)", err.Message, err.Code)
}

// environment is the gateway's environment for Hermes: without the gateway's settings, and without a session source
// that would file the sessions of Gripi under another of Hermes's platforms, which ACP neither lists nor loads.
func environment(gateway []string) []string {
	return slices.DeleteFunc(agent.ScrubbedEnvironment(gateway), func(entry string) bool {
		return strings.HasPrefix(entry, "HERMES_SESSION_SOURCE=")
	})
}

func start(path string, env []string) (*connection, error) {
	process := exec.Command(path, "acp")
	process.Env = env
	// Hermes runs tools and MCP servers in processes of its own, which stop with it.
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := process.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := process.Start(); err != nil {
		return nil, fmt.Errorf("Hermes could not start: %w", err)
	}
	conn := &connection{process: process, stdin: stdin, grace: 5 * time.Second, waiting: make(map[int64]chan message), exited: make(chan struct{})}
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&conn.stderr, stderr)
		close(stderrDone)
	}()
	go func() {
		conn.read(stdout)
		<-stderrDone
		conn.finish(process.Wait())
	}()
	return conn, nil
}

func (conn *connection) read(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadBytes('\n')
		var received message
		if json.Unmarshal(line, &received) == nil && received.Method == "" {
			var id int64
			if json.Unmarshal(received.ID, &id) == nil {
				conn.mu.Lock()
				reply := conn.waiting[id]
				delete(conn.waiting, id)
				conn.mu.Unlock()
				if reply != nil {
					reply <- received
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (conn *connection) finish(exit error) {
	status := "Hermes exited"
	if failure := new(exec.ExitError); errors.As(exit, &failure) && failure.ExitCode() > 0 {
		status = fmt.Sprintf("Hermes exited with status %d", failure.ExitCode())
	}
	if detail := conn.stderr.String(); detail != "" {
		status += ":\n" + detail
	}
	conn.exitErr = errors.New(status)
	close(conn.exited)
}

func (conn *connection) hasExited() bool {
	select {
	case <-conn.exited:
		return true
	default:
		return false
	}
}

// request sends a request to Hermes and decodes its result into result, unless result is nil.
func (conn *connection) request(ctx context.Context, method string, params, result any) error {
	reply := make(chan message, 1)
	conn.mu.Lock()
	conn.nextID++
	id := conn.nextID
	conn.waiting[id] = reply
	conn.mu.Unlock()
	defer func() {
		conn.mu.Lock()
		delete(conn.waiting, id)
		conn.mu.Unlock()
	}()
	encoded, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	conn.writing.Lock()
	_, err = conn.stdin.Write(append(encoded, '\n'))
	conn.writing.Unlock()
	if err != nil {
		// Hermes has exited or is exiting, which says more than the closed pipe.
		select {
		case <-conn.exited:
			return conn.exitErr
		case <-ctx.Done():
			return err
		}
	}

	select {
	case answer := <-reply:
		if answer.Error != nil {
			return answer.Error
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(answer.Result, result)
	case <-conn.exited:
		return conn.exitErr
	case <-ctx.Done():
		return fmt.Errorf("Hermes did not answer %s: %w", method, ctx.Err())
	}
}

// initialize agrees on the protocol with Hermes and checks that it offers what Gripi needs.
func (conn *connection) initialize(ctx context.Context) error {
	var answer struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession         bool `json:"loadSession"`
			SessionCapabilities struct {
				List *struct{} `json:"list"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	// Hermes runs its tools itself, so Gripi offers it neither files nor terminals.
	params := map[string]any{"protocolVersion": protocolVersion, "clientCapabilities": map[string]any{}}
	if err := conn.request(ctx, "initialize", params, &answer); err != nil {
		return err
	}
	if answer.ProtocolVersion != protocolVersion {
		return fmt.Errorf("Hermes speaks version %d of the Agent Client Protocol, and Gripi version %d", answer.ProtocolVersion, protocolVersion)
	}
	if !answer.AgentCapabilities.LoadSession || answer.AgentCapabilities.SessionCapabilities.List == nil {
		return errors.New("Hermes cannot list and load its sessions over the Agent Client Protocol; update Hermes")
	}
	return nil
}

// Close ends Hermes's input, which ends its sessions, and stops its process group after a grace period.
func (conn *connection) Close() error {
	conn.closeOnce.Do(func() {
		_ = conn.stdin.Close()
		select {
		case <-conn.exited:
		case <-time.After(conn.grace):
		}
		// Also stops what Hermes left running.
		if err := syscall.Kill(-conn.process.Process.Pid, syscall.SIGKILL); err == nil || errors.Is(err, syscall.ESRCH) {
			<-conn.exited
		}
	})
	return nil
}

// tail keeps the end of what Hermes prints on standard error, which explains why it failed.
type tail struct {
	mu     sync.Mutex
	buffer []byte
}

const tailBytes = 4096

func (tail *tail) Write(data []byte) (int, error) {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	tail.buffer = append(tail.buffer, data...)
	if excess := len(tail.buffer) - tailBytes; excess > 0 {
		tail.buffer = tail.buffer[excess:]
	}
	return len(data), nil
}

func (tail *tail) String() string {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	return strings.TrimSpace(string(tail.buffer))
}
