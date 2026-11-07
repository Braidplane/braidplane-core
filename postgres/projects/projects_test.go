package projects

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type fakeDBTX struct {
	execFn     func(context.Context, string, ...any) (pgconn.CommandTag, error)
	queryFn    func(context.Context, string, ...any) (pgx.Rows, error)
	queryRowFn func(context.Context, string, ...any) pgx.Row
}

func (f fakeDBTX) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return f.execFn(ctx, query, args...)
}

func (f fakeDBTX) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return f.queryFn(ctx, query, args...)
}

func (f fakeDBTX) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return f.queryRowFn(ctx, query, args...)
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, value := range r.values {
		assignScanValue(dest[i], value)
	}
	return nil
}

type fakeRows struct {
	rows [][]any
	idx  int
	err  error
}

func (r *fakeRows) Close() {}

func (r *fakeRows) Err() error {
	return r.err
}

func (r *fakeRows) CommandTag() pgconn.CommandTag {
	return pgconn.NewCommandTag("SELECT 0")
}

func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}

func (r *fakeRows) Next() bool {
	r.idx++
	return r.idx < len(r.rows)
}

func (r *fakeRows) Scan(dest ...any) error {
	for i, value := range r.rows[r.idx] {
		assignScanValue(dest[i], value)
	}
	return nil
}

func (r *fakeRows) Values() ([]any, error) {
	return nil, nil
}

func (r *fakeRows) RawValues() [][]byte {
	return nil
}

func (r *fakeRows) Conn() *pgx.Conn {
	return nil
}

func assignScanValue(dest, value any) {
	switch target := dest.(type) {
	case *int:
		*target = value.(int)
	case *int32:
		*target = value.(int32)
	case *slug.Slug:
		*target = value.(slug.Slug)
	case *string:
		*target = value.(string)
	case *pgtype.Timestamp:
		*target = value.(pgtype.Timestamp)
	case *pgtype.Timestamptz:
		*target = value.(pgtype.Timestamptz)
	case *pgtype.Int4:
		*target = value.(pgtype.Int4)
	case *pgtype.Int8:
		*target = value.(pgtype.Int8)
	case *pgtype.Text:
		*target = value.(pgtype.Text)
	case *[]byte:
		*target = value.([]byte)
	default:
		panic("unsupported scan destination")
	}
}

func projectRowValues(
	projectSlug slug.Slug,
	name string,
	now time.Time,
	backendSlug, backendRepo, trackedBranch string,
) []any {
	return []any{
		1,
		int32(7),
		projectSlug,
		name,
		pgtype.Int8{Int64: 9, Valid: backendSlug != ""},
		pgtype.Text{String: backendRepo, Valid: backendRepo != ""},
		pgtype.Text{String: trackedBranch, Valid: trackedBranch != ""},
		pgtype.Timestamp{Time: now, Valid: true},
		pgtype.Timestamp{Time: now.Add(time.Minute), Valid: true},
		pgtype.Text{String: "pipelines", Valid: backendSlug != ""},
		pgtype.Text{String: "abcdef1", Valid: backendSlug != ""},
		pgtype.Text{String: backendSlug, Valid: backendSlug != ""},
		backendRepo,
		trackedBranch,
	}
}

func organizationRowValues(orgSlug slug.Slug, now time.Time) []any {
	return []any{
		1,
		"Acme",
		orgSlug,
		[]byte("{}"),
		pgtype.Timestamp{Time: now, Valid: true},
		pgtype.Timestamp{Time: now.Add(time.Minute), Valid: true},
	}
}

func TestRepository_CreateMapsProjectAndScopesByOrganization(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("ops")
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)

	repo := NewRepository()
	got, err := repo.Create(context.Background(), fakeDBTX{
		queryRowFn: func(_ context.Context, _ string, args ...any) pgx.Row {
			require.Equal(t, projectSlug, args[0])
			require.Equal(t, "Ops", args[1])
			require.Equal(t, orgSlug, args[2])
			return fakeRow{
				values: projectRowValues(
					projectSlug,
					"Ops",
					now,
					"pipelines-git",
					"https://github.com/acme/pipelines.git",
					"release",
				),
			}
		},
	}, &CreateParams{
		OrgSlug: orgSlug,
		Slug:    projectSlug,
		Name:    "Ops",
	})

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, projectSlug, got.Slug)
	assert.Equal(t, "Ops", got.Name)
	require.NotNil(t, got.PipelineBackendDataSourceSlug)
	assert.Equal(t, must.Slug("pipelines-git"), *got.PipelineBackendDataSourceSlug)
	require.NotNil(t, got.PipelineBackendRepo)
	assert.Equal(t, "https://github.com/acme/pipelines.git", *got.PipelineBackendRepo)
	require.NotNil(t, got.PipelineBackendTrackedBranch)
	assert.Equal(t, "release", *got.PipelineBackendTrackedBranch)
	assert.Equal(t, now, got.CreatedAt)
	assert.Equal(t, now.Add(time.Minute), got.UpdatedAt)
}

func TestRepository_FindBySlugReturnsNotFound(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	_, err := repo.FindBySlug(context.Background(), fakeDBTX{
		queryRowFn: func(context.Context, string, ...any) pgx.Row {
			return fakeRow{err: sql.ErrNoRows}
		},
	}, must.Slug("acme"), must.Slug("missing"))

	require.ErrorIs(t, err, ErrNotFound)
}

func TestRepository_ListByOrgMapsRowsAndCursorScope(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)

	repo := NewRepository()
	got, err := repo.ListByOrg(context.Background(), fakeDBTX{
		queryFn: func(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
			require.Equal(t, orgSlug, args[0])
			cursorSlug, ok := args[1].(pgtype.Text)
			require.True(t, ok)
			require.Equal(t, "default", cursorSlug.String)
			require.Equal(t, int64(2), args[6])
			return &fakeRows{idx: -1, rows: [][]any{
				projectRowValues(must.Slug("ops"), "Ops", now, "", "", ""),
				projectRowValues(must.Slug("support"), "Support", now.Add(time.Hour), "pipelines-git", "https://github.com/acme/pipelines.git", "main"),
			}}, nil
		},
	}, orgSlug, model.ListRequest{
		Limit:  2,
		SortBy: "created_at",
		Order:  "asc",
		Cursor: "2026-05-15T10:00:00Z_default",
	})

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, must.Slug("ops"), got[0].Slug)
	assert.Equal(t, "Support", got[1].Name)
	require.NotNil(t, got[1].PipelineBackendTrackedBranch)
	assert.Equal(t, "main", *got[1].PipelineBackendTrackedBranch)
}

func TestRepository_UpdateReturnsNotFoundWhenNoRowsAffected(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	err := repo.Update(context.Background(), fakeDBTX{
		execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		},
	}, &UpdateParams{
		OrgSlug: must.Slug("acme"),
		Slug:    must.Slug("ops"),
		Name:    "Ops",
	})

	require.ErrorIs(t, err, ErrNotFound)
}

func TestRepository_UpdateWrapsUnexpectedError(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	err := repo.Update(context.Background(), fakeDBTX{
		execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, xerror.Unexpected().Detail("database unavailable")
		},
	}, &UpdateParams{OrgSlug: must.Slug("acme"), Slug: must.Slug("ops"), Name: "Ops"})

	require.Error(t, err)
	require.True(t, xerror.Is(err, xerror.Unexpected()))
}

func TestRepository_SetPipelineBackendMapsRepoAndTrackedBranch(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	err := repo.SetPipelineBackend(context.Background(), fakeDBTX{
		execFn: func(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
			repoArg, ok := args[0].(pgtype.Text)
			require.True(t, ok)
			require.True(t, repoArg.Valid)
			require.Equal(t, "https://github.com/acme/pipelines.git", repoArg.String)

			branchArg, ok := args[1].(pgtype.Text)
			require.True(t, ok)
			require.True(t, branchArg.Valid)
			require.Equal(t, "release", branchArg.String)
			dirArg, ok := args[2].(pgtype.Text)
			require.True(t, ok)
			require.True(t, dirArg.Valid)
			require.Equal(t, "pipelines", dirArg.String)
			commitArg, ok := args[3].(pgtype.Text)
			require.True(t, ok)
			require.True(t, commitArg.Valid)
			require.Equal(t, "abcdef1", commitArg.String)

			require.Equal(t, must.Slug("acme"), args[4])
			require.Equal(t, must.Slug("ops"), args[5])
			require.Equal(t, must.Slug("pipelines-git"), args[6])
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}, &SetPipelineBackendParams{
		OrgSlug:                       must.Slug("acme"),
		Slug:                          must.Slug("ops"),
		PipelineBackendDataSourceSlug: must.Slug("pipelines-git"),
		PipelineBackendRepo:           "https://github.com/acme/pipelines.git",
		PipelineBackendTrackedBranch:  "release",
		PipelineBackendPipelineDir:    "pipelines",
		PipelineBackendFirstCommitSHA: ptr.Ptr("abcdef1"),
	})

	require.NoError(t, err)
}

func TestRepository_SetPipelineBackendReturnsConflictForRepoBranchUniqueness(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	err := repo.SetPipelineBackend(context.Background(), fakeDBTX{
		execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "uq_projects_org_pipeline_backend_repo_branch",
			}
		},
	}, &SetPipelineBackendParams{
		OrgSlug:                       must.Slug("acme"),
		Slug:                          must.Slug("ops"),
		PipelineBackendDataSourceSlug: must.Slug("pipelines-git"),
		PipelineBackendRepo:           "https://github.com/acme/pipelines.git",
		PipelineBackendTrackedBranch:  "release",
	})

	require.ErrorIs(t, err, ErrPipelineBackendTaken)
}

func TestRepository_SetPipelineBackendReturnsUnavailableWhenDataSourceGuardMisses(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	repo := NewRepository()
	err := repo.SetPipelineBackend(context.Background(), fakeDBTX{
		execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		},
		queryRowFn: func(_ context.Context, _ string, args ...any) pgx.Row {
			switch len(args) {
			case 2:
				return fakeRow{values: projectRowValues(must.Slug("ops"), "Ops", now, "", "", "")}
			case 1:
				return fakeRow{values: organizationRowValues(must.Slug("acme"), now)}
			default:
				t.Fatalf("unexpected query args: %d", len(args))
				return fakeRow{err: sql.ErrNoRows}
			}
		},
	}, &SetPipelineBackendParams{
		OrgSlug:                       must.Slug("acme"),
		Slug:                          must.Slug("ops"),
		PipelineBackendDataSourceSlug: must.Slug("pipelines-git"),
		PipelineBackendRepo:           "https://github.com/acme/pipelines.git",
		PipelineBackendTrackedBranch:  "release",
	})

	require.ErrorIs(t, err, ErrPipelineBackendDataSourceUnavailable)
}

func TestRepository_SetPipelineBackendReturnsNotFoundWhenProjectIsMissing(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	err := repo.SetPipelineBackend(context.Background(), fakeDBTX{
		execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		},
		queryRowFn: func(context.Context, string, ...any) pgx.Row {
			return fakeRow{err: sql.ErrNoRows}
		},
	}, &SetPipelineBackendParams{
		OrgSlug:                       must.Slug("acme"),
		Slug:                          must.Slug("ops"),
		PipelineBackendDataSourceSlug: must.Slug("pipelines-git"),
		PipelineBackendRepo:           "https://github.com/acme/pipelines.git",
		PipelineBackendTrackedBranch:  "release",
	})

	require.ErrorIs(t, err, ErrNotFound)
	require.NotErrorIs(t, err, ErrPipelineBackendDataSourceUnavailable)
}

func TestRepository_DeleteReturnsNotFoundWhenNoRowsAffected(t *testing.T) {
	t.Parallel()

	repo := NewRepository()
	err := repo.Delete(context.Background(), fakeDBTX{
		execFn: func(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
			require.Equal(t, []any{must.Slug("acme"), must.Slug("missing")}, args)
			return pgconn.NewCommandTag("DELETE 0"), nil
		},
	}, must.Slug("acme"), must.Slug("missing"))

	require.ErrorIs(t, err, ErrNotFound)
}

var (
	_ pgx.Rows = (*fakeRows)(nil)
	_ pgx.Row  = fakeRow{}
)
