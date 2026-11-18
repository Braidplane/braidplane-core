package e2e

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/apikeys"
)

func TestAPIKeyRoleLockStaysScopedThroughMutation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "API key role lock")
	_, err := pool.Exec(ctx, `
INSERT INTO organizations (name, slug) VALUES ('Key lock A', 'key-lock-a'), ('Key lock B', 'key-lock-b');
INSERT INTO roles (organization_id, name, slug)
SELECT id, 'Operator', 'operator' FROM organizations WHERE slug = 'key-lock-a'
UNION ALL
SELECT id, 'Reader', 'reader' FROM organizations WHERE slug = 'key-lock-b';`)
	require.NoError(t, err)
	repo := apikeys.NewRepository()
	first, err := repo.Create(ctx, pool, apikeys.CreateParams{
		OrgSlug: "key-lock-a", Slug: "fixture", Name: "First fixture", RoleSlug: "operator",
		TokenHash: "synthetic-first-hash", TokenPrefix: "first",
	})
	require.NoError(t, err)
	second, err := repo.Create(ctx, pool, apikeys.CreateParams{
		OrgSlug: "key-lock-b", Slug: "fixture", Name: "Second fixture", RoleSlug: "reader",
		TokenHash: "synthetic-second-hash", TokenPrefix: "second",
	})
	require.NoError(t, err)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	role, err := repo.LockRoleBySlug(ctx, tx, "key-lock-a", "fixture")
	require.NoError(t, err)
	require.EqualValues(t, "operator", role)

	// A separate transaction may lock the other tenant's row but must wait for this one.
	const tryLock = `SELECT id FROM api_keys WHERE id = $1 FOR UPDATE NOWAIT`
	var id int
	err = pool.QueryRow(ctx, tryLock, first.ID).Scan(&id)
	var lockError *pgconn.PgError
	require.ErrorAs(t, err, &lockError)
	require.Equal(t, "55P03", lockError.Code)
	require.NoError(t, pool.QueryRow(ctx, tryLock, second.ID).Scan(&id))
	require.Equal(t, second.ID, id)
	role, err = repo.LockRoleBySlug(ctx, tx, "key-lock-b", "fixture")
	require.NoError(t, err)
	require.EqualValues(t, "reader", role)
	_, err = repo.LockRoleBySlug(ctx, tx, "key-lock-a", "missing")
	require.ErrorIs(t, err, apikeys.ErrNotFound)

	require.NoError(t, repo.Update(ctx, tx, apikeys.UpdateParams{
		OrgSlug: "key-lock-a", Slug: "fixture", Name: "Updated fixture",
	}))
	rotated, err := repo.Rotate(ctx, tx, apikeys.RotateParams{
		OrgSlug: "key-lock-a", Slug: "fixture", TokenHash: "synthetic-rotated-hash", TokenPrefix: "rotated",
	})
	require.NoError(t, err)
	require.Equal(t, first.ID, rotated.ID)
	require.NoError(t, repo.Revoke(ctx, tx, "key-lock-a", "fixture"))
	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, pool.QueryRow(ctx, tryLock, first.ID).Scan(&id))
	require.Equal(t, first.ID, id)
	updated, err := repo.FindBySlug(ctx, pool, "key-lock-a", "fixture")
	require.NoError(t, err)
	require.Equal(t, first.ID, updated.ID)
	require.Equal(t, "Updated fixture", updated.Name)
	require.Equal(t, "rotated", updated.TokenPrefix)
	require.NotNil(t, updated.RevokedAt)
	untouched, err := repo.FindBySlug(ctx, pool, "key-lock-b", "fixture")
	require.NoError(t, err)
	require.Equal(t, second.ID, untouched.ID)
	require.Equal(t, second.Name, untouched.Name)
	require.Equal(t, second.TokenPrefix, untouched.TokenPrefix)
	require.Nil(t, untouched.RevokedAt)
}
