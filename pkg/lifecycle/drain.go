package lifecycle

import (
	"context"
	"sync/atomic"
	"time"
)

// DrainLifecycle tracks whether a process has started graceful drain.
type DrainLifecycle struct {
	draining atomic.Bool
}

// NewDrainLifecycle creates process-local drain state for readiness checks and shutdown.
func NewDrainLifecycle() *DrainLifecycle {
	return &DrainLifecycle{}
}

// BeginDrain marks the process as draining and reports whether this call changed state.
func (d *DrainLifecycle) BeginDrain() bool {
	if d == nil {
		return false
	}
	return d.draining.CompareAndSwap(false, true)
}

// IsDraining reports whether readiness should reject new traffic for shutdown drain.
func (d *DrainLifecycle) IsDraining() bool {
	if d == nil {
		return false
	}
	return d.draining.Load()
}

// WaitDrainWindow waits for external traffic routing to observe not-ready state.
func (d *DrainLifecycle) WaitDrainWindow(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
