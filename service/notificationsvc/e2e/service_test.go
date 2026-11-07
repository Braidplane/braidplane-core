package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
)

var notificationE2ESlugSequence atomic.Uint64

type fakeDeliveryProducer struct {
	calls []notificationsvc.NotificationDeliveryJobParams
	err   error
}

func (f *fakeDeliveryProducer) EnqueueNotificationDelivery(
	_ context.Context,
	_ postgres.DBTX,
	params notificationsvc.NotificationDeliveryJobParams,
) (queue.QueuedJob, error) {
	f.calls = append(f.calls, params)
	if f.err != nil {
		return queue.QueuedJob{}, f.err
	}
	return queue.QueuedJob{Kind: "notification_delivery", Queue: "notification_delivery", State: "queued"}, nil
}

func newNotificationE2EService(
	pool *pgxpool.Pool,
	producer *fakeDeliveryProducer,
	emailProducer notificationsvc.NotificationEmailProducer,
) notificationsvc.Service {
	if producer == nil {
		producer = &fakeDeliveryProducer{}
	}
	if emailProducer == nil {
		emailProducer = &testutil.FakeEmailProducer{}
	}
	return notificationsvc.New(
		postgres.NewTransactionManager(pool),
		reponotifications.New(),
		clock.New(),
		producer,
		emailProducer,
		notificationsvc.LifecycleEventProducerFunc(func(context.Context, postgres.DBTX, slug.Slug, model.EventActor, eventrecorder.Event) {}),
		notificationsvc.ConnectionProviders{},
		"https://app.example",
	)
}

func notificationE2EPrincipal() notificationsvc.AlertManagementPrincipal {
	return notificationsvc.AlertManagementPrincipal{UserSlug: must.Slug("alice")}
}

func notificationE2ENotificationTypePtr(value model.NotificationType) *model.NotificationType {
	return &value
}

func notificationE2ESeverityPtr(value model.NotificationSeverity) *model.NotificationSeverity {
	return &value
}

func notificationE2ELifecycleStatePtr(value model.NotificationLifecycleState) *model.NotificationLifecycleState {
	return &value
}

func TestService_CreateAlertDestinationStoresNonSecretConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres alert destination E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "alert destination non-secret config")
	svc := newNotificationE2EService(pool, nil, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-dest-org")
	destinationSlug := uniqueNotificationE2ESlug("notif-dest-email")
	seedNotificationOrg(t, ctx, pool, orgSlug)

	created, err := svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Slug:    destinationSlug,
		Name:    "Operations email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"display_label": "Operations",
			"recipients":    []string{"ops@example.com"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, destinationSlug, created.Slug)
	require.Equal(t, model.AlertDestinationTypeEmail, created.Type)
	require.Equal(t, "Operations", created.NonSecretConfig["display_label"])
	require.Equal(t, "ops@example.com", created.NonSecretConfig["recipients"].([]any)[0])

	var storedConfig string
	err = pool.QueryRow(ctx, `
SELECT nd.non_secret_config::text
FROM alert_destinations nd
JOIN organizations o ON o.id = nd.organization_id
WHERE o.slug = $1
    AND nd.slug = $2
`, orgSlug, destinationSlug).Scan(&storedConfig)
	require.NoError(t, err)
	require.JSONEq(t, `{"display_label":"Operations","recipients":["ops@example.com"]}`, storedConfig)
}

func TestService_CreateProjectEnvironmentPolicyRequiresEnabledProjectEnvironment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres alert policy E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "alert policy project environment scope")
	svc := newNotificationE2EService(pool, nil, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-pe-org")
	projectSlug := uniqueNotificationE2ESlug("notif-pe-project")
	environmentSlug := uniqueNotificationE2ESlug("notif-pe-env")
	destinationSlug := uniqueNotificationE2ESlug("notif-pe-email")
	seedAlertPolicyScopeRows(t, ctx, pool, orgSlug, projectSlug, environmentSlug, destinationSlug)

	_, err := svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             uniqueNotificationE2ESlug("notif-pe-policy-missing"),
		Name:             "Project environment policy without enabled environment",
		Scope:            model.AlertPolicyScopeProjectEnvironment,
		ProjectSlug:      &projectSlug,
		EnvironmentSlug:  &environmentSlug,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	var appErr *xerror.Error
	require.True(t, xerror.As(err, &appErr))
	require.Equal(t, xerror.KindInvalidInput, appErr.Kind())

	enableProjectEnvironment(t, ctx, pool, orgSlug, projectSlug, environmentSlug)
	created, err := svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             uniqueNotificationE2ESlug("notif-pe-policy-enabled"),
		Name:             "Project environment policy with enabled environment",
		Scope:            model.AlertPolicyScopeProjectEnvironment,
		ProjectSlug:      &projectSlug,
		EnvironmentSlug:  &environmentSlug,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	require.NoError(t, err)
	require.Equal(t, projectSlug, *created.ProjectSlug)
	require.Equal(t, environmentSlug, *created.EnvironmentSlug)
	require.Equal(t, []slug.Slug{destinationSlug}, created.DestinationSlugs)
}

func TestService_AlertManagementOperationsAreTenantScoped(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres alert management E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "alert management tenant scope")
	svc := newNotificationE2EService(pool, nil, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-alert-mgmt-org")
	otherOrgSlug := uniqueNotificationE2ESlug("notif-alert-mgmt-other-org")
	destinationSlug := uniqueNotificationE2ESlug("notif-alert-mgmt-email")
	policySlug := uniqueNotificationE2ESlug("notif-alert-mgmt-policy")
	seedNotificationOrg(t, ctx, pool, orgSlug)
	seedNotificationOrg(t, ctx, pool, otherOrgSlug)

	_, err := svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Slug:    destinationSlug,
		Name:    "Primary tenant email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"primary@example.com"},
		},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
		OrgSlug: otherOrgSlug,
		Slug:    destinationSlug,
		Name:    "Other tenant email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"other@example.com"},
		},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Primary tenant policy",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          otherOrgSlug,
		Slug:             policySlug,
		Name:             "Other tenant policy",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	require.NoError(t, err)

	destinations, err := svc.ListAlertDestinations(ctx, notificationE2EPrincipal(), orgSlug, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, destinations.Items, 1)
	require.Equal(t, "Primary tenant email", destinations.Items[0].Name)
	policies, err := svc.ListAlertPolicies(ctx, notificationE2EPrincipal(), orgSlug, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, policies.Items, 1)
	require.Equal(t, "Primary tenant policy", policies.Items[0].Name)
	require.Nil(t, policies.Items[0].LifecycleState)
	require.Equal(t, []slug.Slug{destinationSlug}, policies.Items[0].DestinationSlugs)

	updatedDestination, err := svc.UpdateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.UpdateAlertDestinationParams{
		OrgSlug: orgSlug,
		Slug:    destinationSlug,
		Name:    "Primary tenant email updated",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"primary-updated@example.com"},
		},
		Enabled: false,
	})
	require.NoError(t, err)
	require.Equal(t, "Primary tenant email updated", updatedDestination.Name)
	require.False(t, updatedDestination.Enabled)

	updatedPolicy, err := svc.UpdateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.UpdateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Primary tenant policy updated",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityWarning),
		LifecycleState:   notificationE2ELifecycleStatePtr(model.NotificationLifecycleStateAcknowledged),
		DestinationSlugs: []slug.Slug{destinationSlug},
		Enabled:          false,
	})
	require.NoError(t, err)
	require.Equal(t, "Primary tenant policy updated", updatedPolicy.Name)
	require.NotNil(t, updatedPolicy.Severity)
	require.Equal(t, model.NotificationSeverityWarning, *updatedPolicy.Severity)
	require.NotNil(t, updatedPolicy.LifecycleState)
	require.Equal(t, model.NotificationLifecycleStateAcknowledged, *updatedPolicy.LifecycleState)
	require.False(t, updatedPolicy.Enabled)

	otherDestination, err := svc.GetAlertDestination(ctx, notificationE2EPrincipal(), otherOrgSlug, destinationSlug)
	require.NoError(t, err)
	require.Equal(t, "Other tenant email", otherDestination.Name)
	require.True(t, otherDestination.Enabled)
	otherPolicy, err := svc.GetAlertPolicy(ctx, notificationE2EPrincipal(), otherOrgSlug, policySlug)
	require.NoError(t, err)
	require.Equal(t, "Other tenant policy", otherPolicy.Name)
	require.NotNil(t, otherPolicy.Severity)
	require.Equal(t, model.NotificationSeverityCritical, *otherPolicy.Severity)
	require.Nil(t, otherPolicy.LifecycleState)
	require.True(t, otherPolicy.Enabled)

	require.NoError(t, svc.DeleteAlertPolicy(ctx, notificationE2EPrincipal(), orgSlug, policySlug))
	require.NoError(t, svc.DeleteAlertDestination(ctx, notificationE2EPrincipal(), orgSlug, destinationSlug))

	destinations, err = svc.ListAlertDestinations(ctx, notificationE2EPrincipal(), orgSlug, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, destinations.Items)
	policies, err = svc.ListAlertPolicies(ctx, notificationE2EPrincipal(), orgSlug, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, policies.Items)

	otherDestinations, err := svc.ListAlertDestinations(ctx, notificationE2EPrincipal(), otherOrgSlug, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, otherDestinations.Items, 1)
	require.Equal(t, "Other tenant email", otherDestinations.Items[0].Name)
	otherPolicies, err := svc.ListAlertPolicies(ctx, notificationE2EPrincipal(), otherOrgSlug, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, otherPolicies.Items, 1)
	require.Equal(t, "Other tenant policy", otherPolicies.Items[0].Name)
}

func TestService_CreateCandidateRejectsDeletedProjectEnvironmentHint(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres notification candidate E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "notification candidate disabled project environment")
	svc := newNotificationE2EService(pool, nil, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-disabled-pe-org")
	projectSlug := uniqueNotificationE2ESlug("notif-disabled-pe-project")
	environmentSlug := uniqueNotificationE2ESlug("notif-disabled-pe-env")
	destinationSlug := uniqueNotificationE2ESlug("notif-disabled-pe-email")
	recordSlug := uniqueNotificationE2ESlug("notif-disabled-pe-record")
	seedAlertPolicyScopeRows(t, ctx, pool, orgSlug, projectSlug, environmentSlug, destinationSlug)
	enableProjectEnvironment(t, ctx, pool, orgSlug, projectSlug, environmentSlug)

	_, err := pool.Exec(ctx, `
UPDATE project_environments pe
SET deleted_at = NOW(),
    updated_at = NOW()
FROM organizations o
JOIN projects p ON p.organization_id = o.id
JOIN environments e ON e.organization_id = o.id
WHERE pe.project_id = p.id
    AND pe.environment_id = e.id
    AND o.slug = $1
    AND p.slug = $2
    AND e.slug = $3
`, orgSlug, projectSlug, environmentSlug)
	require.NoError(t, err)

	got, err := svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &recordSlug,
		ProjectSlug:       &projectSlug,
		EnvironmentSlug:   &environmentSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: fmt.Sprintf("pipeline:run_failed:%s:%s", projectSlug, environmentSlug),
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-disabled-link",
		},
		SafeMetadata: map[string]any{
			"summary": "contract failed",
		},
	})

	var appErr *xerror.Error
	require.True(t, xerror.As(err, &appErr))
	require.Equal(t, xerror.KindInvalidInput, appErr.Kind())
	require.Nil(t, got)

	var recordCount int
	err = pool.QueryRow(ctx, `
SELECT count(*)
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = $1
    AND nr.slug = $2
`, orgSlug, recordSlug).Scan(&recordCount)
	require.NoError(t, err)
	require.Zero(t, recordCount)
}

func TestService_CreateCandidatePersistsRecordAndEvaluatesAlertPolicies(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres notification candidate E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "notification candidate policy matching")
	producer := &fakeDeliveryProducer{}
	svc := newNotificationE2EService(pool, producer, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-candidate-org")
	otherOrgSlug := uniqueNotificationE2ESlug("notif-candidate-other-org")
	projectSlug := uniqueNotificationE2ESlug("notif-candidate-project")
	environmentSlug := uniqueNotificationE2ESlug("notif-candidate-env")
	destinationSlug := uniqueNotificationE2ESlug("notif-candidate-email")
	orgPolicySlug := uniqueNotificationE2ESlug("notif-candidate-org-policy")
	projectPolicySlug := uniqueNotificationE2ESlug("notif-candidate-project-policy")
	environmentPolicySlug := uniqueNotificationE2ESlug("notif-candidate-env-policy")
	otherDestinationSlug := uniqueNotificationE2ESlug("notif-candidate-other-email")
	otherPolicySlug := uniqueNotificationE2ESlug("notif-candidate-other-policy")
	noPolicyRecordSlug := uniqueNotificationE2ESlug("notif-candidate-no-policy")
	alertedRecordSlug := uniqueNotificationE2ESlug("notif-candidate-alerted")
	dedupedRecordSlug := uniqueNotificationE2ESlug("notif-candidate-deduped")
	laterEventSlug := uniqueNotificationE2ESlug("notif-candidate-event")
	dedupeFingerprint := fmt.Sprintf("pipeline:run_failed:%s:%s:%s", projectSlug, environmentSlug, "daily-ledger")

	seedAlertPolicyScopeRows(t, ctx, pool, orgSlug, projectSlug, environmentSlug, destinationSlug)
	enableProjectEnvironment(t, ctx, pool, orgSlug, projectSlug, environmentSlug)
	seedNotificationOrg(t, ctx, pool, otherOrgSlug)

	_, err := svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             orgPolicySlug,
		Name:             "Organization pipeline failures",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             projectPolicySlug,
		Name:             "Project pipeline failures",
		Scope:            model.AlertPolicyScopeProject,
		ProjectSlug:      &projectSlug,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             environmentPolicySlug,
		Name:             "Environment pipeline failures",
		Scope:            model.AlertPolicyScopeProjectEnvironment,
		ProjectSlug:      &projectSlug,
		EnvironmentSlug:  &environmentSlug,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	require.NoError(t, err)

	_, err = svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
		OrgSlug: otherOrgSlug,
		Slug:    otherDestinationSlug,
		Name:    "Other tenant email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"other-ops@example.com"},
		},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          otherOrgSlug,
		Slug:             otherPolicySlug,
		Name:             "Other tenant retry alerts",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRetryExhausted),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityWarning),
		DestinationSlugs: []slug.Slug{otherDestinationSlug},
	})
	require.NoError(t, err)

	noPolicy, err := svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &noPolicyRecordSlug,
		ProjectSlug:       &projectSlug,
		EnvironmentSlug:   &environmentSlug,
		NotificationType:  model.NotificationTypePipelineRetryExhausted,
		Severity:          model.NotificationSeverityWarning,
		DedupeFingerprint: dedupeFingerprint + ":warning",
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-warning",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, noPolicy.Record)
	require.Equal(t, noPolicyRecordSlug, noPolicy.Record.Slug)
	require.Equal(t, projectSlug, *noPolicy.Record.ProjectSlug)
	require.Equal(t, environmentSlug, *noPolicy.Record.EnvironmentSlug)
	require.Empty(t, producer.calls)

	var noPolicyRecords int
	err = pool.QueryRow(ctx, `
SELECT count(*)
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = $1
    AND nr.dedupe_fingerprint = $2
`, orgSlug, dedupeFingerprint+":warning").Scan(&noPolicyRecords)
	require.NoError(t, err)
	require.Equal(t, 1, noPolicyRecords)

	first, err := svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &alertedRecordSlug,
		ProjectSlug:       &projectSlug,
		EnvironmentSlug:   &environmentSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: dedupeFingerprint,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-first",
		},
		SafeMetadata: map[string]any{
			"failure_kind": "contract",
			"summary":      "first failure",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, first.Record)
	require.Equal(t, alertedRecordSlug, first.Record.Slug)
	require.Equal(t, projectSlug, *first.Record.ProjectSlug)
	require.Equal(t, environmentSlug, *first.Record.EnvironmentSlug)
	require.Nil(t, first.Record.SourceEventSlug)
	require.Equal(t, 1, first.Record.OccurrenceCount)
	require.Len(t, producer.calls, 1)
	require.Equal(t, orgSlug, producer.calls[0].OrgSlug)
	require.Equal(t, alertedRecordSlug, producer.calls[0].RecordSlug)
	require.Equal(t, destinationSlug, producer.calls[0].DestinationSlug)

	second, err := svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &dedupedRecordSlug,
		ProjectSlug:       &projectSlug,
		EnvironmentSlug:   &environmentSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: dedupeFingerprint,
		SourceEventSlug:   &laterEventSlug,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-second",
		},
		SafeMetadata: map[string]any{
			"failure_kind": "contract",
			"summary":      "second failure",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, second.Record)
	require.Equal(t, alertedRecordSlug, second.Record.Slug)
	require.Equal(t, laterEventSlug, *second.Record.SourceEventSlug)
	require.Equal(t, 2, second.Record.OccurrenceCount)
	require.Equal(t, "run-second", second.Record.SourceRefs["run_slug"])
	require.Len(t, producer.calls, 1)

	var storedRecords int
	var storedProjectSlug string
	var storedEnvironmentSlug string
	var storedEventSlug *string
	err = pool.QueryRow(ctx, `
SELECT count(*), max(p.slug), max(e.slug), max(nr.source_event_slug)
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
LEFT JOIN projects p ON p.id = nr.project_id
LEFT JOIN environments e ON e.id = nr.environment_id
WHERE o.slug = $1
    AND nr.dedupe_fingerprint = $2
`, orgSlug, dedupeFingerprint).Scan(&storedRecords, &storedProjectSlug, &storedEnvironmentSlug, &storedEventSlug)
	require.NoError(t, err)
	require.Equal(t, 1, storedRecords)
	require.Equal(t, projectSlug.String(), storedProjectSlug)
	require.Equal(t, environmentSlug.String(), storedEnvironmentSlug)
	require.NotNil(t, storedEventSlug)
	require.Equal(t, laterEventSlug.String(), *storedEventSlug)
}

func TestService_CreateCandidateQueuesOneEmailDispatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres notification delivery E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "notification dispatch queueing")
	producer := &fakeDeliveryProducer{}
	svc := newNotificationE2EService(pool, producer, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-delivery-org")
	destinationSlug := uniqueNotificationE2ESlug("notif-delivery-email")
	policySlug := uniqueNotificationE2ESlug("notif-delivery-policy")
	firstRecordSlug := uniqueNotificationE2ESlug("notif-delivery-record")
	secondRecordSlug := uniqueNotificationE2ESlug("notif-delivery-record-second")
	dedupeFingerprint := fmt.Sprintf("pipeline:run_failed:%s", firstRecordSlug)
	seedNotificationOrg(t, ctx, pool, orgSlug)

	_, err := svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Slug:    destinationSlug,
		Name:    "Operations email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"ops@example.com"},
		},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Critical pipeline failures",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	require.NoError(t, err)

	first, err := svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &firstRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: dedupeFingerprint,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-first",
		},
		SafeMetadata: map[string]any{
			"summary": "contract failed",
		},
	})
	require.NoError(t, err)
	require.Equal(t, firstRecordSlug, first.Record.Slug)
	require.Len(t, producer.calls, 1)
	require.Equal(t, orgSlug, producer.calls[0].OrgSlug)
	require.Equal(t, firstRecordSlug, producer.calls[0].RecordSlug)
	require.Equal(t, destinationSlug, producer.calls[0].DestinationSlug)

	second, err := svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &secondRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: dedupeFingerprint,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-second",
		},
		SafeMetadata: map[string]any{
			"summary": "contract failed again",
		},
	})
	require.NoError(t, err)
	require.Equal(t, firstRecordSlug, second.Record.Slug)
	require.Len(t, producer.calls, 1)

	var summaryStatus string
	var jobKind string
	var jobQueue string
	var jobState string
	err = pool.QueryRow(ctx, `
SELECT
    nr.dispatch_summary -> $3 ->> 'status',
    nr.dispatch_summary -> $3 ->> 'job_kind',
    nr.dispatch_summary -> $3 ->> 'job_queue',
    nr.dispatch_summary -> $3 ->> 'job_state'
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = $1
    AND nr.slug = $2
`, orgSlug, firstRecordSlug, destinationSlug).Scan(&summaryStatus, &jobKind, &jobQueue, &jobState)
	require.NoError(t, err)
	require.Equal(t, "queued", summaryStatus)
	require.Equal(t, "notification_delivery", jobKind)
	require.Equal(t, "notification_delivery", jobQueue)
	require.Equal(t, "queued", jobState)
}

func TestService_DeliverNotificationRecordsQueuedAlertDeliveryAttempt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres alert delivery diagnostics E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "alert delivery queued diagnostics")
	producer := &fakeDeliveryProducer{}
	emailProducer := &testutil.FakeEmailProducer{
		EnqueueNotificationFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			model.NotificationEmailRecipient,
			model.NotificationEmail,
			slug.Slug,
		) (queue.QueuedJob, error) {
			return queue.QueuedJob{Kind: "email_send", Queue: "email", State: "queued"}, nil
		},
	}
	svc := newNotificationE2EService(pool, producer, emailProducer)

	orgSlug := uniqueNotificationE2ESlug("alert-diagnostics-org")
	destinationSlug := uniqueNotificationE2ESlug("alert-diagnostics-email")
	policySlug := uniqueNotificationE2ESlug("alert-diagnostics-policy")
	recordSlug := uniqueNotificationE2ESlug("alert-diagnostics-record")
	dedupeFingerprint := fmt.Sprintf("pipeline:run_failed:%s", recordSlug)
	seedNotificationOrg(t, ctx, pool, orgSlug)

	_, err := svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Slug:    destinationSlug,
		Name:    "Operations email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"ops@example.com"},
		},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Critical pipeline failures",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	require.NoError(t, err)

	created, err := svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &recordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: dedupeFingerprint,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-alert-diagnostics",
		},
		SafeMetadata: map[string]any{
			"summary": "contract failed",
		},
	})
	require.NoError(t, err)
	require.Equal(t, recordSlug, created.Record.Slug)
	require.Len(t, producer.calls, 1)
	require.Equal(t, policySlug, producer.calls[0].AlertPolicySlug)

	err = svc.DeliverNotification(ctx, notificationsvc.DeliverNotificationParams{
		OrgSlug:         orgSlug,
		RecordSlug:      recordSlug,
		DestinationSlug: destinationSlug,
		AlertPolicySlug: policySlug,
	})
	require.NoError(t, err)
	require.Equal(t, 1, emailProducer.EnqueueNotificationCalls)

	var gotRecordSlug string
	var gotPolicySlug string
	var gotDestinationSlug string
	var gotStatus string
	var gotDestinationType string
	var gotProviderAttempt int
	var gotJobKind string
	var gotJobQueue string
	var gotJobState string
	var gotProviderMetadata string
	var queuedAt time.Time
	var sentAt sql.NullTime
	var failedAt sql.NullTime
	err = pool.QueryRow(ctx, `
SELECT
    nr.slug,
    ap.slug,
    ad.slug,
    ada.status,
    ada.destination_type,
    ada.provider_attempt,
    ada.job_kind,
    ada.job_queue,
    ada.job_state,
    ada.provider_response_metadata::text,
    ada.queued_at,
    ada.sent_at,
    ada.failed_at
FROM alert_delivery_attempts ada
JOIN notification_records nr ON nr.id = ada.notification_record_id
JOIN organizations o ON o.id = nr.organization_id
JOIN alert_policies ap ON ap.id = ada.alert_policy_id
JOIN alert_destinations ad ON ad.id = ada.destination_id
WHERE o.slug = $1
    AND nr.slug = $2
    AND ad.slug = $3
`, orgSlug, recordSlug, destinationSlug).Scan(
		&gotRecordSlug,
		&gotPolicySlug,
		&gotDestinationSlug,
		&gotStatus,
		&gotDestinationType,
		&gotProviderAttempt,
		&gotJobKind,
		&gotJobQueue,
		&gotJobState,
		&gotProviderMetadata,
		&queuedAt,
		&sentAt,
		&failedAt,
	)
	require.NoError(t, err)
	require.Equal(t, recordSlug.String(), gotRecordSlug)
	require.Equal(t, policySlug.String(), gotPolicySlug)
	require.Equal(t, destinationSlug.String(), gotDestinationSlug)
	require.Equal(t, "queued", gotStatus)
	require.Equal(t, "email", gotDestinationType)
	require.Equal(t, 1, gotProviderAttempt)
	require.Equal(t, "email_send", gotJobKind)
	require.Equal(t, "email", gotJobQueue)
	require.Equal(t, "queued", gotJobState)
	require.JSONEq(t, `{}`, gotProviderMetadata)
	require.False(t, queuedAt.IsZero())
	require.False(t, sentAt.Valid)
	require.False(t, failedAt.Valid)

	attempts, err := svc.ListAlertDeliveryAttempts(ctx, orgSlug, recordSlug, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.False(t, attempts.HasMore)
	require.Len(t, attempts.Items, 1)
	require.Equal(t, recordSlug, attempts.Items[0].NotificationRecordSlug)
	require.NotNil(t, attempts.Items[0].AlertPolicySlug)
	require.Equal(t, policySlug, *attempts.Items[0].AlertPolicySlug)
	require.NotNil(t, attempts.Items[0].DestinationSlug)
	require.Equal(t, destinationSlug, *attempts.Items[0].DestinationSlug)
	require.Equal(t, model.AlertDeliveryAttemptStatusQueued, attempts.Items[0].Status)

	attemptSlug := attempts.Items[0].Slug
	err = svc.MarkAlertDeliveryAttemptSent(ctx, notificationsvc.MarkAlertDeliveryAttemptSentParams{
		OrgSlug:         orgSlug,
		AttemptSlug:     attemptSlug,
		ProviderAttempt: 2,
		ProviderResponseMetadata: map[string]any{
			"message_id": "safe-message-id",
		},
	})
	require.NoError(t, err)
	err = svc.MarkAlertDeliveryAttemptFailed(ctx, notificationsvc.MarkAlertDeliveryAttemptFailedParams{
		OrgSlug:         orgSlug,
		AttemptSlug:     attemptSlug,
		ProviderAttempt: 3,
		ErrorCategory:   "smtp",
		ErrorMessage:    "late worker failure",
	})
	require.ErrorIs(t, err, reponotifications.ErrDeliveryNotFound)

	var terminalStatus string
	var terminalProviderAttempt int
	var terminalSentAt sql.NullTime
	var terminalFailedAt sql.NullTime
	err = pool.QueryRow(ctx, `
SELECT
    ada.status,
    ada.provider_attempt,
    ada.sent_at,
    ada.failed_at
FROM alert_delivery_attempts ada
JOIN notification_records nr ON nr.id = ada.notification_record_id
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = $1
    AND ada.slug = $2
`, orgSlug, attemptSlug).Scan(
		&terminalStatus,
		&terminalProviderAttempt,
		&terminalSentAt,
		&terminalFailedAt,
	)
	require.NoError(t, err)
	require.Equal(t, "sent", terminalStatus)
	require.Equal(t, 2, terminalProviderAttempt)
	require.True(t, terminalSentAt.Valid)
	require.False(t, terminalFailedAt.Valid)

	_, err = pool.Exec(ctx, `
DELETE FROM alert_policies ap
USING organizations o
WHERE o.id = ap.organization_id
    AND o.slug = $1
    AND ap.slug = $2
`, orgSlug, policySlug)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
DELETE FROM alert_destinations ad
USING organizations o
WHERE o.id = ad.organization_id
    AND o.slug = $1
    AND ad.slug = $2
`, orgSlug, destinationSlug)
	require.NoError(t, err)

	attempts, err = svc.ListAlertDeliveryAttempts(ctx, orgSlug, recordSlug, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, attempts.Items, 1)
	require.Nil(t, attempts.Items[0].AlertPolicySlug)
	require.Nil(t, attempts.Items[0].DestinationSlug)
	require.Nil(t, attempts.Items[0].DestinationName)
	require.Equal(t, model.AlertDestinationTypeEmail, attempts.Items[0].DestinationType)
	require.Equal(t, model.AlertDeliveryAttemptStatusSent, attempts.Items[0].Status)

	otherOrgSlug := uniqueNotificationE2ESlug("alert-diagnostics-other-org")
	seedNotificationOrg(t, ctx, pool, otherOrgSlug)
	_, err = svc.ListAlertDeliveryAttempts(ctx, otherOrgSlug, recordSlug, model.ListRequest{Limit: 10})
	require.ErrorIs(t, err, notificationsvc.ErrRecordNotFound)
}

func TestService_DeliverNotificationRejectsUnboundAlertDestination(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres alert delivery binding E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "alert delivery unbound destination")
	emailProducer := &testutil.FakeEmailProducer{
		EnqueueNotificationFn: func(
			context.Context,
			postgres.DBTX,
			slug.Slug,
			model.NotificationEmailRecipient,
			model.NotificationEmail,
			slug.Slug,
		) (queue.QueuedJob, error) {
			return queue.QueuedJob{Kind: "email_send", Queue: "email", State: "queued"}, nil
		},
	}
	svc := newNotificationE2EService(pool, nil, emailProducer)

	orgSlug := uniqueNotificationE2ESlug("alert-unbound-org")
	boundDestinationSlug := uniqueNotificationE2ESlug("alert-unbound-bound-email")
	unboundDestinationSlug := uniqueNotificationE2ESlug("alert-unbound-other-email")
	policySlug := uniqueNotificationE2ESlug("alert-unbound-policy")
	recordSlug := uniqueNotificationE2ESlug("alert-unbound-record")
	seedNotificationOrg(t, ctx, pool, orgSlug)

	for _, destinationSlug := range []slug.Slug{boundDestinationSlug, unboundDestinationSlug} {
		_, err := svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
			OrgSlug: orgSlug,
			Slug:    destinationSlug,
			Name:    destinationSlug.String(),
			Type:    model.AlertDestinationTypeEmail,
			NonSecretConfig: map[string]any{
				"recipients": []string{"ops@example.com"},
			},
		})
		require.NoError(t, err)
	}
	_, err := svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Critical pipeline failures",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{boundDestinationSlug},
	})
	require.NoError(t, err)
	_, err = svc.UpsertRecord(ctx, notificationsvc.UpsertRecordParams{
		OrgSlug:           orgSlug,
		Slug:              recordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: fmt.Sprintf("pipeline:unbound:%s", recordSlug),
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs:        map[string]string{"run_slug": "run-unbound"},
		SafeMetadata:      map[string]any{"summary": "unbound destination"},
	})
	require.NoError(t, err)

	err = svc.DeliverNotification(ctx, notificationsvc.DeliverNotificationParams{
		OrgSlug:         orgSlug,
		RecordSlug:      recordSlug,
		DestinationSlug: unboundDestinationSlug,
		AlertPolicySlug: policySlug,
	})
	require.ErrorIs(t, err, reponotifications.ErrDeliveryInvalidInput)

	var attempts int
	err = pool.QueryRow(ctx, `
SELECT count(*)
FROM alert_delivery_attempts ada
JOIN notification_records nr ON nr.id = ada.notification_record_id
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = $1
`, orgSlug).Scan(&attempts)
	require.NoError(t, err)
	require.Zero(t, attempts)
}

func TestService_CreateCandidateRollsBackWhenProducerEnqueueFails(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres notification delivery E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "notification delivery enqueue failure retry")
	producer := &fakeDeliveryProducer{err: fmt.Errorf("river unavailable")}
	svc := newNotificationE2EService(pool, producer, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-delivery-retry-org")
	destinationSlug := uniqueNotificationE2ESlug("notif-delivery-retry-email")
	policySlug := uniqueNotificationE2ESlug("notif-delivery-retry-policy")
	recordSlug := uniqueNotificationE2ESlug("notif-delivery-retry-record")
	secondRecordSlug := uniqueNotificationE2ESlug("notif-delivery-retry-record-second")
	dedupeFingerprint := fmt.Sprintf("pipeline:run_failed:%s", recordSlug)
	seedNotificationOrg(t, ctx, pool, orgSlug)

	_, err := svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Slug:    destinationSlug,
		Name:    "Operations email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"ops@example.com"},
		},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Critical pipeline failures",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	require.NoError(t, err)

	first, err := svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &recordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: dedupeFingerprint,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-first",
		},
		SafeMetadata: map[string]any{
			"summary": "contract failed",
		},
	})
	require.ErrorContains(t, err, "enqueue notification delivery job")
	require.ErrorContains(t, err, "river unavailable")
	require.Nil(t, first)
	require.Len(t, producer.calls, 1)

	var recordCount int
	err = pool.QueryRow(ctx, `
SELECT count(*)
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = $1
    AND nr.dedupe_fingerprint = $2
`, orgSlug, dedupeFingerprint).Scan(&recordCount)
	require.NoError(t, err)
	require.Equal(t, 0, recordCount)

	producer.err = nil
	second, err := svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &secondRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: dedupeFingerprint,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-second",
		},
		SafeMetadata: map[string]any{
			"summary": "contract failed again",
		},
	})
	require.NoError(t, err)
	require.Equal(t, secondRecordSlug, second.Record.Slug)
	require.Len(t, producer.calls, 2)
	require.Equal(t, secondRecordSlug, producer.calls[1].RecordSlug)
	require.Equal(t, destinationSlug, producer.calls[1].DestinationSlug)

	summaryStatus := notificationDispatchStateForRecord(
		t,
		ctx,
		pool,
		orgSlug,
		secondRecordSlug,
		destinationSlug,
	)
	require.Equal(t, "queued", summaryStatus.String)
}

func TestService_DeliverNotificationQueuesEmailWithoutMutatingNotificationDispatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres notification delivery E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "notification delivery success")
	producer := &fakeDeliveryProducer{}
	orgSlug := uniqueNotificationE2ESlug("notif-deliver-org")
	emailProducer := &testutil.FakeEmailProducer{
		EnqueueNotificationFn: func(
			_ context.Context,
			tx postgres.DBTX,
			gotOrgSlug slug.Slug,
			recipient model.NotificationEmailRecipient,
			notification model.NotificationEmail,
			alertDeliveryAttemptSlug slug.Slug,
		) (queue.QueuedJob, error) {
			require.NotNil(t, tx)
			require.Equal(t, orgSlug, gotOrgSlug)
			require.NotEmpty(t, alertDeliveryAttemptSlug)
			require.Equal(t, "ops@example.com", recipient.Email)
			require.Equal(t, "Ops Team", recipient.FullName)
			require.Equal(t, "en", recipient.Locale)
			require.Equal(t, "Pipeline failed hard", notification.Summary)
			require.Equal(t, model.NotificationTypePipelineRunFailed, notification.NotificationType)
			require.Equal(t, model.NotificationSeverityCritical, notification.Severity)
			require.Equal(t, model.EventEntityTypePipelineRun, notification.SourceEntityType)
			require.Equal(t, model.EventActionExecute, notification.SourceAction)
			require.Equal(t, 1, notification.OccurrenceCount)
			return queue.QueuedJob{Kind: "email_send", Queue: "email", State: "queued"}, nil
		},
	}
	svc := newNotificationE2EService(pool, producer, emailProducer)

	destinationSlug := uniqueNotificationE2ESlug("notif-deliver-email")
	policySlug := uniqueNotificationE2ESlug("notif-deliver-policy")
	recordSlug := uniqueNotificationE2ESlug("notif-deliver-record")
	seedNotificationOrg(t, ctx, pool, orgSlug)

	_, err := svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Slug:    destinationSlug,
		Name:    "Operations email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []map[string]string{
				{
					"email":     "ops@example.com",
					"full_name": "Ops Team",
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Critical pipeline failures",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	require.NoError(t, err)

	_, err = svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &recordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: fmt.Sprintf("pipeline:run_failed:%s", recordSlug),
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-delivery",
		},
		SafeMetadata: map[string]any{
			"summary": "Pipeline failed hard",
		},
	})
	require.NoError(t, err)
	require.Len(t, producer.calls, 1)

	err = svc.DeliverNotification(ctx, notificationsvc.DeliverNotificationParams{
		OrgSlug:         orgSlug,
		RecordSlug:      recordSlug,
		DestinationSlug: destinationSlug,
		AlertPolicySlug: producer.calls[0].AlertPolicySlug,
	})
	require.NoError(t, err)
	require.Equal(t, 1, emailProducer.EnqueueNotificationCalls)

	var summaryStatus sql.NullString
	err = pool.QueryRow(ctx, `
SELECT
    nr.dispatch_summary -> $3 ->> 'status'
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = $1
    AND nr.slug = $2
`, orgSlug, recordSlug, destinationSlug).Scan(&summaryStatus)
	require.NoError(t, err)
	require.Equal(t, "queued", summaryStatus.String)
}

func TestService_CreateCandidateQueuesExplicitInfoAlertPolicyDispatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres notification info alert delivery E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "notification info alert delivery")
	producer := &fakeDeliveryProducer{}
	svc := newNotificationE2EService(pool, producer, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-info-org")
	destinationSlug := uniqueNotificationE2ESlug("notif-info-email")
	policySlug := uniqueNotificationE2ESlug("notif-info-policy")
	recordSlug := uniqueNotificationE2ESlug("notif-info-record")
	seedNotificationOrg(t, ctx, pool, orgSlug)

	_, err := svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Slug:    destinationSlug,
		Name:    "Info email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"ops@example.com"},
		},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Info pipeline notifications",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityInfo),
		DestinationSlugs: []slug.Slug{destinationSlug},
	})
	require.NoError(t, err)

	result, err := svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &recordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityInfo,
		DedupeFingerprint: fmt.Sprintf("pipeline:run_failed:%s", recordSlug),
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-info",
		},
		SafeMetadata: map[string]any{
			"summary": "informational update",
		},
	})
	require.NoError(t, err)
	require.Equal(t, recordSlug, result.Record.Slug)
	require.Len(t, producer.calls, 1)
	require.Equal(t, recordSlug, producer.calls[0].RecordSlug)
	require.Equal(t, destinationSlug, producer.calls[0].DestinationSlug)

	summaryStatus := notificationDispatchStateForRecord(
		t,
		ctx,
		pool,
		orgSlug,
		recordSlug,
		destinationSlug,
	)
	require.Equal(t, "queued", summaryStatus.String)
}

func TestService_CreateCandidateEvaluatesSourceAlertPolicySelectors(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres notification source alert selector E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "notification source alert selector")
	producer := &fakeDeliveryProducer{}
	svc := newNotificationE2EService(pool, producer, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-source-org")
	otherOrgSlug := uniqueNotificationE2ESlug("notif-source-other-org")
	destinationSlug := uniqueNotificationE2ESlug("notif-source-email")
	otherDestinationSlug := uniqueNotificationE2ESlug("notif-source-other-email")
	policySlug := uniqueNotificationE2ESlug("notif-source-policy")
	otherPolicySlug := uniqueNotificationE2ESlug("notif-source-other-policy")
	genericPolicySlug := uniqueNotificationE2ESlug("notif-source-generic-policy")
	mismatchedRecordSlug := uniqueNotificationE2ESlug("notif-source-mismatch")
	matchedRecordSlug := uniqueNotificationE2ESlug("notif-source-match")
	otherMatchedRecordSlug := uniqueNotificationE2ESlug("notif-source-other-match")
	genericMatchedRecordSlug := uniqueNotificationE2ESlug("notif-source-generic-match")
	sourceKind := model.EventEntityTypePipelineRun
	sourceRef := must.Slug("daily-ledger")
	projectSourceKind := model.EventEntityTypeProject
	projectSourceAction := model.EventActionCreate
	projectSourceRef := must.Slug("ops-project")

	seedNotificationOrg(t, ctx, pool, orgSlug)
	seedNotificationOrg(t, ctx, pool, otherOrgSlug)

	_, err := svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Slug:    destinationSlug,
		Name:    "Source-specific email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"ops@example.com"},
		},
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{
		OrgSlug: otherOrgSlug,
		Slug:    otherDestinationSlug,
		Name:    "Other source-specific email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"other-ops@example.com"},
		},
	})
	require.NoError(t, err)

	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Daily ledger failures",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{destinationSlug},
		SourceKind:       &sourceKind,
		SourceRef:        &sourceRef,
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          otherOrgSlug,
		Slug:             otherPolicySlug,
		Name:             "Other daily ledger failures",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunFailed),
		Severity:         notificationE2ESeverityPtr(model.NotificationSeverityCritical),
		DestinationSlugs: []slug.Slug{otherDestinationSlug},
		SourceKind:       &sourceKind,
		SourceRef:        &sourceRef,
	})
	require.NoError(t, err)
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             genericPolicySlug,
		Name:             "Project creation events",
		Scope:            model.AlertPolicyScopeOrganization,
		DestinationSlugs: []slug.Slug{destinationSlug},
		SourceKind:       &projectSourceKind,
		SourceAction:     &projectSourceAction,
		SourceRef:        &projectSourceRef,
	})
	require.NoError(t, err)

	_, err = svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &mismatchedRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: fmt.Sprintf("pipeline:run_failed:%s", mismatchedRecordSlug),
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "inventory-sync",
			"run_slug":      "run-mismatch",
		},
		SafeMetadata: map[string]any{
			"summary": "different pipeline failure",
		},
	})
	require.NoError(t, err)
	require.Empty(t, producer.calls)

	_, err = svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &matchedRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: fmt.Sprintf("pipeline:run_failed:%s", matchedRecordSlug),
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": sourceRef.String(),
			"run_slug":      "run-match",
		},
		SafeMetadata: map[string]any{
			"summary": "daily ledger failure",
		},
	})
	require.NoError(t, err)
	require.Len(t, producer.calls, 1)
	require.Equal(t, orgSlug, producer.calls[0].OrgSlug)
	require.Equal(t, matchedRecordSlug, producer.calls[0].RecordSlug)
	require.Equal(t, destinationSlug, producer.calls[0].DestinationSlug)
	require.Equal(t, policySlug, producer.calls[0].AlertPolicySlug)

	_, err = svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           otherOrgSlug,
		RecordSlug:        &otherMatchedRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: fmt.Sprintf("pipeline:run_failed:%s", otherMatchedRecordSlug),
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": sourceRef.String(),
			"run_slug":      "run-other-match",
		},
		SafeMetadata: map[string]any{
			"summary": "other tenant daily ledger failure",
		},
	})
	require.NoError(t, err)
	require.Len(t, producer.calls, 2)
	require.Equal(t, otherOrgSlug, producer.calls[1].OrgSlug)
	require.Equal(t, otherMatchedRecordSlug, producer.calls[1].RecordSlug)
	require.Equal(t, otherDestinationSlug, producer.calls[1].DestinationSlug)
	require.Equal(t, otherPolicySlug, producer.calls[1].AlertPolicySlug)

	_, err = svc.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &genericMatchedRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityWarning,
		DedupeFingerprint: fmt.Sprintf("project:create:%s", genericMatchedRecordSlug),
		SourceEntityType:  model.EventEntityTypeProject,
		SourceAction:      model.EventActionCreate,
		SourceRefs: map[string]string{
			"source_ref": projectSourceRef.String(),
		},
		SafeMetadata: map[string]any{
			"summary": "project created",
		},
	})
	require.NoError(t, err)
	require.Len(t, producer.calls, 3)
	require.Equal(t, orgSlug, producer.calls[2].OrgSlug)
	require.Equal(t, genericMatchedRecordSlug, producer.calls[2].RecordSlug)
	require.Equal(t, destinationSlug, producer.calls[2].DestinationSlug)
	require.Equal(t, genericPolicySlug, producer.calls[2].AlertPolicySlug)
}

func TestService_UpsertRecordDeduplicatesUnresolvedRecords(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres notification record E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "notification record dedupe")
	svc := newNotificationE2EService(pool, nil, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-record-org")
	secondOrgSlug := uniqueNotificationE2ESlug("notif-record-org-b")
	firstRecordSlug := uniqueNotificationE2ESlug("notif-record-first")
	secondRecordSlug := uniqueNotificationE2ESlug("notif-record-second")
	thirdRecordSlug := uniqueNotificationE2ESlug("notif-record-third")
	fourthRecordSlug := uniqueNotificationE2ESlug("notif-record-fourth")
	sourceEventSlug := uniqueNotificationE2ESlug("notif-record-event")
	dedupeFingerprint := "pipeline:failed:warehouse:prod:daily-ledger"
	seedNotificationOrg(t, ctx, pool, orgSlug)
	seedNotificationOrg(t, ctx, pool, secondOrgSlug)

	first, err := svc.UpsertRecord(ctx, notificationsvc.UpsertRecordParams{
		OrgSlug:           orgSlug,
		Slug:              firstRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityWarning,
		DedupeFingerprint: dedupeFingerprint,
		SourceEventSlug:   &sourceEventSlug,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-first",
		},
		SafeMetadata: map[string]any{
			"failure_kind": "connector",
			"summary":      "first failure",
		},
	})
	require.NoError(t, err)
	require.Equal(t, firstRecordSlug, first.Slug)
	require.Equal(t, model.NotificationLifecycleStateOpen, first.LifecycleState)
	require.Equal(t, 1, first.OccurrenceCount)
	require.Equal(t, "run-first", first.SourceRefs["run_slug"])

	second, err := svc.UpsertRecord(ctx, notificationsvc.UpsertRecordParams{
		OrgSlug:           orgSlug,
		Slug:              secondRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: dedupeFingerprint,
		SourceEventSlug:   &sourceEventSlug,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-second",
		},
		SafeMetadata: map[string]any{
			"failure_kind": "connector",
			"summary":      "second failure",
		},
	})
	require.NoError(t, err)
	require.Equal(t, firstRecordSlug, second.Slug)
	require.Equal(t, model.NotificationSeverityCritical, second.Severity)
	require.Equal(t, 2, second.OccurrenceCount)
	require.Equal(t, "run-second", second.SourceRefs["run_slug"])
	require.Equal(t, "second failure", second.SafeMetadata["summary"])

	var storedRecords int
	var storedNotificationType string
	err = pool.QueryRow(ctx, `
SELECT count(*), max(nr.notification_type)
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = $1
    AND nr.dedupe_fingerprint = $2
`, orgSlug, dedupeFingerprint).Scan(&storedRecords, &storedNotificationType)
	require.NoError(t, err)
	require.Equal(t, 1, storedRecords)
	require.Equal(t, "pipeline.run_failed", storedNotificationType)

	third, err := svc.UpsertRecord(ctx, notificationsvc.UpsertRecordParams{
		OrgSlug:           secondOrgSlug,
		Slug:              thirdRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: dedupeFingerprint,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-third",
		},
		SafeMetadata: map[string]any{
			"failure_kind": "connector",
			"summary":      "second tenant failure",
		},
	})
	require.NoError(t, err)
	require.Equal(t, thirdRecordSlug, third.Slug)

	var crossTenantRecords int
	err = pool.QueryRow(ctx, `
SELECT count(*)
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE nr.dedupe_fingerprint = $1
    AND o.slug IN ($2, $3)
`, dedupeFingerprint, orgSlug, secondOrgSlug).Scan(&crossTenantRecords)
	require.NoError(t, err)
	require.Equal(t, 2, crossTenantRecords)

	acknowledged, err := svc.AcknowledgeNotification(ctx, notificationE2EPrincipal(), orgSlug, firstRecordSlug)
	require.NoError(t, err)
	require.Equal(t, firstRecordSlug, acknowledged.Slug)
	require.Equal(t, model.NotificationLifecycleStateAcknowledged, acknowledged.LifecycleState)
	require.NotNil(t, acknowledged.AcknowledgedAt)

	resolved, err := svc.ResolveNotification(ctx, notificationE2EPrincipal(), orgSlug, firstRecordSlug)
	require.NoError(t, err)
	require.Equal(t, firstRecordSlug, resolved.Slug)
	require.Equal(t, model.NotificationLifecycleStateResolved, resolved.LifecycleState)
	require.NotNil(t, resolved.ResolvedAt)

	fourth, err := svc.UpsertRecord(ctx, notificationsvc.UpsertRecordParams{
		OrgSlug:           orgSlug,
		Slug:              fourthRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityWarning,
		DedupeFingerprint: dedupeFingerprint,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-fourth",
		},
		SafeMetadata: map[string]any{
			"failure_kind": "connector",
			"summary":      "new failure after resolution",
		},
	})
	require.NoError(t, err)
	require.Equal(t, fourthRecordSlug, fourth.Slug)
	require.Equal(t, model.NotificationLifecycleStateOpen, fourth.LifecycleState)

	rows, err := pool.Query(ctx, `
SELECT nr.lifecycle_state, nr.slug
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = $1
    AND nr.dedupe_fingerprint = $2
ORDER BY nr.lifecycle_state, nr.slug
`, orgSlug, dedupeFingerprint)
	require.NoError(t, err)
	defer rows.Close()

	statesBySlug := map[string]string{}
	for rows.Next() {
		var state, recordSlug string
		require.NoError(t, rows.Scan(&state, &recordSlug))
		statesBySlug[recordSlug] = state
	}
	require.NoError(t, rows.Err())
	require.Equal(t, map[string]string{
		firstRecordSlug.String():  "resolved",
		fourthRecordSlug.String(): "open",
	}, statesBySlug)
}

func TestService_ResolveNotificationDoesNotOverwriteProducerResolutionEvidence(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres notification lifecycle E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "notification producer resolution")
	svc := newNotificationE2EService(pool, nil, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-resolve-org")
	recordSlug := uniqueNotificationE2ESlug("notif-producer-resolved")
	seedNotificationOrg(t, ctx, pool, orgSlug)

	created, err := svc.UpsertRecord(ctx, notificationsvc.UpsertRecordParams{
		OrgSlug:           orgSlug,
		Slug:              recordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityWarning,
		DedupeFingerprint: "pipeline:producer-resolved",
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": "daily-ledger",
			"run_slug":      "run-producer-resolved",
		},
		SafeMetadata: map[string]any{
			"summary": "producer resolved failure",
		},
	})
	require.NoError(t, err)
	require.Equal(t, model.NotificationLifecycleStateOpen, created.LifecycleState)

	producerResolvedAt := time.Date(2026, 6, 30, 12, 30, 0, 0, time.UTC)
	producerUpdatedAt := producerResolvedAt.Add(time.Minute)
	_, err = pool.Exec(ctx, `
UPDATE notification_records nr
SET lifecycle_state = 'resolved',
    resolved_at = $3,
    updated_at = $4
FROM organizations o
WHERE o.id = nr.organization_id
    AND o.slug = $1
    AND nr.slug = $2
`, orgSlug, recordSlug, producerResolvedAt, producerUpdatedAt)
	require.NoError(t, err)

	got, err := svc.ResolveNotification(ctx, notificationE2EPrincipal(), orgSlug, recordSlug)
	require.NoError(t, err)
	require.Equal(t, model.NotificationLifecycleStateResolved, got.LifecycleState)
	require.NotNil(t, got.ResolvedAt)
	require.True(t, got.ResolvedAt.Equal(producerResolvedAt))

	var storedUpdatedAt time.Time
	err = pool.QueryRow(ctx, `
SELECT nr.updated_at
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = $1
    AND nr.slug = $2
`, orgSlug, recordSlug).Scan(&storedUpdatedAt)
	require.NoError(t, err)
	require.True(t, storedUpdatedAt.Equal(producerUpdatedAt))
}

func TestService_ListNotificationsScopesAndFiltersLifecycle(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres notification list E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "notification list")
	svc := newNotificationE2EService(pool, nil, nil)

	orgSlug := uniqueNotificationE2ESlug("notif-list-org")
	secondOrgSlug := uniqueNotificationE2ESlug("notif-list-org-other")
	openRecordSlug := uniqueNotificationE2ESlug("notif-list-open")
	resolvedRecordSlug := uniqueNotificationE2ESlug("notif-list-resolved")
	otherOrgRecordSlug := uniqueNotificationE2ESlug("notif-list-other")
	seedNotificationOrg(t, ctx, pool, orgSlug)
	seedNotificationOrg(t, ctx, pool, secondOrgSlug)

	_, err := svc.UpsertRecord(ctx, notificationsvc.UpsertRecordParams{
		OrgSlug:           orgSlug,
		Slug:              openRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: "list-open",
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"project_slug": "ops",
			"run_slug":     "run-open",
		},
		SafeMetadata: map[string]any{
			"summary": "open failure",
		},
	})
	require.NoError(t, err)
	_, err = svc.UpsertRecord(ctx, notificationsvc.UpsertRecordParams{
		OrgSlug:           orgSlug,
		Slug:              resolvedRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityWarning,
		DedupeFingerprint: "list-resolved",
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"project_slug": "ops",
			"run_slug":     "run-resolved",
		},
		SafeMetadata: map[string]any{
			"summary": "resolved failure",
		},
	})
	require.NoError(t, err)
	_, err = svc.UpsertRecord(ctx, notificationsvc.UpsertRecordParams{
		OrgSlug:           secondOrgSlug,
		Slug:              otherOrgRecordSlug,
		NotificationType:  model.NotificationTypePipelineRunFailed,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: "list-other-org",
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs:        map[string]string{},
		SafeMetadata:      map[string]any{"summary": "other tenant failure"},
	})
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
UPDATE notification_records nr
SET lifecycle_state = 'resolved',
    resolved_at = '2026-06-30T11:00:00Z'::timestamptz,
    last_seen_at = '2026-06-30T11:00:00Z'::timestamptz,
    dispatch_summary = jsonb_build_object(
        'ops-email',
        jsonb_build_object(
            'status', 'queued',
            'queued_at', '2026-06-30T11:01:00Z'::timestamptz,
            'job_kind', 'notification_delivery',
            'job_queue', 'notification_delivery',
            'job_state', 'queued'
        )
    ),
    updated_at = NOW()
FROM organizations o
WHERE o.id = nr.organization_id
    AND o.slug = $1
    AND nr.slug = $2
`, orgSlug, resolvedRecordSlug)
	require.NoError(t, err)

	active, err := svc.ListNotifications(ctx, orgSlug, notificationsvc.ListNotificationsFilters{}, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, active.Items, 1)
	require.Equal(t, openRecordSlug, active.Items[0].Slug)
	require.Equal(t, "open failure", active.Items[0].Summary)

	resolved := model.NotificationLifecycleStateResolved
	recent, err := svc.ListNotifications(
		ctx,
		orgSlug,
		notificationsvc.ListNotificationsFilters{LifecycleState: &resolved},
		model.ListRequest{Limit: 10},
	)
	require.NoError(t, err)
	require.Len(t, recent.Items, 1)
	require.Equal(t, resolvedRecordSlug, recent.Items[0].Slug)
	require.Equal(t, model.NotificationLifecycleStateResolved, recent.Items[0].LifecycleState)
	require.Equal(t, "resolved failure", recent.Items[0].Summary)
	require.NotNil(t, recent.Items[0].ResolvedAt)
	require.Len(t, recent.Items[0].DispatchStatuses, 1)
	require.Equal(t, model.NotificationDispatchStatusQueued, recent.Items[0].DispatchStatuses[0].Status)
	require.Equal(t, must.Slug("ops-email"), recent.Items[0].DispatchStatuses[0].DestinationSlug)
	require.NotNil(t, recent.Items[0].DispatchStatuses[0].QueuedAt)
}

func notificationDispatchStateForRecord(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgSlug, recordSlug, destinationSlug slug.Slug,
) sql.NullString {
	t.Helper()

	var summaryStatus sql.NullString
	err := pool.QueryRow(ctx, `
SELECT
    nr.dispatch_summary -> $3 ->> 'status'
FROM notification_records nr
JOIN organizations o ON o.id = nr.organization_id
WHERE o.slug = $1
    AND nr.slug = $2
`, orgSlug, recordSlug, destinationSlug).Scan(&summaryStatus)
	require.NoError(t, err)
	return summaryStatus
}

func seedNotificationOrg(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgSlug slug.Slug) {
	t.Helper()

	_, err := pool.Exec(ctx, `
INSERT INTO organizations (slug, name)
VALUES ($1, $2)
`, orgSlug, orgSlug.String())
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		// Remove project bindings before the environment's RESTRICT foreign key.
		_, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, cleanupErr)
	})
}

func seedAlertPolicyScopeRows(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug, destinationSlug slug.Slug,
) {
	t.Helper()

	seedNotificationOrg(t, ctx, pool, orgSlug)

	_, err := pool.Exec(ctx, `
INSERT INTO projects (organization_id, slug, name)
SELECT id, $2, $3
FROM organizations
WHERE slug = $1
`, orgSlug, projectSlug, projectSlug.String())
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
INSERT INTO environments (organization_id, slug, name)
SELECT id, $2, $3
FROM organizations
WHERE slug = $1
`, orgSlug, environmentSlug, environmentSlug.String())
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
INSERT INTO alert_destinations (organization_id, slug, name, type, non_secret_config, enabled)
SELECT id, $2, $3, 'email', '{"recipients":["ops@example.com"]}'::jsonb, true
FROM organizations
WHERE slug = $1
`, orgSlug, destinationSlug, destinationSlug.String())
	require.NoError(t, err)
}

func enableProjectEnvironment(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) {
	t.Helper()

	_, err := pool.Exec(ctx, `
INSERT INTO project_environments (organization_id, project_id, environment_id, is_default)
SELECT p.organization_id, p.id, e.id, false
FROM organizations o
JOIN projects p ON p.organization_id = o.id
JOIN environments e ON e.organization_id = o.id
WHERE o.slug = $1
    AND p.slug = $2
    AND e.slug = $3
`, orgSlug, projectSlug, environmentSlug)
	require.NoError(t, err)
}

func uniqueNotificationE2ESlug(prefix string) slug.Slug {
	return must.Slug(fmt.Sprintf("%s-%d", prefix, notificationE2ESlugSequence.Add(1)))
}
