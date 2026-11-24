package pi

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

func fakePiAdapter(t *testing.T) *Adapter {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required")
	}
	_, file, _, _ := runtime.Caller(0)
	t.Setenv("GRIPI_E2E_SESSIONS_ROOT", t.TempDir())
	adapter := New(Options{Command: []string{node, filepath.Join(filepath.Dir(file), "..", "..", "..", "e2e", "support", "fake_pi.mjs")}, StateDir: t.TempDir()})
	t.Cleanup(func() { _ = adapter.Close() })
	return adapter
}

// Pi keeps a scratch session only in memory, so it names no session file for it.
func TestOnlyScratchSessionsHaveNoSessionFile(t *testing.T) {
	adapter := fakePiAdapter(t)

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

// The history of a scratch session comes from Pi through Gripi's extension, without image data, and each image on its
// own.
func TestScratchHistoryComesFromTheExtension(t *testing.T) {
	adapter := fakePiAdapter(t)
	started, err := adapter.StartInCWD(t.TempDir(), "", agent.StartOptions{Scratch: true})
	if err != nil {
		t.Fatal(err)
	}
	client := started.(*Client)
	t.Cleanup(func() { _ = client.Close() })
	png := []byte("\x89PNG\r\n\x1a\nfake")
	image := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(image, png, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.Prompt(ctx, agent.Prompt{Text: "Look", Images: func() ([]agent.PromptImage, error) {
		return []agent.PromptImage{{Path: image, MIMEType: "image/png", Size: int64(len(png))}}, nil
	}}); err != nil {
		t.Fatal(err)
	}

	var prompt map[string]any
	for prompt == nil {
		response, err := client.History(ctx)
		if err != nil || response["success"] != true {
			t.Fatalf("history = %v, %v", response, err)
		}
		for _, item := range response["data"].(map[string]any)["entries"].([]any) {
			if message, _ := item.(map[string]any)["message"].(map[string]any); message["role"] == "user" {
				prompt = item.(map[string]any)
			}
		}
		if prompt == nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
	content := prompt["message"].(map[string]any)["content"].([]any)
	if part := content[len(content)-1].(map[string]any); part["type"] != "image" || part["data"] != "-" {
		t.Fatalf("prompt content = %v", content)
	}
	response, err := client.HistoryImage(ctx, prompt["id"].(string), 0)
	data, _ := response["data"].(map[string]any)
	if err != nil || data["data"] != base64.StdEncoding.EncodeToString(png) || data["mimeType"] != "image/png" {
		t.Fatalf("image = %v, %v", response, err)
	}
}
