package repositorysetups

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestRepositoryCreateReturnsWhetherTheTenantScopedInsertWon(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		tag  string
		want bool
	}{
		{name: "created", tag: "INSERT 0 1", want: true},
		{name: "existing or missing scope", tag: "INSERT 0 0", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := New().Create(t.Context(), fakeDB{exec: func(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
				require.Equal(t, []any{slug.Slug("setup"), []byte(`{"request":true}`), slug.Slug("org"), slug.Slug("project")}, args)
				return pgconn.NewCommandTag(tc.tag), nil
			}}, "org", "project", "setup", []byte(`{"request":true}`))
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestRepositoryGetMapsAbsenceAndCopiesJSON(t *testing.T) {
	t.Parallel()
	request := []byte(`{"owner":"acme"}`)
	result := []byte(`{"repository_id":"42"}`)
	db := fakeDB{queryRow: func(_ context.Context, _ string, args ...any) pgx.Row {
		require.Equal(t, []any{slug.Slug("org"), slug.Slug("project")}, args)
		return fakeRow{scan: func(dest ...any) error {
			*dest[0].(*slug.Slug) = "setup"
			*dest[1].(*[]byte) = request
			*dest[2].(*[]byte) = result
			*dest[3].(*string) = "succeeded"
			*dest[4].(*string) = ""
			return nil
		}}
	}}
	got, err := New().Get(t.Context(), db, "org", "project")
	require.NoError(t, err)
	require.Equal(t, slug.Slug("setup"), got.Slug)
	request[2] = 'X'
	result[2] = 'X'
	require.JSONEq(t, `{"owner":"acme"}`, string(got.Request))
	require.JSONEq(t, `{"repository_id":"42"}`, string(got.Result))

	_, err = New().Get(t.Context(), fakeDB{queryRow: func(context.Context, string, ...any) pgx.Row {
		return fakeRow{scan: func(...any) error { return pgx.ErrNoRows }}
	}}, "org", "project")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestRepositoryClaimAndLeaseMutationsUseTypedLeaseOutcomes(t *testing.T) {
	t.Parallel()
	_, err := New().Claim(t.Context(), fakeDB{queryRow: func(context.Context, string, ...any) pgx.Row {
		return fakeRow{scan: func(...any) error { return pgx.ErrNoRows }}
	}}, "org", "project", "setup", "worker")
	require.ErrorIs(t, err, ErrBusy)

	for _, method := range []struct {
		name string
		call func() error
	}{
		{name: "update", call: func() error {
			return New().Update(t.Context(), fakeDB{exec: zeroRows}, "org", "project", "worker", &Row{})
		}},
		{name: "release", call: func() error { return New().Release(t.Context(), fakeDB{exec: zeroRows}, "org", "project", "worker") }},
	} {
		t.Run(method.name, func(t *testing.T) {
			t.Parallel()
			require.ErrorIs(t, method.call(), ErrLeaseLost)
		})
	}
}

func TestRepositoryWrapsDatabaseFailures(t *testing.T) {
	t.Parallel()
	dbFailure := xerror.Unexpected().Detail("database unavailable")
	_, err := New().Create(t.Context(), fakeDB{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, dbFailure
	}}, "org", "project", "setup", nil)
	require.ErrorIs(t, err, dbFailure)
}

type fakeDB struct {
	exec     func(context.Context, string, ...any) (pgconn.CommandTag, error)
	queryRow func(context.Context, string, ...any) pgx.Row
}

func (db fakeDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return db.exec(ctx, sql, args...)
}

func (fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, xerror.Unexpected().Detail("unexpected query")
}

func (db fakeDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return db.queryRow(ctx, sql, args...)
}

type fakeRow struct{ scan func(...any) error }

func (row fakeRow) Scan(dest ...any) error { return row.scan(dest...) }

func zeroRows(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 0"), nil
}

var _ interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
} = fakeDB{}
