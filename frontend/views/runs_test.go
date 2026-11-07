package views

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/runlineage"
)

func TestLineageDisplayHelpersUseSourceLanguageAndSanitizedEvidence(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Source", lineageKindLabel("query"))
	require.Equal(t, "Transform", lineageKindLabel("transform"))
	require.Equal(t, "Sink", lineageKindLabel("sink"))
	require.Equal(t, "Custom Step", lineageKindLabel("custom_step"))
	require.Equal(t, "Step", lineageKindLabel(" "))
	require.Equal(t, "badge badge-primary badge-outline badge-xs", lineageKindBadgeClass("query"))
	require.Equal(t, "block h-1 bg-info", lineageKindRailClass("transform"))
	require.Equal(t, "h-2.5 w-2.5 rounded-full bg-success", lineageKindDotClass("sink"))
	require.Equal(t, 2, lineageKindCount([]runlineage.Node{{Kind: "query"}, {Kind: "sink"}, {Kind: "query"}}, "query"))

	operationType := "sql"
	operationRef := api.Slug("list-users")
	dataSourceSlug := api.Slug("source-postgres")
	outputDetails := api.PipelineRunStepOutputDetails{}
	require.NoError(t, outputDetails.FromPipelineRunRowsOutputDetails(api.PipelineRunRowsOutputDetails{RowCount: 3}))
	snapshot := &api.PipelineRunStepSnapshot{
		Input: api.PipelineRunStepInputSnapshot{
			DataSourceSlug: &dataSourceSlug,
			OperationType:  &operationType,
			OperationRef:   &operationRef,
			ParameterKeys:  []string{"tenant_id", "active"},
		},
		Output: &api.PipelineRunStepOutputSnapshot{
			DurationMs: 12,
			Details:    outputDetails,
		},
	}
	node := runlineage.Node{
		Kind: "query",
		Name: "Read users",
		Slug: "read-users",
		Planned: runlineage.PlannedEvidence{
			DataSourceRef: "logical-users",
			OperationType: "sql",
			OperationRef:  "list-users",
			ParameterKeys: []string{"active", "tenant_id"},
		},
		Status:   runlineage.StatusSucceeded,
		Snapshot: snapshot,
	}

	require.Equal(t, "source-postgres", lineageDataSourceLabel(snapshot))
	require.Equal(t, "tenant_id, active", lineageParameterKeysLabel(snapshot))
	require.Equal(t, "Data source ref: logical-users · Operation: sql / list-users · Parameter names: active, tenant_id", lineagePlannedSummary(node))
	require.Contains(t, lineageObservedSummary(node), "Resolved data source: source-postgres")
	require.Contains(t, lineageObservedSummary(node), "Output: 3 rows")
	require.Contains(t, lineagePayloadPreview(node), "Planned ·")
	require.Contains(t, lineagePayloadPreview(node), "Observed ·")
	require.Contains(t, lineageNodeAriaLabel(node), "Source step Read users, Succeeded.")
	require.NotContains(t, lineagePayloadPreview(node), "payload value")

	transform := runlineage.Node{
		Kind: "transform",
		Planned: runlineage.PlannedEvidence{
			Inputs:     []runlineage.PlannedInput{{Name: "users", FromStepSlug: "read-users"}},
			OutputKind: "rows",
		},
		Status: runlineage.StatusFailed,
	}
	require.Equal(t, "Input bindings: users ← read-users · Output kind: rows", lineagePlannedSummary(transform))
	require.Contains(t, lineageObservedSummary(transform), "No persisted snapshot")

	sink := runlineage.Node{
		Kind: "sink",
		Planned: runlineage.PlannedEvidence{
			DataSourceRef: "warehouse",
			OperationType: "sql",
			UsesInline:    true,
			Inputs:        []runlineage.PlannedInput{{Name: "input", FromStepSlug: "normalize-users"}},
		},
	}
	require.Equal(t, "Input: normalize-users · Destination ref: warehouse · Operation: sql / inline", lineagePlannedSummary(sink))
}

func TestRunStatusBadgeClass(t *testing.T) {
	t.Parallel()

	require.Equal(t, "badge badge-success badge-outline", runStatusBadgeClass(api.PipelineRunMetadataStatusSucceeded))
	require.Equal(t, "badge badge-error badge-outline", runStatusBadgeClass(api.PipelineRunMetadataStatusFailed))
	require.Equal(t, "badge badge-info badge-outline", runStatusBadgeClass(api.PipelineRunMetadataStatus("running")))
	require.Equal(t, "badge badge-warning badge-outline", runStatusBadgeClass(api.PipelineRunMetadataStatus("queued")))
}

func TestRunTimestampAndDurationLabels(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 5, 14, 10, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	nullable := ts

	require.Equal(t, "-", formatRunTimestamp(api.Timestamp{}))
	require.Equal(t, "2026-05-14T08:30:00Z", formatRunTimestamp(ts))
	require.Equal(t, "-", formatNullableRunTimestamp(nil))
	require.Equal(t, "2026-05-14T08:30:00Z", formatNullableRunTimestamp(&nullable))
	require.Equal(t, "999 ms", runDurationLabel(999))
	require.Equal(t, "1.50 s", runDurationLabel(1500))
}

func TestRunDashboardMetadataHelpers(t *testing.T) {
	t.Parallel()

	requestID := " req-123 "
	traceID := " trace-123 "
	actorSlug := api.NullableSlug("alice")
	sourceEventSlug := api.NullableSlug("evt-pipeline-run-execute-001")
	retryOfRunSlug := api.NullableSlug("run-001")
	replayRun := api.NullableSlug("run-001")
	replayStep := api.NullableSlug("normalize-users")

	require.Equal(t, "Recent runs", runStatusFilterLabel(""))
	require.Equal(t, "Failed runs", runStatusFilterLabel(string(api.PipelineRunMetadataStatusFailed)))
	require.Equal(t, "Succeeded runs", runStatusFilterLabel(string(api.PipelineRunMetadataStatusSucceeded)))
	require.Equal(t, "Recent runs", runStatusFilterLabel("running"))
	require.False(t, runFiltersActive("", " ", "", ""))
	require.True(t, runFiltersActive("failed", " ", "", ""))
	require.True(t, runFiltersActive("", "acme-users-sync", "", ""))
	require.True(t, runFiltersActive("", "", "backfill", "series-001"))

	source := api.PipelineRunSourceSummary{}
	require.NoError(t, source.FromPipelineRunManualSourceSummary(api.PipelineRunManualSourceSummary{
		Summary:         "Manual run",
		Entrypoint:      api.PipelineRunManualSourceSummaryEntrypointUi,
		ActorType:       api.EventActorTypeUser,
		ActorSlug:       &actorSlug,
		AttemptNumber:   2,
		RootRunSlug:     api.Slug("run-001"),
		RetryOfRunSlug:  &retryOfRunSlug,
		RequestId:       &requestID,
		TraceId:         &traceID,
		ParameterKeys:   []string{"run_slug", "protected_environment_confirmation"},
		SourceEventSlug: &sourceEventSlug,
	}))
	sourceView := apiRunSourceView(&source)
	emptySourceView := apiRunSourceView(nil)
	require.Equal(t, "Manual run via ui", runSourceLabel(sourceView))
	require.Equal(t, "user alice", runSourceActorLabel(sourceView))
	require.Equal(t, "Request req-123 / Trace trace-123", runSourceRequestLabel(sourceView))
	require.Equal(t, "run_slug, protected_environment_confirmation", runSourceParameterKeysLabel(sourceView))
	require.Equal(t, "evt-pipeline-run-execute-001", runSourceEventLabel(sourceView))
	require.Equal(t, "attempt 2", runAttemptLabel(sourceView))
	require.Equal(t, "Root run-001 / retry of run-001", runRetryGroupLabel(sourceView))
	require.True(t, runHasRetryContext(sourceView))
	require.Equal(t, "manual", runSourceTypeLabel(sourceView))
	require.Equal(t, "Source unavailable", runSourceLabel(emptySourceView))
	require.Equal(t, "Actor unavailable", runSourceActorLabel(emptySourceView))
	require.Equal(t, "No request context", runSourceRequestLabel(emptySourceView))
	require.Equal(t, "No supplied parameter names", runSourceParameterKeysLabel(emptySourceView))
	require.Equal(t, "No source event", runSourceEventLabel(emptySourceView))
	require.Equal(t, "Attempt not recorded", runAttemptLabel(emptySourceView))
	require.Equal(t, "No retry group", runRetryGroupLabel(emptySourceView))
	require.False(t, runHasRetryContext(emptySourceView))
	require.Equal(t, "source unavailable", runSourceTypeLabel(emptySourceView))
	require.False(t, runHasReplayContext(nil, emptySourceView))
	require.Equal(t, "No replay context", runReplayContextLabel(nil, nil))
	require.True(t, runHasReplayContext(&replayRun, emptySourceView))
	require.Equal(t, "Replay of run-001", runReplayContextLabel(&replayRun, nil))
	require.Equal(t, "Replay of run-001 from normalize-users", runReplayContextLabel(&replayRun, &replayStep))
	require.False(t, runCheckpointStatusVisible(api.PipelineRunCheckpointStatusNotApplicable))
	require.True(t, runCheckpointStatusVisible(api.PipelineRunCheckpointStatusBlocked))
}

func TestScheduledRunSourceAndRetryHelpers(t *testing.T) {
	t.Parallel()

	sourceEventSlug := api.NullableSlug("evt-schedule-fire-001")
	retryOfRunSlug := api.NullableSlug("run-001")
	source := api.PipelineRunSourceSummary{}
	require.NoError(t, source.FromPipelineRunScheduleSourceSummary(api.PipelineRunScheduleSourceSummary{
		Summary:         "Scheduled retry",
		Entrypoint:      api.PipelineRunScheduleSourceSummaryEntrypointScheduler,
		ActorType:       api.EventActorTypeSystem,
		AttemptNumber:   2,
		RootRunSlug:     api.Slug("run-001"),
		RetryOfRunSlug:  &retryOfRunSlug,
		ParameterKeys:   []string{"trigger_slug"},
		SourceEventSlug: &sourceEventSlug,
		Schedule: api.PipelineRunSourceScheduleSummary{
			TriggerSlug:     api.Slug("nightly-users"),
			TriggerRevision: 3,
			Cron:            "0 2 * * *",
			Timezone:        "UTC",
			ScheduledFireAt: time.Date(2026, 3, 16, 2, 0, 0, 0, time.UTC),
			ActualFireAt:    time.Date(2026, 3, 16, 2, 0, 5, 0, time.UTC),
			MisfireResult:   api.PipelineRunScheduleMisfireResultOnTime,
		},
	}))

	sourceView := apiRunSourceView(&source)
	require.Equal(t, "schedule", runSourceTypeLabel(sourceView))
	require.Equal(t, "nightly-users rev 3", runScheduleTriggerLabel(sourceView))
	require.Equal(t, "0 2 * * * / UTC", runScheduleCadenceLabel(sourceView))
	require.Equal(t, "scheduled 2026-03-16T02:00:00Z / actual 2026-03-16T02:00:05Z", runScheduleFireLabel(sourceView))
	require.Equal(t, "on_time", runScheduleMisfireLabel(sourceView))
	require.Equal(t, "badge badge-error badge-outline", retryAttemptStatusBadgeClass(api.PipelineTriggerRetryAttemptStatusFailed))

	decisionReason := "retry policy queued one retry"
	require.Equal(t, decisionReason, retryAttemptDecisionLabel(api.PipelineRunTriggerRetryAttempt{DecisionReason: &decisionReason}))
}

func TestRunFailureTriageMetadataHelpers(t *testing.T) {
	t.Parallel()

	operationType := "sql"
	operationRef := api.Slug("upsert-users")
	usesInline := true
	failureDataSource := "warehouse-postgres"
	sourceDataSource := api.Slug("source-postgres")

	require.Equal(t, "-", runOperationLabel(nil))
	require.Equal(t, "sql / upsert-users", runOperationLabel(&api.PipelineRunStepSnapshot{
		Input: api.PipelineRunStepInputSnapshot{
			OperationType: &operationType,
			OperationRef:  &operationRef,
		},
	}))
	require.Equal(t, "sql / inline", runOperationLabel(&api.PipelineRunStepSnapshot{
		Input: api.PipelineRunStepInputSnapshot{
			OperationType: &operationType,
			UsesInline:    &usesInline,
		},
	}))
	require.Equal(t, "warehouse-postgres", runFailureDataSourceLabel(
		&api.PipelineRunFailureContext{DataSourceSlug: &failureDataSource},
		nil,
	))
	require.Equal(t, "source-postgres", runFailureDataSourceLabel(nil, &api.PipelineRunStepSnapshot{
		Input: api.PipelineRunStepInputSnapshot{DataSourceSlug: &sourceDataSource},
	}))
	require.Equal(t, "-", runFailureDataSourceLabel(nil, nil))
}

func TestRunFinalSinkOutputView(t *testing.T) {
	t.Parallel()

	sinkKind := api.PipelineStepKind("sink")
	queryKind := api.PipelineStepKind("query")
	s3OperationType := "s3"
	ftpOperationType := "ftp"
	targetPath := "exports/users/run-001.json"
	filename := "users.json"
	objectStoreSlug := api.Slug("object-store")
	details := api.PipelineRunStepOutputDetails{}
	require.NoError(t, details.FromPipelineRunFileWriteOutputDetails(api.PipelineRunFileWriteOutputDetails{
		TargetPath: targetPath,
		File: &api.FilePayloadMetadata{
			Filename:        &filename,
			MimeType:        "application/json",
			SizeBytes:       128,
			DigestAlgorithm: "sha256",
			Digest:          "abc123",
		},
	}))
	run := api.PipelineRunRecord{
		Metadata: api.PipelineRunMetadata{
			StepSlug: api.Slug("write-users"),
		},
		Timeline: api.PipelineRunTimeline{
			Snapshots: []api.PipelineRunStepSnapshot{{
				Step: api.Slug("write-users"),
				Kind: &sinkKind,
				Input: api.PipelineRunStepInputSnapshot{
					DataSourceSlug: &objectStoreSlug,
					OperationType:  &s3OperationType,
				},
				Output: &api.PipelineRunStepOutputSnapshot{
					ResultType: "S3",
					VendorType: "s3",
					Details:    details,
				},
			}},
		},
	}

	view := runSinkOutputView(runStepSnapshot(run, run.Metadata.StepSlug))
	require.True(t, view.Visible)
	require.True(t, view.Available)
	require.Equal(t, "write-users", view.Step)
	require.Equal(t, "object-store", view.DataSource)
	require.Equal(t, "s3", view.Operation)
	require.Equal(t, "Object key", view.LocationLabel)
	require.Equal(t, targetPath, view.TargetPath)
	require.Equal(t, "users.json / application/json / 128 bytes", view.FileMetadata)

	missingTargetRun := run
	missingTargetRun.Timeline.Snapshots[0].Input.OperationType = &ftpOperationType
	missingTargetRun.Timeline.Snapshots[0].Output = nil
	missingView := runSinkOutputView(runStepSnapshot(missingTargetRun, missingTargetRun.Metadata.StepSlug))
	require.True(t, missingView.Visible)
	require.False(t, missingView.Available)
	require.Equal(t, "Path", missingView.LocationLabel)
	require.Empty(t, missingView.TargetPath)

	sourceReadRun := run
	sourceReadRun.Timeline.Snapshots[0].Kind = &queryKind
	require.False(t, runSinkOutputView(runStepSnapshot(sourceReadRun, sourceReadRun.Metadata.StepSlug)).Visible)
}

func TestRunEventDisplayHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		eventType string
		status    string
		wantBadge string
		wantLabel string
		wantDot   string
	}{
		{
			name:      "run started",
			eventType: eventTypeRunStarted,
			wantBadge: "badge badge-info badge-sm badge-outline",
			wantLabel: "Run started",
			wantDot:   "h-3 w-3 rounded-full bg-info",
		},
		{
			name:      "run completed",
			eventType: eventTypeRunCompleted,
			wantBadge: "badge badge-success badge-sm badge-outline",
			wantLabel: "Run completed",
			wantDot:   "h-3 w-3 rounded-full bg-success",
		},
		{
			name:      "run failed",
			eventType: eventTypeRunCompleted,
			status:    statusFailed,
			wantBadge: "badge badge-error badge-sm badge-outline",
			wantLabel: "Run completed",
			wantDot:   "h-3 w-3 rounded-full bg-error",
		},
		{
			name:      "step started",
			eventType: eventTypeStepStarted,
			wantBadge: "badge badge-ghost badge-sm badge-outline",
			wantLabel: "Step started",
			wantDot:   "h-3 w-3 rounded-full bg-base-300",
		},
		{
			name:      "step failed",
			eventType: eventTypeStepFailed,
			wantBadge: "badge badge-error badge-sm badge-outline",
			wantLabel: "Step failed",
			wantDot:   "h-3 w-3 rounded-full bg-error",
		},
		{
			name:      "unknown",
			eventType: "custom",
			wantBadge: "badge badge-ghost badge-sm badge-outline",
			wantLabel: "custom",
			wantDot:   "h-3 w-3 rounded-full bg-base-300",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantBadge, runEventBadgeClass(tt.eventType, tt.status))
			require.Equal(t, tt.wantLabel, runEventTypeLabel(tt.eventType))
			require.Equal(t, tt.wantDot, runTimelineDotClass(tt.eventType, tt.status))
		})
	}
}

func TestRunStepSnapshot(t *testing.T) {
	t.Parallel()

	run := api.PipelineRunRecord{
		Timeline: api.PipelineRunTimeline{
			Snapshots: []api.PipelineRunStepSnapshot{
				{Step: "fetch-users"},
				{Step: "persist-users"},
			},
		},
	}

	require.Equal(t, api.Slug("persist-users"), runStepSnapshot(run, "persist-users").Step)
	require.Nil(t, runStepSnapshot(run, "missing"))
}

func TestFailureDisplayHelpers(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Data source error", failureKindLabel(failureKindDataSource))
	require.Equal(t, "Platform error", failureKindLabel(failureKindPlatform))
	require.Equal(t, "custom", failureKindLabel("custom"))
	require.Equal(t, "badge badge-sm badge-warning", failureKindBadgeClass(failureKindDataSource))
	require.Equal(t, "badge badge-sm badge-error", failureKindBadgeClass(failureKindPlatform))
	require.Equal(t, "badge badge-sm badge-ghost", failureKindBadgeClass("custom"))
	require.Equal(t, "Source step", failureSideLabel(failureSideSource))
	require.Equal(t, "Sink step", failureSideLabel(failureSideSink))
	require.Equal(t, "Transform step", failureSideLabel(failureSideTransform))
	require.Equal(t, "Connector", failureSideLabel(failureSideConnector))
	require.Equal(t, "custom", failureSideLabel("custom"))
}

func TestRunFinalSinkOutputViewShowsDatabaseRowCount(t *testing.T) {
	t.Parallel()
	kind := api.PipelineStepKind("sink")
	details := api.PipelineRunStepOutputDetails{}
	require.NoError(t, details.FromPipelineRunRowsOutputDetails(api.PipelineRunRowsOutputDetails{RowCount: 3}))
	run := api.PipelineRunRecord{
		Metadata: api.PipelineRunMetadata{StepSlug: "write-comparisons"},
		Timeline: api.PipelineRunTimeline{Snapshots: []api.PipelineRunStepSnapshot{{
			Step: "write-comparisons", Kind: &kind,
			Output: &api.PipelineRunStepOutputSnapshot{ResultType: "SQL", VendorType: "bigquery", Details: details},
		}}},
	}
	view := runSinkOutputView(runStepSnapshot(run, run.Metadata.StepSlug))
	require.True(t, view.Visible)
	require.True(t, view.Available)
	require.NotNil(t, view.RowCount)
	require.Equal(t, 3, *view.RowCount)
}

func TestTimelineUsesProfileTimezoneAndOriginalInstants(t *testing.T) {
	ctx := WithRunTimezone(t.Context(), "Europe/Warsaw")
	first, err := time.Parse(time.RFC3339Nano, "2026-10-25T00:59:59.125Z")
	require.NoError(t, err)
	second := first.Add(time.Second)
	require.Equal(t, "25 Oct 2026, 02:59:59.125 CEST (UTC+02:00)", runTimelineTimestamp(ctx, first))
	require.Equal(t, "25 Oct 2026, 02:00:00.125 CET (UTC+01:00)", runTimelineTimestamp(ctx, second))
	events := []api.PipelineRunTimelineEvent{{At: second}, {At: first}}
	ordered := runlineage.ChronologicalEvents(events)
	require.Equal(t, first, ordered[0].At)
	require.Equal(t, second, events[0].At)
	require.Equal(t, "Time unavailable", runTimelineTimestamp(ctx, time.Time{}))
	require.Contains(t, runTimelineTimestamp(WithRunTimezone(ctx, "invalid"), first), "UTC+00:00")
}
