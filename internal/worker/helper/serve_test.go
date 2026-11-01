package helper_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/helper"
)

// fakeClock stands in for CLOCK_BOOTTIME.
type fakeClock struct{ now atomic.Int64 }

func (clock *fakeClock) Now() time.Duration      { return time.Duration(clock.now.Load()) }
func (clock *fakeClock) Set(value time.Duration) { clock.now.Store(int64(value)) }

// fakeMain talks to a helper the way main does, but by hand.
type fakeMain struct {
	t         *testing.T
	input     io.WriteCloser
	writing   sync.Mutex
	messages  chan helper.Message
	answering atomic.Bool // whether to answer pings by itself instead of passing them on
	served    chan struct{}
	err       error // why the helper stopped, once served is closed
	nextID    uint64
}

// newFakeMain reads the helper's messages from output. The caller calls stopped when the helper has stopped.
func newFakeMain(t *testing.T, input io.WriteCloser, output io.Reader) *fakeMain {
	main := &fakeMain{t: t, input: input, messages: make(chan helper.Message, 100), served: make(chan struct{})}
	go func() {
		defer close(main.messages)
		decoder := json.NewDecoder(output)
		for {
			var message helper.Message
			if decoder.Decode(&message) != nil {
				return
			}
			if message.Type == "ping" && main.answering.Load() {
				main.write(helper.Response{ID: message.ID, OK: true})
			} else {
				main.messages <- message
			}
		}
	}()
	t.Cleanup(func() {
		input.Close()
		select {
		case <-main.served:
		case <-time.After(5 * time.Second):
			t.Error("the helper did not stop")
		}
	})
	return main
}

func (main *fakeMain) stopped(err error) {
	main.err = err
	close(main.served)
}

// serveHelper serves a helper in this process, with a clock that the test sets.
func serveHelper(t *testing.T, options helper.Options) (*fakeMain, *fakeClock) {
	t.Helper()
	clock := &fakeClock{}
	if options.RuntimeDir == "" {
		options.RuntimeDir = t.TempDir()
	}
	options.Clock = clock.Now
	inputReader, input := io.Pipe()
	outputReader, output := io.Pipe()
	main := newFakeMain(t, input, outputReader)
	go func() {
		main.stopped(helper.Serve(context.Background(), inputReader, output, options))
		output.Close()
	}()
	return main, clock
}

func (main *fakeMain) next() helper.Message {
	main.t.Helper()
	select {
	case message, ok := <-main.messages:
		if !ok {
			main.t.Fatal("the helper stopped")
		}
		return message
	case <-time.After(2 * time.Second):
		main.t.Fatal("no message from the helper")
		return helper.Message{}
	}
}

// nextPing skips other messages until the next ping and returns its ID.
func (main *fakeMain) nextPing() uint64 {
	main.t.Helper()
	for {
		if message := main.next(); message.Type == "ping" {
			return message.ID
		}
	}
}

func (main *fakeMain) write(value any) error {
	main.writing.Lock()
	defer main.writing.Unlock()
	return json.NewEncoder(main.input).Encode(value)
}

func (main *fakeMain) send(value any) {
	main.t.Helper()
	if err := main.write(value); err != nil {
		main.t.Fatal(err)
	}
}

func (main *fakeMain) answer(ping uint64) {
	main.t.Helper()
	main.send(map[string]any{"id": ping, "ok": true})
}

// request sends a request and returns the helper's response, skipping pings.
func (main *fakeMain) request(request map[string]any) helper.Message {
	main.t.Helper()
	main.nextID++
	request["id"] = main.nextID
	main.send(request)
	for {
		if message := main.next(); message.Type == "" && message.ID == main.nextID {
			return message
		}
	}
}

// settle returns once the helper has read everything sent so far, because it answers requests in order of arrival.
func (main *fakeMain) settle() {
	main.t.Helper()
	main.request(map[string]any{"type": "nothing"})
}

func (main *fakeMain) assertServing(within time.Duration) {
	main.t.Helper()
	select {
	case <-main.served:
		main.t.Fatalf("the helper stopped: %v", main.err)
	case <-time.After(within):
	}
}

func (main *fakeMain) assertStopped(expected error) {
	main.t.Helper()
	select {
	case <-main.served:
		if !errors.Is(main.err, expected) {
			main.t.Fatalf("the helper stopped with %v, want %v", main.err, expected)
		}
	case <-time.After(5 * time.Second):
		main.t.Fatal("the helper is still serving")
	}
}

func TestHelperSaysHelloAndPingsEverySecond(t *testing.T) {
	main, clock := serveHelper(t, helper.Options{Version: "v1"})

	hello := main.next()
	if hello.Type != "hello" || hello.Version != "v1" || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(hello.Instance) {
		t.Fatalf("hello = %+v", hello)
	}
	for expected := uint64(1); expected <= 3; expected++ {
		if ping := main.nextPing(); ping != expected {
			t.Fatalf("ping %d, want %d", ping, expected)
		}
		clock.Set(clock.Now() + time.Second)
	}
	if other, _ := serveHelper(t, helper.Options{}); other.next().Instance == hello.Instance {
		t.Fatalf("two helpers have the instance %s", hello.Instance)
	}
}

func TestLeaseRunsFromTheSendTimeOfTheLatestAnsweredPing(t *testing.T) {
	main, clock := serveHelper(t, helper.Options{})
	first := main.nextPing()
	clock.Set(time.Second)
	second := main.nextPing()
	clock.Set(10 * time.Second)
	main.nextPing()

	main.answer(second)
	main.answer(first)
	main.settle()
	clock.Set(31*time.Second - time.Millisecond)
	main.assertServing(300 * time.Millisecond)
	clock.Set(31 * time.Second)
	main.assertStopped(helper.ErrLeaseExpired)
}

func TestHelperThatNobodyAnswersStopsOneLeaseAfterItStarted(t *testing.T) {
	main, clock := serveHelper(t, helper.Options{Lease: 5 * time.Second})
	main.nextPing()
	clock.Set(5*time.Second - time.Millisecond)
	main.assertServing(300 * time.Millisecond)
	clock.Set(5 * time.Second)
	main.assertStopped(helper.ErrLeaseExpired)
}

func TestHelperStopsWhenMainClosesTheConnection(t *testing.T) {
	main, _ := serveHelper(t, helper.Options{})
	main.answer(main.nextPing())
	main.input.Close()
	main.assertStopped(nil)
}

func TestOnlyOneHelperServesARuntimeFolder(t *testing.T) {
	folder := t.TempDir()
	first, _ := serveHelper(t, helper.Options{RuntimeDir: folder})
	first.next()

	err := helper.Serve(context.Background(), strings.NewReader(""), io.Discard, helper.Options{RuntimeDir: folder})
	if err == nil || !strings.Contains(err.Error(), "another") {
		t.Fatalf("second helper = %v", err)
	}
}

func TestAuthApplyGoesToTheHookWhilePingsGoOn(t *testing.T) {
	type write struct{ path, contents string }
	writes := make(chan write)
	release := make(chan error)
	main, clock := serveHelper(t, helper.Options{ApplyAuth: func(path, contents string) error {
		writes <- write{path, contents}
		return <-release
	}})
	main.answer(main.nextPing())

	main.nextID++
	main.send(map[string]any{"id": main.nextID, "type": "auth.apply", "path": "/home/me/.pi/agent/auth.json", "contents": `{"a":1}`})
	if got := <-writes; got != (write{"/home/me/.pi/agent/auth.json", `{"a":1}`}) {
		t.Fatalf("hook got %+v", got)
	}
	clock.Set(time.Second)
	main.answer(main.nextPing())
	release <- nil
	if response := main.next(); response.ID != main.nextID || !response.OK || response.Error != "" {
		t.Fatalf("response = %+v", response)
	}

	go func() { <-writes; release <- errors.New("auth.json is locked") }()
	if response := main.request(map[string]any{"type": "auth.apply", "path": "/x", "contents": "{}"}); response.OK || response.Error != "auth.json is locked" {
		t.Fatalf("response = %+v", response)
	}
}

func TestRequestsTheHelperCannotDoFail(t *testing.T) {
	main, _ := serveHelper(t, helper.Options{})
	main.answer(main.nextPing())

	for _, request := range []map[string]any{
		{"type": "auth.apply", "path": "/x", "contents": "{}"},
		{"type": "session.create", "session": "s1", "cwd": "/", "args": []string{"pi"}},
		{"type": "session.create", "session": "../s1", "cwd": "/", "args": []string{"pi"}},
		{"type": "launch"},
	} {
		if response := main.request(request); response.OK || response.Error == "" {
			t.Fatalf("%v = %+v", request, response)
		}
	}
	for _, request := range []map[string]any{{"type": "session.close", "session": "s1"}, {"type": "session.release", "session": "s1"}} {
		if response := main.request(request); !response.OK {
			t.Fatalf("%v of an unknown session = %+v", request, response)
		}
	}
}
