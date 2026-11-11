package auth

import (
	"sync"
	"time"
)

// attemptLimiter remains a small deterministic unit-test helper. Runtime
// authentication limits are enforced by Repository.AllowRateLimit in PostgreSQL.
type attemptLimiter struct {
	mu      sync.Mutex
	entries map[string][]time.Time
	maxKeys int
}

func newAttemptLimiter(maxKeys int) *attemptLimiter {
	return &attemptLimiter{entries: make(map[string][]time.Time), maxKeys: maxKeys}
}

func (l *attemptLimiter) allow(key string, now time.Time, maxAttempts int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-window)
	attempts := l.entries[key][:0]
	for _, attempt := range l.entries[key] {
		if attempt.After(cutoff) {
			attempts = append(attempts, attempt)
		}
	}
	if len(attempts) >= maxAttempts {
		l.entries[key] = attempts
		return false
	}
	if _, exists := l.entries[key]; !exists && len(l.entries) >= l.maxKeys {
		for existingKey, existingAttempts := range l.entries {
			if len(existingAttempts) == 0 || !existingAttempts[len(existingAttempts)-1].After(cutoff) {
				delete(l.entries, existingKey)
			}
		}
		if len(l.entries) >= l.maxKeys {
			return false
		}
	}
	l.entries[key] = append(attempts, now)
	return true
}
