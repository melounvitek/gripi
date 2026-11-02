package helper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// mirrorSocket is where `gripi worker mirror` connects, in the runtime folder.
const mirrorSocket = "mirror.sock"

// finalBarrierTimeout bounds the barrier that mirror.stop takes before it stops the mirror.
const finalBarrierTimeout = time.Minute

// mirroredUnit is a unit whose Source runs in the helper, so that barriers of its sessions reach the Source.
type mirroredUnit struct {
	unit     mirror.Unit
	source   *mirror.Source
	ctx      context.Context
	cancel   context.CancelFunc
	attached bool
	done     chan struct{} // closed when the Source has run, or will never run

	// repository is, for a unit of Pi's session folders, the main folder of the repository whose sessions they hold.
	repository string
}

// startMirror prepares a unit's Source, which runs once `gripi worker mirror` connects main's Destination to it. A
// unit of Pi's session folders, whose sessions are those of the repository whose main folder is repository, sends its
// changes without waiting for barriers and only the complete lines of the sessions' files, and gets the folders that
// don't exist yet, because their sessions are mirrored only from folders that exist.
func (s *server) startMirror(unit *mirror.Unit, sessions bool, repository string) error {
	if unit == nil || !filepath.IsAbs(unit.Main) {
		return errors.New("mirror.start needs a unit with an absolute main folder")
	}
	if sessions {
		if repository == "" {
			return errors.New("mirror.start of session folders needs their repository's main folder as path")
		}
		for _, folder := range append([]string{unit.Main}, unit.Worktrees...) {
			if err := os.MkdirAll(folder, 0o700); err != nil {
				return err
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.mirrors[unit.Main]; current != nil {
		// A Source that never got its channel, or whose channel has ended, makes way for a new one.
		select {
		case <-current.done:
		default:
			if current.attached {
				return fmt.Errorf("%s is mirrored already", unit.Main)
			}
		}
		current.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mirrors[unit.Main] = &mirroredUnit{
		unit: *unit, repository: repository, source: mirror.NewSource(*unit, mirror.Options{AutoBarrier: sessions, CompleteLines: sessions}),
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	return nil
}

// stopMirror takes a last barrier, so that main has every change, then stops the unit's Source.
func (s *server) stopMirror(main string) error {
	s.mu.Lock()
	current := s.mirrors[main]
	delete(s.mirrors, main)
	attached := current != nil && current.attached
	s.mu.Unlock()
	if current == nil {
		return nil
	}
	var err error
	if attached {
		ctx, cancel := context.WithTimeout(current.ctx, finalBarrierTimeout)
		_, _, err = current.source.Barrier(ctx)
		cancel()
	}
	current.cancel()
	if attached {
		<-current.done
	}
	return err
}

// stopMirrors stops every Source without a last barrier, because main has gone.
func (s *server) stopMirrors() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for main, current := range s.mirrors {
		current.cancel()
		delete(s.mirrors, main)
	}
}

// serveMirrors runs each unit's Source over the stream of the `gripi worker mirror` that names this helper's
// instance and the unit in its first line.
func (s *server) serveMirrors(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go s.runMirror(connection)
	}
}

func (s *server) runMirror(connection net.Conn) {
	defer connection.Close()
	hello, reader, err := readHello(connection)
	if err != nil {
		return
	}
	s.mu.Lock()
	current := s.mirrors[hello.Path]
	switch {
	case hello.Instance != s.instance:
		err = fmt.Errorf("the helper on the worker is %s, not %s", s.instance, hello.Instance)
	case current == nil:
		err = fmt.Errorf("%s is not mirrored", hello.Path)
	case current.attached:
		err = fmt.Errorf("%s has a mirror channel already", hello.Path)
	default:
		current.attached = true
	}
	s.mu.Unlock()
	if err != nil {
		replyHello(connection, err)
		return
	}
	current.run(connection, reader, hello.Path)
}

// run runs the Source over main's channel, which the caller took for it, once main has the answer. Without the
// answer, the Source never runs, and makes way for the next one that mirror.start prepares.
func (current *mirroredUnit) run(connection net.Conn, reader io.Reader, name string) {
	defer close(current.done)
	if !replyHello(connection, nil) {
		current.cancel()
		return
	}
	stream := struct {
		io.Reader
		io.Writer
		io.Closer
	}{reader, connection, connection}
	if err := current.source.Run(current.ctx, stream); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("mirror of %s: %v", name, err)
	}
}

// sources returns the Sources that a session needs: that of the unit that holds the session's folder, and that of the
// unit's sessions' folders, which holds the session's file.
func (s *server) sources(session string) (repository, sessions *mirror.Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.sessions[session]
	if current == nil {
		return nil, nil
	}
	for _, unit := range s.mirrors {
		if unit.repository == "" && unit.source.Contains(current.cwd) {
			repository = unit.source
			for _, other := range s.mirrors {
				if other.repository == unit.unit.Main {
					sessions = other.source
				}
			}
		}
	}
	return repository, sessions
}

// Barrier and Healthy answer the worker extension from both Sources of a session, so main has both the repository
// and the session's file as they were. Those of a move's session run in its host.
func (s *server) Barrier(ctx context.Context, session string) error {
	if host, moved := s.hostOf(session); moved {
		if host == nil {
			return errors.New("nothing mirrors this session's folder to main")
		}
		_, err := host.call(ctx, Message{Type: "barrier"})
		return err
	}
	repository, sessions := s.sources(session)
	return bothBarriers(ctx, repository, sessions)
}

func (s *server) Healthy(session string) bool {
	if host, moved := s.hostOf(session); moved {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		answer, err := Response{}, errors.New("no mirrors")
		if host != nil {
			answer, err = host.call(ctx, Message{Type: "health"})
		}
		return err == nil && answer.OK
	}
	return bothHealthy(s.sources(session))
}

// bothBarriers returns once main has what a session changed in its repository and its file.
func bothBarriers(ctx context.Context, repository, session *mirror.Source) error {
	switch {
	case repository == nil:
		return errors.New("nothing mirrors this session's folder to main")
	case session == nil:
		return errors.New("nothing mirrors this session's file to main")
	}
	// Each barrier waits for main, so they wait together.
	sessionDone := make(chan error, 1)
	go func() {
		_, _, err := session.Barrier(ctx)
		sessionDone <- err
	}()
	_, _, err := repository.Barrier(ctx)
	return errors.Join(err, <-sessionDone)
}

func bothHealthy(repository, session *mirror.Source) bool {
	return repository != nil && session != nil && repository.Healthy() && session.Healthy()
}

// readHello reads the first line of a stream to the helper, which names the helper's instance.
func readHello(connection net.Conn) (Message, *bufio.Reader, error) {
	reader := bufio.NewReader(connection)
	connection.SetReadDeadline(time.Now().Add(10 * time.Second))
	var hello Message
	line, err := reader.ReadBytes('\n')
	if err == nil {
		err = json.Unmarshal(line, &hello)
	}
	connection.SetReadDeadline(time.Time{})
	return hello, reader, err
}

// replyHello answers the first line of a stream and reports whether the stream goes on.
func replyHello(connection net.Conn, err error) bool {
	reply := Message{OK: err == nil}
	if err != nil {
		reply.Error = err.Error()
	}
	return json.NewEncoder(connection).Encode(reply) == nil && err == nil
}

// AttachMirror passes stdin and stdout through to the Source of the unit whose main folder is main, as
// `gripi worker mirror` does, if the helper that runs it is instance.
func AttachMirror(runtimeDir, main, instance string, stdin io.Reader, stdout io.Writer) error {
	return pass(filepath.Join(runtimeDir, mirrorSocket), Message{Instance: instance, Path: main}, stdin, stdout)
}

// AttachMoveMirror passes stdin and stdout through to a mirror of a move, that of the session's file if file is set,
// as `gripi worker mirror --move` does, if the helper that runs the move is instance.
func AttachMoveMirror(runtimeDir, move, file, instance string, stdin io.Reader, stdout io.Writer) error {
	if err := checkID("move", move); err != nil {
		return err
	}
	return pass(hostConfig{RuntimeDir: runtimeDir, Move: move}.socket(), Message{Type: "mirror", Instance: instance, File: file}, stdin, stdout)
}

// pass sends hello to the helper's socket at path and, once the helper agrees, passes stdin and stdout through.
func pass(path string, hello Message, stdin io.Reader, stdout io.Writer) error {
	connection, err := net.Dial("unix", path)
	if err != nil {
		return fmt.Errorf("the helper does not listen at %s: %w", path, err)
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(hello); err != nil {
		return err
	}
	reader := bufio.NewReader(connection)
	var reply Message
	line, err := reader.ReadBytes('\n')
	if err == nil {
		err = json.Unmarshal(line, &reply)
	}
	if err != nil {
		return fmt.Errorf("the helper did not answer: %w", err)
	}
	if !reply.OK {
		return errors.New(reply.Error)
	}
	go func() {
		io.Copy(connection, stdin)
		connection.(*net.UnixConn).CloseWrite()
	}()
	_, err = io.Copy(stdout, reader)
	return err
}
