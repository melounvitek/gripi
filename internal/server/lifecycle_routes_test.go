package server_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

func TestLifecycleDecisionsBeforePromptEventsArrive(t *testing.T) {
	for _, action := range []string{"delete", "tree", "extension delete"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			sessionsRoot := filepath.Join(root, "sessions")
			if err := os.Mkdir(sessionsRoot, 0700); err != nil {
				t.Fatal(err)
			}
			sessionPath := filepath.Join(sessionsRoot, "target.jsonl")
			writeActionSession(t, sessionPath, root)
			_, file, _, _ := runtime.Caller(0)
			fakePi := filepath.Join(filepath.Dir(file), "..", "..", "e2e", "support", "fake_pi.mjs")
			fakeLog := filepath.Join(root, "fake.log")
			t.Setenv("GRIPI_E2E_SESSIONS_ROOT", sessionsRoot)
			t.Setenv("GRIPI_E2E_FAKE_PI_LOG", fakeLog)
			t.Setenv("GRIPI_E2E_HOLD_PROMPT_EVENTS", "1")
			handler, err := gateway.NewHandler(config.Config{
				Address: "127.0.0.1:4567", Environment: "test", Home: root,
				SessionsRoot: sessionsRoot, AttachmentsRoot: filepath.Join(root, "attachments"),
				ReadStatePath: filepath.Join(root, "read.json"), PinnedSessionsPath: filepath.Join(root, "pinned.json"), SessionTagsPath: filepath.Join(root, "tags.json"),
				BrowserAccessPath: filepath.Join(root, "browser.json"), BrowserAuthDisabled: true,
				PiCommand: []string{"node", fakePi}, RPCIdleTimeout: time.Hour,
			}, gripi.WebFiles)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })

			message := "Start the steer scenario"
			if action == "extension delete" {
				message = "/immediate-command"
			}
			prompt := serveAction(handler, formActionRequest("/prompt", map[string]string{"session": sessionPath, "message": message}, true))
			if prompt.Code != http.StatusOK {
				t.Fatalf("prompt = %d %s", prompt.Code, prompt.Body.String())
			}
			events := serveAction(handler, getActionRequest("/events?session="+url.QueryEscape(sessionPath)))
			if events.Code != http.StatusOK || strings.Contains(events.Body.String(), `"type":"agent_start"`) || !strings.Contains(events.Body.String(), `"gateway_busy":false`) {
				t.Fatalf("expected accepted prompt before lifecycle delivery: %d %s", events.Code, events.Body.String())
			}
			before, err := os.ReadFile(sessionPath)
			if err != nil {
				t.Fatal(err)
			}
			if action != "tree" {
				deleted := serveAction(handler, formActionRequest("/sessions/delete", map[string]string{"session": sessionPath}, true))
				if action == "extension delete" {
					if deleted.Code != http.StatusOK {
						t.Fatalf("idle extension delete = %d %s", deleted.Code, deleted.Body.String())
					}
					return
				}
				if deleted.Code != http.StatusConflict || !strings.Contains(deleted.Body.String(), "Cannot delete a running session") {
					t.Errorf("busy delete = %d %s", deleted.Code, deleted.Body.String())
				}
				if after, err := os.ReadFile(sessionPath); err != nil || string(after) != string(before) {
					t.Fatalf("busy delete changed session file: %v", err)
				}
				return
			}
			tree := serveAction(handler, formActionRequest("/sessions/tree", map[string]string{"session": sessionPath, "entry_id": "user-1"}, true))
			if tree.Code != http.StatusOK {
				t.Fatalf("tree = %d %s", tree.Code, tree.Body.String())
			}
			log, err := os.ReadFile(fakeLog)
			if err != nil {
				t.Fatal(err)
			}
			abortAt, navigateAt := strings.Index(string(log), `"type":"abort"`), strings.Index(string(log), "/gripi_tree_navigate")
			if abortAt < 0 || navigateAt < abortAt {
				t.Fatalf("tree did not abort before navigating: %s", log)
			}
			next := serveAction(handler, formActionRequest("/prompt", map[string]string{"session": sessionPath, "message": message}, true))
			if next.Code != http.StatusOK {
				t.Fatalf("prompt after tree = %d %s", next.Code, next.Body.String())
			}
		})
	}
}
