package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"

	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"

	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/postgres/roles"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelineschedulesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinetriggersvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

type scheduleWebhookSecrets struct{}

func (scheduleWebhookSecrets) IssuePipelineWebhookSecret(context.Context, credentialssvc.PipelineWebhookSecretParams) (credentialssvc.IssuedPipelineWebhookSecret, error) {
	return credentialssvc.IssuedPipelineWebhookSecret{}, nil
}

func (scheduleWebhookSecrets) RevokePipelineWebhookSecrets(context.Context, credentialssvc.PipelineWebhookSecretParams) error {
	return nil
}

func TestService_DispatchDueScheduleEnqueuesFireThenExecutionCreatesRunEventAndCompletesFire(t *testing.T) { // Schedule scanner intentionally crosses tenants and would see parallel schedule E2E fixtures.
	if testing.Short() {
		t.Skip("skipping real-Postgres scheduled pipeline dispatch E2E test in short mode")
	}

	ctx := context.Background()
	pool := openScheduleE2EPool(t)
	txm := postgres.NewTransactionManager(pool)
	eventsSvc := eventssvc.New(txm, events.New(), clock.New())
	eventProducer := eventtest.NewProducer(eventsSvc)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	pipelineSvc := newScheduleE2EPipelineService(t, pool, txm, store, eventProducer)
	triggerSvc := pipelinetriggersvc.New(false, txm, pipelinetriggers.NewRepository(), scheduleCompletionValidator{}, eventProducer, scheduleWebhookSecrets{})
	enqueueClient, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	unique := time.Now().UnixNano()
	scheduleQueue := fmt.Sprintf("%s_service_e2e_%d", jobs.PipelineScheduleQueue, unique)
	scheduleProducer, err := workerproducer.NewPipelineScheduleProducerForQueue(
		queue.NewEnqueuer(enqueueClient),
		scheduleQueue,
	)
	require.NoError(t, err)
	candidateProducer, err := workerproducer.NewNotificationCandidateProducer(queue.NewEnqueuer(enqueueClient))
	require.NoError(t, err)
	scheduleSvc := pipelineschedulesvc.New(txm, pipelinetriggers.NewRepository(), pipelineSvc, scheduleProducer, nil, pipelineschedulesvc.WithNotificationProducer(candidateProducer))
	stack := newScheduleE2EStack(pool, txm, eventProducer)

	orgSlug := must.Slug(fmt.Sprintf("schedule-dispatch-org-%d", unique))
	userSlug := must.Slug(fmt.Sprintf("schedule-dispatch-user-%d", unique))
	projectSlug := must.Slug("default")
	environmentSlug := must.Slug("scheduled")
	pipelineSlug := must.Slug("scheduled-users")
	triggerSlug := must.Slug("daily-users")
	now := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	seedScheduleE2EProjectEnvironment(t, pool, stack, userSlug, orgSlug, projectSlug, environmentSlug)

	pipeline := scheduleE2EPipeline(pipelineSlug)
	materializePipelines(t, ctx, store, orgSlug, projectSlug, pipeline)
	spec, err := store.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
	require.NoError(t, err)
	require.NotNil(t, spec.CurrentVersionSpecHash)
	_, err = pipelineSvc.Deploy(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, model.SystemEventActor(), pipelinesvc.DeployPipelineRequest{
		SpecHash: *spec.CurrentVersionSpecHash,
	})
	require.NoError(t, err)

	_, err = triggerSvc.CreatePipelineTrigger(ctx, orgSlug, projectSlug, environmentSlug, model.NewPipelineTrigger{
		Name:             "Daily users",
		Slug:             triggerSlug,
		Type:             model.PipelineTriggerTypeSchedule,
		Enabled:          true,
		PipelineSlug:     pipelineSlug,
		RuntimeInputKeys: []string{"customer_id", "dry_run"},
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:         false,
			MaxAttempts:     1,
			BackoffStrategy: model.PipelineTriggerBackoffStrategyNone,
		},
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 7 * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}, model.SystemEventActor())
	require.NoError(t, err)

	queueErr := xerror.Unexpected().Detail("synthetic candidate enqueue failure")
	failingSvc := pipelineschedulesvc.New(txm, pipelinetriggers.NewRepository(), pipelineSvc, scheduleProducer, nil,
		pipelineschedulesvc.WithNotificationProducer(testutil.FakeRunSucceededNotifier{CandidateFn: func(ctx context.Context, tx postgres.DBTX, candidate model.NotificationCandidate) error {
			require.NoError(t, candidateProducer.EnqueueCandidate(ctx, tx, candidate))
			return queueErr
		}}))
	_, err = failingSvc.DispatchDueSchedules(ctx, now.Add(2*time.Minute))
	require.ErrorIs(t, err, queueErr)
	var fireCount, candidateCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_schedule_fires f JOIN project_environments pe ON pe.id=f.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1`, orgSlug).Scan(&fireCount))
	require.Zero(t, fireCount, "candidate enqueue failure rolls back the skipped fire claim")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='notification_candidate' AND args->>'organization_slug'=$1`, orgSlug.String()).Scan(&candidateCount))
	require.Zero(t, candidateCount, "candidate jobs roll back with source state")
	result, err := scheduleSvc.DispatchDueSchedules(ctx, now)
	require.NoError(t, err)
	require.Equal(t, pipelineschedulesvc.DispatchResult{Scanned: 1, Claimed: 1, Enqueued: 1}, result)
	for _, elapsed := range []time.Duration{2 * time.Minute, 3 * time.Minute} {
		_, err = scheduleSvc.DispatchDueSchedules(ctx, now.Add(elapsed))
		require.NoError(t, err)
	}
	var delayedJobs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'notification_candidate' AND args->>'organization_slug' = $1`, orgSlug.String()).Scan(&delayedJobs))
	require.Equal(t, 1, delayedJobs, "repeated scans coalesce the same delayed fire into one durable candidate job")
	delayed, err := pipelinetriggers.NewRepository().ListDelayedScheduleFiresForSystem(ctx, pool, now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, delayed, 1)
	require.Equal(t, orgSlug, delayed[0].OrgSlug)
	require.Equal(t, pipelineSlug, delayed[0].PipelineSlug)
	executionArgs := jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: orgSlug,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		TriggerSlug:      triggerSlug,
		TriggerRevision:  1,
		ScheduledFireAt:  now,
	}
	requireScheduleFireExecutionJobInQueue(t, pool, scheduleQueue, executionArgs)

	runs, err := pipelineSvc.ListRuns(ctx, orgSlug, projectSlug, environmentSlug, nil, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	require.Equal(t, "queued", runs.Items[0].Status)
	require.Empty(t, runs.Items[0].PipelineVersionSpecHash)

	err = scheduleSvc.ExecuteClaimedScheduleFire(ctx, executionArgs)
	require.NoError(t, err)

	runs, err = pipelineSvc.ListRuns(ctx, orgSlug, projectSlug, environmentSlug, nil, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	run := runs.Items[0]
	expectedRunSlug := pipelineschedulesvc.ScheduleFireClaim{
		TriggerSlug:     triggerSlug,
		ScheduledFireAt: now,
	}.FireSlug().String()
	require.Equal(t, expectedRunSlug, run.RunSlug)
	require.Equal(t, pipelineSlug.String(), run.PipelineSlug)
	require.Equal(t, model.PipelineRunSourceTypeSchedule, run.Source.Type)
	require.Equal(t, model.PipelineRunSourceEntrypointScheduler, run.Source.Entrypoint)
	require.Equal(t, model.EventActorTypeSystem, run.Source.ActorType)
	require.Nil(t, run.Source.ActorSlug)
	require.Equal(t, []string{"customer_id", "dry_run"}, run.Source.ParameterKeys)
	require.NotNil(t, run.Source.Schedule)
	require.Equal(t, triggerSlug, run.Source.Schedule.TriggerSlug)
	require.Equal(t, int64(1), run.Source.Schedule.TriggerRevision)
	require.Equal(t, "0 7 * * *", run.Source.Schedule.Cron)
	require.Equal(t, "UTC", run.Source.Schedule.Timezone)
	require.True(t, run.Source.Schedule.ScheduledFireAt.Equal(now))
	require.True(t, run.Source.Schedule.ActualFireAt.Equal(now))
	require.Equal(t, model.PipelineTriggerScheduleMisfireResultOnTime, run.Source.Schedule.MisfireResult)
	require.NotNil(t, run.Source.SourceEventSlug)

	event, err := eventsSvc.GetEvent(ctx, orgSlug, *run.Source.SourceEventSlug)
	require.NoError(t, err)
	require.Equal(t, model.EventActorTypeSystem, event.ActorType)
	require.Equal(t, model.EventEntityTypePipelineRun, event.EntityType)
	require.Equal(t, model.EventActionExecute, event.Action)
	require.Equal(t, "schedule", event.Metadata["pipeline_run_source_type"])
	require.Equal(t, "scheduler", event.Metadata["pipeline_run_source_entrypoint"])
	require.Equal(t, []any{"customer_id", "dry_run"}, event.Metadata["pipeline_run_source_parameter_keys"])
	require.Equal(t, triggerSlug.String(), event.Metadata["schedule_trigger_slug"])
	revision, ok := event.Metadata["schedule_trigger_revision"].(float64)
	require.True(t, ok)
	require.InEpsilon(t, 1, revision, 0)
	require.Equal(t, "0 7 * * *", event.Metadata["schedule_cron"])
	require.Equal(t, "UTC", event.Metadata["schedule_timezone"])
	require.Equal(t, "on_time", event.Metadata["schedule_misfire_result"])
	require.NotContains(t, fmt.Sprint(event.Metadata), "cus_123")

	completion := requireScheduleFireCompletion(t, pool, orgSlug, projectSlug, environmentSlug, triggerSlug, 1, now)
	require.Equal(t, "succeeded", completion.status)
	require.NotNil(t, completion.runSlug)
	require.Equal(t, run.RunSlug, *completion.runSlug)
	require.NotNil(t, completion.sourceEventSlug)
	require.Equal(t, run.Source.SourceEventSlug.String(), *completion.sourceEventSlug)
	require.Nil(t, completion.errorSummary)
	delayed, err = pipelinetriggers.NewRepository().ListDelayedScheduleFiresForSystem(ctx, pool, now.Add(time.Minute))
	require.NoError(t, err)
	require.Empty(t, delayed, "completed fires leave the overdue scan")
}

func TestService_DispatchDueScheduleUsesUpdatedEnabledTriggerRevision(t *testing.T) { // Schedule scanner intentionally crosses tenants and would see parallel schedule E2E fixtures.
	if testing.Short() {
		t.Skip("skipping real-Postgres scheduled pipeline dispatch E2E test in short mode")
	}

	ctx := context.Background()
	pool := openScheduleE2EPool(t)
	txm := postgres.NewTransactionManager(pool)
	eventsSvc := eventssvc.New(txm, events.New(), clock.New())
	eventProducer := eventtest.NewProducer(eventsSvc)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	pipelineSvc := newScheduleE2EPipelineService(t, pool, txm, store, eventProducer)
	triggerSvc := pipelinetriggersvc.New(false, txm, pipelinetriggers.NewRepository(), scheduleCompletionValidator{}, eventProducer, scheduleWebhookSecrets{})
	enqueueClient, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	unique := time.Now().UnixNano()
	scheduleQueue := fmt.Sprintf("%s_service_e2e_%d", jobs.PipelineScheduleQueue, unique)
	scheduleProducer, err := workerproducer.NewPipelineScheduleProducerForQueue(
		queue.NewEnqueuer(enqueueClient),
		scheduleQueue,
	)
	require.NoError(t, err)
	scheduleSvc := pipelineschedulesvc.New(txm, pipelinetriggers.NewRepository(), pipelineSvc, scheduleProducer, nil)
	stack := newScheduleE2EStack(pool, txm, eventProducer)

	orgSlug := must.Slug(fmt.Sprintf("schedule-update-org-%d", unique))
	userSlug := must.Slug(fmt.Sprintf("schedule-update-user-%d", unique))
	projectSlug := must.Slug("default")
	environmentSlug := must.Slug("scheduled")
	pipelineSlug := must.Slug("scheduled-users")
	triggerSlug := must.Slug("daily-users")
	now := time.Date(2026, time.June, 12, 9, 0, 0, 0, time.UTC)
	seedScheduleE2EProjectEnvironment(t, pool, stack, userSlug, orgSlug, projectSlug, environmentSlug)

	pipeline := scheduleE2EPipeline(pipelineSlug)
	materializePipelines(t, ctx, store, orgSlug, projectSlug, pipeline)
	spec, err := store.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
	require.NoError(t, err)
	require.NotNil(t, spec.CurrentVersionSpecHash)
	_, err = pipelineSvc.Deploy(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, model.SystemEventActor(), pipelinesvc.DeployPipelineRequest{
		SpecHash: *spec.CurrentVersionSpecHash,
	})
	require.NoError(t, err)

	_, err = triggerSvc.CreatePipelineTrigger(ctx, orgSlug, projectSlug, environmentSlug, model.NewPipelineTrigger{
		Name:             "Disabled daily users",
		Slug:             triggerSlug,
		Type:             model.PipelineTriggerTypeSchedule,
		Enabled:          false,
		PipelineSlug:     pipelineSlug,
		RuntimeInputKeys: []string{"stale_customer_id"},
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:         false,
			MaxAttempts:     1,
			BackoffStrategy: model.PipelineTriggerBackoffStrategyNone,
		},
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 6 * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}, model.SystemEventActor())
	require.NoError(t, err)

	updated, err := triggerSvc.UpdatePipelineTrigger(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, model.UpdatePipelineTrigger{
		Name:             "Enabled daily users",
		Type:             model.PipelineTriggerTypeSchedule,
		Enabled:          true,
		PipelineSlug:     pipelineSlug,
		RuntimeInputKeys: []string{"dry_run", "customer_id", "dry_run"},
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:         false,
			MaxAttempts:     1,
			BackoffStrategy: model.PipelineTriggerBackoffStrategyNone,
		},
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 9 * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}, model.SystemEventActor())
	require.NoError(t, err)
	require.Equal(t, int64(2), updated.Revision)
	require.True(t, updated.Enabled)
	require.Equal(t, []string{"customer_id", "dry_run"}, updated.RuntimeInputKeys)

	result, err := scheduleSvc.DispatchDueSchedules(ctx, now)
	require.NoError(t, err)
	require.Equal(t, pipelineschedulesvc.DispatchResult{Scanned: 1, Claimed: 1, Enqueued: 1}, result)
	executionArgs := jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: orgSlug,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		TriggerSlug:      triggerSlug,
		TriggerRevision:  2,
		ScheduledFireAt:  now,
	}
	requireScheduleFireExecutionJobInQueue(t, pool, scheduleQueue, executionArgs)

	err = scheduleSvc.ExecuteClaimedScheduleFire(ctx, executionArgs)
	require.NoError(t, err)

	runs, err := pipelineSvc.ListRuns(ctx, orgSlug, projectSlug, environmentSlug, nil, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	run := runs.Items[0]
	expectedRunSlug := pipelineschedulesvc.ScheduleFireClaim{
		TriggerSlug:     triggerSlug,
		ScheduledFireAt: now,
	}.FireSlug().String()
	require.Equal(t, expectedRunSlug, run.RunSlug)
	require.Equal(t, pipelineSlug.String(), run.PipelineSlug)
	require.Equal(t, model.PipelineRunSourceTypeSchedule, run.Source.Type)
	require.Equal(t, []string{"customer_id", "dry_run"}, run.Source.ParameterKeys)
	require.NotNil(t, run.Source.Schedule)
	require.Equal(t, triggerSlug, run.Source.Schedule.TriggerSlug)
	require.Equal(t, int64(2), run.Source.Schedule.TriggerRevision)
	require.Equal(t, "0 9 * * *", run.Source.Schedule.Cron)
	require.Equal(t, "UTC", run.Source.Schedule.Timezone)
	require.True(t, run.Source.Schedule.ScheduledFireAt.Equal(now))
	require.True(t, run.Source.Schedule.ActualFireAt.Equal(now))

	completion := requireScheduleFireCompletion(t, pool, orgSlug, projectSlug, environmentSlug, triggerSlug, 2, now)
	require.Equal(t, "succeeded", completion.status)
	require.NotNil(t, completion.runSlug)
	require.Equal(t, run.RunSlug, *completion.runSlug)
}

func TestService_TriggerRetryAttemptRetriesFailedRunTwice(t *testing.T) { // Schedule scanner intentionally crosses tenants and would see parallel schedule E2E fixtures.
	if testing.Short() {
		t.Skip("skipping real-Postgres scheduled pipeline retry E2E test in short mode")
	}

	ctx := context.Background()
	pool := openScheduleE2EPool(t)
	txm := postgres.NewTransactionManager(pool)
	eventsSvc := eventssvc.New(txm, events.New(), clock.New())
	eventProducer := eventtest.NewProducer(eventsSvc)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	queryExecutor := &scheduleE2EQueryExecutor{
		executeResults: []scheduleE2EExecuteResult{
			{err: xerror.Unexpected().Detail("warehouse unavailable")},
			{err: xerror.Unexpected().Detail("warehouse unavailable again")},
			{},
		},
	}
	pipelineSvc := newScheduleE2EPipelineServiceWithQueryExecutor(t, pool, txm, store, eventProducer, queryExecutor)
	triggerSvc := pipelinetriggersvc.New(false, txm, pipelinetriggers.NewRepository(), scheduleCompletionValidator{}, eventProducer, scheduleWebhookSecrets{})
	enqueueClient, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	enqueuer := queue.NewEnqueuer(enqueueClient)
	unique := time.Now().UnixNano()
	scheduleQueue := fmt.Sprintf("%s_service_e2e_%d", jobs.PipelineScheduleQueue, unique)
	retryQueue := fmt.Sprintf("%s_service_e2e_%d", jobs.PipelineRetryQueue, unique)
	scheduleProducer, err := workerproducer.NewPipelineScheduleProducerForQueue(enqueuer, scheduleQueue)
	require.NoError(t, err)
	retryProducer, err := workerproducer.NewPipelineRetryProducerForQueue(enqueuer, retryQueue)
	require.NoError(t, err)
	scheduleSvc := pipelineschedulesvc.New(
		txm,
		pipelinetriggers.NewRepository(),
		pipelineSvc,
		scheduleProducer,
		retryProducer,
	)
	stack := newScheduleE2EStack(pool, txm, eventProducer)

	orgSlug := must.Slug(fmt.Sprintf("schedule-retry-org-%d", unique))
	userSlug := must.Slug(fmt.Sprintf("schedule-retry-user-%d", unique))
	projectSlug := must.Slug("default")
	environmentSlug := must.Slug("scheduled")
	pipelineSlug := must.Slug("scheduled-users")
	triggerSlug := must.Slug("daily-users")
	now := time.Date(2026, time.June, 11, 7, 0, 0, 0, time.UTC)
	seedScheduleE2EProjectEnvironment(t, pool, stack, userSlug, orgSlug, projectSlug, environmentSlug)

	pipeline := scheduleE2EPipeline(pipelineSlug)
	materializePipelines(t, ctx, store, orgSlug, projectSlug, pipeline)
	spec, err := store.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
	require.NoError(t, err)
	require.NotNil(t, spec.CurrentVersionSpecHash)
	_, err = pipelineSvc.Deploy(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, model.SystemEventActor(), pipelinesvc.DeployPipelineRequest{
		SpecHash: *spec.CurrentVersionSpecHash,
	})
	require.NoError(t, err)

	_, err = triggerSvc.CreatePipelineTrigger(ctx, orgSlug, projectSlug, environmentSlug, model.NewPipelineTrigger{
		Name:             "Daily users",
		Slug:             triggerSlug,
		Type:             model.PipelineTriggerTypeSchedule,
		Enabled:          true,
		PipelineSlug:     pipelineSlug,
		RuntimeInputKeys: []string{"customer_id", "dry_run"},
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:               true,
			MaxAttempts:           3,
			BackoffStrategy:       model.PipelineTriggerBackoffStrategyFixed,
			InitialDelaySeconds:   60,
			MaxDelaySeconds:       60,
			RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindDataSource},
		},
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 7 * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}, model.SystemEventActor())
	require.NoError(t, err)

	dispatch, err := scheduleSvc.DispatchDueSchedules(ctx, now)
	require.NoError(t, err)
	require.Equal(t, pipelineschedulesvc.DispatchResult{Scanned: 1, Claimed: 1, Enqueued: 1}, dispatch)
	fireArgs := jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: orgSlug,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		TriggerSlug:      triggerSlug,
		TriggerRevision:  1,
		ScheduledFireAt:  now,
	}
	requireScheduleFireExecutionJobInQueue(t, pool, scheduleQueue, fireArgs)

	err = scheduleSvc.ExecuteClaimedScheduleFire(ctx, fireArgs)
	require.NoError(t, err)

	runs, err := pipelineSvc.ListRuns(ctx, orgSlug, projectSlug, environmentSlug, nil, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, runs.Items, 2)
	failedRun := runs.Items[0]
	if failedRun.Status == "queued" {
		failedRun = runs.Items[1]
	}
	require.Equal(t, "failed", failedRun.Status)
	require.NotNil(t, failedRun.FailureKind)
	require.Equal(t, model.PipelineTriggerRetryFailureKindDataSource.String(), *failedRun.FailureKind)
	require.Equal(t, 1, failedRun.Source.AttemptNumber)
	require.Equal(t, slug.FromString(failedRun.RunSlug), failedRun.Source.RootRunSlug)
	require.Nil(t, failedRun.Source.RetryOfRunSlug)
	require.NotNil(t, failedRun.Source.SourceEventSlug)

	completion := requireScheduleFireCompletion(t, pool, orgSlug, projectSlug, environmentSlug, triggerSlug, 1, now)
	require.Equal(t, "failed", completion.status)
	require.NotNil(t, completion.runSlug)
	require.Equal(t, failedRun.RunSlug, *completion.runSlug)
	require.NotNil(t, completion.sourceEventSlug)
	require.Equal(t, failedRun.Source.SourceEventSlug.String(), *completion.sourceEventSlug)

	attempt := requireTriggerRetryAttempt(t, pool, orgSlug, projectSlug, environmentSlug, triggerSlug)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusQueued.String(), attempt.status)
	require.Nil(t, attempt.decisionReason)
	require.Equal(t, pipelineSlug.String(), attempt.pipelineSlug)
	require.Equal(t, *spec.CurrentVersionSpecHash, attempt.pipelineVersionSpecHash)
	require.Equal(t, []string{"customer_id", "dry_run"}, attempt.runtimeInputKeys)
	require.True(t, attempt.retryEnabled)
	require.Equal(t, int32(3), attempt.retryMaxAttempts)
	require.Equal(t, model.PipelineTriggerBackoffStrategyFixed.String(), attempt.retryBackoffStrategy)
	require.Equal(t, int32(60), attempt.retryInitialDelaySeconds)
	require.Equal(t, int32(60), attempt.retryMaxDelaySeconds)
	require.Equal(t, []string{model.PipelineTriggerRetryFailureKindDataSource.String()}, attempt.retryableFailureKinds)
	require.Equal(t, failedRun.RunSlug, attempt.rootRunSlug)
	require.Equal(t, failedRun.RunSlug, attempt.retryOfRunSlug)
	require.Equal(t, failedRun.RunSlug+"-retry-2", attempt.runSlug)
	require.Equal(t, int32(2), attempt.attemptNumber)
	require.NotNil(t, attempt.failureKind)
	require.Equal(t, model.PipelineTriggerRetryFailureKindDataSource.String(), *attempt.failureKind)
	require.True(t, attempt.scheduledAt.Equal(failedRun.StartedAt.Add(time.Minute)))
	require.True(t, attempt.enqueuedAt.Valid)
	require.False(t, attempt.startedAt.Valid)
	require.False(t, attempt.completedAt.Valid)
	require.Equal(t, failedRun.Source.SourceEventSlug.String(), *attempt.sourceEventSlug)
	require.Equal(t, "schedule", attempt.sourceType)
	require.Equal(t, "scheduler", attempt.sourceEntrypoint)
	require.Equal(t, "system", attempt.sourceActorType)
	require.Equal(t, []string{"customer_id", "dry_run"}, attempt.sourceParameterKeys)
	require.Equal(t, triggerSlug.String(), *attempt.sourceScheduleTriggerSlug)
	require.Equal(t, int64(1), *attempt.sourceScheduleTriggerRevision)
	require.Equal(t, "0 7 * * *", *attempt.sourceScheduleCron)
	require.Equal(t, "UTC", *attempt.sourceScheduleTimezone)

	retryArgs := jobs.PipelineTriggerRetryExecutionArgs{
		OrganizationSlug: orgSlug,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		RetryAttemptSlug: slug.FromString(attempt.attemptSlug),
		ScheduledAt:      attempt.scheduledAt,
	}
	requireTriggerRetryExecutionJob(t, pool, retryQueue, retryArgs)

	err = scheduleSvc.ExecuteTriggerRetryAttempt(ctx, retryArgs)
	require.NoError(t, err)
	require.Equal(t, 2, queryExecutor.Snapshot())

	runs, err = pipelineSvc.ListRuns(ctx, orgSlug, projectSlug, environmentSlug, nil, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, runs.Items, 3)
	firstRetryRun := requirePipelineRunBySlug(t, runs.Items, attempt.runSlug)
	require.Equal(t, "failed", firstRetryRun.Status)
	require.Equal(t, attempt.pipelineVersionSpecHash, firstRetryRun.PipelineVersionSpecHash)
	require.NotNil(t, firstRetryRun.FailureKind)
	require.Equal(t, model.PipelineTriggerRetryFailureKindDataSource.String(), *firstRetryRun.FailureKind)
	require.Equal(t, 2, firstRetryRun.Source.AttemptNumber)
	require.Equal(t, slug.FromString(failedRun.RunSlug), firstRetryRun.Source.RootRunSlug)
	require.NotNil(t, firstRetryRun.Source.RetryOfRunSlug)
	require.Equal(t, slug.FromString(failedRun.RunSlug), *firstRetryRun.Source.RetryOfRunSlug)
	require.NotNil(t, firstRetryRun.Source.SourceEventSlug)
	require.NotEqual(t, failedRun.Source.SourceEventSlug.String(), firstRetryRun.Source.SourceEventSlug.String())
	require.NotNil(t, firstRetryRun.Source.Schedule)
	require.Equal(t, triggerSlug, firstRetryRun.Source.Schedule.TriggerSlug)
	require.Equal(t, int64(1), firstRetryRun.Source.Schedule.TriggerRevision)

	attempts := requireTriggerRetryAttempts(t, pool, orgSlug, projectSlug, environmentSlug, triggerSlug)
	require.Len(t, attempts, 2)
	firstAttempt := attempts[0]
	require.Equal(t, attempt.attemptSlug, firstAttempt.attemptSlug)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusFailed.String(), firstAttempt.status)
	require.True(t, firstAttempt.startedAt.Valid)
	require.True(t, firstAttempt.completedAt.Valid)
	require.NotNil(t, firstAttempt.sourceEventSlug)
	require.Equal(t, firstRetryRun.Source.SourceEventSlug.String(), *firstAttempt.sourceEventSlug)

	secondAttempt := attempts[1]
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusQueued.String(), secondAttempt.status)
	require.Nil(t, secondAttempt.decisionReason)
	require.Equal(t, failedRun.RunSlug, secondAttempt.rootRunSlug)
	require.Equal(t, firstRetryRun.RunSlug, secondAttempt.retryOfRunSlug)
	require.Equal(t, failedRun.RunSlug+"-retry-3", secondAttempt.runSlug)
	require.Equal(t, int32(3), secondAttempt.attemptNumber)
	require.NotNil(t, secondAttempt.failureKind)
	require.Equal(t, model.PipelineTriggerRetryFailureKindDataSource.String(), *secondAttempt.failureKind)
	require.True(t, secondAttempt.scheduledAt.Equal(firstRetryRun.StartedAt.Add(time.Minute)))
	require.True(t, secondAttempt.enqueuedAt.Valid)
	require.False(t, secondAttempt.startedAt.Valid)
	require.False(t, secondAttempt.completedAt.Valid)
	require.NotNil(t, secondAttempt.sourceEventSlug)
	require.Equal(t, firstRetryRun.Source.SourceEventSlug.String(), *secondAttempt.sourceEventSlug)

	secondRetryArgs := jobs.PipelineTriggerRetryExecutionArgs{
		OrganizationSlug: orgSlug,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		RetryAttemptSlug: slug.FromString(secondAttempt.attemptSlug),
		ScheduledAt:      secondAttempt.scheduledAt,
	}
	requireTriggerRetryExecutionJob(t, pool, retryQueue, secondRetryArgs)

	err = scheduleSvc.ExecuteTriggerRetryAttempt(ctx, secondRetryArgs)
	require.NoError(t, err)
	require.Equal(t, 3, queryExecutor.Snapshot())

	runs, err = pipelineSvc.ListRuns(ctx, orgSlug, projectSlug, environmentSlug, nil, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, runs.Items, 3)
	secondRetryRun := requirePipelineRunBySlug(t, runs.Items, secondAttempt.runSlug)
	require.Equal(t, "succeeded", secondRetryRun.Status)
	require.Equal(t, secondAttempt.pipelineVersionSpecHash, secondRetryRun.PipelineVersionSpecHash)
	require.Equal(t, 3, secondRetryRun.Source.AttemptNumber)
	require.Equal(t, slug.FromString(failedRun.RunSlug), secondRetryRun.Source.RootRunSlug)
	require.NotNil(t, secondRetryRun.Source.RetryOfRunSlug)
	require.Equal(t, slug.FromString(firstRetryRun.RunSlug), *secondRetryRun.Source.RetryOfRunSlug)
	require.NotNil(t, secondRetryRun.Source.SourceEventSlug)
	require.NotEqual(t, firstRetryRun.Source.SourceEventSlug.String(), secondRetryRun.Source.SourceEventSlug.String())
	require.NotNil(t, secondRetryRun.Source.Schedule)
	require.Equal(t, triggerSlug, secondRetryRun.Source.Schedule.TriggerSlug)
	require.Equal(t, int64(1), secondRetryRun.Source.Schedule.TriggerRevision)

	completedAttempts := requireTriggerRetryAttempts(t, pool, orgSlug, projectSlug, environmentSlug, triggerSlug)
	require.Len(t, completedAttempts, 2)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusFailed.String(), completedAttempts[0].status)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusSucceeded.String(), completedAttempts[1].status)
	require.True(t, completedAttempts[1].startedAt.Valid)
	require.True(t, completedAttempts[1].completedAt.Valid)
	require.Nil(t, completedAttempts[1].errorSummary)
	require.NotNil(t, completedAttempts[1].sourceEventSlug)
	require.Equal(t, secondRetryRun.Source.SourceEventSlug.String(), *completedAttempts[1].sourceEventSlug)

	visibleAttempts, err := triggerSvc.ListPipelineRunTriggerRetryAttempts(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		slug.FromString(firstRetryRun.RunSlug),
	)
	require.NoError(t, err)
	require.Len(t, visibleAttempts, 2)
	require.Equal(t, slug.FromString(completedAttempts[0].attemptSlug), visibleAttempts[0].AttemptSlug)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusFailed, visibleAttempts[0].Status)
	require.Equal(t, slug.FromString(failedRun.RunSlug), visibleAttempts[0].RootRunSlug)
	require.Equal(t, slug.FromString(firstRetryRun.RunSlug), visibleAttempts[0].RunSlug)
	require.NotNil(t, visibleAttempts[0].SourceEventSlug)
	require.Equal(t, firstRetryRun.Source.SourceEventSlug.String(), visibleAttempts[0].SourceEventSlug.String())
	require.Equal(t, slug.FromString(completedAttempts[1].attemptSlug), visibleAttempts[1].AttemptSlug)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusSucceeded, visibleAttempts[1].Status)
	require.Equal(t, slug.FromString(secondRetryRun.RunSlug), visibleAttempts[1].RunSlug)
	require.NotNil(t, visibleAttempts[1].SourceEventSlug)
	require.Equal(t, secondRetryRun.Source.SourceEventSlug.String(), visibleAttempts[1].SourceEventSlug.String())
}

func TestService_ExecuteClaimedScheduleFireSkipsFireAfterEnvironmentSoftDelete(t *testing.T) { // Schedule scanner intentionally crosses tenants and would see parallel schedule E2E fixtures.
	if testing.Short() {
		t.Skip("skipping real-Postgres scheduled pipeline dispatch E2E test in short mode")
	}

	ctx := context.Background()
	pool := openScheduleE2EPool(t)
	txm := postgres.NewTransactionManager(pool)
	eventsSvc := eventssvc.New(txm, events.New(), clock.New())
	eventProducer := eventtest.NewProducer(eventsSvc)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	pipelineSvc := newScheduleE2EPipelineService(t, pool, txm, store, eventProducer)
	triggerSvc := pipelinetriggersvc.New(false, txm, pipelinetriggers.NewRepository(), scheduleCompletionValidator{}, eventProducer, scheduleWebhookSecrets{})
	enqueueClient, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	unique := time.Now().UnixNano()
	scheduleQueue := fmt.Sprintf("%s_service_e2e_%d", jobs.PipelineScheduleQueue, unique)
	scheduleProducer, err := workerproducer.NewPipelineScheduleProducerForQueue(
		queue.NewEnqueuer(enqueueClient),
		scheduleQueue,
	)
	require.NoError(t, err)
	scheduleSvc := pipelineschedulesvc.New(txm, pipelinetriggers.NewRepository(), pipelineSvc, scheduleProducer, nil)
	stack := newScheduleE2EStack(pool, txm, eventProducer)

	orgSlug := must.Slug(fmt.Sprintf("schedule-soft-delete-org-%d", unique))
	userSlug := must.Slug(fmt.Sprintf("schedule-soft-delete-user-%d", unique))
	projectSlug := must.Slug("default")
	environmentSlug := must.Slug("scheduled")
	pipelineSlug := must.Slug("scheduled-users")
	triggerSlug := must.Slug("daily-users")
	now := time.Date(2026, time.June, 9, 8, 0, 0, 0, time.UTC)
	seedScheduleE2EProjectEnvironment(t, pool, stack, userSlug, orgSlug, projectSlug, environmentSlug)

	pipeline := scheduleE2EPipeline(pipelineSlug)
	materializePipelines(t, ctx, store, orgSlug, projectSlug, pipeline)
	spec, err := store.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
	require.NoError(t, err)
	require.NotNil(t, spec.CurrentVersionSpecHash)
	_, err = pipelineSvc.Deploy(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, model.SystemEventActor(), pipelinesvc.DeployPipelineRequest{
		SpecHash: *spec.CurrentVersionSpecHash,
	})
	require.NoError(t, err)

	_, err = triggerSvc.CreatePipelineTrigger(ctx, orgSlug, projectSlug, environmentSlug, model.NewPipelineTrigger{
		Name:             "Daily users",
		Slug:             triggerSlug,
		Type:             model.PipelineTriggerTypeSchedule,
		Enabled:          true,
		PipelineSlug:     pipelineSlug,
		RuntimeInputKeys: []string{"customer_id"},
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:         false,
			MaxAttempts:     1,
			BackoffStrategy: model.PipelineTriggerBackoffStrategyNone,
		},
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 8 * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}, model.SystemEventActor())
	require.NoError(t, err)

	result, err := scheduleSvc.DispatchDueSchedules(ctx, now)
	require.NoError(t, err)
	require.Equal(t, pipelineschedulesvc.DispatchResult{Scanned: 1, Claimed: 1, Enqueued: 1}, result)
	executionArgs := jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: orgSlug,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		TriggerSlug:      triggerSlug,
		TriggerRevision:  1,
		ScheduledFireAt:  now,
	}

	softDeleteScheduleE2EProjectEnvironment(t, pool, orgSlug, projectSlug, environmentSlug)

	err = scheduleSvc.ExecuteClaimedScheduleFire(ctx, executionArgs)
	require.NoError(t, err)
	completion := requireScheduleFireCompletion(t, pool, orgSlug, projectSlug, environmentSlug, triggerSlug, 1, now)
	require.Equal(t, "claimed", completion.status)
	require.Nil(t, completion.runSlug)
	require.Nil(t, completion.sourceEventSlug)
	require.Nil(t, completion.errorSummary)

	err = scheduleSvc.ExecuteClaimedScheduleFire(ctx, executionArgs)
	require.NoError(t, err)
	afterRetry := requireScheduleFireCompletion(t, pool, orgSlug, projectSlug, environmentSlug, triggerSlug, 1, now)
	require.Equal(t, completion.status, afterRetry.status)
	require.Equal(t, completion.runSlug, afterRetry.runSlug)
	require.Equal(t, completion.sourceEventSlug, afterRetry.sourceEventSlug)
	require.Equal(t, completion.errorSummary, afterRetry.errorSummary)
}

func TestService_ExecuteClaimedScheduleFireHonorsPinnedVersionPolicy(t *testing.T) { // Schedule scanner intentionally crosses tenants and would see parallel schedule E2E fixtures.
	if testing.Short() {
		t.Skip("skipping real-Postgres scheduled pipeline dispatch E2E test in short mode")
	}

	ctx := context.Background()
	pool := openScheduleE2EPool(t)
	txm := postgres.NewTransactionManager(pool)
	eventsSvc := eventssvc.New(txm, events.New(), clock.New())
	eventProducer := eventtest.NewProducer(eventsSvc)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	pipelineSvc := newScheduleE2EPipelineService(t, pool, txm, store, eventProducer)
	triggerSvc := pipelinetriggersvc.New(false, txm, pipelinetriggers.NewRepository(), scheduleCompletionValidator{}, eventProducer, scheduleWebhookSecrets{})
	enqueueClient, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	unique := time.Now().UnixNano()
	scheduleQueue := fmt.Sprintf("%s_service_e2e_%d", jobs.PipelineScheduleQueue, unique)
	scheduleProducer, err := workerproducer.NewPipelineScheduleProducerForQueue(
		queue.NewEnqueuer(enqueueClient),
		scheduleQueue,
	)
	require.NoError(t, err)
	scheduleSvc := pipelineschedulesvc.New(txm, pipelinetriggers.NewRepository(), pipelineSvc, scheduleProducer, nil)
	stack := newScheduleE2EStack(pool, txm, eventProducer)

	orgSlug := must.Slug(fmt.Sprintf("schedule-pinned-org-%d", unique))
	userSlug := must.Slug(fmt.Sprintf("schedule-pinned-user-%d", unique))
	projectSlug := must.Slug("default")
	environmentSlug := must.Slug("scheduled")
	pipelineSlug := must.Slug("scheduled-users")
	triggerSlug := must.Slug("daily-users")
	now := time.Date(2026, time.June, 10, 7, 0, 0, 0, time.UTC)
	seedScheduleE2EProjectEnvironment(t, pool, stack, userSlug, orgSlug, projectSlug, environmentSlug)

	pipelineV1 := scheduleE2EPipeline(pipelineSlug)
	materializePipelines(t, ctx, store, orgSlug, projectSlug, pipelineV1)
	specV1, err := store.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
	require.NoError(t, err)
	require.NotNil(t, specV1.CurrentVersionSpecHash)
	pinnedHash := *specV1.CurrentVersionSpecHash
	_, err = pipelineSvc.Deploy(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, model.SystemEventActor(), pipelinesvc.DeployPipelineRequest{
		SpecHash: pinnedHash,
	})
	require.NoError(t, err)

	_, err = triggerSvc.CreatePipelineTrigger(ctx, orgSlug, projectSlug, environmentSlug, model.NewPipelineTrigger{
		Name:             "Daily users",
		Slug:             triggerSlug,
		Type:             model.PipelineTriggerTypeSchedule,
		Enabled:          true,
		PipelineSlug:     pipelineSlug,
		RuntimeInputKeys: []string{"customer_id"},
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode:              model.PipelineTriggerVersionPolicyModePinned,
			PinnedVersionHash: &pinnedHash,
		},
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:         false,
			MaxAttempts:     1,
			BackoffStrategy: model.PipelineTriggerBackoffStrategyNone,
		},
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 7 * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}, model.SystemEventActor())
	require.NoError(t, err)

	pipelineV2 := scheduleE2EPipeline(pipelineSlug)
	pipelineV2.Metadata.Name = "Scheduled Users V2"
	materializePipelines(t, ctx, store, orgSlug, projectSlug, pipelineV2)
	specV2, err := store.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
	require.NoError(t, err)
	require.NotNil(t, specV2.CurrentVersionSpecHash)
	require.NotEqual(t, pinnedHash, *specV2.CurrentVersionSpecHash)
	_, err = pipelineSvc.Deploy(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, model.SystemEventActor(), pipelinesvc.DeployPipelineRequest{
		SpecHash: *specV2.CurrentVersionSpecHash,
	})
	require.NoError(t, err)

	result, err := scheduleSvc.DispatchDueSchedules(ctx, now)
	require.NoError(t, err)
	require.Equal(t, pipelineschedulesvc.DispatchResult{Scanned: 1, Claimed: 1, Enqueued: 1}, result)

	err = scheduleSvc.ExecuteClaimedScheduleFire(ctx, jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: orgSlug,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		TriggerSlug:      triggerSlug,
		TriggerRevision:  1,
		ScheduledFireAt:  now,
	})
	require.NoError(t, err)

	runs, err := pipelineSvc.ListRuns(ctx, orgSlug, projectSlug, environmentSlug, nil, model.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	require.Equal(t, pinnedHash, runs.Items[0].PipelineVersionSpecHash)
	require.NotEqual(t, *specV2.CurrentVersionSpecHash, runs.Items[0].PipelineVersionSpecHash)
}

func softDeleteScheduleE2EProjectEnvironment(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tag, err := pool.Exec(ctx, `
		UPDATE project_environments pe
		SET deleted_at = NOW(), delete_reason = 'schedule execution regression', updated_at = NOW()
		FROM projects p, organizations o, environments e
		WHERE pe.project_id = p.id
		  AND p.organization_id = o.id
		  AND pe.environment_id = e.id
		  AND e.organization_id = o.id
		  AND o.slug = $1
		  AND p.slug = $2
		  AND e.slug = $3`,
		orgSlug.String(),
		projectSlug.String(),
		environmentSlug.String(),
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())

	tag, err = pool.Exec(ctx, `
		UPDATE environments e
		SET deleted_at = NOW(), delete_reason = 'schedule execution regression', updated_at = NOW()
		FROM organizations o
		WHERE e.organization_id = o.id
		  AND o.slug = $1
		  AND e.slug = $2`,
		orgSlug.String(),
		environmentSlug.String(),
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())
}

func requireScheduleFireExecutionJobInQueue(
	t *testing.T,
	pool *pgxpool.Pool,
	queueName string,
	args jobs.PipelineScheduleFireExecutionArgs,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	argsJSON, err := json.Marshal(args)
	require.NoError(t, err)
	var count int
	err = pool.QueryRow(
		ctx,
		`SELECT count(*) FROM river_job WHERE queue = $1 AND kind = $2 AND args @> $3::jsonb`,
		queueName,
		args.Kind(),
		string(argsJSON),
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func requireTriggerRetryExecutionJob(
	t *testing.T,
	pool *pgxpool.Pool,
	queueName string,
	args jobs.PipelineTriggerRetryExecutionArgs,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	argsJSON, err := json.Marshal(args)
	require.NoError(t, err)
	var count int
	err = pool.QueryRow(
		ctx,
		`SELECT count(*) FROM river_job WHERE queue = $1 AND kind = $2 AND args @> $3::jsonb`,
		queueName,
		args.Kind(),
		string(argsJSON),
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

type scheduleFireCompletionRow struct {
	status          string
	runSlug         *string
	sourceEventSlug *string
	errorSummary    *string
}

type triggerRetryAttemptRow struct {
	attemptSlug                   string
	status                        string
	decisionReason                *string
	pipelineSlug                  string
	pipelineVersionSpecHash       string
	runtimeInputKeys              []string
	retryEnabled                  bool
	retryMaxAttempts              int32
	retryBackoffStrategy          string
	retryInitialDelaySeconds      int32
	retryMaxDelaySeconds          int32
	retryableFailureKinds         []string
	sourceType                    string
	sourceEntrypoint              string
	sourceActorType               string
	sourceEventSlug               *string
	sourceParameterKeys           []string
	sourceScheduleTriggerSlug     *string
	sourceScheduleTriggerRevision *int64
	sourceScheduleCron            *string
	sourceScheduleTimezone        *string
	rootRunSlug                   string
	retryOfRunSlug                string
	runSlug                       string
	attemptNumber                 int32
	failureKind                   *string
	scheduledAt                   time.Time
	enqueuedAt                    pgtype.Timestamptz
	startedAt                     pgtype.Timestamptz
	completedAt                   pgtype.Timestamptz
	errorSummary                  *string
}

func requireScheduleFireCompletion(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	triggerRevision int64,
	scheduledFireAt time.Time,
) scheduleFireCompletionRow {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var row scheduleFireCompletionRow
	err := pool.QueryRow(
		ctx,
		`SELECT psf.status, psf.run_slug, psf.source_event_slug, psf.error_summary
		 FROM pipeline_schedule_fires psf
		 JOIN pipeline_triggers pt ON pt.id = psf.pipeline_trigger_id
		 JOIN project_environments pe ON pe.id = psf.project_environment_id
		 JOIN projects p ON p.id = pe.project_id
		 JOIN organizations o ON o.id = p.organization_id
		 JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
		 WHERE o.slug = $1
		   AND p.slug = $2
		   AND e.slug = $3
		   AND pt.slug = $4
		   AND psf.trigger_revision = $5
		   AND psf.scheduled_fire_at = $6`,
		orgSlug.String(),
		projectSlug.String(),
		environmentSlug.String(),
		triggerSlug.String(),
		triggerRevision,
		scheduledFireAt,
	).Scan(&row.status, &row.runSlug, &row.sourceEventSlug, &row.errorSummary)
	require.NoError(t, err)
	return row
}

func requireTriggerRetryAttempt(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
) triggerRetryAttemptRow {
	t.Helper()

	attempts := requireTriggerRetryAttempts(t, pool, orgSlug, projectSlug, environmentSlug, triggerSlug)
	require.NotEmpty(t, attempts)
	return attempts[len(attempts)-1]
}

func requireTriggerRetryAttempts(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
) []triggerRetryAttemptRow {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := pool.Query(
		ctx,
		`SELECT
			ptra.attempt_slug,
			ptra.status,
			ptra.decision_reason,
			ptra.pipeline_slug,
			ptra.pipeline_version_spec_hash,
			ptra.runtime_input_keys,
			ptra.retry_enabled,
			ptra.retry_max_attempts,
			ptra.retry_backoff_strategy,
			ptra.retry_initial_delay_seconds,
			ptra.retry_max_delay_seconds,
			ptra.retryable_failure_kinds,
			ptra.source_type,
			ptra.source_entrypoint,
			ptra.source_actor_type,
			ptra.source_event_slug,
			ptra.source_parameter_keys,
			ptra.source_schedule_trigger_slug,
			ptra.source_schedule_trigger_revision,
			ptra.source_schedule_cron,
			ptra.source_schedule_timezone,
			ptra.root_run_slug,
			ptra.retry_of_run_slug,
			ptra.run_slug,
			ptra.attempt_number,
			ptra.failure_kind,
			ptra.scheduled_at,
			ptra.enqueued_at,
			ptra.started_at,
			ptra.completed_at,
			ptra.error_summary
		 FROM pipeline_trigger_retry_attempts ptra
		 JOIN pipeline_triggers pt ON pt.id = ptra.pipeline_trigger_id
		 JOIN project_environments pe ON pe.id = ptra.project_environment_id
		 JOIN projects p ON p.id = pe.project_id
		 JOIN organizations o ON o.id = p.organization_id
		 JOIN environments e ON e.id = pe.environment_id AND e.organization_id = o.id
		 WHERE o.slug = $1
		   AND p.slug = $2
		   AND e.slug = $3
		   AND pt.slug = $4
		 ORDER BY ptra.attempt_number ASC`,
		orgSlug.String(),
		projectSlug.String(),
		environmentSlug.String(),
		triggerSlug.String(),
	)
	require.NoError(t, err)
	defer rows.Close()

	var attempts []triggerRetryAttemptRow
	for rows.Next() {
		attempt, scanErr := scanTriggerRetryAttempt(rows)
		require.NoError(t, scanErr)
		attempts = append(attempts, attempt)
	}
	require.NoError(t, rows.Err())
	return attempts
}

type triggerRetryAttemptScanner interface {
	Scan(dest ...any) error
}

func scanTriggerRetryAttempt(scanner triggerRetryAttemptScanner) (triggerRetryAttemptRow, error) {
	var row triggerRetryAttemptRow
	err := scanner.Scan(
		&row.attemptSlug,
		&row.status,
		&row.decisionReason,
		&row.pipelineSlug,
		&row.pipelineVersionSpecHash,
		&row.runtimeInputKeys,
		&row.retryEnabled,
		&row.retryMaxAttempts,
		&row.retryBackoffStrategy,
		&row.retryInitialDelaySeconds,
		&row.retryMaxDelaySeconds,
		&row.retryableFailureKinds,
		&row.sourceType,
		&row.sourceEntrypoint,
		&row.sourceActorType,
		&row.sourceEventSlug,
		&row.sourceParameterKeys,
		&row.sourceScheduleTriggerSlug,
		&row.sourceScheduleTriggerRevision,
		&row.sourceScheduleCron,
		&row.sourceScheduleTimezone,
		&row.rootRunSlug,
		&row.retryOfRunSlug,
		&row.runSlug,
		&row.attemptNumber,
		&row.failureKind,
		&row.scheduledAt,
		&row.enqueuedAt,
		&row.startedAt,
		&row.completedAt,
		&row.errorSummary,
	)
	return row, err
}

func requirePipelineRunBySlug(
	t *testing.T,
	runs []*pipelinesvc.PipelineRunMetadata,
	runSlug string,
) *pipelinesvc.PipelineRunMetadata {
	t.Helper()

	for _, run := range runs {
		if run.RunSlug == runSlug {
			return run
		}
	}
	require.Failf(t, "pipeline run not found", "run_slug=%s", runSlug)
	return nil
}

func openScheduleE2EPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connString := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if connString == "" {
		t.Skip("skipping real-Postgres scheduled dispatch E2E test: DATABASE_URL is not set")
	}

	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("required real-Postgres scheduled dispatch E2E test: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("required real-Postgres scheduled dispatch E2E test: %v", err)
	}

	cleanupScheduleE2EFixtures(t, pool)
	t.Cleanup(pool.Close)
	return pool
}

// cleanupScheduleE2EFixtures removes schedule-service test data left by aborted
// runs so the cross-tenant schedule scanner sees only the current test fixture.
func cleanupScheduleE2EFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `DELETE FROM project_environments WHERE organization_id IN (SELECT id FROM organizations WHERE slug LIKE 'schedule-dispatch-org-%'
		   OR slug LIKE 'schedule-retry-org-%'
		   OR slug LIKE 'schedule-soft-delete-org-%'
		   OR slug LIKE 'schedule-pinned-org-%'
		   OR slug LIKE 'schedule-update-org-%')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		DELETE FROM organizations
		WHERE slug LIKE 'schedule-dispatch-org-%'
		   OR slug LIKE 'schedule-retry-org-%'
		   OR slug LIKE 'schedule-soft-delete-org-%'
		   OR slug LIKE 'schedule-pinned-org-%'
		   OR slug LIKE 'schedule-update-org-%'`)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		DELETE FROM users
		WHERE slug LIKE 'schedule-dispatch-user-%'
		   OR slug LIKE 'schedule-retry-user-%'
		   OR slug LIKE 'schedule-soft-delete-user-%'
		   OR slug LIKE 'schedule-pinned-user-%'
		   OR slug LIKE 'schedule-update-user-%'`)
	require.NoError(t, err)
}

func seedScheduleE2EProjectEnvironment(
	t *testing.T,
	pool *pgxpool.Pool,
	stack scheduleE2EStack,
	userSlug,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	user, err := stack.users.CreateUser(ctx, model.NewUser{
		FullName: "Schedule Dispatch Owner",
		Email:    userSlug.String() + "@example.test",
		Password: "password123",
		Role:     model.RoleSuperAdmin,
		Locale:   "en",
	})
	require.NoError(t, err)

	_, err = stack.organizations.CreateOrganization(ctx, user.Slug, model.UserEventActor(user.Slug), model.NewOrganization{
		Name: "Schedule Dispatch Org",
		Slug: orgSlug,
	})
	require.NoError(t, err)

	_, err = stack.projects.CreateProject(ctx, orgSlug, model.NewProject{
		Name: "Default Project",
		Slug: projectSlug,
	}, model.UserEventActor(user.Slug))
	require.NoError(t, err)

	_, err = stack.environments.CreateEnvironment(ctx, orgSlug, model.NewEnvironment{
		Name:  "Scheduled",
		Slug:  environmentSlug,
		Color: model.EnvironmentColorPurple,
	}, model.UserEventActor(user.Slug))
	require.NoError(t, err)

	_, err = stack.environments.EnableProjectEnvironment(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		model.EnableProjectEnvironment{},
		model.UserEventActor(user.Slug),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()

		_, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, cleanupErr)

		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM users WHERE slug = $1`, user.Slug)
		require.NoError(t, cleanupErr)
	})
}

type scheduleE2EStack struct {
	organizations orgsvc.Service
	environments  environmentssvc.Service
	projects      projectssvc.Service
	users         userssvc.UsersService
}

func newScheduleE2EStack(
	pool *pgxpool.Pool,
	txm postgres.TransactionManager,
	eventProducer workerproducer.EventProducer,
) scheduleE2EStack {
	memberInvitationsRepo := memberinvitations.NewRepository()
	membersRepo := members.NewRepository()
	organizationsRepo := organizations.NewRepository()
	usersSvc := userssvc.New(
		txm,
		&testutil.FakeHasher{},
		users.NewRepository(),
		&testutil.FakeEmailProducer{},
		eventProducer,
		"https://frontend.example.test",
	)
	rolesSvc := rolessvc.New(txm, roles.NewRepository(), permissions.NewRepository(), eventProducer)
	membersSvc := memberssvc.New(
		txm,
		membersRepo,
		memberInvitationsRepo,
		organizationsRepo,
		rolesSvc,
		usersSvc,
		&testutil.FakeEmailProducer{},
		eventProducer,
		"https://frontend.example.test",
		clock.New(),
	)
	environmentsRepo := environments.NewRepository()
	environmentsSvc := environmentssvc.New(txm, environmentsRepo, eventProducer)

	return scheduleE2EStack{
		organizations: orgsvc.New(
			txm,
			organizationsRepo,
			environmentsSvc,
			membersSvc,
			rolesSvc,
			eventProducer,
			admissionsvc.New(txm, organizationsRepo),
		),
		environments: environmentsSvc,
		projects: projectssvc.New(
			txm,
			projects.NewRepository(),
			organizations.NewRepository(),
			environmentsRepo,
			nil,
			nil,
			nil,
			nil,
			eventProducer,
		),
		users: usersSvc,
	}
}

func scheduleE2EPipeline(pipelineSlug slug.Slug) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Scheduled Users",
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: must.Slug("fetch-users"),
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: must.Slug("warehouse"),
				OperationType:  "sql",
				OperationRef:   must.Slug("list-users"),
			},
		}},
	}
}

func newScheduleE2EPipelineService(
	t *testing.T,
	pool *pgxpool.Pool,
	txm postgres.TransactionManager,
	store pipelinesvc.PipelineStore,
	eventProducer workerproducer.EventProducer,
) pipelinesvc.Service {
	t.Helper()

	return newScheduleE2EPipelineServiceWithQueryExecutor(
		t,
		pool,
		txm,
		store,
		eventProducer,
		&scheduleE2EQueryExecutor{},
	)
}

type scheduleE2EQueryExecutorPort interface {
	FindBySlug(context.Context, slug.Slug, slug.Slug, slug.Slug) (*queriessvc.Query, error)
	Execute(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		model.EventActor,
		map[string]any,
		...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error)
	GetExpectedInputContract(context.Context, slug.Slug, slug.Slug, slug.Slug) (*payload.Contract, error)
	ExecuteInline(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error)
}

func newScheduleE2EPipelineServiceWithQueryExecutor(
	t *testing.T,
	pool *pgxpool.Pool,
	txm postgres.TransactionManager,
	store pipelinesvc.PipelineStore,
	eventProducer workerproducer.EventProducer,
	queryExecutor scheduleE2EQueryExecutorPort,
) pipelinesvc.Service {
	t.Helper()

	sinkEffectLedger, err := pipelinesvc.NewSinkEffectLedger(txm, pipelinesinkeffects.New())
	require.NoError(t, err)

	svc, err := pipelinesvc.New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		store,
		pool,
		txm,
		pipelineruns.New(), incrementalcheckpoints.New(),
		pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		driftimpact.New(),
		queryExecutor,
		allowAuthorizationService{},
		&fakes.AdmissionService{},
		&fakes.DataSourceService{},
		&fakes.ProjectsService{},
		scheduleE2EGitSourceResolver{},
		clock.New(),
		noopScheduleE2EPipelineRunProducer{},
		noopScheduleE2EPipelineGitProducer{},
		eventProducer,
		noopScheduleE2EPipelineRuntimeResolver{},
		sinkEffectLedger,
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)
	return svc
}

type scheduleE2EQueryExecutor struct {
	mu             sync.Mutex
	executeCalls   int
	executeResults []scheduleE2EExecuteResult
}

type scheduleE2EExecuteResult struct {
	result *result.ExecutionResult
	err    error
}

func (*scheduleE2EQueryExecutor) FindBySlug(context.Context, slug.Slug, slug.Slug, slug.Slug) (*queriessvc.Query, error) {
	return nil, xerror.Unexpected().Detail("saved Workspace operations are not used by scheduler tests")
}

func (e *scheduleE2EQueryExecutor) Execute(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	map[string]any,
	...queriessvc.ExecuteOption,
) (*result.ExecutionResult, error) {
	e.mu.Lock()
	e.executeCalls++
	var response scheduleE2EExecuteResult
	if len(e.executeResults) > 0 {
		response = e.executeResults[0]
		e.executeResults = e.executeResults[1:]
	}
	e.mu.Unlock()

	if response.result != nil || response.err != nil {
		return response.result, response.err
	}
	return &result.ExecutionResult{}, nil
}

func (e *scheduleE2EQueryExecutor) Snapshot() int {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.executeCalls
}

func (*scheduleE2EQueryExecutor) GetExpectedInputContract(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*payload.Contract, error) {
	return &payload.Contract{}, nil
}

func (*scheduleE2EQueryExecutor) ExecuteInline(
	context.Context,
	slug.Slug,
	slug.Slug,
	map[string]any,
	map[string]any,
) (*result.ExecutionResult, error) {
	return &result.ExecutionResult{}, nil
}

type scheduleE2EGitSourceResolver struct{}

func (scheduleE2EGitSourceResolver) Resolve(
	context.Context,
	slug.Slug,
	pipelinegit.SourceRequest,
) (*pipelinegit.Source, error) {
	return &pipelinegit.Source{}, nil
}

type noopScheduleE2EPipelineRuntimeResolver struct{}

func (noopScheduleE2EPipelineRuntimeResolver) ResolvePipelineDataSourceRefs(
	_ context.Context,
	_, _, _ slug.Slug,
	pipeline *pipelinespec.Pipeline,
) (*datasourcerefssvc.ResolvedPipeline, error) {
	return &datasourcerefssvc.ResolvedPipeline{Pipeline: pipeline}, nil
}

func (noopScheduleE2EPipelineRuntimeResolver) SyncPipelineDataSourceRefs(
	context.Context,
	slug.Slug,
	slug.Slug,
	*pipelinespec.Pipeline,
	model.EventActor,
) (*datasourcerefssvc.PipelineDataSourceRefSyncResult, error) {
	return &datasourcerefssvc.PipelineDataSourceRefSyncResult{}, nil
}

func (noopScheduleE2EPipelineRuntimeResolver) ReconcilePipelineDataSourceRefs(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	actor model.EventActor,
) (*datasourcerefssvc.PipelineDataSourceRefSyncResult, error) {
	return noopScheduleE2EPipelineRuntimeResolver{}.SyncPipelineDataSourceRefs(ctx, orgSlug, projectSlug, pipeline, actor)
}

func (noopScheduleE2EPipelineRuntimeResolver) DeleteDataSourceRefsForPipeline(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	model.EventActor,
) error {
	return nil
}

func (noopScheduleE2EPipelineRuntimeResolver) ResolveRuntimeSecrets(
	context.Context,
	slug.Slug,
	slug.Slug,
) ([]model.RuntimeSecretValue, error) {
	return nil, nil
}

type noopScheduleE2EPipelineGitProducer struct{}

type noopScheduleE2EPipelineRunProducer struct{}

func (noopScheduleE2EPipelineRunProducer) EnqueuePipelineRunExecution(
	context.Context,
	postgres.DBTX,
	jobs.PipelineRunExecutionArgs,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (noopScheduleE2EPipelineGitProducer) EnqueueWriteFile(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*string,
	string,
	[]byte,
	string,
	bool,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (noopScheduleE2EPipelineGitProducer) EnqueueDeleteFile(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*string,
	string,
	string,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (noopScheduleE2EPipelineGitProducer) EnqueuePublishProposal(
	context.Context,
	postgres.DBTX,
	jobs.PipelineGitPublishProposalArgs,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (noopScheduleE2EPipelineGitProducer) EnqueueRefreshProposal(
	context.Context,
	postgres.DBTX,
	jobs.PipelineGitRefreshProposalArgs,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

type scheduleCompletionValidator struct{}

func (scheduleCompletionValidator) ValidateCompletionExecutionContext(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot) error {
	return nil
}
