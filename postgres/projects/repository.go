package projects

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

// Repository provides access to project persistence.
type Repository interface {
	Create(ctx context.Context, tx postgres.DBTX, params *CreateParams) (*Project, error)
	FindBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
	) (*Project, error)
	FindBySlugForUpdate(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
	) (*Project, error)
	ListByOrg(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		req model.ListRequest,
	) ([]*Project, error)
	Update(ctx context.Context, tx postgres.DBTX, params *UpdateParams) error
	SetPipelineBackend(
		ctx context.Context,
		tx postgres.DBTX,
		params *SetPipelineBackendParams,
	) error
	RevokeChangedProposalAuthority(context.Context, postgres.DBTX, BindProposalAuthorityParams) error
	BindGitHubProposalAuthority(context.Context, postgres.DBTX, BindProposalAuthorityParams) error
	BindGitBranchProposalAuthority(context.Context, postgres.DBTX, BindProposalAuthorityParams) error
	ClearPipelineBackend(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug slug.Slug) error
	Delete(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug slug.Slug) error
}
