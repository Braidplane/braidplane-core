package driftimpact

import (
	"time"

	pipelinedrift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type Report struct {
	Slug           slug.Slug
	ReportType     pipelinedrift.ReportType
	Status         pipelinedrift.ReportStatus
	Severity       pipelinedrift.ReportSeverity
	Summary        string
	ChangesJSONB   []byte
	ImpactsJSONB   []byte
	ProjectSlug    *slug.Slug
	PipelineSlug   *slug.Slug
	DataSourceSlug *slug.Slug
	CreatedAt      time.Time
	SupersededAt   *time.Time
}

type CreateParams struct {
	Slug           slug.Slug
	ReportType     pipelinedrift.ReportType
	Status         pipelinedrift.ReportStatus
	Severity       pipelinedrift.ReportSeverity
	Summary        string
	ChangesJSONB   []byte
	ImpactsJSONB   []byte
	ProjectSlug    *slug.Slug
	PipelineSlug   *slug.Slug
	DataSourceSlug *slug.Slug
}

type ListFilters struct {
	ProjectSlug    *slug.Slug
	PipelineSlug   *slug.Slug
	DataSourceSlug *slug.Slug
	Status         *pipelinedrift.ReportStatus
	Severity       *pipelinedrift.ReportSeverity
	Limit          int32
}
