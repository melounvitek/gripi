package push

import (
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestSubscriptionStorePreservesMalformedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	malformed := []byte(`{"subscriptions":`)
	if err := os.WriteFile(path, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewSubscriptionStore(path)

	if _, err := store.List("owner"); err == nil {
		t.Fatal("List() succeeded")
	}
	if err := store.Upsert("owner", testSubscription("one")); err == nil {
		t.Fatal("Upsert() succeeded")
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != string(malformed) {
		t.Fatalf("malformed state was rewritten: %q", persisted)
	}
}

func TestSubscriptionStoreUpsertsRemovesAndScopesByOwner(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	first := testSubscription("one")
	second := testSubscription("two")
	updated := first
	updated.Keys.Auth = encodeKey([]byte("fedcba9876543210"))

	if err := store.Upsert("owner-a", first); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert("owner-a", second); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert("owner-b", testSubscription("owner-b")); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert("owner-a", updated); err != nil {
		t.Fatal(err)
	}

	ownerA, err := store.List("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerA) != 2 || ownerA[0].Endpoint != updated.Endpoint || ownerA[0].Keys.Auth != updated.Keys.Auth || ownerA[1].Endpoint != second.Endpoint {
		t.Fatalf("owner-a subscriptions = %#v", ownerA)
	}
	ownerB, err := store.List("owner-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerB) != 1 || ownerB[0].Endpoint != testSubscription("owner-b").Endpoint {
		t.Fatalf("owner-b subscriptions = %#v", ownerB)
	}

	removed, err := store.Remove("owner-a", testSubscription("owner-b").Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("Remove() removed another owner's subscription")
	}
	removed, err = store.Remove("owner-a", updated.Endpoint)
	if err != nil || !removed {
		t.Fatalf("Remove() = %t, %v", removed, err)
	}
	ownerA, err = store.List("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerA) != 1 || ownerA[0].Endpoint != second.Endpoint {
		t.Fatalf("owner-a after removal = %#v", ownerA)
	}
	ownerB, err = store.List("owner-b")
	if err != nil || len(ownerB) != 1 {
		t.Fatalf("owner-b after removal = %#v, %v", ownerB, err)
	}
}

func TestSubscriptionStoreListsAndRemovesOwners(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	for ownerIndex, owner := range []string{"owner-b", "owner-a"} {
		for device := range 2 {
			if err := store.Upsert(owner, testSubscription(fmt.Sprintf("%d-%d", ownerIndex, device))); err != nil {
				t.Fatal(err)
			}
		}
	}
	owners, err := store.Owners()
	if err != nil || !slices.Equal(owners, []string{"owner-a", "owner-b"}) {
		t.Fatalf("owners = %#v, %v", owners, err)
	}
	if err := store.RemoveOwner("owner-a"); err != nil {
		t.Fatal(err)
	}
	if subscriptions, err := store.List("owner-a"); err != nil || len(subscriptions) != 0 {
		t.Fatalf("removed owner subscriptions = %#v, %v", subscriptions, err)
	}
	if subscriptions, err := store.List("owner-b"); err != nil || len(subscriptions) != 2 {
		t.Fatalf("preserved owner subscriptions = %#v, %v", subscriptions, err)
	}
}

func TestSubscriptionStoreMovesAnEndpointToItsCurrentOwner(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	subscription := testSubscription("device")
	if err := store.Upsert("old-owner", subscription); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert("new-owner", subscription); err != nil {
		t.Fatal(err)
	}

	oldSubscriptions, err := store.List("old-owner")
	if err != nil {
		t.Fatal(err)
	}
	newSubscriptions, err := store.List("new-owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(oldSubscriptions) != 0 || len(newSubscriptions) != 1 || newSubscriptions[0] != subscription {
		t.Fatalf("subscriptions after owner move = old %#v, new %#v", oldSubscriptions, newSubscriptions)
	}
}

func TestSubscriptionStoreEnforcesPerOwnerAndTotalLimits(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "per-owner.json"))
	for index := range MaxSubscriptionsPerOwner {
		if err := store.Upsert("owner", testSubscription(fmt.Sprintf("owner-%d", index))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Upsert("owner", testSubscription("overflow")); !errors.Is(err, ErrSubscriptionLimit) {
		t.Fatalf("per-owner overflow = %v", err)
	}
	if err := store.Upsert("another-owner", testSubscription("new-owner")); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	path := filepath.Join(root, "total.json")
	persisted := subscriptionState{Subscriptions: make([]persistedSubscription, 0, MaxSubscriptions)}
	for index := range MaxSubscriptions {
		subscription := testSubscription(fmt.Sprintf("total-%d", index))
		persisted.Subscriptions = append(persisted.Subscriptions, persistedSubscription{Owner: fmt.Sprintf("owner-%d", index), Subscription: subscription})
	}
	writeSubscriptionJSON(t, path, persisted)
	if err := NewSubscriptionStore(path).Upsert("new-owner", testSubscription("total-overflow")); !errors.Is(err, ErrSubscriptionLimit) {
		t.Fatalf("total overflow = %v", err)
	}
}

func TestSubscriptionStoreRejectsInvalidInput(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	valid := testSubscription("valid")
	tests := []struct {
		name         string
		owner        string
		subscription Subscription
	}{
		{name: "empty owner", owner: "", subscription: valid},
		{name: "oversized owner", owner: strings.Repeat("o", MaxOwnerBytes+1), subscription: valid},
		{name: "invalid owner utf8", owner: string([]byte{0xff}), subscription: valid},
		{name: "empty endpoint", owner: "owner", subscription: Subscription{Keys: valid.Keys}},
		{name: "non https endpoint", owner: "owner", subscription: Subscription{Endpoint: "http://push.example/sub", Keys: valid.Keys}},
		{name: "malformed endpoint", owner: "owner", subscription: Subscription{Endpoint: "https://", Keys: valid.Keys}},
		{name: "oversized endpoint", owner: "owner", subscription: Subscription{Endpoint: "https://" + strings.Repeat("a", MaxEndpointBytes), Keys: valid.Keys}},
		{name: "invalid auth key", owner: "owner", subscription: Subscription{Endpoint: valid.Endpoint, Keys: SubscriptionKeys{Auth: "bad", P256dh: valid.Keys.P256dh}}},
		{name: "wrong auth key length", owner: "owner", subscription: Subscription{Endpoint: valid.Endpoint, Keys: SubscriptionKeys{Auth: encodeKey(make([]byte, 15)), P256dh: valid.Keys.P256dh}}},
		{name: "wrong p256dh key length", owner: "owner", subscription: Subscription{Endpoint: valid.Endpoint, Keys: SubscriptionKeys{Auth: valid.Keys.Auth, P256dh: encodeKey(make([]byte, 64))}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Upsert(tt.owner, tt.subscription); err == nil {
				t.Fatal("Upsert() accepted invalid input")
			}
		})
	}
	if subscriptions, err := store.List("owner"); err != nil || len(subscriptions) != 0 {
		t.Fatalf("invalid input persisted subscriptions = %#v, %v", subscriptions, err)
	}
}

func TestSubscriptionStoreIsSafeForConcurrentUpserts(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	const workers = MaxSubscriptionsPerOwner + 8
	start := make(chan struct{})
	results := make(chan error, workers)
	var wait sync.WaitGroup
	for index := range workers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results <- store.Upsert("owner", testSubscription(fmt.Sprintf("concurrent-%d", index)))
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)

	allowed := 0
	for err := range results {
		if err == nil {
			allowed++
			continue
		}
		if !errors.Is(err, ErrSubscriptionLimit) {
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if allowed != MaxSubscriptionsPerOwner {
		t.Fatalf("allowed concurrent upserts = %d", allowed)
	}
	subscriptions, err := store.List("owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(subscriptions) != MaxSubscriptionsPerOwner {
		t.Fatalf("persisted subscriptions = %d", len(subscriptions))
	}
}

func testSubscription(suffix string) Subscription {
	curve := elliptic.P256()
	return Subscription{
		Endpoint: "https://push.example/sub/" + suffix,
		Keys: SubscriptionKeys{
			Auth:   encodeKey([]byte("0123456789abcdef")),
			P256dh: encodeKey(elliptic.Marshal(curve, curve.Params().Gx, curve.Params().Gy)),
		},
	}
}

func encodeKey(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func writeSubscriptionJSON(t *testing.T, path string, value subscriptionState) {
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
