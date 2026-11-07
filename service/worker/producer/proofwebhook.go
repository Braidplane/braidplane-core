package producer

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type ProofWebhookProducer struct{ enqueuer queue.Enqueuer }

func NewProofWebhookProducer(enqueuer queue.Enqueuer) (*ProofWebhookProducer, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("queue enqueuer is required")
	}
	return &ProofWebhookProducer{enqueuer: enqueuer}, nil
}

func (p *ProofWebhookProducer) EnqueueProofChange(
	ctx context.Context,
	tx postgres.DBTX,
	event gitprovider.VerifiedChangeEvent,
) (queue.QueuedJob, error) {
	queued, err := p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args: jobs.ProofWebhookArgs{
			DeliveryID: event.DeliveryID, BodySHA256: event.BodySHA256, Action: event.Action, Provider: event.Provider,
			OrganizationSlug:     event.OrganizationSlug,
			ProviderConnectionID: event.ProviderConnectionID, RepositoryID: event.RepositoryID,
			ChangeRequestID: event.ChangeRequestID, HeadSHA: event.HeadSHA,
			SourceRef: event.SourceRef, TargetRef: event.TargetRef,
		},
		Queue: jobs.PipelineGitQueue,
	})
	if err != nil {
		return queue.QueuedJob{}, xerror.Wrapf("enqueue proof webhook delivery: %w", err)
	}
	return queued, nil
}
