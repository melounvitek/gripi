package server_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

func TestRestartedGatewayListsUnchangedSessionsWithoutReadingThem(t *testing.T) {
	root := t.TempDir()
	sessionsRoot := filepath.Join(root, "sessions")
	if err := os.Mkdir(sessionsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Address: "127.0.0.1:4567", Environment: "test", Home: root,
		SessionsRoot: sessionsRoot, AttachmentsRoot: filepath.Join(root, "attachments"),
		ReadStatePath: filepath.Join(root, "state", "read.json"), PinnedSessionsPath: filepath.Join(root, "state", "pinned.json"), SessionTagsPath: filepath.Join(root, "state", "tags.json"),
		BrowserAccessPath: filepath.Join(root, "state", "browser.json"), BrowserAuthDisabled: true,
		PiCommand: []string{"false"},
	}
	type closer interface{ Close(context.Context) error }
	start := func() http.Handler {
		t.Helper()
		handler, err := gateway.NewHandler(cfg, gripi.WebFiles)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = handler.(closer).Close(context.Background()) })
		return handler
	}
	lists := func(handler http.Handler, path string) bool {
		response := serveAction(handler, getActionRequest("/sidebar?no_session=1"))
		return response.Code == http.StatusOK && strings.Contains(response.Body.String(), `data-session-path="`+path+`"`)
	}

	beforeStart := filepath.Join(sessionsRoot, "before-start.jsonl")
	writeActionSession(t, beforeStart, root)
	start()
	makeUnreadableKeepingFileSignature(t, beforeStart)
	restarted := start()
	if !lists(restarted, beforeStart) {
		t.Fatal("a session listed at startup was read again after a restart")
	}

	afterStart := filepath.Join(sessionsRoot, "after-start.jsonl")
	writeActionSession(t, afterStart, root)
	if !lists(restarted, afterStart) {
		t.Fatal("a session created after startup was not listed")
	}
	if err := restarted.(closer).Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	makeUnreadableKeepingFileSignature(t, afterStart)
	if final := start(); !lists(final, beforeStart) || !lists(final, afterStart) {
		t.Fatal("a session listed before shutdown was read again after a restart")
	}
}

// makeUnreadableKeepingFileSignature replaces a session with content that no
// longer lists, so a session that still lists was not read again.
func makeUnreadableKeepingFileSignature(t *testing.T, path string) {
	t.Helper()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(strings.Repeat("x", int(stat.Size()))), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, time.Time{}, stat.ModTime()); err != nil {
		t.Fatal(err)
	}
}
