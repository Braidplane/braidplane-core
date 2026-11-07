package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/instanceadmins"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/instanceadminsvc"
)

func TestConcurrentRevokesRetainOneInstanceAdmin(t *testing.T) {
	pool, svc := roleFixture(t)
	first := createUser(t, pool, "first-admin", model.RoleSuperAdmin, true)
	second := createUser(t, pool, "second-admin", model.RoleSuperAdmin, true)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, subject := range []slug.Slug{first, second} {
		go func() { <-start; results <- svc.Revoke(ctx, subject) }()
	}
	close(start)
	var succeeded, refused int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case xerror.Is(err, instanceadminsvc.ErrLastAdmin):
			refused++
		default:
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, refused)
	count, err := instanceadmins.NewRepository().CountSuperAdmins(ctx, pool)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	var audits int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM instance_admin_audits WHERE target_user_slug IN ($1,$2) AND previous_role=$3 AND new_role=$4", first, second, model.RoleSuperAdmin.String(), model.RoleUser.String()).Scan(&audits))
	require.Equal(t, 1, audits)
}

func TestAuditInsertFailureRollsBackRoleChange(t *testing.T) {
	pool, svc := roleFixture(t)
	subject := createUser(t, pool, "audit-rollback", model.RoleUser, true)
	// The package-owned database lets the fixture reject real audit writes without a fake repository.
	_, err := pool.Exec(t.Context(), "ALTER TABLE instance_admin_audits ADD CONSTRAINT fixture_reject_audit CHECK (false) NOT VALID")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "ALTER TABLE instance_admin_audits DROP CONSTRAINT IF EXISTS fixture_reject_audit")
		require.NoError(t, err)
	})
	err = svc.Grant(t.Context(), subject)
	require.ErrorContains(t, err, "fixture_reject_audit")
	user, err := instanceadmins.NewRepository().FindUserForUpdate(t.Context(), pool, subject)
	require.NoError(t, err)
	require.Equal(t, model.RoleUser, user.Role)
	require.Equal(t, 0, auditCount(t, pool, subject))
	_, err = pool.Exec(t.Context(), "ALTER TABLE instance_admin_audits DROP CONSTRAINT fixture_reject_audit")
	require.NoError(t, err)
	require.NoError(t, svc.Grant(t.Context(), subject))
	require.NoError(t, svc.Grant(t.Context(), subject))
	user, err = instanceadmins.NewRepository().FindUserForUpdate(t.Context(), pool, subject)
	require.NoError(t, err)
	require.Equal(t, model.RoleSuperAdmin, user.Role)
	require.Equal(t, 1, auditCount(t, pool, subject))
}

func TestGrantRefusesUnverifiedUser(t *testing.T) {
	pool, svc := roleFixture(t)
	subject := createUser(t, pool, "unverified-admin", model.RoleUser, false)
	require.ErrorIs(t, svc.Grant(t.Context(), subject), instanceadminsvc.ErrUserUnverified)
	user, err := instanceadmins.NewRepository().FindUserForUpdate(t.Context(), pool, subject)
	require.NoError(t, err)
	require.Equal(t, model.RoleUser, user.Role)
	require.Equal(t, 0, auditCount(t, pool, subject))
}

func roleFixture(t *testing.T) (*pgxpool.Pool, *instanceadminsvc.Service) {
	t.Helper()
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	pool := e2edb.OpenPostgresPool(t, "instance administrator role changes")
	svc, err := instanceadminsvc.New(postgres.NewTransactionManager(pool), instanceadmins.NewRepository())
	require.NoError(t, err)
	return pool, svc
}

func createUser(t *testing.T, pool *pgxpool.Pool, name string, role model.Role, verified bool) slug.Slug {
	t.Helper()
	subject := must.Slug(name)
	repo := users.NewRepository()
	_, err := repo.CreateUser(t.Context(), pool, &users.CreateParams{Slug: subject, Email: name + "@example.test", FullName: "Synthetic Instance Admin", Role: role, Locale: "en"})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DELETE FROM instance_admin_audits WHERE target_user_slug=$1", subject)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), "DELETE FROM users WHERE slug=$1", subject)
		require.NoError(t, err)
	})
	if verified {
		require.NoError(t, repo.MarkEmailAsVerified(t.Context(), pool, subject, time.Now()))
	}
	return subject
}

func auditCount(t *testing.T, pool *pgxpool.Pool, subject slug.Slug) int {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM instance_admin_audits WHERE target_user_slug=$1", subject).Scan(&count))
	return count
}
