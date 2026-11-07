package producer

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

// PipelineScheduleProducer enqueues scheduled fire execution for background workers.
type PipelineScheduleProducer interface {
	EnqueueRunFinalization(context.Context, postgres.DBTX, queue.JobArgs) (queue.QueuedJob, error)
	EnqueueScheduleFireExecution(
		ctx context.Context,
		tx postgres.DBTX,
		args jobs.PipelineScheduleFireExecutionArgs,
	) (queue.QueuedJob, error)
}

type pipelineScheduleProducer struct {
	enqueuer queue.Enqueuer
	queue    string
}

// NewPipelineScheduleProducer creates a producer for the default schedule queue.
func NewPipelineScheduleProducer(enqueuer queue.Enqueuer) (PipelineScheduleProducer, error) {
	return NewPipelineScheduleProducerForQueue(enqueuer, jobs.PipelineScheduleQueue)
}

// NewPipelineScheduleProducerForQueue creates a producer for tests and custom worker queues.
func NewPipelineScheduleProducerForQueue(enqueuer queue.Enqueuer, queueName string) (PipelineScheduleProducer, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("queue enqueuer is required")
	}
	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		return nil, xerror.Unexpected().Detail("pipeline schedule queue is required")
	}

	return &pipelineScheduleProducer{enqueuer: enqueuer, queue: queueName}, nil
}

// EnqueueScheduleFireExecution inserts one claimed scheduled fire execution job in tx.
func (p *pipelineScheduleProducer) EnqueueScheduleFireExecution(
	ctx context.Context,
	tx postgres.DBTX,
	args jobs.PipelineScheduleFireExecutionArgs,
) (queue.QueuedJob, error) {
	return p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args:  args,
		Queue: p.queue,
	})
}

// EnqueueRunFinalization restores a source-owner job only after terminal run
// persistence. Those workers resume the exact attempt without connector calls.
func (p *pipelineScheduleProducer) EnqueueRunFinalization(ctx context.Context, tx postgres.DBTX, args queue.JobArgs) (queue.QueuedJob, error) {
	queueName := p.queue
	if _, ok := args.(jobs.PipelineTriggerRetryExecutionArgs); ok {
		queueName = jobs.PipelineRetryQueue
	}
	return p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{Args: args, Queue: queueName})
}
