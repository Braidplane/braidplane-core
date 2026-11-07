package pats

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	Create(ctx context.Context, tx postgres.DBTX, params CreateParams) (*PersonalAccessToken, error)
	FindBySlug(ctx context.Context, tx postgres.DBTX, userSlug, patSlug slug.Slug) (*PersonalAccessToken, error)
	ListByUser(
		ctx context.Context,
		tx postgres.DBTX,
		userSlug slug.Slug,
		req model.ListRequest,
	) ([]*PersonalAccessToken, error)
	Update(ctx context.Context, tx postgres.DBTX, params UpdateParams) error
	Revoke(ctx context.Context, tx postgres.DBTX, userSlug, patSlug slug.Slug) error
	DeleteRevoked(ctx context.Context, tx postgres.DBTX, userSlug, patSlug slug.Slug) error
	Rotate(ctx context.Context, tx postgres.DBTX, params RotateParams) (*PersonalAccessToken, error)
	FindActivePrincipalByTokenHash(ctx context.Context, tx postgres.DBTX, tokenHash string) (*Principal, error)
	MarkUsed(ctx context.Context, tx postgres.DBTX, userSlug, patSlug slug.Slug) error
}
