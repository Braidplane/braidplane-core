package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
	retentionrepo "github.com/Braidplane/braidplane-core/postgres/retention"
	"github.com/Braidplane/braidplane-core/postgres/wishlistregistrations"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
	workerhostsvc "github.com/Braidplane/braidplane-core/service/worker"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

func TestWorkerHostEventRecorderCommittedJobPersistsEvent(t *testing.T) { // Real River/Postgres worker E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	orgSlug := slug.Slug(fmt.Sprintf("worker-e2e-events-%d", time.Now().UnixNano()))
	entitySlug := slug.Slug(fmt.Sprintf("daily-users-exec-%d", time.Now().UnixNano()))
	argsFilter := map[string]any{"entity_slug": entitySlug.String()}
	cleanupRiverJobsByArgs(t, pool, jobs.EventsQueue, jobs.EventRecordArgs{}.Kind(), argsFilter)
	seedWorkerE2EOrg(t, pool, orgSlug)

	eventProducer, eventsSvc := newWorkerE2EEventProducer(t, pool)
	queued := enqueueEventRecordInTx(
		t,
		pool,
		eventProducer,
		orgSlug,
		model.APIKeyEventActor(slug.Slug("etl-worker")),
		workerE2ETestEvent{record: eventrecorder.Record{
			SlugPrefix: slug.Slug("evt-query-execute"),
			EntityType: model.EventEntityTypeQueryExecution,
			EntitySlug: entitySlug,
			Action:     model.EventActionExecute,
			Metadata: map[string]any{
				"query_slug": "daily-users",
			},
		}},
		false,
	)

	require.Positive(t, queued.ID)
	require.Equal(t, queue.QueuedJob{
		ID:    queued.ID,
		Kind:  jobs.EventRecordArgs{}.Kind(),
		Queue: jobs.EventsQueue,
		State: "queued",
	}, queued)

	job := waitForRiverJobState(
		t,
		ctx,
		pool,
		jobs.EventsQueue,
		jobs.EventRecordArgs{}.Kind(),
		argsFilter,
		"completed",
	)
	require.Equal(t, "completed", job.state)

	entityType := model.EventEntityTypeQueryExecution
	action := model.EventActionExecute
	events, err := eventsSvc.ListEvents(ctx, orgSlug, eventssvc.ListEventsFilters{
		EntityType: &entityType,
		EntitySlug: &entitySlug,
		Action:     &action,
	}, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, events.Items, 1)
	require.Equal(t, model.EventActorTypeAPIKey, events.Items[0].ActorType)
	require.Equal(t, "daily-users", events.Items[0].Metadata["query_slug"])
}

func TestWorkerHostEventPruneJobDeletesExpiredEvents(t *testing.T) { // Real River/Postgres worker E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	orgSlug := slug.Slug(fmt.Sprintf("worker-e2e-events-prune-%d", time.Now().UnixNano()))
	seedWorkerE2EOrg(t, pool, orgSlug)
	unrelatedOrgSlug := slug.Slug(fmt.Sprintf("worker-e2e-events-prune-unrelated-%d", time.Now().UnixNano()))
	seedWorkerE2EOrg(t, pool, unrelatedOrgSlug)

	client, eventsSvc := newWorkerE2EEventsHarness(t, pool, orgSlug)
	enqueuer := queue.NewEnqueuer(client)

	expiredSlug := slug.Slug(fmt.Sprintf("evt-prune-expired-%d", time.Now().UnixNano()))
	freshSlug := slug.Slug(fmt.Sprintf("evt-prune-fresh-%d", time.Now().UnixNano()))
	expiredEntitySlug := slug.Slug(fmt.Sprintf("entity-expired-%d", time.Now().UnixNano()))
	freshEntitySlug := slug.Slug(fmt.Sprintf("entity-fresh-%d", time.Now().UnixNano()))

	_, err := eventsSvc.CreateEvent(ctx, eventssvc.CreateEventParams{
		OrgSlug:    orgSlug,
		Slug:       expiredSlug,
		ActorType:  model.EventActorTypeSystem,
		EntityType: model.EventEntityTypeQueryExecution,
		EntitySlug: &expiredEntitySlug,
		Action:     model.EventActionExecute,
		OccurredAt: time.Now().UTC().Add(-(eventssvc.DefaultRetentionPeriod + 24*time.Hour)),
	})
	require.NoError(t, err)

	_, err = eventsSvc.CreateEvent(ctx, eventssvc.CreateEventParams{
		OrgSlug:    orgSlug,
		Slug:       freshSlug,
		ActorType:  model.EventActorTypeSystem,
		EntityType: model.EventEntityTypeQueryExecution,
		EntitySlug: &freshEntitySlug,
		Action:     model.EventActionExecute,
		OccurredAt: time.Now().UTC().Add(-24 * time.Hour),
	})
	require.NoError(t, err)

	afterID := maxRiverJobIDForKind(t, ctx, pool, jobs.EventMaintenanceQueue, jobs.EventPruneArgs{}.Kind())
	queued := enqueueEventPruneInTx(t, pool, enqueuer)
	require.Positive(t, queued.ID)
	require.Equal(t, queue.QueuedJob{
		ID:    queued.ID,
		Kind:  jobs.EventPruneArgs{}.Kind(),
		Queue: jobs.EventMaintenanceQueue,
		State: "queued",
	}, queued)

	job := waitForRiverJobStateAfterID(
		t,
		ctx,
		pool,
		afterID,
		jobs.EventMaintenanceQueue,
		jobs.EventPruneArgs{}.Kind(),
		"completed",
	)
	require.Equal(t, "completed", job.state)
	require.Equal(t, 1, job.attempt)
	runs, err := newWorkerE2ERetentionService(pool).ListMaintenanceRuns(ctx, unrelatedOrgSlug)
	require.NoError(t, err)
	require.Empty(t, runs)

	_, err = eventsSvc.GetEvent(ctx, orgSlug, expiredSlug)
	require.ErrorIs(t, err, eventssvc.ErrEventNotFound)

	freshEvent, err := eventsSvc.GetEvent(ctx, orgSlug, freshSlug)
	require.NoError(t, err)
	require.Equal(t, freshSlug, freshEvent.Slug)
}

func TestOrganizationRetentionServiceScopesEventPolicies(t *testing.T) { // Real Postgres E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres retention service E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	shortOrgSlug := slug.Slug(fmt.Sprintf("worker-e2e-retention-short-%d", time.Now().UnixNano()))
	longOrgSlug := slug.Slug(fmt.Sprintf("worker-e2e-retention-long-%d", time.Now().UnixNano()))
	seedWorkerE2EOrg(t, pool, shortOrgSlug)
	seedWorkerE2EOrg(t, pool, longOrgSlug)

	_, err := pool.Exec(ctx, `
		UPDATE organizations
		SET settings = jsonb_build_object(
			'retention', jsonb_build_object(
				'query_execution_retention_days', 30,
				'replay_checkpoint_retention_days', 30,
				'run_history_retention_days', 30,
				'event_log_retention_days', 1
			)
		)
		WHERE slug = $1
	`, shortOrgSlug.String())
	require.NoError(t, err)

	_, eventsSvc := newWorkerE2EEventsHarness(t, pool, shortOrgSlug, longOrgSlug)
	shortEventSlug := slug.Slug(fmt.Sprintf("evt-retention-short-%d", time.Now().UnixNano()))
	longEventSlug := slug.Slug(fmt.Sprintf("evt-retention-long-%d", time.Now().UnixNano()))
	entitySlug := slug.Slug(fmt.Sprintf("entity-retention-%d", time.Now().UnixNano()))
	for _, event := range []struct {
		orgSlug slug.Slug
		slug    slug.Slug
	}{
		{orgSlug: shortOrgSlug, slug: shortEventSlug},
		{orgSlug: longOrgSlug, slug: longEventSlug},
	} {
		_, err = eventsSvc.CreateEvent(ctx, eventssvc.CreateEventParams{
			OrgSlug:    event.orgSlug,
			Slug:       event.slug,
			ActorType:  model.EventActorTypeSystem,
			EntityType: model.EventEntityTypeQueryExecution,
			EntitySlug: &entitySlug,
			Action:     model.EventActionExecute,
			OccurredAt: now.AddDate(0, 0, -2),
		})
		require.NoError(t, err)
	}

	retentionSvc := newWorkerE2EScopedRetentionService(
		pool,
		retentionrepo.New(),
		testutil.FakeClock{NowFn: func() time.Time { return now }},
		shortOrgSlug,
		longOrgSlug,
	)
	sourceJobID := time.Now().UnixNano()
	result, err := retentionSvc.PruneExpiredCustomerData(ctx, retentionsvc.MaintenanceExecution{
		SourceJobID: sourceJobID,
		Attempt:     1,
		StartedAt:   now,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Events)

	shortRuns, err := retentionSvc.ListMaintenanceRuns(ctx, shortOrgSlug)
	require.NoError(t, err)
	require.Len(t, shortRuns, 1)
	require.Equal(t, model.OrganizationRetentionRunStatusSucceeded, shortRuns[0].Status)
	require.EqualValues(t, 1, shortRuns[0].EventsDeleted)
	require.Equal(t, 1, shortRuns[0].PolicySnapshot.EventLogRetentionDays)

	longRuns, err := retentionSvc.ListMaintenanceRuns(ctx, longOrgSlug)
	require.NoError(t, err)
	require.Len(t, longRuns, 1)
	require.Equal(t, model.OrganizationRetentionRunStatusSucceeded, longRuns[0].Status)
	require.Zero(t, longRuns[0].EventsDeleted)
	require.Equal(t, model.DefaultOrganizationRetentionPolicy(), *longRuns[0].PolicySnapshot)

	_, err = eventsSvc.GetEvent(ctx, shortOrgSlug, shortEventSlug)
	require.ErrorIs(t, err, eventssvc.ErrEventNotFound)
	longEvent, err := eventsSvc.GetEvent(ctx, longOrgSlug, longEventSlug)
	require.NoError(t, err)
	require.Equal(t, longEventSlug, longEvent.Slug)

	retryResult, err := retentionSvc.PruneExpiredCustomerData(ctx, retentionsvc.MaintenanceExecution{
		SourceJobID: sourceJobID,
		Attempt:     2,
		StartedAt:   now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Zero(t, retryResult.QueryExecutions)
	require.Zero(t, retryResult.ReplayCheckpoints)
	require.Zero(t, retryResult.PipelineRuns)
	require.Zero(t, retryResult.Events)

	shortRunsAfterRetry, err := retentionSvc.ListMaintenanceRuns(ctx, shortOrgSlug)
	require.NoError(t, err)
	require.Len(t, shortRunsAfterRetry, 1)
	require.Equal(t, shortRuns[0].Slug, shortRunsAfterRetry[0].Slug)
	longRunsAfterRetry, err := retentionSvc.ListMaintenanceRuns(ctx, longOrgSlug)
	require.NoError(t, err)
	require.Len(t, longRunsAfterRetry, 1)
	require.Equal(t, longRuns[0].Slug, longRunsAfterRetry[0].Slug)
}

func TestOrganizationRetentionServiceSkipsOrganizationDeletedAfterListing(t *testing.T) { // Real Postgres E2E controls deletion between listing and pruning.
	if testing.Short() {
		t.Skip("skipping real-Postgres retention service E2E test in short mode")
	}
	pool := openWorkerE2EPool(t)
	deletedOrg := slug.Slug(fmt.Sprintf("worker-e2e-retention-deleted-%d", time.Now().UnixNano()))
	remainingOrg := slug.Slug(fmt.Sprintf("worker-e2e-retention-remaining-%d", time.Now().UnixNano()))
	seedWorkerE2EOrg(t, pool, deletedOrg)
	seedWorkerE2EOrg(t, pool, remainingOrg)
	repo := &deleteListedRetentionOrganizationRepository{
		Repository: retentionrepo.New(), pool: pool, deletedOrg: deletedOrg, remainingOrg: remainingOrg,
	}
	svc := newWorkerE2EScopedRetentionService(pool, repo, clock.New(), deletedOrg, remainingOrg)
	result, err := svc.PruneExpiredCustomerData(t.Context(), retentionsvc.MaintenanceExecution{
		SourceJobID: time.Now().UnixNano(), Attempt: 1, StartedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, []slug.Slug{deletedOrg}, result.SkippedOrgs)
	runs, err := svc.ListMaintenanceRuns(t.Context(), deletedOrg)
	require.NoError(t, err)
	require.Empty(t, runs)
	runs, err = svc.ListMaintenanceRuns(t.Context(), remainingOrg)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, model.OrganizationRetentionRunStatusSucceeded, runs[0].Status)
}

func TestOrganizationRetentionRunSkipsConcurrentOrganizationDeletion(t *testing.T) { // Real Postgres E2E controls the concurrent deletion lock.
	if testing.Short() {
		t.Skip("skipping real-Postgres retention concurrency test in short mode")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool := openWorkerE2EPool(t)
	org := slug.Slug(fmt.Sprintf("worker-e2e-retention-concurrent-delete-%d", time.Now().UnixNano()))
	seedWorkerE2EOrg(t, pool, org)

	deletion, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = deletion.Rollback(context.Background()) }()
	_, err = deletion.Exec(ctx, "DELETE FROM organizations WHERE slug = $1", org.String())
	require.NoError(t, err)
	writer, err := pool.Acquire(ctx)
	require.NoError(t, err)
	writerPID := writer.Conn().PgConn().PID()
	result := make(chan error, 1)
	go func() {
		defer writer.Release()
		now := time.Now().UTC()
		result <- retentionrepo.New().CreateRun(ctx, writer, org, now.UnixNano(), model.OrganizationRetentionRun{
			Slug: org, Attempt: 1, Status: model.OrganizationRetentionRunStatusSucceeded,
			StartedAt: now, CompletedAt: now,
		})
	}()

	// Commit only once the insert is waiting on this deletion, not before its snapshot.
	require.Eventually(t, func() bool {
		var blocked bool
		err := deletion.QueryRow(ctx, "SELECT pg_backend_pid() = ANY(pg_blocking_pids($1))", writerPID).Scan(&blocked)
		return err == nil && blocked
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, deletion.Commit(ctx))
	select {
	case err := <-result:
		require.ErrorIs(t, err, retentionrepo.ErrOrganizationNotFound)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

type deleteListedRetentionOrganizationRepository struct {
	retentionrepo.Repository
	pool         *pgxpool.Pool
	deletedOrg   slug.Slug
	remainingOrg slug.Slug
}

func (r *deleteListedRetentionOrganizationRepository) ListOrganizationSettings(ctx context.Context, tx postgres.DBTX) ([]retentionrepo.OrganizationSettings, error) {
	organizations, err := r.Repository.ListOrganizationSettings(ctx, tx)
	if err != nil {
		return nil, err
	}
	// Keep this regression's pruning scoped to its own fixtures.
	var selected []retentionrepo.OrganizationSettings
	for _, org := range organizations {
		if org.Slug == r.deletedOrg || org.Slug == r.remainingOrg {
			selected = append(selected, org)
		}
	}
	_, err = r.pool.Exec(ctx, "DELETE FROM organizations WHERE slug = $1", r.deletedOrg.String())
	return selected, err
}

func TestOrganizationRetentionServiceRollsBackDeletesBeforeFailureAudit(t *testing.T) { // Real Postgres E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres retention service E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	orgSlug := slug.Slug(fmt.Sprintf("worker-e2e-retention-rollback-%d", time.Now().UnixNano()))
	seedWorkerE2EOrg(t, pool, orgSlug)
	_, err := pool.Exec(ctx, `
		UPDATE organizations
		SET settings = jsonb_build_object(
			'retention', jsonb_build_object(
				'query_execution_retention_days', 30,
				'replay_checkpoint_retention_days', 30,
				'run_history_retention_days', 30,
				'event_log_retention_days', 1
			)
		)
		WHERE slug = $1
	`, orgSlug.String())
	require.NoError(t, err)

	_, eventsSvc := newWorkerE2EEventsHarness(t, pool, orgSlug)
	eventSlug := slug.Slug(fmt.Sprintf("evt-retention-rollback-%d", time.Now().UnixNano()))
	entitySlug := slug.Slug(fmt.Sprintf("entity-retention-rollback-%d", time.Now().UnixNano()))
	_, err = eventsSvc.CreateEvent(ctx, eventssvc.CreateEventParams{
		OrgSlug:    orgSlug,
		Slug:       eventSlug,
		ActorType:  model.EventActorTypeSystem,
		EntityType: model.EventEntityTypeQueryExecution,
		EntitySlug: &entitySlug,
		Action:     model.EventActionExecute,
		OccurredAt: now.AddDate(0, 0, -2),
	})
	require.NoError(t, err)

	dependencyErr := xerror.Unexpected().Detail("private retention dependency failure")
	repo := &failSuccessfulRetentionRunRepository{
		Repository: retentionrepo.New(),
		err:        dependencyErr,
	}
	retentionSvc := newWorkerE2EScopedRetentionService(
		pool,
		repo,
		testutil.FakeClock{NowFn: func() time.Time { return now }},
		orgSlug,
	)
	result, err := retentionSvc.PruneExpiredCustomerData(ctx, retentionsvc.MaintenanceExecution{
		SourceJobID: time.Now().UnixNano(),
		Attempt:     1,
		StartedAt:   now.Add(-time.Minute),
	})

	require.Error(t, err)
	require.True(t, xerror.Is(err, dependencyErr))
	require.Zero(t, result.Events)
	retainedEvent, err := eventsSvc.GetEvent(ctx, orgSlug, eventSlug)
	require.NoError(t, err)
	require.Equal(t, eventSlug, retainedEvent.Slug)

	runs, err := retentionSvc.ListMaintenanceRuns(ctx, orgSlug)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, model.OrganizationRetentionRunStatusFailed, runs[0].Status)
	require.Equal(t, model.OrganizationRetentionRunFailureCodeMaintenanceFailed, runs[0].FailureCode)
	require.NotNil(t, runs[0].PolicySnapshot)
	require.Zero(t, runs[0].QueryExecutionsDeleted)
	require.Zero(t, runs[0].ReplayCheckpointsDeleted)
	require.Zero(t, runs[0].PipelineRunsDeleted)
	require.Zero(t, runs[0].EventsDeleted)
}

type failSuccessfulRetentionRunRepository struct {
	retentionrepo.Repository
	err error
}

func (r *failSuccessfulRetentionRunRepository) CreateRun(
	ctx context.Context,
	tx postgres.DBTX,
	org slug.Slug,
	sourceJobID int64,
	run model.OrganizationRetentionRun,
) error {
	if run.Status == model.OrganizationRetentionRunStatusSucceeded {
		return r.err
	}
	return r.Repository.CreateRun(ctx, tx, org, sourceJobID, run)
}

func TestWorkerHostEventRecorderRollbackPreventsJobVisibility(t *testing.T) { // Real River/Postgres worker E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	ctx := context.Background()
	pool := openWorkerE2EPool(t)
	orgSlug := slug.Slug(fmt.Sprintf("worker-e2e-events-rollback-%d", time.Now().UnixNano()))
	entitySlug := slug.Slug(fmt.Sprintf("daily-users-exec-%d", time.Now().UnixNano()))
	argsFilter := map[string]any{"entity_slug": entitySlug.String()}
	cleanupRiverJobsByArgs(t, pool, jobs.EventsQueue, jobs.EventRecordArgs{}.Kind(), argsFilter)
	seedWorkerE2EOrg(t, pool, orgSlug)

	eventProducer, eventsSvc := newWorkerE2EEventProducer(t, pool)
	_ = enqueueEventRecordInTx(
		t,
		pool,
		eventProducer,
		orgSlug,
		model.SystemEventActor(),
		workerE2ETestEvent{record: eventrecorder.Record{
			SlugPrefix: slug.Slug("evt-query-execute"),
			EntityType: model.EventEntityTypeQueryExecution,
			EntitySlug: entitySlug,
			Action:     model.EventActionExecute,
		}},
		true,
	)

	count, err := countRiverJobsByArgs(
		ctx,
		pool,
		jobs.EventsQueue,
		jobs.EventRecordArgs{}.Kind(),
		argsFilter,
	)
	require.NoError(t, err)
	require.Equal(t, 0, count)

	entityType := model.EventEntityTypeQueryExecution
	action := model.EventActionExecute
	events, err := eventsSvc.ListEvents(ctx, orgSlug, eventssvc.ListEventsFilters{
		EntityType: &entityType,
		EntitySlug: &entitySlug,
		Action:     &action,
	}, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, events.Items)
}

func TestRequiredSecurityAuditValidationRollsBackSourceMutation(t *testing.T) { // Real Postgres E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River worker E2E test in short mode")
	}

	pool := openWorkerE2EPool(t)
	orgSlug := slug.Slug(fmt.Sprintf("worker-e2e-audit-rollback-%d", time.Now().UnixNano()))
	entitySlug := slug.Slug(fmt.Sprintf("audit-target-%d", time.Now().UnixNano()))
	seedWorkerE2EOrg(t, pool, orgSlug)
	eventProducer, _ := newWorkerE2EEventProducer(t, pool)

	err := postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		if _, updateErr := tx.Exec(
			ctx,
			"UPDATE organizations SET name = $1 WHERE slug = $2",
			"must-roll-back",
			orgSlug.String(),
		); updateErr != nil {
			return updateErr
		}

		_, enqueueErr := eventProducer.EnqueueRequired(
			ctx,
			tx,
			orgSlug,
			model.SystemEventActor(),
			workerE2ETestEvent{record: eventrecorder.Record{
				SlugPrefix:    "evt-security-audit-rollback",
				EventClass:    model.EventClassSecurityAudit,
				EntityType:    model.EventEntityTypeOrganization,
				EntitySlug:    entitySlug,
				Action:        model.EventActionUpdate,
				SchemaVersion: model.CurrentEventSchemaVersion,
				Metadata:      map[string]any{"token": "must-not-persist"},
			}},
		)
		return enqueueErr
	})
	require.ErrorContains(t, err, "security audit metadata contains unsafe key: token")

	var name string
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT name FROM organizations WHERE slug = $1", orgSlug.String()).Scan(&name),
	)
	require.NotEqual(t, "must-roll-back", name)

	count, countErr := countRiverJobsByArgs(
		t.Context(),
		pool,
		jobs.EventsQueue,
		jobs.EventRecordArgs{}.Kind(),
		map[string]any{"entity_slug": entitySlug.String()},
	)
	require.NoError(t, countErr)
	require.Zero(t, count)
}

func newWorkerE2EEventProducer(
	t *testing.T,
	pool *pgxpool.Pool,
) (workerproducer.EventProducer, eventssvc.Service) {
	t.Helper()

	client, eventsSvc := newWorkerE2EEventsHarness(t, pool)
	eventProducer, err := workerproducer.NewEventProducer(queue.NewEnqueuer(client), clock.New())
	require.NoError(t, err)

	return eventProducer, eventsSvc
}

func newWorkerE2EEventsHarness(
	t *testing.T,
	pool *pgxpool.Pool,
	organizations ...slug.Slug,
) (*queue.Client, eventssvc.Service) {
	t.Helper()

	eventsSvc := eventssvc.New(postgres.NewTransactionManager(pool), repoevents.New(), clock.New())
	retentionSvc := newWorkerE2EScopedRetentionService(pool, retentionrepo.New(), clock.New(), organizations...)
	workerSvc, err := workerhostsvc.New(
		noopPipelineGitExecutor{},
		noopPipelineGitProposalRecorder{},
		noopPipelineGitProposalAuthorizer{},
		&testutil.FakeEmailService{},
		eventsSvc,
		retentionSvc,
		noopScheduleDispatcher{},
		noopPipelineRunExecutor{},
		noopSchemaImportExecutor{},
		noopNotificationDeliveryService{},
		workerE2ETokenCipher(t),
		slog.New(slog.DiscardHandler),
		workerE2EQueueConcurrency(),
		noopPipelineWebhookExecutor{},
	)

	require.NoError(t, err)

	client, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Logger: slog.New(slog.DiscardHandler),
		Queues: workerSvc.Queues(),
		Registrars: []queue.WorkerRegistrar{
			workerSvc,
		},
		RetryPolicy: fixedRetryPolicy{delay: time.Hour},
	})
	require.NoError(t, err)
	require.NoError(t, client.Start(context.Background()))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Stop(stopCtx))
	})

	return client, eventsSvc
}

type workerE2ERetentionRepository struct {
	retentionrepo.Repository
	organizations []slug.Slug
}

// This event harness owns no global abuse-control or wishlist fixtures.
func (workerE2ERetentionRepository) DeleteAbuseControlBucketsBefore(context.Context, postgres.DBTX, time.Time) (int64, error) {
	return 0, nil
}

func (workerE2ERetentionRepository) DeleteExpired(context.Context, postgres.DBTX, time.Time) (int64, error) {
	return 0, nil
}

func (r workerE2ERetentionRepository) ListOrganizationSettings(ctx context.Context, tx postgres.DBTX) ([]retentionrepo.OrganizationSettings, error) {
	organizations, err := r.Repository.ListOrganizationSettings(ctx, tx)
	if err != nil {
		return nil, err
	}
	// Maintenance jobs must only prune this harness's fixtures in a retained database.
	var fixtures []retentionrepo.OrganizationSettings
	for _, organization := range organizations {
		if slices.Contains(r.organizations, organization.Slug) {
			fixtures = append(fixtures, organization)
		}
	}
	return fixtures, nil
}

func newWorkerE2EScopedRetentionService(pool *pgxpool.Pool, repo retentionrepo.Repository, clk clock.Clock, organizations ...slug.Slug) retentionsvc.Service {
	fixtureRepo := workerE2ERetentionRepository{Repository: repo, organizations: organizations}
	return retentionsvc.New(postgres.NewTransactionManager(pool), fixtureRepo, fixtureRepo, clk)
}

func newWorkerE2ERetentionService(pool *pgxpool.Pool) retentionsvc.Service {
	return retentionsvc.New(
		postgres.NewTransactionManager(pool),
		retentionrepo.New(),
		wishlistregistrations.NewRepository(),
		clock.New(),
	)
}

func enqueueEventRecordInTx(
	t *testing.T,
	pool *pgxpool.Pool,
	producer workerproducer.EventProducer,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
	rollback bool,
) queue.QueuedJob {
	t.Helper()

	var queued queue.QueuedJob
	err := postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		eventJob, ok := producer.EnqueueRecord(ctx, tx, orgSlug, actor, event)
		require.True(t, ok)
		queued = eventJob.Queued
		if rollback {
			return xerror.Unexpected().Detail("rollback requested")
		}
		return nil
	})
	if rollback {
		require.ErrorContains(t, err, "rollback requested")
		return queued
	}
	require.NoError(t, err)

	return queued
}

func enqueueEventPruneInTx(
	t *testing.T,
	pool *pgxpool.Pool,
	enqueuer queue.Enqueuer,
) queue.QueuedJob {
	t.Helper()

	var queued queue.QueuedJob
	err := postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		var enqueueErr error
		queued, enqueueErr = enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
			Args:  jobs.EventPruneArgs{},
			Queue: jobs.EventMaintenanceQueue,
		})
		return enqueueErr
	})
	require.NoError(t, err)

	return queued
}

func maxRiverJobIDForKind(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	queueName string,
	kind string,
) int64 {
	t.Helper()

	var maxID int64
	err := pool.QueryRow(
		ctx,
		`SELECT COALESCE(MAX(id), 0) FROM river_job WHERE queue = $1 AND kind = $2`,
		queueName,
		kind,
	).Scan(&maxID)
	require.NoError(t, err)

	return maxID
}

func waitForRiverJobStateAfterID(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	afterID int64,
	queueName string,
	kind string,
	wantState string,
) riverJobSnapshot {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var last riverJobSnapshot
	for {
		job, found, err := findRiverJobAfterID(waitCtx, pool, afterID, queueName, kind)
		if waitCtx.Err() != nil {
			failRiverJobStateTimeout(t, queueName, kind, wantState, last)
		}
		require.NoError(t, err)
		if found {
			last = job
			if job.state == wantState {
				return job
			}
		}

		select {
		case <-waitCtx.Done():
			failRiverJobStateTimeout(t, queueName, kind, wantState, last)
		case <-ticker.C:
		}
	}
}

func findRiverJobAfterID(
	ctx context.Context,
	pool *pgxpool.Pool,
	afterID int64,
	queueName string,
	kind string,
) (riverJobSnapshot, bool, error) {
	var job riverJobSnapshot
	err := pool.QueryRow(
		ctx,
		`SELECT state::text, attempt::int, COALESCE(array_to_json(errors)::text, '[]')
		 FROM river_job
		 WHERE id > $1 AND queue = $2 AND kind = $3
		 ORDER BY id DESC
		 LIMIT 1`,
		afterID,
		queueName,
		kind,
	).Scan(&job.state, &job.attempt, &job.errorsJSON)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return riverJobSnapshot{}, false, nil
		}
		return riverJobSnapshot{}, false, err
	}

	return job, true, nil
}

func seedWorkerE2EOrg(t *testing.T, pool *pgxpool.Pool, orgSlug slug.Slug) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(
		ctx,
		`INSERT INTO organizations (name, slug) VALUES ($1, $2)`,
		"Worker E2E Org",
		orgSlug.String(),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()

		_, cleanupErr := pool.Exec(
			cleanupCtx,
			`DELETE FROM organizations WHERE slug = $1`,
			orgSlug.String(),
		)
		require.NoError(t, cleanupErr)
	})
}

type workerE2ETestEvent struct {
	record eventrecorder.Record
}

func (e workerE2ETestEvent) EventRecord() (eventrecorder.Record, error) {
	return e.record, nil
}
