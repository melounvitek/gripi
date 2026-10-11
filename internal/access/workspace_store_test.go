package access

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agentpi "github.com/melounvitek/gripi/internal/agent/pi"
)

func TestWorkspaceStorePreservesMalformedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace-access.json")
	malformed := []byte(`{"approved_workspaces":`)
	if err := os.WriteFile(path, malformed, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewWorkspaceStore(path)

	if approved, err := store.Approved("workspace"); err == nil || approved {
		t.Fatalf("Approved() = %t, %v", approved, err)
	}
	if _, err := store.RequestAccess("workspace", "browser"); err == nil {
		t.Fatal("RequestAccess() succeeded")
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != string(malformed) {
		t.Fatalf("malformed state was rewritten: %q", persisted)
	}
}

func TestWorkspaceStoresPreserveExistingStateFormats(t *testing.T) {
	root := t.TempDir()
	accessPath := filepath.Join(root, "workspace-access.json")
	now := time.Now().UTC().Format(time.RFC3339)
	existing := `{"approved_workspaces":[{"workspace_id":"workspace-a","approved_at":"` + now + `"}],"pending_requests":[{"code":"ABCD-EFGH","workspace_id":"workspace-b","browser_token":"browser","created_at":"` + now + `","requested_at":"` + now + `"}]}`
	if err := os.WriteFile(accessPath, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewWorkspaceStore(accessPath)
	approved, err := store.Approved("workspace-a")
	if err != nil || !approved {
		t.Fatalf("approved = %v, %v", approved, err)
	}
	pending, found, err := store.RequestForCode("ABCD-EFGH")
	if err != nil || !found || pending.BrowserToken != "browser" {
		t.Fatalf("pending = %+v, %v, %v", pending, found, err)
	}
	if _, found, err := store.ApproveCode("ABCD-EFGH"); err != nil || !found {
		t.Fatalf("approve = %v, %v", found, err)
	}
	contents, err := os.ReadFile(accessPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(contents, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["approved_workspaces"]; !ok {
		t.Fatal("approved_workspaces missing")
	}
	if _, ok := raw["pending_requests"]; !ok {
		t.Fatal("pending_requests missing")
	}

	ownersPath := filepath.Join(root, "session-owners.json")
	session := filepath.Join(root, "sessions", "one.jsonl")
	ownersJSON := `{"sessions":{` + quoted(session) + `:"workspace-a"}}`
	if err := os.WriteFile(ownersPath, []byte(ownersJSON), 0600); err != nil {
		t.Fatal(err)
	}
	owners := NewWorkspaceOwnershipStore(ownersPath, nil)
	owned, err := owners.OwnedBy(session, "workspace-a")
	if err != nil || !owned {
		t.Fatalf("owned = %v, %v", owned, err)
	}
	hash := sha256Hex(session)
	ownsHash, err := owners.OwnsHash(hash, "workspace-a")
	if err != nil || !ownsHash {
		t.Fatalf("hash = %v, %v", ownsHash, err)
	}
}

func TestWorkspaceRequestsStayBoundToEachBrowser(t *testing.T) {
	store := NewWorkspaceStore(filepath.Join(t.TempDir(), "access.json"))
	first, err := store.RequestAccess("workspace", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RequestAccess("workspace", "second")
	if err != nil {
		t.Fatal(err)
	}
	if first.Code == second.Code {
		t.Fatal("browser requests shared a code")
	}
	if _, found, err := store.ApproveCode(first.Code); err != nil || !found {
		t.Fatal(err)
	}
	approved, err := store.Approved("workspace")
	if err != nil || !approved {
		t.Fatalf("approved = %v, %v", approved, err)
	}
}

func TestWorkspaceOwnershipNormalizesPersistedPhysicalSessionPaths(t *testing.T) {
	root := t.TempDir()
	physicalRoot := filepath.Join(root, "physical-sessions")
	configuredRoot := filepath.Join(root, "configured-sessions")
	if err := os.Mkdir(physicalRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physicalRoot, configuredRoot); err != nil {
		t.Fatal(err)
	}
	physicalPath := filepath.Join(physicalRoot, "session.jsonl")
	configuredPath := filepath.Join(configuredRoot, "session.jsonl")
	ownersPath := filepath.Join(root, "owners.json")
	contents := `{"sessions":{` + quoted(physicalPath) + `:"workspace-a"}}`
	if err := os.WriteFile(ownersPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewWorkspaceOwnershipStore(ownersPath, agentpi.New(agentpi.Options{SessionsRoot: configuredRoot}).Store())

	owned, err := store.OwnedBy(configuredPath, "workspace-a")
	if err != nil || !owned {
		t.Fatalf("owned = %v, %v", owned, err)
	}
	paths, err := store.OwnedPaths("workspace-a")
	if err != nil || !paths[configuredPath] || paths[physicalPath] {
		t.Fatalf("paths = %#v, %v", paths, err)
	}
	for _, path := range []string{configuredPath, physicalPath} {
		owned, err := store.OwnsHash(sha256Hex(path), "workspace-a")
		if err != nil || !owned {
			t.Fatalf("hash for %q = %v, %v", path, owned, err)
		}
	}
	if created, err := store.Claim(configuredPath, "workspace-a"); err != nil || created {
		t.Fatalf("claim = %v, %v", created, err)
	}
	persisted, err := os.ReadFile(ownersPath)
	if err != nil || !strings.Contains(string(persisted), configuredPath) || strings.Contains(string(persisted), physicalPath) {
		t.Fatalf("persisted = %q, %v", persisted, err)
	}
	conflicting := `{"sessions":{` + quoted(configuredPath) + `:"workspace-a",` + quoted(physicalPath) + `:"workspace-b"}}`
	if err := os.WriteFile(ownersPath, []byte(conflicting), 0600); err != nil {
		t.Fatal(err)
	}
	if owned, err := store.OwnedBy(configuredPath, "workspace-a"); owned || !errors.Is(err, ErrSessionOwnedByAnotherWorkspace) {
		t.Fatalf("conflicting ownership = %v, %v", owned, err)
	}
}

func TestWorkspaceOwnershipCannotBeClaimedByAnotherWorkspace(t *testing.T) {
	store := NewWorkspaceOwnershipStore(filepath.Join(t.TempDir(), "owners.json"), nil)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	created, err := store.Claim(path, "workspace-a")
	if err != nil || !created {
		t.Fatalf("first claim = %v, %v", created, err)
	}
	created, err = store.Claim(path, "workspace-a")
	if err != nil || created {
		t.Fatalf("repeated claim = %v, %v", created, err)
	}
	if _, err := store.Claim(path, "workspace-b"); !errors.Is(err, ErrSessionOwnedByAnotherWorkspace) {
		t.Fatalf("claim conflict = %v", err)
	}
	owned, err := store.OwnedBy(path, "workspace-a")
	if err != nil || !owned {
		t.Fatalf("original owner = %v, %v", owned, err)
	}
}

func TestWorkspaceOwnershipFailsClosedWhenStateIsMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owners.json")
	contents := []byte(`{"sessions":`)
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewWorkspaceOwnershipStore(path, nil)

	if _, err := store.Claim("/tmp/session.jsonl", "workspace"); err == nil {
		t.Fatal("claim succeeded with malformed ownership state")
	}
	if owned, err := store.OwnedBy("/tmp/session.jsonl", "workspace"); err == nil || owned {
		t.Fatalf("owned = %v, %v", owned, err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != string(contents) {
		t.Fatalf("malformed ownership state was rewritten: %q", persisted)
	}
}

func TestWorkspaceSecretAndOwnershipAreRaceSafe(t *testing.T) {
	root := t.TempDir()
	secretStore := NewWorkspaceSecretStore(filepath.Join(root, "secret"))
	results := make(chan string, 16)
	for range 16 {
		go func() {
			secret, err := secretStore.Secret()
			if err != nil {
				results <- "error:" + err.Error()
				return
			}
			results <- secret
		}()
	}
	first := ""
	for range 16 {
		value := <-results
		if first == "" {
			first = value
		}
		if value != first {
			t.Fatalf("secret mismatch %q != %q", value, first)
		}
	}
	owners := NewWorkspaceOwnershipStore(filepath.Join(root, "owners.json"), nil)
	done := make(chan error, 20)
	for index := range 20 {
		go func() {
			_, err := owners.Claim(filepath.Join(root, "sessions", fmtIntTest(index)+".jsonl"), "workspace")
			done <- err
		}()
	}
	for range 20 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func quoted(value string) string { contents, _ := json.Marshal(value); return string(contents) }
func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
func fmtIntTest(value int) string { return strconv.Itoa(value) }
