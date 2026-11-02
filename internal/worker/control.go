package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// GoneMargin is how much longer than the lease main waits before it treats a worker as gone, for clocks that run at
// slightly different speeds, and for the helper to stop: it kills every session at once when the lease ends, and exits
// within about a second.
const GoneMargin = 2 * time.Second

// ErrControlClosed is the error of requests to a helper whose control channel has ended.
var ErrControlClosed = errors.New("the worker's control channel has closed")

// Session is what the helper needs to start a session's Pi.
type Session struct {
	// ID names the session on the worker: 1 to 64 letters, digits, '-' or '_'.
	ID string
	// Cwd is Pi's working folder.
	Cwd string
	// Args are Pi's command and its arguments.
	Args []string
	// Env holds NAME=value variables on top of the helper's environment. They travel only over the control channel,
	// never on a command line.
	Env []string
	// Move is the ID of the move whose view Pi runs in, if any.
	Move string
}

// Move is a session that moves to the worker on a private copy of its repository, which the session's Pi, its
// mirrors and its Docker socket see at main's paths there. Paths are main's.
type Move struct {
	// ID names the move on the worker: 1 to 64 letters, digits, '-' or '_'.
	ID string
	// Repository is the repository's common Git folder; empty for a folder outside Git.
	Repository string
	// Roots are the worktrees that the copy holds, the session's own first.
	Roots []string
	// Private are the folders directly in main's home folder that hold Repository and Roots, which the move's view
	// shows from the copy.
	Private []string
}

// Control is main's end of a helper's control channel. It answers the helper's pings at once, whatever else it
// waits for, and tracks when the worker must have stopped every session.
type Control struct {
	lease    time.Duration
	instance string
	input    io.WriteCloser
	answers  chan uint64
	requests chan helper.Message
	closing  chan struct{}
	closed   chan struct{} // the helper's output has ended
	lost     chan struct{}
	gone     chan struct{}
	close    sync.Once
	lose     sync.Once

	mu         sync.Mutex
	lastID     uint64
	pending    map[uint64]chan helper.Message
	lastAnswer time.Time
}

// NewControl runs the control protocol on a helper's output and input. It returns once the helper has said hello with
// version and main has answered its first ping, so that requests reach a helper whose lease runs.
func NewControl(ctx context.Context, output io.Reader, input io.WriteCloser, version string, lease time.Duration) (*Control, error) {
	control := &Control{
		lease: lease, input: input, answers: make(chan uint64, 16), requests: make(chan helper.Message),
		closing: make(chan struct{}), closed: make(chan struct{}), lost: make(chan struct{}), gone: make(chan struct{}), pending: map[uint64]chan helper.Message{},
	}
	hello := make(chan helper.Message, 1)
	answered := make(chan struct{})
	go control.read(output, hello)
	go control.write(answered)
	err := func() error {
		select {
		case message := <-hello:
			if message.Version != version {
				return fmt.Errorf("the helper on the worker has version %s, not %s", message.Version, version)
			}
			control.instance = message.Instance
		case <-control.closed:
			return errors.New("the helper ended before it said hello")
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-answered:
			return nil
		case <-control.closed:
			return errors.New("the helper ended before its first ping")
		case <-ctx.Done():
			return ctx.Err()
		}
	}()
	if err != nil {
		control.Close()
		return nil, err
	}
	go control.watch()
	return control, nil
}

func (control *Control) read(output io.Reader, hello chan<- helper.Message) {
	defer func() {
		control.mu.Lock()
		defer control.mu.Unlock()
		close(control.closed)
		for id, reply := range control.pending {
			close(reply)
			delete(control.pending, id)
		}
	}()
	decoder := json.NewDecoder(output)
	for {
		var message helper.Message
		if err := decoder.Decode(&message); err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("read from the worker's helper: %v", err)
			}
			return
		}
		switch message.Type {
		case "hello":
			select {
			case hello <- message:
			default:
			}
		case "ping":
			// When answers back up, the link is stuck, and the lease must run out.
			select {
			case control.answers <- message.ID:
			default:
			}
		case "":
			control.mu.Lock()
			if reply := control.pending[message.ID]; reply != nil {
				reply <- message
				delete(control.pending, message.ID)
			}
			control.mu.Unlock()
		}
	}
}

// write sends answers before requests, and notes when each answer has left.
func (control *Control) write(answered chan<- struct{}) {
	defer control.input.Close()
	encoder := json.NewEncoder(control.input)
	var first sync.Once
	answer := func(ping uint64) {
		if encoder.Encode(helper.Response{ID: ping, OK: true}) != nil {
			return
		}
		control.mu.Lock()
		control.lastAnswer = time.Now()
		control.mu.Unlock()
		first.Do(func() { close(answered) })
	}
	for {
		select {
		case ping := <-control.answers:
			answer(ping)
			continue
		default:
		}
		select {
		case ping := <-control.answers:
			answer(ping)
		case request := <-control.requests:
			encoder.Encode(request)
		case <-control.closing:
			return
		}
	}
}

// watch closes lost when the helper's output ends or main hasn't answered for half a lease, and gone once the lease
// and GoneMargin have passed since the last answer.
func (control *Control) watch() {
	lose := func() { control.lose.Do(func() { close(control.lost) }) }
	closed := control.closed
	for {
		last := control.LastAnswer()
		wait := time.Until(last.Add(control.lease + GoneMargin))
		if wait <= 0 {
			lose()
			close(control.gone)
			return
		}
		if silent := time.Until(last.Add(control.lease / 2)); silent <= 0 {
			lose()
		} else {
			wait = min(wait, silent)
		}
		timer := time.NewTimer(wait)
		select {
		case <-closed:
			lose()
			closed = nil
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (control *Control) call(ctx context.Context, request helper.Message) error {
	_, err := control.request(ctx, request)
	return err
}

// request sends a request to the helper and returns its answer.
func (control *Control) request(ctx context.Context, request helper.Message) (helper.Message, error) {
	reply := make(chan helper.Message, 1)
	control.mu.Lock()
	control.lastID++
	request.ID = control.lastID
	select {
	case <-control.closed:
		control.mu.Unlock()
		return helper.Message{}, ErrControlClosed
	default:
		control.pending[request.ID] = reply
	}
	control.mu.Unlock()
	forget := func() {
		control.mu.Lock()
		delete(control.pending, request.ID)
		control.mu.Unlock()
	}
	select {
	case control.requests <- request:
	case <-control.closing:
		forget()
		return helper.Message{}, ErrControlClosed
	case <-ctx.Done():
		forget()
		return helper.Message{}, ctx.Err()
	}
	select {
	case response, ok := <-reply:
		if !ok {
			return helper.Message{}, ErrControlClosed
		}
		if !response.OK {
			return response, fmt.Errorf("worker: %s", response.Error)
		}
		return response, nil
	case <-ctx.Done():
		forget()
		return helper.Message{}, ctx.Err()
	}
}

// Instance names the helper, so that streams reach only this one.
func (control *Control) Instance() string { return control.instance }

// CreateSession starts Pi for a session, in the session's cgroup on the worker, and in its move's view if it has one.
func (control *Control) CreateSession(ctx context.Context, session Session) error {
	return control.call(ctx, helper.Message{Type: "session.create", Session: session.ID, Cwd: session.Cwd, Args: session.Args, Env: session.Env, Move: session.Move})
}

// PrepareMove makes the folder of a move's copy on the worker, unless it has it, and returns it as the helper sees
// it. Then the move's copy gets main's shadow of the move's folders (Conn.UploadMove), and the session's file
// (Conn.UploadSessionFile), before the session starts there.
func (control *Control) PrepareMove(ctx context.Context, move Move) (string, error) {
	response, err := control.request(ctx, helper.Message{Type: "move.prepare", Move: move.ID, Repository: move.Repository, Roots: move.Roots, Private: move.Private})
	return response.Path, err
}

// SeedMove gives a move's copy, once it has main's shadow, the folders that Git ignores, such as node_modules, from
// the latest earlier copy of its repository on the worker, or from those that deleted copies left.
func (control *Control) SeedMove(ctx context.Context, id string) error {
	return control.call(ctx, helper.Message{Type: "move.seed", Move: id})
}

// StartMoveMirror prepares a mirror of a move, which sends the copy's changes, or with file, the session's file's,
// once AttachMoveMirror connects main's copy to it. A session's barriers need both of its move's mirrors.
func (control *Control) StartMoveMirror(ctx context.Context, id, file string) error {
	return control.call(ctx, helper.Message{Type: "mirror.start", Move: id, File: file})
}

// ReleaseMove kills every process of a move's sessions, then has main take the last barrier of each of its mirrors,
// which run until then, and stops them, so that main's copies end. It returns the numbers of those barriers, 0 for a
// mirror that never started.
func (control *Control) ReleaseMove(ctx context.Context, id string) (helper.Barriers, error) {
	response, err := control.request(ctx, helper.Message{Type: "move.release", Move: id})
	if err != nil || response.Barriers == nil {
		return helper.Barriers{}, err
	}
	return *response.Barriers, nil
}

// DeleteMove deletes the copy of a released move, once main has what it needs of it, and leaves the folders in it
// that Git ignores to the next copies of its repository. A move that is gone already is fine.
func (control *Control) DeleteMove(ctx context.Context, id string) error {
	return control.call(ctx, helper.Message{Type: "move.delete", Move: id})
}

// CloseSession stops a session's Pi, as Gripi stops an idle Pi, and keeps the processes that Pi started.
func (control *Control) CloseSession(ctx context.Context, id string) error {
	return control.call(ctx, helper.Message{Type: "session.close", Session: id})
}

// ReleaseSession kills every process of a session on the worker.
func (control *Control) ReleaseSession(ctx context.Context, id string) error {
	return control.call(ctx, helper.Message{Type: "session.release", Session: id})
}

// ApplyAuth writes a login file on the worker under Pi's lock.
func (control *Control) ApplyAuth(ctx context.Context, path, contents string) error {
	return control.call(ctx, helper.Message{Type: "auth.apply", Path: path, Contents: contents})
}

// StartMirror prepares the helper's mirror of a unit, which sends the unit's changes once AttachMirror connects main's
// copy to it, and answers the barriers of the sessions in the unit. A unit of Pi's session folders, those of the
// sessions in the repository whose main folder is sessionsOf, sends its changes without waiting for barriers, and the
// helper makes the folders that don't exist yet. The barriers of a session need both mirrors.
func (control *Control) StartMirror(ctx context.Context, unit mirror.Unit, sessionsOf string) error {
	return control.call(ctx, helper.Message{Type: "mirror.start", Unit: &unit, Sessions: sessionsOf != "", Path: sessionsOf})
}

// StopMirror sends main the last changes of the unit whose main folder is main, then stops its mirror.
func (control *Control) StopMirror(ctx context.Context, main string) error {
	return control.call(ctx, helper.Message{Type: "mirror.stop", Path: main})
}

// LastAnswer is when main last answered a ping.
func (control *Control) LastAnswer() time.Time {
	control.mu.Lock()
	defer control.mu.Unlock()
	return control.lastAnswer
}

// Lost is closed once main has lost the worker: the helper's output has ended, as it does when the connection's master
// dies, or main hasn't answered a ping for half a lease, as over a link that hangs. ssh's own keepalive gives up on a
// master about as late. The worker may still run sessions until it is gone.
func (control *Control) Lost() <-chan struct{} { return control.lost }

// Gone is closed once the worker must have stopped every session: a lease and GoneMargin after the last answer. A
// closed connection doesn't make the worker gone sooner, because it doesn't prove that the helper has seen it.
func (control *Control) Gone() <-chan struct{} { return control.gone }

// GoneAt is when the worker will be gone, unless main answers again.
func (control *Control) GoneAt() time.Time {
	return control.LastAnswer().Add(control.lease + GoneMargin)
}

// Closed is closed when the helper's output has ended.
func (control *Control) Closed() <-chan struct{} { return control.closed }

// Close stops answering and ends the helper's input, which makes the helper stop every session at once.
func (control *Control) Close() error {
	control.close.Do(func() { close(control.closing) })
	return nil
}
