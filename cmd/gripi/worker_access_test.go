package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
	remote "github.com/melounvitek/gripi/internal/worker"
)

// fakePlacement stands in for the gateway's record of which sessions are on a worker.
type fakePlacement struct{ sessions []string }

func (placement fakePlacement) Session(key string) bool {
	return slices.Contains(placement.sessions, key)
}

func (fakePlacement) Name() string { return "beelink" }

// Settings can put each of the gateway's files and folders anywhere in the home folder, and no session may move to a
// worker on a copy that would hide one there.
func TestNoSessionMovesOnACopyThatHidesTheGatewaysOwnFiles(t *testing.T) {
	isolateSettings(t)
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "agent", "agent"))
	for _, name := range []string{
		"GRIPI_ENV_PATH", "GRIPI_SESSIONS_ROOT", "GRIPI_ATTACHMENTS_ROOT", "GRIPI_WEB_PUSH_VAPID_PATH", "GRIPI_PUSH_SUBSCRIPTIONS_PATH",
		"GRIPI_SESSION_CWDS_PATH", "GRIPI_READ_STATE_PATH", "GRIPI_PINNED_SESSIONS_PATH", "GRIPI_SESSION_TAGS_PATH", "GRIPI_ENVIRONMENT_PATH",
		"GRIPI_BROWSER_ACCESS_PATH", "GRIPI_WORKSPACE_SECRET_PATH", "GRIPI_WORKSPACE_ACCESS_PATH", "GRIPI_WORKSPACE_OWNERSHIP_PATH",
		"GRIPI_WORKERS_PATH", "GRIPI_WORKER_PLACEMENTS_PATH", "GRIPI_RESTART_PATH", "GRIPI_SOCKET_PATH",
	} {
		t.Setenv(name, filepath.Join(home, name, strings.ToLower(name)))
	}
	cfg, err := config.Load(os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	handler, workers, err := newGateway(cfg, remote.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.(interface{ Close(context.Context) error }).Close(context.Background())
	defer workers.Close()
	if err := saveWorkers(cfg.WorkersPath, []worker{{Name: "beelink", Destination: "gripi@beelink"}}); err != nil {
		t.Fatal(err)
	}
	prepare := func(context.Context, func(string) bool) (func(), error) {
		return nil, errors.New("the move stopped the sessions")
	}

	// Every file or folder that the settings name, but Hermes's program, lives in a folder of its own, which holds a
	// repository too.
	settings := reflect.ValueOf(cfg)
	for i := range settings.NumField() {
		name := settings.Type().Field(i).Name
		if settings.Field(i).Kind() != reflect.String || name == "HermesPath" || !strings.HasSuffix(name, "Path") && !strings.HasSuffix(name, "Root") && !strings.HasSuffix(name, "Dir") {
			continue
		}
		repository := filepath.Join(filepath.Dir(settings.Field(i).String()), "repository")
		if err := os.MkdirAll(repository, 0700); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("git", "init", "-q", repository).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, output)
		}
		key := filepath.Join(home, "sessions", "session.jsonl")
		if err := workers.Move(context.Background(), key, repository, "beelink", prepare); !refused(err) || !strings.Contains(err.Error(), "would hide") {
			t.Errorf("moving a session in the folder of %s = %v", name, err)
		}
	}
}

// Repositories that the previous version of Gripi left on workers only make the workers unavailable.
func TestTheGatewayRunsWithoutWorkersWhileThePreviousVersionHasRepositoriesOnThem(t *testing.T) {
	cfg, _, _ := fakePiConfig(t)
	cfg.WorkerPlacementsPath = filepath.Join(t.TempDir(), "worker-placements.json")
	previous := `[{"worker": "beelink", "unit": {"main": "/home/me/Work/app"}}]`
	if err := os.WriteFile(cfg.WorkerPlacementsPath, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	handler, workers, err := newGateway(cfg, remote.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.(interface{ Close(context.Context) error }).Close(context.Background())
	if workers != nil {
		t.Fatal("the gateway runs sessions on workers")
	}
	if !strings.Contains(logged.String(), "Workers are unavailable") || !strings.Contains(logged.String(), "/home/me/Work/app on beelink") {
		t.Fatalf("log %q", logged.String())
	}
	socket := socketPath(t)
	server, err := startLocalServer(handler.(interface{ Local() http.Handler }).Local(), socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	t.Setenv("GRIPI_SOCKET_PATH", socket)

	if code, stdout, stderr := runCLI("send", "0a1-alpha", "Hello", "--json"); code != 0 {
		t.Fatalf("gripi send = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runCLI("wait", "0a1-alpha", "--timeout", "10"); code != 0 || !strings.Contains(stdout, "idle") {
		t.Fatalf("gripi wait = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, _, stderr := runCLI("worker", "move", "0a1-alpha", "beelink"); code != 1 || !strings.Contains(stderr, "runs no sessions on workers") {
		t.Fatalf("gripi worker move = %d, stderr %q", code, stderr)
	}
	if contents, _ := os.ReadFile(cfg.WorkerPlacementsPath); string(contents) != previous {
		t.Fatalf("the placements changed to %s", contents)
	}
}

func refused(err error) bool {
	var refusal interface{ Refused() bool }
	return errors.As(err, &refusal) && refusal.Refused()
}

func TestGripiOnAWorkerReachesOnlyTheSessionsOnTheWorker(t *testing.T) {
	// Socket paths have a short system limit.
	state := filepath.Dir(socketPath(t))
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	alpha, beta := fakePiGateway(t, func(configured *config.Config) {
		configured.ReadStatePath = filepath.Join(state, "read.json")
		cfg = *configured
	})
	if code, _, stderr := runCLI("tag", "0a1-beta", "main-only"); code != 0 {
		t.Fatalf("gripi tag on main = %d, stderr %q", code, stderr)
	}
	socket, stop, err := startWorkerSocket(cfg, browser, "beelink", fakePlacement{sessions: []string{alpha}})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, _, err := startWorkerSocket(cfg, browser, "../beelink", fakePlacement{}); err == nil {
		t.Fatal("a worker's name put its socket outside the folder of workers' sockets")
	}

	// On the worker, the forward puts the socket where gripi looks by default, in a home without Gripi's other files.
	home := filepath.Join(state, "worker-home")
	if err := os.MkdirAll(filepath.Join(home, ".pi", "gripi"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(socket, filepath.Join(home, ".pi", "gripi", "gripi.sock")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GRIPI_SOCKET_PATH", "")

	if code, stdout, stderr := runCLI("list"); code != 0 || !strings.Contains(stdout, "0a1-alpha") || strings.Contains(stdout, "0a1-beta") {
		t.Fatalf("gripi list = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runCLI("show", "0a1-alpha"); code != 0 || stdout != "Answer from 0a1-alpha\n" {
		t.Fatalf("gripi show = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runCLI("tag", "0a1-alpha", "on-worker"); code != 0 || !strings.Contains(stdout, "on-worker") {
		t.Fatalf("gripi tag = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runCLI("tags"); code != 0 || !strings.Contains(stdout, "on-worker") || strings.Contains(stdout, "main-only") {
		t.Fatalf("gripi tags = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr := runCLI("send", "0a1-alpha", "Start the follow-up scenario", "--json")
	if session := decodeSession(t, stdout); code != 0 || session.Path != alpha {
		t.Fatalf("gripi send = %d, stderr %q, session %+v", code, stderr, session)
	}
	for _, command := range [][]string{{"show", "0a1-beta"}, {"send", beta, "Hello"}, {"tag", beta, "from-worker"}} {
		if code, stdout, stderr := runCLI(command...); code != 1 || stdout != "" || !strings.Contains(stderr, "matches no session on worker beelink, where gripi reaches only the sessions on beelink; ask the user to run the command on main") {
			t.Fatalf("gripi %q on a session on main = %d, stdout %q, stderr %q", command, code, stdout, stderr)
		}
	}
	t.Setenv("PI_SESSION_FILE", beta)
	if code, _, stderr := runCLI("sudo", "--", "true"); code != 1 || !strings.Contains(stderr, "the command runs in no session of yours on worker beelink, so nobody could answer; ask the user to run it on main") {
		t.Fatalf("gripi sudo in a session on main = %d, stderr %q", code, stderr)
	}

	stop()
	if code, _, stderr := runCLI("list"); code != 1 || !strings.Contains(stderr, "cannot reach the gateway") {
		t.Fatalf("gripi list after stop = %d, stderr %q", code, stderr)
	}
}

// gripi sudo in a session on a worker asks as on main, for the password of the worker's user, and runs the command as
// root there.
func TestSudoInASessionOnAWorkerRunsTheCommandThere(t *testing.T) {
	state := filepath.Dir(socketPath(t))
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	alpha, _ := fakePiGateway(t, func(configured *config.Config) {
		configured.ReadStatePath = filepath.Join(state, "read.json")
		cfg = *configured
	})
	var ran []string
	workers := &recordedWorkers{
		location: gateway.WorkerLocation{Worker: "beelink", State: "connected", User: "gripi"},
		sudo: func(key, cwd string, argv []string, password []byte, stdout, stderr io.Writer) (int, error) {
			ran = append(ran, key, cwd, strings.Join(argv, " "), string(password))
			io.WriteString(stdout, "out\n")
			io.WriteString(stderr, "err\n")
			return 7, nil
		},
	}
	browser.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)
	socket, stop, err := startWorkerSocket(cfg, browser, "beelink", fakePlacement{sessions: []string{alpha}})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	t.Setenv("GRIPI_SOCKET_PATH", socket)
	outsideSessions(t)
	t.Setenv("PI_SESSION_FILE", alpha)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	result := startCLI("sudo", "--", "apt-get", "install", "-y", "ripgrep")
	request := sudoRequestsUntil(t, alpha, "", 1)[0]
	if request["worker"] != "beelink" || request["user"] != "gripi" || request["display"] != "apt-get install -y ripgrep" || request["cwd"] != cwd {
		t.Fatalf("the request = %v", request)
	}
	if code := answerSudo(request["id"].(string), "the worker's password", ""); code != http.StatusOK {
		t.Fatalf("answer = %d", code)
	}
	if answered := <-result; answered.code != 7 || answered.stdout != "out\n" || !strings.HasSuffix(answered.stderr, "err\n") {
		t.Fatalf("gripi sudo = %+v", answered)
	}
	if want := []string{alpha, cwd, "apt-get install -y ripgrep", "the worker's password"}; !slices.Equal(ran, want) {
		t.Fatalf("the worker ran %q, want %q", ran, want)
	}

	workers.sudo = func(string, string, []string, []byte, io.Writer, io.Writer) (int, error) {
		return 0, refusal{errors.New("the session is coming back from its worker")}
	}
	result = startCLI("sudo", "--", "true")
	request = sudoRequestsUntil(t, alpha, "", 1)[0]
	answerSudo(request["id"].(string), "the worker's password", "")
	if answered := <-result; answered.code != 1 || !strings.Contains(answered.stderr, "409: the session is coming back from its worker") {
		t.Fatalf("gripi sudo that the worker refused = %+v", answered)
	}
}

type refusal struct{ error }

func (refusal) Refused() bool { return true }
