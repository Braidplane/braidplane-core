package consumer

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type pipelineRunExecutor interface {
	ExecuteQueuedPipelineRun(ctx context.Context, args jobs.PipelineRunExecutionArgs) error
}

// NewPipelineRunExecutionWorker creates the worker that executes one accepted manual/API run.
func NewPipelineRunExecutionWorker(
	service pipelineRunExecutor,
	logger *slog.Logger,
) river.Worker[jobs.PipelineRunExecutionArgs] {
	if service == nil {
		panic("nil pipelineRunExecutor passed to NewPipelineRunExecutionWorker")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &pipelineRunExecutionWorker{
		service: service,
		logger:  logger,
	}
}

type pipelineRunExecutionWorker struct {
	river.WorkerDefaults[jobs.PipelineRunExecutionArgs]
	service pipelineRunExecutor
	logger  *slog.Logger
}

// Work executes one previously accepted run and logs only safe run identifiers.
func (w *pipelineRunExecutionWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.PipelineRunExecutionArgs],
) error {
	if err := w.service.ExecuteQueuedPipelineRun(ctx, job.Args); err != nil {
		w.logger.ErrorContext(
			ctx,
			"pipeline run execution job failed",
			slog.Any("error", err),
			slog.String("organization_slug", job.Args.OrganizationSlug.String()),
			slog.String("project_slug", job.Args.ProjectSlug.String()),
			slog.String("environment_slug", job.Args.EnvironmentSlug.String()),
			slog.String("pipeline_slug", job.Args.PipelineSlug.String()),
			slog.String("run_slug", job.Args.RunSlug.String()),
		)
		return err
	}

	w.logger.InfoContext(
		ctx,
		"pipeline run execution job succeeded",
		slog.String("organization_slug", job.Args.OrganizationSlug.String()),
		slog.String("project_slug", job.Args.ProjectSlug.String()),
		slog.String("environment_slug", job.Args.EnvironmentSlug.String()),
		slog.String("pipeline_slug", job.Args.PipelineSlug.String()),
		slog.String("run_slug", job.Args.RunSlug.String()),
	)
	return nil
}
