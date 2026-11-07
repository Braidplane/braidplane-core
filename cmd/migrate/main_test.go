package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/config"
)

func TestCatalogReportsEmbeddedIdentityWithoutConfig(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	require.NoError(t, run([]string{"catalog"}, fakeEnv(map[string]string{"CONFIG_PATH": "/missing/config"}), &output))
	var identity struct {
		Version uint   `json:"version"`
		Digest  string `json:"digest"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &identity))
	manifest, err := os.ReadFile("../../postgres/migrations/manifest.sha256")
	require.NoError(t, err)
	assert.Equal(t, string(manifest), fmt.Sprintf("%d %s\n", identity.Version, identity.Digest))
}

func TestCatalogWrapsOutputFailure(t *testing.T) {
	t.Parallel()
	reader, writer := io.Pipe()
	require.NoError(t, reader.Close())
	t.Cleanup(func() { require.NoError(t, writer.Close()) })
	err := (&catalogCommand{}).Run(&migrateRuntime{stdout: writer})
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.ErrorContains(t, err, "write migration catalog")
}

// TestRunRejectsMissingCommand verifies Kong reports the narrow command surface.
func TestRunRejectsMissingCommand(t *testing.T) {
	t.Parallel()

	err := run(nil, fakeEnv(nil), &bytes.Buffer{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse migration command")
}

// TestRunRejectsUnsupportedCommand rejects unsupported migration operations.
func TestRunRejectsUnsupportedCommand(t *testing.T) {
	t.Parallel()

	err := run([]string{"down"}, fakeEnv(nil), &bytes.Buffer{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse migration command")
	assert.Contains(t, err.Error(), "down")
}

// TestRunUpCommandRequiresConfigFile prevents running migrations without runtime config.
func TestRunUpCommandRequiresConfigFile(t *testing.T) {
	t.Parallel()

	err := run([]string{"up"}, fakeEnv(nil), &bytes.Buffer{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read config file")
}

// TestLoadMigrateRuntimeConfigUsesSharedPrecedence proves migration config uses shared precedence.
func TestLoadMigrateRuntimeConfigUsesSharedPrecedence(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	writeMigrateConfig(t, configPath, `[deployment]
profile = "local"

[database]
host = "toml-db.example.test"
port = 15430
user = "toml_user"
password = "toml-password"
database = "toml_db"
ssl_mode = "disable"
`)
	clearMigrateConfigEnv(t)
	t.Setenv("DB_HOST", "env-db.example.test")
	t.Setenv("DB_USER", "env_user")
	t.Setenv("DB_PASSWORD", "env-password")

	runtimeCfg, err := loadMigrateRuntimeConfig(configPath, []string{
		"--db-host=flag-db.example.test",
		"--db-port",
		"15432",
	})
	require.NoError(t, err)
	cfg, err := buildUpConfig("", "schema_migrations", runtimeCfg)
	require.NoError(t, err)

	parsed, err := url.Parse(cfg.databaseURL)
	require.NoError(t, err)
	password, hasPassword := parsed.User.Password()
	assert.True(t, hasPassword)
	assert.Equal(t, "env-password", password)
	assert.Equal(t, "env_user", parsed.User.Username())
	assert.Equal(t, "flag-db.example.test:15432", parsed.Host)
	assert.Equal(t, "/toml_db", parsed.Path)
	assert.Equal(t, "disable", parsed.Query().Get("sslmode"))
	assert.NotContains(t, cfg.databaseURL, "pool_max_conns")
	assert.Equal(t, filepath.Clean(defaultMigrationsPath), cfg.migrationsPath)
}

// TestBuildUpConfigDefaultsToBundledMigrations pins the release-image path.
func TestBuildUpConfigDefaultsToBundledMigrations(t *testing.T) {
	t.Parallel()

	runtimeCfg := testMigrateRuntimeConfig()
	cfg, err := buildUpConfig(defaultMigrationsPath, "schema_migrations", runtimeCfg)

	require.NoError(t, err)
	parsed, err := url.Parse(cfg.databaseURL)
	require.NoError(t, err)
	assert.Equal(t, `"public"."schema_migrations"`, parsed.Query().Get("x-migrations-table"))
	assert.Equal(t, "true", parsed.Query().Get("x-migrations-table-quoted"))
	assert.Equal(t, filepath.Clean(defaultMigrationsPath), cfg.migrationsPath)
}

// TestRunUpCommandParsesExplicitMigrationPath supports disposable DB validation.
func TestRunUpCommandParsesExplicitMigrationPath(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfg, err := buildUpConfig(tmpDir, "schema_migrations", testMigrateRuntimeConfig())

	require.NoError(t, err)
	assert.Equal(t, tmpDir, cfg.migrationsPath)
}

func TestBuildUpConfigSelectsCatalogLedger(t *testing.T) {
	t.Parallel()
	for _, ledger := range []string{"core_schema_migrations", "managed_schema_migrations"} {
		t.Run(ledger, func(t *testing.T) {
			t.Parallel()
			cfg, err := buildUpConfig("/release/migrations", ledger, testMigrateRuntimeConfig())
			require.NoError(t, err)
			u, err := url.Parse(cfg.databaseURL)
			require.NoError(t, err)
			assert.Equal(t, `"public"."`+ledger+`"`, u.Query().Get("x-migrations-table"))
		})
	}
	_, err := buildUpConfig("/release/migrations", "public.core_schema_migrations", testMigrateRuntimeConfig())
	require.Error(t, err)
}

func TestUpHelpDocumentsLedgerSelection(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	require.NoError(t, run([]string{"up", "--help"}, fakeEnv(nil), &output))
	assert.Contains(t, output.String(), "--ledger")
}

// TestSplitMigrateArgsKeepsCommandFlags verifies config flags do not leak into Kong parsing.
func TestSplitMigrateArgsKeepsCommandFlags(t *testing.T) {
	t.Parallel()

	configArgs, commandArgs, err := splitMigrateArgs([]string{
		"--db-host",
		"flag-db.example.test",
		"up",
		"--path",
		"/tmp/migrations",
		"--ledger",
		"core_schema_migrations",
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"--db-host", "flag-db.example.test"}, configArgs)
	assert.Equal(t, []string{"up", "--path", "/tmp/migrations", "--ledger", "core_schema_migrations"}, commandArgs)
}

// fakeEnv creates a deterministic environment lookup for command tests.
func fakeEnv(values map[string]string) envLookup {
	return func(key string) string {
		return strings.TrimSpace(values[key])
	}
}

// testMigrateRuntimeConfig returns a complete database config for migration unit tests.
func testMigrateRuntimeConfig() *migrateRuntimeConfig {
	return &migrateRuntimeConfig{
		Deployment: config.DeploymentConfig{Profile: config.DeploymentProfileLocal},
		Database:   configDatabaseForTest(),
	}
}

// configDatabaseForTest keeps expected migration URLs readable in tests.
func configDatabaseForTest() config.DatabaseConfig {
	return config.DatabaseConfig{
		Host:     "example.com",
		Port:     5432,
		User:     "user",
		Password: "password",
		Database: "braidplane",
		SSLMode:  "require",
	}
}

// clearMigrateConfigEnv removes shared config environment overrides from command tests.
func clearMigrateConfigEnv(t *testing.T) {
	t.Helper()

	for _, envVar := range []string{
		"DEPLOYMENT_PROFILE",
		"DB_HOST",
		"DB_PORT",
		"DB_USER",
		"DB_PASSWORD",
		"DB_DATABASE",
		"DB_SSL_MODE",
		"DB_MAX_CONNS",
		"DB_MIN_CONNS",
		"DB_MAX_CONN_LIFETIME",
		"DB_MAX_CONN_IDLE_TIME",
		"DB_HEALTH_CHECK_PERIOD",
	} {
		t.Setenv(envVar, "")
	}
}

// writeMigrateConfig writes a TOML fixture for migration config tests.
func writeMigrateConfig(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
