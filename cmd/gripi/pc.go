package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	gateway "github.com/melounvitek/gripi/internal/server"
)

const pcHelp = `Usage:
  gripi pc ssh -- <command> [arguments]
  gripi pc run [--cwd <folder>] -- <command> [arguments]

These use the user's computer, where the Gripi desktop app runs with
File → Allow PC Access for This Server on. The user approves each use there,
and for an SSH login also enters their computer's password.

gripi pc ssh runs a command on this machine, the gateway, with SSH_AUTH_SOCK
pointing at the SSH agent of the user's computer. So ssh, and Git through it,
log in with that computer's keys, which never leave it. The computer signs only
logins to hosts in its known_hosts. Each login asks the user, unless they
allowed the key on that host for 5 minutes. The command runs with gripi's
input, output and environment, and with this machine's SSH configuration and
known_hosts.

gripi pc run runs a command on the user's computer, as the user, and passes on
its output and exit code. Each command asks the user, who sees it with the
file that the computer's PATH finds for it. It runs there in --cwd, an
absolute folder on that computer, or else in the user's home folder, with the
desktop app's environment, without a shell, so use sh -c for pipes, and with
no input. Gripi keeps only the first 16 MiB of each output. When gripi pc run
ends first, the computer stops the command.

Both go to the computer where the user last used the session: sent a message
in it, or clicked or typed in it in the desktop app, or else in the session
that started it as a sub-session. They never go to another computer. gripi pc
says why it refused one, such as when the session was last used on a phone,
in a browser or on a computer whose app isn't connected, or not since the
gateway started. Ask the user to open the session in the desktop app on the
computer that should do this, and to click in it, rather than retrying.

gripi pc finds its session, whose name the user sees, by PI_SESSION_FILE,
which Pi's bash tool sets, or by HERMES_SESSION_ID, which Hermes's terminal
sets.

Examples:
  gripi pc ssh -- git push
  gripi pc run --cwd /home/me/app -- sh -c 'git status | head'

Exit codes:
  The command's own once it ran. When the computer signs no login for
  gripi pc ssh, as when gripi pc refused it, or the user denied it or
  cancelled it in Gripi, the agent answers with a failure, and gripi pc prints
  why once, starting with "gripi pc:". When the command cannot run, gripi pc
  prints why and exits with
  1  the command does not run in a Pi or Hermes session, cannot start, or for
     gripi pc run, did not run on the computer, as when gripi pc refused it,
     or the user denied it or cancelled it in Gripi
  2  usage error
`

const (
	// sshAgentMessageLimit is the size limit of an SSH agent message, as OpenSSH's agent has it.
	sshAgentMessageLimit = 256 << 10
	sshAgentFailure      = 5
)

func runOnPC(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(arguments) > 0 && arguments[0] == "run" {
		return runCommandOnPC(arguments[1:], stdout, stderr)
	}
	if len(arguments) == 0 || arguments[0] != "ssh" {
		return usageError(stderr, "pc", "needs ssh or run, as in 'gripi pc ssh -- git push'")
	}
	if len(arguments) < 3 || arguments[1] != "--" {
		return usageError(stderr, "pc", "put the command after --, as in 'gripi pc ssh -- git push'")
	}
	session := runningSession()
	if session == "" {
		return failure(stderr, "pc", errors.New("run it from a Pi or Hermes session in Gripi; neither PI_SESSION_FILE nor HERMES_SESSION_ID is set"))
	}
	client, err := newGatewayClient()
	if err != nil {
		return failure(stderr, "pc", err)
	}
	directory, err := os.MkdirTemp("", "gripi-pc-")
	if err != nil {
		return failure(stderr, "pc", err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return failure(stderr, "pc", err)
	}
	defer listener.Close()
	relay := &sshAgentRelay{client: client, session: session, stderr: stderr, reported: map[string]bool{}}
	ended, end := context.WithCancel(context.Background())
	defer end()
	relay.connections.Add(1)
	go relay.serve(ended, listener)

	command := exec.Command(arguments[2], arguments[3:]...)
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	command.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket)
	code, err := runForwardingSignals(command)
	if err != nil {
		return failure(stderr, "pc", err)
	}
	// The command's end closes the agent connections, also those of the processes that it left running, which report
	// it to the gateway before gripi exits.
	end()
	listener.Close()
	reported := make(chan struct{})
	go func() {
		relay.connections.Wait()
		close(reported)
	}()
	select {
	case <-reported:
	case <-time.After(5 * time.Second):
	}
	return code
}

// runCommandOnPC asks the user's computer to run a command, and passes on its output and exit code.
func runCommandOnPC(arguments []string, stdout, stderr io.Writer) int {
	separator := slices.Index(arguments, "--")
	if separator < 0 || separator == len(arguments)-1 {
		return usageError(stderr, "pc", "put the command after --, as in 'gripi pc run -- systemctl --user restart pipewire'")
	}
	flags := flag.NewFlagSet("pc run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	cwd := flags.String("cwd", "", "")
	if err := flags.Parse(arguments[:separator]); err != nil {
		return usageError(stderr, "pc", err.Error())
	}
	if flags.NArg() > 0 || *cwd != "" && !filepath.IsAbs(*cwd) {
		return usageError(stderr, "pc", "takes only --cwd with an absolute folder on the computer before --")
	}
	session := runningSession()
	if session == "" {
		return failure(stderr, "pc", errors.New("run it from a Pi or Hermes session in Gripi; neither PI_SESSION_FILE nor HERMES_SESSION_ID is set"))
	}
	client, err := newGatewayClient()
	if err != nil {
		return failure(stderr, "pc", err)
	}
	form := url.Values{"session": {session}, "argv": arguments[separator+1:]}
	if *cwd != "" {
		form.Set("cwd", *cwd)
	}
	request, err := http.NewRequest(http.MethodPost, "http://gripi/pc/run", strings.NewReader(form.Encode()))
	if err != nil {
		return failure(stderr, "pc", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The gateway answers once the command has run, however long that takes.
	var result gateway.SudoResult
	if err := client.do(request, 0, &result); err != nil {
		return failure(stderr, "pc", err)
	}
	stdout.Write(result.Stdout)
	stderr.Write(result.Stderr)
	if result.Truncated {
		fmt.Fprintln(stderr, "gripi pc: the command printed more than 16 MiB to an output, and Gripi dropped the rest")
	}
	return result.ExitCode
}

// runForwardingSignals runs a command, passes on the signals that would stop gripi, and returns the command's exit
// status, or 128 plus the signal that killed it.
func runForwardingSignals(command *exec.Cmd) (int, error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer func() {
		signal.Stop(signals)
		close(signals)
	}()
	if err := command.Start(); err != nil {
		return 0, err
	}
	go func() {
		for received := range signals {
			command.Process.Signal(received)
		}
	}()
	err := command.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status := exit.Sys().(syscall.WaitStatus); status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	return 0, err
}

// sshAgentRelay is an SSH agent that passes each message on to the gateway, which relays it to the desktop app's
// computer.
type sshAgentRelay struct {
	client  *gatewayClient
	session string
	// connections counts the open connections, and the accepting of new ones.
	connections sync.WaitGroup
	mu          sync.Mutex
	stderr      io.Writer
	reported    map[string]bool
}

func (relay *sshAgentRelay) serve(ended context.Context, listener net.Listener) {
	defer relay.connections.Done()
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		relay.connections.Add(1)
		go relay.serveConnection(ended, connection)
	}
}

// serveConnection relays the messages of a connection one at a time until it closes or ended ends, which ends the
// request that waits for a reply, so that the user's computer stops asking.
func (relay *sshAgentRelay) serveConnection(ended context.Context, connection net.Conn) {
	defer relay.connections.Done()
	ctx, cancel := context.WithCancel(ended)
	defer cancel()
	context.AfterFunc(ctx, func() { connection.Close() })
	id := make([]byte, 16)
	rand.Read(id)
	// The first message opens the connection on the gateway, which keeps it on the computer that gets that one.
	form := url.Values{"session": {relay.session}, "connection": {hex.EncodeToString(id)}, "open": {"1"}}
	// A client of the agent waits for each reply, so a message that comes before it ends the connection. Reading on
	// while a request waits sees the connection close.
	messages, waiting := make(chan []byte, 1), make(chan struct{}, 1)
	go func() {
		defer cancel()
		defer close(messages)
		for {
			message, err := readAgentMessage(connection)
			if err != nil {
				return
			}
			select {
			case waiting <- struct{}{}:
				messages <- message
			default:
				return
			}
		}
	}()
	for message := range messages {
		form.Set("message", base64.StdEncoding.EncodeToString(message))
		reply, err := relay.post(ctx, form)
		form.Del("open")
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			relay.report(err)
			reply = []byte{sshAgentFailure}
		}
		<-waiting
		if _, err := connection.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(reply))), reply...)); err != nil {
			break
		}
	}
	form.Del("message")
	form.Set("close", "1")
	closing, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	relay.post(closing, form)
}

// readAgentMessage reads a message of the SSH agent protocol without the length that comes before it.
func readAgentMessage(reader io.Reader) ([]byte, error) {
	var length uint32
	if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	if length > sshAgentMessageLimit {
		return nil, fmt.Errorf("an agent message of %d bytes is too long", length)
	}
	message := make([]byte, length)
	_, err := io.ReadFull(reader, message)
	return message, err
}

// post sends a request to the gateway's SSH agent route until ctx ends. Unlike gatewayClient.do, it keeps ctx, which
// ends with the agent connection.
func (relay *sshAgentRelay) post(ctx context.Context, form url.Values) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gripi/pc/ssh-agent", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	if relay.client.userToken != "" {
		request.Header.Set("Authorization", "Bearer "+relay.client.userToken)
	}
	response, err := relay.client.http.Do(request)
	if err != nil {
		var wrapped *url.Error
		if errors.As(err, &wrapped) {
			err = wrapped.Err
		}
		return nil, fmt.Errorf("cannot reach the gateway (%w). Is it running?", err)
	}
	defer response.Body.Close()
	var answer struct {
		Message []byte `json:"message"`
		Error   string `json:"error"`
	}
	// A reply is an agent message, in base64.
	err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&answer)
	if response.StatusCode != http.StatusOK {
		if answer.Error == "" {
			answer.Error = fmt.Sprintf("the gateway answered %d", response.StatusCode)
		}
		return nil, errors.New(answer.Error)
	}
	return answer.Message, err
}

// report prints each error once, as the command may make many connections that fail alike.
func (relay *sshAgentRelay) report(err error) {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if !relay.reported[err.Error()] {
		relay.reported[err.Error()] = true
		fmt.Fprintf(relay.stderr, "gripi pc: %v\n", err)
	}
}
