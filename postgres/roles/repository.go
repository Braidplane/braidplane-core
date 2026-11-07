package roles

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	Create(ctx context.Context, tx postgres.DBTX, params *CreateParams) (*Role, error)
	EnsureDefault(ctx context.Context, tx postgres.DBTX, params *CreateParams) (*Role, error)
	FindBySlug(ctx context.Context, tx postgres.DBTX, orgSlug, roleSlug slug.Slug) (*Role, error)
	ListByOrganization(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		req model.ListRequest,
		includeDeleted bool,
		onlyDeleted bool,
	) ([]*Role, error)
	GetUserPermissionsInOrg(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, userSlug slug.Slug,
	) ([]*UserPermission, error)
	Update(ctx context.Context, tx postgres.DBTX, params *UpdateParams) error
	Delete(ctx context.Context, tx postgres.DBTX, params *DeleteParams) error
	Restore(ctx context.Context, tx postgres.DBTX, params *RestoreParams) error
}
