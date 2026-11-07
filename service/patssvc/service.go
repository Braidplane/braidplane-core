package patssvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pats"
)

type Service interface {
	ListPersonalAccessTokens(
		ctx context.Context,
		userSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.PersonalAccessToken], error)
	CreatePersonalAccessToken(
		ctx context.Context,
		userSlug slug.Slug,
		newPAT model.NewPersonalAccessToken,
	) (*model.PersonalAccessTokenWithToken, error)
	GetPersonalAccessToken(
		ctx context.Context,
		userSlug, patSlug slug.Slug,
	) (*model.PersonalAccessToken, error)
	UpdatePersonalAccessToken(
		ctx context.Context,
		userSlug, patSlug slug.Slug,
		update model.UpdatePersonalAccessToken,
	) (*model.PersonalAccessToken, error)
	RevokePersonalAccessToken(ctx context.Context, userSlug, patSlug slug.Slug) error
	DeleteRevokedPersonalAccessToken(ctx context.Context, userSlug, patSlug slug.Slug) error
	RotatePersonalAccessToken(
		ctx context.Context,
		userSlug, patSlug slug.Slug,
	) (*model.PersonalAccessTokenWithToken, error)
	Authenticate(ctx context.Context, token string) (*model.UserPrincipal, error)
}

type service struct {
	txm      postgres.TransactionManager
	patsRepo pats.Repository
}

func New(
	txm postgres.TransactionManager,
	patsRepo pats.Repository,
) Service {
	return &service{
		txm:      txm,
		patsRepo: patsRepo,
	}
}
