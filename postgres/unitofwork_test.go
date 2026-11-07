package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunInTx_ReusesContextTransaction(t *testing.T) {
	t.Parallel()

	expectedTx := fakeDBTX{}
	txm := &poolTransactionManager{}
	ctx := context.WithValue(context.Background(), txContextKey{}, expectedTx)

	err := txm.RunInTx(ctx, func(_ context.Context, tx DBTX) error {
		require.Equal(t, expectedTx, tx)
		return nil
	})

	require.NoError(t, err)
}

func TestRunRead_ReusesContextTransaction(t *testing.T) {
	t.Parallel()

	expectedTx := fakeDBTX{}
	txm := &poolTransactionManager{}
	ctx := context.WithValue(context.Background(), txContextKey{}, expectedTx)

	err := txm.RunRead(ctx, func(_ context.Context, db DBTX) error {
		require.Equal(t, expectedTx, db)
		return nil
	})

	require.NoError(t, err)
}

func TestWithoutTransactionPreservesCallerContext(t *testing.T) {
	t.Parallel()

	type callerContextKey struct{}
	expectedTx := fakeDBTX{}
	ctx := context.WithValue(context.Background(), callerContextKey{}, "trace-value")
	ctx = context.WithValue(ctx, txContextKey{}, expectedTx)

	detached := WithoutTransaction(ctx)

	_, hasTx := txFromContext(detached)
	require.False(t, hasTx)
	require.Equal(t, "trace-value", detached.Value(callerContextKey{}))
}

func TestInTx_ReturnsCallbackValue(t *testing.T) {
	t.Parallel()

	got, err := InTx(context.Background(), stubTransactionManager{}, func(_ context.Context, tx DBTX) (string, error) {
		require.Equal(t, fakeDBTX{}, tx)
		return "created", nil
	})

	require.NoError(t, err)
	require.Equal(t, "created", got)
}

func TestInRead_ReturnsCallbackValue(t *testing.T) {
	t.Parallel()

	got, err := InRead(context.Background(), stubTransactionManager{}, func(_ context.Context, db DBTX) (string, error) {
		require.Equal(t, fakeDBTX{}, db)
		return "found", nil
	})

	require.NoError(t, err)
	require.Equal(t, "found", got)
}

type stubTransactionManager struct{}

func (stubTransactionManager) RunInTx(ctx context.Context, fn func(context.Context, DBTX) error) error {
	return fn(ctx, fakeDBTX{})
}

func (stubTransactionManager) RunRead(ctx context.Context, fn func(context.Context, DBTX) error) error {
	return fn(ctx, fakeDBTX{})
}

func TestConsistentSnapshotCannotSilentlyReuseReadCommittedTransaction(t *testing.T) {
	txm := &poolTransactionManager{}
	ctx := context.WithValue(context.Background(), txContextKey{}, fakeDBTX{})
	called := false
	err := txm.RunInTx(WithConsistentSnapshot(ctx), func(context.Context, DBTX) error { called = true; return nil })
	require.Error(t, err)
	require.False(t, called)
}
