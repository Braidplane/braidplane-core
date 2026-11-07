package runrecencysvc

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/stretchr/testify/require"
)

type successRepositoryFunc func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error

func (f successRepositoryFunc) RecordSuccess(ctx context.Context, tx postgres.DBTX, org, project, environment, run slug.Slug) error {
	return f(ctx, tx, org, project, environment, run)
}

func TestSuccessCapture(t *testing.T) {
	for _, kind := range []model.NotificationType{model.NotificationTypePipelineRunSucceeded, model.NotificationTypePipelineRunFailed} {
		t.Run(kind.String(), func(t *testing.T) {
			var order []string
			project, environment := slug.Slug("ops"), slug.Slug("prod")
			p := NewSuccessProducer(successRepositoryFunc(func(_ context.Context, _ postgres.DBTX, org, gotProject, gotEnvironment, run slug.Slug) error {
				require.Equal(t, slug.Slug("acme"), org)
				require.Equal(t, project, gotProject)
				require.Equal(t, environment, gotEnvironment)
				require.Equal(t, slug.Slug("run-one"), run)
				order = append(order, "evidence")
				return nil
			}), testutil.FakeRunSucceededNotifier{CandidateFn: func(context.Context, postgres.DBTX, model.NotificationCandidate) error {
				order = append(order, "candidate")
				return nil
			}})
			err := p.EnqueueCandidate(context.Background(), nil, model.NotificationCandidate{OrgSlug: "acme", ProjectSlug: &project, EnvironmentSlug: &environment, NotificationType: kind, Severity: model.NotificationSeverityInfo, DedupeFingerprint: "run-one", ObservedAt: time.Now(), SourceEntityType: model.EventEntityTypePipelineRun, SourceAction: model.EventActionExecute, SourceRefs: map[string]string{"run_slug": "run-one"}})
			require.NoError(t, err)
			expected := []string{"candidate"}
			if kind == model.NotificationTypePipelineRunSucceeded {
				expected = []string{"evidence", "candidate"}
			}
			require.Equal(t, expected, order)
		})
	}
}

func TestSuccessCaptureFailureStopsHandoff(t *testing.T) {
	failure := xerror.Unexpected().Detail("evidence unavailable")
	project, environment := slug.Slug("ops"), slug.Slug("prod")
	p := NewSuccessProducer(successRepositoryFunc(func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error { return failure }), testutil.FakeRunSucceededNotifier{CandidateFn: func(context.Context, postgres.DBTX, model.NotificationCandidate) error {
		t.Fatal("candidate must not escape failed evidence transaction")
		return nil
	}})
	require.ErrorIs(t, p.EnqueueCandidate(context.Background(), nil, model.NotificationCandidate{OrgSlug: "acme", ProjectSlug: &project, EnvironmentSlug: &environment, NotificationType: model.NotificationTypePipelineRunSucceeded, Severity: model.NotificationSeverityInfo, DedupeFingerprint: "run-one", SourceEntityType: model.EventEntityTypePipelineRun, SourceAction: model.EventActionExecute, SourceRefs: map[string]string{"run_slug": "run-one"}}), failure)
}
