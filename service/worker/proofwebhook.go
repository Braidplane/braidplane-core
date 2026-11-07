package worker

import (
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
)

type ProofWebhookRegistrar struct {
	processor consumer.ProofWebhookProcessor
	logger    *slog.Logger
}

func NewProofWebhookRegistrar(processor consumer.ProofWebhookProcessor, logger *slog.Logger) (*ProofWebhookRegistrar, error) {
	if processor == nil {
		return nil, xerror.Unexpected().Detail("proof webhook processor is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ProofWebhookRegistrar{processor: processor, logger: logger}, nil
}

func (r *ProofWebhookRegistrar) Register(workers *river.Workers) error {
	if workers == nil {
		return xerror.Unexpected().Detail("River workers registry is required")
	}
	worker, err := consumer.NewProofWebhookWorker(r.processor, r.logger)
	if err != nil {
		return xerror.Wrapf("create proof webhook worker: %w", err)
	}
	if err := river.AddWorkerSafely(workers, worker); err != nil {
		return xerror.Wrapf("register proof webhook worker: %w", err)
	}
	return nil
}
