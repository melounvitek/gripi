package helper

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/worker/dockerproxy"
	"github.com/melounvitek/gripi/internal/worker/mirror"
	"github.com/melounvitek/gripi/internal/worker/view"
)

// A move is a session that runs on the worker on a private copy of its repository, which main uploads into the
// move's folder in DataDir/copies and takes back when the session returns. Everything of the move runs in its view
// (view.Move), where its private roots, the folders directly in main's home folder that hold the repository, show
// the copy: the session's Pi with what Pi starts, in the session's cgroup, and the move's host, in a cgroup of its
// own, which runs the mirrors of the copy and of the session's file and the session's Docker socket.

// move is what the helper knows of a move. Its exported fields are kept next to its copy, so that a later helper
// knows the move too.
type move struct {
	Repository string    `json:"repository,omitempty"` // the repository's common Git folder, empty outside Git
	Roots      []string  `json:"roots"`                // the copy's worktrees, the session's own first
	Private    []string  `json:"private"`
	Created    time.Time `json:"created"`

	id string
	// Guarded by server.mu: every session that started in the move, and whether it is closed to new ones, as once
	// released or deleted.
	sessions []string
	closed   bool

	mu   sync.Mutex // held while the host starts, answers move.release or stops
	host *hostProcess
	// What the last move.release that reached the host answered, for a retry.
	released    Barriers
	releaseFail error
}

// Barriers are the last barriers that main applied of a move's mirrors, which move.release answers; 0 for a mirror
// that never started.
type Barriers struct {
	Repository uint64 `json:"repository"`
	Session    uint64 `json:"session"`
}

func (m *move) cgroup(cgroup string) string { return filepath.Join(cgroup, "move-"+m.id) }

func (m *move) sameFolders(other *move) bool {
	return m.Repository == other.Repository && slices.Equal(m.Roots, other.Roots) && slices.Equal(m.Private, other.Private)
}

// check refuses private roots that aren't folders directly in home or that hold one of keep, and folders of the copy
// outside the private roots.
func (m *move) check(home string, keep ...string) error {
	if len(m.Private) == 0 {
		return errors.New("a move needs its private roots")
	}
	for _, root := range m.Private {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(root) != filepath.Clean(home) {
			return fmt.Errorf("the private root %q is not a folder directly in main's home folder %s", root, home)
		}
		if i := slices.IndexFunc(keep, func(folder string) bool { return within(folder, root) }); i >= 0 {
			return fmt.Errorf("the private root %s holds Gripi's folder %s", root, keep[i])
		}
	}
	if len(m.Roots) == 0 {
		return errors.New("a move needs the folders of its copy")
	}
	for _, folder := range m.Roots {
		if !filepath.IsAbs(folder) || filepath.Clean(folder) != folder || !m.inPrivate(folder) {
			return fmt.Errorf("the folder %q is outside the move's private roots", folder)
		}
	}
	if m.Repository != "" && !m.inPrivate(m.Repository) {
		return fmt.Errorf("the Git folder %q is outside the move's private roots", m.Repository)
	}
	return nil
}

func (m *move) inPrivate(path string) bool {
	return slices.ContainsFunc(m.Private, func(root string) bool { return within(path, root) })
}

func within(path, folder string) bool { return path == folder || strings.HasPrefix(path, folder+"/") }

func (s *server) copyFolder(id string) string { return filepath.Join(s.options.DataDir, "copies", id) }

// cacheOf holds the ignored folders that deleted copies of the move's repository left.
func (s *server) cacheOf(m *move) string {
	sum := sha256.Sum256([]byte(m.Repository))
	return filepath.Join(s.options.DataDir, "ignored", hex.EncodeToString(sum[:8]))
}

func (s *server) view(m *move) view.Move {
	return view.Move{Home: s.options.Home, Copy: s.copyFolder(m.id), Private: m.Private}
}

// prepare makes the folder of a move's copy, if an earlier request hasn't, and returns it.
func (s *server) prepare(request Message) (string, error) {
	if err := checkID("move", request.Move); err != nil {
		return "", err
	}
	if s.options.Home == "" || s.options.DataDir == "" {
		return "", errors.New("this helper runs no moves: it lacks main's home folder or its data folder")
	}
	prepared := &move{Repository: request.Repository, Roots: request.Roots, Private: request.Private, Created: time.Now(), id: request.Move}
	// In the view of main's home folder, the data folder is in main's home folder too.
	keep := []string{s.options.DataDir}
	if rest, found := strings.CutPrefix(s.options.DataDir, s.options.OwnHome+"/"); found && s.options.OwnHome != "" {
		keep = append(keep, filepath.Join(s.options.Home, rest))
	}
	if err := prepared.check(s.options.Home, keep...); err != nil {
		return "", err
	}
	copy := s.copyFolder(prepared.id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return "", errors.New("the helper is stopping")
	}
	known, err := s.knownMove(prepared.id)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if known != nil && !known.sameFolders(prepared) {
		return "", fmt.Errorf("move %s was prepared with other folders", prepared.id)
	}
	// The view shows the copy's folders at the private roots, which need folders to show them on. A mount on a link
	// would show them at its target, whose paths Git would give the worktrees that the session makes.
	for _, root := range prepared.Private {
		if target, err := os.Readlink(root); err == nil {
			return "", fmt.Errorf("%s in the worker user's home folder is a symbolic link to %s; a move needs a folder there to show its copy of %s", filepath.Base(root), target, root)
		}
	}
	for _, root := range prepared.Private {
		for _, folder := range []string{filepath.Join(copy, filepath.Base(root)), root} {
			if err := os.MkdirAll(folder, 0o700); err != nil {
				return "", err
			}
		}
	}
	if known == nil {
		data, err := json.Marshal(prepared)
		if err == nil {
			err = writeAtomically(copy+".json", data)
		}
		if err != nil {
			return "", err
		}
		s.moves[prepared.id] = prepared
	}
	return copy, nil
}

func writeAtomically(path string, data []byte) error {
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// move returns a move that this helper or an earlier one prepared.
func (s *server) move(id string) (*move, error) {
	if err := checkID("move", id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.knownMove(id)
}

// knownMove is move with s.mu held.
func (s *server) knownMove(id string) (*move, error) {
	if known := s.moves[id]; known != nil {
		return known, nil
	}
	if s.options.DataDir == "" {
		return nil, errors.New("this helper runs no moves: it lacks its data folder")
	}
	data, err := os.ReadFile(s.copyFolder(id) + ".json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("move %s is not prepared: %w", id, err)
	}
	known := &move{id: id}
	if err == nil {
		err = json.Unmarshal(data, known)
	}
	if err != nil {
		return nil, fmt.Errorf("move %s: %w", id, err)
	}
	s.moves[id] = known
	return known, nil
}

// seed gives a move's copy the folders that Git ignores, such as node_modules, which main never sends, from the
// latest earlier copy of its repository on the worker, or else from those that deleted copies left.
func (s *server) seed(id string) error {
	m, err := s.move(id)
	if err != nil || m.Repository == "" {
		return err
	}
	s.cache.Lock()
	defer s.cache.Unlock()
	from := s.cacheOf(m)
	latest := time.Time{}
	paths, _ := filepath.Glob(filepath.Join(s.options.DataDir, "copies", "*.json"))
	for _, path := range paths {
		var other move
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &other) != nil || other.Repository != m.Repository {
			continue
		}
		if other.Created.Before(m.Created) && other.Created.After(latest) {
			latest, from = other.Created, strings.TrimSuffix(path, ".json")
		}
	}
	if _, err := os.Stat(from); err != nil {
		return nil
	}
	host, err := s.host(m)
	if err == nil {
		_, err = host.call(context.Background(), Message{Type: "seed", Path: from})
	}
	return err
}

// releaseMove keeps new sessions out of a move, kills every process of its sessions, then has main take the last
// barrier of each of the move's mirrors, which have kept running meanwhile, and stops them. A retry answers the same,
// unless the mirrors started again, as after a failed release, whose last barriers it takes then.
func (s *server) releaseMove(id string) (Barriers, error) {
	m, err := s.move(id)
	if err != nil {
		return Barriers{}, err
	}
	s.mu.Lock()
	running, cgroups := s.closeMove(m)
	s.forget(running...)
	s.mu.Unlock()
	if err := killCgroups(releaseTimeout, cgroups...); err != nil {
		return Barriers{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.host == nil {
		return m.released, m.releaseFail
	}
	answer, err := m.host.call(context.Background(), Message{Type: "release"})
	if stopped := s.stopHost(m); err == nil {
		err = stopped
	}
	// A host without mirrors, as one that a refused session started, has no last barriers to change.
	if err == nil && answer.Barriers == nil {
		return m.released, m.releaseFail
	}
	m.released, m.releaseFail = Barriers{}, err
	if err == nil {
		m.released = *answer.Barriers
	}
	return m.released, m.releaseFail
}

// closeMove keeps new sessions from starting in the move, with s.mu held, and returns those that run in it and the
// cgroups of every session that ran in it, whose processes may have outlived a release.
func (s *server) closeMove(m *move) (running, cgroups []string) {
	m.closed = true
	for _, id := range m.sessions {
		if current := s.sessions[id]; current != nil && current.move == m.id {
			running = append(running, id)
		}
		cgroups = append(cgroups, s.sessionCgroup(id, m.id))
	}
	return running, cgroups
}

// deleteMove deletes a released move's copy, once main has what it needs of it, and leaves the folders in it that Git
// ignores to the next copies of its repository.
func (s *server) deleteMove(id string) error {
	m, err := s.move(id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	running, cgroups := s.closeMove(m)
	s.mu.Unlock()
	if len(running) > 0 {
		return fmt.Errorf("move %s still runs sessions; release it first", id)
	}
	// What an earlier release failed to kill, while the mirrors still run for the release that main tries again, then
	// what an earlier stop of the host failed to kill.
	if err := killCgroups(releaseTimeout, cgroups...); err != nil {
		return err
	}
	m.mu.Lock()
	err = s.stopHost(m)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	s.cache.Lock()
	defer s.cache.Unlock()
	copy := s.copyFolder(id)
	// A copy that never got its repository, as after a failed move, has nothing that Git ignores.
	if _, err := os.Stat(filepath.Join(copy, strings.TrimPrefix(m.Repository, s.options.Home))); m.Repository != "" && err == nil {
		host, err := s.host(m)
		if err == nil {
			_, err = host.call(context.Background(), Message{Type: "stash", Path: s.cacheOf(m)})
		}
		if err != nil {
			return err
		}
	}
	m.mu.Lock()
	err = s.stopHost(m)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	// Without its description, the copy is no longer one to seed from.
	if err := os.Remove(copy + ".json"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.mu.Lock()
	delete(s.moves, id)
	s.mu.Unlock()
	return errors.Join(os.RemoveAll(copy), os.RemoveAll(s.seeds(id)))
}

func (s *server) seeds(id string) string { return filepath.Join(s.options.DataDir, "seeds", id) }

// dockerSocket is the DOCKER_HOST of a move's sessions, which its host serves.
func (s *server) dockerSocket(m *move) string {
	return filepath.Join(s.options.RuntimeDir, m.id+".docker.sock")
}

// createInMove starts Pi for a session of a move, in the move's view, where the session's folder must be.
func (s *server) createInMove(request Message) error {
	m, err := s.move(request.Move)
	if err != nil {
		return err
	}
	if !m.inPrivate(request.Cwd) {
		return fmt.Errorf("the session's folder %q is outside the move's private roots", request.Cwd)
	}
	// Its Docker socket.
	if _, err := s.host(m); err != nil {
		return err
	}
	return s.create(request, m)
}

func (s *server) startMoveMirror(id, file string) error {
	m, err := s.move(id)
	if err != nil {
		return err
	}
	host, err := s.host(m)
	if err == nil {
		_, err = host.call(context.Background(), Message{Type: "mirror.start", File: file})
	}
	return err
}

// hostOf returns the host of the move of a session, which runs the mirrors that its barriers need.
func (s *server) hostOf(session string) (*hostProcess, error) {
	s.mu.Lock()
	current := s.sessions[session]
	var m *move
	if current != nil && current.move != "" {
		m = s.moves[current.move]
	}
	s.mu.Unlock()
	if m == nil {
		return nil, errors.New("nothing mirrors this session's folder to main")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.host == nil {
		return nil, errors.New("nothing mirrors this session's folder to main")
	}
	return m.host, nil
}

// hostProcess is a move's host, `gripi worker move-host`, which runs in the move's view (Host).
type hostProcess struct {
	socket string
	input  io.Closer // its end makes the host stop
	exited chan struct{}
}

// hostStart bounds the start of a move's view and its host.
const hostStart = 30 * time.Second

// host returns the move's host, which it starts unless it runs.
func (s *server) host(m *move) (*hostProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.host != nil {
		select {
		case <-m.host.exited:
			m.host = nil
		default:
			return m.host, nil
		}
	}
	s.mu.Lock()
	stopping := s.stopping
	s.mu.Unlock()
	if stopping {
		return nil, errors.New("the helper is stopping")
	}
	if s.options.Cgroup == "" {
		return nil, errors.New("moves need a cgroup that systemd delegated to the helper (systemd-run --user -p Delegate=yes)")
	}
	config := hostConfig{
		Move: m.id, Instance: s.instance, RuntimeDir: s.options.RuntimeDir, Home: s.options.Home, Copy: s.copyFolder(m.id), Seeds: s.seeds(m.id),
		Unit:   mirror.Unit{Main: m.Roots[0], Worktrees: m.Roots[1:], GitDir: m.Repository, Within: m.Private},
		Docker: dockerproxy.Options{Listen: s.dockerSocket(m), Upstream: s.options.DockerUpstream, Worker: s.options.Name, User: currentUser()},
	}
	// Docker sees the copy and the worker user's home folder where the view shows them at main's paths.
	for _, root := range m.Private {
		config.Docker.Mappings = append(config.Docker.Mappings, dockerproxy.Mapping{From: root, To: filepath.Join(config.Copy, filepath.Base(root))})
	}
	if s.options.OwnHome != "" && s.options.OwnHome != s.options.Home {
		config.Docker.Mappings = append(config.Docker.Mappings, dockerproxy.Mapping{From: s.options.Home, To: s.options.OwnHome})
	}
	command, err := s.view(m).Command("/", []string{"/proc/self/exe", "worker", "move-host"}, os.Environ())
	if err != nil {
		return nil, err
	}
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, outputWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer output.Close()
	stderr := &lastLine{}
	command.Stdout, command.Stderr = outputWriter, stderr
	err = startInCgroup(command, m.cgroup(s.options.Cgroup))
	outputWriter.Close()
	if err != nil {
		return nil, fmt.Errorf("start the view of move %s: %w", m.id, err)
	}
	process := &hostProcess{socket: config.socket(), input: input, exited: make(chan struct{})}
	go func() {
		command.Wait()
		close(process.exited)
	}()
	ready := make(chan error, 1)
	go func() {
		if err := json.NewEncoder(input).Encode(config); err != nil {
			ready <- err
			return
		}
		line, err := bufio.NewReader(output).ReadString('\n')
		if err == nil && line != "ready\n" {
			err = fmt.Errorf("it said %q", line)
		}
		ready <- err
	}()
	select {
	case err = <-ready:
	case <-time.After(hostStart):
		err = errors.New("it did not start in time")
	}
	if err != nil {
		killCgroups(releaseTimeout, m.cgroup(s.options.Cgroup))
		input.Close()
		<-process.exited
		return nil, fmt.Errorf("the view of move %s: %w %s", m.id, err, stderr)
	}
	m.host = process
	return process, nil
}

// stopHost kills the move's host, also what an earlier stop failed to kill, with m.mu held.
func (s *server) stopHost(m *move) error {
	err := killCgroups(releaseTimeout, m.cgroup(s.options.Cgroup))
	if m.host != nil {
		m.host.input.Close()
		if err == nil {
			<-m.host.exited
		}
		m.host = nil
	}
	return err
}

// call sends a request to the host and returns its answer, or its error.
func (process *hostProcess) call(ctx context.Context, request Message) (Response, error) {
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", process.socket)
	if err != nil {
		return Response{}, err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	var answer Response
	if err = json.NewEncoder(connection).Encode(request); err == nil {
		err = json.NewDecoder(connection).Decode(&answer)
	}
	switch {
	case ctx.Err() != nil:
		return answer, ctx.Err()
	case err != nil:
		return answer, fmt.Errorf("the move's view did not answer: %w", err)
	case answer.Error != "":
		return answer, errors.New(answer.Error)
	}
	return answer, nil
}

// lastLine passes what a process writes on to the helper's standard error, which main logs, and keeps its last line
// for errors.
type lastLine struct {
	mu   sync.Mutex
	last []byte
}

func (stderr *lastLine) Write(data []byte) (int, error) {
	os.Stderr.Write(data)
	stderr.mu.Lock()
	defer stderr.mu.Unlock()
	if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); lines[len(lines)-1] != "" {
		stderr.last = []byte(lines[len(lines)-1])
	}
	return len(data), nil
}

func (stderr *lastLine) String() string {
	stderr.mu.Lock()
	defer stderr.mu.Unlock()
	return string(stderr.last)
}
