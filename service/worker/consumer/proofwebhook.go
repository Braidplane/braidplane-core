package consumer

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	proofrepo "github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/service/proofwebhooksvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type ProofWebhookProcessor interface {
	Process(context.Context, gitprovider.VerifiedChangeEvent) error
}

type proofWebhookWorker struct {
	river.WorkerDefaults[jobs.ProofWebhookArgs]
	processor ProofWebhookProcessor
	logger    *slog.Logger
}

func NewProofWebhookWorker(processor ProofWebhookProcessor, logger *slog.Logger) (river.Worker[jobs.ProofWebhookArgs], error) {
	if processor == nil {
		return nil, proofwebhooksvc.ErrProcessingUnavailable
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &proofWebhookWorker{processor: processor, logger: logger}, nil
}

func (w *proofWebhookWorker) Work(ctx context.Context, job *river.Job[jobs.ProofWebhookArgs]) error {
	args := job.Args
	attrs := []slog.Attr{
		slog.String("delivery_id", args.DeliveryID),
		slog.String("provider", string(args.Provider)),
		slog.String("repository_id", args.RepositoryID),
		slog.String("change_request_id", args.ChangeRequestID),
		slog.String("head_sha", args.HeadSHA),
	}
	w.logger.LogAttrs(ctx, slog.LevelInfo, "proof webhook job started", attrs...)
	if err := w.processor.Process(ctx, args.Event()); err != nil {
		safe, retry := safeProofWebhookError(err)
		w.logger.LogAttrs(ctx, slog.LevelError, "proof webhook job failed", append(attrs,
			slog.String("error_code", safe.Code()))...)
		if retry {
			return safe
		}
		return xerror.Wrapf("cancel terminal proof webhook job: %w", river.JobCancel(safe))
	}
	w.logger.LogAttrs(ctx, slog.LevelInfo, "proof webhook job succeeded", attrs...)
	return nil
}

func safeProofWebhookError(err error) (*xerror.Error, bool) {
	if xerror.Is(err, context.Canceled) || xerror.Is(err, context.DeadlineExceeded) {
		return proofwebhooksvc.ErrProcessingUnavailable, true
	}
	if xerror.Is(err, proofrepo.ErrCandidateNotFound) {
		return proofwebhooksvc.ErrProcessingUnavailable, true
	}
	var typed *xerror.Error
	if xerror.As(err, &typed) {
		kind := typed.Kind()
		if kind == xerror.KindConnectionFailed || kind == xerror.KindTimeout ||
			kind == xerror.KindUnavailable || kind == xerror.KindInternal {
			return proofwebhooksvc.ErrProcessingUnavailable, true
		}
	}
	return proofwebhooksvc.ErrDeliveryRejected, false
}
