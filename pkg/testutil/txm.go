package testutil

import (
	"context"

	"github.com/Braidplane/braidplane-core/postgres"
)

// FakeTransactionManager is a test double for postgres.TransactionManager.
// It executes the callback directly without opening a real transaction.
type FakeTransactionManager struct {
	Tx postgres.DBTX
}

func (m FakeTransactionManager) RunInTx(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	return fn(ctx, m.Tx)
}

func (m FakeTransactionManager) RunRead(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	return fn(ctx, m.Tx)
}
