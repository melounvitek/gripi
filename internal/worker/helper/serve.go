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
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/melounvitek/gripi/internal/worker/dockerproxy"
	"golang.org/x/sys/unix"
)

// Message is one line of the control protocol, newline-delimited JSON in both directions. Each side numbers its own
// requests and answers the other's with a Message that has the request's ID and no type:
//
//   - helper to main: {"type":"hello","instance":…,"version":…,"user":…,"docker":…} once, then {"id":N,"type":"ping"}
//     every second;
//   - main to helper: {"id":N,"type":"session.create","session":…,"cwd":…,"args":[…],"env":["NAME=value",…]},
//     session.close and session.release with "session", and auth.apply with "path" and "contents";
//   - main to helper, for a session that moves to the worker on a private copy of its repository (see move.go):
//     move.prepare with "move", "repository", "roots" and "private", answered with the copy's folder as "path";
//     move.seed, move.release, answered with "barriers", and move.delete with "move"; session.create with "move"
//     too; and mirror.start with "move", and the session's file as "file" for the session file's mirror;
//   - main to helper, for a command that gripi sudo runs as root (see sudo.go): sudo.offer with "session", "token",
//     "password", "cwd" and "args", answered with the session's cgroup as "path" and what sudo runs as "args";
//   - answers: {"id":N,"ok":true} or {"id":N,"ok":false,"error":"…"}.
//
// The streams that main opens to the helper start with a Message too, which names the helper's instance.
type Message struct {
	ID       uint64   `json:"id,omitempty"`
	Type     string   `json:"type,omitempty"`
	Instance string   `json:"instance,omitempty"`
	Version  string   `json:"version,omitempty"`
	Session  string   `json:"session,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	Args     []string `json:"args,omitempty"`
	Env      []string `json:"env,omitempty"`
	Path     string   `json:"path,omitempty"`
	To       string   `json:"to,omitempty"`
	Contents string   `json:"contents,omitempty"`
	OK       bool     `json:"ok,omitempty"`
	Error    string   `json:"error,omitempty"`
	// User is whom the sessions run as. Docker is "available" when they can use Docker, and otherwise says why they
	// can't and what an admin of the worker can do; it is empty when the helper has no Docker endpoint.
	User   string `json:"user,omitempty"`
	Docker string `json:"docker,omitempty"`
	// Token names the Password of a command that gripi sudo runs as root.
	Token    string `json:"token,omitempty"`
	Password string `json:"password,omitempty"`

	Move       string    `json:"move,omitempty"`
	Repository string    `json:"repository,omitempty"`
	Roots      []string  `json:"roots,omitempty"`
	Private    []string  `json:"private,omitempty"`
	File       string    `json:"file,omitempty"`
	Barriers   *Barriers `json:"barriers,omitempty"`
}

// Response answers a request, with "ok" even when it is false.
type Response struct {
	ID       uint64    `json:"id"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Path     string    `json:"path,omitempty"`
	Args     []string  `json:"args,omitempty"`
	Barriers *Barriers `json:"barriers,omitempty"`
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
	// mirrors of the session's move, and fails those of other sessions.
	Mirror Mirror
	// ApplyAuth writes a login file for auth.apply. Without it, auth.apply fails.
	ApplyAuth func(path, contents string) error
	// Clock is CLOCK_BOOTTIME when nil. It must keep counting while the machine is suspended, so a lease can't
	// outlast what main allows.
	Clock func() time.Duration

	// Home is main's home folder, and OwnHome the worker user's, as Docker sees it; Home when empty. DataDir holds
	// the moves' copies, in copies/, and the ignored folders that deleted copies left, in ignored/, at the path that
	// Docker sees too. Moves need Home and DataDir.
	Home, OwnHome, DataDir string
	// DockerUpstream is the Docker endpoint that each move's Docker socket reaches, if any.
	DockerUpstream string
	// Name is main's name for the worker, which the helper's messages give.
	Name string
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
	moves    map[string]*move
	// passwords wait for the askpass program of the sudo of a command that gripi sudo runs, by its token.
	passwords map[string][]byte

	cache sync.Mutex // held while moves take ignored folders from earlier copies or leave them
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
		pings: map[uint64]time.Duration{}, renewed: options.Clock(), sessions: map[string]*session{}, moves: map[string]*move{},
		passwords: map[string][]byte{},
	}
	if s.options.Mirror == nil {
		s.options.Mirror = s
	}
	receives, err := net.Listen("unix", filepath.Join(options.RuntimeDir, receiveSocket))
	if err != nil {
		return err
	}
	defer receives.Close()
	go s.serveReceives(ctx, receives)
	askpass, err := net.Listen("unix", filepath.Join(options.RuntimeDir, askpassSocket))
	if err != nil {
		return err
	}
	defer askpass.Close()
	go http.Serve(askpass, http.HandlerFunc(s.answerAskpass))
	defer close(s.done)
	go s.write(output)
	closed := make(chan struct{})
	go s.read(input, closed)
	s.send(Message{Type: "hello", Instance: s.instance, Version: options.Version, User: currentUser(), Docker: s.docker(ctx)})
	err = s.keepLease(ctx, closed)
	s.mu.Lock()
	s.stopping = true
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	cgroups := s.forget(ids...)
	for _, m := range s.moves {
		cgroups = append(cgroups, m.cgroup(s.options.Cgroup))
	}
	s.mu.Unlock()
	if err := killCgroups(stopTimeout, cgroups...); err != nil {
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

// docker says whether the sessions can use Docker, as the hello does.
func (s *server) docker(ctx context.Context) string {
	if s.options.DockerUpstream == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := dockerproxy.Check(ctx, s.options.DockerUpstream); err != nil {
		return dockerproxy.Unusable(s.options.Name, currentUser(), err)
	}
	return "available"
}

func currentUser() string {
	if current, err := user.Current(); err == nil {
		return current.Username
	}
	return strconv.Itoa(os.Getuid())
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
		if request.Move != "" {
			err = s.createInMove(request)
		} else {
			err = s.create(request, nil)
		}
	case "mirror.start":
		err = s.startMoveMirror(request.Move, request.File)
	case "move.prepare":
		var copy string
		copy, err = s.prepare(request)
		if err == nil {
			s.send(Response{ID: request.ID, OK: true, Path: copy})
			return
		}
	case "move.seed":
		err = s.seed(request.Move)
	case "move.release":
		var barriers Barriers
		barriers, err = s.releaseMove(request.Move)
		if err == nil {
			s.send(Response{ID: request.ID, OK: true, Barriers: &barriers})
			return
		}
	case "move.delete":
		err = s.deleteMove(request.Move)
	case "sudo.offer":
		var cgroup string
		var args []string
		if cgroup, args, err = s.offerSudo(request); err == nil {
			s.send(Response{ID: request.ID, OK: true, Path: cgroup, Args: args})
			return
		}
	case "session.close":
		err = s.close(request.Session)
	case "session.release":
		err = s.release(releaseTimeout, request.Session)
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
