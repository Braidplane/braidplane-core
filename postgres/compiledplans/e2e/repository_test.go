package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
)

func TestRepository_PersistsImmutablePlansByDigest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-Postgres compiled plan repository test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "compiled plan repository integration test")
	orgA := slug.Slug(fmt.Sprintf("compiled-plans-a-%d", time.Now().UnixNano()))
	orgB := slug.Slug(fmt.Sprintf("compiled-plans-b-%d", time.Now().UnixNano()))
	seedProject(t, ctx, pool, orgA)
	seedProject(t, ctx, pool, orgB)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = ANY($1)`, []string{orgA.String(), orgB.String()})
		require.NoError(t, err)
	})

	repo := compiledplans.NewRepository()
	firstMaterialization := []byte(`{"amount":1e+00,"contracts":["one"]}`)
	first, err := repo.Create(ctx, pool, orgA, "data", compiledplans.CreateParams{
		PipelineSlug: "acme", Digest: "sha256:first", AuthoredSpecHash: "sha256:authored",
		FormatVersion: 1, Materialization: firstMaterialization,
	})
	require.NoError(t, err)
	require.Equal(t, firstMaterialization, first.Materialization)

	_, err = repo.Create(ctx, pool, orgA, "data", compiledplans.CreateParams{
		PipelineSlug: "acme", Digest: "sha256:second", AuthoredSpecHash: "sha256:authored",
		FormatVersion: 1, Materialization: []byte(`{"amount":1,"contracts":["two"]}`),
	})
	require.NoError(t, err)

	persisted, err := repo.Find(ctx, pool, orgA, "data", "acme", "sha256:first")
	require.NoError(t, err)
	require.Equal(t, first, persisted)

	_, err = repo.Create(ctx, pool, orgA, "data", compiledplans.CreateParams{
		PipelineSlug: "acme", Digest: "sha256:first", AuthoredSpecHash: "sha256:changed",
		FormatVersion: 2, Materialization: []byte(`changed`),
	})
	require.ErrorIs(t, err, compiledplans.ErrNotFound)
	persisted, err = repo.Find(ctx, pool, orgA, "data", "acme", "sha256:first")
	require.NoError(t, err)
	require.Equal(t, first, persisted)

	_, err = repo.Find(ctx, pool, orgB, "data", "acme", "sha256:first")
	require.ErrorIs(t, err, compiledplans.ErrNotFound)
}

func seedProject(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org slug.Slug) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		WITH organization AS (
			INSERT INTO organizations (slug, name) VALUES ($1, $1) RETURNING id
		)
		INSERT INTO projects (organization_id, slug, name)
		SELECT id, 'data', 'Data' FROM organization
	`, org)
	require.NoError(t, err)
}
