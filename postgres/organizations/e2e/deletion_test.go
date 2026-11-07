package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/stretchr/testify/require"
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
