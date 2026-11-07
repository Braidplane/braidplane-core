package pipelinevalidations

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type Result struct {
	Slug             slug.Slug
	OrgSlug          slug.Slug
	ProjectSlug      slug.Slug
	EnvironmentSlug  slug.Slug
	PipelineSlug     slug.Slug
	Source           string
	Valid            bool
	SpecHash         *string
	DiagnosticsJSONB []byte
	ImpactReportSlug *slug.Slug
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type CreateParams struct {
	Slug             slug.Slug
	PipelineSlug     slug.Slug
	Source           string
	Valid            bool
	SpecHash         *string
	DiagnosticsJSONB []byte
	ImpactReportSlug *slug.Slug
}

type ListFilters struct {
	PipelineSlug *slug.Slug
	Limit        int32
}
