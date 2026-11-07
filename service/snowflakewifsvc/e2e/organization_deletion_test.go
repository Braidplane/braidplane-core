package e2e

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/snowflakewifsvc"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/require"
)

type unusedEnvironments struct{ t *testing.T }

func (e unusedEnvironments) CreateDefaultEnvironments(context.Context, slug.Slug) error {
	e.t.Fatal("organization deletion must not create environments")
	return nil
}

func TestOrganizationDeletionWaitsForWIFReconciliation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires worktree PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "organization deletion with Snowflake WIF cleanup")
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	org, err := slug.WithRandomSuffix("wif-delete", 8)
	require.NoError(t, err)
	var orgID int
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1) RETURNING id`, org).Scan(&orgID))
	var credentialID int64
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO credentials (organization_id, slug, kind, status) VALUES ($1, 'identity', 'static_secret', 'active') RETURNING id`, orgID).Scan(&credentialID))
	_, err = tx.Exec(ctx, `INSERT INTO data_sources (organization_id, credential_id, name, slug, type, config) VALUES ($1, $2, 'Warehouse', 'warehouse', 'snowflake', '{"account":"account-one"}')`, orgID, credentialID)
	require.NoError(t, err)
	txm := testutil.FakeTransactionManager{Tx: tx}
	orgs := orgsvc.New(txm, organizations.NewRepository(), unusedEnvironments{t}, &fakes.MembersService{}, &fakes.RolesService{}, eventtest.NewProducer(nil), &fakes.AdmissionService{})
	const issuer = "https://issuer.test"
	const ds = slug.Slug("warehouse")
	_, err = snowflakewifsvc.SetDesired(ctx, tx, org, ds, issuer, "account-one", true)
	require.NoError(t, err)
	registrar := &recordingRegistrar{created: map[string]spiffeid.ID{}, revoked: map[string]spiffeid.ID{}}
	reconciler, err := snowflakewifsvc.NewReconciler(txm, registrar, events.New())
	require.NoError(t, err)
	require.NoError(t, reconciler.Reconcile(ctx))
	identity, err := snowflakewifsvc.Resolve(ctx, txm, issuer, org, ds, "account-one")
	require.NoError(t, err)
	var entryID string
	require.NoError(t, tx.QueryRow(ctx, `SELECT entry_id FROM snowflake_wif_bindings WHERE organization_id=$1`, orgID).Scan(&entryID))
	require.Equal(t, identity.Subject, registrar.created[entryID])

	_, err = tx.Exec(ctx, `UPDATE data_sources SET deleted_at=CURRENT_TIMESTAMP WHERE organization_id=$1`, orgID)
	require.NoError(t, err)
	// End at ready so SetDesired must transition the binding to revoking.
	for _, state := range []string{"pending", "failed", "revoking", "ready"} {
		t.Run(state, func(t *testing.T) {
			_, err := tx.Exec(ctx, `UPDATE snowflake_wif_bindings SET state=$2 WHERE organization_id=$1`, orgID, state)
			require.NoError(t, err)
			require.ErrorIs(t, orgs.DeleteOrganization(ctx, org), orgsvc.ErrDeletionBlocked)
		})
	}
	_, err = snowflakewifsvc.SetDesired(ctx, tx, org, ds, "", "", false)
	require.NoError(t, err)
	err = orgs.DeleteOrganization(ctx, org)
	require.ErrorIs(t, err, orgsvc.ErrDeletionBlocked)
	require.ErrorContains(t, err, "wait for Snowflake workload identity cleanup")
	require.NotContains(t, registrar.revoked, entryID)

	// Another tenant's deletion must not wait on this binding.
	other := slug.Slug(org.String() + "-other")
	_, err = tx.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, other)
	require.NoError(t, err)
	require.NoError(t, orgs.DeleteOrganization(ctx, other))

	registrar.revokeErr = xerror.Unavailable(xerror.DataSource)
	require.NoError(t, reconciler.Reconcile(ctx))
	var state, retainedID string
	var desired, retryScheduled bool
	require.NoError(t, tx.QueryRow(ctx, `SELECT state, entry_id, desired, reconcile_after > CURRENT_TIMESTAMP FROM snowflake_wif_bindings WHERE organization_id=$1`, orgID).Scan(&state, &retainedID, &desired, &retryScheduled))
	require.Equal(t, "revoking", state)
	require.Equal(t, entryID, retainedID)
	require.False(t, desired)
	require.True(t, retryScheduled)
	require.NotContains(t, registrar.revoked, entryID)
	require.ErrorIs(t, orgs.DeleteOrganization(ctx, org), orgsvc.ErrDeletionBlocked)

	registrar.revokeErr = nil
	_, err = tx.Exec(ctx, `UPDATE snowflake_wif_bindings SET reconcile_after=CURRENT_TIMESTAMP WHERE organization_id=$1`, orgID)
	require.NoError(t, err)
	require.NoError(t, reconciler.Reconcile(ctx))
	require.Equal(t, identity.Subject, registrar.revoked[entryID])
	var remaining int
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM snowflake_wif_bindings WHERE organization_id=$1`, orgID).Scan(&remaining))
	require.Zero(t, remaining)
	require.NoError(t, orgs.DeleteOrganization(ctx, org))
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM organizations WHERE id=$1`, orgID).Scan(&remaining))
	require.Zero(t, remaining)
}
