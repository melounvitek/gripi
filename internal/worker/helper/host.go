package helper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/melounvitek/gripi/internal/worker/dockerproxy"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// hostConfig is what a move's host needs to know, which the helper sends as the first line of the host's input.
type hostConfig struct {
	Move       string              `json:"move"`
	Instance   string              `json:"instance"` // the helper's, which main's mirror channels name
	RuntimeDir string              `json:"runtime_dir"`
	Home       string              `json:"home"`  // main's home folder
	Copy       string              `json:"copy"`  // the move's copy, outside the view's private roots
	Seeds      string              `json:"seeds"` // a folder on the copy's file system for seeding it
	Unit       mirror.Unit         `json:"unit"`  // the copy's folders, at main's paths
	Docker     dockerproxy.Options `json:"docker"`
}

// socket is where the host takes the helper's requests and main's mirror channels, which start with a Message.
func (config hostConfig) socket() string {
	return filepath.Join(config.RuntimeDir, config.Move+".move.sock")
}

// hostServer runs a move's mirrors in the move's view.
type hostServer struct {
	config hostConfig
	mu     sync.Mutex
	// The mirrors of the copy and of the session's file, once mirror.start has prepared them.
	repository, session *mirroredUnit
}

// Host runs a move's mirrors and Docker socket in the move's view, as `gripi worker move-host` does there for the
// helper. It reads its configuration from the first line of input, says "ready" on output once it takes requests,
// and runs until input ends, as when the helper stops.
func Host(input io.Reader, output io.Writer) error {
	lines := bufio.NewReader(input)
	line, err := lines.ReadBytes('\n')
	var config hostConfig
	if err == nil {
		err = json.Unmarshal(line, &config)
	}
	if err != nil {
		return fmt.Errorf("read the move's configuration: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := &hostServer{config: config}
	defer host.stop()
	os.Remove(config.socket())
	listener, err := net.Listen("unix", config.socket())
	if err != nil {
		return err
	}
	defer listener.Close()
	if config.Docker.Upstream != "" {
		os.Remove(config.Docker.Listen)
		go func() {
			if err := dockerproxy.Serve(ctx, config.Docker); ctx.Err() == nil {
				log.Printf("serve Docker for move %s: %v", config.Move, err)
			}
		}()
	}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go host.handle(ctx, connection)
		}
	}()
	if _, err := fmt.Fprintln(output, "ready"); err != nil {
		return err
	}
	io.Copy(io.Discard, lines)
	return nil
}

func (host *hostServer) stop() {
	host.mu.Lock()
	defer host.mu.Unlock()
	for _, unit := range []*mirroredUnit{host.repository, host.session} {
		if unit != nil {
			unit.cancel()
		}
	}
}

// handle answers one request of the helper, or runs a mirror for main's channel.
func (host *hostServer) handle(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	request, reader, err := readHello(connection)
	if err != nil {
		return
	}
	if request.Type == "mirror" {
		host.runMirror(request, reader, connection)
		return
	}
	// A barrier ends when the helper stops waiting for it.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		reader.ReadByte()
		cancel()
	}()
	var answer Response
	switch request.Type {
	case "mirror.start":
		err = host.start(request.File)
	case "barrier":
		repository, session := host.sources()
		err = bothBarriers(ctx, repository, session)
	case "health":
		answer.OK = bothHealthy(host.sources())
		json.NewEncoder(connection).Encode(answer)
		return
	case "release":
		answer.Barriers, err = host.release()
	case "seed":
		err = host.seed(request.Path)
	case "stash":
		err = host.stash(request.Path)
	default:
		err = fmt.Errorf("unknown request %q", request.Type)
	}
	answer.OK = err == nil
	if err != nil {
		answer.Error = err.Error()
	}
	json.NewEncoder(connection).Encode(answer)
}

// start prepares the mirror of the copy, or with file, of the session's file, which needs its folder to exist.
func (host *hostServer) start(file string) error {
	unit, options, slot := host.config.Unit, mirror.Options{}, &host.repository
	if file != "" {
		if !filepath.IsAbs(file) || filepath.Clean(file) != file || !strings.HasSuffix(file, ".jsonl") {
			return fmt.Errorf("%q is no session file", file)
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return err
		}
		unit, options, slot = mirror.Unit{Main: filepath.Dir(file), File: file}, mirror.Options{AutoBarrier: true, CompleteLines: true}, &host.session
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if current := *slot; current != nil {
		// A Source that never got its channel, or whose channel has ended, makes way for a new one.
		select {
		case <-current.done:
		default:
			if current.attached {
				return fmt.Errorf("move %s mirrors %s already", host.config.Move, current.unit.Main)
			}
		}
		current.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	*slot = &mirroredUnit{unit: unit, source: mirror.NewSource(unit, options), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	return nil
}

func (host *hostServer) sources() (repository, session *mirror.Source) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.repository != nil {
		repository = host.repository.source
	}
	if host.session != nil {
		session = host.session.source
	}
	return repository, session
}

// runMirror runs a mirror over main's channel, which named it and the helper's instance in its first line.
func (host *hostServer) runMirror(hello Message, reader io.Reader, connection net.Conn) {
	host.mu.Lock()
	current := host.repository
	if hello.File != "" {
		current = host.session
	}
	var err error
	switch {
	case hello.Instance != host.config.Instance:
		err = fmt.Errorf("the helper on the worker is %s, not %s", host.config.Instance, hello.Instance)
	case current == nil || current.unit.File != hello.File:
		err = fmt.Errorf("move %s doesn't mirror %q", host.config.Move, hello.File)
	case current.attached:
		err = fmt.Errorf("move %s has a mirror channel for %q already", host.config.Move, hello.File)
	default:
		current.attached = true
	}
	host.mu.Unlock()
	if err != nil {
		replyHello(connection, err)
		return
	}
	current.run(connection, reader, "move "+host.config.Move)
}

// release has main take the last barrier of each mirror, then stops them, once they have stopped. A mirror that
// never started has no barrier to take, and without mirrors, there are no barriers.
func (host *hostServer) release() (*Barriers, error) {
	host.mu.Lock()
	units := []*mirroredUnit{host.repository, host.session}
	if units[0] == nil && units[1] == nil {
		host.mu.Unlock()
		return nil, nil
	}
	attached := []bool{host.repository != nil && host.repository.attached, host.session != nil && host.session.attached}
	host.repository, host.session = nil, nil
	host.mu.Unlock()
	barriers := make([]uint64, len(units))
	errs := make([]error, len(units))
	var finished sync.WaitGroup
	for i, unit := range units {
		if unit == nil {
			continue
		}
		finished.Go(func() {
			if !attached[i] {
				unit.cancel()
				errs[i] = fmt.Errorf("main's copy of %s isn't connected, so it may lack the last changes", unit.unit.Main)
				return
			}
			ctx, cancel := context.WithTimeout(unit.ctx, finalBarrierTimeout)
			barriers[i], _, errs[i] = unit.source.Barrier(ctx)
			cancel()
			unit.cancel()
			<-unit.done
		})
	}
	finished.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &Barriers{Repository: barriers[0], Session: barriers[1]}, nil
}

// at returns where a folder like the copy has path, one of main's.
func (host *hostServer) at(folder, path string) string {
	return filepath.Join(folder, strings.TrimPrefix(path, host.config.Home))
}

// seed copies the folders that Git ignores into the copy, where it lacks them, from the same paths in from: an
// earlier copy of the repository, or what deleted copies left. Where the file system can, the copies share their
// contents with the originals until either changes.
func (host *hostServer) seed(from string) error {
	if err := os.RemoveAll(host.config.Seeds); err != nil {
		return err
	}
	if err := os.MkdirAll(host.config.Seeds, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(host.config.Seeds)
	return mirror.WalkMirrored(host.config.Unit, func(folder string, ignored func(string) bool) error {
		entries, err := os.ReadDir(host.at(from, folder))
		if err != nil {
			return nil // from lacks it
		}
		for _, entry := range entries {
			path := filepath.Join(folder, entry.Name())
			if _, err := os.Lstat(path); !entry.IsDir() || !errors.Is(err, fs.ErrNotExist) || !ignored(path) {
				continue
			}
			// A copy cut short stays outside the copy.
			staged := filepath.Join(host.config.Seeds, entry.Name())
			if output, err := exec.Command("cp", "-a", "--reflink=auto", host.at(from, path), staged).CombinedOutput(); err != nil {
				return fmt.Errorf("copy %s: %w: %s", host.at(from, path), err, output)
			}
			if err := os.Rename(staged, host.at(host.config.Copy, path)); err != nil {
				return err
			}
		}
		return nil
	})
}

// stash moves the folders in the copy that Git ignores to the same paths in cache, in place of what it had there.
func (host *hostServer) stash(cache string) error {
	return mirror.WalkMirrored(host.config.Unit, func(folder string, ignored func(string) bool) error {
		entries, err := os.ReadDir(folder)
		if err != nil {
			return nil
		}
		for _, entry := range entries {
			path := filepath.Join(folder, entry.Name())
			if !entry.IsDir() || !ignored(path) {
				continue
			}
			cached := host.at(cache, path)
			if err := os.RemoveAll(cached); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(cached), 0o700); err != nil {
				return err
			}
			// The copy's own path, because a rename can't leave the view's private roots.
			if err := os.Rename(host.at(host.config.Copy, path), cached); err != nil {
				return err
			}
		}
		return nil
	})
}
