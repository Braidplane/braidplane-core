package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/testutil/datasourcee2e"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/snowflakewifsvc"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/require"
)

type fakeWIFRegistrar struct {
	entries map[string]spiffeid.ID
	failure error
	creates map[spiffeid.ID]int
	partial bool
}

func (f *fakeWIFRegistrar) Ensure(_ context.Context, id string, subject spiffeid.ID) error {
	if f.failure != nil && !f.partial {
		return f.failure
	}
	if existing, ok := f.entries[id]; ok {
		if existing != subject {
			return xerror.Unavailable(xerror.DataSource).Detail("ownership mismatch")
		}
		return f.failure
	}
	f.entries[id] = subject
	f.creates[subject]++
	return f.failure
}

func (f *fakeWIFRegistrar) Revoke(_ context.Context, id string, subject spiffeid.ID) error {
	if f.failure != nil {
		return f.failure
	}
	if existing, ok := f.entries[id]; ok && existing != subject {
		return xerror.Unavailable(xerror.DataSource).Detail("ownership mismatch")
	}
	delete(f.entries, id)
	return nil
}

type rollbackWIFTransaction struct{ postgres.TransactionManager }

func (m rollbackWIFTransaction) RunInTx(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	return m.TransactionManager.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		if err := fn(ctx, db); err != nil {
			return err
		}
		return xerror.Unavailable(xerror.DataSource).Detail("injected commit failure")
	})
}

func TestManagedSnowflakeWIFLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires worktree PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "Snowflake WIF lifecycle")
	ctx := t.Context()
	orgA, orgB := datasourcee2e.UniqueSlug(t, "wif-a"), datasourcee2e.UniqueSlug(t, "wif-b")
	for _, org := range []slug.Slug{orgA, orgB} {
		datasourcee2e.SeedOrganization(t, pool, org)
	}
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM snowflake_wif_bindings WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`, []string{orgA.String(), orgB.String()})
		require.NoError(t, err)
	})
	const issuer = "https://issuer.test"
	const source = slug.Slug("warehouse")
	stack := datasourcee2e.NewStack(t, pool)
	connectionTests := 0
	var connectionTestError error
	svc := stack.ServiceWithConfig(t, datasourcee2e.ServiceConfig{
		SnowflakeWIFIssuer: issuer,
		ConnectionTester: datasourcee2e.ConnectionTestExecutorFunc(func(context.Context, datasourcessvc.ConnectionTestRequest) error {
			connectionTests++
			return connectionTestError
		}),
	})
	params := func(account string) *datasourcessvc.CreateParams {
		return &datasourcessvc.CreateParams{
			Name: "Warehouse", Slug: source, Type: model.DataSourceTypeSnowflake,
			ConfigJSON:  []byte(`{"account":"` + account + `","warehouse":"COMPUTE","database":"TEST","schema":"PUBLIC","role":"DEDICATED","wif_objects":[{"name":"ORDERS","privilege":"SELECT"}]}`),
			Credentials: map[string]any{"auth_type": "workload_identity"}, ReadOnly: true,
		}
	}
	missingGrants := params("account-one")
	missingGrants.ConfigJSON = []byte(`{"account":"account-one","warehouse":"COMPUTE","database":"TEST","schema":"PUBLIC","role":"DEDICATED"}`)
	_, err := svc.Create(ctx, orgA, model.SystemEventActor(), missingGrants)
	require.ErrorContains(t, err, "table")
	for _, org := range []slug.Slug{orgA, orgB} {
		created, err := svc.Create(ctx, org, model.SystemEventActor(), params("account-one"))
		require.NoError(t, err)
		require.False(t, created.IsActive)
		_, err = snowflakewifsvc.Resolve(ctx, stack.Txm, issuer, org, source, "account-one")
		require.ErrorContains(t, err, "not ready")
	}
	onboarding, err := svc.GetSnowflakeOnboarding(ctx, orgA, source)
	require.NoError(t, err)
	require.Equal(t, "pending", onboarding.IdentityState)
	require.False(t, onboarding.Active)
	require.Contains(t, onboarding.Subject, "/tenant/"+orgA.String()+"/")
	require.Contains(t, onboarding.SQL, `GRANT SELECT ON TABLE "TEST"."PUBLIC"."ORDERS"`)
	_, err = svc.GetSnowflakeOnboarding(ctx, "unrelated-tenant", source)
	require.Error(t, err)

	require.Zero(t, connectionTests)
	renamed, err := svc.Update(ctx, orgA, source, model.SystemEventActor(), &datasourcessvc.UpdateParams{Name: "Pending warehouse"})
	require.NoError(t, err)
	require.False(t, renamed.IsActive)
	require.Zero(t, connectionTests)
	_, connectionTestError = snowflakewifsvc.Resolve(ctx, stack.Txm, issuer, orgA, source, "account-one")
	require.ErrorContains(t, svc.TestExistingConnection(ctx, orgA, source), "not ready")
	require.Equal(t, 1, connectionTests)
	connectionTestError = nil
	fake := &fakeWIFRegistrar{entries: map[string]spiffeid.ID{}, creates: map[spiffeid.ID]int{}}
	ownedEntries := func() map[string]spiffeid.ID {
		owned := map[string]spiffeid.ID{}
		for id, subject := range fake.entries {
			if strings.HasPrefix(subject.Path(), "/tenant/"+orgA.String()+"/") || strings.HasPrefix(subject.Path(), "/tenant/"+orgB.String()+"/") {
				owned[id] = subject
			}
		}
		return owned
	}
	reconcile := func() {
		t.Helper()
		// Advance due time deterministically instead of sleeping through retries.
		_, err := pool.Exec(ctx, `UPDATE snowflake_wif_bindings SET reconcile_after = CURRENT_TIMESTAMP WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1))`, []string{orgA.String(), orgB.String()})
		require.NoError(t, err)
		runner, err := snowflakewifsvc.NewReconciler(stack.Txm, fake, events.New())
		require.NoError(t, err)
		require.NoError(t, runner.Reconcile(ctx))
	}
	resolve := func(org slug.Slug, account string, ready bool) {
		t.Helper()
		identity, err := snowflakewifsvc.Resolve(ctx, stack.Txm, issuer, org, source, account)
		if !ready {
			require.Error(t, err)
			return
		}
		require.NoError(t, err)
		require.Equal(t, "/tenant/"+org.String()+"/snowflake/"+account+"/connection/warehouse", identity.Subject.Path())
	}
	// Losing the database commit after SPIRE creation preserves the pending ID.
	failingCommit, err := snowflakewifsvc.NewReconciler(rollbackWIFTransaction{stack.Txm}, fake, events.New())
	require.NoError(t, err)
	require.ErrorContains(t, failingCommit.Reconcile(ctx), "injected commit failure")
	require.Len(t, ownedEntries(), 2)
	resolve(orgA, "account-one", false)
	resolve(orgB, "account-one", false)
	// A create accepted by SPIRE followed by a timeout must retain its exact ID.
	fake.failure, fake.partial = xerror.Unavailable(xerror.DataSource).Detail("secret-canary-provider-error"), true
	reconcile()
	require.Len(t, ownedEntries(), 2)
	resolve(orgA, "account-one", false)
	fake.failure, fake.partial = nil, false
	reconcile()
	reconcile()
	for _, subject := range ownedEntries() {
		require.Equal(t, 1, fake.creates[subject])
	}
	require.Len(t, ownedEntries(), 2)
	resolve(orgA, "account-one", true)
	resolve(orgB, "account-one", true)
	require.NoError(t, svc.TestExistingConnection(ctx, orgA, source))
	require.Equal(t, 2, connectionTests)
	onboarding, err = svc.GetSnowflakeOnboarding(ctx, orgA, source)
	require.NoError(t, err)
	require.True(t, onboarding.Active)
	require.Equal(t, "ready", onboarding.IdentityState)
	readyUpdate, err := svc.UpdateCredentials(ctx, orgA, source, model.SystemEventActor(), &datasourcessvc.UpdateCredentialsParams{Credentials: map[string]any{"auth_type": "workload_identity"}})
	require.NoError(t, err)
	require.False(t, readyUpdate.IsActive)
	require.True(t, readyUpdate.ReadOnly)
	require.Equal(t, 2, connectionTests)
	require.NoError(t, svc.TestExistingConnection(ctx, orgA, source))
	require.Equal(t, 3, connectionTests)
	assertWIFCapabilities(t, pool, orgA, source)
	resolve(orgA, "wrong-account", false)
	resolve("unrelated-tenant", "account-one", false)

	// A stale registration is repaired after restart without minting a new identity ID.
	for id, subject := range fake.entries {
		if subject.Path() == "/tenant/"+orgA.String()+"/snowflake/account-one/connection/warehouse" {
			delete(fake.entries, id)
		}
	}
	reconcile()
	require.Len(t, ownedEntries(), 2)
	resolve(orgA, "account-one", true)
	_, err = pool.Exec(ctx, `UPDATE snowflake_wif_bindings SET verified_at = CURRENT_TIMESTAMP - INTERVAL '61 seconds' WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgA)
	require.NoError(t, err)
	resolve(orgA, "account-one", false)
	resolve(orgB, "account-one", true)
	reconcile()

	// An account change invalidates the previous binding in the same commit.
	updated, err := svc.Update(ctx, orgA, source, model.SystemEventActor(), &datasourcessvc.UpdateParams{ConfigJSON: params("account-two").ConfigJSON})
	require.NoError(t, err)
	require.False(t, updated.IsActive)
	require.True(t, updated.ReadOnly)
	resolve(orgA, "account-one", false)
	resolve(orgA, "account-two", false)
	reconcile()
	require.Len(t, ownedEntries(), 2)
	resolve(orgA, "account-two", true)
	resolve(orgB, "account-one", true)

	disabled := false
	_, err = svc.Update(ctx, orgA, source, model.SystemEventActor(), &datasourcessvc.UpdateParams{IsActive: &disabled})
	require.NoError(t, err)
	resolve(orgA, "account-two", false)
	_, err = svc.Update(ctx, orgA, source, model.SystemEventActor(), &datasourcessvc.UpdateParams{Name: "Renamed while disabled"})
	require.NoError(t, err)
	resolve(orgA, "account-two", false)
	fake.failure = xerror.Unavailable(xerror.DataSource).Detail("secret-canary-provider-error")
	reconcile()
	require.Len(t, ownedEntries(), 2)
	fake.failure = nil
	reconcile()
	require.Len(t, ownedEntries(), 1)
	onboarding, err = svc.GetSnowflakeOnboarding(ctx, orgA, source)
	require.NoError(t, err)
	require.Equal(t, "disabled", onboarding.IdentityState)
	require.False(t, onboarding.Active)
	resolve(orgB, "account-one", true)

	// Deletion is locally effective even if SPIRE is temporarily unreachable.
	require.NoError(t, svc.Delete(ctx, orgB, source, model.SystemEventActor()))
	resolve(orgB, "account-one", false)
	fake.failure = xerror.Unavailable(xerror.DataSource).Detail("secret-canary-provider-error")
	reconcile()
	require.Len(t, ownedEntries(), 1)
	fake.failure = nil
	reconcile()
	require.Empty(t, ownedEntries())
	require.NoError(t, svc.Restore(ctx, orgB, source, model.SystemEventActor()))
	resolve(orgB, "account-one", false)
	_, err = svc.UpdateCredentials(ctx, orgB, source, model.SystemEventActor(), &datasourcessvc.UpdateCredentialsParams{Credentials: map[string]any{"auth_type": "workload_identity"}})
	require.NoError(t, err)
	reconcile()
	resolve(orgB, "account-one", true)
	// Removing the issuer setting must not prevent cleanup of existing intent.
	disabledSvc := stack.ServiceWithConfig(t, datasourcee2e.ServiceConfig{ConnectionTester: datasourcee2e.ConnectionTestExecutorFunc(func(context.Context, datasourcessvc.ConnectionTestRequest) error { return nil })})
	_, err = disabledSvc.UpdateCredentials(ctx, orgB, source, model.SystemEventActor(), &datasourcessvc.UpdateCredentialsParams{Credentials: map[string]any{"auth_type": "password", "username": "test-user", "password": "test-password"}})
	require.NoError(t, err)
	resolve(orgB, "account-one", false)
	reconcile()
	require.Empty(t, ownedEntries())
	testsBeforePending := connectionTests
	pendingUpdate, err := svc.UpdateCredentials(ctx, orgB, source, model.SystemEventActor(), &datasourcessvc.UpdateCredentialsParams{Credentials: map[string]any{"auth_type": "workload_identity"}})
	require.NoError(t, err)
	require.False(t, pendingUpdate.IsActive)
	require.Equal(t, testsBeforePending, connectionTests)
	reconcile()
	resolve(orgB, "account-one", true)
	_, err = disabledSvc.Update(ctx, orgB, source, model.SystemEventActor(), &datasourcessvc.UpdateParams{IsActive: &disabled})
	require.NoError(t, err)
	resolve(orgB, "account-one", false)
	reconcile()
	require.Empty(t, ownedEntries())
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1)) AND metadata->>'lifecycle' = 'snowflake_wif'`, []string{orgA.String(), orgB.String()}).Scan(&count))
	require.Positive(t, count)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE organization_id IN (SELECT id FROM organizations WHERE slug = ANY($1)) AND metadata::text LIKE '%secret-canary%'`, []string{orgA.String(), orgB.String()}).Scan(&count))
	require.Zero(t, count)
}
