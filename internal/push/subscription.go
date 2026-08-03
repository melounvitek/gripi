package push

import (
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/melounvitek/gripi/internal/state"
)

const (
	MaxOwnerBytes             = 128
	MaxEndpointBytes          = 2048
	MaxEncodedKeyBytes        = 128
	MaxSubscriptionsPerOwner  = 10
	MaxSubscriptions          = 1000
	MaxSubscriptionStateBytes = 4 << 20
)

var (
	ErrInvalidOwner             = errors.New("invalid subscription owner")
	ErrInvalidSubscription      = errors.New("invalid push subscription")
	ErrInvalidSubscriptionState = errors.New("invalid push subscription state")
	ErrSubscriptionLimit        = errors.New("push subscription limit reached")
)

type SubscriptionKeys struct {
	Auth   string `json:"auth"`
	P256dh string `json:"p256dh"`
}

type Subscription struct {
	Endpoint string           `json:"endpoint"`
	Keys     SubscriptionKeys `json:"keys"`
}

type persistedSubscription struct {
	Owner string `json:"owner"`
	Subscription
}

type subscriptionState struct {
	Subscriptions []persistedSubscription `json:"subscriptions"`
}

type SubscriptionStore struct {
	file *state.File
	mu   sync.Mutex
}

func NewSubscriptionStore(path string) *SubscriptionStore {
	return &SubscriptionStore{file: state.NewFile(path)}
}

func (store *SubscriptionStore) Upsert(owner string, subscription Subscription) error {
	if err := validateOwner(owner); err != nil {
		return err
	}
	if err := ValidateSubscription(subscription); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.readState()
	if err != nil {
		return err
	}
	endpointIndex := -1
	ownerCount := 0
	for index, candidate := range current.Subscriptions {
		if candidate.Endpoint == subscription.Endpoint {
			endpointIndex = index
		}
		if candidate.Owner == owner {
			ownerCount++
		}
	}
	if endpointIndex >= 0 {
		candidate := &current.Subscriptions[endpointIndex]
		if candidate.Owner != owner && ownerCount >= MaxSubscriptionsPerOwner {
			return fmt.Errorf("%w: owner limit is %d", ErrSubscriptionLimit, MaxSubscriptionsPerOwner)
		}
		candidate.Owner = owner
		candidate.Subscription = subscription
		return store.writeState(current)
	}
	if len(current.Subscriptions) >= MaxSubscriptions {
		return fmt.Errorf("%w: total limit is %d", ErrSubscriptionLimit, MaxSubscriptions)
	}
	if ownerCount >= MaxSubscriptionsPerOwner {
		return fmt.Errorf("%w: owner limit is %d", ErrSubscriptionLimit, MaxSubscriptionsPerOwner)
	}
	current.Subscriptions = append(current.Subscriptions, persistedSubscription{Owner: owner, Subscription: subscription})
	return store.writeState(current)
}

func (store *SubscriptionStore) List(owner string) ([]Subscription, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.readState()
	if err != nil {
		return nil, err
	}
	result := make([]Subscription, 0)
	for _, candidate := range current.Subscriptions {
		if candidate.Owner == owner {
			result = append(result, candidate.Subscription)
		}
	}
	return result, nil
}

func (store *SubscriptionStore) Owners() ([]string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.readState()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, subscription := range current.Subscriptions {
		seen[subscription.Owner] = true
	}
	result := make([]string, 0, len(seen))
	for owner := range seen {
		result = append(result, owner)
	}
	sort.Strings(result)
	return result, nil
}

func (store *SubscriptionStore) RemoveOwner(owner string) error {
	if err := validateOwner(owner); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.readState()
	if err != nil {
		return err
	}
	filtered := current.Subscriptions[:0]
	for _, subscription := range current.Subscriptions {
		if subscription.Owner != owner {
			filtered = append(filtered, subscription)
		}
	}
	if len(filtered) == len(current.Subscriptions) {
		return nil
	}
	current.Subscriptions = filtered
	return store.writeState(current)
}

func (store *SubscriptionStore) Remove(owner, endpoint string) (bool, error) {
	if err := validateOwner(owner); err != nil {
		return false, err
	}
	if err := validateEndpoint(endpoint); err != nil {
		return false, err
	}
	return store.remove(owner, endpoint, nil)
}

func (store *SubscriptionStore) remove(owner, endpoint string, expected *Subscription) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.readState()
	if err != nil {
		return false, err
	}
	for index, candidate := range current.Subscriptions {
		if candidate.Owner != owner || candidate.Endpoint != endpoint {
			continue
		}
		if expected != nil && candidate.Subscription != *expected {
			return false, nil
		}
		current.Subscriptions = append(current.Subscriptions[:index], current.Subscriptions[index+1:]...)
		if err := store.writeState(current); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func ValidateSubscription(subscription Subscription) error {
	if err := validateEndpoint(subscription.Endpoint); err != nil {
		return err
	}
	if err := validateSubscriptionKey(subscription.Keys.Auth, 16, "auth"); err != nil {
		return err
	}
	if err := validateSubscriptionKey(subscription.Keys.P256dh, 65, "p256dh"); err != nil {
		return err
	}
	return nil
}

func validateOwner(owner string) error {
	if owner == "" || len(owner) > MaxOwnerBytes || !utf8.ValidString(owner) || strings.TrimSpace(owner) != owner {
		return fmt.Errorf("%w: owner must be a non-empty UTF-8 string of at most %d bytes", ErrInvalidOwner, MaxOwnerBytes)
	}
	return nil
}

func validateEndpoint(endpoint string) error {
	if endpoint == "" || len(endpoint) > MaxEndpointBytes || !utf8.ValidString(endpoint) || strings.TrimSpace(endpoint) != endpoint {
		return fmt.Errorf("%w: endpoint must be a non-empty UTF-8 HTTPS URL of at most %d bytes", ErrInvalidSubscription, MaxEndpointBytes)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%w: endpoint must be an HTTPS URL without credentials or a fragment", ErrInvalidSubscription)
	}
	return nil
}

func validateSubscriptionKey(value string, expectedLength int, name string) error {
	if value == "" || len(value) > MaxEncodedKeyBytes {
		return fmt.Errorf("%w: %s key has an invalid length", ErrInvalidSubscription, name)
	}
	decoded, err := decodeSubscriptionKey(value)
	if err != nil || len(decoded) != expectedLength {
		return fmt.Errorf("%w: %s key must be valid base64 with %d decoded bytes", ErrInvalidSubscription, name, expectedLength)
	}
	if name == "p256dh" {
		x, y := elliptic.Unmarshal(elliptic.P256(), decoded)
		if x == nil || y == nil {
			return fmt.Errorf("%w: p256dh key is not a P-256 public key", ErrInvalidSubscription)
		}
	}
	return nil
}

func decodeSubscriptionKey(value string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		decoded, err := encoding.DecodeString(value)
		if err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64 subscription key")
}

func (store *SubscriptionStore) readState() (subscriptionState, error) {
	contents, exists, err := store.file.Read()
	if err != nil {
		return subscriptionState{}, err
	}
	if !exists {
		return subscriptionState{Subscriptions: []persistedSubscription{}}, nil
	}
	if len(contents) > MaxSubscriptionStateBytes {
		return subscriptionState{}, fmt.Errorf("%w: state exceeds %d bytes", ErrInvalidSubscriptionState, MaxSubscriptionStateBytes)
	}
	var current *subscriptionState
	if err := json.Unmarshal(contents, &current); err != nil || current == nil || current.Subscriptions == nil {
		if err == nil {
			err = errors.New("subscriptions must be an array")
		}
		return subscriptionState{}, fmt.Errorf("%w: %v", ErrInvalidSubscriptionState, err)
	}
	if len(current.Subscriptions) > MaxSubscriptions {
		return subscriptionState{}, fmt.Errorf("%w: state contains more than %d subscriptions", ErrInvalidSubscriptionState, MaxSubscriptions)
	}
	seen := make(map[string]struct{}, len(current.Subscriptions))
	ownerCounts := make(map[string]int)
	for _, candidate := range current.Subscriptions {
		if err := validateOwner(candidate.Owner); err != nil {
			return subscriptionState{}, fmt.Errorf("%w: %v", ErrInvalidSubscriptionState, err)
		}
		if err := ValidateSubscription(candidate.Subscription); err != nil {
			return subscriptionState{}, fmt.Errorf("%w: %v", ErrInvalidSubscriptionState, err)
		}
		key := candidate.Endpoint
		if _, found := seen[key]; found {
			return subscriptionState{}, fmt.Errorf("%w: duplicate endpoint", ErrInvalidSubscriptionState)
		}
		seen[key] = struct{}{}
		ownerCounts[candidate.Owner]++
		if ownerCounts[candidate.Owner] > MaxSubscriptionsPerOwner {
			return subscriptionState{}, fmt.Errorf("%w: owner has more than %d subscriptions", ErrInvalidSubscriptionState, MaxSubscriptionsPerOwner)
		}
	}
	return *current, nil
}

func (store *SubscriptionStore) writeState(current subscriptionState) error {
	contents, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if len(contents) > MaxSubscriptionStateBytes {
		return fmt.Errorf("%w: state exceeds %d bytes", ErrInvalidSubscriptionState, MaxSubscriptionStateBytes)
	}
	return store.file.Write(contents)
}
