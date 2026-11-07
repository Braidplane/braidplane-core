package producer

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type ProofPublicationProducer struct{ enqueuer queue.Enqueuer }

func NewProofPublicationProducer(enqueuer queue.Enqueuer) (*ProofPublicationProducer, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("Proof publication enqueuer is required")
	}
	return &ProofPublicationProducer{enqueuer: enqueuer}, nil
}

func (p *ProofPublicationProducer) EnqueueProofReceipt(ctx context.Context, tx postgres.DBTX, receipt model.ProofReceipt) error {
	c := receipt.Candidate
	_, err := p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args:  jobs.ProofPublicationArgs{Organization: c.OrganizationSlug, Project: c.ProjectSlug, Environment: c.EnvironmentSlug, Receipt: receipt.ID},
		Queue: jobs.PipelineGitQueue,
	})
	if err != nil {
		return xerror.Wrapf("enqueue Proof receipt publication: %w", err)
	}
	return nil
}
