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
// slightly different speeds.
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
	gone     chan struct{}
	close    sync.Once

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
		closing: make(chan struct{}), closed: make(chan struct{}), gone: make(chan struct{}), pending: map[uint64]chan helper.Message{},
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

func (control *Control) watch() {
	for {
		wait := time.Until(control.LastAnswer().Add(control.lease + GoneMargin))
		if wait <= 0 {
			close(control.gone)
			return
		}
		time.Sleep(wait)
	}
}

func (control *Control) call(ctx context.Context, request helper.Message) error {
	reply := make(chan helper.Message, 1)
	control.mu.Lock()
	control.lastID++
	request.ID = control.lastID
	select {
	case <-control.closed:
		control.mu.Unlock()
		return ErrControlClosed
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
		return ErrControlClosed
	case <-ctx.Done():
		forget()
		return ctx.Err()
	}
	select {
	case response, ok := <-reply:
		if !ok {
			return ErrControlClosed
		}
		if !response.OK {
			return fmt.Errorf("worker: %s", response.Error)
		}
		return nil
	case <-ctx.Done():
		forget()
		return ctx.Err()
	}
}

// Instance names the helper, so that streams reach only this one.
func (control *Control) Instance() string { return control.instance }

// CreateSession starts Pi for a session, in the session's cgroup on the worker.
func (control *Control) CreateSession(ctx context.Context, session Session) error {
	return control.call(ctx, helper.Message{Type: "session.create", Session: session.ID, Cwd: session.Cwd, Args: session.Args, Env: session.Env})
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
// copy to it, and answers the barriers of the sessions in the unit. A unit of Pi's session folders, with sessions,
// sends its changes without waiting for barriers, and the helper makes the folders that don't exist yet.
func (control *Control) StartMirror(ctx context.Context, unit mirror.Unit, sessions bool) error {
	return control.call(ctx, helper.Message{Type: "mirror.start", Unit: &unit, Sessions: sessions})
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

// Gone is closed once the worker must have stopped every session: a lease and GoneMargin after the last answer. A
// closed connection doesn't make the worker gone sooner, because it doesn't prove that the helper has seen it.
func (control *Control) Gone() <-chan struct{} { return control.gone }

// Closed is closed when the helper's output has ended.
func (control *Control) Closed() <-chan struct{} { return control.closed }

// Close stops answering and ends the helper's input, which makes the helper stop every session at once.
func (control *Control) Close() error {
	control.close.Do(func() { close(control.closing) })
	return nil
}
