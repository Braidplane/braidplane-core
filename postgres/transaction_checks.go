package postgres

import (
	"context"
	"slices"
	"sync"
)

// A request can admit more than one target before a business operation starts.
type transactionChecks struct {
	mu     sync.Mutex
	checks []func(context.Context, DBTX) error
}
type transactionChecksKey struct{}

func WithTransactionChecks(ctx context.Context) context.Context {
	if state, _ := ctx.Value(transactionChecksKey{}).(*transactionChecks); state != nil {
		return ctx
	}
	return context.WithValue(WithConsistentSnapshot(ctx), transactionChecksKey{}, &transactionChecks{})
}

func AddTransactionCheck(ctx context.Context, check func(context.Context, DBTX) error) {
	state, _ := ctx.Value(transactionChecksKey{}).(*transactionChecks)
	if state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.checks = append(state.checks, check)
	}
}

func pendingTransactionChecks(ctx context.Context) []func(context.Context, DBTX) error {
	state, _ := ctx.Value(transactionChecksKey{}).(*transactionChecks)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return slices.Clone(state.checks)
}

// WithoutTransactionChecks reads authorization facts without replaying other decisions
// from the request. The caller's database transaction, if any, is preserved.
func WithoutTransactionChecks(ctx context.Context) context.Context {
	return context.WithValue(ctx, transactionChecksKey{}, (*transactionChecks)(nil))
}
