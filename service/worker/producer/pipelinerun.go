package producer

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

// PipelineRunProducer enqueues manual/API pipeline run execution for background workers.
type PipelineRunProducer interface {
	EnqueuePipelineRunExecution(
		ctx context.Context,
		tx postgres.DBTX,
		args jobs.PipelineRunExecutionArgs,
	) (queue.QueuedJob, error)
}

type runJobBinder interface {
	BindWorkerJob(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string, int64) error
}

type pipelineRunProducer struct {
	runs     runJobBinder
	enqueuer queue.Enqueuer
	queue    string
}

// NewPipelineRunProducer creates a producer for the default pipeline run queue.
func NewPipelineRunProducer(enqueuer queue.Enqueuer, runs runJobBinder) (PipelineRunProducer, error) {
	return NewPipelineRunProducerForQueue(enqueuer, runs, jobs.PipelineRunQueue)
}

// NewPipelineRunProducerForQueue creates a producer for tests and custom worker queues.
func NewPipelineRunProducerForQueue(enqueuer queue.Enqueuer, runs runJobBinder, queueName string) (PipelineRunProducer, error) {
	if enqueuer == nil || runs == nil {
		return nil, xerror.Unexpected().Detail("queue enqueuer and run job repository are required")
	}
	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		return nil, xerror.Unexpected().Detail("pipeline run queue is required")
	}

	return &pipelineRunProducer{enqueuer: enqueuer, runs: runs, queue: queueName}, nil
}

// EnqueuePipelineRunExecution inserts one manual/API pipeline run execution job in tx.
func (p *pipelineRunProducer) EnqueuePipelineRunExecution(
	ctx context.Context,
	tx postgres.DBTX,
	args jobs.PipelineRunExecutionArgs,
) (queue.QueuedJob, error) {
	job, err := p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args:              args,
		Queue:             p.queue,
		InitiatingAccount: args.Actor.UserSlugValue(),
	})
	if err != nil {
		return queue.QueuedJob{}, err
	}
	if err := p.runs.BindWorkerJob(ctx, tx, args.OrganizationSlug, args.ProjectSlug, args.EnvironmentSlug, args.RunSlug.String(), job.ID); err != nil {
		return queue.QueuedJob{}, xerror.Wrapf("bind pipeline run worker: %w", err)
	}
	return job, nil
}
