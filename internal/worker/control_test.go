package worker_test

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker"
	"github.com/melounvitek/gripi/internal/worker/helper"
)

// fakeClock stands in for the helper's CLOCK_BOOTTIME.
type fakeClock struct{ now atomic.Int64 }

func (clock *fakeClock) Now() time.Duration          { return time.Duration(clock.now.Load()) }
func (clock *fakeClock) Advance(delta time.Duration) { clock.now.Add(int64(delta)) }

// valve passes a stream through until it is shut. A shut valve holds reads back and drops writes, as a link does
// that stopped working in one direction.
type valve struct {
	reader io.Reader
	writer io.WriteCloser
	shut   atomic.Bool
}

func (valve *valve) Read(buffer []byte) (int, error) {
	for valve.shut.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	return valve.reader.Read(buffer)
}

func (valve *valve) Write(buffer []byte) (int, error) {
	if valve.shut.Load() {
		return len(buffer), nil
	}
	return valve.writer.Write(buffer)
}

func (valve *valve) Close() error { return valve.writer.Close() }

// linkedHelper is a helper served in this process and connected to main's Control.
type linkedHelper struct {
	control          *worker.Control
	toHelper, toMain *valve
	served           chan error
}

func linkHelper(t *testing.T, version string, lease time.Duration, options helper.Options) (*linkedHelper, error) {
	t.Helper()
	options.RuntimeDir, options.Lease = t.TempDir(), lease
	helperInput, mainOutput := io.Pipe()
	mainInput, helperOutput := io.Pipe()
	link := &linkedHelper{toHelper: &valve{writer: mainOutput}, toMain: &valve{reader: mainInput}, served: make(chan error, 1)}
	go func() {
		err := helper.Serve(context.Background(), helperInput, helperOutput, options)
		helperOutput.Close()
		link.served <- err
	}()
	t.Cleanup(func() {
		link.toHelper.Close()
		<-link.served
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	link.control, err = worker.NewControl(ctx, link.toMain, link.toHelper, version, lease)
	return link, err
}

func TestControlCallsTheHelper(t *testing.T) {
	var written atomic.Value
	link, err := linkHelper(t, "v1", helper.DefaultLease, helper.Options{Version: "v1", ApplyAuth: func(path, contents string) error {
		if path == "/locked" {
			return errors.New("auth.json is locked")
		}
		written.Store(path + " " + contents)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	control := link.control
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(control.Instance()) {
		t.Fatalf("instance = %q", control.Instance())
	}

	if err := control.ApplyAuth(ctx, "/home/me/.pi/agent/auth.json", `{"a":1}`); err != nil || written.Load() != `/home/me/.pi/agent/auth.json {"a":1}` {
		t.Fatalf("ApplyAuth = %v, wrote %v", err, written.Load())
	}
	if err := control.ApplyAuth(ctx, "/locked", "{}"); err == nil || !strings.Contains(err.Error(), "auth.json is locked") {
		t.Fatalf("ApplyAuth of a locked file = %v", err)
	}
	err = control.CreateSession(ctx, worker.Session{ID: "s1", Cwd: "/", Args: []string{"pi"}, Env: []string{"A=1"}})
	if err == nil || !strings.Contains(err.Error(), "cgroup") {
		t.Fatalf("CreateSession without cgroups = %v", err)
	}
	if err := control.CloseSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := control.ReleaseSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}

	control.Close()
	select {
	case err := <-link.served:
		link.served <- err
		if err != nil {
			t.Fatalf("the helper stopped with %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing the control channel did not stop the helper")
	}
}

func TestControlRefusesAHelperOfAnotherVersion(t *testing.T) {
	_, err := linkHelper(t, "v1", helper.DefaultLease, helper.Options{Version: "v2"})
	if err == nil || !strings.Contains(err.Error(), "v1") || !strings.Contains(err.Error(), "v2") {
		t.Fatalf("NewControl = %v", err)
	}
}

func TestControlAnswersPingsWhileRequestsWait(t *testing.T) {
	clock := &fakeClock{}
	release := make(chan struct{})
	link, err := linkHelper(t, "v1", helper.DefaultLease, helper.Options{Version: "v1", Clock: clock.Now, ApplyAuth: func(string, string) error {
		<-release
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	applied := make(chan error)
	go func() { applied <- link.control.ApplyAuth(context.Background(), "/a", "{}") }()

	const step = 5 * time.Second
	for range 2 * helper.DefaultLease / step {
		answered := link.control.LastAnswer()
		clock.Advance(step)
		for deadline := time.Now().Add(2 * time.Second); !link.control.LastAnswer().After(answered); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("main stopped answering pings")
			}
		}
	}
	close(release)
	if err := <-applied; err != nil {
		t.Fatal(err)
	}
}

// Main loses a worker whose link hangs after half a lease without an answer, long before the worker is gone.
func TestMainLosesAWorkerThatStopsPinging(t *testing.T) {
	t.Parallel()
	const lease = 4 * time.Second
	link, err := linkHelper(t, "v1", lease, helper.Options{Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	link.toMain.shut.Store(true)
	select {
	case <-link.control.Lost():
		last := link.control.LastAnswer()
		if lost := time.Since(last); lost < lease/2 || lost > lease/2+time.Second {
			t.Fatalf("lost %s after the last answer", lost)
		}
		if !link.control.GoneAt().Equal(last.Add(lease + worker.GoneMargin)) {
			t.Fatalf("gone at %v, last answer %v", link.control.GoneAt(), last)
		}
	case <-time.After(lease):
		t.Fatal("main did not lose the worker")
	}
	select {
	case <-link.control.Gone():
		t.Fatal("the worker is gone when main loses it")
	default:
	}
}

func TestMainLosesAWorkerWhoseHelperEnded(t *testing.T) {
	link, err := linkHelper(t, "v1", helper.DefaultLease, helper.Options{Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	link.toHelper.Close()
	select {
	case <-link.control.Lost():
	case <-time.After(2 * time.Second):
		t.Fatal("main did not lose the worker whose helper ended")
	}
	select {
	case <-link.control.Gone():
		t.Fatal("a closed connection made the worker gone")
	default:
	}
}

// The helper must stop before main treats the worker as gone, whichever direction of the link stops working.
func TestWorkerIsGoneOnlyAfterTheHelperStopped(t *testing.T) {
	const lease = 500 * time.Millisecond
	for _, direction := range []string{"to main", "to the helper"} {
		t.Run(direction, func(t *testing.T) {
			t.Parallel()
			link, err := linkHelper(t, "v1", lease, helper.Options{Version: "v1"})
			if err != nil {
				t.Fatal(err)
			}
			if direction == "to main" {
				link.toMain.shut.Store(true)
			} else {
				link.toHelper.shut.Store(true)
			}

			var stopped time.Time
			select {
			case err := <-link.served:
				link.served <- err
				stopped = time.Now()
				if !errors.Is(err, helper.ErrLeaseExpired) {
					t.Fatalf("the helper stopped with %v", err)
				}
			case <-link.control.Gone():
				t.Fatal("the worker is gone while its helper runs")
			case <-time.After(lease + 2*time.Second):
				t.Fatal("the helper did not stop")
			}
			select {
			case <-link.control.Gone():
				if gone := time.Now(); gone.Before(stopped) || gone.Before(link.control.LastAnswer().Add(lease+worker.GoneMargin)) {
					t.Fatalf("gone at %v, helper stopped at %v, last answer %v", gone, stopped, link.control.LastAnswer())
				}
			case <-time.After(lease + worker.GoneMargin + time.Second):
				t.Fatal("the worker is not gone")
			}
		})
	}
}
