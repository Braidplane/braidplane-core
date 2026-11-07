package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestAsPgxTx_NilTxReturnsError(t *testing.T) {
	t.Parallel()

	tx, err := AsPgxTx(nil)
	require.Nil(t, tx)
	require.ErrorContains(t, err, "postgres transaction is required")
}

func TestAsPgxTx_NonPgxTxReturnsError(t *testing.T) {
	t.Parallel()

	tx, err := AsPgxTx(fakeDBTX{})
	require.Nil(t, tx)
	require.ErrorContains(t, err, "does not implement pgx.Tx")
}

func TestAsPgxTx_ReturnsConcretePgxTx(t *testing.T) {
	t.Parallel()

	expected := fakePgxTx{}

	tx, err := AsPgxTx(expected)
	require.NoError(t, err)
	require.Equal(t, expected, tx)
}

type fakeDBTX struct{}

func (fakeDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (fakeDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, xerror.Unexpected().Detail("not implemented")
}

func (fakeDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

type fakePgxTx struct{}

func (fakePgxTx) Begin(context.Context) (pgx.Tx, error) {
	return fakePgxTx{}, nil
}

func (fakePgxTx) Commit(context.Context) error {
	return nil
}

func (fakePgxTx) Rollback(context.Context) error {
	return nil
}

func (fakePgxTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, nil
}

func (fakePgxTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	return nil
}

func (fakePgxTx) LargeObjects() pgx.LargeObjects {
	return pgx.LargeObjects{}
}

func (fakePgxTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, xerror.Unexpected().Detail("not implemented")
}

func (fakePgxTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (fakePgxTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, xerror.Unexpected().Detail("not implemented")
}

func (fakePgxTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

func (fakePgxTx) Conn() *pgx.Conn {
	return nil
}
