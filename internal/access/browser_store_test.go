package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBrowserStorePersistsRubyCompatibleRequestsAndApprovalFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-access.json")
	store := NewBrowserStore(path)
	now := time.Date(2026, time.February, 3, 4, 5, 6, 789, time.UTC)
	store.now = func() time.Time { return now }

	created, err := store.EnsurePending("browser-token", "127.0.0.1", "Browser/1")
	if err != nil {
		t.Fatal(err)
	}
	if created.Token != "browser-token" || created.Requested || created.RequestedAt != nil {
		t.Fatalf("created request = %#v", created)
	}
	if matched := regexp.MustCompile(`^[A-Z0-9]{4}-[A-Z0-9]{4}$`).MatchString(created.Code); !matched {
		t.Fatalf("code = %q", created.Code)
	}
	if created.CreatedAt != "2026-02-03T04:05:06Z" {
		t.Fatalf("created_at = %q", created.CreatedAt)
	}
	if status, err := store.PendingStatus("browser-token"); err != nil || status != StatusCreated {
		t.Fatalf("PendingStatus() = %q, %v", status, err)
	}

	requested, err := store.RequestAccess("browser-token", "changed", "changed")
	if err != nil {
		t.Fatal(err)
	}
	if !requested.Requested || requested.RequestedAt == nil || *requested.RequestedAt != "2026-02-03T04:05:06Z" {
		t.Fatalf("requested request = %#v", requested)
	}
	if requested.IP != "127.0.0.1" || requested.UserAgent != "Browser/1" {
		t.Fatalf("existing metadata changed: %#v", requested)
	}
	if status, err := store.PendingStatus("browser-token"); err != nil || status != StatusPending {
		t.Fatalf("PendingStatus() = %q, %v", status, err)
	}
	pending, err := store.PendingRequests()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Code != created.Code {
		t.Fatalf("pending requests = %#v", pending)
	}

	approved, found, err := store.ApproveCode(created.Code)
	if err != nil {
		t.Fatal(err)
	}
	if !found || approved.Token != "browser-token" {
		t.Fatalf("ApproveCode() = %#v, %t", approved, found)
	}
	if ok, err := store.Approved("browser-token"); err != nil || !ok {
		t.Fatalf("Approved() = %t, %v", ok, err)
	}
	if status, err := store.PendingStatus("browser-token"); err != nil || status != StatusApproved {
		t.Fatalf("PendingStatus() = %q, %v", status, err)
	}

	var persisted map[string]any
	readJSON(t, path, &persisted)
	if len(persisted) != 2 || persisted["approved_browsers"] == nil || persisted["pending_requests"] == nil {
		t.Fatalf("persisted fields = %#v", persisted)
	}
	browsers := persisted["approved_browsers"].([]any)
	browser := browsers[0].(map[string]any)
	if browser["token"] != "browser-token" || browser["approved_at"] != "2026-02-03T04:05:06Z" || browser["label"] != "Browser/1" {
		t.Fatalf("approved browser = %#v", browser)
	}
}

func TestBrowserStoreDeniesRerequestsAndReplacesTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-access.json")
	store := NewBrowserStore(path)
	request, err := store.RequestAccess("old-token", "", "Old browser")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.DenyCode(request.Code); err != nil || !found {
		t.Fatalf("DenyCode() found = %t, err = %v", found, err)
	}
	if status, err := store.PendingStatus("old-token"); err != nil || status != StatusDenied {
		t.Fatalf("PendingStatus() = %q, %v", status, err)
	}
	if pending, err := store.PendingRequests(); err != nil || len(pending) != 0 {
		t.Fatalf("PendingRequests() = %#v, %v", pending, err)
	}

	rerequested, err := store.RequestAccess("old-token", "", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if rerequested.DeniedAt != nil {
		t.Fatalf("denied_at remains set: %#v", rerequested)
	}
	if _, found, err := store.ApproveToken("old-token"); err != nil || !found {
		t.Fatalf("ApproveToken() found = %t, err = %v", found, err)
	}
	if _, err := store.EnsurePending("old-token", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsurePending("new-token", "", ""); err != nil {
		t.Fatal(err)
	}
	if replaced, err := store.ReplaceBrowserToken("old-token", "new-token", "Replacement"); err != nil || !replaced {
		t.Fatalf("ReplaceBrowserToken() = %t, %v", replaced, err)
	}
	if _, found, err := store.PendingRequest("old-token"); err != nil || found {
		t.Fatalf("old pending request found = %t, %v", found, err)
	}
	if _, found, err := store.PendingRequest("new-token"); err != nil || found {
		t.Fatalf("new pending request found = %t, %v", found, err)
	}
	if ok, err := store.Approved("old-token"); err != nil || ok {
		t.Fatalf("old token approved = %t, %v", ok, err)
	}
	if ok, err := store.Approved("new-token"); err != nil || !ok {
		t.Fatalf("new token approved = %t, %v", ok, err)
	}
}

func TestBrowserStoreBoundsPersistedMetadataByUTF8Bytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-access.json")
	store := NewBrowserStore(path)
	request, err := store.EnsurePending(
		"browser",
		strings.Repeat("i", MaxIPBytes+10),
		"a"+strings.Repeat("ü", MaxUserAgentBytes),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.IP) > MaxIPBytes {
		t.Fatalf("IP length = %d", len(request.IP))
	}
	if len(request.UserAgent) > MaxUserAgentBytes || !utf8.ValidString(request.UserAgent) {
		t.Fatalf("user agent is %d bytes or invalid UTF-8: %q", len(request.UserAgent), request.UserAgent)
	}

	if _, err := store.ApproveCurrentBrowser("approved", "a"+strings.Repeat("ü", MaxUserAgentBytes)); err != nil {
		t.Fatal(err)
	}
	var persisted browserState
	readJSON(t, path, &persisted)
	if label := persisted.ApprovedBrowsers[0].Label; len(label) > MaxUserAgentBytes || !utf8.ValidString(label) {
		t.Fatalf("label is %d bytes or invalid UTF-8: %q", len(label), label)
	}
}

func TestBrowserStoreRecoversFromCorruptedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-access.json")
	if err := os.WriteFile(path, []byte("{not JSON"), 0o644); err != nil {
		t.Fatal(err)
	}

	request, err := NewBrowserStore(path).EnsurePending("recovered", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if request.Token != "recovered" {
		t.Fatalf("request = %#v", request)
	}
	var persisted browserState
	readJSON(t, path, &persisted)
	if len(persisted.ApprovedBrowsers) != 0 || len(persisted.PendingRequests) != 1 {
		t.Fatalf("recovered state = %#v", persisted)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestBrowserStorePrunesExpiredAndMalformedRequestsBeforeApplyingTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-access.json")
	now := time.Date(2026, time.January, 31, 12, 0, 0, 0, time.UTC)
	requests := make([]PendingRequest, 0, MaxPendingRequests)
	for index := range MaxPendingRequests - 1 {
		old := now.Add(-RequestedRetention - time.Second).Format(time.RFC3339)
		requests = append(requests, PendingRequest{
			Code: fmt.Sprintf("OLD%d", index), Token: fmt.Sprintf("old-%d", index), Requested: true,
			CreatedAt: old, RequestedAt: &old,
		})
	}
	malformed := "not-a-time"
	requests = append(requests, PendingRequest{Code: "BAD", Token: "bad", Requested: true, CreatedAt: malformed, RequestedAt: &malformed})
	writeJSON(t, path, browserState{ApprovedBrowsers: []ApprovedBrowser{}, PendingRequests: requests})

	store := NewBrowserStore(path)
	store.now = func() time.Time { return now }
	created, err := store.EnsurePending("new", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Token != "new" {
		t.Fatalf("created = %#v", created)
	}
	var persisted browserState
	readJSON(t, path, &persisted)
	if len(persisted.PendingRequests) != 1 || persisted.PendingRequests[0].Token != "new" {
		t.Fatalf("pending requests = %#v", persisted.PendingRequests)
	}
}

func TestBrowserStoreUsesDifferentRetentionForPendingStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-access.json")
	now := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	oldUnrequested := now.Add(-UnrequestedRetention - time.Second).Format(time.RFC3339)
	oldDenied := now.Add(-DeniedRetention - time.Second).Format(time.RFC3339)
	oldRequested := now.Add(-RequestedRetention - time.Second).Format(time.RFC3339)
	recent := now.Add(-time.Hour).Format(time.RFC3339)
	writeJSON(t, path, browserState{
		ApprovedBrowsers: []ApprovedBrowser{{Token: "approved", ApprovedAt: oldRequested, Label: "keep"}},
		PendingRequests: []PendingRequest{
			{Code: "OLD1", Token: "unrequested", CreatedAt: oldUnrequested},
			{Code: "OLD2", Token: "denied", Requested: true, CreatedAt: oldDenied, RequestedAt: &oldDenied, DeniedAt: &oldDenied},
			{Code: "OLD3", Token: "requested", Requested: true, CreatedAt: oldRequested, RequestedAt: &oldRequested},
			{Code: "NEW1", Token: "recent", Requested: true, CreatedAt: recent, RequestedAt: &recent},
		},
	})
	store := NewBrowserStore(path)
	store.now = func() time.Time { return now }
	if _, err := store.EnsurePending("new", "", ""); err != nil {
		t.Fatal(err)
	}

	var persisted browserState
	readJSON(t, path, &persisted)
	if len(persisted.ApprovedBrowsers) != 1 || persisted.ApprovedBrowsers[0].Token != "approved" {
		t.Fatalf("approved browsers = %#v", persisted.ApprovedBrowsers)
	}
	if len(persisted.PendingRequests) != 2 || persisted.PendingRequests[0].Token != "recent" || persisted.PendingRequests[1].Token != "new" {
		t.Fatalf("pending requests = %#v", persisted.PendingRequests)
	}
}

func TestBrowserStoreEnforcesTheActivePendingLimitAndBoundsDeniedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-access.json")
	store := NewBrowserStore(path)
	for index := range MaxPendingRequests {
		if _, err := store.EnsurePending(fmt.Sprintf("token-%d", index), "", ""); err != nil {
			t.Fatal(err)
		}
	}
	existing, err := store.RequestAccess("token-0", "", "")
	if err != nil {
		t.Fatalf("existing request rejected: %v", err)
	}
	_, err = store.EnsurePending("overflow", "", "")
	var full *PendingRequestsFullError
	if !errors.As(err, &full) || full.Limit != MaxPendingRequests {
		t.Fatalf("overflow error = %#v", err)
	}
	if _, found, err := store.DenyCode(existing.Code); err != nil || !found {
		t.Fatalf("DenyCode() found = %t, err = %v", found, err)
	}
	if _, err := store.EnsurePending("replacement", "", ""); err != nil {
		t.Fatalf("denial did not restore capacity: %v", err)
	}

	now := time.Date(2026, time.January, 10, 0, 0, 0, 0, time.UTC)
	terminal := make([]PendingRequest, 0, MaxTerminalRequests+1)
	for index := range MaxTerminalRequests + 1 {
		createdAt := now.Add(-time.Hour).Format(time.RFC3339)
		deniedAt := now.Add(-time.Hour + time.Duration(index)*time.Second).Format(time.RFC3339)
		terminal = append(terminal, PendingRequest{
			Code: fmt.Sprintf("DENIED%d", index), Token: fmt.Sprintf("denied-%d", index), Requested: true,
			CreatedAt: createdAt, RequestedAt: &createdAt, DeniedAt: &deniedAt,
		})
	}
	writeJSON(t, path, browserState{ApprovedBrowsers: []ApprovedBrowser{}, PendingRequests: terminal})
	store = NewBrowserStore(path)
	store.now = func() time.Time { return now }
	if _, err := store.EnsurePending("active", "", ""); err != nil {
		t.Fatal(err)
	}
	var persisted browserState
	readJSON(t, path, &persisted)
	if len(persisted.PendingRequests) != MaxTerminalRequests+1 {
		t.Fatalf("request count = %d", len(persisted.PendingRequests))
	}
	for _, request := range persisted.PendingRequests {
		if request.Token == "denied-0" {
			t.Fatal("oldest denied request was retained")
		}
	}
}

func TestBrowserStoreIsSafeForConcurrentRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-access.json")
	store := NewBrowserStore(path)
	const workers = MaxPendingRequests + 20
	start := make(chan struct{})
	results := make(chan error, workers)
	var wait sync.WaitGroup
	for index := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.EnsurePending(fmt.Sprintf("token-%d", index), "", "")
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	allowed := 0
	limited := 0
	for err := range results {
		if err == nil {
			allowed++
			continue
		}
		var full *PendingRequestsFullError
		if !errors.As(err, &full) {
			t.Fatalf("unexpected error: %v", err)
		}
		limited++
	}
	if allowed != MaxPendingRequests || limited != workers-MaxPendingRequests {
		t.Fatalf("allowed = %d, limited = %d", allowed, limited)
	}
	var persisted browserState
	readJSON(t, path, &persisted)
	if len(persisted.PendingRequests) != MaxPendingRequests {
		t.Fatalf("persisted request count = %d", len(persisted.PendingRequests))
	}
}

func TestBrowserStoreStatusUsesACoherentSnapshotDuringApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-access.json")
	store := NewBrowserStore(path)
	request, err := store.RequestAccess("browser", "", "")
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	statuses := make(chan Status, 100)
	var wait sync.WaitGroup
	for range 100 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			status, err := store.PendingStatus("browser")
			if err != nil {
				t.Error(err)
				return
			}
			statuses <- status
		}()
	}
	close(start)
	if _, found, err := store.ApproveCode(request.Code); err != nil || !found {
		t.Fatalf("ApproveCode() found = %t, err = %v", found, err)
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		if status != StatusPending && status != StatusApproved {
			t.Fatalf("status = %q", status)
		}
	}
}

func TestBrowserStorePrunesStaleRequestsDuringStatusChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-access.json")
	now := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	old := now.Add(-RequestedRetention - time.Second).Format(time.RFC3339)
	writeJSON(t, path, browserState{
		ApprovedBrowsers: []ApprovedBrowser{},
		PendingRequests:  []PendingRequest{{Code: "OLD", Token: "old", Requested: true, CreatedAt: old, RequestedAt: &old}},
	})
	store := NewBrowserStore(path)
	store.now = func() time.Time { return now }

	status, err := store.PendingStatus("old")
	if err != nil {
		t.Fatal(err)
	}
	if status != StatusCreated {
		t.Fatalf("status = %q", status)
	}
	var persisted browserState
	readJSON(t, path, &persisted)
	if len(persisted.PendingRequests) != 0 {
		t.Fatalf("pending requests = %#v", persisted.PendingRequests)
	}
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, target); err != nil {
		t.Fatalf("decode %s: %v\n%s", path, err, contents)
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}
