package e2e

import (
	"os"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/stretchr/testify/require"
)

func TestRiverUpgradePreservesQueuedAndRunningJobs(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	pool := e2edb.OpenIsolatedPostgresPool(t, "River upgrade")
	ctx := t.Context()
	// Reconstruct River's v6 shape in this isolated database, then apply the forward migration.
	_, err := pool.Exec(ctx, `
DROP TABLE river_notification;
CREATE TABLE river_client(id text PRIMARY KEY);
CREATE TABLE river_client_queue(river_client_id text REFERENCES river_client(id));
ALTER TABLE river_job ALTER COLUMN max_attempts DROP DEFAULT;
INSERT INTO river_job(kind,queue,max_attempts,args,state,attempt,attempted_at) VALUES
('upgrade_fixture','upgrade_fixture',3,'{"synthetic":true}','available',0,NULL),
('upgrade_fixture','upgrade_fixture',3,'{"synthetic":true}','running',1,now());
CREATE TABLE river_upgrade_snapshot AS SELECT * FROM river_job;
ALTER TABLE river_queue ALTER COLUMN updated_at DROP DEFAULT;
DELETE FROM river_migration WHERE line='main' AND version IN (7,8);
`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../migrations/000004_upgrade_river_to_0_51.up.sql")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(migration))
	require.NoError(t, err)
	var differences int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_upgrade_snapshot b LEFT JOIN river_job j USING(id) WHERE to_jsonb(b) IS DISTINCT FROM to_jsonb(j)`).Scan(&differences))
	require.Zero(t, differences, "upgrade must preserve retained queue ownership and payloads")
	var versions []int
	require.NoError(t, pool.QueryRow(ctx, `SELECT array_agg(version ORDER BY version) FROM river_migration WHERE line='main'`).Scan(&versions))
	require.Equal(t, []int{1, 2, 3, 4, 5, 6, 7, 8}, versions)
	var removed bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('river_client') IS NULL AND to_regclass('river_client_queue') IS NULL AND to_regclass('river_notification') IS NOT NULL`).Scan(&removed))
	require.True(t, removed)
	_, err = pool.Exec(ctx, `INSERT INTO river_job(kind,queue,args) VALUES('upgrade_fixture','upgrade_fixture','{}'); INSERT INTO river_queue(name) VALUES('upgrade_fixture')`)
	require.NoError(t, err, "new defaults must support River inserts")
}
