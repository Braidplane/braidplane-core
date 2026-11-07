package e2e

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	migrate "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
)

func TestMigrationTracksKeepIndependentPublicLedgers(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	admin := e2edb.OpenPostgresPool(t, "migration tracks")
	database := fmt.Sprintf("migration_tracks_tracks_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{database}.Sanitize()
	_, err := admin.Exec(t.Context(), "CREATE DATABASE "+identifier)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		require.NoError(t, err)
	})
	cfg := admin.Config().Copy()
	cfg.ConnConfig.Database = database
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `
CREATE SCHEMA shadow;
CREATE TABLE public.schema_migrations(version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL);
INSERT INTO public.schema_migrations VALUES (74, false);`)
	require.NoError(t, err)

	apply := func(ledger string, files map[string]string) {
		t.Helper()
		directory := t.TempDir()
		for name, sql := range files {
			require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(sql), 0o600))
		}
		// The driver's connection search path must not redirect its ledger.
		u, err := url.Parse(e2edb.PostgresConfigFromEnv().DatabaseURL)
		require.NoError(t, err)
		u.Path = database
		query := u.Query()
		query.Set("search_path", "shadow,public")
		u.RawQuery = query.Encode()
		selected, err := postgres.MigrationDatabaseURL(u.String(), ledger)
		require.NoError(t, err)
		runner, err := migrate.New("file://"+filepath.ToSlash(directory), selected)
		require.NoError(t, err)
		t.Cleanup(func() {
			sourceErr, databaseErr := runner.Close()
			require.NoError(t, sourceErr)
			require.NoError(t, databaseErr)
		})
		require.NoError(t, runner.Up())
		require.ErrorIs(t, runner.Up(), migrate.ErrNoChange)
	}
	core := map[string]string{
		"000001_core.up.sql": "CREATE TABLE public.core_fixture(value text); INSERT INTO public.core_fixture VALUES ('preserved');",
	}
	apply("core_schema_migrations", core)
	apply("managed_schema_migrations", map[string]string{
		"000001_managed.up.sql": "CREATE TABLE public.managed_fixture(value text); INSERT INTO public.managed_fixture SELECT value FROM public.core_fixture;",
	})
	core["000002_core.up.sql"] = "ALTER TABLE public.core_fixture ADD COLUMN revision integer NOT NULL DEFAULT 2;"
	apply("core_schema_migrations", core)

	for ledger, version := range map[string]uint{"schema_migrations": 74, "core_schema_migrations": 2, "managed_schema_migrations": 1} {
		preflight, err := postgres.NewMigrationPreflight(pool, ledger, version)
		require.NoError(t, err)
		require.NoError(t, preflight.Check(t.Context()), ledger)
	}
	var misplaced bool
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT to_regclass('shadow.core_schema_migrations') IS NOT NULL OR to_regclass('shadow.managed_schema_migrations') IS NOT NULL").Scan(&misplaced))
	require.False(t, misplaced)
	var value string
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT value FROM public.managed_fixture").Scan(&value))
	require.Equal(t, "preserved", value)
}
