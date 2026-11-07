package members

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	Create(ctx context.Context, tx postgres.DBTX, params *CreateParams) (*Member, error)
	Find(ctx context.Context, tx postgres.DBTX, orgSlug, userSlug slug.Slug) (*Member, error)
	FindByEmail(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, email string) (*Member, error)
	CountByOrganization(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) (int64, error)
	CountByRole(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		roleSlug slug.Slug,
	) (int64, error)
	ListByOrganization(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		req model.ListRequest,
	) ([]*Member, error)
	Update(ctx context.Context, tx postgres.DBTX, orgSlug, userSlug, roleSlug slug.Slug) error
	Remove(ctx context.Context, tx postgres.DBTX, orgSlug, userSlug slug.Slug) error
}
