package e2edb

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// #nosec G101 -- local-only e2e database default; DATABASE_URL overrides it.
const defaultPostgresDatabaseURL = "postgres://postgres:postgres@localhost:35432/braidplane?sslmode=disable"

type PostgresConfig struct {
	DatabaseURL string
}

func PostgresConfigFromEnv() PostgresConfig {
	cfg := PostgresConfig{DatabaseURL: defaultPostgresDatabaseURL}
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		cfg.DatabaseURL = databaseURL
	}
	return cfg
}

func OpenPostgresPool(t testing.TB, testName string) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, PostgresConfigFromEnv().DatabaseURL)
	if err != nil {
		t.Fatalf("real-Postgres %s infrastructure setup failed: %v", testName, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("real-Postgres %s infrastructure setup failed: %v", testName, err)
	}

	t.Cleanup(pool.Close)
	return pool
}

// OpenIsolatedPostgresPool creates a fresh migrated database on the package-owned fixture server.
func OpenIsolatedPostgresPool(t testing.TB, testName string) *pgxpool.Pool {
	t.Helper()
	admin := OpenPostgresPool(t, testName+" admin")
	database := "braidplane_test_" + strings.ToLower(rand.Text())
	identifier := pgx.Identifier{database}.Sanitize()
	_, err := admin.Exec(context.Background(), "CREATE DATABASE "+identifier)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		require.NoError(t, err)
	})
	target, err := url.Parse(PostgresConfigFromEnv().DatabaseURL)
	require.NoError(t, err)
	target.Path = "/" + database
	selected, err := postgres.MigrationDatabaseURL(target.String(), "core_schema_migrations")
	require.NoError(t, err)
	catalog, err := postgres.EmbeddedMigrationCatalog()
	require.NoError(t, err)
	_, err = postgres.ApplyMigrationCatalog(selected, catalog)
	require.NoError(t, err)
	pool, err := pgxpool.New(context.Background(), target.String())
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}
