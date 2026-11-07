package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
)

func TestTransactionManagerCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	callbackErr := xerror.Unexpected().Detail("callback failed")
	panicValue := &struct{ message string }{"callback panicked"}
	for _, tc := range []struct {
		name   string
		cancel bool
		panics bool
		err    error
	}{
		{name: "commit"},
		{name: "returned error", err: callbackErr},
		{name: "panic", panics: true},
		{name: "cancelled error", cancel: true, err: callbackErr},
		{name: "cancelled panic", cancel: true, panics: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := transactionTestPool(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			txm := postgres.NewTransactionManager(pool)
			run := func() {
				err := txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
					tx, err := postgres.AsPgxTx(db)
					require.NoError(t, err)
					// Keep a failing regression from hanging pool.Close.
					t.Cleanup(func() {
						cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
						defer cleanupCancel()
						_ = tx.Rollback(cleanupCtx)
					})
					_, err = db.Exec(ctx, "INSERT INTO transaction_cleanup VALUES (1)")
					require.NoError(t, err)
					if tc.cancel {
						cancel()
					}
					if tc.panics {
						panic(panicValue)
					}
					return tc.err
				})
				if tc.err != nil {
					require.ErrorIs(t, err, tc.err)
				} else {
					require.NoError(t, err)
				}
			}
			if tc.panics {
				require.PanicsWithValue(t, panicValue, run)
			} else {
				run()
			}
			require.Zero(t, pool.Stat().AcquiredConns())
			checkCtx, checkCancel := context.WithTimeout(t.Context(), time.Second)
			defer checkCancel()
			conn, err := pool.Acquire(checkCtx)
			require.NoError(t, err)
			defer conn.Release()
			var count int
			require.NoError(t, conn.QueryRow(checkCtx, "SELECT count(*) FROM transaction_cleanup").Scan(&count))
			if tc.err == nil && !tc.panics {
				require.Equal(t, 1, count)
			} else {
				require.Zero(t, count)
			}
		})
	}
}

func TestNestedTransactionPanicLeavesCleanupToOwner(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	pool := transactionTestPool(t)
	txm := postgres.NewTransactionManager(pool)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := txm.RunInTx(ctx, func(ctx context.Context, outer postgres.DBTX) error {
		require.PanicsWithValue(t, "nested panic", func() {
			_ = txm.RunInTx(ctx, func(ctx context.Context, inner postgres.DBTX) error {
				require.Same(t, outer, inner)
				_, err := inner.Exec(ctx, "INSERT INTO transaction_cleanup VALUES (1)")
				require.NoError(t, err)
				panic("nested panic")
			})
		})
		require.EqualValues(t, 1, pool.Stat().AcquiredConns())
		_, err := outer.Exec(ctx, "INSERT INTO transaction_cleanup VALUES (2)")
		return err
	})
	require.NoError(t, err)
	require.Zero(t, pool.Stat().AcquiredConns())
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM transaction_cleanup").Scan(&count))
	require.Equal(t, 2, count)
}

func transactionTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(e2edb.PostgresConfigFromEnv().DatabaseURL)
	require.NoError(t, err)
	config.MaxConns = 1
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, "CREATE TEMP TABLE transaction_cleanup (value integer)")
	require.NoError(t, err)
	return pool
}
