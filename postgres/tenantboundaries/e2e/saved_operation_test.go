package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/tenantfixture"
	"github.com/Braidplane/braidplane-core/postgres/queries"
)

func TestSavedOperationPresenceStaysWithinTenant(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "saved operation readiness")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedEnvironmentBoundary(t, tx, true)
	var org [2]slug.Slug
	for i := range org {
		require.NoError(t, tx.QueryRow(ctx, "SELECT slug FROM organizations WHERE id=$1", f.Org[i]).Scan(&org[i]))
	}
	_, err = tx.Exec(ctx, "INSERT INTO queries(data_source_id,slug,name,data) VALUES ($1,'saved','Saved','{}')", f.Source[1])
	require.NoError(t, err)
	repo := queries.New()
	check := func(tenant, source slug.Slug, want bool) {
		t.Helper()
		exists, err := repo.ExistsOnOtherDataSource(ctx, tx, tenant, source, "saved")
		require.NoError(t, err)
		require.Equal(t, want, exists)
	}
	check(org[0], "other", false)
	check(org[1], "source", false)
	check(org[1], "other", true)
	_, err = tx.Exec(ctx, "UPDATE queries SET deleted_at=now() WHERE data_source_id=$1", f.Source[1])
	require.NoError(t, err)
	check(org[1], "other", false)
}
