package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestSessionTagsResolveSymlinksAndPendingAliasesWithoutRPC(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	configured := filepath.Join(root, "configured")
	if err := os.Mkdir(physical, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, configured); err != nil {
		t.Fatal(err)
	}
	physicalPath := filepath.Join(physical, "persisted.jsonl")
	configuredPath := filepath.Join(configured, "persisted.jsonl")
	writeSessionRecords(t, physicalPath, []map[string]any{{"type": "session", "version": 3, "id": "persisted", "cwd": root}})
	pending := rpc.NewPendingSessionRegistry(nil)
	pendingPath := filepath.Join(configured, "pending.jsonl")
	alias := filepath.Join(configured, "old-pending.jsonl")
	pending.Remember(pendingPath, root)
	pending.Remap(alias, configuredPath)
	registry := rpc.NewRegistry(func(string) (rpc.RPCClient, error) { t.Error("tag request started RPC"); return nil, os.ErrNotExist }, nil)
	app := &application{config: config.Config{SessionsRoot: configured, Home: root}, sessionCache: sessions.NewCache(), gatewayState: sessions.NewGatewayState(filepath.Join(root, "read"), filepath.Join(root, "pins"), filepath.Join(root, "tags"), configured), pendingSessions: pending, rpcClients: registry}
	mux := http.NewServeMux()
	app.registerSessionRoutes(mux)
	for _, test := range []struct{ input, expected string }{{physicalPath, configuredPath}, {pendingPath, pendingPath}, {alias, configuredPath}} {
		request := httptest.NewRequest(http.MethodPost, "/sessions/tags", strings.NewReader(url.Values{"session": {test.input}, "tag": {"Work"}, "assigned": {"true"}}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		var result struct {
			Session string   `json:"session"`
			Tags    []string `json:"tags"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Session != test.expected || !reflect.DeepEqual(result.Tags, []string{"work"}) {
			t.Fatalf("%q = %d %s", test.input, response.Code, response.Body.String())
		}
	}
	// Orphaned metadata must not contribute names or counts.
	if err := app.gatewayState.SetTag(filepath.Join(configured, "missing.jsonl"), "hidden", true); err != nil {
		t.Fatal(err)
	}
	for _, owned := range []bool{false, true} {
		if owned {
			app.ownsSession = func(_ *http.Request, path string) bool { return path == pendingPath }
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/tags", nil))
		var result struct {
			Tags []sessions.TagCount `json:"tags"`
		}
		count := 2
		if owned {
			count = 1
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || !reflect.DeepEqual(result.Tags, []sessions.TagCount{{Name: "work", Count: count}}) {
			t.Fatalf("pending catalog = %d %s", response.Code, response.Body.String())
		}
	}
}
