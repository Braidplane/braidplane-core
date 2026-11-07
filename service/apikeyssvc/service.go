package apikeyssvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/apikeys"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

type Service interface {
	ListAPIKeys(
		ctx context.Context,
		orgSlug slug.Slug,
		roleFilter *slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.APIKey], error)
	CreateAPIKey(
		ctx context.Context,
		orgSlug slug.Slug,
		actor model.EventActor,
		newAPIKey model.NewAPIKey,
	) (*model.APIKeyWithToken, error)
	GetAPIKey(ctx context.Context, orgSlug, apiKeySlug slug.Slug) (*model.APIKey, error)
	UpdateAPIKey(
		ctx context.Context,
		orgSlug, apiKeySlug slug.Slug,
		actor model.EventActor,
		update model.UpdateAPIKey,
	) (*model.APIKey, error)
	RevokeAPIKey(ctx context.Context, orgSlug, apiKeySlug slug.Slug, actor model.EventActor) error
	DeleteRevokedAPIKey(ctx context.Context, orgSlug, apiKeySlug slug.Slug, actor model.EventActor) error
	RotateAPIKey(
		ctx context.Context,
		orgSlug, apiKeySlug slug.Slug,
		actor model.EventActor,
	) (*model.APIKeyWithToken, error)
	Authenticate(ctx context.Context, token string) (*model.APIKeyPrincipal, error)
}

type service struct {
	txm           postgres.TransactionManager
	apiKeysRepo   apikeys.Repository
	rolesSvc      rolessvc.Service
	eventProducer workerproducer.EventProducer
}

func New(
	txm postgres.TransactionManager,
	apiKeysRepo apikeys.Repository,
	rolesSvc rolessvc.Service,
	eventProducer workerproducer.EventProducer,
) Service {
	return &service{
		txm:           txm,
		apiKeysRepo:   apiKeysRepo,
		rolesSvc:      rolesSvc,
		eventProducer: eventProducer,
	}
}
