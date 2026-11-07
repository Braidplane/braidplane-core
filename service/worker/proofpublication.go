package worker

import (
	"log/slog"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/riverqueue/river"
)

type ProofPublicationRegistrar struct {
	worker river.Worker[jobs.ProofPublicationArgs]
}

func NewProofPublicationRegistrar(processor consumer.ProofPublicationProcessor, logger *slog.Logger) (*ProofPublicationRegistrar, error) {
	w, err := consumer.NewProofPublicationWorker(processor, logger)
	if err != nil {
		return nil, xerror.Wrapf("create Proof publication worker: %w", err)
	}
	return &ProofPublicationRegistrar{worker: w}, nil
}

func (r *ProofPublicationRegistrar) Register(workers *river.Workers) error {
	if workers == nil {
		return xerror.Unexpected().Detail("River workers registry is required")
	}
	if err := river.AddWorkerSafely(workers, r.worker); err != nil {
		return xerror.Wrapf("register Proof publication worker: %w", err)
	}
	return nil
}
