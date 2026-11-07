package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Braidplane/braidplane-core/config"
	"github.com/Braidplane/braidplane-core/pkg/kongcmd"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
)

const (
	defaultMigrationsPath = "/postgres/migrations"
	configPathEnv         = "CONFIG_PATH"
	defaultConfigPath     = "config.toml"
)

type migrateConfig struct {
	databaseURL    string
	migrationsPath string
}

type migrateRuntimeConfig struct {
	Deployment config.DeploymentConfig `toml:"deployment"`
	Database   config.DatabaseConfig   `toml:"database"`
}

type envLookup func(string) string

type migrateCLI struct {
	Up      upCommand      `cmd:"" help:"Apply pending database migrations from the bundled release directory."`
	Catalog catalogCommand `cmd:"" help:"Print the embedded migration catalog identity without connecting to a database."`
}

type catalogCommand struct{}

func (c *catalogCommand) Run(runtime *migrateRuntime) error {
	version, digest, err := postgres.EmbeddedMigrationCatalogIdentity()
	if err != nil {
		return err
	}
	if err := json.NewEncoder(runtime.stdout).Encode(struct {
		Version uint   `json:"version"`
		Digest  string `json:"digest"`
	}{version, digest}); err != nil {
		return xerror.Wrapf("write migration catalog: %w", err)
	}
	return nil
}

type upCommand struct {
	Path   string `default:"/postgres/migrations" help:"Migration directory bundled into the release image."`
	Ledger string `default:"core_schema_migrations" help:"Public PostgreSQL migration ledger for this catalog."`
}

type migrateRuntime struct {
	configPath string
	configArgs []string
	stdout     io.Writer
}

// main runs the release migration command and keeps secrets out of stderr.
func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "braidplane-migrate: %v\n", err)
		os.Exit(1)
	}
}

// run parses the operator command and applies the requested migration action.
func run(args []string, getenv envLookup, stdout io.Writer) error {
	return runWithStderr(args, getenv, stdout, io.Discard)
}

// runWithStderr configures Kong and converts parse exits into repository errors.
func runWithStderr(args []string, getenv envLookup, stdout, stderr io.Writer) error {
	configArgs, commandArgs, err := splitMigrateArgs(args)
	if err != nil {
		return xerror.ValidationFailed().Detailf("parse migration config flags: %w", err)
	}

	result, err := kongcmd.Parse(
		commandArgs,
		migrateCLI{},
		"braidplane-migrate",
		"Run Braidplane release database migrations.",
		stdout,
		stderr,
		func(code int) error {
			return xerror.ValidationFailed().Detailf("migration command exited with status %d", code)
		},
	)
	if result.Exited {
		return err
	}
	if err != nil {
		return xerror.ValidationFailed().Detailf("parse migration command: %w", err)
	}

	if err := result.Context.Run(&migrateRuntime{
		configPath: resolveConfigPath(getenv),
		configArgs: configArgs,
		stdout:     stdout,
	}); err != nil {
		return xerror.Wrapf("run migration command: %w", err)
	}
	return nil
}

// Run validates the up command configuration and applies pending migrations.
func (c *upCommand) Run(runtime *migrateRuntime) error {
	runtimeCfg, err := loadMigrateRuntimeConfig(runtime.configPath, runtime.configArgs)
	if err != nil {
		return err
	}
	cfg, err := buildUpConfig(c.Path, c.Ledger, runtimeCfg)
	if err != nil {
		return err
	}
	return runUp(cfg, runtime.stdout)
}

// splitMigrateArgs separates shared config flags from Kong-owned command arguments.
func splitMigrateArgs(args []string) ([]string, []string, error) {
	cfg := defaultMigrateRuntimeConfig()
	return config.ExtractFlagArgs(cfg, args)
}

// resolveConfigPath returns the TOML config file path used by the migration command.
func resolveConfigPath(getenv envLookup) string {
	if value := strings.TrimSpace(getenv(configPathEnv)); value != "" {
		return value
	}
	return defaultConfigPath
}

// defaultMigrateRuntimeConfig returns migration defaults before TOML, env, and flags apply.
func defaultMigrateRuntimeConfig() *migrateRuntimeConfig {
	return &migrateRuntimeConfig{
		Deployment: config.DeploymentConfig{
			Profile: config.DeploymentProfileLocal,
		},
		Database: config.DefaultDatabaseConfig(),
	}
}

// loadMigrateRuntimeConfig loads migration database settings through the shared config package.
func loadMigrateRuntimeConfig(path string, args []string) (*migrateRuntimeConfig, error) {
	cfg := defaultMigrateRuntimeConfig()
	if err := config.LoadIntoWithArgs(path, cfg, args); err != nil {
		return nil, err
	}
	if err := validateMigrateRuntimeConfig(cfg); err != nil {
		return nil, xerror.ValidationFailed().Detailf("invalid migration configuration: %w", err)
	}
	return cfg, nil
}

// validateMigrateRuntimeConfig rejects incomplete database settings before migration execution.
func validateMigrateRuntimeConfig(cfg *migrateRuntimeConfig) error {
	if err := config.ValidateDeploymentProfile(&cfg.Deployment.Profile); err != nil {
		return err
	}
	cfg.Database.Host = strings.TrimSpace(cfg.Database.Host)
	cfg.Database.User = strings.TrimSpace(cfg.Database.User)
	cfg.Database.Database = strings.TrimSpace(cfg.Database.Database)
	cfg.Database.SSLMode = strings.TrimSpace(cfg.Database.SSLMode)
	if cfg.Database.Host == "" {
		return xerror.ValidationFailed().Detail("database.host is required")
	}
	if cfg.Database.Port <= 0 {
		return xerror.ValidationFailed().Detail("database.port must be greater than 0")
	}
	if cfg.Database.User == "" {
		return xerror.ValidationFailed().Detail("database.user is required")
	}
	if cfg.Database.Database == "" {
		return xerror.ValidationFailed().Detail("database.database is required")
	}
	if cfg.Database.SSLMode == "" {
		return xerror.ValidationFailed().Detail("database.ssl_mode is required")
	}
	if config.IsNonLocalDeploymentProfile(cfg.Deployment.Profile) {
		if err := config.ValidateDeploymentPostgres(
			cfg.Deployment.Profile,
			cfg.Database.Host,
			cfg.Database.User,
			cfg.Database.Password,
			cfg.Database.SSLMode,
		); err != nil {
			return err
		}
	}
	return nil
}

// buildUpConfig validates the migration path and derives the database URL from shared config.
func buildUpConfig(migrationsPath, ledger string, runtimeCfg *migrateRuntimeConfig) (migrateConfig, error) {
	databaseURL, err := postgres.MigrationDatabaseURL(runtimeCfg.Database.ConnectionURL(), ledger)
	if err != nil {
		return migrateConfig{}, err
	}
	cfg := migrateConfig{
		databaseURL:    databaseURL,
		migrationsPath: migrationsPath,
	}
	if strings.TrimSpace(cfg.migrationsPath) == "" {
		cfg.migrationsPath = defaultMigrationsPath
	}

	cfg.migrationsPath = filepath.Clean(cfg.migrationsPath)
	if cfg.migrationsPath == "." || cfg.migrationsPath == string(filepath.Separator) {
		return migrateConfig{}, xerror.ValidationFailed().Detail(
			"migration path must point to the bundled release migrations",
		)
	}
	migrationsPath, err = filepath.Abs(cfg.migrationsPath)
	if err != nil {
		return migrateConfig{}, xerror.ValidationFailed().Detailf("resolve migration path: %w", err)
	}
	cfg.migrationsPath = migrationsPath
	return cfg, nil
}

// runUp applies the selected release catalog through the shared migration engine.
func runUp(cfg migrateConfig, stdout io.Writer) error {
	changed, err := postgres.ApplyMigrationCatalog(cfg.databaseURL, os.DirFS(cfg.migrationsPath))
	if err != nil {
		return err
	}
	if !changed {
		return writeOutputLine(stdout, "braidplane-migrate: database already up to date")
	}
	return writeOutputLine(stdout, "braidplane-migrate: migrations applied")
}

// writeOutputLine writes operator-visible status without ignoring stream errors.
func writeOutputLine(stdout io.Writer, message string) error {
	if _, err := fmt.Fprintln(stdout, message); err != nil {
		return xerror.Unexpected().Detailf("write migration output: %w", err)
	}
	return nil
}
