package consumer

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type pipelineRetryDispatcher interface {
	ExecuteTriggerRetryAttempt(ctx context.Context, args jobs.PipelineTriggerRetryExecutionArgs) error
}

// NewPipelineTriggerRetryExecutionWorker creates the worker that executes one trigger retry attempt.
func NewPipelineTriggerRetryExecutionWorker(
	service pipelineRetryDispatcher,
	logger *slog.Logger,
) river.Worker[jobs.PipelineTriggerRetryExecutionArgs] {
	if service == nil {
		panic("nil pipelineRetryDispatcher passed to NewPipelineTriggerRetryExecutionWorker")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &pipelineTriggerRetryExecutionWorker{
		service: service,
		logger:  logger,
	}
}

type pipelineTriggerRetryExecutionWorker struct {
	river.WorkerDefaults[jobs.PipelineTriggerRetryExecutionArgs]
	service pipelineRetryDispatcher
	logger  *slog.Logger
}

// Work executes one persisted trigger retry attempt and logs safe retry identifiers.
func (w *pipelineTriggerRetryExecutionWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.PipelineTriggerRetryExecutionArgs],
) error {
	if err := w.service.ExecuteTriggerRetryAttempt(ctx, job.Args); err != nil {
		return err
	}
	w.logger.InfoContext(
		ctx,
		"pipeline trigger retry attempt executed",
		slog.String("organization_slug", job.Args.OrganizationSlug.String()),
		slog.String("project_slug", job.Args.ProjectSlug.String()),
		slog.String("environment_slug", job.Args.EnvironmentSlug.String()),
		slog.String("retry_attempt_slug", job.Args.RetryAttemptSlug.String()),
		slog.Time("scheduled_at", job.Args.ScheduledAt),
	)
	return nil
}
