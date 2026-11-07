package datasourcerefs

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	CreateDataSourceRef(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		params CreateDataSourceRefParams,
	) (*DataSourceRef, error)
	FindDataSourceRefBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug, dataSourceRefSlug slug.Slug,
	) (*DataSourceRef, error)
	ListDataSourceRefs(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		req model.ListRequest,
	) ([]*DataSourceRef, error)
	ListDataSourceRefsByPipeline(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) ([]*DataSourceRef, error)
	UpdateDataSourceRef(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug, dataSourceRefSlug slug.Slug,
		params UpdateDataSourceRefParams,
	) (*DataSourceRef, error)
	DeleteDataSourceRef(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug, dataSourceRefSlug slug.Slug,
	) error
	DeleteDataSourceRefsForPipeline(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) error
	ListProtectedBindingEnvironmentsForDataSourceRef(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug, dataSourceRefSlug slug.Slug,
	) ([]model.ProjectEnvironmentProtection, error)
	ProjectEnvironmentEnabled(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) (bool, error)
	FindProjectEnvironmentProtection(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) (*model.ProjectEnvironmentProtection, error)
	UpsertEnvironmentBinding(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug, environmentSlug, dataSourceRefSlug slug.Slug,
		params UpsertEnvironmentBindingParams,
	) (*DataSourceBinding, error)
	FindEnvironmentBinding(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug, environmentSlug, dataSourceRefSlug slug.Slug,
	) (*DataSourceBinding, error)
	ListEnvironmentBindings(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		req model.ListRequest,
	) ([]*DataSourceBinding, error)
	ListEnvironmentBindingsForDataSourceRef(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug, dataSourceRefSlug slug.Slug,
	) ([]*DataSourceBinding, error)
	ResolveEnvironmentBinding(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug, environmentSlug, dataSourceRefSlug slug.Slug,
	) (*DataSourceBinding, error)
	ListRuntimeVariables(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) ([]model.RuntimeVariable, error)
	DeleteEnvironmentBinding(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug, environmentSlug, dataSourceRefSlug slug.Slug,
	) error
}

func NewRepository() Repository {
	return &repository{}
}
