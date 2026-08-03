package push

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

const (
	DefaultWebPushTTL   = 24 * 60 * 60
	MaxDeliveryAttempts = 3
	retryBaseDelay      = 100 * time.Millisecond
)

type DeliveryResult struct {
	StatusCode int
}

type Delivery interface {
	Deliver(context.Context, Subscription, []byte) (DeliveryResult, error)
}

type WebPushDelivery struct {
	identity   *VAPIDIdentity
	subscriber string
	client     HTTPClient
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

func NewWebPushDelivery(identity *VAPIDIdentity, subscriber string, client HTTPClient) *WebPushDelivery {
	return &WebPushDelivery{identity: identity, subscriber: subscriber, client: client}
}

func (delivery *WebPushDelivery) Deliver(ctx context.Context, subscription Subscription, payload []byte) (DeliveryResult, error) {
	if delivery == nil || delivery.identity == nil {
		return DeliveryResult{}, errors.New("Web Push delivery has no VAPID identity")
	}
	if delivery.subscriber == "" {
		return DeliveryResult{}, errors.New("Web Push delivery has no VAPID subscriber")
	}
	if err := ValidateSubscription(subscription); err != nil {
		return DeliveryResult{}, err
	}
	keys, err := delivery.identity.Keys()
	if err != nil {
		return DeliveryResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	response, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: subscription.Endpoint,
		Keys: webpush.Keys{
			Auth:   subscription.Keys.Auth,
			P256dh: subscription.Keys.P256dh,
		},
	}, &webpush.Options{
		HTTPClient:      delivery.client,
		Subscriber:      delivery.subscriber,
		TTL:             DefaultWebPushTTL,
		VAPIDPublicKey:  keys.PublicKey,
		VAPIDPrivateKey: keys.PrivateKey,
	})
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return DeliveryResult{}, ctx.Err()
		}
		return DeliveryResult{}, errors.New("Web Push request failed")
	}
	if response == nil {
		return DeliveryResult{}, errors.New("Web Push delivery returned no response")
	}
	return DeliveryResult{StatusCode: response.StatusCode}, nil
}

type Notifier struct {
	store       *SubscriptionStore
	delivery    Delivery
	maxAttempts int
	sleep       func(context.Context, time.Duration) error
}

func NewNotifier(store *SubscriptionStore, delivery Delivery) *Notifier {
	return &Notifier{
		store:       store,
		delivery:    delivery,
		maxAttempts: MaxDeliveryAttempts,
		sleep:       waitForRetry,
	}
}

func (notifier *Notifier) Deliver(ctx context.Context, owner string, payload []byte) error {
	if notifier == nil || notifier.store == nil {
		return errors.New("push notifier has no subscription store")
	}
	if notifier.delivery == nil {
		return errors.New("push notifier has no delivery")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	subscriptions, err := notifier.store.List(owner)
	if err != nil {
		return err
	}
	attempts := notifier.maxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var failures []error
	for _, subscription := range subscriptions {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		result, err := notifier.deliverOne(ctx, subscription, payload, attempts)
		if isStaleStatus(result.StatusCode) {
			if _, removeErr := notifier.store.remove(owner, subscription.Endpoint, &subscription); removeErr != nil {
				failures = append(failures, fmt.Errorf("remove stale push subscription: %w", removeErr))
			}
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("deliver push subscription: %w", err))
		}
	}
	return errors.Join(failures...)
}

func (notifier *Notifier) deliverOne(ctx context.Context, subscription Subscription, payload []byte, attempts int) (DeliveryResult, error) {
	var lastResult DeliveryResult
	var lastError error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return lastResult, err
		}
		result, err := notifier.delivery.Deliver(ctx, subscription, payload)
		lastResult = result
		if isStaleStatus(result.StatusCode) {
			return result, nil
		}
		if err == nil && isSuccessfulStatus(result.StatusCode) {
			return result, nil
		}
		if err != nil && ctx.Err() != nil {
			return result, ctx.Err()
		}
		if err == nil {
			lastError = fmt.Errorf("delivery returned HTTP status %d", result.StatusCode)
			if !isTransientStatus(result.StatusCode) {
				return result, lastError
			}
		} else {
			lastError = err
			if result.StatusCode != 0 && !isTransientStatus(result.StatusCode) {
				return result, err
			}
		}
		if attempt == attempts {
			break
		}
		if err := notifier.sleep(ctx, retryDelay(attempt)); err != nil {
			return lastResult, err
		}
	}
	return lastResult, fmt.Errorf("delivery failed after %d attempts: %w", attempts, lastError)
}

func isSuccessfulStatus(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

func isStaleStatus(status int) bool {
	return status == http.StatusNotFound || status == http.StatusGone
}

func isTransientStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests || (status >= http.StatusInternalServerError && status < 600)
}

func retryDelay(attempt int) time.Duration {
	return retryBaseDelay * time.Duration(1<<max(attempt-1, 0))
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
