package permissions

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	Create(ctx context.Context, tx postgres.DBTX, params *CreateParams) (*Permission, error)
	FindOrCreate(
		ctx context.Context,
		tx postgres.DBTX,
		resources, actions []string,
	) ([]*Permission, error)
	ListAll(ctx context.Context, tx postgres.DBTX) ([]*Permission, error)
	ListForRole(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, roleSlug slug.Slug,
	) ([]*Permission, error)
	AddToRole(ctx context.Context, tx postgres.DBTX, params *AddToRoleParams) error
	RemoveFromRole(ctx context.Context, tx postgres.DBTX, params *RemoveFromRoleParams) error
	ClearRolePermissions(
		ctx context.Context,
		tx postgres.DBTX,
		params *ClearRolePermissionsParams,
	) error
}
