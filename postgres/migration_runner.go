package postgres

import (
	"context"
	"io/fs"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	migrate "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// EmbeddedMigrationCatalog returns the core catalog compiled into this module version.
func EmbeddedMigrationCatalog() (fs.FS, error) {
	files, err := fs.Sub(embeddedMigrationFiles, "migrations")
	if err != nil {
		return nil, xerror.Wrapf("open embedded core catalog: %w", err)
	}
	return files, nil
}

// ApplyMigrationCatalog applies one catalog to an explicitly selected ledger URL.
// Construct databaseURL with MigrationDatabaseURL before calling this function.
func ApplyMigrationCatalog(databaseURL string, catalog fs.FS) (bool, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Query().Get("x-migrations-table") == "" {
		return false, xerror.ValidationFailed().Detail("an explicit migration ledger database URL is required")
	}
	if catalog == nil {
		return false, xerror.ValidationFailed().Detail("migration catalog is required")
	}
	source, err := iofs.New(catalog, ".")
	if err != nil {
		return false, xerror.Wrapf("open migration catalog: %w", err)
	}
	if err := guardLegacyMigrationTarget(parsed); err != nil {
		_ = source.Close()
		return false, err
	}
	runner, err := migrate.NewWithSourceInstance("iofs", source, databaseURL)
	if err != nil {
		_ = source.Close()
		return false, xerror.Unexpected().Detailf("initialize migration runner: %s", sanitizeMigrationError(err, databaseURL))
	}
	defer func() { _, _ = runner.Close() }()
	if err := runner.Up(); err != nil {
		if xerror.Is(err, migrate.ErrNoChange) {
			return false, nil
		}
		return false, xerror.Unexpected().Detailf("apply migrations: %s", sanitizeMigrationError(err, databaseURL))
	}
	return true, nil
}

// sanitizeMigrationError removes the configured database URL and password from
// dependency errors before they reach operator logs.
func sanitizeMigrationError(err error, databaseURL string) string {
	message := err.Error()
	if databaseURL != "" {
		message = strings.ReplaceAll(message, databaseURL, "<redacted database url>")
	}
	parsed, parseErr := url.Parse(databaseURL)
	if parseErr == nil && parsed.User != nil {
		if password, ok := parsed.User.Password(); ok && password != "" {
			message = strings.ReplaceAll(message, password, "<redacted>")
		}
	}
	return message
}

// guardLegacyMigrationTarget refuses a new ledger before the migrate driver can create it.
func guardLegacyMigrationTarget(selected *url.URL) error {
	query := selected.Query()
	table := query.Get("x-migrations-table")
	const prefix = `"public"."`
	if !strings.HasPrefix(table, prefix) || !strings.HasSuffix(table, `"`) || query.Get("x-migrations-table-quoted") != "true" {
		return xerror.ValidationFailed().Detail("migration ledger URL must use an explicitly quoted public table")
	}
	ledger := strings.TrimSuffix(strings.TrimPrefix(table, prefix), `"`)
	if err := validateMigrationLedger(ledger); err != nil {
		return err
	}
	if ledger == "schema_migrations" {
		return nil
	}
	plain := *selected
	query.Del("x-migrations-table")
	query.Del("x-migrations-table-quoted")
	plain.RawQuery = query.Encode()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, plain.String())
	if err != nil {
		return xerror.Unexpected().Detailf("inspect migration target: %s", sanitizeMigrationError(err, plain.String()))
	}
	defer func() { _ = connection.Close(ctx) }()
	var legacyPresent bool
	if err := connection.QueryRow(ctx, `SELECT pg_catalog.to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&legacyPresent); err != nil {
		return xerror.Unexpected().Detailf("inspect migration target metadata: %s", sanitizeMigrationError(err, plain.String()))
	}
	if !legacyPresent {
		return nil
	}
	old, err := ReadMigrationState(ctx, connection, "schema_migrations")
	if err != nil {
		return xerror.Wrapf("inspect legacy migration metadata: %w", err)
	}
	current, err := ReadMigrationState(ctx, connection, ledger)
	if err != nil {
		return xerror.Wrapf("inspect selected migration metadata: %w", err)
	}
	if !old.Present || old.Version == 0 || old.Dirty || !current.Present || current.Version == 0 || current.Dirty {
		return xerror.ValidationFailed().Detail("legacy migration metadata requires verified baseline adoption before applying this catalog")
	}
	return nil
}
