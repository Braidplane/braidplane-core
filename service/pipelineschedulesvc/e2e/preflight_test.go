package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/datasourcee2e"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelineschedulesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinetriggersvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

func TestSchedulePreflightRefreshesPostgresAndChecksTheSelectedOccurrence(t *testing.T) {
	if testing.Short() {
		t.Skip("scheduled preflight integration requires a Core PostgreSQL fixture")
	}

	ctx := t.Context()
	pool := openScheduleE2EPool(t)
	txm := postgres.NewTransactionManager(pool)
	eventProducer := eventtest.NewProducer(eventssvc.New(txm, events.New(), clock.New()))
	stack := newScheduleE2EStack(pool, txm, eventProducer)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	unique := time.Now().UnixNano()
	org := must.Slug(fmt.Sprintf("schedule-dispatch-org-%d", unique))
	user := must.Slug(fmt.Sprintf("schedule-dispatch-user-%d", unique))
	project, environment := must.Slug("default"), must.Slug("scheduled")
	pipelineSlug, triggerSlug, source := must.Slug("scheduled-customers"), must.Slug("daily-customers"), must.Slug("warehouse")
	seedScheduleE2EProjectEnvironment(t, pool, stack, user, org, project, environment)

	_, err = pool.Exec(ctx, `CREATE TABLE preflight_customers (customer_id text NOT NULL)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DROP TABLE preflight_customers`)
		require.NoError(t, err)
	})
	_, err = pool.Exec(ctx, `INSERT INTO preflight_customers (customer_id) VALUES ('42')`)
	require.NoError(t, err)

	enqueueClient, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	enqueuer := queue.NewEnqueuer(enqueueClient)
	schemaProducer, err := workerproducer.NewDataSourceSchemaImportProducer(enqueuer)
	require.NoError(t, err)
	providerUnavailable := false
	dataSources := datasourcee2e.NewStack(t, pool).ServiceWithConfig(t, datasourcee2e.ServiceConfig{
		ConnectionTester:     datasourcee2e.ConnectionTestExecutorFunc(func(context.Context, datasourcessvc.ConnectionTestRequest) error { return nil }),
		SchemaImportProducer: schemaProducer,
		ConnectorFactory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
			// The fixture connector exposes PostgreSQL metadata without a second service or live credentials.
			conn := testutil.NewFakeSQLConnector()
			conn.IntrospectSchemaFn = func(ctx context.Context) (*sqlconnector.Schema, error) {
				if providerUnavailable {
					return nil, datasourcessvc.ErrSchemaFetchFailed.WithPublicDetail("Schema provider temporarily unavailable. Check database access.")
				}
				var columnType string
				err := pool.QueryRow(ctx, `SELECT data_type FROM information_schema.columns
					WHERE table_schema = 'public' AND table_name = 'preflight_customers' AND column_name = 'customer_id'`).Scan(&columnType)
				if err != nil {
					return nil, err
				}
				return &sqlconnector.Schema{Tables: []sqlconnector.Table{{
					Name: "preflight_customers", Columns: []sqlconnector.Column{{Name: "customer_id", Type: columnType}},
				}}}, nil
			}
			return conn, nil
		},
	})
	actor := model.SystemEventActor()
	_, err = dataSources.Create(ctx, org, actor, datasourcee2e.NewPostgresDataSourceParams(source, "Warehouse"))
	require.NoError(t, err)
	schemaJobs := func() []jobs.DataSourceSchemaImportArgs {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT args FROM river_job WHERE kind = $1 AND args->>'organization_slug' = $2 ORDER BY id`, jobs.DataSourceSchemaImportArgs{}.Kind(), org)
		require.NoError(t, err)
		defer rows.Close()
		var pending []jobs.DataSourceSchemaImportArgs
		for rows.Next() {
			var args jobs.DataSourceSchemaImportArgs
			require.NoError(t, rows.Scan(&args))
			pending = append(pending, args)
		}
		require.NoError(t, rows.Err())
		return pending
	}
	_, err = dataSources.RefreshSchema(ctx, org, source, actor)
	require.NoError(t, err)
	require.Len(t, schemaJobs(), 1)
	refresh, err := dataSources.ExecuteQueuedSchemaImport(ctx, schemaJobs()[0])
	require.NoError(t, err)
	require.Equal(t, model.DataSourceSchemaImportStatusReady, refresh.Status)

	queryExecutor := &scheduleE2EQueryExecutor{}
	pipelineSvc := newScheduleE2EPipelineServiceWithQueryExecutor(t, pool, txm, store, eventProducer, queryExecutor, dataSources)
	repo := pipelinetriggers.NewRepository()
	triggerSvc := pipelinetriggersvc.New(false, txm, repo, scheduleCompletionValidator{}, eventProducer, scheduleWebhookSecrets{})
	scheduleProducer, err := workerproducer.NewPipelineScheduleProducerForQueue(enqueuer, fmt.Sprintf("preflight_schedule_%d", unique))
	require.NoError(t, err)
	notifier, err := workerproducer.NewNotificationCandidateProducer(enqueuer)
	require.NoError(t, err)
	scheduleSvc := pipelineschedulesvc.New(txm, repo, pipelineSvc, scheduleProducer, nil,
		pipelineschedulesvc.WithPreflightServices(pipelineSvc, dataSources), pipelineschedulesvc.WithNotificationProducer(notifier))
	preflightNotifications := func() []jobs.NotificationCandidateArgs {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT args FROM river_job
			WHERE kind = $1 AND args->>'organization_slug' = $2 AND args->'candidate'->>'NotificationType' = $3
			ORDER BY id`, jobs.NotificationCandidateArgs{}.Kind(), org, model.NotificationTypePipelineScheduledPreflightFailed.String())
		require.NoError(t, err)
		defer rows.Close()
		var notifications []jobs.NotificationCandidateArgs
		for rows.Next() {
			var args jobs.NotificationCandidateArgs
			require.NoError(t, rows.Scan(&args))
			notifications = append(notifications, args)
		}
		require.NoError(t, rows.Err())
		return notifications
	}
	deploy := func(fieldType model.ContractFieldType) string {
		t.Helper()
		pipeline := scheduleE2EPipeline(pipelineSlug)
		pipeline.Steps[0].Query.OutputContract = &payload.Contract{
			Name: "Customers", Fields: []payload.ContractField{{Name: "customer_id", Type: fieldType, Required: true}},
		}
		materializePipelines(t, ctx, store, org, project, pipeline)
		spec, err := store.FindSpec(ctx, org, project, pipelineSlug)
		require.NoError(t, err)
		require.NotNil(t, spec.CurrentVersionSpecHash)
		hash := *spec.CurrentVersionSpecHash
		_, err = pipelineSvc.Deploy(ctx, org, project, environment, pipelineSlug, actor, pipelinesvc.DeployPipelineRequest{SpecHash: hash})
		require.NoError(t, err)
		return hash
	}
	textVersion := deploy(model.ContractFieldTypeValueString)

	fireAt := time.Now().UTC().Truncate(time.Hour).Add(8 * time.Hour)
	schedule := model.PipelineTriggerScheduleConfig{
		Cron: fmt.Sprintf("0 %d * * *", fireAt.Hour()), Timezone: "UTC",
		MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip, PreflightLeadTime: time.Hour,
	}
	retryPolicy := model.PipelineTriggerRetryPolicy{MaxAttempts: 1, BackoffStrategy: model.PipelineTriggerBackoffStrategyNone}
	latest := model.PipelineTriggerVersionPolicy{Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed}
	trigger, err := triggerSvc.CreatePipelineTrigger(ctx, org, project, environment, model.NewPipelineTrigger{
		Slug: triggerSlug, Name: "Daily customers", Type: model.PipelineTriggerTypeSchedule, Enabled: true,
		PipelineSlug: pipelineSlug, VersionPolicy: latest, RetryPolicy: retryPolicy, ScheduleConfig: &schedule,
	}, actor)
	require.NoError(t, err)
	require.Equal(t, time.Hour, trigger.ScheduleConfig.PreflightLeadTime)

	dispatch := func(occurrence time.Time, revision int64, lead time.Duration) jobs.PipelineSchedulePreflightArgs {
		t.Helper()
		result, err := scheduleSvc.DispatchDueSchedules(ctx, occurrence.Add(-lead))
		require.NoError(t, err)
		require.Equal(t, 1, result.PreflightsEnqueued)
		var raw []byte
		err = pool.QueryRow(ctx, `SELECT args FROM river_job
			WHERE kind = $1 AND args->>'organization_slug' = $2
				AND (args->>'trigger_revision')::bigint = $3
				AND (args->>'scheduled_fire_at')::timestamptz = $4`,
			jobs.PipelineSchedulePreflightArgs{}.Kind(), org, revision, occurrence).Scan(&raw)
		require.NoError(t, err)
		var args jobs.PipelineSchedulePreflightArgs
		require.NoError(t, json.Unmarshal(raw, &args))
		require.Equal(t, environment, args.EnvironmentSlug)
		return args
	}
	read := func() pipelinetriggersvc.ScheduleFireList {
		t.Helper()
		view, err := triggerSvc.ListPipelineScheduleFires(ctx, org, project, environment, triggerSlug, model.ListRequest{})
		require.NoError(t, err)
		return view
	}
	execute := func(args jobs.PipelineSchedulePreflightArgs, wantWaiting bool) {
		t.Helper()
		waiting, err := scheduleSvc.ExecuteSchedulePreflight(ctx, args)
		require.NoError(t, err)
		require.Equal(t, wantWaiting, waiting)
	}

	first := dispatch(fireAt, trigger.Revision, time.Hour)
	retriedDispatch, err := scheduleSvc.DispatchDueSchedules(ctx, fireAt.Add(-time.Hour))
	require.NoError(t, err)
	require.Zero(t, retriedDispatch.PreflightsEnqueued)
	execute(first, true)
	execute(first, true)
	require.Len(t, schemaJobs(), 2)
	require.Nil(t, read().UpcomingPreflights[0].CompletedAt)
	require.Empty(t, preflightNotifications())

	_, err = pool.Exec(ctx, `ALTER TABLE preflight_customers ALTER COLUMN customer_id TYPE integer USING customer_id::integer`)
	require.NoError(t, err)
	integerVersion := deploy(model.ContractFieldTypeValueInteger)
	require.NotEqual(t, textVersion, integerVersion)
	refresh, err = dataSources.ExecuteQueuedSchemaImport(ctx, schemaJobs()[1])
	require.NoError(t, err)
	require.Equal(t, model.DataSourceSchemaImportStatusReady, refresh.Status)
	execute(first, false)
	execute(first, false)
	view := read()
	require.Empty(t, view.Items)
	require.Len(t, view.UpcomingPreflights, 1)
	preflight := view.UpcomingPreflights[0]
	require.Equal(t, fireAt, preflight.ScheduledFireAt.UTC())
	require.Equal(t, fireAt.Add(-time.Hour), preflight.PreflightAt.UTC())
	require.NotNil(t, preflight.VersionHash)
	require.Equal(t, textVersion, *preflight.VersionHash)
	require.NotNil(t, preflight.CompletedAt)
	require.Len(t, preflight.Diagnostics, 1)
	require.Equal(t, pipelineruntime.ErrQueryOutputContractSchemaMismatch.Code(), preflight.Diagnostics[0].Code)
	require.Contains(t, preflight.Diagnostics[0].Message, `"customer_id" type differs`)
	require.Contains(t, preflight.Diagnostics[0].Message, "contract=string, schema=integer")
	require.Len(t, schemaJobs(), 2)
	notices := preflightNotifications()
	require.Len(t, notices, 1, "replaying a failed preflight does not enqueue another candidate")
	candidate := notices[0].Candidate
	require.Equal(t, model.NotificationSeverityWarning, candidate.Severity)
	require.Equal(t, &environment, candidate.EnvironmentSlug)
	require.Equal(t, triggerSlug.String(), candidate.SourceRefs["source_ref"])
	require.Equal(t, fireAt.Format(time.RFC3339Nano), candidate.SourceRefs["scheduled_fire_at"])
	require.Equal(t, preflight.Diagnostics[0].Code, candidate.SafeMetadata["reason_code"])
	require.NotContains(t, candidate.SafeMetadata["summary"], "customer_id")

	fired, err := scheduleSvc.DispatchDueSchedules(ctx, fireAt)
	require.NoError(t, err)
	require.Equal(t, 1, fired.Enqueued)
	view = read()
	require.NotEmpty(t, view.Items)
	require.Equal(t, preflight, view.Items[0].Preflight)

	nextFireAt := fireAt.Add(24 * time.Hour)
	oldRevisionJob := dispatch(nextFireAt, trigger.Revision, time.Hour)
	schedule.PreflightLeadTime = 6 * time.Hour
	update := model.UpdatePipelineTrigger{
		Name: trigger.Name, Type: trigger.Type, Enabled: true, PipelineSlug: pipelineSlug,
		VersionPolicy: model.PipelineTriggerVersionPolicy{Mode: model.PipelineTriggerVersionPolicyModePinned, PinnedVersionHash: &textVersion},
		RetryPolicy:   retryPolicy, ScheduleConfig: &schedule,
	}
	trigger, err = triggerSvc.UpdatePipelineTrigger(ctx, org, project, environment, triggerSlug, update, actor)
	require.NoError(t, err)
	require.Equal(t, 6*time.Hour, trigger.ScheduleConfig.PreflightLeadTime)
	// The test advances the dispatcher clock past the first fire before editing.
	setScheduleTriggerTimes(t, pool, org, triggerSlug, fireAt.Add(time.Minute))
	execute(oldRevisionJob, false)
	require.Len(t, schemaJobs(), 2)

	next := dispatch(nextFireAt, trigger.Revision, 6*time.Hour)
	execute(next, true)
	require.Len(t, schemaJobs(), 3)
	providerUnavailable = true
	refresh, err = dataSources.ExecuteQueuedSchemaImport(ctx, schemaJobs()[2])
	require.NoError(t, err)
	require.Equal(t, model.DataSourceSchemaImportStatusFailed, refresh.Status)
	execute(next, false)
	view = read()
	require.Len(t, view.UpcomingPreflights, 1)
	failed := view.UpcomingPreflights[0]
	require.Equal(t, textVersion, *failed.VersionHash)
	require.NotNil(t, failed.CompletedAt)
	require.Len(t, failed.Diagnostics, 1)
	require.Equal(t, "SCHEMA_REFRESH_FAILED", failed.Diagnostics[0].Code)
	require.Equal(t, &source, failed.Diagnostics[0].DataSourceSlug)
	require.Contains(t, failed.Diagnostics[0].Message, "Check database access.")
	notices = preflightNotifications()
	require.Len(t, notices, 2)
	require.Equal(t, "SCHEMA_REFRESH_FAILED", notices[1].Candidate.SafeMetadata["reason_code"])
	require.NotEqual(t, notices[0].ObservationSlug, notices[1].ObservationSlug)

	last := dispatch(nextFireAt.Add(24*time.Hour), trigger.Revision, 6*time.Hour)
	update.Enabled = false
	_, err = triggerSvc.UpdatePipelineTrigger(ctx, org, project, environment, triggerSlug, update, actor)
	require.NoError(t, err)
	execute(last, false)
	require.Len(t, schemaJobs(), 3)
	require.Empty(t, read().UpcomingPreflights)
	require.Len(t, preflightNotifications(), 2)
	require.Zero(t, queryExecutor.executeCalls)
}
