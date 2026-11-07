package httpsecurity

import (
	"context"
	"strings"
	"sync"
	"time"
)

// AbuseLimit describes one shared fixed-window budget. Scope must identify the
// operation class; Key must identify the client or tenant without containing a
// credential or other secret.
type AbuseLimit struct {
	Scope  string
	Key    string
	Limit  int
	Window time.Duration
}

// AbuseDecision reports whether the operation may proceed and when a denied
// window resets.
type AbuseDecision struct {
	Allowed bool
	ResetAt time.Time
}

// AbuseLimiter is the storage-independent boundary used by HTTP middleware.
// Production implementations must share state across replicas and restarts.
type AbuseLimiter interface {
	Attempt(context.Context, AbuseLimit) (AbuseDecision, error)
}

// MemoryAbuseLimiter is a deterministic test/local adapter. Production wiring
// uses the Postgres implementation so replacing a process cannot reset limits.
type MemoryAbuseLimiter struct {
	mu      sync.Mutex
	entries map[string]throttleEntry
	now     func() time.Time
}

// NewMemoryAbuseLimiter creates one in-memory limiter.
func NewMemoryAbuseLimiter() *MemoryAbuseLimiter {
	return &MemoryAbuseLimiter{
		entries: make(map[string]throttleEntry),
		now:     time.Now,
	}
}

// Attempt atomically records one request against the supplied operation budget.
func (l *MemoryAbuseLimiter) Attempt(_ context.Context, budget AbuseLimit) (AbuseDecision, error) {
	limit := budget.Limit
	if limit <= 0 {
		limit = 1
	}
	window := budget.Window
	if window <= 0 {
		window = time.Minute
	}
	key := strings.TrimSpace(budget.Scope) + "|" + strings.TrimSpace(budget.Key)
	if key == "|" {
		key = "unknown"
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	entry := l.entries[key]
	if entry.resetAt.IsZero() || !now.Before(entry.resetAt) {
		entry = throttleEntry{count: 1, resetAt: now.Add(window)}
		l.entries[key] = entry
		return AbuseDecision{Allowed: true, ResetAt: entry.resetAt}, nil
	}
	if entry.count >= limit {
		return AbuseDecision{Allowed: false, ResetAt: entry.resetAt}, nil
	}
	entry.count++
	l.entries[key] = entry
	return AbuseDecision{Allowed: true, ResetAt: entry.resetAt}, nil
}
