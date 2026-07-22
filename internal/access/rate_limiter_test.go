package access

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimiterLimitsEachKeyWithinTheWindow(t *testing.T) {
	now := time.Unix(0, 0)
	limiter := newRateLimiter(2, time.Minute, DefaultMaxRateLimitKeys, func() time.Time { return now })

	if !limiter.Allow("client") || !limiter.Allow("client") {
		t.Fatal("initial attempts were rejected")
	}
	if limiter.Allow("client") {
		t.Fatal("attempt beyond the limit was allowed")
	}
	if !limiter.Allow("other") {
		t.Fatal("independent key was rejected")
	}

	now = now.Add(time.Minute)
	if !limiter.Allow("client") {
		t.Fatal("attempt was not allowed when the window expired")
	}
}

func TestRateLimiterReleasesExpiredKeysWhenCapacityIsFull(t *testing.T) {
	now := time.Unix(0, 0)
	limiter := newRateLimiter(1, 10*time.Second, 2, func() time.Time { return now })

	if !limiter.Allow("one") || !limiter.Allow("two") {
		t.Fatal("initial keys were rejected")
	}
	if limiter.Allow("three") {
		t.Fatal("key beyond capacity was allowed")
	}

	now = now.Add(10 * time.Second)
	if !limiter.Allow("three") {
		t.Fatal("expired keys did not release capacity")
	}
}

func TestRateLimiterAllowsOnlyTheLimitUnderConcurrency(t *testing.T) {
	limiter := NewRateLimiter(5, time.Minute)
	const workers = 50
	start := make(chan struct{})
	var allowed atomic.Int64
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			if limiter.Allow("client") {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()

	if allowed.Load() != 5 {
		t.Fatalf("allowed = %d", allowed.Load())
	}
}
