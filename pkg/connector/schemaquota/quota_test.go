package schemaquota

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChargeRejectsBeforeCallerRetainsNextItem(t *testing.T) {
	t.Parallel()

	ctx := WithQuota(context.Background(), 2, 32)
	retained := make([]string, 0, 2)
	for _, candidate := range []string{"one", "two", "three"} {
		err := Charge(ctx, 1, len(candidate))
		if err != nil {
			require.ErrorContains(t, err, "item limit")
			break
		}
		retained = append(retained, candidate)
	}
	require.Equal(t, []string{"one", "two"}, retained)
}

func TestChargeRejectsByteBudgetBeforeAppend(t *testing.T) {
	t.Parallel()

	ctx := WithQuota(context.Background(), 10, 5)
	require.NoError(t, Charge(ctx, 1, 5))
	require.ErrorContains(t, Charge(ctx, 1, 1), "byte limit")
	require.Zero(t, RemainingBytes(ctx, 1024))
}
