package environmentssvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

type Service interface {
	CreateDefaultEnvironments(ctx context.Context, orgSlug slug.Slug) error
	CreateEnvironment(
		ctx context.Context,
		orgSlug slug.Slug,
		newEnvironment model.NewEnvironment,
		actor model.EventActor,
	) (*model.Environment, error)
	GetEnvironment(ctx context.Context, orgSlug, environmentSlug slug.Slug) (*model.Environment, error)
	ListEnvironments(
		ctx context.Context,
		orgSlug slug.Slug,
		req model.ListEnvironmentsRequest,
	) (model.ListResponse[model.Environment], error)
	UpdateEnvironment(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
		update model.UpdateEnvironment,
		actor model.EventActor,
	) (*model.Environment, error)
	DeleteEnvironment(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
		deleted model.DeleteEnvironment,
		actor model.EventActor,
	) error
	EnableProjectEnvironment(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params model.EnableProjectEnvironment,
		actor model.EventActor,
	) (*model.ProjectEnvironment, error)
	ListProjectEnvironments(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.ProjectEnvironment], error)
	DisableProjectEnvironment(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		confirmation *slug.Slug,
		actor model.EventActor,
	) error
	UpsertRuntimeVariable(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
		params model.UpsertRuntimeVariable,
		actor model.EventActor,
	) (*model.RuntimeVariable, error)
	GetRuntimeVariable(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
		key string,
	) (*model.RuntimeVariable, error)
	ListRuntimeVariables(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.RuntimeVariable], error)
	DeleteRuntimeVariable(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
		key string,
		confirmation *slug.Slug,
		actor model.EventActor,
	) error
	UpsertEnvironmentSecret(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
		params model.UpsertEnvironmentSecret,
		actor model.EventActor,
	) (*model.EnvironmentSecret, error)
	GetEnvironmentSecret(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
		key string,
	) (*model.EnvironmentSecret, error)
	ListEnvironmentSecrets(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.EnvironmentSecret], error)
	DeleteEnvironmentSecret(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
		key string,
		confirmation *slug.Slug,
		actor model.EventActor,
	) error
	ResolveRuntimeSecrets(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
	) ([]model.RuntimeSecretValue, error)
}

type service struct {
	txm           postgres.TransactionManager
	environments  environments.Repository
	credentials   credentialssvc.Service
	eventProducer workerproducer.EventProducer
}

type Option func(*service)

// WithCredentialService attaches the shared credential lifecycle used by environment secrets.
func WithCredentialService(credentials credentialssvc.Service) Option {
	return func(s *service) {
		s.credentials = credentials
	}
}

func New(
	txm postgres.TransactionManager,
	environmentsRepo environments.Repository,
	eventProducer workerproducer.EventProducer,
	options ...Option,
) Service {
	s := &service{
		txm:           txm,
		environments:  environmentsRepo,
		eventProducer: eventProducer,
	}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	return s
}
