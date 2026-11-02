package helper

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// A session's cgroup outlives its Pi, like the background processes Pi started: Gripi closes an idle Pi and starts
// another one later. Only session.release kills them.
type session struct {
	cgroup    string
	cwd       string         // Pi's folder
	move      string         // the move that the session runs in, if any
	listeners []net.Listener // the attach and extension sockets

	// While Pi runs:
	pi       *exec.Cmd
	exited   chan struct{}
	stdin    *os.File
	stdout   *os.File
	attached bool
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// checkID checks the ID of a session or a move.
func checkID(kind, id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("%s %q is not 1 to 64 letters, digits, '-' or '_'", kind, id)
	}
	return nil
}

// closeTimeout is how long session.close waits for Pi to stop on SIGTERM before it kills Pi.
const closeTimeout = 5 * time.Second

// releaseTimeout is how long session.release waits for a session's processes to die once it has killed them.
const releaseTimeout = 5 * time.Second

// create starts Pi for a session, in the session's cgroup, with the helper's environment, request.Env and
// GRIPI_WORKER_SOCKET. With inMove, it starts Pi in the move's view, with the move's DOCKER_HOST.
func (s *server) create(request Message, inMove *move) error {
	id := request.Session
	if err := checkID("session", id); err != nil {
		return err
	}
	if len(request.Args) == 0 {
		return errors.New("session.create needs Pi's command in args")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return errors.New("the helper is stopping")
	}
	if !s.answered {
		return errors.New("main has not answered a ping yet")
	}
	if s.options.Cgroup == "" {
		return errors.New("sessions need a cgroup that systemd delegated to the helper (systemd-run --user -p Delegate=yes)")
	}
	var moveID string
	if inMove != nil {
		if inMove.closed {
			return fmt.Errorf("move %s is released", inMove.id)
		}
		moveID = inMove.id
	}
	current := s.sessions[id]
	if current == nil {
		current = &session{cgroup: s.sessionCgroup(id, moveID), move: moveID}
		if inMove != nil {
			inMove.sessions = append(inMove.sessions, id)
		}
		for _, name := range []string{id + ".attach", id + ".sock"} {
			listener, err := net.Listen("unix", filepath.Join(s.options.RuntimeDir, name))
			if err != nil {
				closeAll(current.listeners)
				return err
			}
			current.listeners = append(current.listeners, listener)
		}
		s.sessions[id] = current
		go s.serveAttach(current, current.listeners[0])
		go s.serveExtension(id, current.listeners[1])
	}
	if current.pi != nil {
		return fmt.Errorf("Pi already runs for session %s", id)
	}
	if current.move != moveID {
		return fmt.Errorf("session %s runs in another move", id)
	}
	current.cwd = request.Cwd

	environment := append(append(os.Environ(), request.Env...), "GRIPI_WORKER_SOCKET="+current.listeners[1].Addr().String())
	if inMove != nil {
		environment = append(environment, "DOCKER_HOST=unix://"+s.dockerSocket(inMove))
	}
	path, err := lookPath(request.Args[0], environment)
	if err != nil {
		return err
	}
	pi := &exec.Cmd{Path: path, Args: request.Args, Env: environment, Dir: request.Cwd}
	if inMove != nil {
		if pi, err = s.view(inMove).Command(request.Cwd, append([]string{path}, request.Args[1:]...), environment); err != nil {
			return err
		}
	}
	stdinReader, stdin, err := os.Pipe()
	if err != nil {
		return err
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		stdinReader.Close()
		stdin.Close()
		return err
	}
	pi.Stdin, pi.Stdout = stdinReader, stdoutWriter
	err = startInCgroup(pi, current.cgroup)
	stdinReader.Close()
	stdoutWriter.Close()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("start Pi: %w", err)
	}
	current.pi, current.exited, current.stdin, current.stdout, current.attached = pi, make(chan struct{}), stdin, stdout, false
	go s.waitForPi(id, current)
	return nil
}

// sessionCgroup is the cgroup of a session, of one in a move a cgroup of the move's own, because the same session may
// move again before the earlier move is gone.
func (s *server) sessionCgroup(id, move string) string {
	if move != "" {
		id += "." + move
	}
	return filepath.Join(s.options.Cgroup, "session-"+id)
}

func (s *server) waitForPi(id string, current *session) {
	err := current.pi.Wait()
	if err != nil {
		log.Printf("session %s: Pi exited: %v", id, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current.pi = nil
	current.stdin.Close()
	if current.attached {
		// Wakes up passOutput, which reads what Pi wrote last.
		current.stdout.SetReadDeadline(time.Now().Add(drainTime))
	} else {
		current.stdout.Close()
	}
	close(current.exited)
}

// close stops a session's Pi and keeps its other processes.
func (s *server) close(id string) error {
	s.mu.Lock()
	current := s.sessions[id]
	if current == nil || current.pi == nil {
		s.mu.Unlock()
		return nil
	}
	process, exited := current.pi.Process, current.exited
	s.mu.Unlock()
	process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
		return nil
	case <-time.After(closeTimeout):
	}
	process.Kill()
	<-exited
	return nil
}

// release kills every process of the sessions and forgets them. It waits at most timeout for the processes to die.
func (s *server) release(timeout time.Duration, ids ...string) error {
	s.mu.Lock()
	cgroups := s.forget(ids...)
	s.mu.Unlock()
	return killCgroups(timeout, cgroups...)
}

// forget forgets the sessions, with s.mu held, and returns their cgroups.
func (s *server) forget(ids ...string) []string {
	var cgroups []string
	for _, id := range ids {
		if current := s.sessions[id]; current != nil {
			delete(s.sessions, id)
			closeAll(current.listeners)
			cgroups = append(cgroups, current.cgroup)
		}
	}
	return cgroups
}

func (s *server) signalPis(signal syscall.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, current := range s.sessions {
		if current.pi != nil {
			current.pi.Process.Signal(signal)
		}
	}
}

func closeAll(listeners []net.Listener) {
	for _, listener := range listeners {
		listener.Close()
	}
}

// lookPath finds a command in the PATH of the environment Pi gets, rather than in the helper's.
func lookPath(command string, environment []string) (string, error) {
	if strings.Contains(command, "/") {
		return command, nil
	}
	var path string
	for _, variable := range environment {
		if value, found := strings.CutPrefix(variable, "PATH="); found {
			path = value
		}
	}
	for _, folder := range filepath.SplitList(path) {
		candidate := filepath.Join(folder, command)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is not in PATH (%s)", command, path)
}

// drainTime is how long the attach stream waits for more output after Pi has exited. Processes that Pi started can
// keep its output open, so the stream can't wait for the end of it.
const drainTime = 100 * time.Millisecond

// serveAttach passes a session's Pi RPC through to each `gripi worker attach` that names this helper's instance in
// its first line. When the attach stream ends, Pi's input ends, and Pi exits; the session's cgroup stays.
func (s *server) serveAttach(current *session, listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go s.attach(current, connection)
	}
}

func (s *server) attach(current *session, connection net.Conn) {
	defer connection.Close()
	hello, reader, err := readHello(connection)
	if err != nil {
		return
	}
	s.mu.Lock()
	stdin, stdout, exited := current.stdin, current.stdout, current.exited
	switch {
	case hello.Instance != s.instance:
		err = fmt.Errorf("the helper on the worker is %s, not %s", s.instance, hello.Instance)
	case current.pi == nil:
		err = errors.New("Pi is not running")
	case current.attached:
		err = errors.New("Pi is attached already")
	default:
		current.attached = true
	}
	s.mu.Unlock()
	if !replyHello(connection, err) {
		return
	}
	go func() {
		io.Copy(stdin, reader)
		stdin.Close()
	}()
	passOutput(connection, stdout, exited)
	stdout.Close()
}

// passOutput copies Pi's output to the attach stream until Pi has exited and wrote nothing more for drainTime.
func passOutput(connection net.Conn, stdout *os.File, exited <-chan struct{}) {
	buffer := make([]byte, 64<<10)
	for {
		count, err := stdout.Read(buffer)
		if _, writeErr := connection.Write(buffer[:count]); writeErr != nil || err != nil {
			return
		}
		select {
		case <-exited:
			stdout.SetReadDeadline(time.Now().Add(drainTime))
		default:
		}
	}
}

// Attach passes stdin and stdout through to the Pi of a session, as `gripi worker attach` does, if the helper that
// runs it is instance. It returns when Pi's output ends.
func Attach(runtimeDir, id, instance string, stdin io.Reader, stdout io.Writer) error {
	if err := checkID("session", id); err != nil {
		return err
	}
	return pass(filepath.Join(runtimeDir, id+".attach"), Message{Instance: instance}, stdin, stdout)
}
