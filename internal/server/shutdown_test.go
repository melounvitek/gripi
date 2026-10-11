package server_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestClosedGatewayRemovesPiExtensionAndStillSavesBaselines(t *testing.T) {
	cfg, alpha, beta, _ := fakePiConfig(t)
	arguments := filepath.Join(t.TempDir(), "pi-arguments")
	// The shell records the arguments the gateway gives Pi, then runs the fake Pi with them.
	cfg.PiCommand = append([]string{"sh", "-c", `printf '%s\n' "$@" > "$0"; exec "$@"`, arguments}, cfg.PiCommand...)
	running := startGateway(t, cfg)
	followedAsExternal(running)
	if response := postPrompt(running, browserHost, url.Values{"session": {alpha}, "message": {"Show the deterministic browser response"}}); response.Code != http.StatusOK {
		t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
	}
	recorded, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(string(recorded), "\n")
	index := slices.Index(fields, "--extension")
	if index < 0 {
		t.Fatalf("Pi arguments = %q", recorded)
	}
	extension := fields[index+1]
	if _, err := os.Stat(extension); err != nil {
		t.Fatalf("Pi extension while Pi runs: %v", err)
	}

	stopGateway(t, running)
	if _, err := os.Stat(filepath.Dir(extension)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Pi extension directory after close: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(cfg.ReadStatePath), "session-baselines.json"))
	var baselines map[string]json.RawMessage
	if err != nil || json.Unmarshal(contents, &baselines) != nil || baselines[beta] == nil {
		t.Fatalf("baselines after close = %s, %v", contents, err)
	}
}
