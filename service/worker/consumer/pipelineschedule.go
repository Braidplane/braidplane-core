package consumer

import (
	"context"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/service/pipelineschedulesvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type scheduleDispatchService interface {
	DispatchDueSchedules(ctx context.Context, now time.Time) (pipelineschedulesvc.DispatchResult, error)
}

type scheduleFireExecutionService interface {
	ExecuteClaimedScheduleFire(ctx context.Context, args jobs.PipelineScheduleFireExecutionArgs) error
}

// NewPipelineScheduleDispatchWorker creates the worker that hands periodic scans to the schedule service.
func NewPipelineScheduleDispatchWorker(
	service scheduleDispatchService,
	clk clock.Clock,
	logger *slog.Logger,
) river.Worker[jobs.PipelineScheduleDispatchArgs] {
	if service == nil {
		panic("nil scheduleDispatchService passed to NewPipelineScheduleDispatchWorker")
	}
	if clk == nil {
		clk = clock.New()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &pipelineScheduleDispatchWorker{
		service: service,
		clock:   clk,
		logger:  logger,
	}
}

type pipelineScheduleDispatchWorker struct {
	river.WorkerDefaults[jobs.PipelineScheduleDispatchArgs]

	service scheduleDispatchService
	clock   clock.Clock
	logger  *slog.Logger
}

func (w *pipelineScheduleDispatchWorker) Work(
	ctx context.Context,
	_ *river.Job[jobs.PipelineScheduleDispatchArgs],
) error {
	result, err := w.service.DispatchDueSchedules(ctx, w.clock.Now().UTC())
	if err != nil {
		w.logger.ErrorContext(ctx, "pipeline schedule dispatch job failed", slog.Any("error", err))
		return err
	}

	w.logger.InfoContext(
		ctx,
		"pipeline schedule dispatch job succeeded",
		slog.Int("scanned", result.Scanned),
		slog.Int("claimed", result.Claimed),
		slog.Int("enqueued", result.Enqueued),
		slog.Int("skipped_disabled", result.SkippedDisabled),
		slog.Int("skipped_misfire", result.SkippedMisfire),
		slog.Int("already_claimed", result.AlreadyClaimed),
	)
	return nil
}

// NewPipelineScheduleFireExecutionWorker creates the worker that executes one claimed schedule fire.
func NewPipelineScheduleFireExecutionWorker(
	service scheduleFireExecutionService,
	logger *slog.Logger,
) river.Worker[jobs.PipelineScheduleFireExecutionArgs] {
	if service == nil {
		panic("nil scheduleFireExecutionService passed to NewPipelineScheduleFireExecutionWorker")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &pipelineScheduleFireExecutionWorker{
		service: service,
		logger:  logger,
	}
}

type pipelineScheduleFireExecutionWorker struct {
	river.WorkerDefaults[jobs.PipelineScheduleFireExecutionArgs]

	service scheduleFireExecutionService
	logger  *slog.Logger
}

func (w *pipelineScheduleFireExecutionWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.PipelineScheduleFireExecutionArgs],
) error {
	if err := w.service.ExecuteClaimedScheduleFire(ctx, job.Args); err != nil {
		w.logger.ErrorContext(ctx, "pipeline schedule fire execution job failed", slog.Any("error", err))
		return err
	}

	w.logger.InfoContext(
		ctx,
		"pipeline schedule fire execution job succeeded",
		slog.String("organization_slug", job.Args.OrganizationSlug.String()),
		slog.String("project_slug", job.Args.ProjectSlug.String()),
		slog.String("environment_slug", job.Args.EnvironmentSlug.String()),
		slog.String("trigger_slug", job.Args.TriggerSlug.String()),
		slog.Int64("trigger_revision", job.Args.TriggerRevision),
		slog.Time("scheduled_fire_at", job.Args.ScheduledFireAt),
	)
	return nil
}
