package server_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

func TestRestartedGatewayKeepsFollowingSessionUsedInPiCLI(t *testing.T) {
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
	start := func() http.Handler {
		t.Helper()
		handler, err := gateway.NewHandler(cfg, gripi.WebFiles)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
		return handler
	}
	external := func(handler http.Handler) bool {
		response := serveAction(handler, getActionRequest("/sidebar?no_session=1"))
		return response.Code == http.StatusOK && strings.Contains(response.Body.String(), `class="session-row is-external`)
	}

	path := filepath.Join(sessionsRoot, "session.jsonl")
	writeActionSession(t, path, root)
	running := start()
	if external(running) {
		t.Fatal("a session nobody else wrote to is followed as external")
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"type":"message","id":"pi-cli","parentId":"assistant-1","timestamp":"2026-01-01T00:00:03Z","message":{"role":"user","content":[{"type":"text","text":"Sent from Pi CLI"}]}}` + "\n")
	if closeErr := file.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	if !external(running) {
		t.Fatal("a session Pi CLI wrote to is not followed as external")
	}

	// Pi CLI writes nothing more, so only what the gateway saved can tell the session is external.
	if !external(start()) {
		t.Fatal("a restart forgot that Pi CLI is using the session")
	}
}
