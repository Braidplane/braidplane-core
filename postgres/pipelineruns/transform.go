package pipelineruns

import (
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func toPipelineRun(row dbgen.PipelineRun, environmentSlug string, sourceWebhook json.RawMessage) *PipelineRun {
	diagnostics := pipelinerun.IncrementalRunDiagnostics{}
	if len(row.IncrementalDiagnostics) > 0 {
		_ = json.Unmarshal(row.IncrementalDiagnostics, &diagnostics)
	}

	return &PipelineRun{
		WorkerJobID:                postgres.FromNullableInt64(row.WorkerJobID),
		DeploymentID:               postgres.FromNullableInt64(row.DeploymentID),
		DeploymentRevision:         postgres.FromNullableInt64(row.DeploymentRevision),
		PipelineVersionID:          row.PipelineVersionID.Int64,
		DeploymentResolutionDigest: postgres.FromNullableText(row.DeploymentResolutionDigest),
		PipelineVersionSpecHash:    row.PipelineVersionSpecHash.String,
		EnvironmentSlug:            environmentSlug,
		APIVersion:                 row.ApiVersion,
		PipelineSlug:               row.PipelineSlug,
		RunSlug:                    row.RunSlug,
		Source: pipelinerun.RunSourceSummary{
			Type:            pipelineRunSourceType(row.SourceType),
			Summary:         row.SourceSummary,
			EnvironmentSlug: slug.FromString(row.SourceEnvironmentSlug),
			Entrypoint:      pipelineRunSourceEntrypoint(row.SourceEntrypoint),
			ActorType:       eventActorType(row.SourceActorType),
			ActorSlug:       nullableTextSlug(row.SourceActorSlug),
			RequestID:       postgres.FromNullableText(row.SourceRequestID),
			TraceID:         postgres.FromNullableText(row.SourceTraceID),
			ParameterKeys:   row.SourceParameterKeys,
			SourceEventSlug: nullableTextSlug(row.SourceEventSlug),
			AttemptNumber:   int(row.SourceAttemptNumber),
			RootRunSlug:     pipelineRunRootSlug(row),
			RetryOfRunSlug:  nullableTextSlug(row.SourceRetryOfRunSlug),
			Schedule:        pipelineRunScheduleSummary(row),
			Webhook:         pipelineRunWebhookSummary(row.SourceWebhookDeliveryID, sourceWebhook),
		},
		RequestID:       postgres.FromNullableText(row.RequestID),
		Status:          row.Status,
		DurationMs:      row.DurationMs,
		StepCount:       row.StepCount,
		StepSlug:        row.StepSlug,
		ErrorSummary:    postgres.FromNullableText(row.ErrorSummary),
		FailureKind:     postgres.FromNullableText(row.FailureKind),
		FailureSide:     postgres.FromNullableText(row.FailureSide),
		StartedAt:       row.StartedAt.Time,
		EndedAt:         postgres.FromNullableTimestamptz(row.EndedAt),
		ReplayOfRunSlug: postgres.FromNullableText(row.ReplayOfRunSlug),
		ReplayFromStep:  postgres.FromNullableText(row.ReplayFromStep),
		Timeline:        row.Timeline,
		Incremental: pipelinerun.IncrementalRunSummary{
			Mode:                row.RunMode,
			CheckpointStatus:    row.CheckpointStatus,
			SourceStepSlug:      nullableTextValue(row.IncrementalSourceStepSlug),
			BlockedReason:       nullableTextValue(row.IncrementalBlockedReason),
			ReplaySafetyStatus:  nullableTextValue(row.ReplaySafetyStatus),
			ReplayBlockedReason: nullableTextValue(row.ReplayBlockedReason),
			BackfillSeriesSlug:  nullableTextValue(row.BackfillSeriesSlug),
			BackfillChunkIndex:  int(row.BackfillChunkIndex),
			BackfillChunkCount:  int(row.BackfillChunkCount),
		},
		IncrementalDiagnostics: diagnostics,
		CreatedAt:              row.CreatedAt.Time,
	}
}

func pipelineRunWebhookSummary(deliveryID pgtype.Int8, sourceWebhook json.RawMessage) *pipelinerun.RunSourceWebhookSummary {
	if !deliveryID.Valid || len(sourceWebhook) == 0 {
		return nil
	}
	var summary pipelinerun.RunSourceWebhookSummary
	if err := json.Unmarshal(sourceWebhook, &summary); err != nil {
		return nil
	}
	summary.DeliveryRecordID = deliveryID.Int64
	return &summary
}

// pipelineRunScheduleSummary parses optional schedule source columns for scheduled run history.
func pipelineRunScheduleSummary(row dbgen.PipelineRun) *pipelinerun.RunSourceScheduleSummary {
	if !row.SourceScheduleTriggerSlug.Valid {
		return nil
	}
	if !row.SourceScheduleTriggerRevision.Valid ||
		!row.SourceScheduleCron.Valid ||
		!row.SourceScheduleTimezone.Valid ||
		!row.SourceScheduleScheduledFireAt.Valid ||
		!row.SourceScheduleActualFireAt.Valid ||
		!row.SourceScheduleMisfireResult.Valid {
		return nil
	}
	misfireResult, err := model.PipelineTriggerScheduleMisfireResultString(
		row.SourceScheduleMisfireResult.String,
	)
	if err != nil {
		misfireResult = model.PipelineTriggerScheduleMisfireResultUnknown
	}
	return &pipelinerun.RunSourceScheduleSummary{
		TriggerSlug:     slug.FromString(row.SourceScheduleTriggerSlug.String),
		TriggerRevision: row.SourceScheduleTriggerRevision.Int64,
		Cron:            row.SourceScheduleCron.String,
		Timezone:        row.SourceScheduleTimezone.String,
		ScheduledFireAt: row.SourceScheduleScheduledFireAt.Time,
		ActualFireAt:    row.SourceScheduleActualFireAt.Time,
		MisfireResult:   misfireResult,
	}
}

// pipelineRunSourceType parses the stored run source category into the typed model value.
func pipelineRunSourceType(value string) model.PipelineRunSourceType {
	parsed, err := model.PipelineRunSourceTypeString(value)
	if err != nil {
		return model.PipelineRunSourceTypeUnknown
	}
	return parsed
}

// pipelineRunSourceEntrypoint parses the stored run entrypoint into the typed model value.
func pipelineRunSourceEntrypoint(value string) model.PipelineRunSourceEntrypoint {
	parsed, err := model.PipelineRunSourceEntrypointString(value)
	if err != nil {
		return model.PipelineRunSourceEntrypointUnknown
	}
	return parsed
}

// eventActorType parses the stored actor type into the typed model value.
func eventActorType(value string) model.EventActorType {
	parsed, err := model.EventActorTypeString(value)
	if err != nil {
		return model.EventActorTypeUnknown
	}
	return parsed
}

func nullableTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// pipelineRunRootSlug returns the retry root slug, defaulting first-attempt rows to their run slug.
func pipelineRunRootSlug(row dbgen.PipelineRun) slug.Slug {
	if !row.SourceRootRunSlug.Valid || row.SourceRootRunSlug.String == "" {
		return slug.FromString(row.RunSlug)
	}
	return slug.FromString(row.SourceRootRunSlug.String)
}

func nullableTextSlug(value pgtype.Text) *slug.Slug {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed := slug.FromString(value.String)
	return &parsed
}
