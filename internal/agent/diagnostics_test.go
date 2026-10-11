package agent

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestDiagnosticsEmitsMetadataWithoutCommandContents(t *testing.T) {
	var output bytes.Buffer
	diagnostics := &Diagnostics{Enabled: true, Writer: &output, Clock: func() time.Time { return time.Unix(1000, 0) }}
	diagnostics.Log("command_started", map[string]any{"command": "prompt", "rpc_id": "prompt-1"})
	var payload map[string]any
	if json.Unmarshal(output.Bytes(), &payload) != nil {
		t.Fatalf("diagnostic = %q", output.String())
	}
	if payload["component"] != "pi_rpc" || payload["command"] != "prompt" || payload["rpc_id"] != "prompt-1" {
		t.Fatalf("payload = %#v", payload)
	}
	if bytes.Contains(output.Bytes(), []byte("secret prompt")) {
		t.Fatal("diagnostics leaked command contents")
	}
}
