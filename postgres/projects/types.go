package projects

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateParams struct {
	OrgSlug slug.Slug
	Slug    slug.Slug
	Name    string
}

type UpdateParams struct {
	OrgSlug slug.Slug
	Slug    slug.Slug
	Name    string
}

type SetPipelineBackendParams struct {
	OrgSlug                       slug.Slug
	Slug                          slug.Slug
	PipelineBackendDataSourceSlug slug.Slug
	// PipelineBackendRepo is the normalized external repo identity resolved from
	// the git data source config. It is not an FK because repos are not local DB rows.
	PipelineBackendRepo           string
	PipelineBackendTrackedBranch  string
	PipelineBackendPipelineDir    string
	PipelineBackendFirstCommitSHA *string
}

type Project struct {
	Slug                          slug.Slug
	Name                          string
	PipelineBackendDataSourceSlug *slug.Slug
	PipelineBackendRepo           *string
	PipelineBackendTrackedBranch  *string
	PipelineBackendPipelineDir    *string
	PipelineBackendFirstCommitSHA *string
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
}

// BindProposalAuthorityParams pins the verified provider identity to the current project backend.
type BindProposalAuthorityParams struct {
	OrgSlug, ProjectSlug, DataSourceSlug, CredentialSlug slug.Slug
	Authority                                            model.PipelineProposalAuthority
}
