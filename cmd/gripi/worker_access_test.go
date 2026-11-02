package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/config"
	remote "github.com/melounvitek/gripi/internal/worker"
)

// fakePlacement stands in for the gateway's record of which sessions are on a worker.
type fakePlacement struct{ sessions []string }

func (placement fakePlacement) Session(key string) bool {
	return slices.Contains(placement.sessions, key)
}

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
		if code, stdout, stderr := runCLI(command...); code != 1 || stdout != "" || !strings.Contains(stderr, "no session matches") {
			t.Fatalf("gripi %q on a session on main = %d, stdout %q, stderr %q", command, code, stdout, stderr)
		}
	}
	t.Setenv("PI_SESSION_FILE", alpha)
	if code, _, stderr := runCLI("sudo", "--", "true"); code != 1 || !strings.Contains(stderr, "403") {
		t.Fatalf("gripi sudo = %d, stderr %q", code, stderr)
	}

	stop()
	if code, _, stderr := runCLI("list"); code != 1 || !strings.Contains(stderr, "cannot reach the gateway") {
		t.Fatalf("gripi list after stop = %d, stderr %q", code, stderr)
	}
}
