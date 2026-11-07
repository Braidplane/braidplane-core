package worker

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/accountclosuresvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/riverqueue/river"
)

type AccountDeletionWorker struct {
	river.WorkerDefaults[jobs.AccountDeletionArgs]
	Service *accountclosuresvc.Service
}

func (w *AccountDeletionWorker) Register(workers *river.Workers) error {
	if w.Service == nil || workers == nil {
		return xerror.Unexpected().Detail("account deletion worker dependencies are required")
	}
	if err := river.AddWorkerSafely(workers, w); err != nil {
		return xerror.Wrapf("register account deletion worker: %w", err)
	}
	return nil
}

func (w *AccountDeletionWorker) Work(ctx context.Context, job *river.Job[jobs.AccountDeletionArgs]) error {
	completed, err := w.Service.Complete(ctx, job.Args.Request)
	if err != nil {
		logging.LogError(ctx, "account deletion failed", err)
		// Never persist database diagnostics or account data in River errors.
		return xerror.Unavailable(xerror.User).Detail("account deletion failed")
	}
	if !completed {
		return xerror.Wrapf("retry account deletion: %w", river.JobSnooze(time.Minute))
	}
	return nil
}
