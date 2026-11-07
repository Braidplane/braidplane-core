package pipelinevalidations

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var (
	ErrResultNotFound  = xerror.NotFound(xerror.PipelineValidationResult)
	ErrPipelineMissing = xerror.NotFound(xerror.Pipeline)
)

type Repository interface {
	Create(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params CreateParams,
	) (*Result, error)
	FindBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, resultSlug slug.Slug,
	) (*Result, error)
	List(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		filters ListFilters,
	) ([]*Result, error)
}

type repository struct{}

func New() Repository {
	return &repository{}
}

func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params CreateParams,
) (*Result, error) {
	row, err := dbgen.New(tx).CreatePipelineValidationResult(
		ctx,
		dbgen.CreatePipelineValidationResultParams{
			OrgSlug:          orgSlug,
			ProjectSlug:      projectSlug,
			EnvironmentSlug:  environmentSlug,
			ValidationSlug:   params.Slug.String(),
			PipelineSlug:     params.PipelineSlug,
			Source:           params.Source,
			Valid:            params.Valid,
			SpecHash:         optionalStringToText(params.SpecHash),
			Diagnostics:      params.DiagnosticsJSONB,
			ImpactReportSlug: optionalSlugToText(params.ImpactReportSlug),
		},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, r.createTargetNotFoundError(ctx, tx, orgSlug, projectSlug, params.PipelineSlug)
		}
		return nil, xerror.Wrapf("create pipeline validation result: %w", err)
	}
	return resultFromCreateRow(row), nil
}

func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, resultSlug slug.Slug,
) (*Result, error) {
	row, err := dbgen.New(tx).FindPipelineValidationResultBySlug(
		ctx,
		dbgen.FindPipelineValidationResultBySlugParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
			ValidationSlug:  resultSlug.String(),
		},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrResultNotFound.With(err)
		}
		return nil, xerror.Wrapf("find pipeline validation result: %w", err)
	}
	return resultFromFindRow(row), nil
}

func (r *repository) List(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	filters ListFilters,
) ([]*Result, error) {
	if filters.Limit <= 0 {
		filters.Limit = 20
	}
	rows, err := dbgen.New(tx).ListPipelineValidationResults(
		ctx,
		dbgen.ListPipelineValidationResultsParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
			PipelineSlug:    optionalSlugToText(filters.PipelineSlug),
			LimitCount:      int64(filters.Limit),
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("list pipeline validation results: %w", err)
	}
	results := make([]*Result, 0, len(rows))
	for _, row := range rows {
		results = append(results, resultFromListRow(row))
	}
	return results, nil
}

// createTargetNotFoundError distinguishes missing active pipelines from missing active runtime targets.
func (r *repository) createTargetNotFoundError(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) error {
	_, err := dbgen.New(tx).FindPipelineSpecBySlug(
		ctx,
		dbgen.FindPipelineSpecBySlugParams{
			OrgSlug:     orgSlug,
			ProjectSlug: projectSlug,
			Slug:        pipelineSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return ErrPipelineMissing.With(err)
		}
		return xerror.Wrapf("check pipeline validation result pipeline: %w", err)
	}
	return xerror.NotFound(xerror.ProjectEnvironment)
}

func resultFromCreateRow(row dbgen.CreatePipelineValidationResultRow) *Result {
	return resultFromBase(
		slug.Slug(row.ValidationSlug),
		row.OrgSlug,
		row.ProjectSlug,
		row.EnvironmentSlug,
		slug.Slug(row.PipelineSlug),
		row.Source,
		row.Valid,
		row.SpecHash,
		row.Diagnostics,
		row.ImpactReportSlug,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func resultFromFindRow(row dbgen.FindPipelineValidationResultBySlugRow) *Result {
	return resultFromBase(
		slug.Slug(row.ValidationSlug),
		row.OrgSlug,
		row.ProjectSlug,
		row.EnvironmentSlug,
		slug.Slug(row.PipelineSlug),
		row.Source,
		row.Valid,
		row.SpecHash,
		row.Diagnostics,
		row.ImpactReportSlug,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func resultFromListRow(row dbgen.ListPipelineValidationResultsRow) *Result {
	return resultFromBase(
		slug.Slug(row.ValidationSlug),
		row.OrgSlug,
		row.ProjectSlug,
		row.EnvironmentSlug,
		slug.Slug(row.PipelineSlug),
		row.Source,
		row.Valid,
		row.SpecHash,
		row.Diagnostics,
		row.ImpactReportSlug,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func resultFromBase(
	resultSlug slug.Slug,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	environmentSlug slug.Slug,
	pipelineSlug slug.Slug,
	source string,
	valid bool,
	specHash pgtype.Text,
	diagnostics []byte,
	impactReportSlug pgtype.Text,
	createdAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
) *Result {
	return &Result{
		Slug:             resultSlug,
		OrgSlug:          orgSlug,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		PipelineSlug:     pipelineSlug,
		Source:           source,
		Valid:            valid,
		SpecHash:         postgres.FromNullableText(specHash),
		DiagnosticsJSONB: diagnostics,
		ImpactReportSlug: optionalSlugFromText(impactReportSlug),
		CreatedAt:        createdAt.Time,
		UpdatedAt:        updatedAt.Time,
	}
}

func optionalStringToText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{Valid: false}
	}
	return postgres.ToText(*value)
}

func optionalSlugToText(value *slug.Slug) pgtype.Text {
	if value == nil || value.String() == "" {
		return pgtype.Text{Valid: false}
	}
	return postgres.ToText(value.String())
}

func optionalSlugFromText(value pgtype.Text) *slug.Slug {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed := slug.Slug(value.String)
	return &parsed
}
