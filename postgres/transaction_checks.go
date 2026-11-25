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
	if checksFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(WithConsistentSnapshot(ctx), transactionChecksKey{}, &transactionChecks{})
}

func AddTransactionCheck(ctx context.Context, check func(context.Context, DBTX) error) {
	if state := checksFromContext(ctx); state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.checks = append(state.checks, check)
	}
}

func checksFromContext(ctx context.Context) *transactionChecks {
	state, _ := ctx.Value(transactionChecksKey{}).(*transactionChecks)
	return state
}

func (state *transactionChecks) since(offset int) []func(context.Context, DBTX) error {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return slices.Clone(state.checks[offset:])
}

// Each transaction verifies a decision once, including decisions added by nested callers.
type (
	verifiedChecks    map[*transactionChecks]int
	verifiedChecksKey struct{}
)

func (verified verifiedChecks) run(ctx context.Context, db DBTX) error {
	state := checksFromContext(ctx)
	for _, check := range state.since(verified[state]) {
		if err := check(ctx, db); err != nil {
			return err
		}
		verified[state]++
	}
	return nil
}

// WithoutTransactionChecks reads authorization facts without replaying other decisions
// from the request. The caller's database transaction, if any, is preserved.
func WithoutTransactionChecks(ctx context.Context) context.Context {
	return context.WithValue(ctx, transactionChecksKey{}, (*transactionChecks)(nil))
}
