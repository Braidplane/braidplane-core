package pipelinesvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ErrRetryElapsed marks a retry refused before runtime starts.
var ErrRetryElapsed = xerror.New(xerror.KindInvalidInput, "PIPELINE_RETRY_ELAPSED", "retry elapsed-time cap exceeded")

type executionStartDeadlineKey struct{}

type executionStartDeadline struct {
	at    time.Time
	clock clock.Clock
}

// WithExecutionStartDeadline limits when runtime may start, including admission wait.
func WithExecutionStartDeadline(ctx context.Context, at time.Time, clk clock.Clock) context.Context {
	return context.WithValue(ctx, executionStartDeadlineKey{}, executionStartDeadline{at: at, clock: clk})
}

// CheckExecutionStartDeadline permits starting exactly at the captured deadline.
func CheckExecutionStartDeadline(ctx context.Context) error {
	deadline, ok := ctx.Value(executionStartDeadlineKey{}).(executionStartDeadline)
	if ok && deadline.clock.Now().After(deadline.at) {
		return ErrRetryElapsed
	}
	return nil
}
