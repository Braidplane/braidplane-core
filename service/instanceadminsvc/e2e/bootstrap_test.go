package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/instanceadmins"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/instanceadminsvc"
	"github.com/stretchr/testify/require"
)

func TestConcurrentBootstrapCreatesOnlyOneAdministrator(t *testing.T) {
	pool, _ := roleFixture(t)
	hasher := crypto.NewArgon2WithParams(8, 1, 1, 16, 32)
	svc, err := instanceadminsvc.NewBootstrapper(postgres.NewTransactionManager(pool), instanceadmins.NewRepository(), users.NewRepository(), hasher)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, name := range []string{"bootstrap-one", "bootstrap-two"} {
		t.Cleanup(func() {
			_, err := pool.Exec(context.Background(), "DELETE FROM instance_admin_audits WHERE target_user_slug=$1", name)
			require.NoError(t, err)
			_, err = pool.Exec(context.Background(), "DELETE FROM users WHERE slug=$1", name)
			require.NoError(t, err)
		})
		go func() {
			<-start
			results <- svc.Bootstrap(ctx, must.Slug(name), "Owner", name+"@example.test", "synthetic-password")
		}()
	}
	close(start)
	var succeeded int
	for range 2 {
		if err := <-results; err == nil {
			succeeded++
		} else {
			require.ErrorContains(t, err, "bootstrap is closed")
		}
	}
	require.Equal(t, 1, succeeded)
	count, err := instanceadmins.NewRepository().CountSuperAdmins(ctx, pool)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	var verified, audits int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE slug IN ('bootstrap-one','bootstrap-two') AND verified_at IS NOT NULL").Scan(&verified))
	require.Equal(t, 1, verified)
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM instance_admin_audits WHERE target_user_slug IN ('bootstrap-one','bootstrap-two')").Scan(&audits))
	require.Equal(t, 1, audits)
}

func TestBootstrapAuditFailureRollsBackAccount(t *testing.T) {
	pool, _ := roleFixture(t)
	svc, err := instanceadminsvc.NewBootstrapper(postgres.NewTransactionManager(pool), instanceadmins.NewRepository(), users.NewRepository(), crypto.NewArgon2WithParams(8, 1, 1, 16, 32))
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), "ALTER TABLE instance_admin_audits ADD CONSTRAINT fixture_reject_bootstrap CHECK (false) NOT VALID")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "ALTER TABLE instance_admin_audits DROP CONSTRAINT IF EXISTS fixture_reject_bootstrap")
		require.NoError(t, err)
	})
	err = svc.Bootstrap(t.Context(), must.Slug("bootstrap-rollback"), "Owner", "rollback@example.test", "synthetic-password")
	require.ErrorContains(t, err, "fixture_reject_bootstrap")
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM users WHERE slug='bootstrap-rollback'").Scan(&count))
	require.Zero(t, count)
}
