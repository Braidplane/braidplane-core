package httpsecurity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestThrottle_PrunesExpiredEntries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	throttle := &Throttle{
		max:        1,
		maxEntries: 2,
		window:     time.Minute,
		lastPrune:  now.Add(-2 * time.Minute),
		entries: map[string]throttleEntry{
			"expired": {count: 1, resetAt: now.Add(-time.Second)},
			"active":  {count: 1, resetAt: now.Add(time.Minute)},
		},
	}

	assert.True(t, throttle.Allow("new", now))
	assert.NotContains(t, throttle.entries, "expired")
	assert.Contains(t, throttle.entries, "active")
	assert.Contains(t, throttle.entries, "new")
}

func TestThrottle_DeniesNewKeysWhenCapacityRemainsFull(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	throttle := &Throttle{
		max:        1,
		maxEntries: 2,
		window:     time.Minute,
		entries: map[string]throttleEntry{
			"active-1": {count: 1, resetAt: now.Add(time.Minute)},
			"active-2": {count: 1, resetAt: now.Add(time.Minute)},
		},
	}

	assert.False(t, throttle.Allow("new", now))
	assert.Len(t, throttle.entries, 2)
}
