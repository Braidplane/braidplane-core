package testutil

import "context"

type FakeSessionLocker struct {
	WithLockFn func(context.Context, string, func(context.Context) error) (bool, error)
}

func (f *FakeSessionLocker) WithLock(ctx context.Context, key string, execute func(context.Context) error) (bool, error) {
	if f.WithLockFn != nil {
		return f.WithLockFn(ctx, key, execute)
	}
	return true, execute(ctx)
}
