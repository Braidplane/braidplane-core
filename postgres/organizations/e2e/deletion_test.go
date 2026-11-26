package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
)

func TestOrganizationDeletionChecksWorkAndPreservesOtherTenant(t *testing.T) {
	pool := e2edb.OpenPostgresPool(t, "organization deletion")
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	target := slug.Slug(fmt.Sprintf("synthetic-org-%d", time.Now().UnixNano()))
	other := target + "-other"
	_, err = tx.Exec(t.Context(), `INSERT INTO organizations(slug,name) VALUES ($1,'Synthetic A'),($2,'Synthetic B')`, target, other)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `INSERT INTO river_job(kind,args,queue,max_attempts) VALUES ('synthetic',jsonb_build_object('organization_slug',$1::text),'synthetic',25),('synthetic',jsonb_build_object('organization_slug',$2::text),'synthetic',25)`, target, other)
	require.NoError(t, err)
	repo := organizations.NewRepository()
	blocked, err := repo.DeletionBlocked(t.Context(), tx, target)
	require.NoError(t, err)
	require.True(t, blocked)
	_, err = tx.Exec(t.Context(), `UPDATE river_job SET state='completed',finalized_at=NOW() WHERE args->>'organization_slug'=$1`, target)
	require.NoError(t, err)
	blocked, err = repo.DeletionBlocked(t.Context(), tx, target)
	require.NoError(t, err)
	require.False(t, blocked)
	require.NoError(t, repo.Delete(t.Context(), tx, target))
	var count int
	require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM river_job WHERE args->>'organization_slug'=$1`, target).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM river_job WHERE args->>'organization_slug'=$1`, other).Scan(&count))
	require.Equal(t, 1, count)
	_, err = repo.FindBySlug(t.Context(), tx, other)
	require.NoError(t, err)
}

func TestOrganizationDeletionConflictsWithConcurrentJobCompletion(t *testing.T) {
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "organization deletion")
	target := slug.Slug(fmt.Sprintf("synthetic-race-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `INSERT INTO organizations(slug,name) VALUES ($1,'Synthetic')`, target)
	require.NoError(t, err)
	var jobID int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO river_job(kind,args,queue,max_attempts)
		VALUES ('synthetic',jsonb_build_object('organization_slug',$1::text),'synthetic',25) RETURNING id`, target).Scan(&jobID))

	repo := organizations.NewRepository()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = repo.FindBySlugForUpdate(ctx, tx, target)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE river_job SET state='completed',finalized_at=NOW() WHERE id=$1`, jobID)
	require.NoError(t, err)

	_, err = repo.DeletionBlocked(ctx, tx, target)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "40001", pgErr.Code)
	var conflict *xerror.Error
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, http.StatusConflict, conflict.Kind().HTTPStatus())
	require.NoError(t, tx.Rollback(ctx))

	retry, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = retry.Rollback(ctx) }()
	_, err = repo.FindBySlugForUpdate(ctx, retry, target)
	require.NoError(t, err)
	blocked, err := repo.DeletionBlocked(ctx, retry, target)
	require.NoError(t, err)
	require.False(t, blocked)
	require.NoError(t, repo.Delete(ctx, retry, target))
	require.NoError(t, retry.Commit(ctx))
}
