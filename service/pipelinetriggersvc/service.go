package pipelinetriggersvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

// Service manages environment-scoped pipeline trigger definitions.
type Service interface {
	CheckCompletionSnapshot(context.Context, slug.Slug, slug.Slug, slug.Slug, int) (*model.PipelineCompletionSnapshot, error)
	CreatePipelineTrigger(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		newTrigger model.NewPipelineTrigger,
		actor model.EventActor,
	) (*model.PipelineTrigger, error)
	GetPipelineTrigger(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	) (*model.PipelineTrigger, error)
	ListPipelineTriggers(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.PipelineTrigger], error)
	ListPipelineScheduleFires(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.PipelineScheduleFire], error)
	ListPipelineRunTriggerRetryAttempts(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, runSlug slug.Slug,
	) ([]model.PipelineTriggerRetryAttempt, error)
	UpdatePipelineTrigger(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
		update model.UpdatePipelineTrigger,
		actor model.EventActor,
	) (*model.PipelineTrigger, error)
	DeletePipelineTrigger(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
		actor model.EventActor,
	) error
	RotatePipelineWebhookSecret(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
		actor model.EventActor,
	) (model.PipelineWebhookSecret, error)
}

type CompletionExecutionValidator interface {
	ValidateCompletionExecutionContext(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot) error
}

type service struct {
	completionValidator       CompletionExecutionValidator
	completionTriggersEnabled bool
	txm                       postgres.TransactionManager
	repo                      pipelinetriggers.Repository
	eventProducer             workerproducer.EventProducer
	webhookSecrets            credentialssvc.PipelineWebhookSecretLifecycleService
}

// New creates a pipeline trigger service with explicit persistence and event dependencies.
func New(
	completionTriggersEnabled bool,
	txm postgres.TransactionManager,
	repo pipelinetriggers.Repository,
	completionValidator CompletionExecutionValidator,
	eventProducer workerproducer.EventProducer,
	webhookSecrets credentialssvc.PipelineWebhookSecretLifecycleService,
) Service {
	return &service{
		completionValidator:       completionValidator,
		completionTriggersEnabled: completionTriggersEnabled,
		txm:                       txm,
		repo:                      repo,
		eventProducer:             eventProducer,
		webhookSecrets:            webhookSecrets,
	}
}
