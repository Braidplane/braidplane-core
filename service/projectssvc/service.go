package projectssvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

type Service interface {
	CreateProject(
		ctx context.Context,
		orgSlug slug.Slug,
		newProject model.NewProject,
		actor model.EventActor,
	) (*model.Project, error)
	GetProject(ctx context.Context, orgSlug, projectSlug slug.Slug) (*model.Project, error)
	ListProjects(
		ctx context.Context,
		orgSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.Project], error)
	UpdateProject(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		update model.UpdateProject,
		actor model.EventActor,
	) (*model.Project, error)
	SetProjectPipelineBackend(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		params model.SetProjectPipelineBackend,
	) (*model.Project, error)
	DeleteProject(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		options model.DeleteProjectOptions,
		actor model.EventActor,
	) error
}

type service struct {
	txm                 postgres.TransactionManager
	projectsRepo        projects.Repository
	orgsRepo            projectBackendOrganizationRepository
	environmentsRepo    defaultProjectEnvironmentRepository
	dataSourcesService  pipelineBackendDataSourceService
	pipelineStore       projectPipelineStore
	pipelineGitProducer workerproducer.PipelineGitProducer
	repositoryManager   pipelinegit.RepositoryManager
	eventProducer       workerproducer.EventProducer
	remoteGitExecutor   pipelinegit.RemoteSourceExecutor
	remoteGitMaxBytes   int64
}

type projectBackendOrganizationRepository interface {
	FindBySlug(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) (*organizations.Organization, error)
}

type defaultProjectEnvironmentRepository interface {
	FindBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, environmentSlug slug.Slug,
	) (*environments.Environment, error)
	EnableProjectEnvironment(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params *environments.EnableProjectParams,
	) (*environments.ProjectEnvironment, error)
}

type pipelineBackendDataSourceService interface {
	FindBySlug(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
	) (*datasourcessvc.DataSource, error)
	ResolveGitAuth(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
	) (*datasourcessvc.GitDataSourceAuth, error)
	ResolveGitExecutionAuth(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
	) (*datasourcessvc.GitExecutionAuth, error)
}

// Option specializes project service composition without widening the public
// service contract used by tests and local combined-mode development.
type Option func(*service)

// WithRemotePipelineBackendExecution routes project-backend adoption through
// the isolated executor instead of cloning tenant-controlled Git in-process.
func WithRemotePipelineBackendExecution(executor pipelinegit.RemoteSourceExecutor, maxBytes int64) Option {
	return func(s *service) {
		s.remoteGitExecutor = executor
		s.remoteGitMaxBytes = maxBytes
	}
}

type projectPipelineStore interface {
	ReconcileFromGit(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		repoStore repostore.RepoStore,
		params pipelinesvc.ReconcileFromGitParams,
	) (*pipelinesvc.ReconcileFromGitResult, error)
	List(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		req model.ListRequest,
	) ([]*pipelinesrepo.PipelineSpec, error)
}

func New(
	txm postgres.TransactionManager,
	projectsRepo projects.Repository,
	orgsRepo projectBackendOrganizationRepository,
	environmentsRepo defaultProjectEnvironmentRepository,
	dataSourcesService pipelineBackendDataSourceService,
	pipelineStore projectPipelineStore,
	pipelineGitProducer workerproducer.PipelineGitProducer,
	repositoryManager pipelinegit.RepositoryManager,
	eventProducer workerproducer.EventProducer,
	options ...Option,
) Service {
	svc := &service{
		txm:                 txm,
		projectsRepo:        projectsRepo,
		orgsRepo:            orgsRepo,
		environmentsRepo:    environmentsRepo,
		dataSourcesService:  dataSourcesService,
		pipelineStore:       pipelineStore,
		pipelineGitProducer: pipelineGitProducer,
		repositoryManager:   repositoryManager,
		eventProducer:       eventProducer,
	}
	for _, option := range options {
		option(svc)
	}
	return svc
}
