package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/postgres/executionrequests"
)

func TestAccountBindingSerializesRemovalAndPreservesTenantScope(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	t.Parallel()
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "execution account attribution")
	name := slug.Slug(fmt.Sprintf("execution-account-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, name)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(ctx, `DELETE FROM execution_requests WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, name)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(ctx, `DELETE FROM organizations WHERE slug = $1`, name)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(ctx, `DELETE FROM users WHERE slug = $1`, name)
		require.NoError(t, cleanupErr)
	})
	_, err = pool.Exec(ctx, `INSERT INTO users (slug, email, role) VALUES ($1, $2, 'user')`, name, name.String()+"@example.com")
	require.NoError(t, err)
	id := uuid.New()
	repo := executionrequests.New()
	require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{
		ID: id, OrganizationSlug: name.String(), Kind: "transform", RequestCiphertext: []byte("encrypted"), DeadlineAt: time.Now().Add(time.Minute),
	}, time.Now().Add(time.Hour)))
	require.ErrorIs(t, repo.BindUser(ctx, pool, id, "another-tenant", name), executiondelivery.ErrAccountUnavailable)

	admission, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = admission.Rollback(ctx) }()
	require.NoError(t, repo.BindUser(ctx, admission, id, name.String(), name))
	removal, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = removal.Rollback(ctx) }()
	_, err = removal.Exec(ctx, `SET LOCAL lock_timeout = '100ms'`)
	require.NoError(t, err)
	_, err = removal.Exec(ctx, `UPDATE users SET deleted_at = NOW() WHERE slug = $1`, name)
	var lockError *pgconn.PgError
	require.ErrorAs(t, err, &lockError)
	require.Equal(t, "55P03", lockError.Code)
	require.NoError(t, removal.Rollback(ctx))
	require.NoError(t, admission.Commit(ctx))

	var bound bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM execution_requests r JOIN users u ON u.id = r.initiating_user_id
		WHERE r.id = $1 AND u.slug = $2)
	`, id, name).Scan(&bound))
	require.True(t, bound)
	_, err = pool.Exec(ctx, `UPDATE users SET deleted_at = NOW() WHERE slug = $1`, name)
	require.NoError(t, err)
	lateID := uuid.New()
	late, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = late.Rollback(ctx) }()
	require.NoError(t, repo.Create(ctx, late, executionrequests.Request{
		ID: lateID, OrganizationSlug: name.String(), Kind: "transform", RequestCiphertext: []byte("encrypted"), DeadlineAt: time.Now().Add(time.Minute),
	}, time.Now().Add(time.Hour)))
	require.ErrorIs(t, repo.BindUser(ctx, late, lateID, name.String(), name), executiondelivery.ErrAccountUnavailable)
	require.NoError(t, late.Rollback(ctx))
	var remaining int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM execution_requests WHERE id = $1`, lateID).Scan(&remaining))
	require.Zero(t, remaining)
}

func TestOrganizationDeletionRequiresExecutionDisposition(t *testing.T) { // Shared expiry claimant.
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "execution parent deletion protection")
	for _, state := range []string{"pending", "running", "ambiguous", "cancelled", "succeeded", "failed"} {
		t.Run(state, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(ctx) }()
			org := slug.Slug(fmt.Sprintf("execution-parent-%d", time.Now().UnixNano()))
			_, err = tx.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, org)
			require.NoError(t, err)
			id := uuid.New()
			repo := executionrequests.New()
			require.NoError(t, repo.Create(ctx, tx, executionrequests.Request{
				ID: id, OrganizationSlug: org.String(), Kind: "query", RequestCiphertext: []byte("encrypted"), DeadlineAt: time.Now().Add(time.Minute),
			}, time.Now().Add(time.Hour)))
			_, err = tx.Exec(ctx, `UPDATE execution_requests SET state = $2, started_at = NOW() WHERE id = $1`, id, state)
			require.NoError(t, err)
			if state == "running" || state == "ambiguous" || state == "cancelled" {
				_, err = tx.Exec(ctx, `UPDATE execution_requests SET expires_at = NOW() - INTERVAL '1 second' WHERE id = $1`, id)
				require.NoError(t, err)
				_, err = repo.DeleteExpired(ctx, tx)
				require.NoError(t, err)
				var bytes int
				require.NoError(t, tx.QueryRow(ctx, `SELECT octet_length(request_ciphertext) FROM execution_requests WHERE id = $1`, id).Scan(&bytes))
				require.Zero(t, bytes)
			}

			attempt, err := tx.Begin(ctx)
			require.NoError(t, err)
			_, err = attempt.Exec(ctx, `DELETE FROM organizations WHERE slug = $1`, org)
			var constraint *pgconn.PgError
			require.ErrorAs(t, err, &constraint)
			require.Equal(t, "23001", constraint.Code)
			require.Equal(t, "execution_requests_organization_id_fkey", constraint.ConstraintName)
			require.NoError(t, attempt.Rollback(ctx))
			var retainedState string
			require.NoError(t, tx.QueryRow(ctx, `SELECT state FROM execution_requests WHERE id = $1`, id).Scan(&retainedState))
			require.Equal(t, state, retainedState)

			// Fixture disposal is explicit; production must first resolve processing.
			_, err = tx.Exec(ctx, `DELETE FROM execution_requests WHERE id = $1`, id)
			require.NoError(t, err)
			deleted, err := tx.Exec(ctx, `DELETE FROM organizations WHERE slug = $1`, org)
			require.NoError(t, err)
			require.EqualValues(t, 1, deleted.RowsAffected())
		})
	}
}
