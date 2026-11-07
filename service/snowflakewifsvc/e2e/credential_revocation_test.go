package e2e

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/service/snowflakewifsvc"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/require"
)

type recordingRegistrar struct {
	created, revoked map[string]spiffeid.ID
	revokeErr        error
}

func (r *recordingRegistrar) Ensure(_ context.Context, id string, subject spiffeid.ID) error {
	r.created[id] = subject
	return nil
}

func (r *recordingRegistrar) Revoke(_ context.Context, id string, subject spiffeid.ID) error {
	if r.revokeErr != nil {
		return r.revokeErr
	}
	r.revoked[id] = subject
	return nil
}

func TestCredentialRevocationDeniesReadinessAndRemovesRegistration(t *testing.T) {
	if testing.Short() {
		t.Skip("requires worktree PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "Snowflake WIF credential revocation")
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	org, err := slug.WithRandomSuffix("wif-revoke", 8)
	require.NoError(t, err)
	var orgID int
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1) RETURNING id`, org).Scan(&orgID))
	var credentialID int64
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO credentials (organization_id, slug, kind, status) VALUES ($1, 'identity', 'static_secret', 'active') RETURNING id`, orgID).Scan(&credentialID))
	_, err = tx.Exec(ctx, `INSERT INTO data_sources (organization_id, credential_id, name, slug, type, config) VALUES ($1, $2, 'Warehouse', 'warehouse', 'snowflake', '{"account":"account-one"}')`, orgID, credentialID)
	require.NoError(t, err)
	const issuer = "https://issuer.test"
	const ds = slug.Slug("warehouse")
	// Keep fixtures and reconciler writes isolated from other packages in the shared database.
	txm := testutil.FakeTransactionManager{Tx: tx}
	require.NoError(t, txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		ready, err := snowflakewifsvc.SetDesired(ctx, db, org, ds, issuer, "account-one", true)
		require.False(t, ready)
		return err
	}))
	registrar := &recordingRegistrar{created: map[string]spiffeid.ID{}, revoked: map[string]spiffeid.ID{}}
	reconciler, err := snowflakewifsvc.NewReconciler(txm, registrar, events.New())
	require.NoError(t, err)
	require.NoError(t, reconciler.Reconcile(ctx))
	identity, err := snowflakewifsvc.Resolve(ctx, txm, issuer, org, ds, "account-one")
	require.NoError(t, err)
	var entryID string
	require.NoError(t, tx.QueryRow(ctx, `SELECT entry_id FROM snowflake_wif_bindings WHERE organization_id=$1`, orgID).Scan(&entryID))
	require.Equal(t, identity.Subject, registrar.created[entryID])

	_, err = tx.Exec(ctx, `UPDATE credentials SET status='revoked' WHERE organization_id=$1 AND id=$2`, orgID, credentialID)
	require.NoError(t, err)
	_, err = snowflakewifsvc.Resolve(ctx, txm, issuer, org, ds, "account-one")
	require.ErrorContains(t, err, "not ready")
	_, err = tx.Exec(ctx, `UPDATE snowflake_wif_bindings SET reconcile_after=CURRENT_TIMESTAMP WHERE organization_id=$1`, orgID)
	require.NoError(t, err)
	require.NoError(t, reconciler.Reconcile(ctx))
	require.Equal(t, identity.Subject, registrar.revoked[entryID])
	var remaining int
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM snowflake_wif_bindings WHERE organization_id=$1`, orgID).Scan(&remaining))
	require.Zero(t, remaining)
}
