package httpsecurity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const defaultThrottleMaxEntries = 10_000

type Throttle struct {
	mu         sync.Mutex
	max        int
	maxEntries int
	window     time.Duration
	lastPrune  time.Time
	entries    map[string]throttleEntry
}

type throttleEntry struct {
	count   int
	resetAt time.Time
}

// NewThrottle creates a process-local fixed-window throttle. It is deliberately
// in-memory: reverse proxy limits remain the distributed outer layer.
func NewThrottle(limit int, window time.Duration) *Throttle {
	if limit <= 0 {
		limit = 5
	}
	if window <= 0 {
		window = time.Minute
	}
	return &Throttle{
		max:        limit,
		maxEntries: defaultThrottleMaxEntries,
		window:     window,
		entries:    make(map[string]throttleEntry),
	}
}

// Allow records one attempt for key and reports whether it remains under the
// current fixed-window limit.
func (t *Throttle) Allow(key string, now time.Time) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "unknown"
	}
	if now.IsZero() {
		now = time.Now()
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.shouldPruneLocked(now) {
		t.pruneExpiredLocked(now)
	}

	entry := t.entries[key]
	if entry.resetAt.IsZero() || !now.Before(entry.resetAt) {
		if len(t.entries) >= t.maxEntries {
			t.pruneExpiredLocked(now)
			if len(t.entries) >= t.maxEntries {
				return false
			}
		}
		t.entries[key] = throttleEntry{count: 1, resetAt: now.Add(t.window)}
		return true
	}

	if entry.count >= t.max {
		return false
	}

	entry.count++
	t.entries[key] = entry
	return true
}

func (t *Throttle) shouldPruneLocked(now time.Time) bool {
	return t.lastPrune.IsZero() || !now.Before(t.lastPrune.Add(t.window))
}

func (t *Throttle) pruneExpiredLocked(now time.Time) {
	for key, entry := range t.entries {
		if entry.resetAt.IsZero() || !now.Before(entry.resetAt) {
			delete(t.entries, key)
		}
	}
	t.lastPrune = now
}

// NormalizeEmail canonicalizes email input for throttle keys without changing
// the value passed to downstream validation or auth services.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// HashKey returns a short stable hash for audit logs so raw emails and full
// throttle keys are never logged.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}
