package e2e

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/Braidplane/braidplane-core/service/worker/producer"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

type candidateHandoffEnqueuer struct {
	base           queue.Enqueuer
	rejectDelivery atomic.Bool
	failed         chan struct{}
}

func (e *candidateHandoffEnqueuer) EnqueueTx(ctx context.Context, tx postgres.DBTX, request queue.EnqueueRequest) (queue.QueuedJob, error) {
	if request.Args.Kind() == (jobs.NotificationDeliveryArgs{}).Kind() && e.rejectDelivery.Load() {
		select {
		case e.failed <- struct{}{}:
		default:
		}
		return queue.QueuedJob{}, xerror.Unavailable(xerror.Notification)
	}
	return e.base.EnqueueTx(ctx, tx, request)
}

type candidateHandoffRetryPolicy struct{}

func (candidateHandoffRetryPolicy) NextRetry(*rivertype.JobRow) time.Time {
	return time.Now().Add(20 * time.Millisecond)
}

type candidateHandoffDelivery struct {
	notificationsvc.Service
	delivered chan notificationsvc.DeliverNotificationParams
}

func (s candidateHandoffDelivery) DeliverNotification(_ context.Context, params notificationsvc.DeliverNotificationParams) error {
	s.delivered <- params
	return nil
}

func TestGenericCandidateHandoffRecoversWithoutChangingRunOutcome(t *testing.T) {
	testGenericCandidateHandoff(t, true)
}

func TestGenericCandidateHandoffHealthyTiming(t *testing.T) {
	testGenericCandidateHandoff(t, false)
}

func testGenericCandidateHandoff(t *testing.T, failDeliveryHandoff bool) {
	t.Helper()
	if testing.Short() {
		t.Skip("requires disposable Postgres and River")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "generic candidate handoff")
	org, project, environment, destination := uniqueNotificationE2ESlug("candidate-org"), slug.Slug("ops"), slug.Slug("dev"), slug.Slug("mail")
	seedAlertPolicyScopeRows(t, ctx, pool, org, project, environment, destination)
	enableProjectEnvironment(t, ctx, pool, org, project, environment)
	_, err := pool.Exec(ctx, `INSERT INTO pipeline_specs (project_id, slug, name, spec_jsonb, spec_hash, source_path, source_ref, last_applied_commit)
SELECT p.id, 'report', 'Report', '{}'::jsonb, 'candidate-test', 'pipelines/report.json', 'refs/heads/main', '0000000000000000000000000000000000000000'
FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND p.slug=$2`, org, project)
	require.NoError(t, err)
	var version int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO pipeline_versions (project_id, pipeline_slug, spec_jsonb, spec_hash)
SELECT p.id, 'report', '{}'::jsonb, 'candidate-test' FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND p.slug=$2 RETURNING id`, org, project).Scan(&version))
	txm := postgres.NewTransactionManager(pool)
	enqueueClient, err := queue.New(pool, nil)
	require.NoError(t, err)
	enqueuer := &candidateHandoffEnqueuer{base: queue.NewEnqueuer(enqueueClient), failed: make(chan struct{}, 1)}
	enqueuer.rejectDelivery.Store(failDeliveryHandoff)
	deliveryProducer, err := producer.NewNotificationDeliveryProducer(enqueuer)
	require.NoError(t, err)
	candidateProducer, err := producer.NewNotificationCandidateProducer(enqueuer)
	require.NoError(t, err)
	svc := notificationsvc.New(txm, reponotifications.New(), clock.New(), deliveryProducer, &testutil.FakeEmailProducer{}, notificationsvc.LifecycleEventProducerFunc(func(context.Context, postgres.DBTX, slug.Slug, model.EventActor, eventrecorder.Event) {}), notificationsvc.ConnectionProviders{}, "https://app.example")
	kind := model.NotificationTypePipelineContractFailure
	_, err = svc.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{OrgSlug: org, Slug: "contracts", Name: "Contracts", Scope: model.AlertPolicyScopeProjectEnvironment, ProjectSlug: &project, EnvironmentSlug: &environment, NotificationType: &kind, DestinationSlugs: []slug.Slug{destination}})
	require.NoError(t, err)
	at := time.Now().UTC()
	result := &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{RunSlug: "contract-run", Status: "running", PipelineVersionID: version},
		Timeline: pipelinerun.RunTimeline{RunSlug: "contract-run", PipelineSlug: "report", Status: "running", Events: []pipelinerun.TimelineEvent{{At: at, Type: "run_started", Status: "running"}}, StructuralChecks: []pipelinerun.StructuralCheck{{ID: "check-product", Step: "supplier", Origin: "step", Phase: "producer_output", Declaration: "step/producer_output/products/v1/product", ProjectionDigest: "projection", Status: pipelinerun.CheckStatusFailed, ReasonCode: "contract_violation", CompletedAt: &at, ContractFailure: &pipelinerun.ContractFailureDiagnostic{FieldName: "product_code", Rule: "required"}}}},
	}
	store := pipelineruntime.NewRunStore(txm, pool, clock.New(), candidateProducer, pipelineruns.New())
	require.NoError(t, store.PersistRun(ctx, org, project, environment, &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "report"}}, result))
	assertRun := func() {
		t.Helper()
		var status string
		require.NoError(t, pool.QueryRow(ctx, `SELECT r.status FROM pipeline_runs r JOIN projects p ON p.id=r.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND r.run_slug='contract-run'`, org).Scan(&status))
		require.Equal(t, "running", status, "notification processing cannot replace the source outcome")
	}
	assertRun()
	workers := river.NewWorkers()
	river.AddWorker(workers, consumer.NewNotificationCandidateWorker(svc))
	delivery := candidateHandoffDelivery{Service: svc, delivered: make(chan notificationsvc.DeliverNotificationParams, 4)}
	river.AddWorker(workers, consumer.NewNotificationDeliveryWorker(delivery, slog.New(slog.DiscardHandler)))
	worker, err := queue.New(pool, &queue.Config{Workers: workers, Queues: map[string]river.QueueConfig{jobs.NotificationDeliveryQueue: {MaxWorkers: 2}}, RetryPolicy: candidateHandoffRetryPolicy{}, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	require.NoError(t, worker.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, worker.Stop(stopCtx))
	})
	if failDeliveryHandoff {
		select {
		case <-enqueuer.failed:
		case <-time.After(5 * time.Second):
			t.Fatal("candidate worker did not attempt the generic delivery handoff")
		}
		assertRun()
		enqueuer.rejectDelivery.Store(false)
	}
	var dispatched notificationsvc.DeliverNotificationParams
	select {
	case dispatched = <-delivery.delivered:
	case <-time.After(10 * time.Second):
		t.Fatal("durable candidate did not recover after the enqueue failure")
	}
	attemptObservedAt := time.Now().UTC()
	assertRun()
	record, err := svc.GetNotification(ctx, org, dispatched.RecordSlug)
	require.NoError(t, err)
	require.Equal(t, 1, record.OccurrenceCount)
	require.Equal(t, "product_code", record.SafeMetadata["field"])
	require.Equal(t, "v1", record.SafeMetadata["contract_revision"])
	require.Equal(t, "contract-run", record.SourceRefs["run_slug"])
	if !failDeliveryHandoff {
		require.Len(t, record.DispatchStatuses, 1)
		require.NotNil(t, record.DispatchStatuses[0].QueuedAt)
		t.Logf("generic handoff timing: failure=%s candidate_persisted=%s delivery_enqueued=%s test_delivery_started=%s persist_delay=%s enqueue_delay=%s start_delay=%s (test adapter; no provider acceptance)",
			at.Format(time.RFC3339Nano), record.CreatedAt.Format(time.RFC3339Nano), record.DispatchStatuses[0].QueuedAt.Format(time.RFC3339Nano), attemptObservedAt.Format(time.RFC3339Nano), record.CreatedAt.Sub(at), record.DispatchStatuses[0].QueuedAt.Sub(at), attemptObservedAt.Sub(at))
	}
	_, err = svc.CreateCandidate(ctx, model.NotificationCandidate{OrgSlug: org, RecordSlug: &dispatched.RecordSlug, ProjectSlug: &project, EnvironmentSlug: &environment, NotificationType: kind, Severity: model.NotificationSeverityWarning, DedupeFingerprint: dispatched.RecordSlug.String(), SourceEntityType: model.EventEntityTypePipelineRun, SourceAction: model.EventActionExecute, SourceRefs: record.SourceRefs})
	require.NoError(t, err)
	record, err = svc.GetNotification(ctx, org, dispatched.RecordSlug)
	require.NoError(t, err)
	require.Equal(t, 1, record.OccurrenceCount, "candidate replay must not become another occurrence")
}
