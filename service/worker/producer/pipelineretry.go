package producer

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

// PipelineRetryProducer enqueues trigger retry execution for background workers.
type PipelineRetryProducer interface {
	EnqueueTriggerRetryExecution(
		ctx context.Context,
		tx postgres.DBTX,
		args jobs.PipelineTriggerRetryExecutionArgs,
	) (queue.QueuedJob, error)
}

type pipelineRetryProducer struct {
	enqueuer queue.Enqueuer
	queue    string
}

// NewPipelineRetryProducer creates a producer for the default retry queue.
func NewPipelineRetryProducer(enqueuer queue.Enqueuer) (PipelineRetryProducer, error) {
	return NewPipelineRetryProducerForQueue(enqueuer, jobs.PipelineRetryQueue)
}

// NewPipelineRetryProducerForQueue creates a producer for tests and custom worker queues.
func NewPipelineRetryProducerForQueue(enqueuer queue.Enqueuer, queueName string) (PipelineRetryProducer, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("queue enqueuer is required")
	}
	if strings.TrimSpace(queueName) == "" {
		return nil, xerror.Unexpected().Detail("pipeline retry queue is required")
	}

	return &pipelineRetryProducer{enqueuer: enqueuer, queue: queueName}, nil
}

// EnqueueTriggerRetryExecution inserts one trigger retry execution job in tx.
func (p *pipelineRetryProducer) EnqueueTriggerRetryExecution(
	ctx context.Context,
	tx postgres.DBTX,
	args jobs.PipelineTriggerRetryExecutionArgs,
) (queue.QueuedJob, error) {
	return p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args:  args,
		Queue: p.queue,
	})
}
