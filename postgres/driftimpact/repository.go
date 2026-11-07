package driftimpact

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	pipelinedrift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var ErrReportNotFound = xerror.NotFound(xerror.DriftImpactReport)

type Repository interface {
	SupersedeOpen(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params CreateParams) error
	Create(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params CreateParams) (*Report, error)
	FindBySlug(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, reportSlug slug.Slug) (*Report, error)
	List(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, filters ListFilters) ([]*Report, error)
}

type repository struct{}

func New() Repository {
	return &repository{}
}

func (r *repository) SupersedeOpen(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	params CreateParams,
) error {
	if err := validateAttachment(params); err != nil {
		return err
	}
	err := dbgen.New(tx).SupersedeOpenDriftImpactReports(
		ctx,
		dbgen.SupersedeOpenDriftImpactReportsParams{
			OrgSlug:        orgSlug,
			ReportType:     params.ReportType.String(),
			ProjectSlug:    postgres.OptionalSlugToText(params.ProjectSlug),
			PipelineSlug:   postgres.OptionalSlugToText(params.PipelineSlug),
			DataSourceSlug: postgres.OptionalSlugToText(params.DataSourceSlug),
		},
	)
	if err != nil {
		return xerror.Wrapf("supersede drift impact reports: %w", err)
	}
	return nil
}

func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	params CreateParams,
) (*Report, error) {
	if err := validateAttachment(params); err != nil {
		return nil, err
	}
	row, err := dbgen.New(tx).CreateDriftImpactReport(
		ctx,
		dbgen.CreateDriftImpactReportParams{
			OrgSlug:        orgSlug,
			Slug:           params.Slug,
			ReportType:     params.ReportType.String(),
			Status:         params.Status.String(),
			Severity:       params.Severity.String(),
			Summary:        params.Summary,
			Changes:        params.ChangesJSONB,
			Impacts:        params.ImpactsJSONB,
			ProjectSlug:    postgres.OptionalSlugToText(params.ProjectSlug),
			PipelineSlug:   postgres.OptionalSlugToText(params.PipelineSlug),
			DataSourceSlug: postgres.OptionalSlugToText(params.DataSourceSlug),
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("create drift impact report: %w", err)
	}
	report := reportFromBase(
		row.Slug,
		row.ReportType,
		row.Status,
		row.Severity,
		row.Summary,
		row.Changes,
		row.Impacts,
		row.CreatedAt,
		row.SupersededAt,
	)
	report.ProjectSlug = params.ProjectSlug
	report.PipelineSlug = params.PipelineSlug
	report.DataSourceSlug = params.DataSourceSlug
	return report, nil
}

func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, reportSlug slug.Slug,
) (*Report, error) {
	row, err := dbgen.New(tx).FindDriftImpactReportBySlug(
		ctx,
		dbgen.FindDriftImpactReportBySlugParams{
			OrgSlug:     orgSlug,
			ProjectSlug: projectSlug.String(),
			ReportSlug:  reportSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrReportNotFound.With(err)
		}
		return nil, xerror.Wrapf("find drift impact report: %w", err)
	}
	report := reportFromBase(
		row.Slug,
		row.ReportType,
		row.Status,
		row.Severity,
		row.Summary,
		row.Changes,
		row.Impacts,
		row.CreatedAt,
		row.SupersededAt,
	)
	report.ProjectSlug = optionalSlugFromValue(row.ProjectSlug)
	report.PipelineSlug = optionalSlugFromValue(row.PipelineSlug)
	report.DataSourceSlug = optionalSlugFromValue(row.DataSourceSlug)
	return report, nil
}

func (r *repository) List(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	filters ListFilters,
) ([]*Report, error) {
	if filters.Limit <= 0 {
		filters.Limit = 50
	}
	rows, err := dbgen.New(tx).ListDriftImpactReports(
		ctx,
		dbgen.ListDriftImpactReportsParams{
			OrgSlug:        orgSlug,
			ProjectSlug:    postgres.OptionalSlugToText(filters.ProjectSlug),
			PipelineSlug:   postgres.OptionalSlugToText(filters.PipelineSlug),
			DataSourceSlug: postgres.OptionalSlugToText(filters.DataSourceSlug),
			Status:         optionalStatusToText(filters.Status),
			Severity:       optionalSeverityToText(filters.Severity),
			LimitCount:     int64(filters.Limit),
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("list drift impact reports: %w", err)
	}

	reports := make([]*Report, 0, len(rows))
	for _, row := range rows {
		report := reportFromBase(
			row.Slug,
			row.ReportType,
			row.Status,
			row.Severity,
			row.Summary,
			row.Changes,
			row.Impacts,
			row.CreatedAt,
			row.SupersededAt,
		)
		report.ProjectSlug = optionalSlugFromValue(row.ProjectSlug)
		report.PipelineSlug = optionalSlugFromValue(row.PipelineSlug)
		report.DataSourceSlug = optionalSlugFromValue(row.DataSourceSlug)
		reports = append(reports, report)
	}
	return reports, nil
}

func validateAttachment(params CreateParams) error {
	hasPipeline := params.ProjectSlug != nil || params.PipelineSlug != nil
	hasCompletePipeline := params.ProjectSlug != nil && params.PipelineSlug != nil
	hasDataSource := params.DataSourceSlug != nil
	if hasCompletePipeline == hasDataSource || hasPipeline != hasCompletePipeline {
		return xerror.InvalidInput(xerror.DriftImpactReport).Detail(
			"drift impact report must attach to exactly one pipeline or data source",
		)
	}
	return nil
}

func reportFromBase(
	reportSlug slug.Slug,
	reportType string,
	status string,
	severity string,
	summary string,
	changes []byte,
	impacts []byte,
	createdAt pgtype.Timestamptz,
	supersededAt pgtype.Timestamptz,
) *Report {
	return &Report{
		Slug:         reportSlug,
		ReportType:   reportTypeFromString(reportType),
		Status:       statusFromString(status),
		Severity:     severityFromString(severity),
		Summary:      summary,
		ChangesJSONB: changes,
		ImpactsJSONB: impacts,
		CreatedAt:    createdAt.Time,
		SupersededAt: postgres.FromNullableTimestamptz(supersededAt),
	}
}

func optionalSlugFromValue(value slug.Slug) *slug.Slug {
	if value.String() == "" {
		return nil
	}
	return &value
}

func optionalStatusToText(value *pipelinedrift.ReportStatus) pgtype.Text {
	if value == nil {
		return pgtype.Text{Valid: false}
	}
	return postgres.ToText(value.String())
}

func optionalSeverityToText(value *pipelinedrift.ReportSeverity) pgtype.Text {
	if value == nil {
		return pgtype.Text{Valid: false}
	}
	return postgres.ToText(value.String())
}

func reportTypeFromString(value string) pipelinedrift.ReportType {
	parsed, err := pipelinedrift.ReportTypeString(value)
	if err != nil {
		return pipelinedrift.ReportTypeUnknown
	}
	return parsed
}

func statusFromString(value string) pipelinedrift.ReportStatus {
	parsed, err := pipelinedrift.ReportStatusString(value)
	if err != nil {
		return pipelinedrift.ReportStatusUnknown
	}
	return parsed
}

func severityFromString(value string) pipelinedrift.ReportSeverity {
	parsed, err := pipelinedrift.ReportSeverityString(value)
	if err != nil {
		return pipelinedrift.ReportSeverityUnknown
	}
	return parsed
}
