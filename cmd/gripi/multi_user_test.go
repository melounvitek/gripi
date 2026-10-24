package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/config"
)

const aliceToken, bobToken = "piu_alice_token_0001", "piu_bob_token_000002"

// multiUserGateway runs fakePiGateway in multi-user mode. Alice owns 0a1-alpha and Bob owns 0a1-beta.
func multiUserGateway(t *testing.T) (alpha, beta string) {
	t.Helper()
	root := t.TempDir()
	var cfg config.Config
	alpha, beta = fakePiGateway(t, func(configured *config.Config) {
		configured.MultiUserMode = true
		configured.WorkspaceSecretPath = filepath.Join(root, "secret")
		configured.WorkspaceAccessPath = filepath.Join(root, "workspace-access.json")
		configured.WorkspaceOwnershipPath = filepath.Join(root, "owners.json")
		cfg = *configured
	})
	secret, err := access.NewWorkspaceSecretStore(cfg.WorkspaceSecretPath).Secret()
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := newAdapter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	owners := access.NewWorkspaceOwnershipStore(cfg.WorkspaceOwnershipPath, adapter.Store())
	for token, path := range map[string]string{aliceToken: alpha, bobToken: beta} {
		workspace := access.WorkspaceID(secret, token)
		if err := access.NewWorkspaceStore(cfg.WorkspaceAccessPath).ApproveWorkspace(workspace); err != nil {
			t.Fatal(err)
		}
		if _, err := owners.Claim(path, workspace); err != nil {
			t.Fatal(err)
		}
	}
	return alpha, beta
}

func TestCommandsOfAMultiUserGatewayActForTheUserOfTheirToken(t *testing.T) {
	multiUserGateway(t)
	if code, stdout, stderr := runCLI("list"); code != 1 || stdout != "" || !strings.Contains(stderr, "GRIPI_USER_TOKEN") {
		t.Fatalf("gripi list without a token = %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	t.Setenv("GRIPI_USER_TOKEN", aliceToken)
	code, stdout, stderr := runCLI("new", t.TempDir(), "--json")
	started := decodeSession(t, stdout)
	if code != 0 || stderr != "" {
		t.Fatalf("gripi new = %d, stderr %q", code, stderr)
	}
	if _, listed, _ := runCLI("list", "--json"); !strings.Contains(listed, "0a1-alpha") || !strings.Contains(listed, started.Path) || strings.Contains(listed, "0a1-beta") {
		t.Fatalf("Alice's sessions = %s", listed)
	}
	if code, stdout, stderr := runCLI("show", "0a1-beta"); code != 1 || stdout != "" || !strings.Contains(stderr, "no session matches") {
		t.Fatalf("gripi show on Bob's session = %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	t.Setenv("GRIPI_USER_TOKEN", bobToken)
	if _, listed, _ := runCLI("list", "--json"); strings.Contains(listed, "0a1-alpha") || strings.Contains(listed, started.Path) || !strings.Contains(listed, "0a1-beta") {
		t.Fatalf("Bob's sessions = %s", listed)
	}
}
