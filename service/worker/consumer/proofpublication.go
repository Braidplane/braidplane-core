package consumer

import (
	"context"
	"log/slog"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/riverqueue/river"
)

type ProofPublicationProcessor interface {
	Refresh(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (bool, error)
}

type proofPublicationWorker struct {
	river.WorkerDefaults[jobs.ProofPublicationArgs]
	processor ProofPublicationProcessor
	logger    *slog.Logger
}

func NewProofPublicationWorker(processor ProofPublicationProcessor, logger *slog.Logger) (river.Worker[jobs.ProofPublicationArgs], error) {
	if processor == nil || logger == nil {
		return nil, xerror.Unexpected().Detail("Proof publication worker dependencies are required")
	}
	return &proofPublicationWorker{processor: processor, logger: logger}, nil
}

func (w *proofPublicationWorker) Work(ctx context.Context, job *river.Job[jobs.ProofPublicationArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a := job.Args
	again, err := w.processor.Refresh(ctx, a.Organization, a.Project, a.Environment, a.Receipt)
	if err != nil {
		// Provider and database diagnostics must not enter durable queue errors or logs.
		w.logger.WarnContext(ctx, "Proof check publication unavailable")
		return xerror.Wrapf("retry Proof publication: %w", river.JobSnooze(time.Minute))
	}
	if again {
		// ponytail: poll retained receipts once per minute; batch reconciliation if volume grows.
		return xerror.Wrapf("refresh Proof publication: %w", river.JobSnooze(time.Minute))
	}
	return nil
}
