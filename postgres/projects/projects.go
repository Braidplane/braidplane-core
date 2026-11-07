package projects

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
)

func NewRepository() Repository {
	return &repository{}
}

type repository struct{}

func toProject(
	projectSlug slug.Slug,
	name string,
	createdAt, updatedAt pgtype.Timestamp,
	pipelineBackendDataSourceSlug pgtype.Text,
	pipelineBackendRepo pgtype.Text,
	pipelineBackendTrackedBranch pgtype.Text,
	pipelineBackendPipelineDir pgtype.Text,
	pipelineBackendFirstCommitSHA pgtype.Text,
) *Project {
	return &Project{
		Slug:                          projectSlug,
		Name:                          name,
		PipelineBackendDataSourceSlug: nullableTextSlug(pipelineBackendDataSourceSlug),
		PipelineBackendRepo:           nullableTextPtr(pipelineBackendRepo),
		PipelineBackendTrackedBranch:  nullableTextPtr(pipelineBackendTrackedBranch),
		PipelineBackendPipelineDir:    nullableTextPtr(pipelineBackendPipelineDir),
		PipelineBackendFirstCommitSHA: nullableTextPtr(pipelineBackendFirstCommitSHA),
		CreatedAt:                     createdAt.Time,
		UpdatedAt:                     updatedAt.Time,
	}
}

func nullableTextSlug(value pgtype.Text) *slug.Slug {
	if !value.Valid || value.String == "" {
		return nil
	}
	ret := slug.Slug(value.String)
	return &ret
}

func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func nullableTextPtr(value pgtype.Text) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params *CreateParams,
) (*Project, error) {
	row, err := dbgen.New(tx).CreateProject(ctx, dbgen.CreateProjectParams{
		OrgSlug:     params.OrgSlug,
		ProjectSlug: params.Slug,
		Name:        params.Name,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		if postgres.IsUniqueViolation(err, "uq_projects_org_slug") {
			return nil, ErrSlugTaken.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toProject(
		row.Slug,
		row.Name,
		row.CreatedAt,
		row.UpdatedAt,
		row.PipelineBackendDataSourceSlug,
		row.PipelineBackendRepo,
		row.PipelineBackendTrackedBranch,
		row.PipelineBackendPipelineDir,
		row.PipelineBackendFirstCommitSha,
	), nil
}

func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
) (*Project, error) {
	row, err := dbgen.New(tx).FindProjectBySlug(ctx, dbgen.FindProjectBySlugParams{
		OrgSlug:     orgSlug,
		ProjectSlug: projectSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toProject(
		row.Slug,
		row.Name,
		row.CreatedAt,
		row.UpdatedAt,
		row.PipelineBackendDataSourceSlug,
		row.PipelineBackendRepo,
		row.PipelineBackendTrackedBranch,
		row.PipelineBackendPipelineDir,
		row.PipelineBackendFirstCommitSha,
	), nil
}

func (r *repository) FindBySlugForUpdate(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
) (*Project, error) {
	row, err := dbgen.New(tx).FindProjectBySlugForUpdate(
		ctx,
		dbgen.FindProjectBySlugForUpdateParams{
			OrgSlug:     orgSlug,
			ProjectSlug: projectSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toProject(
		row.Slug,
		row.Name,
		row.CreatedAt,
		row.UpdatedAt,
		row.PipelineBackendDataSourceSlug,
		row.PipelineBackendRepo,
		row.PipelineBackendTrackedBranch,
		row.PipelineBackendPipelineDir,
		row.PipelineBackendFirstCommitSha,
	), nil
}

func (r *repository) ListByOrg(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*Project, error) {
	cursorSlug, cursorTimestamp, cursorText, err := postgres.BuildCursorValues(
		req.Cursor,
		req.SortBy,
	)
	if err != nil {
		return nil, xerror.Wrapf("build project cursor values: %w", err)
	}

	rows, err := dbgen.New(tx).ListProjectsByOrg(ctx, dbgen.ListProjectsByOrgParams{
		OrgSlug:         orgSlug,
		CursorSlug:      cursorSlug,
		SortBy:          pgtype.Text{String: req.SortBy, Valid: req.SortBy != ""},
		OrderDir:        pgtype.Text{String: req.Order, Valid: req.Order != ""},
		CursorTimestamp: cursorTimestamp,
		CursorText:      cursorText,
		LimitCount:      int64(req.Limit),
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*Project, len(rows))
	for i, row := range rows {
		result[i] = toProject(
			row.Slug,
			row.Name,
			row.CreatedAt,
			row.UpdatedAt,
			row.PipelineBackendDataSourceSlug,
			row.PipelineBackendRepo,
			row.PipelineBackendTrackedBranch,
			row.PipelineBackendPipelineDir,
			row.PipelineBackendFirstCommitSha,
		)
	}

	return result, nil
}

func (r *repository) Update(ctx context.Context, tx postgres.DBTX, params *UpdateParams) error {
	rowsAffected, err := dbgen.New(tx).UpdateProject(ctx, dbgen.UpdateProjectParams{
		Name:        params.Name,
		OrgSlug:     params.OrgSlug,
		ProjectSlug: params.Slug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}

	return nil
}

func (r *repository) SetPipelineBackend(
	ctx context.Context,
	tx postgres.DBTX,
	params *SetPipelineBackendParams,
) error {
	rowsAffected, err := dbgen.New(tx).SetProjectPipelineBackend(
		ctx,
		dbgen.SetProjectPipelineBackendParams{
			OrgSlug:                       params.OrgSlug,
			ProjectSlug:                   params.Slug,
			PipelineBackendDataSourceSlug: params.PipelineBackendDataSourceSlug,
			PipelineBackendRepo:           nullableText(params.PipelineBackendRepo),
			PipelineBackendTrackedBranch:  nullableText(params.PipelineBackendTrackedBranch),
			PipelineBackendPipelineDir:    nullableText(params.PipelineBackendPipelineDir),
			PipelineBackendFirstCommitSha: nullableTextPtrValue(params.PipelineBackendFirstCommitSHA),
		},
	)
	if err != nil {
		if postgres.IsUniqueViolation(err, "uq_projects_org_pipeline_backend_repo_branch") {
			return ErrPipelineBackendTaken.With(err)
		}
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		if _, findErr := r.FindBySlug(ctx, tx, params.OrgSlug, params.Slug); findErr != nil {
			return findErr
		}
		if _, findErr := organizations.NewRepository().FindBySlug(ctx, tx, params.OrgSlug); findErr != nil {
			return findErr
		}
		return ErrPipelineBackendDataSourceUnavailable
	}

	return nil
}

func nullableTextPtrValue(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return nullableText(*value)
}

func (r *repository) ClearPipelineBackend(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
) error {
	rowsAffected, err := dbgen.New(tx).ClearProjectPipelineBackend(
		ctx,
		dbgen.ClearProjectPipelineBackendParams{
			OrgSlug:     orgSlug,
			ProjectSlug: projectSlug,
		},
	)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}

	return nil
}

func (r *repository) Delete(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug slug.Slug) error {
	result, err := dbgen.New(tx).DeleteProject(ctx, dbgen.DeleteProjectParams{
		OrgSlug:     orgSlug,
		ProjectSlug: projectSlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}
