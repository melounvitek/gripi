// Package helper is Gripi's helper on a worker: it runs each session's Pi in a cgroup of its own and stops them all
// when main stops answering its pings.
package helper

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/melounvitek/gripi/internal/worker/mirror"
	"golang.org/x/sys/unix"
)

// Message is one line of the control protocol, newline-delimited JSON in both directions. Each side numbers its own
// requests and answers the other's with a Message that has the request's ID and no type:
//
//   - helper to main: {"type":"hello","instance":…,"version":…} once, then {"id":N,"type":"ping"} every second;
//   - main to helper: {"id":N,"type":"session.create","session":…,"cwd":…,"args":[…],"env":["NAME=value",…]},
//     session.close and session.release with "session", auth.apply with "path" and "contents", mirror.start with
//     "unit" ({"main":…,"worktrees":[…],"git_dir":…}), and for a unit of Pi's session folders "sessions" and the
//     main folder of their sessions' repository as "path", and mirror.stop with the unit's main folder as "path";
//   - answers: {"id":N,"ok":true} or {"id":N,"ok":false,"error":"…"}.
type Message struct {
	ID       uint64       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Instance string       `json:"instance,omitempty"`
	Version  string       `json:"version,omitempty"`
	Session  string       `json:"session,omitempty"`
	Cwd      string       `json:"cwd,omitempty"`
	Args     []string     `json:"args,omitempty"`
	Env      []string     `json:"env,omitempty"`
	Path     string       `json:"path,omitempty"`
	Contents string       `json:"contents,omitempty"`
	Unit     *mirror.Unit `json:"unit,omitempty"`
	Sessions bool         `json:"sessions,omitempty"`
	OK       bool         `json:"ok,omitempty"`
	Error    string       `json:"error,omitempty"`
}

// Response answers a request, with "ok" even when it is false.
type Response struct {
	ID    uint64 `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

const (
	// DefaultLease is how long sessions keep running after the send time of the latest ping that main answered.
	DefaultLease = 30 * time.Second
	// PingInterval is how often the helper pings main.
	PingInterval = time.Second
	// stopTimeout is how long the helper waits on its way out for the sessions' processes to die, once it has killed
	// them all. With it, the helper exits about a second after the lease ends, before main takes the sessions over
	// (worker.GoneMargin).
	stopTimeout = time.Second
)

// ErrLeaseExpired is why the helper stops when main has not answered a ping for a whole lease.
var ErrLeaseExpired = errors.New("main has not answered for a whole lease")

// Options configure Serve.
type Options struct {
	// RuntimeDir holds the helper's lock and the sessions' sockets.
	RuntimeDir string
	// Version is the helper's version, which main compares with its own.
	Version string
	// Cgroup is the cgroup that systemd delegated to the helper (see DelegatedCgroup). Without it, sessions fail.
	Cgroup string
	// Lease is DefaultLease when zero.
	Lease time.Duration
	// Mirror answers the worker extension's barriers and health checks. Without it, the helper answers them from the
	// mirrors (mirror.start) of the unit that holds the session's folder and of that unit's sessions' folders, and
	// fails barriers of other sessions.
	Mirror Mirror
	// ApplyAuth writes a login file for auth.apply. Without it, auth.apply fails.
	ApplyAuth func(path, contents string) error
	// Clock is CLOCK_BOOTTIME when nil. It must keep counting while the machine is suspended, so a lease can't
	// outlast what main allows.
	Clock func() time.Duration
}

type server struct {
	options  Options
	instance string
	output   chan any
	done     chan struct{}

	mu       sync.Mutex
	pings    map[uint64]time.Duration // send times of the pings main has not answered yet
	renewed  time.Duration            // the lease runs from here
	answered bool
	stopping bool // no session starts once Serve has started to kill them
	sessions map[string]*session
	mirrors  map[string]*mirroredUnit // by the unit's main folder
}

// Serve runs the control protocol on input and output. It returns nil when main closes input, ErrLeaseExpired when main
// stops answering, or ctx's error, after killing every session's processes.
func Serve(ctx context.Context, input io.Reader, output io.Writer, options Options) error {
	if options.Lease == 0 {
		options.Lease = DefaultLease
	}
	if options.Clock == nil {
		options.Clock = bootTime
	}
	unlock, err := lockRuntimeDir(options.RuntimeDir)
	if err != nil {
		return err
	}
	defer unlock()
	instance := make([]byte, 8)
	rand.Read(instance)
	s := &server{
		options: options, instance: hex.EncodeToString(instance), output: make(chan any, 64), done: make(chan struct{}),
		pings: map[uint64]time.Duration{}, renewed: options.Clock(), sessions: map[string]*session{}, mirrors: map[string]*mirroredUnit{},
	}
	if s.options.Mirror == nil {
		s.options.Mirror = s
	}
	mirrors, err := net.Listen("unix", filepath.Join(options.RuntimeDir, mirrorSocket))
	if err != nil {
		return err
	}
	defer mirrors.Close()
	go s.serveMirrors(mirrors)
	defer close(s.done)
	go s.write(output)
	closed := make(chan struct{})
	go s.read(input, closed)
	s.send(Message{Type: "hello", Instance: s.instance, Version: options.Version})
	err = s.keepLease(ctx, closed)
	s.stopMirrors()
	s.mu.Lock()
	s.stopping = true
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	if err := s.release(stopTimeout, ids...); err != nil {
		log.Printf("stop the sessions: %v", err)
	}
	return err
}

func lockRuntimeDir(dir string) (unlock func(), err error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "serve.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("another helper serves %s", dir)
		}
		return nil, err
	}
	// Sockets left by a helper that crashed would keep new ones from listening.
	stale, _ := filepath.Glob(filepath.Join(dir, "*.sock"))
	attach, _ := filepath.Glob(filepath.Join(dir, "*.attach"))
	for _, path := range append(stale, attach...) {
		os.Remove(path)
	}
	return func() { lock.Close() }, nil
}

// keepLease pings main every second and returns when the lease expires, main closes the connection or ctx ends.
// Shortly before the lease expires, it asks each Pi to stop, so Pi can stop what it runs.
func (s *server) keepLease(ctx context.Context, closed <-chan struct{}) error {
	alive := watchdog()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var ping uint64
	var nextPing, terminated time.Duration
	for {
		now := s.options.Clock()
		if now >= nextPing {
			ping++
			s.mu.Lock()
			s.pings[ping] = now
			s.mu.Unlock()
			// A link that doesn't take pings must not hold the lease up.
			select {
			case s.output <- Message{ID: ping, Type: "ping"}:
			default:
			}
			nextPing = now + PingInterval
			alive()
		}
		s.mu.Lock()
		expiry := s.renewed + s.options.Lease
		s.mu.Unlock()
		if now >= expiry {
			return ErrLeaseExpired
		}
		if now >= expiry-s.options.Lease/6 && terminated != expiry {
			terminated = expiry
			s.signalPis(syscall.SIGTERM)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closed:
			return nil
		case <-ticker.C:
		}
	}
}

func (s *server) read(input io.Reader, closed chan<- struct{}) {
	defer close(closed)
	decoder := json.NewDecoder(input)
	for {
		var message Message
		if err := decoder.Decode(&message); err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("read from main: %v", err)
			}
			return
		}
		if message.Type == "" {
			s.answer(message.ID)
		} else {
			go s.handle(message)
		}
	}
}

func (s *server) answer(ping uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sent, found := s.pings[ping]
	if !found {
		return
	}
	for id := range s.pings {
		if id <= ping {
			delete(s.pings, id)
		}
	}
	s.renewed = max(s.renewed, sent)
	s.answered = true
}

func (s *server) handle(request Message) {
	var err error
	switch request.Type {
	case "session.create":
		err = s.create(request)
	case "session.close":
		err = s.close(request.Session)
	case "session.release":
		err = s.release(releaseTimeout, request.Session)
	case "mirror.start":
		err = s.startMirror(request.Unit, request.Sessions, request.Path)
	case "mirror.stop":
		err = s.stopMirror(request.Path)
	case "auth.apply":
		if s.options.ApplyAuth == nil {
			err = errors.New("this helper cannot write logins")
		} else {
			err = s.options.ApplyAuth(request.Path, request.Contents)
		}
	default:
		err = fmt.Errorf("unknown request %q", request.Type)
	}
	s.send(response(request.ID, err))
}

func response(id uint64, err error) Response {
	if err != nil {
		return Response{ID: id, Error: err.Error()}
	}
	return Response{ID: id, OK: true}
}

func (s *server) send(message any) {
	select {
	case s.output <- message:
	case <-s.done:
	}
}

func (s *server) write(output io.Writer) {
	encoder := json.NewEncoder(output)
	for {
		select {
		case message := <-s.output:
			// Keep taking messages after a failed write, so nothing waits for a dead link.
			encoder.Encode(message)
		case <-s.done:
			return
		}
	}
}

// watchdog returns a function that tells systemd the helper still works (WatchdogSec), or does nothing outside systemd.
func watchdog() func() {
	address := os.Getenv("NOTIFY_SOCKET")
	// Pi must not inherit them.
	for _, name := range []string{"NOTIFY_SOCKET", "WATCHDOG_PID", "WATCHDOG_USEC"} {
		os.Unsetenv(name)
	}
	if address == "" {
		return func() {}
	}
	connection, err := net.Dial("unixgram", address)
	if err != nil {
		log.Printf("cannot notify systemd: %v", err)
		return func() {}
	}
	return func() { connection.Write([]byte("WATCHDOG=1")) }
}

// DefaultRuntimeDir is $XDG_RUNTIME_DIR/gripi-worker.
func DefaultRuntimeDir() (string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return "", errors.New("XDG_RUNTIME_DIR is not set")
	}
	return filepath.Join(runtime, "gripi-worker"), nil
}

// FileVersion is the version of a helper executable: the SHA-256 of its contents.
func FileVersion(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
