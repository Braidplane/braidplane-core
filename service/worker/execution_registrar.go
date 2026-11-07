package worker

import (
	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type ExecutionRegistrar struct {
	processor  executiondelivery.Processor
	maxWorkers int
}

func NewExecutionRegistrar(
	processor executiondelivery.Processor,
	maxWorkers int,
) (*ExecutionRegistrar, error) {
	if processor == nil {
		return nil, xerror.Unexpected().Detail("execution processor is required")
	}
	if maxWorkers <= 0 {
		return nil, xerror.Unexpected().Detail("execution River queue max workers must be positive")
	}
	return &ExecutionRegistrar{processor: processor, maxWorkers: maxWorkers}, nil
}

func (r *ExecutionRegistrar) Register(workers *river.Workers) error {
	if workers == nil {
		return xerror.Unexpected().Detail("River workers registry is required")
	}
	if err := river.AddWorkerSafely(workers, consumer.NewExecutionRequestWorker(r.processor)); err != nil {
		return xerror.Wrapf("register execution request worker: %w", err)
	}
	return nil
}

func (r *ExecutionRegistrar) Queues() map[string]river.QueueConfig {
	return map[string]river.QueueConfig{
		jobs.ExecutionQueue: {MaxWorkers: r.maxWorkers},
	}
}
