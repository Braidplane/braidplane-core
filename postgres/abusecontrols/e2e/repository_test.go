package e2e

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/abusecontrols"
)

func TestRepository_AtomicallySharesAttemptsAcrossInstances(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "shared abuse control repository integration test")
	scope := fmt.Sprintf("test.shared.%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM abuse_control_buckets WHERE scope = $1`, scope)
		require.NoError(t, err)
	})
	budget := httpsecurity.AbuseLimit{
		Scope: scope, Key: "same-subject", Limit: 3, Window: time.Minute,
	}

	const attempts = 12
	var allowed atomic.Int64
	var failed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(repo abusecontrols.Repository) {
			defer wg.Done()
			decision, err := repo.Attempt(ctx, pool, budget)
			if err != nil {
				failed.Add(1)
				return
			}
			if decision.Allowed {
				allowed.Add(1)
			}
		}(abusecontrols.New())
	}
	wg.Wait()

	require.Zero(t, failed.Load())
	require.Equal(t, int64(3), allowed.Load())
	decision, err := abusecontrols.New().Attempt(ctx, pool, budget)
	require.NoError(t, err)
	require.False(t, decision.Allowed)
	require.True(t, decision.ResetAt.After(time.Now()))
}

func TestRepository_LowerWindowRecoversExistingBucket(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "abuse control operator recovery integration test")
	scope := fmt.Sprintf("test.recovery.%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM abuse_control_buckets WHERE scope = $1`, scope)
		require.NoError(t, err)
	})
	repo := abusecontrols.New()
	longWindow := httpsecurity.AbuseLimit{
		Scope: scope, Key: "same-subject", Limit: 1, Window: time.Hour,
	}

	decision, err := repo.Attempt(ctx, pool, longWindow)
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	decision, err = repo.Attempt(ctx, pool, longWindow)
	require.NoError(t, err)
	require.False(t, decision.Allowed)

	_, err = pool.Exec(ctx, `
		UPDATE abuse_control_buckets
		SET window_started_at = clock_timestamp() - INTERVAL '2 seconds'
		WHERE scope = $1
	`, scope)
	require.NoError(t, err)

	decision, err = repo.Attempt(ctx, pool, httpsecurity.AbuseLimit{
		Scope: scope, Key: "same-subject", Limit: 1, Window: time.Second,
	})
	require.NoError(t, err)
	require.True(t, decision.Allowed, "the current shorter window must release an existing bucket")
}
