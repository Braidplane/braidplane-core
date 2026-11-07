package pipelineruns

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// Repository provides pipeline run persistence operations.
type Repository interface {
	BindWorkerJob(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string, int64) error
	ListInterruptedForSystem(context.Context, postgres.DBTX, time.Time) ([]InterruptedRun, error)
	LockRecoveryFence(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) error
	InspectLogicalRun(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.PipelineRunCompletion, error)
	ListCompletionDecisions(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) ([]model.PipelineCompletionDecision, error)

	PreviousSuccess(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, time.Time) (*pipelineruntime.SuccessfulRun, error)
	CreateRun(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params *CreateRunParams,
	) (*PipelineRun, error)
	MarkRunRunning(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		pipelineSlug slug.Slug,
		runSlug string,
		startedAt time.Time,
		timeline json.RawMessage,
	) (*PipelineRun, error)
	CompleteRun(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		runSlug string,
		params *CompleteRunParams,
	) (*PipelineRun, error)
	FindRunBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		runSlug string,
	) (*PipelineRun, error)
	ListRuns(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		filters *RunListFilters,
		req model.ListRequest,
	) ([]*PipelineRun, error)
	UpdateRunSourceEvent(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		runSlug string,
		sourceEventSlug slug.Slug,
	) error
	FindLatestRunByPipelineSlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	) (*PipelineRun, error)
}

type repository struct{}

const (
	defaultPipelineAPIVersion = "braidplane.com/pipeline/v0alpha1"
	maxInt32Value             = 2147483647
	minInt32Value             = -2147483648
)

// New creates a new pipeline run repository.
func New() Repository {
	return &repository{}
}

func emptyStringNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func slugToStringPtr(value *slug.Slug) *string {
	if value == nil || *value == "" {
		return nil
	}
	text := value.String()
	return &text
}

func normalizeIncrementalSummary(
	summary pipelinerun.IncrementalRunSummary,
) pipelinerun.IncrementalRunSummary {
	if summary.Mode == "" {
		summary.Mode = "full"
	}
	if summary.CheckpointStatus == "" {
		summary.CheckpointStatus = "not_applicable"
	}
	if summary.ReplaySafetyStatus == "" {
		summary.ReplaySafetyStatus = "safe"
	}
	return summary
}

func normalizeRunSource(source pipelinerun.RunSourceSummary, requestID *string) pipelinerun.RunSourceSummary {
	if source.Type == model.PipelineRunSourceTypeUnknown {
		source.Type = model.PipelineRunSourceTypeManual
	}
	if source.Summary == "" {
		source.Summary = "Manual run"
	}
	if source.Entrypoint == model.PipelineRunSourceEntrypointUnknown {
		source.Entrypoint = model.PipelineRunSourceEntrypointUI
	}
	if source.RequestID == nil {
		source.RequestID = requestID
	}
	if source.ParameterKeys == nil {
		source.ParameterKeys = []string{}
	}
	if source.AttemptNumber <= 0 {
		source.AttemptNumber = 1
	}
	return source
}

func normalizeAPIVersion(apiVersion string) string {
	if apiVersion == "" {
		return defaultPipelineAPIVersion
	}
	return apiVersion
}

func sourceActorSlugText(source pipelinerun.RunSourceSummary) pgtype.Text {
	if source.ActorSlug == nil || *source.ActorSlug == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: source.ActorSlug.String(), Valid: true}
}

func sourceScheduleSlugText(source pipelinerun.RunSourceSummary) pgtype.Text {
	if source.Schedule == nil || source.Schedule.TriggerSlug == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: source.Schedule.TriggerSlug.String(), Valid: true}
}

func sourceScheduleRevisionInt8(source pipelinerun.RunSourceSummary) pgtype.Int8 {
	if source.Schedule == nil || source.Schedule.TriggerRevision == 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: source.Schedule.TriggerRevision, Valid: true}
}

func sourceScheduleText(source pipelinerun.RunSourceSummary, value func(*pipelinerun.RunSourceScheduleSummary) string) pgtype.Text {
	if source.Schedule == nil {
		return pgtype.Text{}
	}
	text := value(source.Schedule)
	if text == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: text, Valid: true}
}

func sourceScheduleTime(source pipelinerun.RunSourceSummary, value func(*pipelinerun.RunSourceScheduleSummary) time.Time) pgtype.Timestamptz {
	if source.Schedule == nil {
		return pgtype.Timestamptz{}
	}
	ts := value(source.Schedule)
	if ts.IsZero() {
		return pgtype.Timestamptz{}
	}
	return postgres.ToTimestamptz(ts)
}

func sourceWebhookDeliveryID(source pipelinerun.RunSourceSummary) pgtype.Int8 {
	if source.Webhook == nil || source.Webhook.DeliveryRecordID <= 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: source.Webhook.DeliveryRecordID, Valid: true}
}

// sourceAttemptNumberInt32 validates the attempt number before passing it to SQLC.
func sourceAttemptNumberInt32(source pipelinerun.RunSourceSummary) (int32, error) {
	if source.AttemptNumber < 1 || source.AttemptNumber > maxInt32Value {
		return 0, xerror.InvalidInput(xerror.PipelineRun).
			Detail("run source attempt_number must be between 1 and int32 max")
	}
	return int32(source.AttemptNumber), nil
}

// prepareIncrementalForPersist applies default diagnostics and validates DB-bound integer fields.
func prepareIncrementalForPersist(
	incremental pipelinerun.IncrementalRunSummary,
	diagnostics json.RawMessage,
) (pipelinerun.IncrementalRunSummary, json.RawMessage, int32, int32, error) {
	incremental = normalizeIncrementalSummary(incremental)
	if len(diagnostics) == 0 {
		diagnostics = []byte(`{"sources":[],"replay_safety":{"status":"safe"}}`)
	}
	backfillChunkIndex, err := backfillChunkSQLValue(incremental.BackfillChunkIndex)
	if err != nil {
		return incremental, nil, 0, 0, err
	}
	backfillChunkCount, err := backfillChunkSQLValue(incremental.BackfillChunkCount)
	if err != nil {
		return incremental, nil, 0, 0, err
	}

	return incremental, diagnostics, backfillChunkIndex, backfillChunkCount, nil
}

// backfillChunkSQLValue validates backfill metadata before storing it in PostgreSQL int4 columns.
func backfillChunkSQLValue(value int) (int32, error) {
	if value > maxInt32Value || value < minInt32Value {
		return 0, xerror.InvalidInput(xerror.PipelineRun).
			Detail("backfill chunk metadata exceeds int32 range")
	}
	return int32(value), nil
}

func (r *repository) CreateRun(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params *CreateRunParams,
) (*PipelineRun, error) {
	incremental, incrementalDiagnostics, backfillChunkIndex, backfillChunkCount, err := prepareIncrementalForPersist(
		params.Incremental,
		params.IncrementalDiagnostics,
	)
	if err != nil {
		return nil, err
	}
	source := normalizeRunSource(params.Source, params.RequestID)
	source.EnvironmentSlug = environmentSlug
	source = pipelinerun.NormalizeAttemptSource(source, slug.FromString(params.RunSlug))
	sourceAttemptNumber, err := sourceAttemptNumberInt32(source)
	if err != nil {
		return nil, err
	}

	queries := dbgen.New(tx)
	row, err := queries.CreatePipelineRun(ctx, dbgen.CreatePipelineRunParams{
		PipelineVersionID:             optionalVersionID(params.PipelineVersionID),
		WorkerJobID:                   postgres.ToNullableInt64(params.WorkerJobID),
		DeploymentID:                  postgres.ToNullableInt64(params.DeploymentID),
		DeploymentRevision:            postgres.ToNullableInt64(params.DeploymentRevision),
		DeploymentResolutionDigest:    postgres.ToNullableText(params.DeploymentResolutionDigest),
		OrgSlug:                       orgSlug,
		ProjectSlug:                   projectSlug,
		EnvironmentSlug:               environmentSlug,
		ApiVersion:                    normalizeAPIVersion(params.APIVersion),
		PipelineSlug:                  params.PipelineSlug,
		RunSlug:                       params.RunSlug,
		RequestID:                     postgres.ToNullableText(params.RequestID),
		SourceType:                    source.Type.String(),
		SourceSummary:                 source.Summary,
		SourceEntrypoint:              source.Entrypoint.String(),
		SourceActorType:               source.ActorType.String(),
		SourceActorSlug:               sourceActorSlugText(source),
		SourceRequestID:               postgres.ToNullableText(source.RequestID),
		SourceTraceID:                 postgres.ToNullableText(source.TraceID),
		SourceParameterKeys:           source.ParameterKeys,
		SourceScheduleTriggerSlug:     sourceScheduleSlugText(source),
		SourceScheduleTriggerRevision: sourceScheduleRevisionInt8(source),
		SourceScheduleCron: sourceScheduleText(source, func(summary *pipelinerun.RunSourceScheduleSummary) string {
			return summary.Cron
		}),
		SourceScheduleTimezone: sourceScheduleText(source, func(summary *pipelinerun.RunSourceScheduleSummary) string {
			return summary.Timezone
		}),
		SourceScheduleScheduledFireAt: sourceScheduleTime(source, func(summary *pipelinerun.RunSourceScheduleSummary) time.Time {
			return summary.ScheduledFireAt
		}),
		SourceScheduleActualFireAt: sourceScheduleTime(source, func(summary *pipelinerun.RunSourceScheduleSummary) time.Time {
			return summary.ActualFireAt
		}),
		SourceScheduleMisfireResult: sourceScheduleText(source, func(summary *pipelinerun.RunSourceScheduleSummary) string {
			return summary.MisfireResult.String()
		}),
		SourceWebhookDeliveryID: sourceWebhookDeliveryID(source),
		Status:                  params.Status,
		DurationMs:              params.DurationMs,
		StepCount:               params.StepCount,
		StepSlug:                params.StepSlug,
		ErrorSummary:            postgres.ToNullableText(params.ErrorSummary),
		FailureKind:             postgres.ToNullableText(params.FailureKind),
		FailureSide:             postgres.ToNullableText(params.FailureSide),
		StartedAt:               postgres.ToTimestamptz(params.StartedAt),
		EndedAt:                 postgres.ToNullableTimestamptz(params.EndedAt),
		ReplayOfRunSlug:         postgres.ToNullableText(params.ReplayOfRunSlug),
		ReplayFromStep:          postgres.ToNullableText(params.ReplayFromStep),
		SourceAttemptNumber:     sourceAttemptNumber,
		SourceRootRunSlug:       postgres.SlugToText(source.RootRunSlug),
		SourceRetryOfRunSlug: postgres.ToNullableText(slugToStringPtr(
			source.RetryOfRunSlug,
		)),
		RunMode:          incremental.Mode,
		CheckpointStatus: incremental.CheckpointStatus,
		IncrementalSourceStepSlug: postgres.ToNullableText(
			emptyStringNil(incremental.SourceStepSlug),
		),
		IncrementalBlockedReason: postgres.ToNullableText(
			emptyStringNil(incremental.BlockedReason),
		),
		ReplaySafetyStatus: postgres.ToNullableText(
			emptyStringNil(incremental.ReplaySafetyStatus),
		),
		ReplayBlockedReason: postgres.ToNullableText(
			emptyStringNil(incremental.ReplayBlockedReason),
		),
		BackfillSeriesSlug: postgres.ToNullableText(
			emptyStringNil(incremental.BackfillSeriesSlug),
		),
		BackfillChunkIndex:     backfillChunkIndex,
		BackfillChunkCount:     backfillChunkCount,
		IncrementalDiagnostics: incrementalDiagnostics,
		Timeline:               params.Timeline,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound.With(err)
		}
		return nil, err
	}

	if !row.Inserted {
		return nil, ErrRunAlreadyExists.Detailf("%s", params.RunSlug)
	}
	return toPipelineRun(row.PipelineRun, environmentSlug.String(), row.SourceWebhook), nil
}

// MarkRunRunning records that a queued run has started worker execution.
func (r *repository) MarkRunRunning(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	pipelineSlug slug.Slug,
	runSlug string,
	startedAt time.Time,
	timeline json.RawMessage,
) (*PipelineRun, error) {
	queries := dbgen.New(tx)
	row, err := queries.MarkPipelineRunRunning(ctx, dbgen.MarkPipelineRunRunningParams{
		StartedAt:       postgres.ToTimestamptz(startedAt),
		Timeline:        timeline,
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		PipelineSlug:    pipelineSlug.String(),
		RunSlug:         runSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound.With(err)
		}
		return nil, err
	}
	return toPipelineRun(row.PipelineRun, environmentSlug.String(), row.SourceWebhook), nil
}

// CompleteRun replaces a queued or running run row with terminal execution diagnostics.
func (r *repository) CompleteRun(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
	params *CompleteRunParams,
) (*PipelineRun, error) {
	incremental, incrementalDiagnostics, backfillChunkIndex, backfillChunkCount, err := prepareIncrementalForPersist(
		params.Incremental,
		params.IncrementalDiagnostics,
	)
	if err != nil {
		return nil, err
	}

	queries := dbgen.New(tx)
	row, err := queries.CompletePipelineRun(ctx, dbgen.CompletePipelineRunParams{
		PipelineVersionID:          optionalVersionID(params.PipelineVersionID),
		DeploymentID:               postgres.ToNullableInt64(params.DeploymentID),
		DeploymentRevision:         postgres.ToNullableInt64(params.DeploymentRevision),
		DeploymentResolutionDigest: postgres.ToNullableText(params.DeploymentResolutionDigest),
		Status:                     params.Status,
		DurationMs:                 params.DurationMs,
		StepCount:                  params.StepCount,
		StepSlug:                   params.StepSlug,
		ErrorSummary:               postgres.ToNullableText(params.ErrorSummary),
		FailureKind:                postgres.ToNullableText(params.FailureKind),
		FailureSide:                postgres.ToNullableText(params.FailureSide),
		StartedAt:                  postgres.ToTimestamptz(params.StartedAt),
		EndedAt:                    postgres.ToNullableTimestamptz(params.EndedAt),
		ReplayOfRunSlug:            postgres.ToNullableText(params.ReplayOfRunSlug),
		ReplayFromStep:             postgres.ToNullableText(params.ReplayFromStep),
		RunMode:                    incremental.Mode,
		CheckpointStatus:           incremental.CheckpointStatus,
		IncrementalSourceStepSlug: postgres.ToNullableText(
			emptyStringNil(incremental.SourceStepSlug),
		),
		IncrementalBlockedReason: postgres.ToNullableText(
			emptyStringNil(incremental.BlockedReason),
		),
		ReplaySafetyStatus: postgres.ToNullableText(
			emptyStringNil(incremental.ReplaySafetyStatus),
		),
		ReplayBlockedReason: postgres.ToNullableText(
			emptyStringNil(incremental.ReplayBlockedReason),
		),
		BackfillSeriesSlug: postgres.ToNullableText(
			emptyStringNil(incremental.BackfillSeriesSlug),
		),
		BackfillChunkIndex:     backfillChunkIndex,
		BackfillChunkCount:     backfillChunkCount,
		IncrementalDiagnostics: incrementalDiagnostics,
		Timeline:               params.Timeline,
		OrgSlug:                orgSlug,
		ProjectSlug:            projectSlug,
		EnvironmentSlug:        environmentSlug,
		PipelineSlug:           params.PipelineSlug,
		RunSlug:                runSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound.With(err)
		}
		return nil, err
	}
	return toPipelineRun(row.PipelineRun, environmentSlug.String(), row.SourceWebhook), nil
}

func (r *repository) UpdateRunSourceEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
	sourceEventSlug slug.Slug,
) error {
	queries := dbgen.New(tx)
	rowsAffected, err := queries.UpdatePipelineRunSourceEvent(ctx, dbgen.UpdatePipelineRunSourceEventParams{
		SourceEventSlug: postgres.SlugToText(sourceEventSlug),
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		RunSlug:         runSlug,
	})
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrRunNotFound
	}
	return nil
}

func (r *repository) ListRuns(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	filters *RunListFilters,
	req model.ListRequest,
) ([]*PipelineRun, error) {
	queries := dbgen.New(tx)

	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(req.Cursor, req.SortBy)
	if err != nil {
		return nil, err
	}

	var status *string
	var pipelineSlug *string
	var mode *string
	var backfillSeriesSlug *string
	if filters != nil {
		status = filters.Status
		pipelineSlug = filters.PipelineSlug
		mode = filters.Mode
		backfillSeriesSlug = filters.BackfillSeriesSlug
	}

	rows, err := queries.ListPipelineRunsByProject(
		ctx,
		dbgen.ListPipelineRunsByProjectParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
			Status:          postgres.ToNullableText(status),
			PipelineSlug:    postgres.ToNullableText(pipelineSlug),
			RunMode:         postgres.ToNullableText(mode),
			BackfillSeriesSlug: postgres.ToNullableText(
				backfillSeriesSlug,
			),
			CursorSlug:      cursorSlug,
			CursorTimestamp: cursorTimestamp,
			OrderDir:        postgres.ToText(req.Order),
			LimitCount:      int64(req.Limit),
		},
	)
	if err != nil {
		return nil, err
	}

	runs := make([]*PipelineRun, len(rows))
	for i, row := range rows {
		runs[i] = toPipelineRun(row.PipelineRun, environmentSlug.String(), row.SourceWebhook)
	}

	return runs, nil
}

func (r *repository) FindLatestRunByPipelineSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
) (*PipelineRun, error) {
	queries := dbgen.New(tx)
	row, err := queries.FindLatestPipelineRunByPipelineSlug(
		ctx,
		dbgen.FindLatestPipelineRunByPipelineSlugParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
			PipelineSlug:    string(pipelineSlug),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound.With(err)
		}
		return nil, err
	}

	return toPipelineRun(row.PipelineRun, environmentSlug.String(), row.SourceWebhook), nil
}

func (r *repository) FindRunBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
) (*PipelineRun, error) {
	queries := dbgen.New(tx)
	row, err := queries.FindPipelineRunByRunSlug(ctx, dbgen.FindPipelineRunByRunSlugParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		RunSlug:         runSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound.With(err)
		}
		return nil, err
	}

	return toPipelineRun(row.PipelineRun, environmentSlug.String(), row.SourceWebhook), nil
}

func (r *repository) PreviousSuccess(ctx context.Context, tx postgres.DBTX, org, project, environment, pipeline slug.Slug, version int64, before time.Time) (*pipelineruntime.SuccessfulRun, error) {
	row, err := dbgen.New(tx).FindPreviousSuccessfulPipelineRun(ctx, dbgen.FindPreviousSuccessfulPipelineRunParams{
		OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, PipelineSlug: pipeline.String(), PipelineVersionID: optionalVersionID(version),
		BeforeAt: pgtype.Timestamptz{Time: before, Valid: true},
	})
	if xerror.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound.With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return &pipelineruntime.SuccessfulRun{RunSlug: row.RunSlug, StartedAt: row.StartedAt.Time.UTC(), FinishedAt: row.EndedAt.Time.UTC()}, nil
}

func optionalVersionID(id int64) pgtype.Int8 { return pgtype.Int8{Int64: id, Valid: id != 0} }

func (r *repository) BindWorkerJob(ctx context.Context, db postgres.DBTX, org, project, environment slug.Slug, run string, job int64) error {
	rows, err := dbgen.New(db).BindPipelineRunWorkerJob(ctx, dbgen.BindPipelineRunWorkerJobParams{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: run, WorkerJobID: pgtype.Int8{Int64: job, Valid: true}})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows == 0 {
		return ErrRunNotFound
	}
	return nil
}

func (r *repository) ListInterruptedForSystem(ctx context.Context, db postgres.DBTX, cutoff time.Time) ([]InterruptedRun, error) {
	rows, err := dbgen.New(db).ListInterruptedPipelineRunsForSystem(ctx, postgres.ToTimestamptz(cutoff))
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]InterruptedRun, len(rows))
	for i, row := range rows {
		result[i] = InterruptedRun{OrgSlug: row.OrgSlug, ProjectSlug: row.ProjectSlug, EnvironmentSlug: row.EnvironmentSlug, RunSlug: row.RunSlug, JobState: row.JobState}
	}
	return result, nil
}

func (r *repository) LockRecoveryFence(ctx context.Context, db postgres.DBTX, org, project, environment slug.Slug, run string) error {
	_, err := dbgen.New(db).LockPipelineRunRecoveryFence(ctx, dbgen.LockPipelineRunRecoveryFenceParams{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: run})
	if xerror.Is(err, sql.ErrNoRows) {
		return ErrRunNotFound.With(err)
	}
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}
