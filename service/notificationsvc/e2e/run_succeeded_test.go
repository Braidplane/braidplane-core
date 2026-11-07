package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	slacknotifications "github.com/Braidplane/braidplane-core/service/notificationsvc/provider/slack"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/stretchr/testify/require"
)

func TestSuccessfulRunNotificationPersistenceAndExplicitOptIn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "successful run notifications")
	org, project, environment, destination := uniqueNotificationE2ESlug("success-org"), slug.Slug("ops"), slug.Slug("prod"), slug.Slug("mail")
	seedAlertPolicyScopeRows(t, ctx, pool, org, project, environment, destination)
	enableProjectEnvironment(t, ctx, pool, org, project, environment)
	_, err := pool.Exec(ctx, `INSERT INTO pipeline_specs (project_id, slug, name, spec_jsonb, spec_hash, source_path, source_ref, last_applied_commit)
SELECT p.id, 'orders', 'Orders', '{}'::jsonb, 'success-test', 'pipelines/orders.json', 'refs/heads/main', '0000000000000000000000000000000000000000'
FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND p.slug=$2`, org, project)
	require.NoError(t, err)
	var version int64
	err = pool.QueryRow(ctx, `INSERT INTO pipeline_versions (project_id, pipeline_slug, spec_jsonb, spec_hash)
SELECT p.id, 'orders', '{}'::jsonb, 'success-test' FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND p.slug=$2 RETURNING id`, org, project).Scan(&version)
	require.NoError(t, err)
	producer := &fakeDeliveryProducer{}
	txm := postgres.NewTransactionManager(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	clk := testutil.FakeClock{NowFn: func() time.Time { return now }}
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	credentials, err := credentialssvc.New(txm, repocredentials.New(), cipher, credentialssvc.ProviderRegistry{"slack": notificationInstallProvider{}}, clk, false)
	require.NoError(t, err)
	connection := slug.Slug("finance")
	_, err = credentials.CreateNotificationConnection(ctx, credentialssvc.CreateNotificationConnectionParams{OrgSlug: org, Slug: connection, Name: "Finance Slack", Provider: "slack"})
	require.NoError(t, err)
	install, err := credentials.ConnectNotificationConnection(ctx, credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindNotificationConnection, OrgSlug: org, NotificationConnectionSlug: connection}, model.SystemEventActor())
	require.NoError(t, err)
	_, err = credentials.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{Provider: "slack", State: install.State, Code: "synthetic-code"})
	require.NoError(t, err)
	sends := 0
	sender := slacknotifications.New(credentials, notificationWebhookFunc(func(_ context.Context, _, text string) error {
		sends++
		require.Contains(t, text, "https://app.example/orgs/"+org.String()+"/projects/ops/runs/run-success?environment_slug=prod")
		if sends == 1 {
			return xerror.Unexpected().Detail("synthetic provider timeout")
		}
		return nil
	}))
	svc := notificationsvc.New(txm, reponotifications.New(), clk, producer, &testutil.FakeEmailProducer{}, notificationsvc.LifecycleEventProducerFunc(func(context.Context, postgres.DBTX, slug.Slug, model.EventActor, eventrecorder.Event) {}), notificationsvc.ConnectionProviders{model.AlertDestinationTypeSlack: sender}, "https://app.example")
	destination = "finance-slack"
	_, err = svc.CreateAlertDestination(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertDestinationParams{OrgSlug: org, Slug: destination, Name: "Finance", Type: model.AlertDestinationTypeSlack, NonSecretConfig: map[string]any{"connection_slug": connection.String()}})
	require.NoError(t, err)
	var successfulCandidate, failedCandidate model.NotificationCandidate
	store := pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{
		CandidateFn: func(ctx context.Context, _ postgres.DBTX, candidate model.NotificationCandidate) error {
			if candidate.SourceRefs["run_slug"] == "run-failed" {
				failedCandidate = candidate
			}
			if candidate.SourceRefs["run_slug"] == "run-success" {
				successfulCandidate = candidate
			}
			_, err := svc.CreateCandidate(ctx, candidate)
			return err
		},
	}, pipelineruns.New())
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}}
	persist := func(ctx context.Context, run, status string) error {
		now := time.Now().UTC()
		result := &pipelinerun.RunResult{
			Metadata: pipelinerun.RunMetadata{RunSlug: run, Status: status, PipelineVersionID: version},
			Timeline: pipelinerun.RunTimeline{RunSlug: run, PipelineSlug: "orders", Status: status, Events: []pipelinerun.TimelineEvent{{At: now, Type: "run_completed", Status: status}}},
		}
		return store.PersistRun(ctx, org, project, environment, pipeline, result)
	}
	// Generic info/source policies must not turn success delivery on implicitly.
	info, source := model.NotificationSeverityInfo, model.EventEntityTypePipelineRun
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{OrgSlug: org, Slug: "generic", Name: "Generic", Scope: model.AlertPolicyScopeOrganization, Severity: &info, SourceKind: &source, DestinationSlugs: []slug.Slug{destination}})
	require.NoError(t, err)
	require.NoError(t, persist(ctx, "before-opt-in", "succeeded"))
	require.Empty(t, producer.calls)
	for _, status := range []string{"failed", "canceled", "running", "queued", "partial"} {
		require.NoError(t, persist(ctx, "run-"+status, status))
	}
	require.NotNil(t, failedCandidate.RecordSlug)
	failedRecord, err := svc.GetNotification(ctx, org, *failedCandidate.RecordSlug)
	require.NoError(t, err)
	require.Equal(t, model.NotificationTypePipelineRunFailed, failedRecord.NotificationType)
	require.Equal(t, model.NotificationLifecycleStateOpen, failedRecord.LifecycleState)
	require.Equal(t, 1, failedRecord.OccurrenceCount)
	_, err = svc.CreateCandidate(ctx, failedCandidate)
	require.NoError(t, err)
	failedRecord, err = svc.GetNotification(ctx, org, *failedCandidate.RecordSlug)
	require.NoError(t, err)
	require.Equal(t, 1, failedRecord.OccurrenceCount)
	pipelineRef, otherRef := slug.Slug("orders"), slug.Slug("other")
	for _, p := range []notificationsvc.CreateAlertPolicyParams{
		{OrgSlug: org, Slug: "success", Name: "Success", Scope: model.AlertPolicyScopeProjectEnvironment, ProjectSlug: &project, EnvironmentSlug: &environment, NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunSucceeded), SourceKind: &source, SourceRef: &pipelineRef, DestinationSlugs: []slug.Slug{destination}},
		{OrgSlug: org, Slug: "wrong-pipeline", Name: "Wrong pipeline", Scope: model.AlertPolicyScopeOrganization, NotificationType: notificationE2ENotificationTypePtr(model.NotificationTypePipelineRunSucceeded), SourceKind: &source, SourceRef: &otherRef, DestinationSlugs: []slug.Slug{destination}},
	} {
		_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), p)
		require.NoError(t, err)
	}
	require.NoError(t, persist(ctx, "run-success", "succeeded"))
	require.Len(t, producer.calls, 1)
	require.Equal(t, slug.Slug("success"), producer.calls[0].AlertPolicySlug)
	recordSlug := producer.calls[0].RecordSlug
	record, err := svc.GetNotification(ctx, org, recordSlug)
	require.NoError(t, err)
	require.Equal(t, model.NotificationSeverityInfo, record.Severity)
	require.Equal(t, model.NotificationLifecycleStateResolved, record.LifecycleState)
	require.NotNil(t, record.ResolvedAt)
	require.Equal(t, "run-success", record.SourceRefs["run_slug"])
	require.Equal(t, "orders", record.SourceRefs["pipeline_slug"])
	require.Equal(t, "prod", record.SourceRefs["environment_slug"])

	_, err = svc.CreateCandidate(ctx, successfulCandidate)
	require.NoError(t, err)
	require.Len(t, producer.calls, 1, "repeat completion neither increments occurrences nor enqueues again")
	record, err = svc.GetNotification(ctx, org, recordSlug)
	require.NoError(t, err)
	require.Equal(t, 1, record.OccurrenceCount)
	acknowledged, err := svc.AcknowledgeNotification(ctx, notificationE2EPrincipal(), org, recordSlug)
	require.NoError(t, err)
	require.Equal(t, model.NotificationLifecycleStateResolved, acknowledged.LifecycleState)
	require.Nil(t, acknowledged.AcknowledgedAt)

	delivery := notificationsvc.DeliverNotificationParams{OrgSlug: org, RecordSlug: recordSlug, DestinationSlug: destination, AlertPolicySlug: "success"}
	var providerError *notificationsvc.DeliveryError
	require.ErrorAs(t, svc.DeliverNotification(ctx, delivery), &providerError)
	require.True(t, providerError.Retryable)
	var persistedStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT r.status FROM pipeline_runs r JOIN project_environments pe ON pe.id=r.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND r.run_slug='run-success'`, org).Scan(&persistedStatus))
	require.Equal(t, "succeeded", persistedStatus, "provider failure preserves the successful data outcome")
	now = now.Add(31 * time.Second)
	require.NoError(t, svc.DeliverNotification(ctx, delivery))
	require.NoError(t, svc.DeliverNotification(ctx, delivery))
	require.Equal(t, 2, sends)
	attempts, err := svc.ListAlertDeliveryAttempts(ctx, org, recordSlug, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, attempts.Items, 1)
	require.Equal(t, model.AlertDeliveryAttemptStatusSent, attempts.Items[0].Status)
	require.Equal(t, 2, attempts.Items[0].ProviderAttempt)

	otherOrg := uniqueNotificationE2ESlug("success-other")
	seedNotificationOrg(t, ctx, pool, otherOrg)
	otherScope := successfulCandidate
	otherScope.OrgSlug = otherOrg
	_, err = svc.CreateCandidate(ctx, otherScope)
	require.Error(t, err)
	otherScope = successfulCandidate
	otherScope.EnvironmentSlug = new(slug.Slug("other"))
	otherRecord := slug.Slug("other-scope-record")
	otherScope.RecordSlug = &otherRecord
	_, err = svc.CreateCandidate(ctx, otherScope)
	require.Error(t, err)
	require.Len(t, producer.calls, 1)

	rollback := xerror.Unexpected().Detail("abort completion")
	err = txm.RunInTx(ctx, func(inner context.Context, _ postgres.DBTX) error {
		require.NoError(t, persist(inner, "rolled-back", "succeeded"))
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM notification_records nr JOIN organizations o ON o.id=nr.organization_id WHERE o.slug=$1 AND nr.source_refs->>'run_slug'='rolled-back'`, org).Scan(&count))
	require.Zero(t, count, "notification rolls back with the run outcome")
	producer.calls = producer.calls[:1] // The fake queue has no transaction; discard its rolled-back observation.
	producer.err = xerror.Unexpected().Detail("queue unavailable")
	require.Error(t, persist(ctx, "enqueue-failed", "succeeded"))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_runs r JOIN project_environments pe ON pe.id=r.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND r.run_slug='enqueue-failed'`, org).Scan(&count))
	require.Zero(t, count)
	producer.err = nil
	producer.calls = producer.calls[:1]
	require.NoError(t, persist(ctx, "recovered-run", "succeeded"))
	require.Len(t, producer.calls, 2, "a new recovery run identity has its own success")
	var total int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM notification_records nr JOIN organizations o ON o.id=nr.organization_id WHERE o.slug=$1 AND nr.notification_type='pipeline.run_succeeded'`, org).Scan(&total))
	require.Equal(t, 3, total, "only the three durably successful runs have records")
}
