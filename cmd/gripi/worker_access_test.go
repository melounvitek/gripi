package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/config"
)

// fakePlacement stands in for the gateway's record of what is on a worker.
type fakePlacement struct{ sessions []string }

func (placement fakePlacement) Session(key string) bool {
	return slices.Contains(placement.sessions, key)
}

func (fakePlacement) Folder(string) bool { return false }

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
