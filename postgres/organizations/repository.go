package organizations

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	DeletionBlocked(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) (bool, error)
	Delete(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) error
	Create(ctx context.Context, tx postgres.DBTX, params *CreateParams) (*Organization, error)
	FindBySlug(ctx context.Context, tx postgres.DBTX, s slug.Slug) (*Organization, error)
	FindBySlugForUpdate(ctx context.Context, tx postgres.DBTX, s slug.Slug) (*Organization, error)
	Update(ctx context.Context, tx postgres.DBTX, params *UpdateParams) error
	UpdateSettings(ctx context.Context, tx postgres.DBTX, params *UpdateSettingsParams) error
	ListByUser(
		ctx context.Context,
		tx postgres.DBTX,
		userSlug slug.Slug,
		req model.ListRequest,
	) ([]*OrganizationWithRole, error)
}
