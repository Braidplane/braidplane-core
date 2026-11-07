package pipelineschedulesvc

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestDispatchDueSchedulesClaimsTimezoneAwareFireAndEnqueuesExecutionJob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tx := &fakeScheduleDBTX{}
	now := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	scheduledAt := now
	repo := &recordingScheduleRepository{
		triggers: []DueScheduleTrigger{
			newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 9 * * *", "Europe/Warsaw", nil),
		},
		claimResults: []ScheduleFireClaimResult{{
			Claimed:  true,
			FireSlug: must.Slug("fire-daily-users-20260609"),
		}},
	}
	executor := &recordingPipelineExecutor{}
	producer := &recordingScheduleFireProducer{}
	svc := New(testutil.FakeTransactionManager{Tx: tx}, repo, executor, producer, nil)

	result, err := svc.DispatchDueSchedules(ctx, now)

	require.NoError(t, err)
	require.Equal(t, DispatchResult{Scanned: 1, Claimed: 1, Enqueued: 1}, result)
	require.Len(t, repo.claims, 1)
	require.Equal(t, must.Slug("acme"), repo.claims[0].OrgSlug)
	require.Equal(t, must.Slug("prod"), repo.claims[0].EnvironmentSlug)
	require.Equal(t, must.Slug("daily-users"), repo.claims[0].TriggerSlug)
	require.Equal(t, int64(7), repo.claims[0].TriggerRevision)
	require.True(t, repo.claims[0].ScheduledFireAt.Equal(scheduledAt))
	require.True(t, repo.claims[0].ActualFireAt.Equal(now))
	require.Equal(t, model.PipelineTriggerScheduleMisfireResultOnTime, repo.claims[0].MisfireResult)

	require.Empty(t, executor.calls)
	require.Empty(t, repo.completions)
	require.Len(t, producer.calls, 1)
	require.Same(t, tx, producer.calls[0].tx)
	require.Equal(t, jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: must.Slug("acme"),
		ProjectSlug:      must.Slug("default"),
		EnvironmentSlug:  must.Slug("prod"),
		TriggerSlug:      must.Slug("daily-users"),
		TriggerRevision:  7,
		ScheduledFireAt:  scheduledAt,
	}, producer.calls[0].args)
}

func TestDispatchDueSchedulesSkipsDisabledTriggersAndScopesClaimsByTenant(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.June, 9, 12, 0, 0, 0, time.UTC)
	lastFireAt := now.Add(-24 * time.Hour)
	repo := &recordingScheduleRepository{
		triggers: []DueScheduleTrigger{
			newDueScheduleTrigger("acme-a", "prod", "hourly-users", "sync-users", true, "0 * * * *", "UTC", &lastFireAt),
			newDueScheduleTrigger("acme-b", "prod", "hourly-users", "sync-users", true, "0 * * * *", "UTC", &lastFireAt),
			newDueScheduleTrigger("acme-c", "prod", "hourly-users", "sync-users", false, "0 * * * *", "UTC", &lastFireAt),
		},
		claimResults: []ScheduleFireClaimResult{
			{Claimed: true, FireSlug: must.Slug("fire-acme-a-hourly-users")},
			{Claimed: true, FireSlug: must.Slug("fire-acme-b-hourly-users")},
		},
	}
	executor := &recordingPipelineExecutor{}
	producer := &recordingScheduleFireProducer{}
	svc := New(testutil.FakeTransactionManager{}, repo, executor, producer, nil)

	result, err := svc.DispatchDueSchedules(ctx, now)

	require.NoError(t, err)
	require.Equal(t, DispatchResult{Scanned: 3, Claimed: 2, Enqueued: 2, SkippedDisabled: 1}, result)
	require.Len(t, repo.claims, 2)
	require.Equal(t, must.Slug("acme-a"), repo.claims[0].OrgSlug)
	require.Equal(t, must.Slug("hourly-users"), repo.claims[0].TriggerSlug)
	require.Equal(t, must.Slug("acme-b"), repo.claims[1].OrgSlug)
	require.Equal(t, must.Slug("hourly-users"), repo.claims[1].TriggerSlug)
	require.Empty(t, executor.calls)
	require.Len(t, producer.calls, 2)
	require.Equal(t, must.Slug("acme-a"), producer.calls[0].args.OrganizationSlug)
	require.Equal(t, must.Slug("acme-b"), producer.calls[1].args.OrganizationSlug)
}

func TestDispatchDueSchedulesAppliesMisfirePolicies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.June, 9, 10, 30, 0, 0, time.UTC)
	lastFireAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	expectedFireAt := time.Date(2026, time.June, 9, 10, 0, 0, 0, time.UTC)
	fireOnce := newDueScheduleTrigger("acme", "prod", "catch-up", "sync-users", true, "0 * * * *", "UTC", &lastFireAt)
	fireOnce.ScheduleConfig.MisfirePolicy = model.PipelineTriggerScheduleMisfirePolicyFireOnce
	skip := newDueScheduleTrigger("acme", "prod", "skip-old", "sync-users", true, "0 * * * *", "UTC", &lastFireAt)
	skip.ScheduleConfig.MisfirePolicy = model.PipelineTriggerScheduleMisfirePolicySkip
	repo := &recordingScheduleRepository{
		triggers: []DueScheduleTrigger{fireOnce, skip},
		claimResults: []ScheduleFireClaimResult{
			{Claimed: true, FireSlug: must.Slug("fire-catch-up")},
			{Claimed: true, FireSlug: must.Slug("fire-skip-old")},
		},
	}
	executor := &recordingPipelineExecutor{}
	producer := &recordingScheduleFireProducer{}
	notifier := &recordingNotificationProducer{}
	svc := New(testutil.FakeTransactionManager{}, repo, executor, producer, nil, WithNotificationProducer(notifier))

	result, err := svc.DispatchDueSchedules(ctx, now)

	require.NoError(t, err)
	require.Equal(t, DispatchResult{Scanned: 2, Claimed: 2, Enqueued: 1, SkippedMisfire: 1}, result)
	require.Len(t, repo.claims, 2)
	require.Equal(t, must.Slug("catch-up"), repo.claims[0].TriggerSlug)
	require.True(t, repo.claims[0].ScheduledFireAt.Equal(expectedFireAt))
	require.Equal(t, model.PipelineTriggerScheduleMisfireResultFireOnce, repo.claims[0].MisfireResult)
	require.Equal(t, must.Slug("skip-old"), repo.claims[1].TriggerSlug)
	require.True(t, repo.claims[1].ScheduledFireAt.Equal(expectedFireAt))
	require.Equal(t, model.PipelineTriggerScheduleMisfireResultSkipped, repo.claims[1].MisfireResult)
	require.Empty(t, executor.calls)
	require.Len(t, producer.calls, 1)
	require.Equal(t, must.Slug("catch-up"), producer.calls[0].args.TriggerSlug)
	require.Len(t, repo.completions, 1)
	require.Equal(t, must.Slug("skip-old"), repo.completions[0].TriggerSlug)
	require.Equal(t, scheduleFireStatusSkipped, repo.completions[0].Status)
	require.Len(t, notifier.calls, 2)
	require.Equal(t, model.NotificationTypePipelineScheduledRunDelayed, notifier.calls[0].NotificationType)
	require.Equal(t, model.NotificationTypePipelineScheduledRunMissing, notifier.calls[1].NotificationType)
	require.Equal(t, expectedFireAt.UTC().Format(time.RFC3339Nano), notifier.calls[1].SourceRefs["scheduled_fire_at"])
	require.Equal(t, "skip-old", notifier.calls[1].SourceRefs["trigger_slug"])
}

func TestDispatchDueSchedulesTreatsDispatchLatencyWithinGraceAsOnTime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scheduledAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	now := scheduledAt.Add(30 * time.Second)
	repo := &recordingScheduleRepository{
		triggers: []DueScheduleTrigger{
			newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil),
		},
		claimResults: []ScheduleFireClaimResult{{
			Claimed:  true,
			FireSlug: must.Slug("fire-daily-users-20260609"),
		}},
	}
	executor := &recordingPipelineExecutor{}
	producer := &recordingScheduleFireProducer{}
	svc := New(testutil.FakeTransactionManager{}, repo, executor, producer, nil)

	result, err := svc.DispatchDueSchedules(ctx, now)

	require.NoError(t, err)
	require.Equal(t, DispatchResult{Scanned: 1, Claimed: 1, Enqueued: 1}, result)
	require.Len(t, repo.claims, 1)
	require.True(t, repo.claims[0].ScheduledFireAt.Equal(scheduledAt))
	require.Equal(t, model.PipelineTriggerScheduleMisfireResultOnTime, repo.claims[0].MisfireResult)
	require.Empty(t, executor.calls)
	require.Len(t, producer.calls, 1)
	require.True(t, producer.calls[0].args.ScheduledFireAt.Equal(scheduledAt))
}

func TestDispatchDueSchedulesDoesNotExecuteWhenFireClaimAlreadyExists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	repo := &recordingScheduleRepository{
		triggers: []DueScheduleTrigger{
			newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil),
		},
		claimResults: []ScheduleFireClaimResult{{
			Claimed:  false,
			FireSlug: must.Slug("fire-daily-users-20260609"),
		}},
	}
	executor := &recordingPipelineExecutor{}
	producer := &recordingScheduleFireProducer{}
	svc := New(testutil.FakeTransactionManager{}, repo, executor, producer, nil)

	result, err := svc.DispatchDueSchedules(ctx, now)

	require.NoError(t, err)
	require.Equal(t, DispatchResult{Scanned: 1, AlreadyClaimed: 1}, result)
	require.Len(t, repo.claims, 1)
	require.Empty(t, executor.calls)
	require.Empty(t, repo.completions)
	require.Empty(t, producer.calls)
}

func TestDispatchDueSchedulesReturnsEnqueueErrorWithoutInlineExecution(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	enqueueErr := xerror.Unexpected().Detail("river insert failed")
	repo := &recordingScheduleRepository{
		triggers: []DueScheduleTrigger{
			newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil),
		},
		claimResults: []ScheduleFireClaimResult{
			{Claimed: true, FireSlug: must.Slug("fire-daily-users-20260609")},
		},
	}
	executor := &recordingPipelineExecutor{}
	producer := &recordingScheduleFireProducer{err: enqueueErr}
	svc := New(testutil.FakeTransactionManager{}, repo, executor, producer, nil)

	result, err := svc.DispatchDueSchedules(ctx, now)

	require.ErrorIs(t, err, enqueueErr)
	require.Equal(t, DispatchResult{Scanned: 1, Claimed: 1}, result)
	require.Len(t, producer.calls, 1)
	require.Empty(t, executor.calls)
	require.Empty(t, repo.completions)
}

func TestExecuteClaimedScheduleFireCompletesSucceededFireWithRunSourceMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scheduledAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	actualAt := scheduledAt.Add(3 * time.Second)
	sourceEventSlug := must.Slug("evt-schedule-run")
	trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
	fire := ClaimedScheduleFire{
		Trigger: trigger,
		Claim: ScheduleFireClaim{
			OrgSlug:         trigger.OrgSlug,
			ProjectSlug:     trigger.ProjectSlug,
			EnvironmentSlug: trigger.EnvironmentSlug,
			TriggerSlug:     trigger.TriggerSlug,
			TriggerRevision: trigger.TriggerRevision,
			ScheduledFireAt: scheduledAt,
			ActualFireAt:    actualAt,
			MisfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
		},
		FireSlug: must.Slug("fire-daily-users-20260609"),
	}
	repo := &recordingScheduleRepository{claimedFires: []ClaimedScheduleFire{fire}}
	executor := &recordingPipelineExecutor{
		responses: []pipelineExecuteResult{{
			run: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
				RunSlug: fire.FireSlug.String(),
				Source: pipelinerun.RunSourceSummary{
					SourceEventSlug: &sourceEventSlug,
				},
				Status:   "succeeded",
				StepSlug: "fetch-users",
			}},
		}},
	}
	svc := New(testutil.FakeTransactionManager{}, repo, executor, &recordingScheduleFireProducer{}, nil)

	err := svc.ExecuteClaimedScheduleFire(ctx, jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: trigger.OrgSlug,
		ProjectSlug:      trigger.ProjectSlug,
		EnvironmentSlug:  trigger.EnvironmentSlug,
		TriggerSlug:      trigger.TriggerSlug,
		TriggerRevision:  trigger.TriggerRevision,
		ScheduledFireAt:  scheduledAt,
	})

	require.NoError(t, err)
	require.Equal(t, []ScheduleFireExecutionRef{{
		OrgSlug:         trigger.OrgSlug,
		ProjectSlug:     trigger.ProjectSlug,
		EnvironmentSlug: trigger.EnvironmentSlug,
		TriggerSlug:     trigger.TriggerSlug,
		TriggerRevision: trigger.TriggerRevision,
		ScheduledFireAt: scheduledAt,
	}}, repo.loadRefs)
	require.Equal(t, repo.loadRefs, repo.markRefs)
	require.Len(t, executor.calls, 1)
	call := executor.calls[0]
	require.Equal(t, model.SystemEventActor(), call.actor)
	require.Equal(t, trigger.OrgSlug, call.orgSlug)
	require.Equal(t, trigger.ProjectSlug, call.projectSlug)
	require.Equal(t, trigger.EnvironmentSlug, call.environmentSlug)
	require.Equal(t, trigger.PipelineSlug, call.pipelineSlug)
	require.NotNil(t, call.runSlug)
	require.Equal(t, fire.FireSlug, *call.runSlug)
	require.Equal(t, model.PipelineRunSourceTypeSchedule, call.source.Type)
	require.Equal(t, model.PipelineRunSourceEntrypointScheduler, call.source.Entrypoint)
	require.Equal(t, model.EventActorTypeSystem, call.source.ActorType)
	require.Equal(t, trigger.EnvironmentSlug, call.source.EnvironmentSlug)
	require.Equal(t, 1, call.source.AttemptNumber)
	require.Equal(t, fire.FireSlug, call.source.RootRunSlug)
	require.Nil(t, call.source.RetryOfRunSlug)
	require.Equal(t, []string{"customer_id", "dry_run"}, call.source.ParameterKeys)
	require.NotNil(t, call.source.Schedule)
	require.Equal(t, trigger.TriggerSlug, call.source.Schedule.TriggerSlug)
	require.Equal(t, trigger.TriggerRevision, call.source.Schedule.TriggerRevision)
	require.True(t, call.source.Schedule.ScheduledFireAt.Equal(scheduledAt))
	require.True(t, call.source.Schedule.ActualFireAt.Equal(actualAt))
	require.Equal(t, model.PipelineTriggerScheduleMisfireResultOnTime, call.source.Schedule.MisfireResult)
	require.Empty(t, call.runtimeInput)

	require.Len(t, repo.completions, 1)
	completion := repo.completions[0]
	require.Equal(t, trigger.OrgSlug, completion.OrgSlug)
	require.Equal(t, trigger.ProjectSlug, completion.ProjectSlug)
	require.Equal(t, trigger.EnvironmentSlug, completion.EnvironmentSlug)
	require.Equal(t, trigger.TriggerSlug, completion.TriggerSlug)
	require.Equal(t, trigger.TriggerRevision, completion.TriggerRevision)
	require.True(t, completion.ScheduledFireAt.Equal(scheduledAt))
	require.Equal(t, scheduleFireStatusSucceeded, completion.Status)
	require.NotNil(t, completion.RunSlug)
	require.Equal(t, fire.FireSlug.String(), *completion.RunSlug)
	require.NotNil(t, completion.SourceEventSlug)
	require.Equal(t, sourceEventSlug, *completion.SourceEventSlug)
	require.Nil(t, completion.ErrorSummary)
}

func TestExecuteTriggerRetryAttemptCreatesRunWithRetrySourceLinkage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tx := &fakeScheduleDBTX{}
	scheduledAt := time.Date(2026, time.June, 9, 7, 1, 0, 0, time.UTC)
	rootRunSlug := must.Slug("fire-daily-users-20260609")
	retryRunSlug := must.Slug("fire-daily-users-20260609-retry-2")
	attemptSlug := must.Slug("retry-fire-daily-users-20260609-2")
	trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
	source := pipelinerun.RunSourceSummary{
		Type:          model.PipelineRunSourceTypeSchedule,
		Summary:       "Scheduled run from trigger daily-users",
		Entrypoint:    model.PipelineRunSourceEntrypointScheduler,
		ActorType:     model.EventActorTypeSystem,
		ParameterKeys: trigger.RuntimeInputKeys,
		AttemptNumber: 1,
		RootRunSlug:   rootRunSlug,
		Schedule: &pipelinerun.RunSourceScheduleSummary{
			TriggerSlug:     trigger.TriggerSlug,
			TriggerRevision: trigger.TriggerRevision,
			Cron:            trigger.ScheduleConfig.Cron,
			Timezone:        trigger.ScheduleConfig.Timezone,
			ScheduledFireAt: scheduledAt.Add(-time.Minute),
			ActualFireAt:    scheduledAt.Add(-time.Minute),
			MisfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
		},
	}
	repo := &recordingScheduleRepository{
		loadedRetryAttempts: []TriggerRetryAttempt{{
			OrgSlug:                 trigger.OrgSlug,
			ProjectSlug:             trigger.ProjectSlug,
			EnvironmentSlug:         trigger.EnvironmentSlug,
			TriggerSlug:             trigger.TriggerSlug,
			TriggerRevision:         trigger.TriggerRevision,
			PipelineSlug:            trigger.PipelineSlug,
			VersionPolicy:           trigger.VersionPolicy,
			PipelineVersionSpecHash: "spec-hash-123",
			RuntimeInputKeys:        trigger.RuntimeInputKeys,
			RetryPolicy:             trigger.RetryPolicy,
			Source:                  source,
			RootRunSlug:             rootRunSlug,
			RetryOfRunSlug:          rootRunSlug,
			RunSlug:                 retryRunSlug,
			AttemptSlug:             attemptSlug,
			AttemptNumber:           2,
			FailureKind:             model.PipelineTriggerRetryFailureKindDataSource,
			Status:                  model.PipelineTriggerRetryAttemptStatusQueued,
			ScheduledAt:             scheduledAt,
		}},
	}
	executor := &recordingPipelineExecutor{}
	svc := New(
		testutil.FakeTransactionManager{Tx: tx},
		repo,
		executor,
		&recordingScheduleFireProducer{},
		&recordingPipelineRetryProducer{},
	)

	err := svc.ExecuteTriggerRetryAttempt(ctx, jobs.PipelineTriggerRetryExecutionArgs{
		OrganizationSlug: trigger.OrgSlug,
		ProjectSlug:      trigger.ProjectSlug,
		EnvironmentSlug:  trigger.EnvironmentSlug,
		RetryAttemptSlug: attemptSlug,
		ScheduledAt:      scheduledAt,
	})

	require.NoError(t, err)
	require.Equal(t, []slug.Slug{attemptSlug}, repo.runningRetryAttempts)
	require.Len(t, executor.versionCalls, 1)
	call := executor.versionCalls[0]
	require.Equal(t, "spec-hash-123", call.specHash)
	require.Equal(t, trigger.OrgSlug, call.orgSlug)
	require.Equal(t, trigger.ProjectSlug, call.projectSlug)
	require.Equal(t, trigger.EnvironmentSlug, call.environmentSlug)
	require.Equal(t, trigger.PipelineSlug, call.pipelineSlug)
	require.NotNil(t, call.runSlug)
	require.Equal(t, retryRunSlug, *call.runSlug)
	require.Equal(t, model.SystemEventActor(), call.actor)
	require.Equal(t, model.PipelineRunSourceTypeSchedule, call.source.Type)
	require.Equal(t, trigger.EnvironmentSlug, call.source.EnvironmentSlug)
	require.Equal(t, 2, call.source.AttemptNumber)
	require.Equal(t, rootRunSlug, call.source.RootRunSlug)
	require.NotNil(t, call.source.RetryOfRunSlug)
	require.Equal(t, rootRunSlug, *call.source.RetryOfRunSlug)
	require.NotNil(t, call.source.Schedule)
	require.Equal(t, trigger.TriggerSlug, call.source.Schedule.TriggerSlug)
	require.Empty(t, call.runtimeInput)
	require.Len(t, repo.completedRetryAttempts, 1)
	completion := repo.completedRetryAttempts[0]
	require.Equal(t, attemptSlug, completion.attemptSlug)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusSucceeded, completion.status)
	require.Same(t, tx, completion.tx)
	require.Empty(t, repo.retryAttempts)
}

func TestExecuteTriggerRetryAttemptFailedRunRecordsRetryDecisionNotificationMatrix(t *testing.T) {
	t.Parallel()

	rootRunSlug := must.Slug("fire-daily-users-20260609")
	sourceEventSlug := must.Slug("evt-retry-run")
	type expectedNotification struct {
		retryOfRunSlug      slug.Slug
		retryAttemptSlug    string
		decisionReason      string
		attemptNumber       int
		notificationFailure bool
	}

	tests := []struct {
		name                 string
		currentAttemptNumber int
		retryOfRunSlug       slug.Slug
		retryRunSlug         slug.Slug
		sourceEventSlug      slug.Slug
		failureKind          string
		executionError       error
		wantStatus           model.PipelineTriggerRetryAttemptStatus
		wantRetryProducer    bool
		wantNotification     *expectedNotification
	}{
		{
			name:                 "QueuedRetryDoesNotNotify",
			currentAttemptNumber: 2,
			retryOfRunSlug:       rootRunSlug,
			retryRunSlug:         must.Slug("fire-daily-users-20260609-retry-2"),
			sourceEventSlug:      sourceEventSlug,
			failureKind:          "data_source",
			wantStatus:           model.PipelineTriggerRetryAttemptStatusQueued,
			wantRetryProducer:    true,
		},
		{
			name:                 "ExhaustedRetryNotifies",
			currentAttemptNumber: 3,
			retryOfRunSlug:       must.Slug("fire-daily-users-20260609-retry-2"),
			retryRunSlug:         must.Slug("fire-daily-users-20260609-retry-3"),
			sourceEventSlug:      sourceEventSlug,
			failureKind:          "data_source",
			wantStatus:           model.PipelineTriggerRetryAttemptStatusExhausted,
			wantNotification: &expectedNotification{
				retryOfRunSlug:   must.Slug("fire-daily-users-20260609-retry-3"),
				retryAttemptSlug: "retry-fire-daily-users-20260609-4",
				decisionReason:   "max_attempts_exhausted",
				attemptNumber:    4,
			},
		},
		{
			name:                 "SkippedNonRetryableFailureNotifies",
			currentAttemptNumber: 2,
			retryOfRunSlug:       rootRunSlug,
			retryRunSlug:         must.Slug("fire-daily-users-20260609-retry-2"),
			sourceEventSlug:      sourceEventSlug,
			failureKind:          "contract",
			wantStatus:           model.PipelineTriggerRetryAttemptStatusSkipped,
			wantNotification: &expectedNotification{
				retryOfRunSlug:   must.Slug("fire-daily-users-20260609-retry-2"),
				retryAttemptSlug: "retry-fire-daily-users-20260609-3",
				decisionReason:   "non_retryable_failure_kind",
				attemptNumber:    3,
			},
		},
		{
			name:                 "SkippedRowLimitFailureNotifies",
			currentAttemptNumber: 2,
			retryOfRunSlug:       rootRunSlug,
			retryRunSlug:         must.Slug("fire-daily-users-20260609-retry-2"),
			sourceEventSlug:      sourceEventSlug,
			failureKind:          "data_source",
			executionError:       xerror.Wrapf("%w: %w", pipelinesvc.ErrPipelineExecutionFailed, xerror.ExecutionRowLimitExceeded()),
			wantStatus:           model.PipelineTriggerRetryAttemptStatusSkipped,
			wantNotification: &expectedNotification{
				retryOfRunSlug:   must.Slug("fire-daily-users-20260609-retry-2"),
				retryAttemptSlug: "retry-fire-daily-users-20260609-3",
				decisionReason:   "execution_response_limit_exceeded",
				attemptNumber:    3,
			},
		},
		{
			name:                 "SkippedResponseByteLimitFailureNotifies",
			currentAttemptNumber: 2,
			retryOfRunSlug:       rootRunSlug,
			retryRunSlug:         must.Slug("fire-daily-users-20260609-retry-2"),
			sourceEventSlug:      sourceEventSlug,
			failureKind:          "data_source",
			executionError:       xerror.Wrapf("%w: %w", pipelinesvc.ErrPipelineExecutionFailed, xerror.ExecutionResponseSizeExceeded()),
			wantStatus:           model.PipelineTriggerRetryAttemptStatusSkipped,
			wantNotification: &expectedNotification{
				retryOfRunSlug:   must.Slug("fire-daily-users-20260609-retry-2"),
				retryAttemptSlug: "retry-fire-daily-users-20260609-3",
				decisionReason:   "execution_response_limit_exceeded",
				attemptNumber:    3,
			},
		},
		{
			name:                 "NotificationProducerErrorDoesNotFailRetryCompletion",
			currentAttemptNumber: 3,
			retryOfRunSlug:       must.Slug("fire-daily-users-20260609-retry-2"),
			retryRunSlug:         must.Slug("fire-daily-users-20260609-retry-3"),
			sourceEventSlug:      sourceEventSlug,
			failureKind:          "data_source",
			wantStatus:           model.PipelineTriggerRetryAttemptStatusExhausted,
			wantNotification: &expectedNotification{
				retryOfRunSlug:      must.Slug("fire-daily-users-20260609-retry-3"),
				retryAttemptSlug:    "retry-fire-daily-users-20260609-4",
				decisionReason:      "max_attempts_exhausted",
				attemptNumber:       4,
				notificationFailure: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			tx := &fakeScheduleDBTX{}
			scheduledAt := time.Date(2026, time.June, 9, 7, tc.currentAttemptNumber, 0, 0, time.UTC)
			trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
			trigger.RetryPolicy = enabledRetryPolicy(3, model.PipelineTriggerRetryFailureKindDataSource)
			source := pipelinerun.RunSourceSummary{
				Type:           model.PipelineRunSourceTypeSchedule,
				Summary:        "Scheduled run from trigger daily-users",
				Entrypoint:     model.PipelineRunSourceEntrypointScheduler,
				ActorType:      model.EventActorTypeSystem,
				ParameterKeys:  trigger.RuntimeInputKeys,
				AttemptNumber:  tc.currentAttemptNumber,
				RootRunSlug:    rootRunSlug,
				RetryOfRunSlug: &tc.retryOfRunSlug,
			}
			attemptSlug := must.Slug("retry-fire-daily-users-20260609-" + strconv.Itoa(tc.currentAttemptNumber))
			repo := &recordingScheduleRepository{
				loadedRetryAttempts: []TriggerRetryAttempt{{
					OrgSlug:                 trigger.OrgSlug,
					ProjectSlug:             trigger.ProjectSlug,
					EnvironmentSlug:         trigger.EnvironmentSlug,
					TriggerSlug:             trigger.TriggerSlug,
					TriggerRevision:         trigger.TriggerRevision,
					PipelineSlug:            trigger.PipelineSlug,
					VersionPolicy:           trigger.VersionPolicy,
					PipelineVersionSpecHash: "spec-hash-123",
					RuntimeInputKeys:        trigger.RuntimeInputKeys,
					RetryPolicy:             trigger.RetryPolicy,
					Source:                  source,
					RootRunSlug:             rootRunSlug,
					RetryOfRunSlug:          tc.retryOfRunSlug,
					RunSlug:                 tc.retryRunSlug,
					AttemptSlug:             attemptSlug,
					AttemptNumber:           tc.currentAttemptNumber,
					FailureKind:             model.PipelineTriggerRetryFailureKindDataSource,
					Status:                  model.PipelineTriggerRetryAttemptStatusQueued,
					ScheduledAt:             scheduledAt,
				}},
			}
			executionError := tc.executionError
			if executionError == nil {
				executionError = xerror.Unexpected().Detail("step failed")
			}
			executor := &recordingPipelineExecutor{
				responses: []pipelineExecuteResult{{
					run: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
						RunSlug:                 tc.retryRunSlug.String(),
						PipelineVersionSpecHash: "spec-hash-123",
						Source: pipelinerun.RunSourceSummary{
							AttemptNumber:   tc.currentAttemptNumber,
							RootRunSlug:     rootRunSlug,
							RetryOfRunSlug:  &tc.retryOfRunSlug,
							SourceEventSlug: &tc.sourceEventSlug,
						},
						Status:      "failed",
						StepSlug:    "fetch-users",
						FailureKind: stringPtr(tc.failureKind),
					}},
					err: executionError,
				}},
			}
			retryProducer := &recordingPipelineRetryProducer{}
			notifier := &recordingNotificationProducer{}
			if tc.wantNotification != nil && tc.wantNotification.notificationFailure {
				notifier.err = xerror.Unexpected().Detail("notification service unavailable")
			}
			svc := New(
				testutil.FakeTransactionManager{Tx: tx},
				repo,
				executor,
				&recordingScheduleFireProducer{},
				retryProducer,
				WithNotificationProducer(notifier),
			)

			err := svc.ExecuteTriggerRetryAttempt(ctx, jobs.PipelineTriggerRetryExecutionArgs{
				OrganizationSlug: trigger.OrgSlug,
				ProjectSlug:      trigger.ProjectSlug,
				EnvironmentSlug:  trigger.EnvironmentSlug,
				RetryAttemptSlug: attemptSlug,
				ScheduledAt:      scheduledAt,
			})

			if tc.wantNotification != nil && tc.wantNotification.notificationFailure {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, repo.completedRetryAttempts, 1)
			require.Same(t, tx, repo.completedRetryAttempts[0].tx)
			require.Equal(t, model.PipelineTriggerRetryAttemptStatusFailed, repo.completedRetryAttempts[0].status)
			require.NotNil(t, repo.completedRetryAttempts[0].sourceEventSlug)
			require.Equal(t, tc.sourceEventSlug, *repo.completedRetryAttempts[0].sourceEventSlug)
			require.Len(t, repo.retryAttempts, 1)
			followUp := repo.retryAttempts[0]
			require.Same(t, tx, followUp.tx)
			require.Equal(t, tc.wantStatus, followUp.record.Status)
			require.Equal(t, rootRunSlug, followUp.record.RootRunSlug)
			require.Equal(t, tc.retryRunSlug, followUp.record.RetryOfRunSlug)
			require.Equal(t, tc.currentAttemptNumber+1, followUp.record.AttemptNumber)

			if tc.wantRetryProducer {
				require.Len(t, retryProducer.calls, 1)
				require.Same(t, tx, retryProducer.calls[0].tx)
				require.True(t, followUp.record.ScheduledAt.Equal(scheduledAt.Add(time.Minute)))
			} else {
				require.Empty(t, retryProducer.calls)
			}

			if tc.wantNotification == nil {
				require.Empty(t, notifier.calls)
				return
			}

			require.Len(t, notifier.calls, 2)
			require.Equal(t, model.NotificationTypePipelineRunFailed, notifier.calls[0].NotificationType)
			candidate := notifier.calls[1]
			require.Equal(t, model.NotificationTypePipelineRetryExhausted, candidate.NotificationType)
			require.Equal(t, "pipeline.retry_exhausted:default:prod:daily-users:7:fire-daily-users-20260609", candidate.DedupeFingerprint)
			require.Equal(t, tc.wantNotification.retryOfRunSlug.String(), candidate.SourceRefs["run_slug"])
			require.Equal(t, rootRunSlug.String(), candidate.SourceRefs["root_run_slug"])
			require.Equal(t, tc.wantNotification.retryOfRunSlug.String(), candidate.SourceRefs["retry_of_run_slug"])
			require.Equal(t, tc.wantNotification.retryAttemptSlug, candidate.SourceRefs["retry_attempt_slug"])
			require.NotContains(t, candidate.SourceRefs, "retry_run_slug")
			require.Equal(t, tc.wantNotification.decisionReason, candidate.SafeMetadata["retry_decision_reason"])
			require.Equal(t, tc.wantNotification.attemptNumber, candidate.SafeMetadata["attempt_number"])
		})
	}
}

func TestExecuteTriggerRetryAttemptReturnsNilWhenMarkRunningLosesClaim(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scheduledAt := time.Date(2026, time.June, 9, 7, 1, 0, 0, time.UTC)
	attemptSlug := must.Slug("retry-fire-daily-users-20260609-2")
	trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
	repo := &recordingScheduleRepository{
		loadedRetryAttempts: []TriggerRetryAttempt{{
			OrgSlug:         trigger.OrgSlug,
			ProjectSlug:     trigger.ProjectSlug,
			EnvironmentSlug: trigger.EnvironmentSlug,
			TriggerSlug:     trigger.TriggerSlug,
			PipelineSlug:    trigger.PipelineSlug,
			AttemptSlug:     attemptSlug,
		}},
		markRetryAttemptRunningErr: pipelinetriggers.ErrPipelineTriggerNotFound,
	}
	executor := &recordingPipelineExecutor{}
	svc := New(
		testutil.FakeTransactionManager{Tx: &fakeScheduleDBTX{}},
		repo,
		executor,
		&recordingScheduleFireProducer{},
		&recordingPipelineRetryProducer{},
	)

	err := svc.ExecuteTriggerRetryAttempt(ctx, jobs.PipelineTriggerRetryExecutionArgs{
		OrganizationSlug: trigger.OrgSlug,
		ProjectSlug:      trigger.ProjectSlug,
		EnvironmentSlug:  trigger.EnvironmentSlug,
		RetryAttemptSlug: attemptSlug,
		ScheduledAt:      scheduledAt,
	})

	require.NoError(t, err)
	require.Equal(t, []slug.Slug{attemptSlug}, repo.runningRetryAttempts)
	require.Empty(t, executor.versionCalls)
	require.Empty(t, repo.completedRetryAttempts)
}

func TestCompleteRetryAttemptTxMarksFailedWhenExecutionErrorHasNoRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tx := &fakeScheduleDBTX{}
	repo := &recordingScheduleRepository{}
	svc := &Service{repo: repo, executor: &recordingPipelineExecutor{}}
	runErr := xerror.Unexpected().Detail("runtime unavailable")
	attempt := TriggerRetryAttempt{
		OrgSlug:         must.Slug("acme"),
		ProjectSlug:     must.Slug("default"),
		EnvironmentSlug: must.Slug("prod"),
		AttemptSlug:     must.Slug("retry-fire-daily-users-20260609-2"),
	}

	err := svc.completeRetryAttemptTx(ctx, tx, attempt, nil, runErr)

	require.NoError(t, err)
	require.Len(t, repo.completedRetryAttempts, 1)
	call := repo.completedRetryAttempts[0]
	require.Same(t, tx, call.tx)
	require.Equal(t, attempt.AttemptSlug, call.attemptSlug)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusFailed, call.status)
	require.Nil(t, call.sourceEventSlug)
	require.NotNil(t, call.errorSummary)
	require.Contains(t, *call.errorSummary, "runtime unavailable")
}

func TestExecuteClaimedScheduleFireHonorsPinnedVersionPolicy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scheduledAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
	pinnedHash := "pinned-version-hash"
	trigger.VersionPolicy = model.PipelineTriggerVersionPolicy{
		Mode:              model.PipelineTriggerVersionPolicyModePinned,
		PinnedVersionHash: &pinnedHash,
	}
	fire := ClaimedScheduleFire{
		Trigger: trigger,
		Claim: ScheduleFireClaim{
			OrgSlug:         trigger.OrgSlug,
			ProjectSlug:     trigger.ProjectSlug,
			EnvironmentSlug: trigger.EnvironmentSlug,
			TriggerSlug:     trigger.TriggerSlug,
			TriggerRevision: trigger.TriggerRevision,
			ScheduledFireAt: scheduledAt,
			ActualFireAt:    scheduledAt,
			MisfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
		},
		FireSlug: must.Slug("fire-daily-users-20260609"),
	}
	repo := &recordingScheduleRepository{claimedFires: []ClaimedScheduleFire{fire}}
	executor := &recordingPipelineExecutor{}
	svc := New(testutil.FakeTransactionManager{}, repo, executor, &recordingScheduleFireProducer{}, nil)

	err := svc.ExecuteClaimedScheduleFire(ctx, jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: trigger.OrgSlug,
		ProjectSlug:      trigger.ProjectSlug,
		EnvironmentSlug:  trigger.EnvironmentSlug,
		TriggerSlug:      trigger.TriggerSlug,
		TriggerRevision:  trigger.TriggerRevision,
		ScheduledFireAt:  scheduledAt,
	})

	require.NoError(t, err)
	require.Empty(t, executor.calls)
	require.Len(t, executor.versionCalls, 1)
	require.Equal(t, pinnedHash, executor.versionCalls[0].specHash)
	require.Equal(t, trigger.PipelineSlug, executor.versionCalls[0].pipelineSlug)
	require.NotNil(t, executor.versionCalls[0].runSlug)
	require.Equal(t, fire.FireSlug, *executor.versionCalls[0].runSlug)
	require.Len(t, repo.completions, 1)
	require.Equal(t, scheduleFireStatusSucceeded, repo.completions[0].Status)
}

func TestExecuteClaimedScheduleFireTreatsMissingClaimedFireAsAlreadyHandled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scheduledAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
	repo := &recordingScheduleRepository{}
	executor := &recordingPipelineExecutor{}
	svc := New(testutil.FakeTransactionManager{}, repo, executor, &recordingScheduleFireProducer{}, nil)

	err := svc.ExecuteClaimedScheduleFire(ctx, jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: trigger.OrgSlug,
		ProjectSlug:      trigger.ProjectSlug,
		EnvironmentSlug:  trigger.EnvironmentSlug,
		TriggerSlug:      trigger.TriggerSlug,
		TriggerRevision:  trigger.TriggerRevision,
		ScheduledFireAt:  scheduledAt,
	})

	require.NoError(t, err)
	require.Equal(t, []ScheduleFireExecutionRef{{
		OrgSlug:         trigger.OrgSlug,
		ProjectSlug:     trigger.ProjectSlug,
		EnvironmentSlug: trigger.EnvironmentSlug,
		TriggerSlug:     trigger.TriggerSlug,
		TriggerRevision: trigger.TriggerRevision,
		ScheduledFireAt: scheduledAt,
	}}, repo.loadRefs)
	require.Empty(t, executor.calls)
	require.Empty(t, executor.versionCalls)
	require.Empty(t, repo.completions)
}

func TestExecuteClaimedScheduleFireTreatsLostRunningClaimAsAlreadyHandled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scheduledAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
	fire := ClaimedScheduleFire{
		Trigger: trigger,
		Claim: ScheduleFireClaim{
			OrgSlug:         trigger.OrgSlug,
			ProjectSlug:     trigger.ProjectSlug,
			EnvironmentSlug: trigger.EnvironmentSlug,
			TriggerSlug:     trigger.TriggerSlug,
			TriggerRevision: trigger.TriggerRevision,
			ScheduledFireAt: scheduledAt,
			ActualFireAt:    scheduledAt,
			MisfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
		},
		FireSlug: must.Slug("fire-daily-users-20260609"),
	}
	repo := &recordingScheduleRepository{
		claimedFires:        []ClaimedScheduleFire{fire},
		markScheduleFireErr: pipelinetriggers.ErrPipelineTriggerNotFound,
	}
	executor := &recordingPipelineExecutor{}
	svc := New(testutil.FakeTransactionManager{}, repo, executor, &recordingScheduleFireProducer{}, nil)

	err := svc.ExecuteClaimedScheduleFire(ctx, jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: trigger.OrgSlug,
		ProjectSlug:      trigger.ProjectSlug,
		EnvironmentSlug:  trigger.EnvironmentSlug,
		TriggerSlug:      trigger.TriggerSlug,
		TriggerRevision:  trigger.TriggerRevision,
		ScheduledFireAt:  scheduledAt,
	})

	require.NoError(t, err)
	expectedRef := ScheduleFireExecutionRef{
		OrgSlug:         trigger.OrgSlug,
		ProjectSlug:     trigger.ProjectSlug,
		EnvironmentSlug: trigger.EnvironmentSlug,
		TriggerSlug:     trigger.TriggerSlug,
		TriggerRevision: trigger.TriggerRevision,
		ScheduledFireAt: scheduledAt,
	}
	require.Equal(t, []ScheduleFireExecutionRef{expectedRef}, repo.loadRefs)
	require.Equal(t, []ScheduleFireExecutionRef{expectedRef}, repo.markRefs)
	require.Empty(t, executor.calls)
	require.Empty(t, executor.versionCalls)
	require.Empty(t, repo.completions)
}

func TestExecuteClaimedScheduleFireCompletesFailedFireAndPreservesExecutionErrorBehavior(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scheduledAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
	fire := ClaimedScheduleFire{
		Trigger: trigger,
		Claim: ScheduleFireClaim{
			OrgSlug:         trigger.OrgSlug,
			ProjectSlug:     trigger.ProjectSlug,
			EnvironmentSlug: trigger.EnvironmentSlug,
			TriggerSlug:     trigger.TriggerSlug,
			TriggerRevision: trigger.TriggerRevision,
			ScheduledFireAt: scheduledAt,
			ActualFireAt:    scheduledAt,
			MisfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
		},
		FireSlug: must.Slug("fire-daily-users-20260609"),
	}

	t.Run("ReturnsExecutionErrorWhenNoRunWasPersisted", func(t *testing.T) {
		t.Parallel()

		executionErr := xerror.Unexpected().Detail("runtime unavailable")
		repo := &recordingScheduleRepository{claimedFires: []ClaimedScheduleFire{fire}}
		executor := &recordingPipelineExecutor{
			responses: []pipelineExecuteResult{{err: executionErr}},
		}
		notifier := &recordingNotificationProducer{}
		svc := New(testutil.FakeTransactionManager{}, repo, executor, &recordingScheduleFireProducer{}, nil, WithNotificationProducer(notifier))

		err := svc.ExecuteClaimedScheduleFire(ctx, jobs.PipelineScheduleFireExecutionArgs{
			OrganizationSlug: trigger.OrgSlug,
			ProjectSlug:      trigger.ProjectSlug,
			EnvironmentSlug:  trigger.EnvironmentSlug,
			TriggerSlug:      trigger.TriggerSlug,
			TriggerRevision:  trigger.TriggerRevision,
			ScheduledFireAt:  scheduledAt,
		})

		require.ErrorIs(t, err, executionErr)
		require.Len(t, repo.completions, 1)
		require.Equal(t, scheduleFireStatusFailed, repo.completions[0].Status)
		require.Nil(t, repo.completions[0].RunSlug)
		require.Nil(t, repo.completions[0].SourceEventSlug)
		require.NotNil(t, repo.completions[0].ErrorSummary)
		require.Contains(t, *repo.completions[0].ErrorSummary, "runtime unavailable")
		require.Len(t, notifier.calls, 1)
		require.Equal(t, model.NotificationTypePipelineScheduledRunMissing, notifier.calls[0].NotificationType)
		require.NotContains(t, notifier.calls[0].SafeMetadata["summary"], "runtime unavailable")
	})

	t.Run("TreatsPersistedFailedRunAsCompletedWorkForRiver", func(t *testing.T) {
		t.Parallel()

		executionErr := xerror.Unexpected().Detail("step failed")
		sourceEventSlug := must.Slug("evt-failed-run")
		repo := &recordingScheduleRepository{claimedFires: []ClaimedScheduleFire{fire}}
		executor := &recordingPipelineExecutor{
			responses: []pipelineExecuteResult{{
				run: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
					RunSlug: "run-sync-users",
					Source: pipelinerun.RunSourceSummary{
						SourceEventSlug: &sourceEventSlug,
					},
					Status:   "failed",
					StepSlug: "fetch-users",
				}},
				err: executionErr,
			}},
		}
		svc := New(testutil.FakeTransactionManager{}, repo, executor, &recordingScheduleFireProducer{}, nil)

		err := svc.ExecuteClaimedScheduleFire(ctx, jobs.PipelineScheduleFireExecutionArgs{
			OrganizationSlug: trigger.OrgSlug,
			ProjectSlug:      trigger.ProjectSlug,
			EnvironmentSlug:  trigger.EnvironmentSlug,
			TriggerSlug:      trigger.TriggerSlug,
			TriggerRevision:  trigger.TriggerRevision,
			ScheduledFireAt:  scheduledAt,
		})

		require.NoError(t, err)
		require.Len(t, repo.completions, 1)
		require.Equal(t, scheduleFireStatusFailed, repo.completions[0].Status)
		require.NotNil(t, repo.completions[0].RunSlug)
		require.Equal(t, "run-sync-users", *repo.completions[0].RunSlug)
		require.NotNil(t, repo.completions[0].SourceEventSlug)
		require.Equal(t, sourceEventSlug, *repo.completions[0].SourceEventSlug)
		require.Nil(t, repo.completions[0].ErrorSummary)
	})
}

func TestExecuteClaimedScheduleFireQueuesRetryForRetryableFailedRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tx := &fakeScheduleDBTX{}
	scheduledAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	retryAt := scheduledAt.Add(time.Minute)
	sourceEventSlug := must.Slug("evt-original-schedule-run")
	trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
	trigger.RetryPolicy = enabledRetryPolicy(
		3,
		model.PipelineTriggerRetryFailureKindDataSource,
	)
	fire := ClaimedScheduleFire{
		Trigger: trigger,
		Claim: ScheduleFireClaim{
			OrgSlug:         trigger.OrgSlug,
			ProjectSlug:     trigger.ProjectSlug,
			EnvironmentSlug: trigger.EnvironmentSlug,
			TriggerSlug:     trigger.TriggerSlug,
			TriggerRevision: trigger.TriggerRevision,
			ScheduledFireAt: scheduledAt,
			ActualFireAt:    scheduledAt,
			MisfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
		},
		FireSlug: must.Slug("fire-daily-users-20260609"),
	}
	repo := &recordingScheduleRepository{
		claimedFires: []ClaimedScheduleFire{fire},
		retryAttemptResults: []TriggerRetryAttempt{{
			OrgSlug:         trigger.OrgSlug,
			ProjectSlug:     trigger.ProjectSlug,
			EnvironmentSlug: trigger.EnvironmentSlug,
			TriggerSlug:     trigger.TriggerSlug,
			TriggerRevision: trigger.TriggerRevision,
			PipelineSlug:    trigger.PipelineSlug,
			AttemptSlug:     must.Slug("retry-fire-daily-users-20260609-2"),
			RunSlug:         must.Slug("run-fire-daily-users-20260609-retry-2"),
			ScheduledAt:     retryAt,
			Status:          model.PipelineTriggerRetryAttemptStatusQueued,
		}},
	}
	executionErr := xerror.Unexpected().Detail("step failed")
	executor := &recordingPipelineExecutor{
		responses: []pipelineExecuteResult{{
			run: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
				RunSlug: "fire-daily-users-20260609",
				Source: pipelinerun.RunSourceSummary{
					AttemptNumber:   1,
					RootRunSlug:     fire.FireSlug,
					SourceEventSlug: &sourceEventSlug,
				},
				Status:      "failed",
				StepSlug:    "fetch-users",
				FailureKind: stringPtr("data_source"),
			}},
			err: executionErr,
		}},
	}
	retryProducer := &recordingPipelineRetryProducer{}
	notifier := &recordingNotificationProducer{}
	svc := New(
		testutil.FakeTransactionManager{Tx: tx},
		repo,
		executor,
		&recordingScheduleFireProducer{},
		retryProducer,
		WithNotificationProducer(notifier),
	)

	err := svc.ExecuteClaimedScheduleFire(ctx, jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: trigger.OrgSlug,
		ProjectSlug:      trigger.ProjectSlug,
		EnvironmentSlug:  trigger.EnvironmentSlug,
		TriggerSlug:      trigger.TriggerSlug,
		TriggerRevision:  trigger.TriggerRevision,
		ScheduledFireAt:  scheduledAt,
	})

	require.NoError(t, err)
	require.Len(t, repo.completions, 1)
	require.Equal(t, scheduleFireStatusFailed, repo.completions[0].Status)
	require.Len(t, repo.retryAttempts, 1)
	attempt := repo.retryAttempts[0]
	require.Same(t, tx, attempt.tx)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusQueued, attempt.record.Status)
	require.Equal(t, trigger.OrgSlug, attempt.record.OrgSlug)
	require.Equal(t, trigger.ProjectSlug, attempt.record.ProjectSlug)
	require.Equal(t, trigger.EnvironmentSlug, attempt.record.EnvironmentSlug)
	require.Equal(t, trigger.TriggerSlug, attempt.record.TriggerSlug)
	require.Equal(t, trigger.TriggerRevision, attempt.record.TriggerRevision)
	require.Equal(t, fire.FireSlug, attempt.record.RootRunSlug)
	require.Equal(t, fire.FireSlug, attempt.record.RetryOfRunSlug)
	require.Equal(t, 2, attempt.record.AttemptNumber)
	require.Equal(t, model.PipelineTriggerRetryFailureKindDataSource, attempt.record.FailureKind)
	require.NotNil(t, attempt.record.SourceEventSlug)
	require.Equal(t, sourceEventSlug, *attempt.record.SourceEventSlug)
	require.True(t, attempt.record.ScheduledAt.Equal(retryAt))
	require.Empty(t, attempt.record.DecisionReason)
	require.Len(t, retryProducer.calls, 1)
	require.Same(t, tx, retryProducer.calls[0].tx)
	require.Equal(t, jobs.PipelineTriggerRetryExecutionArgs{
		OrganizationSlug: trigger.OrgSlug,
		ProjectSlug:      trigger.ProjectSlug,
		EnvironmentSlug:  trigger.EnvironmentSlug,
		RetryAttemptSlug: must.Slug("retry-fire-daily-users-20260609-2"),
		ScheduledAt:      retryAt,
	}, retryProducer.calls[0].args)
	require.Empty(t, notifier.calls)
}

func TestExecuteClaimedScheduleFireRecordsSkippedRetryForNonRetryableFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		failureKind    string
		executionError error
		wantKind       model.PipelineTriggerRetryFailureKind
		wantReason     string
	}{
		{"unknown failure", "contract", xerror.Unexpected(), model.PipelineTriggerRetryFailureKindUnknown, "non_retryable_failure_kind"},
		{"row limit", "data_source", xerror.ExecutionRowLimitExceeded(), model.PipelineTriggerRetryFailureKindDataSource, "execution_response_limit_exceeded"},
		{"response byte limit", "data_source", xerror.ExecutionResponseSizeExceeded(), model.PipelineTriggerRetryFailureKindDataSource, "execution_response_limit_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			scheduledAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
			sourceEventSlug := must.Slug("evt-original-schedule-run")
			trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
			trigger.RetryPolicy = enabledRetryPolicy(
				3,
				model.PipelineTriggerRetryFailureKindDataSource,
			)
			fire := ClaimedScheduleFire{
				Trigger: trigger,
				Claim: ScheduleFireClaim{
					OrgSlug:         trigger.OrgSlug,
					ProjectSlug:     trigger.ProjectSlug,
					EnvironmentSlug: trigger.EnvironmentSlug,
					TriggerSlug:     trigger.TriggerSlug,
					TriggerRevision: trigger.TriggerRevision,
					ScheduledFireAt: scheduledAt,
					ActualFireAt:    scheduledAt,
					MisfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
				},
				FireSlug: must.Slug("fire-daily-users-20260609"),
			}
			repo := &recordingScheduleRepository{claimedFires: []ClaimedScheduleFire{fire}}
			executor := &recordingPipelineExecutor{
				responses: []pipelineExecuteResult{{
					run: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
						RunSlug: "fire-daily-users-20260609",
						Source: pipelinerun.RunSourceSummary{
							AttemptNumber:   1,
							RootRunSlug:     fire.FireSlug,
							SourceEventSlug: &sourceEventSlug,
						},
						Status:      "failed",
						StepSlug:    "validate-contract",
						FailureKind: stringPtr(tc.failureKind),
					}},
					err: xerror.Wrapf("%w: %w", pipelinesvc.ErrPipelineExecutionFailed, tc.executionError),
				}},
			}
			retryProducer := &recordingPipelineRetryProducer{}
			notifier := &recordingNotificationProducer{}
			svc := New(
				testutil.FakeTransactionManager{},
				repo,
				executor,
				&recordingScheduleFireProducer{},
				retryProducer,
				WithNotificationProducer(notifier),
			)

			err := svc.ExecuteClaimedScheduleFire(ctx, jobs.PipelineScheduleFireExecutionArgs{
				OrganizationSlug: trigger.OrgSlug,
				ProjectSlug:      trigger.ProjectSlug,
				EnvironmentSlug:  trigger.EnvironmentSlug,
				TriggerSlug:      trigger.TriggerSlug,
				TriggerRevision:  trigger.TriggerRevision,
				ScheduledFireAt:  scheduledAt,
			})

			require.NoError(t, err)
			require.Len(t, repo.retryAttempts, 1)
			attempt := repo.retryAttempts[0].record
			require.Equal(t, model.PipelineTriggerRetryAttemptStatusSkipped, attempt.Status)
			require.Equal(t, fire.FireSlug, attempt.RootRunSlug)
			require.Equal(t, fire.FireSlug, attempt.RetryOfRunSlug)
			require.Equal(t, 2, attempt.AttemptNumber)
			require.Equal(t, tc.wantKind, attempt.FailureKind)
			require.Equal(t, tc.wantReason, attempt.DecisionReason)
			require.Empty(t, retryProducer.calls)
			require.Len(t, notifier.calls, 2)
			require.Equal(t, model.NotificationTypePipelineRunFailed, notifier.calls[0].NotificationType)
			candidate := notifier.calls[1]
			require.Equal(t, trigger.OrgSlug, candidate.OrgSlug)
			require.NotNil(t, candidate.ProjectSlug)
			require.Equal(t, trigger.ProjectSlug, *candidate.ProjectSlug)
			require.NotNil(t, candidate.EnvironmentSlug)
			require.Equal(t, trigger.EnvironmentSlug, *candidate.EnvironmentSlug)
			require.Equal(t, model.NotificationTypePipelineRetryExhausted, candidate.NotificationType)
			require.Equal(t, model.NotificationSeverityWarning, candidate.Severity)
			require.Equal(t, "pipeline.retry_exhausted:default:prod:daily-users:7:fire-daily-users-20260609", candidate.DedupeFingerprint)
			require.NotNil(t, candidate.SourceEventSlug)
			require.Equal(t, sourceEventSlug, *candidate.SourceEventSlug)
			require.Equal(t, model.EventEntityTypePipelineRun, candidate.SourceEntityType)
			require.Equal(t, model.EventActionExecute, candidate.SourceAction)
			require.Equal(t, map[string]string{
				"project_slug":       "default",
				"environment_slug":   "prod",
				"pipeline_slug":      "sync-users",
				"run_slug":           "fire-daily-users-20260609",
				"root_run_slug":      "fire-daily-users-20260609",
				"retry_of_run_slug":  "fire-daily-users-20260609",
				"retry_attempt_slug": "retry-fire-daily-users-20260609-2",
				"trigger_slug":       "daily-users",
				"trigger_revision":   "7",
			}, candidate.SourceRefs)
			require.NotContains(t, candidate.SafeMetadata, "retry_run_slug")
			require.Equal(t, "Pipeline sync-users will not retry run fire-daily-users-20260609", candidate.SafeMetadata["summary"])
			require.Equal(t, "skipped", candidate.SafeMetadata["retry_status"])
			require.Equal(t, tc.wantReason, candidate.SafeMetadata["retry_decision_reason"])
			require.Equal(t, tc.wantKind.String(), candidate.SafeMetadata["failure_kind"])
			require.Equal(t, 2, candidate.SafeMetadata["attempt_number"])
			require.Equal(t, 3, candidate.SafeMetadata["max_attempts"])
		})
	}
}

func TestExecuteClaimedScheduleFireRecordsExhaustedRetryWhenPolicyAttemptsUsed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	scheduledAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	rootRunSlug := must.Slug("fire-daily-users-20260609")
	sourceEventSlug := must.Slug("evt-original-schedule-run")
	trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
	trigger.RetryPolicy = enabledRetryPolicy(
		3,
		model.PipelineTriggerRetryFailureKindDataSource,
	)
	fire := ClaimedScheduleFire{
		Trigger: trigger,
		Claim: ScheduleFireClaim{
			OrgSlug:         trigger.OrgSlug,
			ProjectSlug:     trigger.ProjectSlug,
			EnvironmentSlug: trigger.EnvironmentSlug,
			TriggerSlug:     trigger.TriggerSlug,
			TriggerRevision: trigger.TriggerRevision,
			ScheduledFireAt: scheduledAt,
			ActualFireAt:    scheduledAt,
			MisfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
		},
		FireSlug: rootRunSlug,
	}
	repo := &recordingScheduleRepository{claimedFires: []ClaimedScheduleFire{fire}}
	executor := &recordingPipelineExecutor{
		responses: []pipelineExecuteResult{{
			run: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
				RunSlug: "run-fire-daily-users-20260609-retry-3",
				Source: pipelinerun.RunSourceSummary{
					AttemptNumber:   3,
					RootRunSlug:     rootRunSlug,
					RetryOfRunSlug:  slugPtr(must.Slug("run-fire-daily-users-20260609-retry-2")),
					SourceEventSlug: &sourceEventSlug,
				},
				Status:      "failed",
				StepSlug:    "fetch-users",
				FailureKind: stringPtr("data_source"),
			}},
			err: xerror.Unexpected().Detail("upstream unavailable"),
		}},
	}
	retryProducer := &recordingPipelineRetryProducer{}
	notifier := &recordingNotificationProducer{}
	svc := New(
		testutil.FakeTransactionManager{},
		repo,
		executor,
		&recordingScheduleFireProducer{},
		retryProducer,
		WithNotificationProducer(notifier),
	)

	err := svc.ExecuteClaimedScheduleFire(ctx, jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: trigger.OrgSlug,
		ProjectSlug:      trigger.ProjectSlug,
		EnvironmentSlug:  trigger.EnvironmentSlug,
		TriggerSlug:      trigger.TriggerSlug,
		TriggerRevision:  trigger.TriggerRevision,
		ScheduledFireAt:  scheduledAt,
	})

	require.NoError(t, err)
	require.Len(t, repo.retryAttempts, 1)
	attempt := repo.retryAttempts[0].record
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusExhausted, attempt.Status)
	require.Equal(t, rootRunSlug, attempt.RootRunSlug)
	require.Equal(t, must.Slug("run-fire-daily-users-20260609-retry-3"), attempt.RetryOfRunSlug)
	require.Equal(t, 4, attempt.AttemptNumber)
	require.Equal(t, model.PipelineTriggerRetryFailureKindDataSource, attempt.FailureKind)
	require.Equal(t, "max_attempts_exhausted", attempt.DecisionReason)
	require.Empty(t, retryProducer.calls)
	require.Len(t, notifier.calls, 2)
	require.Equal(t, model.NotificationTypePipelineRunFailed, notifier.calls[0].NotificationType)
	candidate := notifier.calls[1]
	require.Equal(t, model.NotificationTypePipelineRetryExhausted, candidate.NotificationType)
	require.Equal(t, model.NotificationSeverityWarning, candidate.Severity)
	require.Equal(t, "pipeline.retry_exhausted:default:prod:daily-users:7:fire-daily-users-20260609", candidate.DedupeFingerprint)
	require.Equal(t, "run-fire-daily-users-20260609-retry-3", candidate.SourceRefs["run_slug"])
	require.Equal(t, "fire-daily-users-20260609", candidate.SourceRefs["root_run_slug"])
	require.Equal(t, "run-fire-daily-users-20260609-retry-3", candidate.SourceRefs["retry_of_run_slug"])
	require.Equal(t, "retry-fire-daily-users-20260609-4", candidate.SourceRefs["retry_attempt_slug"])
	require.NotContains(t, candidate.SourceRefs, "retry_run_slug")
	require.Equal(t, "Pipeline sync-users exhausted retries after run run-fire-daily-users-20260609-retry-3", candidate.SafeMetadata["summary"])
	require.Equal(t, "exhausted", candidate.SafeMetadata["retry_status"])
	require.Equal(t, "max_attempts_exhausted", candidate.SafeMetadata["retry_decision_reason"])
	require.Equal(t, "data_source", candidate.SafeMetadata["failure_kind"])
	require.Equal(t, 4, candidate.SafeMetadata["attempt_number"])
	require.Equal(t, 3, candidate.SafeMetadata["max_attempts"])
	require.NotContains(t, candidate.SafeMetadata, "retry_run_slug")
}

func TestRetryAttemptRecordAppliesMaxElapsedAndExponentialBackoff(t *testing.T) {
	t.Parallel()

	baseAt := time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC)
	rootRunSlug := must.Slug("fire-daily-users-20260609")
	failureKind := "data_source"

	maxElapsedSeconds := 30
	exhausted, shouldEnqueue, err := BuildRetryAttempt(RetryDecisionInput{
		OrgSlug:                 must.Slug("acme"),
		ProjectSlug:             must.Slug("default"),
		EnvironmentSlug:         must.Slug("prod"),
		TriggerSlug:             must.Slug("daily-users"),
		TriggerRevision:         7,
		PipelineSlug:            must.Slug("sync-users"),
		PipelineVersionSpecHash: "spec-hash-123",
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:               true,
			MaxAttempts:           3,
			BackoffStrategy:       model.PipelineTriggerBackoffStrategyFixed,
			InitialDelaySeconds:   60,
			MaxDelaySeconds:       60,
			RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindDataSource},
			MaxElapsedSeconds:     &maxElapsedSeconds,
		},
		FailedRun: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
			RunSlug:     rootRunSlug.String(),
			StartedAt:   baseAt.Add(-time.Minute),
			Status:      "failed",
			FailureKind: &failureKind,
			Source: pipelinerun.RunSourceSummary{
				AttemptNumber: 1,
				RootRunSlug:   rootRunSlug,
			},
		}},
		BaseAt: baseAt,
	})

	require.NoError(t, err)
	require.False(t, shouldEnqueue)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusExhausted, exhausted.Status)
	require.Equal(t, "max_elapsed_exhausted", exhausted.DecisionReason)
	require.True(t, exhausted.ScheduledAt.Equal(baseAt.Add(time.Minute)))

	followUpStartedAt := baseAt.Add(10 * time.Minute)
	exhaustedFollowUp, shouldEnqueue, err := BuildRetryAttempt(RetryDecisionInput{
		OrgSlug:                 must.Slug("acme"),
		ProjectSlug:             must.Slug("default"),
		EnvironmentSlug:         must.Slug("prod"),
		TriggerSlug:             must.Slug("daily-users"),
		TriggerRevision:         7,
		PipelineSlug:            must.Slug("sync-users"),
		PipelineVersionSpecHash: "spec-hash-123",
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:               true,
			MaxAttempts:           4,
			BackoffStrategy:       model.PipelineTriggerBackoffStrategyFixed,
			InitialDelaySeconds:   60,
			MaxDelaySeconds:       60,
			RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindDataSource},
			MaxElapsedSeconds:     &maxElapsedSeconds,
		},
		FailedRun: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
			RunSlug:     "fire-daily-users-20260609-retry-2",
			StartedAt:   followUpStartedAt,
			Status:      "failed",
			FailureKind: &failureKind,
			Source: pipelinerun.RunSourceSummary{
				AttemptNumber: 2,
				RootRunSlug:   rootRunSlug,
			},
		}},
		BaseAt:        followUpStartedAt,
		ElapsedBaseAt: baseAt,
	})

	require.NoError(t, err)
	require.False(t, shouldEnqueue)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusExhausted, exhaustedFollowUp.Status)
	require.Equal(t, "max_elapsed_exhausted", exhaustedFollowUp.DecisionReason)
	require.True(t, exhaustedFollowUp.ScheduledAt.Equal(followUpStartedAt.Add(time.Minute)))

	capped, shouldEnqueue, err := BuildRetryAttempt(RetryDecisionInput{
		OrgSlug:                 must.Slug("acme"),
		ProjectSlug:             must.Slug("default"),
		EnvironmentSlug:         must.Slug("prod"),
		TriggerSlug:             must.Slug("daily-users"),
		TriggerRevision:         7,
		PipelineSlug:            must.Slug("sync-users"),
		PipelineVersionSpecHash: "spec-hash-123",
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:               true,
			MaxAttempts:           4,
			BackoffStrategy:       model.PipelineTriggerBackoffStrategyExponential,
			InitialDelaySeconds:   60,
			MaxDelaySeconds:       90,
			RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindDataSource},
		},
		FailedRun: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
			RunSlug:     "fire-daily-users-20260609-retry-2",
			StartedAt:   baseAt,
			Status:      "failed",
			FailureKind: &failureKind,
			Source: pipelinerun.RunSourceSummary{
				AttemptNumber: 2,
				RootRunSlug:   rootRunSlug,
			},
		}},
		BaseAt: baseAt,
	})

	require.NoError(t, err)
	require.True(t, shouldEnqueue)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusQueued, capped.Status)
	require.Equal(t, 3, capped.AttemptNumber)
	require.True(t, capped.ScheduledAt.Equal(baseAt.Add(90*time.Second)))
}

func TestRetryDecisionInputsPreferFailedRunStartedAt(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.June, 9, 7, 0, 15, 0, time.UTC)
	fallbackAt := startedAt.Add(5 * time.Minute)
	trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 7 * * *", "UTC", nil)
	run := &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
		StartedAt:               startedAt,
		Status:                  "failed",
		PipelineVersionSpecHash: "spec-hash-123",
	}}
	fire := ClaimedScheduleFire{
		Trigger: trigger,
		Claim: ScheduleFireClaim{
			ActualFireAt: fallbackAt,
		},
	}
	attempt := TriggerRetryAttempt{
		OrgSlug:         trigger.OrgSlug,
		ProjectSlug:     trigger.ProjectSlug,
		EnvironmentSlug: trigger.EnvironmentSlug,
		TriggerSlug:     trigger.TriggerSlug,
		TriggerRevision: trigger.TriggerRevision,
		ScheduledAt:     fallbackAt,
	}

	firstAttemptInput, ok := retryDecisionInputForFailedRun(fire, run)
	require.True(t, ok)
	require.True(t, firstAttemptInput.BaseAt.Equal(startedAt))

	followUpInput, ok := retryDecisionInputForRetryAttempt(attempt, run)
	require.True(t, ok)
	require.True(t, followUpInput.BaseAt.Equal(startedAt))
	require.True(t, followUpInput.ElapsedBaseAt.Equal(startedAt))

	rootActualAt := startedAt.Add(-time.Minute)
	run.Metadata.Source = pipelinerun.RunSourceSummary{
		AttemptNumber: 2,
		Schedule: &pipelinerun.RunSourceScheduleSummary{
			ActualFireAt: rootActualAt,
		},
	}

	followUpInput, ok = retryDecisionInputForRetryAttempt(attempt, run)
	require.True(t, ok)
	require.True(t, followUpInput.BaseAt.Equal(startedAt))
	require.True(t, followUpInput.ElapsedBaseAt.Equal(rootActualAt))
}

func newDueScheduleTrigger(
	orgSlug string,
	environmentSlug string,
	triggerSlug string,
	pipelineSlug string,
	enabled bool,
	cronExpr string,
	timezone string,
	lastFireAt *time.Time,
) DueScheduleTrigger {
	return DueScheduleTrigger{
		OrgSlug:         must.Slug(orgSlug),
		ProjectSlug:     must.Slug("default"),
		EnvironmentSlug: must.Slug(environmentSlug),
		TriggerSlug:     must.Slug(triggerSlug),
		TriggerRevision: 7,
		Enabled:         enabled,
		PipelineSlug:    must.Slug(pipelineSlug),
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RuntimeInputKeys: []string{"customer_id", "dry_run"},
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:         false,
			MaxAttempts:     1,
			BackoffStrategy: model.PipelineTriggerBackoffStrategyNone,
		},
		ScheduleConfig: model.PipelineTriggerScheduleConfig{
			Cron:          cronExpr,
			Timezone:      timezone,
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
		LastScheduledFireAt: lastFireAt,
	}
}

type recordingScheduleRepository struct {
	delayedFires               []pipelinetriggers.DelayedScheduleFire
	triggers                   []DueScheduleTrigger
	claimResults               []ScheduleFireClaimResult
	claimedFires               []ClaimedScheduleFire
	claims                     []ScheduleFireClaim
	loadRefs                   []ScheduleFireExecutionRef
	markRefs                   []ScheduleFireExecutionRef
	markScheduleFireErr        error
	completions                []ScheduleFireCompletion
	retryAttempts              []retryAttemptRecordCall
	retryAttemptResults        []TriggerRetryAttempt
	loadedRetryAttempts        []TriggerRetryAttempt
	runningRetryAttempts       []slug.Slug
	markRetryAttemptRunningErr error
	completedRetryAttempts     []completedRetryAttemptCall
}

type retryAttemptRecordCall struct {
	tx     postgres.DBTX
	record TriggerRetryAttemptRecord
}

func (r *recordingScheduleRepository) ListDueScheduleTriggersForSystem(
	context.Context,
	postgres.DBTX,
) ([]DueScheduleTrigger, error) {
	return append([]DueScheduleTrigger(nil), r.triggers...), nil
}

func (r *recordingScheduleRepository) ClaimScheduleFire(
	_ context.Context,
	_ postgres.DBTX,
	claim ScheduleFireClaim,
) (ScheduleFireClaimResult, error) {
	r.claims = append(r.claims, claim)
	if len(r.claimResults) == 0 {
		return ScheduleFireClaimResult{Claimed: true, FireSlug: claim.FireSlug()}, nil
	}
	result := r.claimResults[0]
	r.claimResults = r.claimResults[1:]
	return result, nil
}

func (r *recordingScheduleRepository) LoadClaimedScheduleFire(
	_ context.Context,
	_ postgres.DBTX,
	ref ScheduleFireExecutionRef,
) (ClaimedScheduleFire, error) {
	r.loadRefs = append(r.loadRefs, ref)
	if len(r.claimedFires) == 0 {
		return ClaimedScheduleFire{}, xerror.NotFound(xerror.PipelineTrigger)
	}
	fire := r.claimedFires[0]
	r.claimedFires = r.claimedFires[1:]
	return fire, nil
}

func (r *recordingScheduleRepository) MarkScheduleFireRunning(
	_ context.Context,
	_ postgres.DBTX,
	ref ScheduleFireExecutionRef,
) error {
	r.markRefs = append(r.markRefs, ref)
	return r.markScheduleFireErr
}

func (r *recordingScheduleRepository) CompleteScheduleFire(
	_ context.Context,
	_ postgres.DBTX,
	completion ScheduleFireCompletion,
) error {
	r.completions = append(r.completions, completion)
	return nil
}

func (r *recordingScheduleRepository) RecordTriggerRetryAttempt(
	_ context.Context,
	tx postgres.DBTX,
	record TriggerRetryAttemptRecord,
) (TriggerRetryAttempt, error) {
	r.retryAttempts = append(r.retryAttempts, retryAttemptRecordCall{tx: tx, record: record})
	if len(r.retryAttemptResults) == 0 {
		attemptNumber := strconv.Itoa(record.AttemptNumber)
		return TriggerRetryAttempt{
			OrgSlug:                 record.OrgSlug,
			ProjectSlug:             record.ProjectSlug,
			EnvironmentSlug:         record.EnvironmentSlug,
			TriggerSlug:             record.TriggerSlug,
			TriggerRevision:         record.TriggerRevision,
			PipelineSlug:            record.PipelineSlug,
			AttemptSlug:             slug.Slug("retry-" + record.RootRunSlug.String() + "-" + attemptNumber),
			Status:                  record.Status,
			Source:                  record.Source,
			RootRunSlug:             record.RootRunSlug,
			RetryOfRunSlug:          record.RetryOfRunSlug,
			RunSlug:                 slug.Slug("run-" + record.RootRunSlug.String() + "-retry-" + attemptNumber),
			AttemptNumber:           record.AttemptNumber,
			FailureKind:             record.FailureKind,
			ScheduledAt:             record.ScheduledAt,
			PipelineVersionSpecHash: record.PipelineVersionSpecHash,
		}, nil
	}
	result := r.retryAttemptResults[0]
	r.retryAttemptResults = r.retryAttemptResults[1:]
	return result, nil
}

func (r *recordingScheduleRepository) LoadTriggerRetryAttemptForExecution(
	_ context.Context,
	_ postgres.DBTX,
	_, _, _ slug.Slug,
	attemptSlug slug.Slug,
) (TriggerRetryAttempt, error) {
	if len(r.loadedRetryAttempts) == 0 {
		return TriggerRetryAttempt{}, xerror.NotFound(xerror.PipelineTrigger)
	}
	attempt := r.loadedRetryAttempts[0]
	r.loadedRetryAttempts = r.loadedRetryAttempts[1:]
	if attempt.AttemptSlug == "" {
		attempt.AttemptSlug = attemptSlug
	}
	return attempt, nil
}

func (r *recordingScheduleRepository) MarkTriggerRetryAttemptRunning(
	_ context.Context,
	_ postgres.DBTX,
	_, _, _ slug.Slug,
	attemptSlug slug.Slug,
) error {
	r.runningRetryAttempts = append(r.runningRetryAttempts, attemptSlug)
	return r.markRetryAttemptRunningErr
}

type completedRetryAttemptCall struct {
	tx              postgres.DBTX
	attemptSlug     slug.Slug
	status          model.PipelineTriggerRetryAttemptStatus
	sourceEventSlug *slug.Slug
	errorSummary    *string
}

func (r *recordingScheduleRepository) CompleteTriggerRetryAttempt(
	_ context.Context,
	tx postgres.DBTX,
	_, _, _ slug.Slug,
	attemptSlug slug.Slug,
	status model.PipelineTriggerRetryAttemptStatus,
	sourceEventSlug *slug.Slug,
	errorSummary *string,
) error {
	r.completedRetryAttempts = append(r.completedRetryAttempts, completedRetryAttemptCall{
		tx:              tx,
		attemptSlug:     attemptSlug,
		status:          status,
		sourceEventSlug: sourceEventSlug,
		errorSummary:    errorSummary,
	})
	return nil
}

type recordingScheduleFireProducer struct {
	calls  []scheduleFireProducerCall
	queued queue.QueuedJob
	err    error
}

type scheduleFireProducerCall struct {
	tx   postgres.DBTX
	args jobs.PipelineScheduleFireExecutionArgs
}

func (p *recordingScheduleFireProducer) EnqueueScheduleFireExecution(
	_ context.Context,
	tx postgres.DBTX,
	args jobs.PipelineScheduleFireExecutionArgs,
) (queue.QueuedJob, error) {
	p.calls = append(p.calls, scheduleFireProducerCall{
		tx:   tx,
		args: args,
	})
	if p.err != nil {
		return queue.QueuedJob{}, p.err
	}
	if p.queued != (queue.QueuedJob{}) {
		return p.queued, nil
	}
	return queue.QueuedJob{
		Kind:  args.Kind(),
		Queue: jobs.PipelineScheduleQueue,
		State: "queued",
	}, nil
}

type recordingPipelineRetryProducer struct {
	calls []pipelineRetryProducerCall
	err   error
}

type pipelineRetryProducerCall struct {
	tx   postgres.DBTX
	args jobs.PipelineTriggerRetryExecutionArgs
}

func (p *recordingPipelineRetryProducer) EnqueueTriggerRetryExecution(
	_ context.Context,
	tx postgres.DBTX,
	args jobs.PipelineTriggerRetryExecutionArgs,
) (queue.QueuedJob, error) {
	p.calls = append(p.calls, pipelineRetryProducerCall{tx: tx, args: args})
	if p.err != nil {
		return queue.QueuedJob{}, p.err
	}
	return queue.QueuedJob{
		Kind:  args.Kind(),
		Queue: jobs.PipelineRetryQueue,
		State: "queued",
	}, nil
}

type recordingNotificationProducer struct {
	calls []notificationsvc.CreateCandidateParams
	err   error
}

func (p *recordingNotificationProducer) EnqueueCandidate(
	_ context.Context,
	_ postgres.DBTX,
	params notificationsvc.CreateCandidateParams,
) error {
	p.calls = append(p.calls, params)
	if p.err != nil {
		return p.err
	}
	return nil
}

type recordingPipelineExecutor struct {
	reconcileErr error
	calls        []pipelineExecuteCall
	versionCalls []pipelineExecuteVersionCall
	responses    []pipelineExecuteResult
}

type pipelineExecuteCall struct {
	orgSlug         slug.Slug
	projectSlug     slug.Slug
	environmentSlug slug.Slug
	pipelineSlug    slug.Slug
	runSlug         *slug.Slug
	actor           model.EventActor
	source          pipelinerun.RunSourceSummary
	runtimeInput    map[string]any
}

type pipelineExecuteVersionCall struct {
	pipelineExecuteCall
	specHash string
}

type pipelineExecuteResult struct {
	run *pipelinesvc.PipelineRunDetail
	err error
}

func (e *recordingPipelineExecutor) Execute(
	_ context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	runSlug *slug.Slug,
	_ *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
) (*pipelinesvc.PipelineRunDetail, error) {
	e.calls = append(e.calls, pipelineExecuteCall{
		orgSlug:         orgSlug,
		projectSlug:     projectSlug,
		environmentSlug: environmentSlug,
		pipelineSlug:    pipelineSlug,
		runSlug:         copyOptionalSlug(runSlug),
		actor:           actor,
		source:          source,
		runtimeInput:    runtimeInput,
	})
	if len(e.responses) > 0 {
		response := e.responses[0]
		e.responses = e.responses[1:]
		return response.run, response.err
	}
	returnedRunSlug := "run-" + pipelineSlug.String()
	if runSlug != nil {
		returnedRunSlug = runSlug.String()
	}
	return &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
		RunSlug:  returnedRunSlug,
		Source:   source,
		Status:   "succeeded",
		StepSlug: "fetch-users",
	}}, nil
}

func (e *recordingPipelineExecutor) ExecuteVersion(
	_ context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	specHash string,
	runSlug *slug.Slug,
	_ *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
) (*pipelinesvc.PipelineRunDetail, error) {
	e.versionCalls = append(e.versionCalls, pipelineExecuteVersionCall{
		pipelineExecuteCall: pipelineExecuteCall{
			orgSlug:         orgSlug,
			projectSlug:     projectSlug,
			environmentSlug: environmentSlug,
			pipelineSlug:    pipelineSlug,
			runSlug:         copyOptionalSlug(runSlug),
			actor:           actor,
			source:          source,
			runtimeInput:    runtimeInput,
		},
		specHash: specHash,
	})
	if len(e.responses) > 0 {
		response := e.responses[0]
		e.responses = e.responses[1:]
		return response.run, response.err
	}
	returnedRunSlug := "run-" + pipelineSlug.String()
	if runSlug != nil {
		returnedRunSlug = runSlug.String()
	}
	return &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
		RunSlug:  returnedRunSlug,
		Source:   source,
		Status:   "succeeded",
		StepSlug: "fetch-users",
	}}, nil
}

func copyOptionalSlug(value *slug.Slug) *slug.Slug {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func enabledRetryPolicy(maxAttempts int, kinds ...model.PipelineTriggerRetryFailureKind) model.PipelineTriggerRetryPolicy {
	return model.PipelineTriggerRetryPolicy{
		Enabled:               true,
		MaxAttempts:           maxAttempts,
		BackoffStrategy:       model.PipelineTriggerBackoffStrategyFixed,
		InitialDelaySeconds:   60,
		MaxDelaySeconds:       60,
		RetryableFailureKinds: append([]model.PipelineTriggerRetryFailureKind(nil), kinds...),
	}
}

func stringPtr(value string) *string {
	return &value
}

func slugPtr(value slug.Slug) *slug.Slug {
	return &value
}

type fakeScheduleDBTX struct{}

func (f *fakeScheduleDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, xerror.Unexpected().Detail("fake schedule tx exec should not be called")
}

func (f *fakeScheduleDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, xerror.Unexpected().Detail("fake schedule tx query should not be called")
}

func (f *fakeScheduleDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	return fakeScheduleRow{}
}

type fakeScheduleRow struct{}

func (fakeScheduleRow) Scan(...any) error {
	return xerror.Unexpected().Detail("fake schedule tx query row should not be called")
}

func (r *recordingScheduleRepository) ReconcileLogicalRunFinalizationsForSystem(context.Context, postgres.DBTX) error {
	return nil
}

func (e *recordingPipelineExecutor) GetRun(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*pipelinesvc.PipelineRunDetail, error) {
	if len(e.responses) > 0 {
		return e.responses[0].run, e.responses[0].err
	}
	return nil, pipelinesvc.ErrPipelineRunNotFound
}

func TestRecoveredScheduleFinalizesStoredAttemptWithoutExecuting(t *testing.T) {
	for _, status := range []string{"succeeded", "running"} {
		t.Run(status, func(t *testing.T) {
			trigger := newDueScheduleTrigger("acme", "prod", "daily", "sync", true, "* * * * *", "UTC", nil)
			fire := ClaimedScheduleFire{Resuming: true, Trigger: trigger, Claim: ScheduleFireClaim{OrgSlug: trigger.OrgSlug, ProjectSlug: trigger.ProjectSlug, EnvironmentSlug: trigger.EnvironmentSlug, TriggerSlug: trigger.TriggerSlug}, FireSlug: must.Slug("fire-recovered")}
			repo := &recordingScheduleRepository{claimedFires: []ClaimedScheduleFire{fire}}
			executor := &recordingPipelineExecutor{responses: []pipelineExecuteResult{{run: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{RunSlug: fire.FireSlug.String(), Status: status}}}}}
			svc := New(testutil.FakeTransactionManager{}, repo, executor, &recordingScheduleFireProducer{}, nil)
			err := svc.ExecuteClaimedScheduleFire(t.Context(), jobs.PipelineScheduleFireExecutionArgs{})
			if status == "succeeded" {
				require.NoError(t, err)
				require.Len(t, repo.completions, 1)
			} else {
				require.Error(t, err)
				require.Empty(t, repo.completions)
			}
			require.Empty(t, executor.calls)
			require.Empty(t, executor.versionCalls)
			require.Empty(t, repo.markRefs)
		})
	}
}

func (r *recordingScheduleRepository) ListPendingRunFinalizationsForSystem(context.Context, postgres.DBTX) ([]pipelinetriggers.PendingRunFinalization, error) {
	return nil, nil
}

func (p *recordingScheduleFireProducer) EnqueueRunFinalization(context.Context, postgres.DBTX, queue.JobArgs) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (r *recordingScheduleRepository) ListDelayedScheduleFiresForSystem(context.Context, postgres.DBTX, time.Time) ([]pipelinetriggers.DelayedScheduleFire, error) {
	return r.delayedFires, nil
}

func TestDelayedScheduleObservationsKeepStableIdentity(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	fire := pipelinetriggers.DelayedScheduleFire{OrgSlug: "acme", ProjectSlug: "ops", EnvironmentSlug: "dev", PipelineSlug: "orders", TriggerSlug: "daily", TriggerRevision: 1, ScheduledFireAt: at}
	repo := &recordingScheduleRepository{delayedFires: []pipelinetriggers.DelayedScheduleFire{fire}}
	notifier := &recordingNotificationProducer{}
	svc := New(testutil.FakeTransactionManager{}, repo, &recordingPipelineExecutor{}, &recordingScheduleFireProducer{}, nil, WithNotificationProducer(notifier))
	_, err := svc.DispatchDueSchedules(t.Context(), at.Add(2*time.Minute))
	require.NoError(t, err)
	_, err = svc.DispatchDueSchedules(t.Context(), at.Add(3*time.Minute))
	require.NoError(t, err)
	require.Len(t, notifier.calls, 2)
	require.Equal(t, notifier.calls[0].RecordSlug, notifier.calls[1].RecordSlug)
	require.Equal(t, model.NotificationTypePipelineScheduledRunDelayed, notifier.calls[0].NotificationType)
	require.Equal(t, at.Add(time.Minute), notifier.calls[0].ObservedAt)
	fire.OrgSlug = "other"
	other := scheduleNotificationCandidate(fire, model.NotificationTypePipelineScheduledRunDelayed)
	require.NotEqual(t, notifier.calls[0].RecordSlug, other.RecordSlug)
	fire.OrgSlug = "acme"
	fire.TriggerRevision++
	other = scheduleNotificationCandidate(fire, model.NotificationTypePipelineScheduledRunDelayed)
	require.NotEqual(t, notifier.calls[0].RecordSlug, other.RecordSlug)
	notifier.err = xerror.Unexpected().Detail("queue unavailable")
	_, err = svc.DispatchDueSchedules(t.Context(), at.Add(4*time.Minute))
	require.ErrorIs(t, err, notifier.err)
}

func (e *recordingPipelineExecutor) AcceptQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug, pipelinerun.RunSourceSummary, time.Time, int64) error {
	return nil
}

func (e *recordingPipelineExecutor) StartQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, time.Time) error {
	return nil
}

func (e *recordingPipelineExecutor) ReconcileInterruptedRuns(context.Context, time.Time) error {
	return e.reconcileErr
}

func (*recordingPipelineExecutor) FailUnadmittedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error {
	return nil
}

func TestReconciliationFailureDoesNotBlockScheduleDispatch(t *testing.T) {
	now := time.Date(2026, 6, 9, 7, 0, 0, 0, time.UTC)
	repo := &recordingScheduleRepository{triggers: []DueScheduleTrigger{newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 9 * * *", "Europe/Warsaw", nil)}, claimResults: []ScheduleFireClaimResult{{Claimed: true, FireSlug: "fire-daily-users-20260609"}}}
	failure := xerror.Unexpected().Detail("retained run could not be decoded")
	executor := &recordingPipelineExecutor{reconcileErr: failure}
	svc := New(testutil.FakeTransactionManager{Tx: &fakeScheduleDBTX{}}, repo, executor, &recordingScheduleFireProducer{}, nil)
	result, err := svc.DispatchDueSchedules(t.Context(), now)
	require.ErrorIs(t, err, failure)
	require.Equal(t, DispatchResult{Scanned: 1, Claimed: 1, Enqueued: 1}, result)
	require.Len(t, repo.claims, 1)
}
