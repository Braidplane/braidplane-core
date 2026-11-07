package notificationsvc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
	"github.com/stretchr/testify/require"
)

type connectionProviderFunc func(context.Context, slug.Slug, slug.Slug, string) error

func (f connectionProviderFunc) Send(ctx context.Context, org, connection slug.Slug, text string) error {
	return f(ctx, org, connection, text)
}

func TestConnectionDeliveryLinksExactSource(t *testing.T) {
	for _, test := range []struct {
		name  string
		kind  model.NotificationType
		refs  map[string]string
		links []string
	}{
		{name: "runtime", kind: model.NotificationTypePipelineContractFailure, refs: map[string]string{"project_slug": "ops", "environment_slug": "dev", "run_slug": "failed-run"}, links: []string{"/orgs/acme/projects/ops/runs/failed-run?environment_slug=dev"}},
		{name: "validation", kind: model.NotificationTypePipelineContractFailure, refs: map[string]string{"project_slug": "ops", "environment_slug": "dev", "pipeline_slug": "report", "validation_result_slug": "validation-1"}, links: []string{"Validation result: validation-1", "/orgs/acme/projects/ops/pipelines/report?environment_slug=dev&section=deployments#pipeline-validation"}},
		{name: "recency", kind: model.NotificationTypePipelineFreshnessSLABreached, refs: map[string]string{"project_slug": "ops", "environment_slug": "dev", "pipeline_slug": "report"}, links: []string{"View successful-run recency: https://app.example.com/orgs/acme/projects/ops/pipelines/report/environments/dev/recency"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rawRefs, err := json.Marshal(test.refs)
			require.NoError(t, err)
			work := &reponotifications.DeliveryWork{
				OrgSlug: "acme", RecordSlug: "notice", DestinationSlug: "slack-alerts", DestinationType: model.AlertDestinationTypeSlack,
				NotificationType: test.kind, SourceRefs: rawRefs, NonSecretConfig: []byte(`{"connection_slug":"ops"}`), SafeMetadata: []byte(`{"summary":"Field product_code failed required."}`),
			}
			repo := &fakeNotificationsRepo{
				claimConnectionDeliveryFn: func(context.Context, postgres.DBTX, reponotifications.ClaimConnectionDeliveryParams) (reponotifications.ConnectionDeliveryClaim, error) {
					return reponotifications.ConnectionDeliveryClaim{Slug: "attempt", Status: model.AlertDeliveryAttemptStatusQueued, Acquired: true, ProviderAttempt: 1}, nil
				},
				finishConnectionDeliveryFn: func(_ context.Context, _ postgres.DBTX, p reponotifications.FinishConnectionDeliveryParams) error {
					require.Equal(t, model.AlertDeliveryAttemptStatusSent, p.Status)
					return nil
				},
			}
			svc := newTestService(repo).(*service)
			svc.frontendURL = "https://app.example.com/"
			sent := false
			svc.connectionProviders = ConnectionProviders{model.AlertDestinationTypeSlack: connectionProviderFunc(func(_ context.Context, org, connection slug.Slug, text string) error {
				sent = true
				require.Equal(t, slug.Slug("acme"), org)
				require.Equal(t, slug.Slug("ops"), connection)
				require.Contains(t, text, "Field product_code failed required.")
				for _, link := range test.links {
					require.Contains(t, text, link)
				}
				return nil
			})}
			require.NoError(t, svc.deliverConnectionNotification(t.Context(), work))
			require.True(t, sent)
		})
	}
}

func TestConnectionDeliveryPersistsProviderRetryDeadline(t *testing.T) {
	now := time.Now().UTC()
	claim := reponotifications.ConnectionDeliveryClaim{Slug: must.Slug("attempt"), Status: model.AlertDeliveryAttemptStatusQueued, Acquired: true, ProviderAttempt: 1}
	work := &reponotifications.DeliveryWork{OrgSlug: must.Slug("acme"), RecordSlug: must.Slug("notice"), DestinationSlug: must.Slug("slack-alerts"), AlertPolicySlug: must.Slug("failures"), DestinationType: model.AlertDestinationTypeSlack, NonSecretConfig: []byte(`{"connection_slug":"ops"}`), SafeMetadata: []byte(`{"summary":"Run failed"}`)}
	repo := &fakeNotificationsRepo{
		claimConnectionDeliveryFn: func(_ context.Context, _ postgres.DBTX, p reponotifications.ClaimConnectionDeliveryParams) (reponotifications.ConnectionDeliveryClaim, error) {
			require.Equal(t, now.Add(time.Minute), p.LeaseUntil)
			return claim, nil
		},
		finishConnectionDeliveryFn: func(_ context.Context, _ postgres.DBTX, p reponotifications.FinishConnectionDeliveryParams) error {
			require.Equal(t, model.AlertDeliveryAttemptStatusFailed, p.Status)
			require.Equal(t, "rate_limited", *p.ErrorCategory)
			require.Equal(t, now.Add(42*time.Second), *p.RetryAfterAt)
			require.Contains(t, string(p.ProviderResponseMetadata), "retry_at")
			return nil
		},
	}
	svc := newTestServiceWithClock(repo, testutil.FakeClock{NowFn: func() time.Time { return now }}).(*service)
	svc.connectionProviders = ConnectionProviders{model.AlertDestinationTypeSlack: connectionProviderFunc(func(_ context.Context, org, connection slug.Slug, text string) error {
		require.Equal(t, work.OrgSlug, org)
		require.Equal(t, must.Slug("ops"), connection)
		require.Contains(t, text, "Run failed")
		return &DeliveryError{Category: "rate_limited", Message: "Rate limited", Retryable: true, RetryAfter: 42 * time.Second}
	})}
	err := svc.deliverConnectionNotification(t.Context(), work)
	var retry *DeliveryError
	require.ErrorAs(t, err, &retry)
	require.Equal(t, 42*time.Second, retry.RetryAfter)
}

func TestConnectionDeliveryDoesNotResendSentOrLeasedAttempt(t *testing.T) {
	now := time.Now().UTC()
	retryAt := now.Add(time.Minute)
	for _, status := range []model.AlertDeliveryAttemptStatus{model.AlertDeliveryAttemptStatusSent, model.AlertDeliveryAttemptStatusFailed} {
		repo := &fakeNotificationsRepo{claimConnectionDeliveryFn: func(context.Context, postgres.DBTX, reponotifications.ClaimConnectionDeliveryParams) (reponotifications.ConnectionDeliveryClaim, error) {
			return reponotifications.ConnectionDeliveryClaim{Status: status, RetryAfterAt: &retryAt}, nil
		}}
		svc := newTestServiceWithClock(repo, testutil.FakeClock{NowFn: func() time.Time { return now }}).(*service)
		svc.connectionProviders = ConnectionProviders{model.AlertDestinationTypeSlack: connectionProviderFunc(func(context.Context, slug.Slug, slug.Slug, string) error { t.Fatal("must not send"); return nil })}
		err := svc.deliverConnectionNotification(t.Context(), &reponotifications.DeliveryWork{})
		if status == model.AlertDeliveryAttemptStatusSent {
			require.NoError(t, err)
		} else {
			var retry *DeliveryError
			require.ErrorAs(t, err, &retry)
			require.Equal(t, time.Minute, retry.RetryAfter)
		}
	}
}

func TestSlackDestinationRejectsRawSecretsAndMissingConnection(t *testing.T) {
	for _, config := range []map[string]any{nil, {"webhook_url": "secret"}, {"connection_slug": "ops", "access_token": "secret"}, {"connection_slug": "invalid slug"}} {
		require.Error(t, validateAlertDestinationConfig(model.AlertDestinationTypeSlack, config))
	}
	require.NoError(t, validateAlertDestinationConfig(model.AlertDestinationTypeSlack, map[string]any{"connection_slug": "ops"}))
}

func TestSlackDestinationTestQueuesUniqueEphemeralJobs(t *testing.T) {
	producer := &fakeNotificationDeliveryProducer{}
	svc := newTestServiceWithDeliveryProducer(&fakeNotificationsRepo{
		findAlertDestinationBySlugFn: func(_ context.Context, _ postgres.DBTX, org, destination slug.Slug) (*reponotifications.AlertDestination, error) {
			require.Equal(t, must.Slug("acme"), org)
			require.Equal(t, must.Slug("ops"), destination)
			return &reponotifications.AlertDestination{Type: model.AlertDestinationTypeSlack, Enabled: true}, nil
		},
	}, testutil.FakeClock{}, producer)
	for range 2 {
		result, err := svc.TestAlertDestination(t.Context(), testAlertPrincipal(), TestAlertDestinationParams{OrgSlug: must.Slug("acme"), DestinationSlug: must.Slug("ops")})
		require.NoError(t, err)
		require.False(t, result.DiagnosticsPersisted)
		require.Equal(t, model.AlertDestinationTypeSlack, result.DestinationType)
	}
	require.Len(t, producer.calls, 2)
	require.True(t, producer.calls[0].Test)
	require.NotEmpty(t, producer.calls[0].RecordSlug)
	require.NotEqual(t, producer.calls[0].RecordSlug, producer.calls[1].RecordSlug)
	require.Empty(t, producer.calls[0].AlertPolicySlug)
}
