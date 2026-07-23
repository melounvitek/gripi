package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCommandCatalogPreservesBuiltinsAndFiltersPrivateExtensionCommands(t *testing.T) {
	commands := CommandsFrom(map[string]any{"data": map[string]any{"commands": []any{
		map[string]any{"name": "review", "source": "skill", "description": "Review code"},
		map[string]any{"name": "compact", "source": "skill", "description": "override"},
		map[string]any{"name": "gripi_tree_snapshot", "source": "extension"},
	}}})
	if len(commands) != 9 || commands[1]["name"] != "compact" || commands[2]["name"] != "export" || commands[8]["name"] != "review" {
		t.Fatalf("commands = %#v", commands)
	}
}

func TestPendingSessionRegistryPreservesCreationTime(t *testing.T) {
	now := time.Unix(1000, 0)
	registry := NewPendingSessionRegistry(func() time.Time { return now })
	registry.Remember("/pending", "/one")
	now = now.Add(time.Hour)
	registry.Remember("/pending", "/two")
	entries := registry.Entries()
	if len(entries) != 1 || entries[0].CWD != "/two" || !entries[0].CreatedAt.Equal(time.Unix(1000, 0)) {
		t.Fatalf("entries = %#v", entries)
	}
	registry.Forget("/pending")
	if _, ok := registry.CWD("/pending"); ok {
		t.Fatal("pending entry remained")
	}
	registry.Remember("/pending", "/three")
	if entries := registry.Entries(); len(entries) != 1 || entries[0].CWD != "/three" {
		t.Fatalf("re-remembered entries = %#v", entries)
	}
}

func TestMaintenanceStopsPromptlyAndContinuesAfterFailure(t *testing.T) {
	calls := make(chan int, 3)
	count := 0
	maintenance, err := NewMaintenance(time.Millisecond, func(context.Context) error {
		count++
		calls <- count
		if count == 1 {
			return errors.New("first")
		}
		return nil
	}, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	if !maintenance.Start(context.Background()) || maintenance.Start(context.Background()) {
		t.Fatal("maintenance start lifecycle")
	}
	deadline := time.After(time.Second)
	for {
		select {
		case call := <-calls:
			if call >= 2 {
				goto complete
			}
		case <-deadline:
			t.Fatal("maintenance did not continue")
		}
	}
complete:
	started := time.Now()
	if !maintenance.Stop() {
		t.Fatal("maintenance did not stop")
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("maintenance stop waited for interval")
	}
	if maintenance.Stop() {
		t.Fatal("duplicate stop succeeded")
	}
}

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
