package writegate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCancelledOwnerCannotReachDispatchGuard(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	err := BeforeWrite(WithBeforeWrite(ctx, func(context.Context) error { called = true; return nil }))
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called)
}
