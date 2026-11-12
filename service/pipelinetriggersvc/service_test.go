package pipelinetriggersvc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

func TestPipelineTriggerEventRecordUsesSanitizedMetadata(t *testing.T) {
	t.Parallel()

	record, err := (pipelineTriggerEvent{
		projectSlug:     must.Slug("backoffice"),
		environmentSlug: must.Slug("prod"),
		trigger: &model.PipelineTrigger{
			Slug:             must.Slug("nightly-invoices"),
			Name:             "Nightly invoices",
			Type:             model.PipelineTriggerTypeSchedule,
			Enabled:          true,
			Revision:         3,
			EnvironmentSlug:  must.Slug("prod"),
			PipelineSlug:     must.Slug("invoice-sync"),
			VersionPolicy:    model.PipelineTriggerVersionPolicy{Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed},
			RuntimeInputKeys: []string{"dry_run"},
			RetryPolicy: model.PipelineTriggerRetryPolicy{
				Enabled:               true,
				MaxAttempts:           3,
				BackoffStrategy:       model.PipelineTriggerBackoffStrategyFixed,
				InitialDelaySeconds:   60,
				MaxDelaySeconds:       300,
				RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindPlatform},
			},
			ScheduleConfig: &model.PipelineTriggerScheduleConfig{
				Cron:          "0 2 * * *",
				Timezone:      "Europe/Warsaw",
				MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicyFireOnce,
			},
		},
		action: model.EventActionCreate,
	}).EventRecord()

	require.NoError(t, err)
	require.Equal(t, model.EventEntityTypePipelineTrigger, record.EntityType)
	require.Equal(t, model.EventActionCreate, record.Action)
	require.Equal(t, "Europe/Warsaw", record.Metadata["schedule_timezone"])
	require.Equal(t, "fire_once", record.Metadata["schedule_misfire_policy"])

	rawMetadata, err := json.Marshal(record.Metadata)
	require.NoError(t, err)
	metadata := string(rawMetadata)
	require.Contains(t, metadata, `"runtime_input_keys":["dry_run"]`)
	require.NotContains(t, metadata, "secret")
	require.NotContains(t, metadata, "webhook")
}

func TestCreatePipelineTriggerPersistsScheduleConfigWithoutDeploymentWhenDisabled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("sync-users")
	triggerSlug := must.Slug("nightly-users")
	maxElapsedSeconds := 300

	repo := &fakeTriggerRepository{
		projectEnvironmentEnabled: true,
		pipelineExists:            true,
		deploymentExists:          false,
		createFn: func(
			_ context.Context,
			_ postgres.DBTX,
			_, _, _ slug.Slug,
			params pipelinetriggers.CreatePipelineTriggerParams,
		) (*pipelinetriggers.PipelineTrigger, error) {
			require.Equal(t, triggerSlug, params.Slug)
			require.Equal(t, model.PipelineTriggerTypeSchedule, params.Type)
			require.False(t, params.Enabled)
			require.NotNil(t, params.RuntimeInputKeys)
			require.Empty(t, params.RuntimeInputKeys)
			require.NotNil(t, params.ScheduleConfig)
			require.Equal(t, "0 2 * * *", params.ScheduleConfig.Cron)
			require.Equal(t, model.PipelineTriggerBackoffStrategyFixed, params.RetryPolicy.BackoffStrategy)
			require.Equal(t, []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindPlatform}, params.RetryPolicy.RetryableFailureKinds)
			return repoTriggerFromCreateParams(environmentSlug, params), nil
		},
	}
	svc := newTestService(repo)

	got, err := svc.CreatePipelineTrigger(ctx, orgSlug, projectSlug, environmentSlug, model.NewPipelineTrigger{
		Name:         "Nightly Users",
		Slug:         triggerSlug,
		Type:         model.PipelineTriggerTypeSchedule,
		Enabled:      false,
		PipelineSlug: pipelineSlug,
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:               true,
			MaxAttempts:           3,
			BackoffStrategy:       model.PipelineTriggerBackoffStrategyFixed,
			InitialDelaySeconds:   60,
			MaxDelaySeconds:       60,
			RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindPlatform},
			MaxElapsedSeconds:     &maxElapsedSeconds,
		},
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 2 * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicyFireOnce,
		},
	}, model.SystemEventActor())

	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.RuntimeInputKeys)
	require.Empty(t, got.RuntimeInputKeys)
	require.True(t, repo.createCalled)
	require.NotNil(t, got.ScheduleConfig)
	require.Equal(t, model.PipelineTriggerScheduleMisfirePolicyFireOnce, got.ScheduleConfig.MisfirePolicy)
}

func TestCreatePipelineTriggerResolvesPinnedVersionPrefix(t *testing.T) {
	t.Parallel()

	fullHash := "abcdef1234567890"
	repo := &fakeTriggerRepository{
		projectEnvironmentEnabled: true,
		pipelineExists:            true,
		pinnedVersionHash:         fullHash,
		createFn: func(
			_ context.Context,
			_ postgres.DBTX,
			_, _, _ slug.Slug,
			params pipelinetriggers.CreatePipelineTriggerParams,
		) (*pipelinetriggers.PipelineTrigger, error) {
			require.NotNil(t, params.VersionPolicy.PinnedVersionHash)
			require.Equal(t, fullHash, *params.VersionPolicy.PinnedVersionHash)
			return repoTriggerFromCreateParams(must.Slug("prod"), params), nil
		},
	}
	svc := newTestService(repo)

	got, err := svc.CreatePipelineTrigger(context.Background(), must.Slug("acme"), must.Slug("backoffice"), must.Slug("prod"), model.NewPipelineTrigger{
		Name:         "Pinned nightly",
		Type:         model.PipelineTriggerTypeSchedule,
		Enabled:      false,
		PipelineSlug: must.Slug("sync-users"),
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode:              model.PipelineTriggerVersionPolicyModePinned,
			PinnedVersionHash: ptrString("abcdef"),
		},
		RetryPolicy: defaultNoRetryPolicy(),
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 * * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}, model.SystemEventActor())

	require.NoError(t, err)
	require.NotNil(t, got.VersionPolicy.PinnedVersionHash)
	require.Equal(t, fullHash, *got.VersionPolicy.PinnedVersionHash)
}

func TestUpdatePipelineTriggerReplacesScheduleConfigAndNormalizesRuntimeInputKeys(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("prod")
	triggerSlug := must.Slug("nightly-users")
	pipelineSlug := must.Slug("sync-users")
	actor := model.UserEventActor(must.Slug("alice"))
	updateCalled := false

	repo := &fakeTriggerRepository{
		projectEnvironmentEnabled: true,
		pipelineExists:            true,
		deploymentExists:          true,
		existing: &pipelinetriggers.PipelineTrigger{
			Slug:            triggerSlug,
			Name:            "Nightly Users",
			Type:            model.PipelineTriggerTypeSchedule,
			Enabled:         false,
			Revision:        1,
			EnvironmentSlug: environmentSlug,
			PipelineSlug:    pipelineSlug,
			VersionPolicy: model.PipelineTriggerVersionPolicy{
				Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
			},
			RetryPolicy: defaultNoRetryPolicy(),
			ScheduleConfig: &model.PipelineTriggerScheduleConfig{
				Cron:          "0 2 * * *",
				Timezone:      "UTC",
				MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
			},
			CreatedBy: model.SystemEventActor(),
			UpdatedBy: model.SystemEventActor(),
			CreatedAt: fixedTestTime(),
			UpdatedAt: fixedTestTime(),
		},
		updateFn: func(
			_ context.Context,
			_ postgres.DBTX,
			gotOrgSlug, gotProjectSlug, gotEnvironmentSlug, gotTriggerSlug slug.Slug,
			params pipelinetriggers.UpdatePipelineTriggerParams,
		) (*pipelinetriggers.PipelineTrigger, error) {
			updateCalled = true
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, projectSlug, gotProjectSlug)
			require.Equal(t, environmentSlug, gotEnvironmentSlug)
			require.Equal(t, triggerSlug, gotTriggerSlug)
			require.Equal(t, actor, params.Actor)
			require.True(t, params.Enabled)
			require.Equal(t, []string{"dry_run", "tenant_id"}, params.RuntimeInputKeys)
			require.NotNil(t, params.ScheduleConfig)
			require.Equal(t, "15 * * * *", params.ScheduleConfig.Cron)
			return repoTriggerFromUpdateParams(environmentSlug, triggerSlug, params), nil
		},
	}
	svc := newTestService(repo)

	got, err := svc.UpdatePipelineTrigger(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, model.UpdatePipelineTrigger{
		Name:         "Hourly Users",
		Type:         model.PipelineTriggerTypeSchedule,
		Enabled:      true,
		PipelineSlug: pipelineSlug,
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RuntimeInputKeys: []string{"tenant_id", "dry_run", "tenant_id"},
		RetryPolicy:      defaultNoRetryPolicy(),
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "15 * * * *",
			Timezone:      "Europe/Warsaw",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicyFireOnce,
		},
	}, actor)

	require.NoError(t, err)
	require.True(t, updateCalled)
	require.Equal(t, int64(2), got.Revision)
	require.Equal(t, []string{"dry_run", "tenant_id"}, got.RuntimeInputKeys)
	require.NotNil(t, got.ScheduleConfig)
	require.Equal(t, model.PipelineTriggerScheduleMisfirePolicyFireOnce, got.ScheduleConfig.MisfirePolicy)
}

func TestUpdatePipelineTriggerRejectsTypeChange(t *testing.T) {
	t.Parallel()

	repo := &fakeTriggerRepository{existing: &pipelinetriggers.PipelineTrigger{
		Type:           model.PipelineTriggerTypeSchedule,
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{},
	}}
	svc := newTestService(repo)

	_, err := svc.UpdatePipelineTrigger(
		context.Background(), must.Slug("acme"), must.Slug("backoffice"), must.Slug("prod"), must.Slug("nightly-users"),
		model.UpdatePipelineTrigger{
			Name: "Incoming", Type: model.PipelineTriggerTypeWebhook, PipelineSlug: must.Slug("sync-users"),
			VersionPolicy: model.PipelineTriggerVersionPolicy{Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed},
			RetryPolicy:   defaultNoRetryPolicy(), WebhookConfig: &model.PipelineTriggerWebhookConfig{},
		},
		model.SystemEventActor(),
	)

	require.True(t, xerror.Is(err, ErrInvalidPipelineTriggerInput))
	require.ErrorContains(t, err, "pipeline trigger type is immutable")
}

func TestUpdatePipelineTriggerRejectsJitter(t *testing.T) {
	t.Parallel()

	trigger := validScheduleTriggerInput()
	trigger.RetryPolicy = model.PipelineTriggerRetryPolicy{
		MaxAttempts: 1, BackoffStrategy: model.PipelineTriggerBackoffStrategyExponential,
		InitialDelaySeconds: 60, MaxDelaySeconds: 300, Jitter: true,
	}
	svc := newTestService(&fakeTriggerRepository{})
	_, err := svc.UpdatePipelineTrigger(
		context.Background(), must.Slug("acme"), must.Slug("backoffice"), must.Slug("prod"), must.Slug("nightly-users"),
		model.UpdatePipelineTrigger{
			Name: trigger.Name, Type: trigger.Type, PipelineSlug: trigger.PipelineSlug,
			VersionPolicy: trigger.VersionPolicy, RetryPolicy: trigger.RetryPolicy, ScheduleConfig: trigger.ScheduleConfig,
		},
		model.SystemEventActor(),
	)

	require.True(t, xerror.Is(err, ErrInvalidPipelineTriggerInput))
	require.ErrorContains(t, err, "retry jitter is unsupported; set jitter to false")
}

func TestPipelineTriggerReadListAndDelete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("prod")
	triggerSlug := must.Slug("nightly-users")
	pipelineSlug := must.Slug("sync-users")
	actor := model.UserEventActor(must.Slug("alice"))

	repo := &fakeTriggerRepository{
		existing: &pipelinetriggers.PipelineTrigger{
			Slug:            triggerSlug,
			Name:            "Nightly Users",
			Type:            model.PipelineTriggerTypeSchedule,
			Enabled:         true,
			Revision:        1,
			EnvironmentSlug: environmentSlug,
			PipelineSlug:    pipelineSlug,
			VersionPolicy: model.PipelineTriggerVersionPolicy{
				Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
			},
			RuntimeInputKeys: []string{"customer_id"},
			RetryPolicy:      defaultNoRetryPolicy(),
			ScheduleConfig: &model.PipelineTriggerScheduleConfig{
				Cron:          "0 2 * * *",
				Timezone:      "UTC",
				MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
			},
			CreatedBy: actor,
			UpdatedBy: actor,
			CreatedAt: fixedTestTime(),
			UpdatedAt: fixedTestTime(),
		},
	}
	svc := newTestService(repo)

	got, err := svc.GetPipelineTrigger(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug)
	require.NoError(t, err)
	require.Equal(t, triggerSlug, got.Slug)
	require.NotNil(t, got.ScheduleConfig)
	require.Equal(t, "0 2 * * *", got.ScheduleConfig.Cron)

	list, err := svc.ListPipelineTriggers(ctx, orgSlug, projectSlug, environmentSlug, model.ListRequest{Limit: 2})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.False(t, list.HasMore)
	require.Equal(t, []string{"customer_id"}, list.Items[0].RuntimeInputKeys)

	err = svc.DeletePipelineTrigger(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, actor)
	require.NoError(t, err)
	require.True(t, repo.deleteCalled)
}

func TestListPipelineScheduleFiresMapsDiagnostics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("prod")
	triggerSlug := must.Slug("nightly-users")
	runSlug := must.Slug("run-nightly-users")
	sourceEventSlug := must.Slug("evt-schedule-fire")
	errorSummary := "connector timeout"
	scheduledAt := fixedTestTime()
	actualAt := scheduledAt.Add(5 * time.Second)
	updatedAt := scheduledAt.Add(time.Minute)

	repo := &fakeTriggerRepository{
		existing: &pipelinetriggers.PipelineTrigger{
			Slug:            triggerSlug,
			Name:            "Nightly Users",
			Type:            model.PipelineTriggerTypeSchedule,
			Enabled:         true,
			Revision:        3,
			EnvironmentSlug: environmentSlug,
			PipelineSlug:    must.Slug("sync-users"),
			VersionPolicy: model.PipelineTriggerVersionPolicy{
				Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
			},
			RetryPolicy:    defaultNoRetryPolicy(),
			ScheduleConfig: &model.PipelineTriggerScheduleConfig{Cron: "0 2 * * *", Timezone: "UTC", MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip},
			CreatedAt:      fixedTestTime(),
			UpdatedAt:      fixedTestTime(),
		},
		scheduleFires: []pipelinetriggers.ScheduleFireDiagnostic{
			{
				FireSlug:        must.Slug("fire-nightly-users-20260607t120000z"),
				TriggerSlug:     triggerSlug,
				TriggerRevision: 3,
				PipelineSlug:    must.Slug("sync-users"),
				ScheduledFireAt: scheduledAt,
				ActualFireAt:    actualAt,
				Status:          model.PipelineScheduleFireStatusFailed,
				MisfireResult:   model.PipelineTriggerScheduleMisfireResultFireOnce,
				RunSlug:         &runSlug,
				SourceEventSlug: &sourceEventSlug,
				ErrorSummary:    &errorSummary,
				CreatedAt:       scheduledAt,
				UpdatedAt:       updatedAt,
			},
			{
				FireSlug:        must.Slug("fire-nightly-users-20260607t121000z"),
				TriggerSlug:     triggerSlug,
				TriggerRevision: 3,
				PipelineSlug:    must.Slug("sync-users"),
				ScheduledFireAt: scheduledAt.Add(10 * time.Minute),
				ActualFireAt:    actualAt.Add(10 * time.Minute),
				Status:          model.PipelineScheduleFireStatusSkipped,
				MisfireResult:   model.PipelineTriggerScheduleMisfireResultSkipped,
				CreatedAt:       scheduledAt.Add(10 * time.Minute),
				UpdatedAt:       updatedAt.Add(10 * time.Minute),
			},
		},
	}
	svc := newTestService(repo)

	got, err := svc.ListPipelineScheduleFires(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, model.ListRequest{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, model.ListRequest{
		Limit:     1,
		SortBy:    model.ListSortScheduledAt,
		Order:     model.ListOrderDesc,
		Direction: model.ListDirectionNext,
	}, repo.scheduleFiresRequest)
	require.Len(t, got.Items, 1)
	require.True(t, got.HasMore)
	require.NotNil(t, got.NextCursor)
	require.Nil(t, got.PrevCursor)
	require.Equal(t, must.Slug("fire-nightly-users-20260607t120000z"), got.Items[0].FireSlug)
	require.Equal(t, model.PipelineScheduleFireStatusFailed, got.Items[0].Status)
	require.Equal(t, model.PipelineTriggerScheduleMisfireResultFireOnce, got.Items[0].MisfireResult)
	require.Equal(t, &runSlug, got.Items[0].RunSlug)
	require.Equal(t, &sourceEventSlug, got.Items[0].SourceEventSlug)
	require.Equal(t, &errorSummary, got.Items[0].ErrorSummary)
	require.Equal(t, scheduledAt, got.Items[0].CreatedAt)
	require.Equal(t, updatedAt, got.Items[0].UpdatedAt)

	prevCursor := scheduledAt.Format(time.RFC3339Nano) + "_fire-nightly-users-20260607t120000z"
	_, err = svc.ListPipelineScheduleFires(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, model.ListRequest{
		Limit:     1,
		Cursor:    prevCursor,
		Direction: model.ListDirectionPrev,
	})
	require.NoError(t, err)
	require.Equal(t, model.ListRequest{
		Limit:     1,
		Cursor:    prevCursor,
		SortBy:    model.ListSortScheduledAt,
		Order:     model.ListOrderDesc,
		Direction: model.ListDirectionPrev,
	}, repo.scheduleFiresRequest)
}

func TestListPipelineScheduleFiresPropagatesRepositoryErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("prod")
	triggerSlug := must.Slug("nightly-users")
	repo := &fakeTriggerRepository{
		existing: &pipelinetriggers.PipelineTrigger{
			Slug:            triggerSlug,
			Name:            "Nightly Users",
			Type:            model.PipelineTriggerTypeSchedule,
			Enabled:         true,
			Revision:        3,
			EnvironmentSlug: environmentSlug,
			PipelineSlug:    must.Slug("sync-users"),
			VersionPolicy: model.PipelineTriggerVersionPolicy{
				Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
			},
			RetryPolicy:    defaultNoRetryPolicy(),
			ScheduleConfig: &model.PipelineTriggerScheduleConfig{Cron: "0 2 * * *", Timezone: "UTC", MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip},
			CreatedAt:      fixedTestTime(),
			UpdatedAt:      fixedTestTime(),
		},
		scheduleFiresErr: pipelinetriggers.ErrPipelineTriggerNotFound,
	}
	svc := newTestService(repo)

	_, err := svc.ListPipelineScheduleFires(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, model.ListRequest{Limit: 1})
	require.Error(t, err)
	require.True(t, xerror.Is(err, pipelinetriggers.ErrPipelineTriggerNotFound))
}

func TestListPipelineRunTriggerRetryAttemptsMapsDiagnostics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgSlug := must.Slug("acme")
	projectSlug := must.Slug("backoffice")
	environmentSlug := must.Slug("prod")
	runSlug := must.Slug("run-nightly-users")
	decisionReason := "max_attempts_exhausted"
	errorSummary := "connector timeout"
	failureKind := model.PipelineTriggerRetryFailureKindDataSource
	sourceEventSlug := must.Slug("evt-retry-run-2")
	scheduledAt := fixedTestTime()
	enqueuedAt := scheduledAt.Add(time.Second)
	startedAt := scheduledAt.Add(2 * time.Second)
	completedAt := scheduledAt.Add(3 * time.Second)
	updatedAt := scheduledAt.Add(4 * time.Second)

	repo := &fakeTriggerRepository{
		retryAttempts: []pipelinetriggers.TriggerRetryAttemptDiagnostic{{
			AttemptSlug:     must.Slug("retry-run-nightly-users-2"),
			Status:          model.PipelineTriggerRetryAttemptStatusFailed,
			DecisionReason:  &decisionReason,
			TriggerSlug:     must.Slug("nightly-users"),
			TriggerRevision: 3,
			PipelineSlug:    must.Slug("sync-users"),
			RootRunSlug:     runSlug,
			RetryOfRunSlug:  runSlug,
			RunSlug:         must.Slug("retry-run-nightly-users-2-run"),
			AttemptNumber:   2,
			FailureKind:     &failureKind,
			SourceEventSlug: &sourceEventSlug,
			ScheduledAt:     scheduledAt,
			EnqueuedAt:      &enqueuedAt,
			StartedAt:       &startedAt,
			CompletedAt:     &completedAt,
			ErrorSummary:    &errorSummary,
			CreatedAt:       scheduledAt,
			UpdatedAt:       updatedAt,
		}},
	}
	svc := newTestService(repo)

	got, err := svc.ListPipelineRunTriggerRetryAttempts(ctx, orgSlug, projectSlug, environmentSlug, runSlug)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, must.Slug("retry-run-nightly-users-2"), got[0].AttemptSlug)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusFailed, got[0].Status)
	require.Equal(t, &decisionReason, got[0].DecisionReason)
	require.Equal(t, must.Slug("nightly-users"), got[0].TriggerSlug)
	require.Equal(t, int64(3), got[0].TriggerRevision)
	require.Equal(t, must.Slug("sync-users"), got[0].PipelineSlug)
	require.Equal(t, runSlug, got[0].RootRunSlug)
	require.Equal(t, runSlug, got[0].RetryOfRunSlug)
	require.Equal(t, must.Slug("retry-run-nightly-users-2-run"), got[0].RunSlug)
	require.Equal(t, 2, got[0].AttemptNumber)
	require.Equal(t, &failureKind, got[0].FailureKind)
	require.Equal(t, &sourceEventSlug, got[0].SourceEventSlug)
	require.Equal(t, scheduledAt, got[0].ScheduledAt)
	require.Equal(t, &enqueuedAt, got[0].EnqueuedAt)
	require.Equal(t, &startedAt, got[0].StartedAt)
	require.Equal(t, &completedAt, got[0].CompletedAt)
	require.Equal(t, &errorSummary, got[0].ErrorSummary)
	require.Equal(t, scheduledAt, got[0].CreatedAt)
	require.Equal(t, updatedAt, got[0].UpdatedAt)
}

func TestListPipelineRunTriggerRetryAttemptsPropagatesRepositoryError(t *testing.T) {
	t.Parallel()

	retryAttemptsErr := xerror.Unexpected().Detail("retry attempt diagnostics unavailable")
	repo := &fakeTriggerRepository{retryAttemptsErr: retryAttemptsErr}
	svc := newTestService(repo)

	_, err := svc.ListPipelineRunTriggerRetryAttempts(
		context.Background(),
		must.Slug("acme"),
		must.Slug("backoffice"),
		must.Slug("prod"),
		must.Slug("run-nightly-users"),
	)

	require.ErrorIs(t, err, retryAttemptsErr)
}

func TestCreatePipelineTriggerRejectsUnsafeRuntimeInputKeys(t *testing.T) {
	t.Parallel()

	repo := &fakeTriggerRepository{projectEnvironmentEnabled: true, pipelineExists: true, deploymentExists: true}
	svc := newTestService(repo)

	secretKey := "api_token"
	_, err := svc.CreatePipelineTrigger(context.Background(), must.Slug("acme"), must.Slug("backoffice"), must.Slug("prod"), model.NewPipelineTrigger{
		Name:         "Nightly Users",
		Type:         model.PipelineTriggerTypeSchedule,
		Enabled:      false,
		PipelineSlug: must.Slug("sync-users"),
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RuntimeInputKeys: []string{secretKey},
		RetryPolicy:      defaultNoRetryPolicy(),
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 * * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}, model.SystemEventActor())

	require.Error(t, err)
	require.True(t, xerror.Is(err, ErrInvalidPipelineTriggerInput))
	require.False(t, repo.createCalled)
}

func TestCreatePipelineTriggerRejectsUnsupportedRetryBackoffStrategy(t *testing.T) {
	t.Parallel()

	repo := &fakeTriggerRepository{projectEnvironmentEnabled: true, pipelineExists: true, deploymentExists: true}
	svc := newTestService(repo)

	_, err := svc.CreatePipelineTrigger(context.Background(), must.Slug("acme"), must.Slug("backoffice"), must.Slug("prod"), model.NewPipelineTrigger{
		Name:         "Nightly Users",
		Type:         model.PipelineTriggerTypeSchedule,
		Enabled:      false,
		PipelineSlug: must.Slug("sync-users"),
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:         false,
			MaxAttempts:     1,
			BackoffStrategy: model.PipelineTriggerBackoffStrategy(99),
		},
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 * * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}, model.SystemEventActor())

	require.Error(t, err)
	require.True(t, xerror.Is(err, ErrInvalidPipelineTriggerInput))
	require.False(t, repo.createCalled)
}

func TestCreatePipelineTriggerRejectsInvalidDefinitions(t *testing.T) {
	t.Parallel()

	pinnedHash := "abc123"
	tooShortElapsed := 30

	tests := []struct {
		name   string
		mutate func(*model.NewPipelineTrigger)
	}{
		{
			name: "MissingScheduleConfig",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.ScheduleConfig = nil
			},
		},
		{
			name: "UnsupportedTriggerType",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.Type = model.PipelineTriggerTypeUnknown
			},
		},
		{
			name: "LatestDeployedCannotPinHash",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.VersionPolicy.PinnedVersionHash = &pinnedHash
			},
		},
		{
			name: "PinnedPolicyRequiresHash",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.VersionPolicy.Mode = model.PipelineTriggerVersionPolicyModePinned
			},
		},
		{
			name: "UnsupportedRetryJitter",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.RetryPolicy.BackoffStrategy = model.PipelineTriggerBackoffStrategyFixed
				trigger.RetryPolicy.InitialDelaySeconds = 60
				trigger.RetryPolicy.MaxDelaySeconds = 60
				trigger.RetryPolicy.Jitter = true
			},
		},
		{
			name: "NoBackoffDisallowsDelay",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.RetryPolicy.InitialDelaySeconds = 1
			},
		},
		{
			name: "FixedBackoffRequiresInitialDelay",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.RetryPolicy.BackoffStrategy = model.PipelineTriggerBackoffStrategyFixed
			},
		},
		{
			name: "RetryEnabledRequiresFailureKinds",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.RetryPolicy.Enabled = true
				trigger.RetryPolicy.MaxAttempts = 2
			},
		},
		{
			name: "MaxElapsedMustCoverInitialDelay",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.RetryPolicy.Enabled = true
				trigger.RetryPolicy.MaxAttempts = 2
				trigger.RetryPolicy.BackoffStrategy = model.PipelineTriggerBackoffStrategyFixed
				trigger.RetryPolicy.InitialDelaySeconds = 60
				trigger.RetryPolicy.MaxDelaySeconds = 60
				trigger.RetryPolicy.RetryableFailureKinds = []model.PipelineTriggerRetryFailureKind{
					model.PipelineTriggerRetryFailureKindPlatform,
				}
				trigger.RetryPolicy.MaxElapsedSeconds = &tooShortElapsed
			},
		},
		{
			name: "InvalidScheduleCron",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.ScheduleConfig.Cron = "not cron"
			},
		},
		{
			name: "InvalidScheduleTimezone",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.ScheduleConfig.Timezone = "Mars/Base"
			},
		},
		{
			name: "UnsupportedScheduleMisfirePolicy",
			mutate: func(trigger *model.NewPipelineTrigger) {
				trigger.ScheduleConfig.MisfirePolicy = model.PipelineTriggerScheduleMisfirePolicy(99)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := &fakeTriggerRepository{projectEnvironmentEnabled: true, pipelineExists: true}
			svc := newTestService(repo)
			trigger := validScheduleTriggerInput()
			tt.mutate(&trigger)

			_, err := svc.CreatePipelineTrigger(context.Background(), must.Slug("acme"), must.Slug("backoffice"), must.Slug("prod"), trigger, model.SystemEventActor())

			require.Error(t, err)
			require.True(t, xerror.Is(err, ErrInvalidPipelineTriggerInput))
			require.False(t, repo.createCalled)
		})
	}
}

func TestCreatePipelineTriggerRequiresDeploymentWhenEnabledLatestDeployed(t *testing.T) {
	t.Parallel()

	repo := &fakeTriggerRepository{
		projectEnvironmentEnabled: true,
		pipelineExists:            true,
		deploymentExists:          false,
	}
	svc := newTestService(repo)

	_, err := svc.CreatePipelineTrigger(context.Background(), must.Slug("acme"), must.Slug("backoffice"), must.Slug("prod"), model.NewPipelineTrigger{
		Name:         "Nightly Users",
		Type:         model.PipelineTriggerTypeSchedule,
		Enabled:      true,
		PipelineSlug: must.Slug("sync-users"),
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RetryPolicy: defaultNoRetryPolicy(),
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 * * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}, model.SystemEventActor())

	require.Error(t, err)
	require.True(t, xerror.Is(err, ErrPipelineDeploymentNotFound))
	require.False(t, repo.createCalled)
}

func TestCreatePipelineWebhookTriggerIssuesOpaqueIdentityAndWriteOnlySecret(t *testing.T) {
	t.Parallel()
	repo := &fakeTriggerRepository{projectEnvironmentEnabled: true, pipelineExists: true, createFn: func(
		_ context.Context, _ postgres.DBTX, _, _, environment slug.Slug, params pipelinetriggers.CreatePipelineTriggerParams,
	) (*pipelinetriggers.PipelineTrigger, error) {
		require.Len(t, params.WebhookConfig.ReceiverID, 43)
		require.Equal(t, "hmac_sha256", params.WebhookConfig.VerificationStrategy)
		return repoTriggerFromCreateParams(environment, params), nil
	}}
	secrets := fakeWebhookSecrets{issueFn: func(_ context.Context, params credentialssvc.PipelineWebhookSecretParams) (credentialssvc.IssuedPipelineWebhookSecret, error) {
		require.Equal(t, must.Slug("incoming"), params.TriggerSlug)
		return credentialssvc.IssuedPipelineWebhookSecret{KeyVersion: 2, Value: "write-only-value"}, nil
	}}
	svc := newTestServiceWithSecrets(repo, secrets)
	got, err := svc.CreatePipelineTrigger(context.Background(), must.Slug("acme"), must.Slug("ops"), must.Slug("prod"), model.NewPipelineTrigger{
		Name: "Incoming", Slug: must.Slug("incoming"), Type: model.PipelineTriggerTypeWebhook,
		PipelineSlug: must.Slug("sync"), VersionPolicy: model.PipelineTriggerVersionPolicy{Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed},
		RuntimeInputKeys: []string{"tenant_id"}, RetryPolicy: defaultNoRetryPolicy(),
		WebhookConfig: &model.PipelineTriggerWebhookConfig{InputMappings: []model.PipelineWebhookInputMapping{{JSONPointer: "/tenant/id", RuntimeInputKey: "tenant_id"}}},
	}, model.SystemEventActor())
	require.NoError(t, err)
	require.Equal(t, &model.PipelineWebhookSecret{KeyVersion: 2, Value: "write-only-value"}, got.WebhookSecret)
	require.Equal(t, []int{2}, got.WebhookConfig.EnabledKeyVersions)
}

func TestRotatePipelineWebhookSecret(t *testing.T) {
	t.Parallel()
	org, project, environment, trigger := must.Slug("acme"), must.Slug("ops"), must.Slug("prod"), must.Slug("incoming")
	actor := model.UserEventActor(must.Slug("alice"))
	repo := &fakeTriggerRepository{existing: &pipelinetriggers.PipelineTrigger{Type: model.PipelineTriggerTypeWebhook}}
	secrets := fakeWebhookSecrets{issueFn: func(_ context.Context, params credentialssvc.PipelineWebhookSecretParams) (credentialssvc.IssuedPipelineWebhookSecret, error) {
		require.Equal(t, credentialssvc.PipelineWebhookSecretParams{
			OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, TriggerSlug: trigger,
		}, params)
		return credentialssvc.IssuedPipelineWebhookSecret{KeyVersion: 3, Value: "rotated-secret"}, nil
	}}

	got, err := newTestServiceWithSecrets(repo, secrets).RotatePipelineWebhookSecret(
		context.Background(), org, project, environment, trigger, actor,
	)

	require.NoError(t, err)
	require.Equal(t, model.PipelineWebhookSecret{KeyVersion: 3, Value: "rotated-secret"}, got)
}

func TestRotatePipelineWebhookSecretRejectsScheduleTrigger(t *testing.T) {
	t.Parallel()
	issued := false
	repo := &fakeTriggerRepository{existing: &pipelinetriggers.PipelineTrigger{Type: model.PipelineTriggerTypeSchedule}}
	secrets := fakeWebhookSecrets{issueFn: func(context.Context, credentialssvc.PipelineWebhookSecretParams) (credentialssvc.IssuedPipelineWebhookSecret, error) {
		issued = true
		return credentialssvc.IssuedPipelineWebhookSecret{}, nil
	}}

	_, err := newTestServiceWithSecrets(repo, secrets).RotatePipelineWebhookSecret(
		context.Background(), must.Slug("acme"), must.Slug("ops"), must.Slug("prod"), must.Slug("nightly"), model.SystemEventActor(),
	)

	require.True(t, xerror.Is(err, ErrInvalidPipelineTriggerInput))
	require.False(t, issued)
}

func TestRotatePipelineWebhookSecretPropagatesIssuanceFailure(t *testing.T) {
	t.Parallel()
	wantErr := xerror.Unexpected().Detail("issue webhook secret")
	repo := &fakeTriggerRepository{existing: &pipelinetriggers.PipelineTrigger{Type: model.PipelineTriggerTypeWebhook}}
	secrets := fakeWebhookSecrets{issueFn: func(context.Context, credentialssvc.PipelineWebhookSecretParams) (credentialssvc.IssuedPipelineWebhookSecret, error) {
		return credentialssvc.IssuedPipelineWebhookSecret{}, wantErr
	}}

	_, err := newTestServiceWithSecrets(repo, secrets).RotatePipelineWebhookSecret(
		context.Background(), must.Slug("acme"), must.Slug("ops"), must.Slug("prod"), must.Slug("incoming"), model.SystemEventActor(),
	)

	require.ErrorIs(t, err, wantErr)
}

func TestDeletePipelineWebhookTriggerRevokesBeforeDeleting(t *testing.T) {
	t.Parallel()
	var calls []string
	org, project, environment, trigger := must.Slug("acme"), must.Slug("ops"), must.Slug("prod"), must.Slug("incoming")
	actor := model.UserEventActor(must.Slug("alice"))
	repo := &fakeTriggerRepository{
		existing: &pipelinetriggers.PipelineTrigger{Slug: trigger, Type: model.PipelineTriggerTypeWebhook},
		deleteFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error {
			calls = append(calls, "delete")
			return nil
		},
	}
	secrets := fakeWebhookSecrets{revokeFn: func(_ context.Context, params credentialssvc.PipelineWebhookSecretParams) error {
		require.Equal(t, credentialssvc.PipelineWebhookSecretParams{
			OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, TriggerSlug: trigger,
		}, params)
		calls = append(calls, "revoke")
		return nil
	}}

	err := newTestServiceWithSecrets(repo, secrets).DeletePipelineTrigger(context.Background(), org, project, environment, trigger, actor)

	require.NoError(t, err)
	require.Equal(t, []string{"revoke", "delete"}, calls)
}

func TestDeletePipelineWebhookTriggerDoesNotDeleteWhenRevocationFails(t *testing.T) {
	t.Parallel()
	wantErr := xerror.Unexpected().Detail("revoke webhook secret")
	repo := &fakeTriggerRepository{existing: &pipelinetriggers.PipelineTrigger{Type: model.PipelineTriggerTypeWebhook}}
	secrets := fakeWebhookSecrets{revokeFn: func(context.Context, credentialssvc.PipelineWebhookSecretParams) error { return wantErr }}

	err := newTestServiceWithSecrets(repo, secrets).DeletePipelineTrigger(
		context.Background(), must.Slug("acme"), must.Slug("ops"), must.Slug("prod"), must.Slug("incoming"), model.SystemEventActor(),
	)

	require.ErrorIs(t, err, wantErr)
	require.False(t, repo.deleteCalled)
}

func newTestService(repo *fakeTriggerRepository) Service {
	return newTestServiceWithSecrets(repo, fakeWebhookSecrets{})
}

func newTestServiceWithSecrets(repo *fakeTriggerRepository, secrets credentialssvc.PipelineWebhookSecretLifecycleService) Service {
	return New(
		true,
		testutil.FakeTransactionManager{},
		repo,
		fakeCompletionValidator{},
		eventtest.NewProducer(nil),
		secrets,
	)
}

type fakeWebhookSecrets struct {
	issueFn  func(context.Context, credentialssvc.PipelineWebhookSecretParams) (credentialssvc.IssuedPipelineWebhookSecret, error)
	revokeFn func(context.Context, credentialssvc.PipelineWebhookSecretParams) error
}

func (f fakeWebhookSecrets) IssuePipelineWebhookSecret(ctx context.Context, params credentialssvc.PipelineWebhookSecretParams) (credentialssvc.IssuedPipelineWebhookSecret, error) {
	if f.issueFn != nil {
		return f.issueFn(ctx, params)
	}
	return credentialssvc.IssuedPipelineWebhookSecret{KeyVersion: 1, Value: "test-webhook-secret"}, nil
}

func (f fakeWebhookSecrets) RevokePipelineWebhookSecrets(ctx context.Context, params credentialssvc.PipelineWebhookSecretParams) error {
	if f.revokeFn != nil {
		return f.revokeFn(ctx, params)
	}
	return nil
}

type fakeTriggerRepository struct {
	snapshot                  *model.PipelineCompletionSnapshot
	refusalReason             string
	protected                 bool
	edges                     []pipelinetriggers.CompletionEdge
	projectEnvironmentEnabled bool
	pipelineExists            bool
	pinnedVersionHash         string
	pinnedVersionErr          error
	deploymentExists          bool
	existing                  *pipelinetriggers.PipelineTrigger
	scheduleFires             []pipelinetriggers.ScheduleFireDiagnostic
	scheduleFiresErr          error
	scheduleFiresRequest      model.ListRequest
	retryAttempts             []pipelinetriggers.TriggerRetryAttemptDiagnostic
	retryAttemptsErr          error
	createCalled              bool
	deleteCalled              bool
	createFn                  func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, pipelinetriggers.CreatePipelineTriggerParams) (*pipelinetriggers.PipelineTrigger, error)
	updateFn                  func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, pipelinetriggers.UpdatePipelineTriggerParams) (*pipelinetriggers.PipelineTrigger, error)
	deleteFn                  func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error
}

func (r *fakeTriggerRepository) ProjectEnvironmentEnabled(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (bool, error) {
	return r.projectEnvironmentEnabled, nil
}

func (r *fakeTriggerRepository) PipelineExists(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (bool, error) {
	return r.pipelineExists, nil
}

func (r *fakeTriggerRepository) ResolvePinnedVersionHash(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	string,
) (string, error) {
	if r.pinnedVersionErr != nil {
		return "", r.pinnedVersionErr
	}
	if r.pinnedVersionHash == "" {
		return "", pipelinetriggers.ErrPipelineVersionNotFound
	}
	return r.pinnedVersionHash, nil
}

func (r *fakeTriggerRepository) DeploymentExists(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (bool, error) {
	return r.deploymentExists, nil
}

func (r *fakeTriggerRepository) CreatePipelineTrigger(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params pipelinetriggers.CreatePipelineTriggerParams,
) (*pipelinetriggers.PipelineTrigger, error) {
	r.createCalled = true
	if r.createFn != nil {
		return r.createFn(ctx, tx, orgSlug, projectSlug, environmentSlug, params)
	}
	return repoTriggerFromCreateParams(environmentSlug, params), nil
}

func (r *fakeTriggerRepository) FindPipelineTriggerBySlug(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*pipelinetriggers.PipelineTrigger, error) {
	if r.existing != nil {
		return r.existing, nil
	}
	return nil, pipelinetriggers.ErrPipelineTriggerNotFound
}

func (r *fakeTriggerRepository) ListPipelineTriggers(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	model.ListRequest,
) ([]*pipelinetriggers.PipelineTrigger, error) {
	if r.existing == nil {
		return nil, nil
	}
	return []*pipelinetriggers.PipelineTrigger{r.existing}, nil
}

func (r *fakeTriggerRepository) ListScheduleFiresForTrigger(
	_ context.Context,
	_ postgres.DBTX,
	_, _, _, _ slug.Slug,
	req model.ListRequest,
) ([]pipelinetriggers.ScheduleFireDiagnostic, error) {
	r.scheduleFiresRequest = req
	if r.scheduleFiresErr != nil {
		return nil, r.scheduleFiresErr
	}
	return r.scheduleFires, nil
}

func (r *fakeTriggerRepository) ListTriggerRetryAttemptsForRun(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) ([]pipelinetriggers.TriggerRetryAttemptDiagnostic, error) {
	if r.retryAttemptsErr != nil {
		return nil, r.retryAttemptsErr
	}
	return r.retryAttempts, nil
}

func (r *fakeTriggerRepository) UpdatePipelineTrigger(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	params pipelinetriggers.UpdatePipelineTriggerParams,
) (*pipelinetriggers.PipelineTrigger, error) {
	if r.updateFn != nil {
		return r.updateFn(ctx, tx, orgSlug, projectSlug, environmentSlug, triggerSlug, params)
	}
	return repoTriggerFromUpdateParams(environmentSlug, triggerSlug, params), nil
}

func (r *fakeTriggerRepository) DeletePipelineTrigger(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
) error {
	r.deleteCalled = true
	if r.deleteFn != nil {
		return r.deleteFn(ctx, tx, orgSlug, projectSlug, environmentSlug, triggerSlug)
	}
	return nil
}

func (r *fakeTriggerRepository) ListDueScheduleTriggersForSystem(
	context.Context,
	postgres.DBTX,
) ([]pipelinetriggers.DueScheduleTrigger, error) {
	return nil, nil
}

func (r *fakeTriggerRepository) ClaimScheduleFire(
	context.Context,
	postgres.DBTX,
	pipelinetriggers.ScheduleFireClaim,
) (pipelinetriggers.ScheduleFireClaimResult, error) {
	return pipelinetriggers.ScheduleFireClaimResult{}, nil
}

func (r *fakeTriggerRepository) LoadClaimedScheduleFire(
	context.Context,
	postgres.DBTX,
	pipelinetriggers.ScheduleFireExecutionRef,
) (pipelinetriggers.ClaimedScheduleFire, error) {
	return pipelinetriggers.ClaimedScheduleFire{}, pipelinetriggers.ErrPipelineTriggerNotFound
}

func (r *fakeTriggerRepository) MarkScheduleFireRunning(
	context.Context,
	postgres.DBTX,
	pipelinetriggers.ScheduleFireExecutionRef,
) error {
	return nil
}

func (r *fakeTriggerRepository) CompleteScheduleFire(
	context.Context,
	postgres.DBTX,
	pipelinetriggers.ScheduleFireCompletion,
) error {
	return nil
}

func (r *fakeTriggerRepository) RecordTriggerRetryAttempt(
	context.Context,
	postgres.DBTX,
	pipelinetriggers.TriggerRetryAttemptRecord,
) (pipelinetriggers.TriggerRetryAttempt, error) {
	return pipelinetriggers.TriggerRetryAttempt{}, nil
}

func (r *fakeTriggerRepository) LoadTriggerRetryAttemptForExecution(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (pipelinetriggers.TriggerRetryAttempt, error) {
	return pipelinetriggers.TriggerRetryAttempt{}, pipelinetriggers.ErrPipelineTriggerNotFound
}

func (r *fakeTriggerRepository) MarkTriggerRetryAttemptRunning(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) error {
	return nil
}

func (r *fakeTriggerRepository) CompleteTriggerRetryAttempt(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	model.PipelineTriggerRetryAttemptStatus,
	*slug.Slug,
	*string,
) error {
	return nil
}

func repoTriggerFromCreateParams(
	environmentSlug slug.Slug,
	params pipelinetriggers.CreatePipelineTriggerParams,
) *pipelinetriggers.PipelineTrigger {
	return &pipelinetriggers.PipelineTrigger{
		Slug:             params.Slug,
		Name:             params.Name,
		Type:             params.Type,
		Enabled:          params.Enabled,
		Revision:         1,
		EnvironmentSlug:  environmentSlug,
		PipelineSlug:     params.PipelineSlug,
		VersionPolicy:    params.VersionPolicy,
		RuntimeInputKeys: append([]string(nil), params.RuntimeInputKeys...),
		RetryPolicy:      params.RetryPolicy,
		ScheduleConfig:   params.ScheduleConfig,
		WebhookConfig:    params.WebhookConfig,
		CreatedBy:        params.Actor,
		UpdatedBy:        params.Actor,
		CreatedAt:        fixedTestTime(),
		UpdatedAt:        fixedTestTime(),
	}
}

func repoTriggerFromUpdateParams(
	environmentSlug, triggerSlug slug.Slug,
	params pipelinetriggers.UpdatePipelineTriggerParams,
) *pipelinetriggers.PipelineTrigger {
	return &pipelinetriggers.PipelineTrigger{
		Slug:             triggerSlug,
		Name:             params.Name,
		Type:             params.Type,
		Enabled:          params.Enabled,
		Revision:         2,
		EnvironmentSlug:  environmentSlug,
		PipelineSlug:     params.PipelineSlug,
		VersionPolicy:    params.VersionPolicy,
		RuntimeInputKeys: append([]string(nil), params.RuntimeInputKeys...),
		RetryPolicy:      params.RetryPolicy,
		ScheduleConfig:   params.ScheduleConfig,
		WebhookConfig:    params.WebhookConfig,
		CreatedBy:        model.SystemEventActor(),
		UpdatedBy:        params.Actor,
		CreatedAt:        fixedTestTime(),
		UpdatedAt:        fixedTestTime().Add(time.Minute),
	}
}

func defaultNoRetryPolicy() model.PipelineTriggerRetryPolicy {
	return model.PipelineTriggerRetryPolicy{
		Enabled:         false,
		MaxAttempts:     1,
		BackoffStrategy: model.PipelineTriggerBackoffStrategyNone,
	}
}

func fixedTestTime() time.Time {
	return time.Date(2026, time.June, 7, 12, 0, 0, 0, time.UTC)
}

func ptrString(value string) *string {
	return &value
}

func validScheduleTriggerInput() model.NewPipelineTrigger {
	return model.NewPipelineTrigger{
		Name:         "Nightly Users",
		Type:         model.PipelineTriggerTypeSchedule,
		Enabled:      false,
		PipelineSlug: must.Slug("sync-users"),
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RetryPolicy: defaultNoRetryPolicy(),
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 * * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}
}

func (r *fakeTriggerRepository) ReconcileLogicalRunFinalizationsForSystem(context.Context, postgres.DBTX) error {
	return nil
}

func (r *fakeTriggerRepository) ListPendingRunFinalizationsForSystem(context.Context, postgres.DBTX) ([]pipelinetriggers.PendingRunFinalization, error) {
	return nil, nil
}

func (r *fakeTriggerRepository) ListDelayedScheduleFiresForSystem(context.Context, postgres.DBTX, time.Time) ([]pipelinetriggers.DelayedScheduleFire, error) {
	return nil, nil
}

func (r *fakeTriggerRepository) LockPipelineTriggerTopology(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (bool, error) {
	return r.protected, nil
}

func (r *fakeTriggerRepository) ListPipelineCompletionEdges(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) ([]pipelinetriggers.CompletionEdge, error) {
	return r.edges, nil
}

type fakeCompletionValidator struct{ err error }

func (v fakeCompletionValidator) ValidateCompletionExecutionContext(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot) error {
	return v.err
}

func (r *fakeTriggerRepository) LoadCompletionSnapshot(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, int) (*model.PipelineCompletionSnapshot, error) {
	return r.snapshot, nil
}

func (r *fakeTriggerRepository) RefuseCompletionSnapshot(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ int, reason string) error {
	r.refusalReason = reason
	return nil
}
