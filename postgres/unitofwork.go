package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// TransactionManager abstracts database unit-of-work access. Services depend
// on this interface instead of *pgxpool.Pool directly, making them testable
// without a real database connection.
type TransactionManager interface {
	// RunInTx executes fn inside a database transaction.
	// If fn returns an error the transaction is rolled back; otherwise it is committed.
	// A panic rolls back the owned transaction and propagates to the caller.
	RunInTx(ctx context.Context, fn func(ctx context.Context, tx DBTX) error) error

	// RunRead reuses the caller's transaction. When a request carries authorization
	// checks, it opens a transaction to verify them in the same snapshot as the read.
	RunRead(ctx context.Context, fn func(ctx context.Context, db DBTX) error) error
}

type (
	txContextKey           struct{}
	snapshotContextKey     struct{}
	transactionSnapshotKey struct{}
)

// WithConsistentSnapshot requests one database view for a multi-query plan.
// Callers must select this before opening their outer unit of work.
func WithConsistentSnapshot(ctx context.Context) context.Context {
	return context.WithValue(ctx, snapshotContextKey{}, true)
}

type transactionlessContext struct {
	context.Context
}

func (c transactionlessContext) Value(key any) any {
	if _, ok := key.(txContextKey); ok {
		return nil
	}
	return c.Context.Value(key)
}

type poolTransactionManager struct {
	pool *pgxpool.Pool
}

// WithoutTransaction preserves caller context values while ensuring database
// work starts outside any active service unit of work.
func WithoutTransaction(ctx context.Context) context.Context {
	return transactionlessContext{Context: ctx}
}

// NewTransactionManager creates a TransactionManager backed by a pgxpool.Pool.
func NewTransactionManager(pool *pgxpool.Pool) TransactionManager {
	return &poolTransactionManager{pool: pool}
}

func (p *poolTransactionManager) RunInTx(
	ctx context.Context,
	fn func(ctx context.Context, tx DBTX) error,
) error {
	if tx, ok := txFromContext(ctx); ok {
		requested, _ := ctx.Value(snapshotContextKey{}).(bool)
		active, _ := ctx.Value(transactionSnapshotKey{}).(bool)
		if requested && !active {
			return xerror.Unexpected().Detail("consistent snapshot must be selected before the outer transaction")
		}
		return fn(ctx, tx)
	}

	checks := pendingTransactionChecks(ctx)
	options := pgx.TxOptions{}
	if snapshot, _ := ctx.Value(snapshotContextKey{}).(bool); snapshot || len(checks) > 0 {
		options.IsoLevel = pgx.RepeatableRead
	}
	tx, err := p.pool.BeginTx(ctx, options)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	rollback := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return tx.Rollback(cleanupCtx)
	}
	// Rollback is safe after commit and also releases the connection during a panic.
	defer func() { _ = rollback() }()

	txCtx := context.WithValue(ctx, txContextKey{}, tx)
	txCtx = context.WithValue(txCtx, transactionSnapshotKey{}, options.IsoLevel == pgx.RepeatableRead)

	// Pin authorized identities in the very snapshot used for the read or write.
	for _, check := range checks {
		if err := check(txCtx, tx); err != nil {
			return err
		}
	}
	err = fn(txCtx, tx)
	if err != nil {
		rlErr := rollback()
		if rlErr != nil {
			return xerror.Join(err, xerror.Unexpected().With(rlErr))
		}

		return xerror.Wrapf("unit of work: %w", err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

func (p *poolTransactionManager) RunRead(
	ctx context.Context,
	fn func(ctx context.Context, db DBTX) error,
) error {
	if _, ok := txFromContext(ctx); ok {
		return p.RunInTx(ctx, fn)
	}
	if p == nil || p.pool == nil {
		return xerror.Unexpected().Detail("postgres read handle is required")
	}

	if len(pendingTransactionChecks(ctx)) > 0 {
		return p.RunInTx(ctx, fn)
	}
	return fn(ctx, p.pool)
}

// InTx executes fn in a transaction and returns the function result without
// forcing callers to manage an outer return variable.
func InTx[T any](
	ctx context.Context,
	txm TransactionManager,
	fn func(ctx context.Context, tx DBTX) (T, error),
) (T, error) {
	var ret T

	err := txm.RunInTx(ctx, func(ctx context.Context, tx DBTX) error {
		var err error
		ret, err = fn(ctx, tx)
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}

	return ret, nil
}

// InRead returns the result of fn using the manager's read handle.
func InRead[T any](
	ctx context.Context,
	txm TransactionManager,
	fn func(ctx context.Context, db DBTX) (T, error),
) (T, error) {
	var ret T

	err := txm.RunRead(ctx, func(ctx context.Context, db DBTX) error {
		var err error
		ret, err = fn(ctx, db)
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}

	return ret, nil
}

func txFromContext(ctx context.Context) (DBTX, bool) {
	if ctx == nil {
		return nil, false
	}

	tx, ok := ctx.Value(txContextKey{}).(DBTX)
	return tx, ok && tx != nil
}
