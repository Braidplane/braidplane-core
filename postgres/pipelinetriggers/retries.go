package pipelinetriggers

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// RecordTriggerRetryAttempt persists one retry decision before any River handoff.
func (r *repository) RecordTriggerRetryAttempt(
	ctx context.Context,
	tx postgres.DBTX,
	record TriggerRetryAttemptRecord,
) (TriggerRetryAttempt, error) {
	retryMaxAttempts, retryInitialDelay, retryMaxDelay, retryMaxElapsed, err := retryPolicySQLValues(record.RetryPolicy)
	if err != nil {
		return TriggerRetryAttempt{}, err
	}
	attemptNumber, err := retryAttemptNumberSQLValue(record.AttemptNumber)
	if err != nil {
		return TriggerRetryAttempt{}, err
	}
	attemptSlug, err := record.AttemptSlug()
	if err != nil {
		return TriggerRetryAttempt{}, err
	}
	sourceEventSlug := record.SourceEventSlug
	if sourceEventSlug == nil {
		sourceEventSlug = record.Source.SourceEventSlug
	}

	row, err := dbgen.New(tx).RecordTriggerRetryAttempt(ctx, dbgen.RecordTriggerRetryAttemptParams{
		OrgSlug:                       record.OrgSlug,
		ProjectSlug:                   record.ProjectSlug,
		EnvironmentSlug:               record.EnvironmentSlug,
		TriggerSlug:                   record.TriggerSlug,
		TriggerRevision:               record.TriggerRevision,
		AttemptSlug:                   attemptSlug.String(),
		Status:                        record.Status.String(),
		DecisionReason:                textOrNull(record.DecisionReason),
		PipelineSlug:                  record.PipelineSlug.String(),
		VersionPolicy:                 record.VersionPolicy.Mode.String(),
		PinnedVersionHash:             optionalString(record.VersionPolicy.PinnedVersionHash),
		PipelineVersionSpecHash:       record.PipelineVersionSpecHash,
		RuntimeInputKeys:              record.RuntimeInputKeys,
		RetryEnabled:                  record.RetryPolicy.Enabled,
		RetryMaxAttempts:              retryMaxAttempts,
		RetryBackoffStrategy:          record.RetryPolicy.BackoffStrategy.String(),
		RetryInitialDelaySeconds:      retryInitialDelay,
		RetryMaxDelaySeconds:          retryMaxDelay,
		RetryJitter:                   record.RetryPolicy.Jitter,
		RetryableFailureKinds:         retryFailureKindStrings(record.RetryPolicy.RetryableFailureKinds),
		RetryMaxElapsedSeconds:        retryMaxElapsed,
		SourceType:                    record.Source.Type.String(),
		SourceSummary:                 record.Source.Summary,
		SourceEntrypoint:              record.Source.Entrypoint.String(),
		SourceActorType:               record.Source.ActorType.String(),
		SourceActorSlug:               optionalSlugText(record.Source.ActorSlug),
		SourceRequestID:               postgres.ToNullableText(record.Source.RequestID),
		SourceTraceID:                 postgres.ToNullableText(record.Source.TraceID),
		SourceEventSlug:               postgres.OptionalSlugToText(sourceEventSlug),
		SourceParameterKeys:           record.Source.ParameterKeys,
		SourceScheduleTriggerSlug:     retrySourceScheduleSlug(record.Source),
		SourceScheduleTriggerRevision: retrySourceScheduleRevision(record.Source),
		SourceScheduleCron:            retrySourceScheduleText(record.Source, func(s *pipelinerun.RunSourceScheduleSummary) string { return s.Cron }),
		SourceScheduleTimezone:        retrySourceScheduleText(record.Source, func(s *pipelinerun.RunSourceScheduleSummary) string { return s.Timezone }),
		SourceScheduleScheduledFireAt: retrySourceScheduleTime(record.Source, func(s *pipelinerun.RunSourceScheduleSummary) time.Time { return s.ScheduledFireAt }),
		SourceScheduleActualFireAt:    retrySourceScheduleTime(record.Source, func(s *pipelinerun.RunSourceScheduleSummary) time.Time { return s.ActualFireAt }),
		SourceScheduleMisfireResult: retrySourceScheduleText(record.Source, func(s *pipelinerun.RunSourceScheduleSummary) string {
			return s.MisfireResult.String()
		}),
		RootRunSlug:    record.RootRunSlug.String(),
		RetryOfRunSlug: record.RetryOfRunSlug.String(),
		RunSlug:        record.RunSlug.String(),
		AttemptNumber:  attemptNumber,
		FailureKind:    retryFailureKindText(record.FailureKind),
		ScheduledAt:    postgres.ToTimestamptz(record.ScheduledAt),
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return TriggerRetryAttempt{}, ErrPipelineTriggerNotFound.With(err)
		}
		return TriggerRetryAttempt{}, xerror.Unexpected().With(err)
	}

	return retryAttemptFromRecord(record, row.AttemptSlug, row.RunSlug, row.ScheduledAt.Time), nil
}

// ListTriggerRetryAttemptsForRun returns safe retry diagnostics linked to a run's retry root.
func (r *repository) ListTriggerRetryAttemptsForRun(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, runSlug slug.Slug,
) ([]TriggerRetryAttemptDiagnostic, error) {
	rows, err := dbgen.New(tx).ListTriggerRetryAttemptsForRun(
		ctx,
		dbgen.ListTriggerRetryAttemptsForRunParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
			RunSlug:         runSlug.String(),
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	attempts := make([]TriggerRetryAttemptDiagnostic, 0, len(rows))
	for _, row := range rows {
		attempt, err := retryAttemptDiagnosticFromRow(row)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, nil
}

// LoadTriggerRetryAttemptForExecution returns one queued retry attempt for a River worker.
func (r *repository) LoadTriggerRetryAttemptForExecution(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, attemptSlug slug.Slug,
) (TriggerRetryAttempt, error) {
	row, err := dbgen.New(tx).LoadTriggerRetryAttemptForExecution(
		ctx,
		dbgen.LoadTriggerRetryAttemptForExecutionParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
			AttemptSlug:     attemptSlug.String(),
		},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return TriggerRetryAttempt{}, ErrPipelineTriggerNotFound.With(err)
		}
		return TriggerRetryAttempt{}, xerror.Unexpected().With(err)
	}
	return retryAttemptFromExecutionRow(row)
}

// MarkTriggerRetryAttemptRunning claims a queued retry attempt for one worker execution.
func (r *repository) MarkTriggerRetryAttemptRunning(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, attemptSlug slug.Slug,
) error {
	rowsAffected, err := dbgen.New(tx).MarkTriggerRetryAttemptRunning(
		ctx,
		dbgen.MarkTriggerRetryAttemptRunningParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
			AttemptSlug:     attemptSlug.String(),
		},
	)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrPipelineTriggerNotFound
	}
	return nil
}

// CompleteTriggerRetryAttempt records the terminal state for one retry attempt.
func (r *repository) CompleteTriggerRetryAttempt(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, attemptSlug slug.Slug,
	status model.PipelineTriggerRetryAttemptStatus,
	sourceEventSlug *slug.Slug,
	errorSummary *string,
) error {
	if !retryAttemptCompletionStatus(status) {
		return xerror.InvalidInput(xerror.PipelineTrigger).
			Detail("pipeline trigger retry attempt completion status must be succeeded or failed")
	}
	rowsAffected, err := dbgen.New(tx).CompleteTriggerRetryAttempt(
		ctx,
		dbgen.CompleteTriggerRetryAttemptParams{
			Status:          status.String(),
			SourceEventSlug: postgres.OptionalSlugToText(sourceEventSlug),
			ErrorSummary:    postgres.ToNullableText(errorSummary),
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
			AttemptSlug:     attemptSlug.String(),
		},
	)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrPipelineTriggerNotFound
	}
	return nil
}

// retryAttemptCompletionStatus reports whether status can close a running retry attempt.
func retryAttemptCompletionStatus(status model.PipelineTriggerRetryAttemptStatus) bool {
	return status == model.PipelineTriggerRetryAttemptStatusSucceeded ||
		status == model.PipelineTriggerRetryAttemptStatusFailed
}

// AttemptSlug returns a bounded deterministic slug for the persisted retry decision.
func (r TriggerRetryAttemptRecord) AttemptSlug() (slug.Slug, error) {
	if r.RootRunSlug == "" {
		return "", xerror.InvalidInput(xerror.PipelineTrigger).
			Detail("retry root_run_slug is required")
	}
	return slug.FromName("retry-" + r.RootRunSlug.String() + "-" + strconv.Itoa(r.AttemptNumber))
}

// retryAttemptFromRecord hydrates the worker projection from the values just persisted.
func retryAttemptFromRecord(record TriggerRetryAttemptRecord, attemptSlug, runSlug string, scheduledAt time.Time) TriggerRetryAttempt {
	return TriggerRetryAttempt{
		AttemptSlug:             slug.FromString(attemptSlug),
		Status:                  record.Status,
		DecisionReason:          record.DecisionReason,
		OrgSlug:                 record.OrgSlug,
		ProjectSlug:             record.ProjectSlug,
		EnvironmentSlug:         record.EnvironmentSlug,
		TriggerSlug:             record.TriggerSlug,
		TriggerRevision:         record.TriggerRevision,
		PipelineSlug:            record.PipelineSlug,
		VersionPolicy:           record.VersionPolicy,
		PipelineVersionSpecHash: record.PipelineVersionSpecHash,
		RuntimeInputKeys:        append([]string(nil), record.RuntimeInputKeys...),
		RetryPolicy:             record.RetryPolicy,
		Source:                  record.Source,
		RootRunSlug:             record.RootRunSlug,
		RetryOfRunSlug:          record.RetryOfRunSlug,
		RunSlug:                 slug.FromString(runSlug),
		AttemptNumber:           record.AttemptNumber,
		FailureKind:             record.FailureKind,
		ScheduledAt:             scheduledAt,
	}
}

// retryAttemptFromExecutionRow maps one SQLC retry row into the worker projection.
func retryAttemptFromExecutionRow(row dbgen.LoadTriggerRetryAttemptForExecutionRow) (TriggerRetryAttempt, error) {
	status, err := model.PipelineTriggerRetryAttemptStatusString(row.Status)
	if err != nil {
		return TriggerRetryAttempt{}, xerror.Unexpected().With(err)
	}
	versionPolicyMode, err := model.PipelineTriggerVersionPolicyModeString(row.VersionPolicy)
	if err != nil {
		return TriggerRetryAttempt{}, xerror.Unexpected().With(err)
	}
	backoffStrategy, err := model.PipelineTriggerBackoffStrategyString(row.RetryBackoffStrategy)
	if err != nil {
		return TriggerRetryAttempt{}, xerror.Unexpected().With(err)
	}
	retryableKinds, err := retryFailureKindsFromStrings(row.RetryableFailureKinds)
	if err != nil {
		return TriggerRetryAttempt{}, err
	}
	failureKind := model.PipelineTriggerRetryFailureKindUnknown
	if row.FailureKind.Valid {
		parsed, parseErr := model.PipelineTriggerRetryFailureKindString(row.FailureKind.String)
		if parseErr != nil {
			return TriggerRetryAttempt{}, xerror.Unexpected().With(parseErr)
		}
		failureKind = parsed
	}

	source, err := retrySourceFromExecutionRow(row)
	if err != nil {
		return TriggerRetryAttempt{}, err
	}

	return TriggerRetryAttempt{
		AttemptSlug:     slug.FromString(row.AttemptSlug),
		Status:          status,
		DecisionReason:  nullableTextValue(row.DecisionReason),
		OrgSlug:         row.OrgSlug,
		ProjectSlug:     row.ProjectSlug,
		EnvironmentSlug: row.EnvironmentSlug,
		TriggerSlug:     row.TriggerSlug,
		TriggerRevision: row.TriggerRevision,
		PipelineSlug:    slug.FromString(row.PipelineSlug),
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode:              versionPolicyMode,
			PinnedVersionHash: optionalTextString(row.PinnedVersionHash),
		},
		PipelineVersionSpecHash: row.PipelineVersionSpecHash,
		RuntimeInputKeys:        row.RuntimeInputKeys,
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:               row.RetryEnabled,
			MaxAttempts:           int(row.RetryMaxAttempts),
			BackoffStrategy:       backoffStrategy,
			InitialDelaySeconds:   int(row.RetryInitialDelaySeconds),
			MaxDelaySeconds:       int(row.RetryMaxDelaySeconds),
			Jitter:                row.RetryJitter,
			RetryableFailureKinds: retryableKinds,
			MaxElapsedSeconds:     optionalIntValue(row.RetryMaxElapsedSeconds),
		},
		Source:         source,
		RootRunSlug:    slug.FromString(row.RootRunSlug),
		RetryOfRunSlug: slug.FromString(row.RetryOfRunSlug),
		RunSlug:        slug.FromString(row.RunSlug),
		AttemptNumber:  int(row.AttemptNumber),
		FailureKind:    failureKind,
		ScheduledAt:    row.ScheduledAt.Time,
		StartedAt:      postgres.FromNullableTimestamptz(row.StartedAt),
		CompletedAt:    postgres.FromNullableTimestamptz(row.CompletedAt),
		ErrorSummary:   postgres.FromNullableText(row.ErrorSummary),
		CreatedAt:      row.CreatedAt.Time,
	}, nil
}

// retryAttemptDiagnosticFromRow maps SQLC rows to the public retry diagnostic projection.
func retryAttemptDiagnosticFromRow(row dbgen.ListTriggerRetryAttemptsForRunRow) (TriggerRetryAttemptDiagnostic, error) {
	status, err := model.PipelineTriggerRetryAttemptStatusString(row.Status)
	if err != nil {
		return TriggerRetryAttemptDiagnostic{}, xerror.Unexpected().With(err)
	}
	failureKind, hasFailureKind, err := optionalRetryFailureKind(row.FailureKind)
	if err != nil {
		return TriggerRetryAttemptDiagnostic{}, err
	}
	var failureKindPtr *model.PipelineTriggerRetryFailureKind
	if hasFailureKind {
		failureKindPtr = &failureKind
	}
	return TriggerRetryAttemptDiagnostic{
		AttemptSlug:     slug.FromString(row.AttemptSlug),
		Status:          status,
		DecisionReason:  postgres.FromNullableText(row.DecisionReason),
		TriggerSlug:     slug.Slug(row.TriggerSlug),
		TriggerRevision: row.TriggerRevision,
		PipelineSlug:    slug.FromString(row.PipelineSlug),
		RootRunSlug:     slug.FromString(row.RootRunSlug),
		RetryOfRunSlug:  slug.FromString(row.RetryOfRunSlug),
		RunSlug:         slug.FromString(row.RunSlug),
		AttemptNumber:   int(row.AttemptNumber),
		FailureKind:     failureKindPtr,
		SourceEventSlug: optionalTextSlug(row.SourceEventSlug),
		ScheduledAt:     row.ScheduledAt.Time,
		EnqueuedAt:      postgres.FromNullableTimestamptz(row.EnqueuedAt),
		StartedAt:       postgres.FromNullableTimestamptz(row.StartedAt),
		CompletedAt:     postgres.FromNullableTimestamptz(row.CompletedAt),
		ErrorSummary:    postgres.FromNullableText(row.ErrorSummary),
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}, nil
}

// retrySourceFromExecutionRow reconstructs the immutable source summary captured for the retry.
func retrySourceFromExecutionRow(row dbgen.LoadTriggerRetryAttemptForExecutionRow) (pipelinerun.RunSourceSummary, error) {
	sourceType, err := model.PipelineRunSourceTypeString(row.SourceType)
	if err != nil {
		return pipelinerun.RunSourceSummary{}, xerror.Unexpected().With(err)
	}
	entrypoint, err := model.PipelineRunSourceEntrypointString(row.SourceEntrypoint)
	if err != nil {
		return pipelinerun.RunSourceSummary{}, xerror.Unexpected().With(err)
	}
	actorType, err := model.EventActorTypeString(row.SourceActorType)
	if err != nil {
		return pipelinerun.RunSourceSummary{}, xerror.Unexpected().With(err)
	}

	source := pipelinerun.RunSourceSummary{
		Type:            sourceType,
		Summary:         row.SourceSummary,
		EnvironmentSlug: row.EnvironmentSlug,
		Entrypoint:      entrypoint,
		ActorType:       actorType,
		ActorSlug:       optionalTextSlug(row.SourceActorSlug),
		RequestID:       postgres.FromNullableText(row.SourceRequestID),
		TraceID:         postgres.FromNullableText(row.SourceTraceID),
		ParameterKeys:   row.SourceParameterKeys,
		SourceEventSlug: optionalTextSlug(row.SourceEventSlug),
		AttemptNumber:   int(row.AttemptNumber),
		RootRunSlug:     slug.FromString(row.RootRunSlug),
		RetryOfRunSlug:  optionalSlugValue(row.RetryOfRunSlug),
		Schedule:        retryScheduleSourceFromExecutionRow(row),
	}
	return source, nil
}

// retryScheduleSourceFromExecutionRow reconstructs optional schedule context for trigger retries.
func retryScheduleSourceFromExecutionRow(row dbgen.LoadTriggerRetryAttemptForExecutionRow) *pipelinerun.RunSourceScheduleSummary {
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

// retryAttemptNumberSQLValue validates the attempt number before passing it into SQLC.
func retryAttemptNumberSQLValue(value int) (int32, error) {
	if value < 1 || value > math.MaxInt32 {
		return 0, xerror.InvalidInput(xerror.PipelineTrigger).
			Detail("retry attempt_number must be between 1 and int32 max")
	}
	return int32(value), nil
}

// retryFailureKindText stores unknown failure kinds as NULL for skipped decisions.
func retryFailureKindText(value model.PipelineTriggerRetryFailureKind) pgtype.Text {
	if value == model.PipelineTriggerRetryFailureKindUnknown {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value.String(), Valid: true}
}

// optionalRetryFailureKind converts nullable SQL failure categories to a typed value and presence flag.
func optionalRetryFailureKind(value pgtype.Text) (model.PipelineTriggerRetryFailureKind, bool, error) {
	if !value.Valid || value.String == "" {
		return model.PipelineTriggerRetryFailureKindUnknown, false, nil
	}
	parsed, err := model.PipelineTriggerRetryFailureKindString(value.String)
	if err != nil {
		return model.PipelineTriggerRetryFailureKindUnknown, false, xerror.Unexpected().With(err)
	}
	if parsed == model.PipelineTriggerRetryFailureKindUnknown {
		return model.PipelineTriggerRetryFailureKindUnknown, false, nil
	}
	return parsed, true, nil
}

// retrySourceScheduleSlug converts optional schedule trigger slug source context to SQL text.
func retrySourceScheduleSlug(source pipelinerun.RunSourceSummary) pgtype.Text {
	if source.Schedule == nil || source.Schedule.TriggerSlug == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: source.Schedule.TriggerSlug.String(), Valid: true}
}

// retrySourceScheduleRevision converts optional schedule trigger revision source context to SQL int8.
func retrySourceScheduleRevision(source pipelinerun.RunSourceSummary) pgtype.Int8 {
	if source.Schedule == nil || source.Schedule.TriggerRevision == 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: source.Schedule.TriggerRevision, Valid: true}
}

// retrySourceScheduleText converts optional schedule source text fields to nullable SQL text.
func retrySourceScheduleText(source pipelinerun.RunSourceSummary, value func(*pipelinerun.RunSourceScheduleSummary) string) pgtype.Text {
	if source.Schedule == nil {
		return pgtype.Text{}
	}
	text := value(source.Schedule)
	if text == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: text, Valid: true}
}

// retrySourceScheduleTime converts optional schedule source timestamps to nullable SQL timestamps.
func retrySourceScheduleTime(source pipelinerun.RunSourceSummary, value func(*pipelinerun.RunSourceScheduleSummary) time.Time) pgtype.Timestamptz {
	if source.Schedule == nil {
		return pgtype.Timestamptz{}
	}
	ts := value(source.Schedule)
	if ts.IsZero() {
		return pgtype.Timestamptz{}
	}
	return postgres.ToTimestamptz(ts)
}

// optionalSlugText converts an optional slug pointer to nullable SQL text.
func optionalSlugText(value *slug.Slug) pgtype.Text {
	if value == nil || *value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value.String(), Valid: true}
}

// optionalTextSlug converts nullable SQL text to an optional slug pointer.
func optionalTextSlug(value pgtype.Text) *slug.Slug {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed := slug.FromString(value.String)
	return &parsed
}

// optionalSlugValue converts optional SQL string columns to slug pointers.
func optionalSlugValue(value string) *slug.Slug {
	if value == "" {
		return nil
	}
	parsed := slug.FromString(value)
	return &parsed
}

// textOrNull stores empty strings as NULL in retry-attempt columns.
func textOrNull(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

// nullableTextValue converts nullable SQL text to a plain string for projections.
func nullableTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// ReconcileLogicalRunFinalizationsForSystem is bounded platform maintenance;
// it uses only persisted explicit final decisions, never absence of a retry.
func (r *repository) ReconcileLogicalRunFinalizationsForSystem(ctx context.Context, tx postgres.DBTX) error {
	if err := dbgen.New(tx).ReconcilePipelineRunFinalizationsForSystem(ctx); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *repository) ListPendingRunFinalizationsForSystem(ctx context.Context, tx postgres.DBTX) ([]PendingRunFinalization, error) {
	rows, err := dbgen.New(tx).ListPendingRunFinalizationsForSystem(ctx)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]PendingRunFinalization, 0, len(rows))
	for _, row := range rows {
		result = append(result, PendingRunFinalization{OrgSlug: row.OrgSlug, ProjectSlug: row.ProjectSlug, EnvironmentSlug: row.EnvironmentSlug, TriggerSlug: row.TriggerSlug, TriggerRevision: row.TriggerRevision, ScheduledAt: row.ScheduledAt.Time, RetryAttemptSlug: row.RetryAttemptSlug, ReceiverID: row.ReceiverID, DeliveryID: row.DeliveryID, AttemptNumber: int(row.AttemptNumber)})
	}
	return result, nil
}
