package worker

import (
	"time"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type CompletionDispatchRegistrar struct {
	worker river.Worker[jobs.PipelineCompletionDispatchArgs]
}

func NewCompletionDispatchRegistrar(dispatcher consumer.CompletionDispatcher) *CompletionDispatchRegistrar {
	return &CompletionDispatchRegistrar{worker: consumer.NewCompletionDispatchWorker(dispatcher)}
}

func (r *CompletionDispatchRegistrar) Register(workers *river.Workers) error {
	if workers == nil {
		return xerror.Unexpected().Detail("River workers registry is required")
	}
	if err := river.AddWorkerSafely(workers, r.worker); err != nil {
		return xerror.Wrapf("register completion dispatch worker: %w", err)
	}
	return nil
}

func (*CompletionDispatchRegistrar) PeriodicJobs() ([]*river.PeriodicJob, error) {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
		args := jobs.PipelineCompletionDispatchArgs{}
		opts := args.InsertOpts()
		return args, &opts
	}, &river.PeriodicJobOpts{ID: jobs.PipelineCompletionDispatchArgs{}.Kind(), RunOnStart: true})}, nil
}
