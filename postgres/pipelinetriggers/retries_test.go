package pipelinetriggers

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func TestTriggerRetryAttemptRecord_AttemptSlugBoundsLongRootSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		record TriggerRetryAttemptRecord
	}{
		{
			name: "BoundsLongRootSlug",
			record: TriggerRetryAttemptRecord{
				RootRunSlug:   slug.Slug(strings.Repeat("a", 100)),
				AttemptNumber: 12,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := test.record.AttemptSlug()

			require.NoError(t, err)
			require.LessOrEqual(t, len(got.String()), 100)
			require.NotEmpty(t, got.String())
		})
	}
}

func TestRetryAttemptFromExecutionRowHydratesProjection(t *testing.T) {
	t.Parallel()

	scheduledAt := time.Date(2026, time.June, 9, 7, 1, 0, 0, time.UTC)
	startedAt := scheduledAt.Add(5 * time.Second)
	completedAt := startedAt.Add(3 * time.Second)
	createdAt := scheduledAt.Add(-time.Second)
	pinnedHash := "version-pinned"
	errorSummary := "step failed"

	tests := []struct {
		name string
		row  dbgen.LoadTriggerRetryAttemptForExecutionRow
	}{
		{
			name: "HydratesFailedPinnedScheduleRetry",
			row: dbgen.LoadTriggerRetryAttemptForExecutionRow{
				AttemptSlug:                   "retry-fire-daily-users-20260609-2",
				Status:                        "failed",
				DecisionReason:                pgtype.Text{String: "worker_failed", Valid: true},
				OrgSlug:                       slug.Slug("acme"),
				ProjectSlug:                   slug.Slug("default"),
				EnvironmentSlug:               slug.Slug("prod"),
				TriggerSlug:                   slug.Slug("daily-users"),
				TriggerRevision:               7,
				PipelineSlug:                  "sync-users",
				VersionPolicy:                 "pinned",
				PinnedVersionHash:             pgtype.Text{String: pinnedHash, Valid: true},
				PipelineVersionSpecHash:       "spec-hash-123",
				RuntimeInputKeys:              []string{"customer_id", "dry_run"},
				RetryEnabled:                  true,
				RetryMaxAttempts:              3,
				RetryBackoffStrategy:          "fixed",
				RetryInitialDelaySeconds:      60,
				RetryMaxDelaySeconds:          120,
				RetryJitter:                   true,
				RetryableFailureKinds:         []string{"data_source"},
				RetryMaxElapsedSeconds:        pgtype.Int4{Int32: 3600, Valid: true},
				SourceType:                    "schedule",
				SourceSummary:                 "Scheduled run from trigger daily-users",
				SourceEntrypoint:              "scheduler",
				SourceActorType:               "system",
				SourceRequestID:               pgtype.Text{String: "req-123", Valid: true},
				SourceTraceID:                 pgtype.Text{String: "trace-123", Valid: true},
				SourceEventSlug:               pgtype.Text{String: "evt-source-run", Valid: true},
				SourceParameterKeys:           []string{"customer_id"},
				SourceScheduleTriggerSlug:     pgtype.Text{String: "daily-users", Valid: true},
				SourceScheduleTriggerRevision: pgtype.Int8{Int64: 7, Valid: true},
				SourceScheduleCron:            pgtype.Text{String: "0 7 * * *", Valid: true},
				SourceScheduleTimezone:        pgtype.Text{String: "UTC", Valid: true},
				SourceScheduleScheduledFireAt: pgtype.Timestamptz{Time: scheduledAt.Add(-time.Minute), Valid: true},
				SourceScheduleActualFireAt:    pgtype.Timestamptz{Time: scheduledAt.Add(-time.Minute), Valid: true},
				SourceScheduleMisfireResult:   pgtype.Text{String: "on_time", Valid: true},
				RootRunSlug:                   "fire-daily-users-20260609",
				RetryOfRunSlug:                "fire-daily-users-20260609",
				RunSlug:                       "fire-daily-users-20260609-retry-2",
				AttemptNumber:                 2,
				FailureKind:                   pgtype.Text{String: "data_source", Valid: true},
				ScheduledAt:                   pgtype.Timestamptz{Time: scheduledAt, Valid: true},
				StartedAt:                     pgtype.Timestamptz{Time: startedAt, Valid: true},
				CompletedAt:                   pgtype.Timestamptz{Time: completedAt, Valid: true},
				ErrorSummary:                  pgtype.Text{String: errorSummary, Valid: true},
				CreatedAt:                     pgtype.Timestamptz{Time: createdAt, Valid: true},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := retryAttemptFromExecutionRow(test.row)

			require.NoError(t, err)
			require.Equal(t, slug.Slug("retry-fire-daily-users-20260609-2"), got.AttemptSlug)
			require.Equal(t, model.PipelineTriggerRetryAttemptStatusFailed, got.Status)
			require.Equal(t, "worker_failed", got.DecisionReason)
			require.Equal(t, slug.Slug("acme"), got.OrgSlug)
			require.Equal(t, slug.Slug("sync-users"), got.PipelineSlug)
			require.Equal(t, model.PipelineTriggerVersionPolicyModePinned, got.VersionPolicy.Mode)
			require.Equal(t, &pinnedHash, got.VersionPolicy.PinnedVersionHash)
			require.Equal(t, "spec-hash-123", got.PipelineVersionSpecHash)
			require.Equal(t, []string{"customer_id", "dry_run"}, got.RuntimeInputKeys)
			require.True(t, got.RetryPolicy.Enabled)
			require.Equal(t, 3, got.RetryPolicy.MaxAttempts)
			require.Equal(t, model.PipelineTriggerBackoffStrategyFixed, got.RetryPolicy.BackoffStrategy)
			require.Equal(t, []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindDataSource}, got.RetryPolicy.RetryableFailureKinds)
			require.NotNil(t, got.RetryPolicy.MaxElapsedSeconds)
			require.Equal(t, 3600, *got.RetryPolicy.MaxElapsedSeconds)
			require.Equal(t, model.PipelineRunSourceTypeSchedule, got.Source.Type)
			require.Equal(t, 2, got.Source.AttemptNumber)
			require.Equal(t, slug.Slug("fire-daily-users-20260609"), got.Source.RootRunSlug)
			require.NotNil(t, got.Source.RetryOfRunSlug)
			require.Equal(t, slug.Slug("fire-daily-users-20260609"), *got.Source.RetryOfRunSlug)
			require.NotNil(t, got.Source.Schedule)
			require.Equal(t, slug.Slug("daily-users"), got.Source.Schedule.TriggerSlug)
			require.Equal(t, model.PipelineTriggerScheduleMisfireResultOnTime, got.Source.Schedule.MisfireResult)
			require.Equal(t, model.PipelineTriggerRetryFailureKindDataSource, got.FailureKind)
			require.True(t, got.ScheduledAt.Equal(scheduledAt))
			require.NotNil(t, got.StartedAt)
			require.True(t, got.StartedAt.Equal(startedAt))
			require.NotNil(t, got.CompletedAt)
			require.True(t, got.CompletedAt.Equal(completedAt))
			require.Equal(t, &errorSummary, got.ErrorSummary)
			require.True(t, got.CreatedAt.Equal(createdAt))
		})
	}
}

func TestRetryAttemptFromExecutionRowRejectsInvalidEnumFields(t *testing.T) {
	t.Parallel()

	scheduledAt := time.Date(2026, time.June, 9, 7, 1, 0, 0, time.UTC)
	baseRow := func() dbgen.LoadTriggerRetryAttemptForExecutionRow {
		return dbgen.LoadTriggerRetryAttemptForExecutionRow{
			AttemptSlug:             "retry-fire-daily-users-20260609-2",
			Status:                  "queued",
			OrgSlug:                 slug.Slug("acme"),
			ProjectSlug:             slug.Slug("default"),
			EnvironmentSlug:         slug.Slug("prod"),
			TriggerSlug:             slug.Slug("daily-users"),
			TriggerRevision:         7,
			PipelineSlug:            "sync-users",
			VersionPolicy:           "latest",
			PipelineVersionSpecHash: "spec-hash-123",
			RetryBackoffStrategy:    "none",
			SourceType:              "manual",
			SourceSummary:           "Manual run",
			SourceEntrypoint:        "ui",
			SourceActorType:         "user",
			RootRunSlug:             "fire-daily-users-20260609",
			RetryOfRunSlug:          "fire-daily-users-20260609",
			RunSlug:                 "fire-daily-users-20260609-retry-2",
			AttemptNumber:           2,
			ScheduledAt:             pgtype.Timestamptz{Time: scheduledAt, Valid: true},
			CreatedAt:               pgtype.Timestamptz{Time: scheduledAt, Valid: true},
		}
	}

	tests := []struct {
		name   string
		mutate func(*dbgen.LoadTriggerRetryAttemptForExecutionRow)
	}{
		{
			name: "InvalidAttemptStatus",
			mutate: func(row *dbgen.LoadTriggerRetryAttemptForExecutionRow) {
				row.Status = "not-a-status"
			},
		},
		{
			name: "InvalidVersionPolicy",
			mutate: func(row *dbgen.LoadTriggerRetryAttemptForExecutionRow) {
				row.VersionPolicy = "not-a-version-policy"
			},
		},
		{
			name: "InvalidBackoffStrategy",
			mutate: func(row *dbgen.LoadTriggerRetryAttemptForExecutionRow) {
				row.RetryBackoffStrategy = "not-a-backoff"
			},
		},
		{
			name: "InvalidRetryableFailureKind",
			mutate: func(row *dbgen.LoadTriggerRetryAttemptForExecutionRow) {
				row.RetryableFailureKinds = []string{"not-a-failure-kind"}
			},
		},
		{
			name: "InvalidFailureKind",
			mutate: func(row *dbgen.LoadTriggerRetryAttemptForExecutionRow) {
				row.FailureKind = pgtype.Text{String: "not-a-failure-kind", Valid: true}
			},
		},
		{
			name: "InvalidSourceType",
			mutate: func(row *dbgen.LoadTriggerRetryAttemptForExecutionRow) {
				row.SourceType = "not-a-source-type"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			row := baseRow()
			test.mutate(&row)

			_, err := retryAttemptFromExecutionRow(row)

			require.Error(t, err)
		})
	}
}

func TestRetryAttemptDiagnosticFromRowHydratesVisibilityProjection(t *testing.T) {
	t.Parallel()

	scheduledAt := time.Date(2026, time.June, 9, 7, 1, 0, 0, time.UTC)
	enqueuedAt := scheduledAt.Add(time.Second)
	startedAt := scheduledAt.Add(2 * time.Second)
	completedAt := scheduledAt.Add(3 * time.Second)
	createdAt := scheduledAt.Add(-time.Minute)
	updatedAt := completedAt.Add(time.Second)
	decisionReason := "max_attempts_exhausted"
	errorSummary := "connector timeout"

	tests := []struct {
		name string
		row  dbgen.ListTriggerRetryAttemptsForRunRow
	}{
		{
			name: "HydratesFailedRetryVisibility",
			row: dbgen.ListTriggerRetryAttemptsForRunRow{
				AttemptSlug:     "retry-fire-daily-users-20260609-2",
				Status:          "failed",
				DecisionReason:  pgtype.Text{String: decisionReason, Valid: true},
				TriggerSlug:     "daily-users",
				TriggerRevision: 7,
				PipelineSlug:    "sync-users",
				RootRunSlug:     "fire-daily-users-20260609",
				RetryOfRunSlug:  "fire-daily-users-20260609",
				RunSlug:         "fire-daily-users-20260609-retry-2",
				AttemptNumber:   2,
				FailureKind:     pgtype.Text{String: "data_source", Valid: true},
				SourceEventSlug: pgtype.Text{String: "evt-retry-run-2", Valid: true},
				ScheduledAt:     pgtype.Timestamptz{Time: scheduledAt, Valid: true},
				EnqueuedAt:      pgtype.Timestamptz{Time: enqueuedAt, Valid: true},
				StartedAt:       pgtype.Timestamptz{Time: startedAt, Valid: true},
				CompletedAt:     pgtype.Timestamptz{Time: completedAt, Valid: true},
				ErrorSummary:    pgtype.Text{String: errorSummary, Valid: true},
				CreatedAt:       pgtype.Timestamptz{Time: createdAt, Valid: true},
				UpdatedAt:       pgtype.Timestamptz{Time: updatedAt, Valid: true},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := retryAttemptDiagnosticFromRow(test.row)

			require.NoError(t, err)
			require.Equal(t, slug.Slug("retry-fire-daily-users-20260609-2"), got.AttemptSlug)
			require.Equal(t, model.PipelineTriggerRetryAttemptStatusFailed, got.Status)
			require.Equal(t, &decisionReason, got.DecisionReason)
			require.Equal(t, slug.Slug("daily-users"), got.TriggerSlug)
			require.Equal(t, slug.Slug("sync-users"), got.PipelineSlug)
			require.Equal(t, slug.Slug("fire-daily-users-20260609"), got.RootRunSlug)
			require.Equal(t, slug.Slug("fire-daily-users-20260609"), got.RetryOfRunSlug)
			require.Equal(t, slug.Slug("fire-daily-users-20260609-retry-2"), got.RunSlug)
			require.Equal(t, 2, got.AttemptNumber)
			require.NotNil(t, got.FailureKind)
			require.Equal(t, model.PipelineTriggerRetryFailureKindDataSource, *got.FailureKind)
			require.NotNil(t, got.SourceEventSlug)
			require.Equal(t, slug.Slug("evt-retry-run-2"), *got.SourceEventSlug)
			require.True(t, got.ScheduledAt.Equal(scheduledAt))
			require.True(t, got.EnqueuedAt.Equal(enqueuedAt))
			require.True(t, got.StartedAt.Equal(startedAt))
			require.True(t, got.CompletedAt.Equal(completedAt))
			require.Equal(t, &errorSummary, got.ErrorSummary)
			require.True(t, got.CreatedAt.Equal(createdAt))
			require.True(t, got.UpdatedAt.Equal(updatedAt))
		})
	}
}

func TestRetryAttemptCompletionStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status model.PipelineTriggerRetryAttemptStatus
		want   bool
	}{
		{
			name:   "Succeeded",
			status: model.PipelineTriggerRetryAttemptStatusSucceeded,
			want:   true,
		},
		{
			name:   "Failed",
			status: model.PipelineTriggerRetryAttemptStatusFailed,
			want:   true,
		},
		{
			name:   "Queued",
			status: model.PipelineTriggerRetryAttemptStatusQueued,
			want:   false,
		},
		{
			name:   "Skipped",
			status: model.PipelineTriggerRetryAttemptStatusSkipped,
			want:   false,
		},
		{
			name:   "Exhausted",
			status: model.PipelineTriggerRetryAttemptStatusExhausted,
			want:   false,
		},
		{
			name:   "Running",
			status: model.PipelineTriggerRetryAttemptStatusRunning,
			want:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := retryAttemptCompletionStatus(test.status)

			require.Equal(t, test.want, got)
		})
	}
}

func TestRetryAttemptFromRecordCopiesDurableFields(t *testing.T) {
	t.Parallel()

	scheduledAt := time.Date(2026, time.June, 9, 7, 1, 0, 0, time.UTC)
	recordScheduledAt := scheduledAt.Add(-time.Hour)
	runSlug := "fire-daily-users-20260609-retry-2"
	recordRunSlug := slug.Slug("record-run-slug-should-not-win")

	tests := []struct {
		name        string
		record      TriggerRetryAttemptRecord
		attemptSlug string
		runSlug     string
		scheduledAt time.Time
	}{
		{
			name: "UsesExplicitRunSlugAndScheduledAt",
			record: TriggerRetryAttemptRecord{
				OrgSlug:                 slug.Slug("acme"),
				ProjectSlug:             slug.Slug("default"),
				EnvironmentSlug:         slug.Slug("prod"),
				TriggerSlug:             slug.Slug("daily-users"),
				TriggerRevision:         7,
				Status:                  model.PipelineTriggerRetryAttemptStatusQueued,
				PipelineSlug:            slug.Slug("sync-users"),
				PipelineVersionSpecHash: "spec-hash-123",
				RuntimeInputKeys:        []string{"customer_id"},
				RetryPolicy: model.PipelineTriggerRetryPolicy{
					Enabled:         true,
					MaxAttempts:     3,
					BackoffStrategy: model.PipelineTriggerBackoffStrategyFixed,
				},
				Source: pipelinerun.RunSourceSummary{
					Type:          model.PipelineRunSourceTypeSchedule,
					Summary:       "Scheduled run",
					Entrypoint:    model.PipelineRunSourceEntrypointScheduler,
					ActorType:     model.EventActorTypeSystem,
					AttemptNumber: 2,
					RootRunSlug:   slug.Slug("fire-daily-users-20260609"),
				},
				RootRunSlug:    slug.Slug("fire-daily-users-20260609"),
				RetryOfRunSlug: slug.Slug("fire-daily-users-20260609"),
				RunSlug:        recordRunSlug,
				AttemptNumber:  2,
				FailureKind:    model.PipelineTriggerRetryFailureKindDataSource,
				ScheduledAt:    recordScheduledAt,
			},
			attemptSlug: "retry-fire-daily-users-20260609-2",
			runSlug:     runSlug,
			scheduledAt: scheduledAt,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := retryAttemptFromRecord(
				test.record,
				test.attemptSlug,
				test.runSlug,
				test.scheduledAt,
			)

			require.Equal(t, slug.Slug("retry-fire-daily-users-20260609-2"), got.AttemptSlug)
			require.Equal(t, test.record.OrgSlug, got.OrgSlug)
			require.Equal(t, test.record.PipelineSlug, got.PipelineSlug)
			require.Equal(t, test.record.PipelineVersionSpecHash, got.PipelineVersionSpecHash)
			require.Equal(t, test.record.RuntimeInputKeys, got.RuntimeInputKeys)
			require.Equal(t, test.record.RetryPolicy, got.RetryPolicy)
			require.Equal(t, test.record.Source, got.Source)
			require.Equal(t, test.record.RootRunSlug, got.RootRunSlug)
			require.Equal(t, test.record.RetryOfRunSlug, got.RetryOfRunSlug)
			require.Equal(t, slug.FromString(test.runSlug), got.RunSlug)
			require.NotEqual(t, test.record.RunSlug, got.RunSlug)
			require.Equal(t, test.record.AttemptNumber, got.AttemptNumber)
			require.True(t, got.ScheduledAt.Equal(test.scheduledAt))
			require.False(t, got.ScheduledAt.Equal(test.record.ScheduledAt))
		})
	}
}
