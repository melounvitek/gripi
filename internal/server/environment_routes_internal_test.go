package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/environment"
)

func TestMultiUserSessionWithoutAnOwnerGetsNoVariables(t *testing.T) {
	root := t.TempDir()
	received := filepath.Join(root, "pi-environment")
	script := filepath.Join(root, "pi")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nenv > '"+received+".tmp'\nmv '"+received+".tmp' '"+received+"'\nexec cat\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Environment: "test", Home: root, SessionsRoot: filepath.Join(root, "sessions"), EnvironmentPath: filepath.Join(root, "environment.json"),
		WorkspaceSecretPath: filepath.Join(root, "secret"), WorkspaceAccessPath: filepath.Join(root, "workspace-access.json"), WorkspaceOwnershipPath: filepath.Join(root, "owners.json"),
		BrowserAuthDisabled: true, MultiUserMode: true, PiCommand: []string{script},
	}
	// Saved while the gateway ran in single-user mode.
	if _, err := environment.NewStore(cfg.EnvironmentPath).Save("", "", environment.Variable{Name: "ENVTEST_TOKEN", Value: "gateway owner's token"}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(*Handler).Close(context.Background()) })

	if _, err := handler.(*Handler).app.rpcClients.EnsureClient(filepath.Join(cfg.SessionsRoot, "unowned.jsonl")); err != nil {
		t.Fatal(err)
	}
	var contents []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if contents, err = os.ReadFile(received); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("Pi did not start: %v", err)
	}
	if strings.Contains(string(contents), "ENVTEST_TOKEN") {
		t.Fatal("a session without an owner got the variables saved in single-user mode")
	}
}
