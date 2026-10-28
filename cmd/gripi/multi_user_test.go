package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/config"
)

const aliceToken, bobToken = "piu_alice_token_0001", "piu_bob_token_000002"

// workspaceCookies are the browser cookies of the users of the gateway that multiUserGateway started last, by token.
var workspaceCookies map[string]string

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
	workspaceCookies = map[string]string{}
	for token, path := range map[string]string{aliceToken: alpha, bobToken: beta} {
		workspace := access.WorkspaceID(secret, token)
		if err := access.NewWorkspaceStore(cfg.WorkspaceAccessPath).ApproveWorkspace(workspace); err != nil {
			t.Fatal(err)
		}
		if _, err := owners.Claim(path, workspace); err != nil {
			t.Fatal(err)
		}
		workspaceCookies[token] = "gripi_workspace=" + workspace
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

func TestClaimGivesAUserASessionThatHasNoOwner(t *testing.T) {
	alpha, _ := multiUserGateway(t)
	// Like a session from Pi CLI, such as an orchestration worker.
	worker := filepath.Join(filepath.Dir(alpha), "worker.jsonl")
	appendSessionRecords(t, worker,
		map[string]any{"type": "session", "version": 3, "id": "0a1-worker-full-id", "timestamp": "2026-01-03T00:00:00Z", "cwd": t.TempDir()},
		map[string]any{"type": "message", "id": "user-1", "parentId": nil, "timestamp": "2026-01-03T00:00:01Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "The brief"}}}},
	)
	t.Setenv("GRIPI_USER_TOKEN", aliceToken)
	if _, listed, _ := runCLI("list", "--json"); strings.Contains(listed, "0a1-worker") {
		t.Fatalf("Alice lists the session before claiming it: %s", listed)
	}
	if code, stdout, stderr := runCLI("claim", "0a1-worker"); code != 1 || stdout != "" || !strings.Contains(stderr, "full ID") {
		t.Fatalf("gripi claim with a prefix = %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	code, stdout, stderr := runCLI("claim", "0a1-worker-full-id", "--json")
	if session := decodeSession(t, stdout); code != 0 || stderr != "" || session.Path != worker {
		t.Fatalf("gripi claim = %d, stderr %q, session %+v", code, stderr, session)
	}
	if code, stdout, stderr := runCLI("tag", "0a1-worker", "run"); code != 0 || !strings.Contains(stdout, "run") {
		t.Fatalf("gripi tag on the claimed session = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, _, stderr := runCLI("claim", worker); code != 0 {
		t.Fatalf("gripi claim on Alice's own session = %d, stderr %q", code, stderr)
	}

	t.Setenv("GRIPI_USER_TOKEN", bobToken)
	if code, stdout, stderr := runCLI("claim", worker); code != 1 || stdout != "" || !strings.Contains(stderr, "another user") {
		t.Fatalf("gripi claim on Alice's session for Bob = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, listed, _ := runCLI("list", "--json"); strings.Contains(listed, "0a1-worker") {
		t.Fatalf("Bob lists Alice's session: %s", listed)
	}
}

func TestClaimOnASingleUserGatewayOnlyFindsTheSession(t *testing.T) {
	alpha, _ := fakePiGateway(t)
	code, stdout, stderr := runCLI("claim", "0a1-alpha", "--json")
	if session := decodeSession(t, stdout); code != 0 || stderr != "" || session.Path != alpha {
		t.Fatalf("gripi claim = %d, stderr %q, session %+v", code, stderr, session)
	}
	if code, stdout, stderr := runCLI("claim", "0a1-a"); code != 1 || stdout != "" || !strings.Contains(stderr, "full ID") {
		t.Fatalf("gripi claim with a prefix = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
