package push

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNotifierUsesFakeDeliveryForEverySubscriptionOwnedByTheUser(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	for _, suffix := range []string{"one", "two"} {
		if err := store.Upsert("owner-a", testSubscription(suffix)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Upsert("owner-b", testSubscription("other")); err != nil {
		t.Fatal(err)
	}
	fake := &fakeDelivery{statuses: []int{http.StatusCreated, http.StatusCreated}}
	notifier := NewNotifier(store, fake)
	notifier.sleep = noWait

	if err := notifier.Deliver(context.Background(), "owner-a", []byte(`{"title":"done"}`)); err != nil {
		t.Fatal(err)
	}
	calls := fake.calls()
	if len(calls) != 2 {
		t.Fatalf("delivery calls = %d", len(calls))
	}
	for _, call := range calls {
		if call.subscription.Endpoint == testSubscription("other").Endpoint {
			t.Fatalf("fake delivery received another owner's subscription: %#v", call)
		}
	}
}

func TestNotifierDoesNotLetOneSlowDeviceBlockOtherSubscriptions(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	for _, suffix := range []string{"slow", "fast"} {
		if err := store.Upsert("owner", testSubscription(suffix)); err != nil {
			t.Fatal(err)
		}
	}
	releaseSlow := make(chan struct{})
	fastCalled := make(chan struct{})
	delivery := deliveryFunc(func(_ context.Context, subscription Subscription, _ []byte) (DeliveryResult, error) {
		if strings.HasSuffix(subscription.Endpoint, "/slow") {
			<-releaseSlow
		} else {
			close(fastCalled)
		}
		return DeliveryResult{StatusCode: http.StatusCreated}, nil
	})
	notifier := NewNotifier(store, delivery)
	done := make(chan error, 1)
	go func() { done <- notifier.Deliver(context.Background(), "owner", []byte("payload")) }()

	select {
	case <-fastCalled:
	case <-time.After(time.Second):
		t.Fatal("fast subscription was blocked by another device")
	}
	close(releaseSlow)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNotifierRemovesSubscriptionsReturning404Or410(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	for _, suffix := range []string{"gone", "expired", "active"} {
		if err := store.Upsert("owner", testSubscription(suffix)); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeDelivery{statusesByEndpoint: map[string]int{
		testSubscription("gone").Endpoint:    http.StatusNotFound,
		testSubscription("expired").Endpoint: http.StatusGone,
		testSubscription("active").Endpoint:  http.StatusCreated,
	}}
	notifier := NewNotifier(store, fake)
	notifier.sleep = noWait

	if err := notifier.Deliver(context.Background(), "owner", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	subscriptions, err := store.List("owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(subscriptions) != 1 || subscriptions[0].Endpoint != testSubscription("active").Endpoint {
		t.Fatalf("remaining subscriptions = %#v", subscriptions)
	}
}

func TestNotifierRetriesTransientFailuresWithBoundedAttempts(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	if err := store.Upsert("owner", testSubscription("retry")); err != nil {
		t.Fatal(err)
	}
	fake := &fakeDelivery{statuses: []int{http.StatusServiceUnavailable, http.StatusCreated}}
	notifier := NewNotifier(store, fake)
	var delays []time.Duration
	notifier.sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}

	if err := notifier.Deliver(context.Background(), "owner", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if calls := len(fake.calls()); calls != 2 {
		t.Fatalf("retry calls = %d", calls)
	}
	if len(delays) != 1 || delays[0] != retryBaseDelay {
		t.Fatalf("retry delays = %v", delays)
	}

	fake = &fakeDelivery{statuses: []int{http.StatusBadGateway, http.StatusBadGateway, http.StatusBadGateway, http.StatusCreated}}
	notifier = NewNotifier(store, fake)
	delays = nil
	notifier.sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	if err := notifier.Deliver(context.Background(), "owner", []byte("payload")); err == nil {
		t.Fatal("unbounded transient failure succeeded")
	}
	if calls := len(fake.calls()); calls != MaxDeliveryAttempts {
		t.Fatalf("bounded retry calls = %d", calls)
	}
	if len(delays) != MaxDeliveryAttempts-1 {
		t.Fatalf("bounded retry delays = %v", delays)
	}
}

func TestNotifierDoesNotRetryAmbiguousNetworkFailures(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	if err := store.Upsert("owner", testSubscription("network")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	notifier := NewNotifier(store, deliveryFunc(func(context.Context, Subscription, []byte) (DeliveryResult, error) {
		calls++
		return DeliveryResult{}, errors.New("connection reset after request")
	}))
	notifier.sleep = noWait

	if err := notifier.Deliver(context.Background(), "owner", []byte("payload")); err == nil {
		t.Fatal("network failure succeeded")
	}
	if calls != 1 {
		t.Fatalf("network failure calls = %d", calls)
	}
}

func TestNotifierDoesNotRetryPermanentFailures(t *testing.T) {
	store := NewSubscriptionStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	if err := store.Upsert("owner", testSubscription("permanent")); err != nil {
		t.Fatal(err)
	}
	fake := &fakeDelivery{statuses: []int{http.StatusBadRequest, http.StatusCreated}}
	notifier := NewNotifier(store, fake)
	notifier.sleep = noWait

	if err := notifier.Deliver(context.Background(), "owner", []byte("payload")); err == nil {
		t.Fatal("permanent failure succeeded")
	}
	if calls := len(fake.calls()); calls != 1 {
		t.Fatalf("permanent failure calls = %d", calls)
	}
}

func TestWebPushDeliveryUsesVAPIDIdentityAndMaintainedWebPushPackage(t *testing.T) {
	root := t.TempDir()
	identity := NewVAPIDIdentity(filepath.Join(root, "vapid.json"))
	var requestURL string
	client := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		requestURL = request.URL.String()
		if request.Header.Get("Authorization") == "" || request.Header.Get("Content-Encoding") != "aes128gcm" {
			return nil, errors.New("missing Web Push headers")
		}
		if request.Header.Get("TTL") != "86400" {
			return nil, errors.New("missing TTL")
		}
		if request.Header.Get("Topic") == "" || len(request.Header.Get("Topic")) > 32 {
			return nil, errors.New("missing bounded topic")
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	delivery := NewWebPushDelivery(identity, "gripi@example.test", client)
	subscription := testCryptographicSubscription("adapter")
	result, err := delivery.Deliver(context.Background(), subscription, []byte(`{"title":"done"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", result.StatusCode)
	}
	parsed, err := url.Parse(requestURL)
	if err != nil || parsed.Path == "" {
		t.Fatalf("request URL = %q, %v", requestURL, err)
	}
}

type deliveryCall struct {
	subscription Subscription
	payload      []byte
}

type fakeDelivery struct {
	mu                 sync.Mutex
	statuses           []int
	statusesByEndpoint map[string]int
	callsLog           []deliveryCall
}

func (fake *fakeDelivery) Deliver(_ context.Context, subscription Subscription, payload []byte) (DeliveryResult, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.callsLog = append(fake.callsLog, deliveryCall{subscription: subscription, payload: append([]byte(nil), payload...)})
	index := len(fake.callsLog) - 1
	status, found := fake.statusesByEndpoint[subscription.Endpoint]
	if !found {
		status = fake.statuses[len(fake.statuses)-1]
		if index < len(fake.statuses) {
			status = fake.statuses[index]
		}
	}
	return DeliveryResult{StatusCode: status}, nil
}

func (fake *fakeDelivery) calls() []deliveryCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]deliveryCall(nil), fake.callsLog...)
}

func noWait(_ context.Context, _ time.Duration) error {
	return nil
}

func (fn httpClientFunc) Do(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type httpClientFunc func(*http.Request) (*http.Response, error)

type deliveryFunc func(context.Context, Subscription, []byte) (DeliveryResult, error)

func (fn deliveryFunc) Deliver(ctx context.Context, subscription Subscription, payload []byte) (DeliveryResult, error) {
	return fn(ctx, subscription, payload)
}

func testCryptographicSubscription(suffix string) Subscription {
	return Subscription{
		Endpoint: "https://push.example/sub/" + suffix,
		Keys:     testSubscription(suffix).Keys,
	}
}

var _ Delivery = (*fakeDelivery)(nil)
