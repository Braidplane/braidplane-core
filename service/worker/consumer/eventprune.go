package consumer

import (
	"context"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type eventPruneService interface {
	PruneExpiredCustomerData(ctx context.Context, execution retentionsvc.MaintenanceExecution) (retentionsvc.Result, error)
}

func NewEventPruneWorker(service eventPruneService, logger *slog.Logger) river.Worker[jobs.EventPruneArgs] {
	if logger == nil {
		logger = slog.Default()
	}

	return &eventPruneWorker{
		service: service,
		logger:  logger,
	}
}

type eventPruneWorker struct {
	river.WorkerDefaults[jobs.EventPruneArgs]

	service eventPruneService
	logger  *slog.Logger
}

func (w *eventPruneWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.EventPruneArgs],
) error {
	execution, err := maintenanceExecution(job)
	if err != nil {
		return err
	}
	result, err := w.service.PruneExpiredCustomerData(ctx, execution)
	if err != nil {
		w.logger.ErrorContext(ctx, "event prune job failed", slog.Any("error", err))
		return err
	}

	w.logger.InfoContext(ctx, "customer-data retention job succeeded", slog.Int64("wishlist_registrations", result.WishlistRegistrations), slog.Int64("abuse_control_buckets", result.AbuseControlBuckets), slog.Int64("query_executions", result.QueryExecutions), slog.Int64("replay_checkpoints", result.ReplayCheckpoints), slog.Int64("pipeline_runs", result.PipelineRuns), slog.Int64("events", result.Events), slog.Any("skipped_organizations", result.SkippedOrgs))
	return nil
}

func maintenanceExecution(job *river.Job[jobs.EventPruneArgs]) (retentionsvc.MaintenanceExecution, error) {
	if job == nil || job.JobRow == nil || job.ID <= 0 || job.Attempt <= 0 {
		return retentionsvc.MaintenanceExecution{}, xerror.Unexpected().Detail("retention worker job metadata is invalid")
	}
	startedAt := time.Now().UTC()
	if job.AttemptedAt != nil {
		startedAt = job.AttemptedAt.UTC()
	}
	return retentionsvc.MaintenanceExecution{
		SourceJobID: job.ID,
		Attempt:     job.Attempt,
		StartedAt:   startedAt,
	}, nil
}
