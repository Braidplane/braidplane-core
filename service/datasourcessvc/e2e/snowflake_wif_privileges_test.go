package e2e

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	repowif "github.com/Braidplane/braidplane-core/postgres/snowflakewif"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func assertWIFCapabilities(t *testing.T, pool *pgxpool.Pool, org, ds slug.Slug) {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Exercise the Core repository capability contract with synthetic roles.
	_, err = tx.Exec(ctx, `
 CREATE ROLE wif_fixture_reconciler_test NOLOGIN;
 GRANT USAGE ON SCHEMA public TO wif_fixture_reconciler_test;
 GRANT SELECT(id,slug) ON organizations TO wif_fixture_reconciler_test;
 GRANT SELECT(id,organization_id,slug,type,config,deleted_at,credential_id),UPDATE(id) ON data_sources TO wif_fixture_reconciler_test;
 GRANT SELECT(id,organization_id,status) ON credentials TO wif_fixture_reconciler_test;
 GRANT SELECT,UPDATE,DELETE ON snowflake_wif_bindings TO wif_fixture_reconciler_test;
 `)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE snowflake_wif_bindings SET reconcile_after = CURRENT_TIMESTAMP WHERE organization_id = (SELECT id FROM organizations WHERE slug=$1)`, org)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `SET LOCAL ROLE wif_fixture_reconciler_test`)
	require.NoError(t, err)
	repo := repowif.Repository{}
	keys, err := repo.Due(ctx, tx)
	require.NoError(t, err)
	var key repowif.Key
	for _, candidate := range keys {
		if candidate.Organization == org && candidate.DataSource == ds {
			key = candidate
		}
	}
	require.NotZero(t, key.ID)
	require.NoError(t, repo.LockConnection(ctx, tx, org, ds))
	binding, err := repo.Get(ctx, tx, key)
	require.NoError(t, err)
	require.Equal(t, org, binding.Organization)
	require.NoError(t, repo.SetState(ctx, tx, binding, "ready"))
	denied := func(statement string) {
		t.Helper()
		_, err := tx.Exec(ctx, "SAVEPOINT denied")
		require.NoError(t, err)
		_, err = tx.Exec(ctx, statement)
		require.ErrorContains(t, err, "permission denied")
		_, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT denied")
		require.NoError(t, err)
	}
	denied(`SELECT encrypted_payload FROM credentials`)
	denied(`UPDATE data_sources SET config='{}'`)
	denied(`INSERT INTO snowflake_wif_bindings(organization_id,data_source_id,issuer,account) VALUES (0,0,'https://issuer.test','account')`)
	_, err = tx.Exec(ctx, `RESET ROLE; CREATE ROLE wif_fixture_wif_executor_test NOLOGIN; GRANT USAGE ON SCHEMA public TO wif_fixture_wif_executor_test; SET LOCAL ROLE wif_fixture_wif_executor_test`)
	require.NoError(t, err)
	denied(`SELECT snowflake_wif_ready('a','b','c','https://issuer.test')`)
	_, err = tx.Exec(ctx, `RESET ROLE; GRANT EXECUTE ON FUNCTION snowflake_wif_ready(TEXT,TEXT,TEXT,TEXT) TO wif_fixture_wif_executor_test; SET LOCAL ROLE wif_fixture_wif_executor_test`)
	require.NoError(t, err)
	ready, err := repo.Ready(ctx, tx, org, ds, binding.Issuer, binding.Account)
	require.NoError(t, err)
	require.True(t, ready)
	ready, err = repo.Ready(ctx, tx, "wrong-tenant", ds, binding.Issuer, binding.Account)
	require.NoError(t, err)
	require.False(t, ready)
	denied(`SELECT * FROM snowflake_wif_bindings`)
	denied(`DELETE FROM snowflake_wif_bindings`)
	denied(`SELECT encrypted_payload FROM credentials`)
	denied(`SELECT config FROM data_sources`)
}
