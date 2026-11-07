package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

//go:embed migrations/*.up.sql
var embeddedMigrationFiles embed.FS

const (
	migrationReasonMetadataMissing = "migration metadata missing"
	migrationReasonDirty           = "migration dirty"
	migrationReasonBehind          = "database migration version behind release"
	migrationReasonAhead           = "database migration version ahead of release"
)

// MigrationState is the database migration state recorded by golang-migrate.
type MigrationState struct {
	Present bool
	Version uint
	Dirty   bool
}

// MigrationPreflightStatus describes whether a database matches this release's
// migration contract without exposing database connection or query details.
type MigrationPreflightStatus struct {
	Ready           bool
	CurrentVersion  uint
	RequiredVersion uint
	Reason          string
}

// MigrationStateReader is the narrow pgx query boundary needed by migration
// preflight checks.
type MigrationStateReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// MigrationPreflight checks that the target database schema matches the running
// release before the application serves traffic.
type MigrationPreflight struct {
	database        MigrationStateReader
	ledger          string
	requiredVersion uint
}

// NewMigrationPreflight checks one explicitly selected release catalog and ledger.
func NewMigrationPreflight(database MigrationStateReader, ledger string, requiredVersion uint) (*MigrationPreflight, error) {
	if database == nil || requiredVersion == 0 {
		return nil, xerror.Unexpected().Detail("migration database and release version are required")
	}
	if err := validateMigrationLedger(ledger); err != nil {
		return nil, err
	}

	return &MigrationPreflight{
		database:        database,
		ledger:          ledger,
		requiredVersion: requiredVersion,
	}, nil
}

// Check verifies that the database migration state is safe for this release.
func (p *MigrationPreflight) Check(ctx context.Context) error {
	_, err := p.Status(ctx)
	return err
}

// Status loads and evaluates migration state for operator startup and readiness
// checks.
func (p *MigrationPreflight) Status(ctx context.Context) (MigrationPreflightStatus, error) {
	if p == nil || p.database == nil {
		return MigrationPreflightStatus{}, xerror.Unexpected().Detail(
			"migration preflight database is required",
		)
	}

	state, err := ReadMigrationState(ctx, p.database, p.ledger)
	if err != nil {
		return MigrationPreflightStatus{}, err
	}

	return EvaluateMigrationPreflight(state, p.requiredVersion)
}

// LatestEmbeddedMigrationVersion returns the highest up migration version
// embedded in the running binary.
func LatestEmbeddedMigrationVersion() (uint, error) {
	names, err := fs.Glob(embeddedMigrationFiles, "migrations/*.up.sql")
	if err != nil {
		return 0, xerror.Unexpected().With(err)
	}

	return LatestMigrationVersion(names)
}

// LatestMigrationVersion returns the highest numeric version from up migration
// filenames.
func LatestMigrationVersion(migrationNames []string) (uint, error) {
	var latest uint64
	found := false

	for _, name := range migrationNames {
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}

		version, err := migrationVersion(name)
		if err != nil {
			return 0, err
		}
		if !found || version > latest {
			latest = version
			found = true
		}
	}

	if !found {
		return 0, xerror.Unexpected().Detail("at least one up migration is required")
	}

	return uint(latest), nil
}

// ReadMigrationState reads golang-migrate metadata without exposing migration
// table details outside the postgres package.
func ReadMigrationState(
	ctx context.Context,
	database MigrationStateReader,
	ledger string,
) (MigrationState, error) {
	if err := validateMigrationLedger(ledger); err != nil {
		return MigrationState{}, err
	}
	identifier := pgx.Identifier{"public", ledger}.Sanitize()
	var present bool
	if err := database.QueryRow(ctx, `SELECT pg_catalog.to_regclass($1) IS NOT NULL`, identifier).Scan(&present); err != nil {
		return MigrationState{}, xerror.Unexpected().With(err)
	}
	if !present {
		return MigrationState{Present: false}, nil
	}

	var state MigrationState
	var version int64
	var rows int64
	state.Present = true
	if err := database.QueryRow(ctx, "SELECT version, dirty, count(*) OVER () FROM "+identifier+" LIMIT 1").Scan(&version, &state.Dirty, &rows); err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return MigrationState{Present: false}, nil
		}
		return MigrationState{}, xerror.Unexpected().With(err)
	}
	if rows != 1 {
		return MigrationState{}, xerror.Unexpected().Detail("migration ledger must contain exactly one version row")
	}
	if version < 0 {
		return MigrationState{}, xerror.Unexpected().Detailf(
			"database migration version %d is invalid",
			version,
		)
	}
	state.Version = uint(version)

	return state, nil
}

var migrationLedgerPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

func validateMigrationLedger(ledger string) error {
	if !migrationLedgerPattern.MatchString(ledger) {
		return xerror.ValidationFailed().Detail("migration ledger must be a lowercase PostgreSQL table name of at most 63 characters")
	}
	return nil
}

// MigrationDatabaseURL selects the same public ledger used by readiness checks.
func MigrationDatabaseURL(databaseURL, ledger string) (string, error) {
	if err := validateMigrationLedger(ledger); err != nil {
		return "", err
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", xerror.ValidationFailed().Detail("invalid migration database URL")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", xerror.ValidationFailed().Detail("migration database URL must use PostgreSQL")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", xerror.ValidationFailed().Detail("invalid migration database URL query")
	}
	query.Set("x-migrations-table", pgx.Identifier{"public", ledger}.Sanitize())
	query.Set("x-migrations-table-quoted", "true")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// EvaluateMigrationPreflight compares database migration metadata to the
// release-required version and rejects every ambiguous state.
func EvaluateMigrationPreflight(
	state MigrationState,
	requiredVersion uint,
) (MigrationPreflightStatus, error) {
	status := MigrationPreflightStatus{
		CurrentVersion:  state.Version,
		RequiredVersion: requiredVersion,
	}

	switch {
	case !state.Present:
		status.Reason = migrationReasonMetadataMissing
		return status, xerror.Unexpected().Detail("migration metadata table is missing")
	case state.Dirty:
		status.Reason = migrationReasonDirty
		return status, xerror.Unexpected().Detailf(
			"database migration version %d is dirty",
			state.Version,
		)
	case state.Version < requiredVersion:
		status.Reason = migrationReasonBehind
		return status, xerror.Unexpected().Detailf(
			"database migration version %d is behind required version %d",
			state.Version,
			requiredVersion,
		)
	case state.Version > requiredVersion:
		status.Reason = migrationReasonAhead
		return status, xerror.Unexpected().Detailf(
			"database migration version %d is ahead of required version %d",
			state.Version,
			requiredVersion,
		)
	default:
		status.Ready = true
		return status, nil
	}
}

// migrationVersion parses the leading numeric migration version from a
// golang-migrate filename.
func migrationVersion(name string) (uint64, error) {
	base := filepath.Base(name)
	prefix, _, ok := strings.Cut(base, "_")
	if !ok || prefix == "" {
		return 0, xerror.Unexpected().Detailf(
			"migration filename must start with a numeric version: %s",
			base,
		)
	}

	version, err := strconv.ParseUint(prefix, 10, 0)
	if err != nil {
		return 0, xerror.Unexpected().With(err).Detail(fmt.Sprintf("migration filename must start with a numeric version: %s", base))
	}

	return version, nil
}
