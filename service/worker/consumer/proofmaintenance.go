package consumer

import (
	"context"
	"log/slog"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/riverqueue/river"
)

type proofObservationMaintenanceWorker struct {
	river.WorkerDefaults[jobs.ProofObservationMaintenanceArgs]
	maintenance *proofevidencesvc.Maintenance
	logger      *slog.Logger
}

func NewProofObservationMaintenanceWorker(maintenance *proofevidencesvc.Maintenance, logger *slog.Logger) (river.Worker[jobs.ProofObservationMaintenanceArgs], error) {
	if maintenance == nil || logger == nil {
		return nil, xerror.Unexpected().Detail("Proof maintenance worker dependencies are required")
	}
	return &proofObservationMaintenanceWorker{maintenance: maintenance, logger: logger}, nil
}

func (w *proofObservationMaintenanceWorker) Work(ctx context.Context, _ *river.Job[jobs.ProofObservationMaintenanceArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := w.maintenance.Run(ctx); err != nil {
		w.logger.ErrorContext(ctx, "proof observation maintenance failed")
		// Database diagnostics must not enter River's durable job error payload.
		return xerror.Unavailable(xerror.Pipeline).Detail("Proof observation maintenance failed")
	}
	w.logger.InfoContext(ctx, "proof observation maintenance completed")
	return nil
}
