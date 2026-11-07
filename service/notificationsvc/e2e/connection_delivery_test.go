package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slack"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	slacknotifications "github.com/Braidplane/braidplane-core/service/notificationsvc/provider/slack"
	"github.com/stretchr/testify/require"
)

type notificationInstallProvider struct{}

func (notificationInstallProvider) AuthorizationURL(context.Context, credentialssvc.AuthorizationURLParams) (string, error) {
	return "https://slack.com/oauth/v2/authorize", nil
}

func (notificationInstallProvider) ExchangeCode(context.Context, credentialssvc.ExchangeCodeParams) (credentialssvc.OAuthTokenSet, error) {
	return credentialssvc.OAuthTokenSet{AccessToken: "local-test-token", SlackWebhookURL: "https://hooks.slack.com/services/TTEST/BTEST/local-test-secret", Scopes: []string{"incoming-webhook"}}, nil
}

func (notificationInstallProvider) Refresh(context.Context, credentialssvc.RefreshParams) (credentialssvc.OAuthTokenSet, error) {
	return credentialssvc.OAuthTokenSet{}, credentialssvc.ErrProviderInvalidGrant
}

func (notificationInstallProvider) Revoke(context.Context, credentialssvc.RevokeProviderGrantParams) error {
	return nil
}

type notificationWebhookFunc func(context.Context, string, string) error

func (f notificationWebhookFunc) SendWebhook(ctx context.Context, url, text string) error {
	return f(ctx, url, text)
}

func TestSlackNotificationDeliveryPolicyRetryAndDisconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "Slack notification delivery")
	org, otherOrg := uniqueNotificationE2ESlug("slack-delivery-org"), uniqueNotificationE2ESlug("slack-delivery-other")
	seedNotificationOrg(t, ctx, pool, org)
	seedNotificationOrg(t, ctx, pool, otherOrg)
	// PostgreSQL persists the lease deadline with microsecond precision.
	now := time.Now().UTC().Truncate(time.Microsecond)
	clk := testutil.FakeClock{NowFn: func() time.Time { return now }}
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	txm := postgres.NewTransactionManager(pool)
	credentials, err := credentialssvc.New(txm, repocredentials.New(), cipher, credentialssvc.ProviderRegistry{"slack": notificationInstallProvider{}}, clk, false)
	require.NoError(t, err)
	connection := uniqueNotificationE2ESlug("slack-connection")
	_, err = credentials.CreateNotificationConnection(ctx, credentialssvc.CreateNotificationConnectionParams{OrgSlug: org, Slug: connection, Name: "Operations Slack", Provider: "slack"})
	require.NoError(t, err)
	owner := credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindNotificationConnection, OrgSlug: org, NotificationConnectionSlug: connection}
	install, err := credentials.ConnectNotificationConnection(ctx, owner, model.SystemEventActor())
	require.NoError(t, err)
	_, err = credentials.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{Provider: "slack", State: install.State, Code: "local-test-code"})
	require.NoError(t, err)
	sends := 0
	var duplicateAttempt func()
	sender := slacknotifications.New(credentials, notificationWebhookFunc(func(_ context.Context, url, text string) error {
		sends++
		require.Equal(t, "https://hooks.slack.com/services/TTEST/BTEST/local-test-secret", url)
		require.Contains(t, text, "pipeline.run_failed")
		if sends == 1 {
			duplicateAttempt()
			return &slack.RateLimitError{RetryAfter: 42 * time.Second}
		}
		return nil
	}))
	producer := &fakeDeliveryProducer{}
	svc := notificationsvc.New(txm, reponotifications.New(), clk, producer, &testutil.FakeEmailProducer{}, notificationsvc.LifecycleEventProducerFunc(func(context.Context, postgres.DBTX, slug.Slug, model.EventActor, eventrecorder.Event) {}), notificationsvc.ConnectionProviders{model.AlertDestinationTypeSlack: sender}, "https://app.example")
	destination := uniqueNotificationE2ESlug("slack-destination")
	create := notificationsvc.CreateAlertDestinationParams{OrgSlug: otherOrg, Slug: destination, Name: "Alerts", Type: model.AlertDestinationTypeSlack, NonSecretConfig: map[string]any{"connection_slug": connection.String()}}
	_, err = svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), create)
	require.Error(t, err, "cross-tenant connection cannot be attached")
	create.OrgSlug = org
	_, err = svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), create)
	require.NoError(t, err)
	candidate := notificationsvc.CreateCandidateParams{OrgSlug: org, NotificationType: model.NotificationTypePipelineRunFailed, Severity: model.NotificationSeverityCritical, DedupeFingerprint: "slack-before-policy", SourceEntityType: model.EventEntityTypePipelineRun, SourceAction: model.EventActionExecute, SourceRefs: map[string]string{"pipeline_slug": "test-pipeline", "run_slug": "test-run"}, SafeMetadata: map[string]any{"summary": "Test failure"}}
	_, err = svc.CreateCandidate(ctx, candidate)
	require.NoError(t, err)
	require.Empty(t, producer.calls, "no implicit subscription before policy consent")
	policy := uniqueNotificationE2ESlug("slack-policy")
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{OrgSlug: org, Slug: policy, Name: "Failures", Scope: model.AlertPolicyScopeOrganization, NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed), DestinationSlugs: []slug.Slug{destination}})
	require.NoError(t, err)
	candidate.DedupeFingerprint = "slack-policy-delivery"
	record, err := svc.CreateCandidate(ctx, candidate)
	require.NoError(t, err)
	_, err = svc.CreateCandidate(ctx, candidate)
	require.NoError(t, err)
	require.Len(t, producer.calls, 1, "local scheduling deduplicates repeated occurrences")
	params := notificationsvc.DeliverNotificationParams{OrgSlug: org, RecordSlug: record.Record.Slug, DestinationSlug: destination, AlertPolicySlug: policy}
	duplicateAttempt = func() {
		var pending *notificationsvc.DeliveryError
		err := svc.DeliverNotification(ctx, params)
		require.ErrorAs(t, err, &pending)
		require.Equal(t, time.Minute, pending.RetryAfter, "committed lease excludes a concurrent provider send")
	}
	var deliveryErr *notificationsvc.DeliveryError
	err = svc.DeliverNotification(ctx, params)
	require.ErrorAs(t, err, &deliveryErr)
	require.Equal(t, 42*time.Second, deliveryErr.RetryAfter)
	require.Equal(t, 1, sends)
	err = svc.DeliverNotification(ctx, params)
	require.ErrorAs(t, err, &deliveryErr)
	require.Equal(t, 1, sends, "early retry must respect persisted provider deadline")
	now = now.Add(43 * time.Second)
	require.NoError(t, svc.DeliverNotification(ctx, params))
	require.NoError(t, svc.DeliverNotification(ctx, params))
	require.Equal(t, 2, sends, "sent row prevents another provider message")
	attempts, err := svc.ListAlertDeliveryAttempts(ctx, org, record.Record.Slug, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, attempts.Items, 1)
	require.Equal(t, model.AlertDeliveryAttemptStatusSent, attempts.Items[0].Status)
	require.Equal(t, 2, attempts.Items[0].ProviderAttempt)
	require.Empty(t, attempts.Items[0].ErrorMessage)
	_, err = credentials.Revoke(ctx, credentialssvc.RevokeParams{Owner: owner})
	require.NoError(t, err)
	candidate.DedupeFingerprint = "slack-after-disconnect"
	next, err := svc.CreateCandidate(ctx, candidate)
	require.NoError(t, err)
	params.RecordSlug = next.Record.Slug
	err = svc.DeliverNotification(ctx, params)
	require.ErrorAs(t, err, &deliveryErr)
	require.False(t, deliveryErr.Retryable)
	require.Equal(t, "connection_disconnected", deliveryErr.Category)
	require.Equal(t, 2, sends, "disconnected grant cannot send")
	require.NoError(t, svc.DeliverNotification(ctx, params), "terminal provider failure is locally deduplicated")
}
