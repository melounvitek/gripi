package helper_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// evalPi stands in for Pi in a move: it runs each line of its input as a command, in the move's view, and ends what
// the command printed with a line "--".
const evalPi = "#!/bin/sh\nwhile read -r line; do eval \"$line\"; echo --; done\n"

// moveHelper is a helper under systemd that runs moves, with main's side of them in this process. With
// GRIPI_TEST_VIEW_HOME, main's home folder differs from this user's, and the helper runs in the view of it.
type moveHelper struct {
	*unitHelper
	t    *testing.T
	home string // main's home folder
	own  string // this user's home folder
	data string // the helper's data folder
	pi   string
}

func startMoveHelper(t *testing.T, lease time.Duration, env ...string) *moveHelper {
	t.Helper()
	if output, err := exec.Command(buildGripi(t), "worker", "enter", "--home", filepath.Join(t.TempDir(), "alice"), "--", "true").CombinedOutput(); err != nil {
		t.Skipf("this system has no private views: %v %s", err, output)
	}
	own, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("GRIPI_TEST_VIEW_HOME")
	if home == "" {
		home = own
	}
	// In the home folder, as on workers, where the file system may share the contents of copies.
	data, err := os.MkdirTemp(own, "gripi-move-test-data-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(data) })
	unit := startUnitHelperWith(t, lease, env, "--home", home, "--data-dir", data)
	pi := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(pi, []byte(evalPi), 0700); err != nil {
		t.Fatal(err)
	}
	return &moveHelper{unitHelper: unit, t: t, home: home, own: own, data: data, pi: pi}
}

// privateRoot returns a new folder directly in main's home folder for the moves of a test, which the views of the moves
// show from their copies, and removes it from this user's home folder afterwards.
func (rig *moveHelper) privateRoot() string {
	name := make([]byte, 4)
	rand.Read(name)
	root := "gripi-move-test-" + hex.EncodeToString(name)
	rig.t.Cleanup(func() { os.RemoveAll(filepath.Join(rig.own, root)) })
	return filepath.Join(rig.home, root)
}

// shadow makes main's shadow of a repository in the private root, with a README, and returns the shadow and the
// repository's folder, at main's path.
func (rig *moveHelper) shadow(private, readme string) (shadow, repository string) {
	rig.t.Helper()
	shadow = rig.t.TempDir()
	repository = filepath.Join(private, "repo")
	folder := shadow + repository
	if err := os.MkdirAll(folder, 0700); err != nil {
		rig.t.Fatal(err)
	}
	git(rig.t, folder, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(folder, "README"), []byte(readme), 0600)
	os.WriteFile(filepath.Join(folder, ".gitignore"), []byte("node_modules/\n"), 0600)
	git(rig.t, folder, "add", "-A")
	git(rig.t, folder, "commit", "-qm", "first")
	return shadow, repository
}

// move prepares a move of the repository and uploads main's shadow of it to the move's copy.
func (rig *moveHelper) move(id, private, shadow, repository string) {
	rig.t.Helper()
	if response := rig.request(map[string]any{
		"type": "move.prepare", "move": id, "repository": filepath.Join(repository, ".git"), "roots": []string{repository}, "private": []string{private},
	}); !response.OK {
		rig.t.Fatalf("move.prepare = %+v", response)
	}
	channel := exec.Command(buildGripi(rig.t), "worker", "receive", "--runtime-dir", rig.runtimeDir, "--move", id, shadow+rig.home, rig.instance)
	channel.Stderr = os.Stderr
	input, _ := channel.StdinPipe()
	output, _ := channel.StdoutPipe()
	if err := channel.Start(); err != nil {
		rig.t.Fatal(err)
	}
	err := mirror.Sync(context.Background(), struct {
		io.Reader
		io.WriteCloser
	}{output, input}, mirror.Unit{Main: shadow + rig.home}, mirror.Options{})
	if err := errors.Join(err, channel.Wait()); err != nil {
		rig.t.Fatalf("upload the shadow: %v", err)
	}
}

// copy is where the copy of the move has main's path.
func (rig *moveHelper) copy(id, path string) string {
	return filepath.Join(rig.data, "copies", id, strings.TrimPrefix(path, rig.home))
}

// pi starts a session of the move whose Pi is evalPi, and returns a function that runs a command there.
func (rig *moveHelper) session(id, move, cwd string, env ...string) func(command string) string {
	rig.t.Helper()
	if response := rig.request(map[string]any{
		"type": "session.create", "session": id, "move": move, "cwd": cwd, "args": []string{rig.pi}, "env": env,
	}); !response.OK {
		rig.t.Fatalf("session.create = %+v", response)
	}
	_, input, output := rig.attach(id)
	return func(command string) string {
		rig.t.Helper()
		fmt.Fprintln(input, command)
		var lines []string
		for {
			line, err := output.ReadString('\n')
			if err != nil {
				rig.t.Fatalf("Pi ended after %q: %v", lines, err)
			}
			if line == "--\n" {
				return strings.Join(lines, "")
			}
			lines = append(lines, line)
		}
	}
}

// mirror starts one of the move's mirrors, the session's file's when file is set, and connects destination to it. It
// returns what the destination's Serve and the channel return.
func (rig *moveHelper) mirror(move, file string, destination *mirror.Destination) <-chan error {
	rig.t.Helper()
	if response := rig.request(map[string]any{"type": "mirror.start", "move": move, "file": file}); !response.OK {
		rig.t.Fatalf("mirror.start = %+v", response)
	}
	arguments := []string{"worker", "mirror", "--runtime-dir", rig.runtimeDir, "--move", move}
	if file != "" {
		arguments = append(arguments, "--file", file)
	}
	channel := exec.Command(buildGripi(rig.t), append(arguments, rig.instance)...)
	channel.Stderr = os.Stderr
	input, _ := channel.StdinPipe()
	output, _ := channel.StdoutPipe()
	if err := channel.Start(); err != nil {
		rig.t.Fatal(err)
	}
	rig.t.Cleanup(func() { channel.Process.Kill() })
	served := make(chan error, 1)
	go func() {
		err := destination.Serve(context.Background(), struct {
			io.Reader
			io.Writer
		}{output, input}, rig.t.TempDir())
		input.Close()
		served <- errors.Join(err, channel.Wait())
	}()
	return served
}

// destinations return main's side of a move's mirrors: of the repository into its shadow, and of the session's file
// under main, as main's own file would be elsewhere.
func (rig *moveHelper) destinations(shadow, main, file string) (repository, session *mirror.Destination) {
	rig.t.Helper()
	repository, err := mirror.NewMovedDestination(rig.t.TempDir(), "/", shadow)
	if err == nil {
		session, err = mirror.NewMovedDestination(rig.t.TempDir(), "/", main)
	}
	if err != nil {
		rig.t.Fatal(err)
	}
	session.File = file
	return repository, session
}

func read(path string) string {
	contents, _ := os.ReadFile(path)
	return string(contents)
}

func TestAMovesSessionWorksOnItsCopyAndNeedsBothOfItsMirrors(t *testing.T) {
	rig := startMoveHelper(t, helper.DefaultLease)
	private := rig.privateRoot()
	shadow, repository := rig.shadow(private, "first\n")
	rig.move("m1", private, shadow, repository)
	// Another session of the repository moves to the same worker, with a copy of its own.
	otherShadow, _ := rig.shadow(private, "second\n")
	rig.move("m2", private, otherShadow, repository)
	pi := rig.session("s1", "m1", repository)
	other := rig.session("s2", "m2", repository)

	if seen := pi(`echo "$HOME"; pwd -P; cat README`); seen != rig.home+"\n"+repository+"\nfirst\n" {
		t.Fatalf("the session sees %q", seen)
	}
	if seen := other("cat README"); seen != "second\n" {
		t.Fatalf("the other session sees %q", seen)
	}
	if answer := extensionRequest(t, rig.runtimeDir, "s1", "barrier"); answer.OK || !strings.Contains(answer.Error, "folder") {
		t.Fatalf("a barrier without mirrors = %+v", answer)
	}
	main, file := t.TempDir(), filepath.Join(t.TempDir(), "sessions", "s1.jsonl")
	repositoryDestination, sessionDestination := rig.destinations(shadow, main, file)
	rig.mirror("m1", "", repositoryDestination)
	if answer := extensionRequest(t, rig.runtimeDir, "s1", "barrier"); answer.OK || !strings.Contains(answer.Error, "session's file") {
		t.Fatalf("a barrier without the session file's mirror = %+v", answer)
	}
	if answer := extensionRequest(t, rig.runtimeDir, "s1", "health"); answer.OK {
		t.Fatal("healthy without the session file's mirror")
	}
	rig.mirror("m1", file, sessionDestination)

	// What the session changes, and a worktree that it makes in the private root, reach main with its next barrier.
	pi(`echo changed > README; git worktree add -q -b feature ../feature; echo new > ../feature/new; echo '{"type":"session"}' >> ` + file)
	if answer := extensionRequest(t, rig.runtimeDir, "s1", "barrier"); !answer.OK {
		t.Fatalf("barrier = %+v", answer)
	}
	if answer := extensionRequest(t, rig.runtimeDir, "s1", "health"); !answer.OK {
		t.Fatal("unhealthy with both mirrors")
	}
	feature := filepath.Join(private, "feature")
	for path, want := range map[string]string{
		shadow + filepath.Join(repository, "README"): "changed\n", shadow + filepath.Join(feature, "new"): "new\n",
		main + file: "{\"type\":\"session\"}\n", rig.copy("m1", filepath.Join(feature, "new")): "new\n",
		rig.copy("m2", filepath.Join(repository, "README")): "second\n",
	} {
		if got := read(path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(rig.own, filepath.Base(private))); len(entries) != 0 {
		t.Errorf("this user's own folder at the private root has %v", entries)
	}
}

func TestReleaseTakesTheFinalBarriersOnceTheMovesProcessesHaveStopped(t *testing.T) {
	rig := startMoveHelper(t, helper.DefaultLease)
	private := rig.privateRoot()
	shadow, repository := rig.shadow(private, "first\n")
	rig.move("m1", private, shadow, repository)
	pi := rig.session("s1", "m1", repository)
	main := t.TempDir()
	file := filepath.Join(t.TempDir(), "sessions", "s1.jsonl")
	repositoryDestination, sessionDestination := rig.destinations(shadow, main, file)
	repositoryServed := rig.mirror("m1", "", repositoryDestination)
	sessionServed := rig.mirror("m1", file, sessionDestination)

	// A process that the session started writes until it is killed.
	pids := strings.Fields(pi(`echo $$; (i=0; while :; do i=$((i+1)); echo $i > counter; echo "{\"n\":$i}" >> ` + file + `; done) & echo $!`))
	time.Sleep(200 * time.Millisecond)
	response := rig.request(map[string]any{"type": "move.release", "move": "m1"})
	if !response.OK || response.Barriers == nil || response.Barriers.Repository == 0 || response.Barriers.Session == 0 {
		t.Fatalf("move.release = %+v", response)
	}
	for _, pid := range pids {
		number, _ := strconv.Atoi(pid)
		assertGone(t, 0, number)
	}
	for _, served := range []<-chan error{repositoryServed, sessionServed} {
		select {
		case err := <-served:
			if err != nil {
				t.Fatalf("a destination ended with %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a mirror still runs after move.release")
		}
	}
	// The writer may die between emptying the file and writing it again.
	counter, err := os.ReadFile(rig.copy("m1", filepath.Join(repository, "counter")))
	if onMain := read(shadow + filepath.Join(repository, "counter")); err != nil || onMain != string(counter) {
		t.Fatalf("main has the counter at %q, the worker at %q, %v", onMain, counter, err)
	}
	if session := read(file); session == "" || read(main+file) != session {
		t.Fatalf("main's copy of the session has %d lines, the worker's %d", strings.Count(read(main+file), "\n"), strings.Count(session, "\n"))
	}
	if _, err := net.Dial("unix", filepath.Join(rig.runtimeDir, "m1.move.sock")); err == nil {
		t.Fatal("the move's mirrors still listen")
	}
}

// A release that couldn't take a mirror's last barrier says so until the mirror's channel works again, and no session
// starts in a released move.
func TestAReleaseThatMissedTheLastChangesKeepsSayingSo(t *testing.T) {
	rig := startMoveHelper(t, helper.DefaultLease)
	private := rig.privateRoot()
	shadow, repository := rig.shadow(private, "first\n")
	rig.move("m1", private, shadow, repository)
	rig.session("s1", "m1", repository)
	if response := rig.request(map[string]any{"type": "mirror.start", "move": "m1"}); !response.OK {
		t.Fatalf("mirror.start = %+v", response)
	}

	for range 2 {
		if response := rig.request(map[string]any{"type": "move.release", "move": "m1"}); response.OK || !strings.Contains(response.Error, "isn't connected") {
			t.Fatalf("move.release without main's copy = %+v", response)
		}
	}
	create := map[string]any{"type": "session.create", "session": "s2", "move": "m1", "cwd": repository, "args": []string{rig.pi}}
	if response := rig.request(create); response.OK || !strings.Contains(response.Error, "released") {
		t.Fatalf("session.create in a released move = %+v", response)
	}
	if response := rig.request(map[string]any{"type": "move.release", "move": "m1"}); response.OK || !strings.Contains(response.Error, "isn't connected") {
		t.Fatalf("move.release after the refused session = %+v", response)
	}
	repositoryDestination, _ := rig.destinations(shadow, t.TempDir(), filepath.Join(t.TempDir(), "s1.jsonl"))
	served := rig.mirror("m1", "", repositoryDestination)
	for repositoryDestination.Checkpoint().Barrier == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	var barriers []helper.Barriers
	for range 2 {
		response := rig.request(map[string]any{"type": "move.release", "move": "m1"})
		if !response.OK || response.Barriers.Repository == 0 {
			t.Fatalf("move.release once main's copy is connected = %+v", response)
		}
		barriers = append(barriers, *response.Barriers)
	}
	if barriers[0] != barriers[1] {
		t.Fatalf("move.release answered %+v, then %+v", barriers[0], barriers[1])
	}
	if err := <-served; err != nil {
		t.Fatalf("main's copy ended with %v", err)
	}
}

// A release takes no final barriers while a process of the move outlives its kill, also when main tries again or
// deletes the move meanwhile, and takes them once the process has died.
func TestAReleaseWaitsForEveryProcessOfTheMoveToDie(t *testing.T) {
	rig := startMoveHelper(t, helper.DefaultLease)
	private := rig.privateRoot()
	shadow, repository := rig.shadow(private, "first\n")
	rig.move("m1", private, shadow, repository)
	pi := rig.session("s1", "m1", repository)
	file := filepath.Join(t.TempDir(), "sessions", "s1.jsonl")
	repositoryDestination, sessionDestination := rig.destinations(shadow, t.TempDir(), file)
	served := []<-chan error{rig.mirror("m1", "", repositoryDestination), rig.mirror("m1", file, sessionDestination)}
	pid, _ := strconv.Atoi(strings.TrimSpace(pi("echo last > last; echo $$")))
	_, die := startSlowToDie(t, cgroupOf(t, pid))

	for range 2 {
		if response := rig.request(map[string]any{"type": "move.release", "move": "m1"}); response.OK || !strings.Contains(response.Error, "still has processes") {
			t.Fatalf("move.release while a process survives = %+v", response)
		}
	}
	if response := rig.request(map[string]any{"type": "move.delete", "move": "m1"}); response.OK {
		t.Fatalf("move.delete while a process survives = %+v", response)
	}
	die()
	if response := rig.request(map[string]any{"type": "move.release", "move": "m1"}); !response.OK || response.Barriers.Repository == 0 || response.Barriers.Session == 0 {
		t.Fatalf("move.release once the process died = %+v", response)
	}
	for _, done := range served {
		if err := <-done; err != nil {
			t.Fatalf("a mirror ended with %v", err)
		}
	}
	if last := read(shadow + filepath.Join(repository, "last")); last != "last\n" {
		t.Fatalf("main's copy of the last change = %q", last)
	}
}

// A move's copy stays while a process of the move's host outlives its kill.
func TestDeletingAMoveWaitsForItsHostToDie(t *testing.T) {
	rig := startMoveHelper(t, helper.DefaultLease)
	private := rig.privateRoot()
	shadow, repository := rig.shadow(private, "first\n")
	rig.move("m1", private, shadow, repository)
	pid, _ := strconv.Atoi(strings.TrimSpace(rig.session("s1", "m1", repository)("echo $$")))
	_, die := startSlowToDie(t, filepath.Join(filepath.Dir(cgroupOf(t, pid)), "move-m1"))

	if response := rig.request(map[string]any{"type": "move.release", "move": "m1"}); response.OK || !strings.Contains(response.Error, "still has processes") {
		t.Fatalf("move.release while the host survives = %+v", response)
	}
	if response := rig.request(map[string]any{"type": "move.delete", "move": "m1"}); response.OK || !strings.Contains(response.Error, "still has processes") {
		t.Fatalf("move.delete while the host survives = %+v", response)
	}
	if _, err := os.Stat(rig.copy("m1", repository)); err != nil {
		t.Fatalf("the copy after the failed move.delete: %v", err)
	}
	die()
	if response := rig.request(map[string]any{"type": "move.delete", "move": "m1"}); !response.OK {
		t.Fatalf("move.delete once the host died = %+v", response)
	}
}

func TestLeaseExpiryKillsEveryProcessOfAMove(t *testing.T) {
	const lease = 2 * time.Second
	rig := startMoveHelper(t, lease)
	rig.answering.Store(true)
	private := rig.privateRoot()
	shadow, repository := rig.shadow(private, "first\n")
	rig.move("m1", private, shadow, repository)
	pi := rig.session("s1", "m1", repository)
	var pids []int
	for _, pid := range strings.Fields(pi("echo $$; sleep 1000 & echo $!")) {
		number, _ := strconv.Atoi(pid)
		pids = append(pids, number)
	}
	if response := rig.request(map[string]any{"type": "mirror.start", "move": "m1"}); !response.OK {
		t.Fatalf("mirror.start = %+v", response)
	}
	hosts, err := os.ReadFile(filepath.Join(filepath.Dir(cgroupOf(t, pids[0])), "move-m1", "cgroup.procs"))
	for _, pid := range strings.Fields(string(hosts)) {
		number, _ := strconv.Atoi(pid)
		pids = append(pids, number)
	}
	if err != nil || len(pids) < 3 {
		t.Fatalf("the move's processes: %v, %v", pids, err)
	}

	rig.answering.Store(false)
	select {
	case <-rig.served:
	case <-time.After(lease + 2*time.Second):
		t.Fatal("the helper still runs")
	}
	assertGone(t, time.Second, pids...)
}

func TestAMovesSessionsReachDockerAtTheCopysPaths(t *testing.T) {
	upstream, created := fakeDocker(t)
	rig := startMoveHelper(t, helper.DefaultLease, "DOCKER_HOST=unix://"+upstream)
	private := rig.privateRoot()
	shadow, repository := rig.shadow(private, "first\n")
	rig.move("m1", private, shadow, repository)
	pi := rig.session("s1", "m1", repository, "DOCKER_HOST=tcp://main:2375")

	socket, ok := strings.CutPrefix(strings.TrimSpace(pi(`echo "$DOCKER_HOST"`)), "unix://")
	if !ok {
		t.Fatalf("Pi got DOCKER_HOST=%s", socket)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}}
	response, err := client.Post("http://docker/v1.52/containers/create", "application/json",
		strings.NewReader(`{"HostConfig":{"Binds":["`+repository+`:/x","`+rig.home+`/.cache:/y"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("/containers/create = %s", response.Status)
	}
	if got, want := strings.TrimSpace(<-created), `{"HostConfig":{"Binds":["`+rig.copy("m1", repository)+`:/x","`+rig.own+`/.cache:/y"]}}`; got != want {
		t.Errorf("Docker got %s, want %s", got, want)
	}

	// Once Docker can't be reached, the session learns where it runs, as whom, and what an admin of the worker can do.
	os.Remove(upstream)
	if response, err = client.Get("http://docker/v1.52/containers/json"); err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	host, _ := os.Hostname()
	if want := fmt.Sprintf("sessions on worker %s run as %s, who can't use Docker there", host, rig.hello.User); response.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), want) {
		t.Fatalf("without Docker, /containers/json = %s %s; want %q", response.Status, body, want)
	}
}

// A new copy of a repository takes the folders that Git ignores, such as node_modules, from the latest earlier copy
// there, or from what deleted copies left, and never what Git doesn't ignore.
func TestAMovesCopyReusesTheIgnoredFoldersOfEarlierCopies(t *testing.T) {
	rig := startMoveHelper(t, helper.DefaultLease)
	private := rig.privateRoot()
	shadow, repository := rig.shadow(private, "first\n")
	rig.move("m1", private, shadow, repository)
	pi := rig.session("s1", "m1", repository)
	pi("mkdir -p node_modules/pkg build && head -c 1048576 /dev/urandom > node_modules/pkg/index.js && echo built > build/out")
	seed := func(id string) {
		t.Helper()
		rig.move(id, private, shadow, repository)
		if response := rig.request(map[string]any{"type": "move.seed", "move": id}); !response.OK {
			t.Fatalf("move.seed = %+v", response)
		}
	}
	modules := filepath.Join(repository, "node_modules", "pkg", "index.js")
	installed := read(rig.copy("m1", modules))

	seed("m2")
	if read(rig.copy("m2", modules)) != installed {
		t.Fatal("the second copy lacks the first copy's node_modules")
	}
	if _, err := os.Stat(rig.copy("m2", filepath.Join(repository, "build"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the second copy's build folder: %v", err)
	}
	if output, err := exec.Command("filefrag", "-v", rig.copy("m2", modules)).CombinedOutput(); err == nil && strings.Contains(string(output), "shared") {
		t.Log("the copy shares its extents with the earlier copy")
	} else if filesystem, _ := exec.Command("stat", "-f", "-c", "%T", rig.data).Output(); strings.TrimSpace(string(filesystem)) == "btrfs" {
		t.Errorf("on btrfs, the seeded file shares no extents: %v %s", err, output)
	}

	finish := func(id string) {
		t.Helper()
		if response := rig.request(map[string]any{"type": "move.release", "move": id}); !response.OK {
			t.Fatalf("move.release = %+v", response)
		}
		if response := rig.request(map[string]any{"type": "move.delete", "move": id}); !response.OK {
			t.Fatalf("move.delete = %+v", response)
		}
		if _, err := os.Stat(filepath.Join(rig.data, "copies", id)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the deleted copy: %v", err)
		}
	}
	// The session moves again before its earlier copy is deleted, which leaves the session as it is.
	if response := rig.request(map[string]any{"type": "move.release", "move": "m1"}); !response.OK {
		t.Fatalf("move.release = %+v", response)
	}
	again := rig.session("s1", "m2", repository)
	finish("m1")
	if alive := again("echo alive"); alive != "alive\n" {
		t.Fatalf("the session in the later move says %q", alive)
	}
	finish("m2")
	seed("m3")
	if read(rig.copy("m3", modules)) != installed {
		t.Fatal("the third copy lacks what the deleted copies left")
	}
}
