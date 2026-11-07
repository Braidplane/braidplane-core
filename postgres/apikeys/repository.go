package apikeys

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	Create(ctx context.Context, tx postgres.DBTX, params CreateParams) (*APIKey, error)
	FindBySlug(ctx context.Context, tx postgres.DBTX, orgSlug, apiKeySlug slug.Slug) (*APIKey, error)
	ListByOrganization(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		roleFilter *slug.Slug,
		req model.ListRequest,
	) ([]*APIKey, error)
	Update(ctx context.Context, tx postgres.DBTX, params UpdateParams) error
	Revoke(ctx context.Context, tx postgres.DBTX, orgSlug, apiKeySlug slug.Slug) error
	DeleteRevoked(ctx context.Context, tx postgres.DBTX, orgSlug, apiKeySlug slug.Slug) error
	Rotate(ctx context.Context, tx postgres.DBTX, params RotateParams) (*APIKey, error)
	FindActivePrincipalByTokenHash(ctx context.Context, tx postgres.DBTX, tokenHash string) (*Principal, error)
	MarkUsed(ctx context.Context, tx postgres.DBTX, orgSlug, apiKeySlug slug.Slug) error
}
