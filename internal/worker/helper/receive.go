package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// receiveSocket is where `gripi worker receive` connects, in the runtime folder.
const receiveSocket = "receive.sock"

// serveReceives makes a copy of a folder on main for each `gripi worker receive` that names this helper's instance in
// its first line. The helper makes the copies itself, so that they see the files as its mirrors and sessions do, also
// in a private view of main's home folder, which shows what was next to that folder when the helper started.
func (s *server) serveReceives(ctx context.Context, listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go s.receive(ctx, connection)
	}
}

// receive makes the folder hello.To a copy of main's folder hello.Path, which main sends with mirror.Sync over the
// connection: with hello.Move, the move's copy, and with hello.File, main's file at the same path, leaving the other
// files in its folder as they are. While it runs, it keeps the copy's state next to the copy, on the same file system.
func (s *server) receive(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	hello, reader, err := readHello(connection)
	if err != nil {
		return
	}
	var destination *mirror.Destination
	var state string
	if hello.Instance != s.instance {
		err = fmt.Errorf("the helper on the worker is %s, not %s", s.instance, hello.Instance)
	} else {
		from, to := hello.Path, hello.To
		switch {
		case hello.Move != "":
			if _, err = s.move(hello.Move); err == nil {
				to = s.copyFolder(hello.Move)
			}
		case hello.File != "":
			from, to = hello.File, hello.File
		}
		state = filepath.Join(filepath.Dir(to), "."+filepath.Base(to)+".gripi-receive")
		if err == nil {
			defer os.RemoveAll(state)
			destination, err = newCopy(state, from, to)
		}
		if err == nil {
			destination.File = hello.File
		}
	}
	if !replyHello(connection, err) {
		return
	}
	aside := filepath.Join(state, "aside")
	stream := struct {
		io.Reader
		io.Writer
	}{reader, connection}
	if err := destination.Serve(ctx, stream, aside); err != nil {
		log.Printf("copy to %s: %v", hello.To, err)
	}
}

// newCopy returns the destination that makes the folder to a copy of the other side's folder from, with its state in
// the folder state. When from and to are the same, the copy keeps the other side's paths.
func newCopy(state, from, to string) (*mirror.Destination, error) {
	// What an interrupted copy left there is sent again, because the sender compares what the folder has.
	if err := os.RemoveAll(state); err != nil {
		return nil, err
	}
	if from == to {
		from, to = "/", "/"
	}
	destination, err := mirror.NewMovedDestination(state, from, to)
	if errors.Is(err, syscall.EROFS) {
		return nil, fmt.Errorf("%w; a folder made next to main's home folder after the gateway connected to the worker is missing from the worker's private view until the gateway connects again", err)
	}
	return destination, err
}

// AttachReceive passes stdin and stdout through to the helper, if it is instance, which makes the folder to a copy of
// main's folder from, as `gripi worker receive` does.
func AttachReceive(runtimeDir, from, to, instance string, stdin io.Reader, stdout io.Writer) error {
	return pass(filepath.Join(runtimeDir, receiveSocket), Message{Instance: instance, Path: from, To: to}, stdin, stdout)
}

// AttachReceiveMove is AttachReceive into the copy of a move, which main's folder from, its shadow of its home
// folder, fills as it is.
func AttachReceiveMove(runtimeDir, move, from, instance string, stdin io.Reader, stdout io.Writer) error {
	return pass(filepath.Join(runtimeDir, receiveSocket), Message{Instance: instance, Path: from, Move: move}, stdin, stdout)
}

// AttachReceiveFile is AttachReceive of main's file at the same path, such as a session's, which needn't exist, and
// whose folder's other files stay as they are.
func AttachReceiveFile(runtimeDir, file, instance string, stdin io.Reader, stdout io.Writer) error {
	return pass(filepath.Join(runtimeDir, receiveSocket), Message{Instance: instance, File: file}, stdin, stdout)
}
