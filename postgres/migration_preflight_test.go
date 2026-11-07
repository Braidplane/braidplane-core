package postgres

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationDatabaseURLSelectsPublicLedger(t *testing.T) {
	t.Parallel()
	for _, ledger := range []string{"schema_migrations", "core_schema_migrations", "managed_schema_migrations"} {
		t.Run(ledger, func(t *testing.T) {
			t.Parallel()
			raw := "postgres://user:p%40ss@example.test/app?sslmode=verify-full&sslrootcert=%2Ftmp%2Fca.pem&x-migrations-table=old"
			got, err := MigrationDatabaseURL(raw, ledger)
			require.NoError(t, err)
			u, err := url.Parse(got)
			require.NoError(t, err)
			assert.Equal(t, `"public"."`+ledger+`"`, u.Query().Get("x-migrations-table"))
			assert.Equal(t, "true", u.Query().Get("x-migrations-table-quoted"))
			assert.Equal(t, "verify-full", u.Query().Get("sslmode"))
			assert.Equal(t, "/tmp/ca.pem", u.Query().Get("sslrootcert"))
			password, ok := u.User.Password()
			assert.True(t, ok)
			assert.Equal(t, "p@ss", password)
		})
	}
}

func TestMigrationLedgerRejectsAmbiguousNamesBeforeQuery(t *testing.T) {
	t.Parallel()
	for _, ledger := range []string{"", "public.core_schema_migrations", "CORE", "1ledger", "core;DROP TABLE users", strings.Repeat("a", 64)} {
		t.Run(ledger, func(t *testing.T) {
			t.Parallel()
			_, err := MigrationDatabaseURL("postgres://example.test/app", ledger)
			require.Error(t, err)
			_, err = ReadMigrationState(t.Context(), nil, ledger)
			require.Error(t, err)
			_, err = NewMigrationPreflight(migrationTestReader(func(context.Context, string, ...any) pgx.Row {
				t.Fatal("invalid ledger must not query")
				return nil
			}), ledger, 1)
			require.Error(t, err)
		})
	}
}

func TestMigrationPreflightReadsOnlySelectedLedger(t *testing.T) {
	t.Parallel()
	for _, ledger := range []string{"core_schema_migrations", "managed_schema_migrations"} {
		t.Run(ledger, func(t *testing.T) {
			t.Parallel()
			calls := 0
			reader := migrationTestReader(func(_ context.Context, query string, args ...any) pgx.Row {
				calls++
				if calls == 1 {
					assert.Equal(t, "SELECT pg_catalog.to_regclass($1) IS NOT NULL", query)
					assert.Equal(t, []any{`"public"."` + ledger + `"`}, args)
					return migrationTestRow(func(dest ...any) error { *dest[0].(*bool) = true; return nil })
				}
				assert.Equal(t, `SELECT version, dirty, count(*) OVER () FROM "public"."`+ledger+`" LIMIT 1`, query)
				return migrationTestRow(func(dest ...any) error {
					*dest[0].(*int64), *dest[1].(*bool) = 2, false
					*dest[2].(*int64) = 1
					return nil
				})
			})
			preflight, err := NewMigrationPreflight(reader, ledger, 2)
			require.NoError(t, err)
			status, err := preflight.Status(t.Context())
			require.NoError(t, err)
			assert.True(t, status.Ready)
			assert.Equal(t, 2, calls)
		})
	}
}

func TestMigrationPreflightRejectsAmbiguousOrMissingLedger(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		present bool
		rows    int64
		version int64
		dirty   bool
		want    string
	}{
		{name: "absent", want: "migration metadata table is missing"},
		{name: "empty", present: true, want: "migration metadata table is missing"},
		{name: "multiple rows", present: true, rows: 2, version: 2, want: "exactly one version row"},
		{name: "negative version", present: true, rows: 1, version: -1, want: "is invalid"},
		{name: "dirty", present: true, rows: 1, version: 2, dirty: true, want: "is dirty"},
		{name: "behind", present: true, rows: 1, version: 1, want: "behind required"},
		{name: "ahead", present: true, rows: 1, version: 3, want: "ahead of required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader := migrationTestReader(func(_ context.Context, query string, _ ...any) pgx.Row {
				return migrationTestRow(func(dest ...any) error {
					if strings.Contains(query, "to_regclass") {
						*dest[0].(*bool) = tc.present
						return nil
					}
					if tc.rows == 0 {
						return pgx.ErrNoRows
					}
					*dest[0].(*int64), *dest[1].(*bool), *dest[2].(*int64) = tc.version, tc.dirty, tc.rows
					return nil
				})
			})
			preflight, err := NewMigrationPreflight(reader, "managed_schema_migrations", 2)
			require.NoError(t, err)
			require.ErrorContains(t, preflight.Check(t.Context()), tc.want)
		})
	}
}

func TestMigrationDatabaseURLRejectsInvalidURLWithoutCredentials(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"postgres://user:private%zz@example.test/app", "https://user:private@example.test/app", "postgres://user:private@example.test/app?sslmode=%zz"} {
		_, err := MigrationDatabaseURL(raw, "core_schema_migrations")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "private")
	}
}

func TestMigrationPreflightRequiresReleaseContract(t *testing.T) {
	t.Parallel()
	reader := migrationTestReader(func(context.Context, string, ...any) pgx.Row { return nil })
	_, err := NewMigrationPreflight(nil, "core_schema_migrations", 1)
	require.Error(t, err)
	_, err = NewMigrationPreflight(reader, "core_schema_migrations", 0)
	require.Error(t, err)
}

type migrationTestReader func(context.Context, string, ...any) pgx.Row

func (r migrationTestReader) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return r(ctx, query, args...)
}

type migrationTestRow func(...any) error

func (r migrationTestRow) Scan(dest ...any) error { return r(dest...) }

func TestLatestMigrationVersionUsesHighestUpMigration(t *testing.T) {
	t.Parallel()

	version, err := LatestMigrationVersion([]string{
		"000001_initial_schema.up.sql",
		"000010_add_pipeline_runs.up.sql",
		"README.md",
	})

	require.NoError(t, err)
	assert.Equal(t, uint(10), version)
}

func TestLatestMigrationVersionRejectsMissingUpMigrations(t *testing.T) {
	t.Parallel()

	_, err := LatestMigrationVersion([]string{
		"README.md",
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "at least one up migration is required")
}

func TestLatestMigrationVersionRejectsInvalidUpMigrationName(t *testing.T) {
	t.Parallel()

	_, err := LatestMigrationVersion([]string{
		"initial_schema.up.sql",
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "migration filename must start with a numeric version")
}

func TestEvaluateMigrationPreflightAllowsExactCleanVersion(t *testing.T) {
	t.Parallel()

	status, err := EvaluateMigrationPreflight(
		MigrationState{Present: true, Version: 1, Dirty: false},
		1,
	)

	require.NoError(t, err)
	assert.Equal(t, MigrationPreflightStatus{
		Ready:           true,
		CurrentVersion:  1,
		RequiredVersion: 1,
		Reason:          "",
	}, status)
}

func TestEvaluateMigrationPreflightRejectsUnsafeStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		state      MigrationState
		required   uint
		wantReason string
		wantErr    string
	}{
		{
			name:       "MissingMigrationMetadata",
			state:      MigrationState{Present: false},
			required:   1,
			wantReason: "migration metadata missing",
			wantErr:    "migration metadata table is missing",
		},
		{
			name:       "DirtyMigration",
			state:      MigrationState{Present: true, Version: 1, Dirty: true},
			required:   1,
			wantReason: "migration dirty",
			wantErr:    "database migration version 1 is dirty",
		},
		{
			name:       "DatabaseBehindRelease",
			state:      MigrationState{Present: true, Version: 1, Dirty: false},
			required:   2,
			wantReason: "database migration version behind release",
			wantErr:    "database migration version 1 is behind required version 2",
		},
		{
			name:       "DatabaseAheadOfRelease",
			state:      MigrationState{Present: true, Version: 3, Dirty: false},
			required:   2,
			wantReason: "database migration version ahead of release",
			wantErr:    "database migration version 3 is ahead of required version 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status, err := EvaluateMigrationPreflight(tt.state, tt.required)

			require.Error(t, err)
			require.ErrorContains(t, err, tt.wantErr)
			assert.Equal(t, MigrationPreflightStatus{
				Ready:           false,
				CurrentVersion:  tt.state.Version,
				RequiredVersion: tt.required,
				Reason:          tt.wantReason,
			}, status)
		})
	}
}
