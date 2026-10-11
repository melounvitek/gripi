package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPromptModeSendsShellCommandsAsPrompts(t *testing.T) {
	handler, alpha, _, _ := fakePiGateway(t)
	for _, mode := range []string{"", "prompt"} {
		request := formActionRequest("/prompt", map[string]string{"session": alpha, "message": "!pwd", "bash_mode": mode}, true)
		if response := serveAction(handler, request); response.Code != http.StatusOK {
			t.Fatalf("mode %q = %d %s", mode, response.Code, response.Body.String())
		}
	}
	var received []string
	for _, line := range strings.Split(strings.TrimSpace(readFakePiLog(t)), "\n") {
		var entry struct {
			Command map[string]any `json:"command"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		switch entry.Command["type"] {
		case "bash":
			received = append(received, fmt.Sprint("bash ", entry.Command["command"]))
		case "prompt":
			received = append(received, fmt.Sprint("prompt ", entry.Command["message"]))
		}
	}
	if strings.Join(received, ", ") != "bash pwd, prompt !pwd" {
		t.Fatalf("Pi received %q", received)
	}
}
