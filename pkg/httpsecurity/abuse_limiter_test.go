package httpsecurity

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMemoryAbuseLimiterRecoversAfterWindowWithoutResettingSharedState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	limiter := NewMemoryAbuseLimiter()
	limiter.now = func() time.Time { return now }
	budget := AbuseLimit{Scope: "auth.subject", Key: "stable-hash-input", Limit: 1, Window: time.Minute}

	decision, err := limiter.Attempt(context.Background(), budget)
	require.NoError(t, err)
	require.True(t, decision.Allowed)

	decision, err = limiter.Attempt(context.Background(), budget)
	require.NoError(t, err)
	require.False(t, decision.Allowed)
	require.Equal(t, now.Add(time.Minute), decision.ResetAt)

	now = now.Add(time.Minute)
	decision, err = limiter.Attempt(context.Background(), budget)
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Equal(t, now.Add(time.Minute), decision.ResetAt)
}
