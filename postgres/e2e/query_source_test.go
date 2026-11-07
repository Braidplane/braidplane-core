package e2e

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/queries"
)

func TestSavedQueryRepositoryPreservesScopedSourceIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	pool := e2edb.OpenPostgresPool(t, "saved query source identity")
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	_, err = tx.Exec(ctx, `
INSERT INTO organizations (name,slug) VALUES ('Query scope A','query-scope-a'),('Query scope B','query-scope-b');
INSERT INTO credentials (organization_id,slug,kind,status)
SELECT id,'workspace-grant','oauth2_grant','active' FROM organizations WHERE slug IN ('query-scope-a','query-scope-b');
INSERT INTO data_sources (organization_id,credential_id,name,slug,type,config,is_active)
SELECT c.organization_id,c.id,s.slug,s.slug,'google_workspace','{}',true
FROM credentials c JOIN organizations o ON o.id=c.organization_id
CROSS JOIN (VALUES ('workspace'),('other-workspace')) s(slug)
WHERE o.slug IN ('query-scope-a','query-scope-b');`)
	require.NoError(t, err)
	repo := queries.New()
	data := json.RawMessage(`{"statement":"first"}`)
	created, _, err := repo.Create(ctx, tx, "query-scope-a", "workspace", &queries.CreateParams{Slug: "saved", Name: "Saved", Data: data})
	require.NoError(t, err)
	require.EqualValues(t, "workspace", created.DataSourceSlug)
	loaded, _, err := repo.FindBySlug(ctx, tx, "query-scope-a", "workspace", "saved")
	require.NoError(t, err)
	require.Equal(t, created.DataSourceSlug, loaded.DataSourceSlug)
	require.JSONEq(t, string(data), string(loaded.Data))
	listed, err := repo.ListByDataSource(ctx, tx, "query-scope-a", "workspace", model.ListRequest{Limit: 10, SortBy: "created_at", Order: "asc"}, false)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, loaded.DataSourceSlug, listed[0].DataSourceSlug)
	updated, _, err := repo.Update(ctx, tx, "query-scope-a", "workspace", "saved", &queries.UpdateParams{Name: "Updated", Data: json.RawMessage(`{"statement":"updated"}`)})
	require.NoError(t, err)
	require.Equal(t, loaded.DataSourceSlug, updated.DataSourceSlug)
	for _, scope := range [][2]slug.Slug{{"query-scope-a", "other-workspace"}, {"query-scope-b", "workspace"}} {
		_, _, err := repo.FindBySlug(ctx, tx, scope[0], scope[1], "saved")
		require.ErrorIs(t, err, queries.ErrQueryNotFound)
	}
	_, _, err = repo.Create(ctx, tx, "query-scope-b", "workspace", &queries.CreateParams{Slug: "saved", Name: "Other tenant", Data: data})
	require.NoError(t, err)
	loaded, _, err = repo.FindBySlug(ctx, tx, "query-scope-a", "workspace", "saved")
	require.NoError(t, err)
	require.JSONEq(t, string(updated.Data), string(loaded.Data))
}
