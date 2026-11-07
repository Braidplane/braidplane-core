package datasourcerefssvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasourcerefs"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

//go:generate -command enumer go tool -modfile=../../tools/go.mod enumer
//go:generate enumer -type=PipelineDataSourceRefSyncStatus -json -text -linecomment -output=pipeline_data_source_ref_sync_status_enumer.go

type Service interface {
	CreateDataSourceRef(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		newRef model.NewDataSourceRef,
		actor model.EventActor,
	) (*model.DataSourceRef, error)
	GetDataSourceRef(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
	) (*model.DataSourceRef, error)
	ListDataSourceRefs(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.DataSourceRef], error)
	UpdateDataSourceRef(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
		update model.UpdateDataSourceRef,
		actor model.EventActor,
	) (*model.DataSourceRef, error)
	DeleteDataSourceRef(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
		actor model.EventActor,
	) error
	UpsertEnvironmentBinding(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug,
		binding model.UpsertDataSourceBinding,
		actor model.EventActor,
	) (*model.DataSourceBinding, error)
	GetEnvironmentBinding(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug,
	) (*model.DataSourceBinding, error)
	ListEnvironmentBindings(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.DataSourceBinding], error)
	DeleteEnvironmentBinding(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug,
		confirmation *slug.Slug,
		actor model.EventActor,
	) error
	ResolvePipelineDataSourceRefs(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		pipeline *pipelinespec.Pipeline,
	) (*ResolvedPipeline, error)
	SyncPipelineDataSourceRefs(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		pipeline *pipelinespec.Pipeline,
		actor model.EventActor,
	) (*PipelineDataSourceRefSyncResult, error)
	ReconcilePipelineDataSourceRefs(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		pipeline *pipelinespec.Pipeline,
		actor model.EventActor,
	) (*PipelineDataSourceRefSyncResult, error)
	DeleteDataSourceRefsForPipeline(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		actor model.EventActor,
	) error
}

type ResolvedPipeline struct {
	Inputs      []deploymentplan.Input
	Pipeline    *pipelinespec.Pipeline
	Resolutions []model.ResolvedPipelineDataSourceRef
}

// PipelineDataSourceRefSyncStatus describes the idempotent ref sync action.
type PipelineDataSourceRefSyncStatus int

const (
	PipelineDataSourceRefSyncStatusCreated   PipelineDataSourceRefSyncStatus = iota // created
	PipelineDataSourceRefSyncStatusUpdated                                          // updated
	PipelineDataSourceRefSyncStatusUnchanged                                        // unchanged
)

// PipelineDataSourceRefSyncItem reports one logical ref touched by spec sync.
type PipelineDataSourceRefSyncItem struct {
	PipelineSlug         slug.Slug
	RefSlug              slug.Slug
	DataSourceType       model.DataSourceType
	RequiredCapabilities []model.DataSourceRefCapability
	Status               PipelineDataSourceRefSyncStatus
}

// PipelineDataSourceRefSyncResult reports logical refs found in a pipeline spec.
type PipelineDataSourceRefSyncResult struct {
	Items []PipelineDataSourceRefSyncItem
}

type service struct {
	txm           postgres.TransactionManager
	repo          datasourcerefs.Repository
	dataSources   datasourcessvc.Service
	eventProducer workerproducer.EventProducer
}

func New(
	txm postgres.TransactionManager,
	repo datasourcerefs.Repository,
	dataSources datasourcessvc.Service,
	eventProducer workerproducer.EventProducer,
) Service {
	return &service{
		txm:           txm,
		repo:          repo,
		dataSources:   dataSources,
		eventProducer: eventProducer,
	}
}
