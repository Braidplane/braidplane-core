// Package writegate lets runtime orchestration guard an adapter's prepared provider write.
package writegate

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ErrSkipped stops provider dispatch when a completed effect is reused.
var ErrSkipped = xerror.New(xerror.KindUnavailable, "CONNECTOR_WRITE_SKIPPED", "connector write skipped")

// ErrPreparationFailed preserves a safe local failure across execution-worker transport.
var ErrPreparationFailed = xerror.New(xerror.KindInvalidInput, "SINK_CONFIGURATION_INVALID", "sink configuration is invalid")

type contextKey struct{}

// HasGuard identifies sink writes that require admission at the provider boundary.
func HasGuard(ctx context.Context) bool {
	_, ok := ctx.Value(contextKey{}).(func(context.Context) error)
	return ok
}

// WithBeforeWrite installs the durable-effect guard for this sink execution.
func WithBeforeWrite(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, contextKey{}, guard)
}

// BeforeWrite runs after local preparation and immediately before provider dispatch.
// Standalone connector executions have no runtime effect guard.
func BeforeWrite(ctx context.Context) error {
	guard, ok := ctx.Value(contextKey{}).(func(context.Context) error)
	if !ok {
		return nil
	}
	return guard(ctx)
}
