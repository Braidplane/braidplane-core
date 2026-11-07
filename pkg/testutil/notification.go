package testutil

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/postgres"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type FakeRunSucceededNotifier struct {
	CandidateFn func(context.Context, postgres.DBTX, model.NotificationCandidate) error
	NotifyFn    func(context.Context, slug.Slug, slug.Slug, slug.Slug, string) error
}

func (f FakeRunSucceededNotifier) EnqueueCandidate(ctx context.Context, tx postgres.DBTX, candidate model.NotificationCandidate) error {
	if f.CandidateFn != nil {
		return f.CandidateFn(ctx, tx, candidate)
	}
	if f.NotifyFn != nil && candidate.NotificationType == model.NotificationTypePipelineRunSucceeded {
		return f.NotifyFn(ctx, candidate.OrgSlug, *candidate.ProjectSlug, *candidate.EnvironmentSlug, candidate.SourceRefs["run_slug"])
	}
	return nil
}

func (f FakeRunSucceededNotifier) NotifyRunSucceeded(ctx context.Context, org, project, environment slug.Slug, run string) error {
	if f.NotifyFn != nil {
		return f.NotifyFn(ctx, org, project, environment, run)
	}
	return nil
}
