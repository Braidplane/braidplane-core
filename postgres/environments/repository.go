package environments

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	Create(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		params *CreateParams,
	) (*Environment, error)
	FindBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
	) (*Environment, error)
	FindAnyBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
	) (*Environment, error)
	ListByOrg(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		req model.ListEnvironmentsRequest,
	) ([]*Environment, error)
	Update(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
		params *UpdateParams,
	) (*Environment, error)
	Delete(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
		params *DeleteParams,
	) error
	EnableProjectEnvironment(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params *EnableProjectParams,
	) (*ProjectEnvironment, error)
	ListProjectEnvironments(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		req model.ListRequest,
	) ([]*ProjectEnvironment, error)
	FindProjectEnvironmentProtection(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) (*model.ProjectEnvironmentProtection, error)
	FindProjectEnvironmentCatalogProtection(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) (*model.ProjectEnvironmentProtection, error)
	DisableProjectEnvironment(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) error
	UpsertRuntimeVariable(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
		params model.UpsertRuntimeVariable,
	) (*model.RuntimeVariable, error)
	FindRuntimeVariable(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
		key string,
	) (*model.RuntimeVariable, error)
	ListRuntimeVariables(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
		req model.ListRequest,
	) ([]*model.RuntimeVariable, error)
	DeleteRuntimeVariable(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
		key string,
	) error
	UpsertEnvironmentSecret(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
		params model.UpsertEnvironmentSecret,
		credentialSlug slug.Slug,
	) (*model.EnvironmentSecret, error)
	FindEnvironmentSecret(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
		key string,
	) (*model.EnvironmentSecret, error)
	ListEnvironmentSecrets(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
		req model.ListRequest,
	) ([]*model.EnvironmentSecret, error)
	ListAllEnvironmentSecrets(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
	) ([]*model.EnvironmentSecret, error)
	DeleteEnvironmentSecretsForEnvironment(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
	) error
	DeleteEnvironmentSecret(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
		key string,
	) error
}

func NewRepository() Repository {
	return &repository{}
}
