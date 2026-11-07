package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestSelfHostedRuntimeGrantsDenySchemaAndLedgerChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	pool := e2edb.OpenPostgresPool(t, "self-hosted runtime grants")
	suffix := fmt.Sprint(time.Now().UnixNano())
	runtimeRole, migratorRole := "runtime_"+suffix, "migrator_"+suffix
	runtimeID, migratorID := pgx.Identifier{runtimeRole}.Sanitize(), pgx.Identifier{migratorRole}.Sanitize()
	conn, err := pool.Acquire(t.Context())
	require.NoError(t, err)
	defer conn.Release()
	_, err = conn.Exec(t.Context(), "CREATE ROLE "+runtimeID+"; CREATE ROLE "+migratorID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DROP OWNED BY "+runtimeID+", "+migratorID+"; DROP ROLE "+runtimeID+", "+migratorID)
		require.NoError(t, err)
	})
	script, err := os.ReadFile("../../scripts/self-hosted-runtime-grants.sql")
	require.NoError(t, err)
	sql := strings.NewReplacer(`:'runtime_role'`, "'"+runtimeRole+"'", `:'migrator_role'`, "'"+migratorRole+"'", `:"runtime_role"`, runtimeID, `:"migrator_role"`, migratorID).Replace(string(script))
	_, err = conn.Exec(t.Context(), sql)
	require.NoError(t, err)
	_, err = conn.Exec(t.Context(), sql)
	require.NoError(t, err, "grant script must be repeatable")
	_, err = conn.Exec(t.Context(), "GRANT "+migratorID+" TO "+runtimeID)
	require.NoError(t, err)
	_, err = conn.Exec(t.Context(), sql)
	require.ErrorContains(t, err, "no inherited memberships")
	_, err = conn.Exec(t.Context(), "ROLLBACK; REVOKE "+migratorID+" FROM "+runtimeID)
	require.NoError(t, err)
	_, err = conn.Exec(t.Context(), "SET ROLE "+migratorID+"; CREATE TABLE public.fixture_runtime_grants (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, value text); RESET ROLE")
	require.NoError(t, err)
	_, err = conn.Exec(t.Context(), "SET ROLE "+runtimeID)
	require.NoError(t, err)
	defer func() { _, err := conn.Exec(context.Background(), "RESET ROLE"); require.NoError(t, err) }()
	_, err = conn.Exec(t.Context(), "INSERT INTO fixture_runtime_grants(value) VALUES ('synthetic'); UPDATE fixture_runtime_grants SET value='updated'; DELETE FROM fixture_runtime_grants")
	require.NoError(t, err)
	version, err := postgres.LatestEmbeddedMigrationVersion()
	require.NoError(t, err)
	preflight, err := postgres.NewMigrationPreflight(conn, "core_schema_migrations", version)
	require.NoError(t, err)
	require.NoError(t, preflight.Check(t.Context()), "runtime role must support startup schema preflight")
	for _, denied := range []string{
		"CREATE TABLE public.fixture_forbidden(id int)",
		"ALTER TABLE public.fixture_runtime_grants ADD COLUMN forbidden text",
		"DELETE FROM public.core_schema_migrations",
	} {
		_, err := conn.Exec(t.Context(), denied)
		require.Error(t, err, denied)
	}
}
