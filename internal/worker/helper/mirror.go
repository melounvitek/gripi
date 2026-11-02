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
	"time"

	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// finalBarrierTimeout bounds the last barrier of each of a move's mirrors, which move.release takes before it stops
// them.
const finalBarrierTimeout = time.Minute

// mirroredUnit is one of a move's mirrors: its Source runs in the move's host, so that the barriers of the move's
// session reach the Source.
type mirroredUnit struct {
	unit     mirror.Unit
	source   *mirror.Source
	ctx      context.Context
	cancel   context.CancelFunc
	attached bool
	done     chan struct{} // closed when the Source has run, or will never run
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

// Barrier and Healthy answer the worker extension from both of the mirrors of a session's move, which run in its
// host, so main has both the repository and the session's file as they were.
func (s *server) Barrier(ctx context.Context, session string) error {
	host, err := s.hostOf(session)
	if err != nil {
		return err
	}
	_, err = host.call(ctx, Message{Type: "barrier"})
	return err
}

func (s *server) Healthy(session string) bool {
	host, err := s.hostOf(session)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	answer, err := host.call(ctx, Message{Type: "health"})
	return err == nil && answer.OK
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
