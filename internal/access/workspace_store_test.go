package access

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

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
	owners := NewWorkspaceOwnershipStore(ownersPath)
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

func TestWorkspaceOwnershipCannotBeClaimedByAnotherWorkspace(t *testing.T) {
	store := NewWorkspaceOwnershipStore(filepath.Join(t.TempDir(), "owners.json"))
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
	store := NewWorkspaceOwnershipStore(path)

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
	owners := NewWorkspaceOwnershipStore(filepath.Join(root, "owners.json"))
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
