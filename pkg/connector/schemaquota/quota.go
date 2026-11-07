// Package schemaquota carries execution-scoped schema introspection budgets to
// provider clients and adapters. Built-in connectors charge the quota before
// appending provider metadata to an in-memory result.
package schemaquota

import (
	"context"
	"sync"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type contextKey struct{}

// Quota is a concurrency-safe work and allocation budget for one introspection.
type Quota struct {
	mu       sync.Mutex
	items    int
	bytes    int
	maxItems int
	maxBytes int
}

// Attached reports whether ctx carries an execution-scoped schema quota.
func Attached(ctx context.Context) bool {
	quota, _ := ctx.Value(contextKey{}).(*Quota)
	return quota != nil
}

// WithQuota attaches a fresh quota to ctx. Invalid limits fail closed when the
// first provider item is charged.
func WithQuota(ctx context.Context, maxItems, maxBytes int) context.Context {
	return context.WithValue(ctx, contextKey{}, &Quota{maxItems: maxItems, maxBytes: maxBytes})
}

// Charge reserves result work before the caller retains provider metadata.
func Charge(ctx context.Context, items, bytes int) error {
	quota, _ := ctx.Value(contextKey{}).(*Quota)
	if quota == nil {
		return nil
	}
	if items < 0 || bytes < 0 {
		return limitError("schema introspection reported an invalid allocation charge")
	}

	quota.mu.Lock()
	defer quota.mu.Unlock()
	if quota.maxItems <= 0 || items > quota.maxItems-quota.items {
		return limitError("schema result exceeds configured item limit")
	}
	if quota.maxBytes <= 0 || bytes > quota.maxBytes-quota.bytes {
		return limitError("schema result exceeds configured byte limit")
	}
	quota.items += items
	quota.bytes += bytes
	return nil
}

// RemainingBytes returns the unreserved byte budget, capped by fallback when
// fallback is positive. It lets HTTP provider clients bound response reads
// before JSON decoding allocates provider objects.
func RemainingBytes(ctx context.Context, fallback int) int {
	quota, _ := ctx.Value(contextKey{}).(*Quota)
	if quota == nil {
		return fallback
	}
	quota.mu.Lock()
	defer quota.mu.Unlock()
	remaining := quota.maxBytes - quota.bytes
	if fallback > 0 && fallback < remaining {
		return fallback
	}
	return remaining
}

func limitError(detail string) error {
	return xerror.ExecutionUnavailable().Detail(detail)
}
