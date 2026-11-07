// Package abusecontrols persists shared HTTP abuse-control windows.
package abusecontrols

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	internalpg "github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// Repository records fixed-window attempts in shared Postgres state.
type Repository interface {
	Attempt(context.Context, postgres.DBTX, httpsecurity.AbuseLimit) (httpsecurity.AbuseDecision, error)
}

type repository struct{}

// New creates a SQLC-backed shared abuse-control repository.
func New() Repository {
	return &repository{}
}

// Attempt stores only a one-way subject hash. The database clock owns the
// window boundary so replica clock skew cannot create extra allowance.
func (r *repository) Attempt(
	ctx context.Context,
	db postgres.DBTX,
	budget httpsecurity.AbuseLimit,
) (httpsecurity.AbuseDecision, error) {
	limit := budget.Limit
	if limit <= 0 {
		limit = 1
	}
	window := budget.Window
	if window <= 0 {
		window = time.Minute
	}
	keyHash := sha256.Sum256([]byte(budget.Key))

	row, err := internalpg.New(db).AttemptAbuseControl(ctx, internalpg.AttemptAbuseControlParams{
		WindowSeconds: pgtype.Float8{Float64: window.Seconds(), Valid: true},
		Scope:         pgtype.Text{String: budget.Scope, Valid: true},
		SubjectHash:   keyHash[:],
		AttemptLimit:  pgtype.Int4{Int32: int32(limit), Valid: true},
	})
	if err != nil {
		return httpsecurity.AbuseDecision{}, xerror.Unexpected().With(err)
	}
	if !row.ResetAt.Valid {
		return httpsecurity.AbuseDecision{}, xerror.Unexpected().Detail("abuse-control reset time is required")
	}
	return httpsecurity.AbuseDecision{Allowed: row.Allowed, ResetAt: row.ResetAt.Time}, nil
}
