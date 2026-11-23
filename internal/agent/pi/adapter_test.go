package pi

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

// Pi keeps a scratch session only in memory, so it names no session file for it.
func TestOnlyScratchSessionsHaveNoSessionFile(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required")
	}
	_, file, _, _ := runtime.Caller(0)
	t.Setenv("GRIPI_E2E_SESSIONS_ROOT", t.TempDir())
	adapter := New(Options{Command: []string{node, filepath.Join(filepath.Dir(file), "..", "..", "..", "e2e", "support", "fake_pi.mjs")}, StateDir: t.TempDir()})
	t.Cleanup(func() { _ = adapter.Close() })

	for _, scratch := range []bool{false, true} {
		client, err := adapter.StartInCWD(t.TempDir(), "", agent.StartOptions{Scratch: scratch})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		state, err := client.State(ctx)
		cancel()
		if err != nil || (state.Key == "") != scratch {
			t.Fatalf("scratch %v: state %+v, %v", scratch, state, err)
		}
	}
}
