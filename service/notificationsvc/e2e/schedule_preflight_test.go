package e2e

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/stretchr/testify/require"
)

func TestSchedulePreflightNotificationRequiresExplicitOptIn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}

	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "scheduled preflight notification routing")
	org, err := slug.WithRandomSuffix("preflight-org", 8)
	require.NoError(t, err)
	project, env, destination := slug.Slug("ops"), slug.Slug("prod"), slug.Slug("mail")
	seedAlertPolicyScopeRows(t, context.WithoutCancel(ctx), pool, org, project, env, destination)
	enableProjectEnvironment(t, ctx, pool, org, project, env)
	producer := &fakeDeliveryProducer{}
	email := &testutil.FakeEmailProducer{
		EnqueueNotificationFn: func(context.Context, postgres.DBTX, slug.Slug, model.NotificationEmailRecipient, model.NotificationEmail, slug.Slug) (queue.QueuedJob, error) {
			return queue.QueuedJob{Kind: "email_send", Queue: "email", State: "queued"}, nil
		},
	}
	svc := newNotificationE2EService(pool, producer, email)

	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug: org, Slug: "all-warnings", Name: "All warnings", Scope: model.AlertPolicyScopeOrganization,
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityWarning),
		DestinationSlugs: []slug.Slug{destination},
	})
	require.NoError(t, err)
	kind := model.NotificationTypePipelineScheduledPreflightFailed
	candidate := model.NotificationCandidate{
		OrgSlug: org, ProjectSlug: &project, EnvironmentSlug: &env, NotificationType: kind,
		Severity: model.NotificationSeverityWarning, DedupeFingerprint: "before-opt-in",
		SourceEntityType: model.EventEntityTypePipelineTrigger, SourceAction: model.EventActionExecute,
		SourceRefs:   map[string]string{"trigger_slug": "daily-customers", "source_ref": "daily-customers"},
		SafeMetadata: map[string]any{"summary": "Source schema refresh failed during scheduled preflight."},
	}
	first, err := svc.CreateCandidate(ctx, candidate)
	require.NoError(t, err)
	require.Empty(t, producer.calls, "severity-only policies do not opt in a destination")

	replay := notificationsvc.DeliverNotificationParams{
		OrgSlug: org, RecordSlug: first.Record.Slug, DestinationSlug: destination, AlertPolicySlug: "all-warnings",
	}
	require.NoError(t, svc.DeliverNotification(ctx, replay))
	require.Zero(t, email.EnqueueNotificationCalls)

	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug: org, Slug: "preflight-only", Name: "Preflight failures",
		Scope: model.AlertPolicyScopeProjectEnvironment, ProjectSlug: &project, EnvironmentSlug: &env,
		NotificationType: &kind, DestinationSlugs: []slug.Slug{destination},
	})
	require.NoError(t, err)
	candidate.DedupeFingerprint = "after-opt-in"
	second, err := svc.CreateCandidate(ctx, candidate)
	require.NoError(t, err)
	require.Len(t, producer.calls, 1)
	require.Equal(t, slug.Slug("preflight-only"), producer.calls[0].AlertPolicySlug)
	require.Equal(t, destination, producer.calls[0].DestinationSlug)
	require.NoError(t, svc.DeliverNotification(ctx, notificationsvc.DeliverNotificationParams{
		OrgSlug: org, RecordSlug: second.Record.Slug, DestinationSlug: destination, AlertPolicySlug: "preflight-only",
	}))
	require.Equal(t, 1, email.EnqueueNotificationCalls)
	require.Equal(t, kind, email.LastNotification.NotificationType)
	require.Equal(t, candidate.SafeMetadata["summary"], email.LastNotification.Summary)
}
