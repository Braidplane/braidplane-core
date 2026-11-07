package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// AsPgxTx unwraps a DBTX into the concrete pgx transaction type required by
// libraries that integrate directly with pgx transaction semantics.
func AsPgxTx(tx DBTX) (pgx.Tx, error) {
	if tx == nil {
		return nil, xerror.Unexpected().Detail("postgres transaction is required")
	}

	pgxTx, ok := tx.(pgx.Tx)
	if !ok {
		return nil, xerror.Unexpected().Detail("postgres transaction does not implement pgx.Tx")
	}

	return pgxTx, nil
}
