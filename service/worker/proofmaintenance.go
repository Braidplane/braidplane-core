package worker

import (
	"log/slog"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/riverqueue/river"
)

type ProofObservationMaintenanceRegistrar struct {
	worker river.Worker[jobs.ProofObservationMaintenanceArgs]
}

func NewProofObservationMaintenanceRegistrar(maintenance *proofevidencesvc.Maintenance, logger *slog.Logger) (*ProofObservationMaintenanceRegistrar, error) {
	worker, err := consumer.NewProofObservationMaintenanceWorker(maintenance, logger)
	if err != nil {
		return nil, err
	}
	return &ProofObservationMaintenanceRegistrar{worker: worker}, nil
}

func (r *ProofObservationMaintenanceRegistrar) Register(workers *river.Workers) error {
	if workers == nil {
		return xerror.Unexpected().Detail("River workers registry is required")
	}
	if err := river.AddWorkerSafely(workers, r.worker); err != nil {
		return xerror.Wrapf("register proof observation maintenance worker: %w", err)
	}
	return nil
}

func (*ProofObservationMaintenanceRegistrar) PeriodicJobs() ([]*river.PeriodicJob, error) {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
		args := jobs.ProofObservationMaintenanceArgs{}
		opts := args.InsertOpts()
		return args, &opts
	}, &river.PeriodicJobOpts{ID: jobs.ProofObservationMaintenanceArgs{}.Kind(), RunOnStart: true})}, nil
}
