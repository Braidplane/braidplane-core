package postgres

import (
	"context"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgservicefile"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Braidplane/braidplane-core/pkg/dbpool"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	migrate "github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
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
	options, err := migrationOptions(parsed)
	if err != nil {
		return false, err
	}
	connection, err := parseMigrationConnection(parsed)
	if err != nil {
		return false, err
	}
	source, err := iofs.New(catalog, ".")
	if err != nil {
		return false, xerror.Wrapf("open migration catalog: %w", err)
	}
	if err := guardLegacyMigrationTarget(options.MigrationsTable, connection); err != nil {
		_ = source.Close()
		return false, err
	}
	db := stdlib.OpenDB(*connection)
	driver, err := migratepgx.WithInstance(db, options)
	if err != nil {
		_ = source.Close()
		_ = db.Close()
		return false, xerror.Unexpected().Detailf("initialize migration runner: %s", sanitizeMigrationError(err, databaseURL))
	}
	runner, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		_ = source.Close()
		_ = driver.Close()
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

func parseMigrationConnection(databaseURL *url.URL) (*pgx.ConnConfig, error) {
	plain := migrate.FilterCustomQuery(databaseURL)
	query := plain.Query()
	for _, key := range []string{"sslmode", "ssl", "service", "servicefile"} {
		if len(query[key]) > 1 || query.Has(key) && query.Get(key) == "" {
			return nil, xerror.ValidationFailed().Detailf("invalid migration connection setting %s", key)
		}
	}
	if query.Get("sslmode") == "" && query.Get("ssl") != "true" {
		mode, configured, err := migrationServiceTLSMode(query)
		if err != nil {
			return nil, err
		}
		if !configured {
			mode = os.Getenv("PGSSLMODE")
		}
		if mode == "" {
			mode = "require"
		}
		// Pin the selected mode so rereading the service cannot change TLS policy.
		query.Set("sslmode", mode)
	}
	// pgx decodes URL query spaces as %20.
	plain.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
	connection, err := pgx.ParseConfig(plain.String())
	if err != nil {
		return nil, xerror.ValidationFailed().Detail("invalid migration connection configuration")
	}
	if err := dbpool.RestrictAuthentication(&connection.Config); err != nil {
		return nil, err
	}
	return connection, nil
}

// migrationServiceTLSMode uses the service-file precedence of pgx.ParseConfig.
func migrationServiceTLSMode(query url.Values) (string, bool, error) {
	service := os.Getenv("PGSERVICE")
	if query.Has("service") {
		service = query.Get("service")
	}
	if service == "" {
		return "", false, nil
	}

	serviceFile := os.Getenv("PGSERVICEFILE")
	if query.Has("servicefile") {
		serviceFile = query.Get("servicefile")
	} else if serviceFile == "" {
		if home, err := os.UserHomeDir(); err == nil {
			serviceFile = filepath.Join(home, ".pg_service.conf")
		}
	}
	profiles, err := pgservicefile.ReadServicefile(serviceFile)
	if err != nil {
		return "", false, xerror.ValidationFailed().Detail("invalid migration connection service file")
	}
	profile, err := profiles.GetService(service)
	if err != nil {
		return "", false, xerror.ValidationFailed().Detail("migration connection service is missing from its service file")
	}
	mode, configured := profile.Settings["sslmode"]
	return mode, configured, nil
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

func migrationOptions(selected *url.URL) (*migratepgx.Config, error) {
	query := selected.Query()
	table := query.Get("x-migrations-table")
	const prefix = `"public"."`
	if !strings.HasPrefix(table, prefix) || !strings.HasSuffix(table, `"`) || query.Get("x-migrations-table-quoted") != "true" {
		return nil, xerror.ValidationFailed().Detail("migration ledger URL must use an explicitly quoted public table")
	}
	ledger := strings.TrimSuffix(strings.TrimPrefix(table, prefix), `"`)
	if err := validateMigrationLedger(ledger); err != nil {
		return nil, err
	}
	// The original migration driver used the URL path as part of the lock key.
	options := &migratepgx.Config{DatabaseName: selected.Path, MigrationsTable: table, MigrationsTableQuoted: true, MultiStatementMaxSize: migratepgx.DefaultMultiStatementMaxSize}
	if value := query.Get("x-statement-timeout"); value != "" {
		milliseconds, err := strconv.Atoi(value)
		if err != nil {
			return nil, xerror.ValidationFailed().Detail("invalid migration statement timeout")
		}
		options.StatementTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	if value := query.Get("x-multi-statement"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return nil, xerror.ValidationFailed().Detail("invalid migration multi-statement setting")
		}
		options.MultiStatementEnabled = enabled
	}
	if value := query.Get("x-multi-statement-max-size"); value != "" {
		size, err := strconv.Atoi(value)
		if err != nil {
			return nil, xerror.ValidationFailed().Detail("invalid migration statement size")
		}
		if size > 0 {
			options.MultiStatementMaxSize = size
		}
	}
	return options, nil
}

// guardLegacyMigrationTarget refuses a new ledger before the migrate driver can create it.
func guardLegacyMigrationTarget(table string, config *pgx.ConnConfig) error {
	ledger := strings.TrimSuffix(strings.TrimPrefix(table, `"public"."`), `"`)
	if ledger == "schema_migrations" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := pgx.ConnectConfig(ctx, config.Copy())
	if err != nil {
		return xerror.Unexpected().Detailf("inspect migration target: %s", sanitizeMigrationError(err, config.ConnString()))
	}
	defer func() { _ = connection.Close(ctx) }()
	var legacyPresent bool
	if err := connection.QueryRow(ctx, `SELECT pg_catalog.to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&legacyPresent); err != nil {
		return xerror.Unexpected().Detailf("inspect migration target metadata: %s", sanitizeMigrationError(err, config.ConnString()))
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
