package runrecencysvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
)

type SuccessRepository interface {
	RecordSuccess(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error
}

type successProducer struct {
	repo SuccessRepository
	next notificationsvc.CandidateProducer
}

// NewSuccessProducer retains compact success evidence in the runtime's existing
// run/checkpoint/candidate transaction, before ordinary run retention can erase it.
func NewSuccessProducer(repo SuccessRepository, next notificationsvc.CandidateProducer) notificationsvc.CandidateProducer {
	if repo == nil || next == nil {
		panic("run recency success repository and candidate producer are required")
	}
	return &successProducer{repo: repo, next: next}
}

func (p *successProducer) EnqueueCandidate(ctx context.Context, tx postgres.DBTX, candidate notificationsvc.CreateCandidateParams) error {
	if candidate.NotificationType == model.NotificationTypePipelineRunSucceeded {
		if err := notificationsvc.ValidateCandidate(candidate); err != nil {
			return err
		}
		run, err := slug.New(candidate.SourceRefs["run_slug"])
		if candidate.ProjectSlug == nil || candidate.EnvironmentSlug == nil || err != nil {
			return xerror.InvalidInput(xerror.PipelineRun).With(err).Detail("run recency success requires project, environment and run identities")
		}
		if err := p.repo.RecordSuccess(ctx, tx, candidate.OrgSlug, *candidate.ProjectSlug, *candidate.EnvironmentSlug, run); err != nil {
			return err
		}
	}
	return p.next.EnqueueCandidate(ctx, tx, candidate)
}
