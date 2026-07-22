package access

import (
	"sync"
	"time"
)

const DefaultMaxRateLimitKeys = 10_000

type RateLimiter struct {
	limit         int
	window        time.Duration
	maxKeys       int
	now           func() time.Time
	mu            sync.Mutex
	attempts      map[string][]time.Time
	lastCleanupAt time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return newRateLimiter(limit, window, DefaultMaxRateLimitKeys, time.Now)
}

func newRateLimiter(limit int, window time.Duration, maxKeys int, now func() time.Time) *RateLimiter {
	return &RateLimiter{
		limit: limit, window: window, maxKeys: maxKeys, now: now,
		attempts: make(map[string][]time.Time),
	}
}

func (l *RateLimiter) Allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastCleanupAt) >= l.window {
		l.cleanupExpiredKeys(now)
	}
	attempts, exists := l.attempts[key]
	if !exists && len(l.attempts) >= l.maxKeys {
		return false
	}
	attempts = activeAttempts(attempts, now, l.window)
	if len(attempts) >= l.limit {
		l.attempts[key] = attempts
		return false
	}
	l.attempts[key] = append(attempts, now)
	return true
}

func (l *RateLimiter) cleanupExpiredKeys(now time.Time) {
	for key, attempts := range l.attempts {
		attempts = activeAttempts(attempts, now, l.window)
		if len(attempts) == 0 {
			delete(l.attempts, key)
		} else {
			l.attempts[key] = attempts
		}
	}
	l.lastCleanupAt = now
}

func activeAttempts(attempts []time.Time, now time.Time, window time.Duration) []time.Time {
	active := attempts[:0]
	for _, attempt := range attempts {
		if now.Sub(attempt) < window {
			active = append(active, attempt)
		}
	}
	return active
}
