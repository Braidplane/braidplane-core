package producer

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type PipelineWebhookProducer interface {
	EnqueuePipelineWebhookDelivery(context.Context, postgres.DBTX, jobs.PipelineWebhookDeliveryArgs) (queue.QueuedJob, error)
}

type pipelineWebhookProducer struct{ enqueuer queue.Enqueuer }

func NewPipelineWebhookProducer(enqueuer queue.Enqueuer) (PipelineWebhookProducer, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("queue enqueuer is required")
	}
	return &pipelineWebhookProducer{enqueuer: enqueuer}, nil
}

func (p *pipelineWebhookProducer) EnqueuePipelineWebhookDelivery(
	ctx context.Context,
	tx postgres.DBTX,
	args jobs.PipelineWebhookDeliveryArgs,
) (queue.QueuedJob, error) {
	return p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{Args: args, Queue: jobs.PipelineScheduleQueue})
}
