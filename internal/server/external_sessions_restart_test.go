package server_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRestartedGatewayKeepsFollowingSessionUsedInPiCLI(t *testing.T) {
	cfg, path, _, _ := fakePiConfig(t)
	running := startGateway(t, cfg)
	if followedAsExternal(running) {
		t.Fatal("a session nobody else wrote to is followed as external")
	}
	appendSessionEntry(t, path)
	if !followedAsExternal(running) {
		t.Fatal("a session Pi CLI wrote to is not followed as external")
	}

	// Pi CLI writes nothing more, so only what the gateway saved can tell the session is external.
	if !followedAsExternal(startGateway(t, cfg)) {
		t.Fatal("a restart forgot that Pi CLI is using the session")
	}
}

func TestRestartedGatewayFollowsSessionPiCLIWroteToWhileItWasDown(t *testing.T) {
	cfg, path, _, _ := fakePiConfig(t)
	running := startGateway(t, cfg)
	if followedAsExternal(running) {
		t.Fatal("a session nobody else wrote to is followed as external")
	}
	stopGateway(t, running)
	appendSessionEntry(t, path)
	if !followedAsExternal(startGateway(t, cfg)) {
		t.Fatal("a restart missed what Pi CLI wrote while the gateway was down")
	}
}

func TestRestartedGatewayDoesNotTakeItsOwnPiForPiCLI(t *testing.T) {
	cfg, path, _, _ := fakePiConfig(t)
	running := startGateway(t, cfg)
	if response := postPrompt(running, browserHost, url.Values{"session": {path}, "message": {"Show the deterministic browser response"}}); response.Code != http.StatusOK {
		t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
	}
	// Watching the file leaves the gateway's snapshot older than the reply, the case in
	// which a saved baseline would make the reply look like Pi CLI's.
	replied := func() bool {
		contents, err := os.ReadFile(path)
		return err == nil && bytes.Count(contents, []byte(`"stopReason":"stop"`)) == 2
	}
	for deadline := time.Now().Add(5 * time.Second); !replied(); time.Sleep(25 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the gateway's Pi never replied")
		}
	}
	stopGateway(t, running)
	if followedAsExternal(startGateway(t, cfg)) {
		t.Fatal("a restart took the reply of the gateway's own Pi for Pi CLI's")
	}
}

func TestGatewayKilledBeforeSavingDoesNotTakeItsOwnPiForPiCLI(t *testing.T) {
	cfg, path, _, _ := fakePiConfig(t)
	running := startGateway(t, cfg)
	followedAsExternal(running)
	stopGateway(t, running)

	// This gateway is not stopped before the next one starts, as if it were killed. It
	// saves nothing about the entry, which stands in for a reply from its own Pi.
	startGateway(t, cfg)
	appendSessionEntry(t, path)
	if followedAsExternal(startGateway(t, cfg)) {
		t.Fatal("what the gateway saw two runs ago made its own Pi's reply look like Pi CLI's")
	}
}

func stopGateway(t *testing.T, handler http.Handler) {
	t.Helper()
	if err := handler.(interface{ Close(context.Context) error }).Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// followedAsExternal looks at the sessions as a sidebar poll does in a browser that shows Pi CLI sessions.
func followedAsExternal(handler http.Handler) bool {
	request := getActionRequest("/sidebar?no_session=1")
	request.AddCookie(&http.Cookie{Name: "gripi_show_cli_sessions", Value: "1"})
	response := serveAction(handler, request)
	return response.Code == http.StatusOK && strings.Contains(response.Body.String(), `class="session-row is-external`)
}

func appendSessionEntry(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"type":"message","id":"appended","parentId":"assistant-1","timestamp":"2026-01-03T00:00:00Z","message":{"role":"user","content":[{"type":"text","text":"Appended"}]}}` + "\n")
	if closeErr := file.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
}
