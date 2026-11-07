package consumer

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func NewExecutionRequestWorker(
	processor executiondelivery.Processor,
) river.Worker[jobs.ExecutionRequestArgs] {
	if processor == nil {
		panic("nil execution processor passed to NewExecutionRequestWorker")
	}
	return &executionRequestWorker{processor: processor}
}

type executionRequestWorker struct {
	river.WorkerDefaults[jobs.ExecutionRequestArgs]
	processor executiondelivery.Processor
}

func (w *executionRequestWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.ExecutionRequestArgs],
) error {
	result, err := w.processor.Process(ctx, job.Args.RequestID)
	if err != nil {
		return err
	}
	switch result.Disposition {
	case executiondelivery.ProcessDone:
		return nil
	case executiondelivery.ProcessRetryAt:
		delay := time.Until(result.RetryAt)
		if delay < 0 {
			delay = 0
		}
		return xerror.Wrapf("snooze execution request: %w", river.JobSnooze(delay))
	default:
		return xerror.Unexpected().Detail("invalid execution process disposition")
	}
}
