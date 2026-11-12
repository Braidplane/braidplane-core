package pipelinetriggers

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// ListDueScheduleTriggersForSystem returns enabled schedule triggers for the
// system-only dispatcher scan. The scan intentionally crosses tenants; claim,
// completion, and execution calls must use the returned tenant slugs to preserve
// tenant-scoped writes and events.
func (r *repository) ListDueScheduleTriggersForSystem(
	ctx context.Context,
	tx postgres.DBTX,
) ([]DueScheduleTrigger, error) {
	rows, err := dbgen.New(tx).ListDueScheduleTriggers(ctx)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	triggers := make([]DueScheduleTrigger, 0, len(rows))
	for _, row := range rows {
		versionPolicyMode, err := model.PipelineTriggerVersionPolicyModeString(row.VersionPolicy)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		misfirePolicy, err := model.PipelineTriggerScheduleMisfirePolicyString(row.ScheduleMisfirePolicy.String)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		backoffStrategy, err := model.PipelineTriggerBackoffStrategyString(row.RetryBackoffStrategy)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		retryableKinds, err := retryFailureKindsFromStrings(row.RetryableFailureKinds)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, DueScheduleTrigger{
			OrgSlug:         row.OrgSlug,
			ProjectSlug:     row.ProjectSlug,
			EnvironmentSlug: row.EnvironmentSlug,
			TriggerSlug:     row.TriggerSlug,
			TriggerRevision: row.TriggerRevision,
			Enabled:         row.Enabled,
			PipelineSlug:    row.PipelineSlug,
			VersionPolicy: model.PipelineTriggerVersionPolicy{
				Mode:              versionPolicyMode,
				PinnedVersionHash: optionalTextString(row.PinnedVersionHash),
			},
			RuntimeInputKeys: row.RuntimeInputKeys,
			RetryPolicy: model.PipelineTriggerRetryPolicy{
				Enabled:               row.RetryEnabled,
				MaxAttempts:           int(row.RetryMaxAttempts),
				BackoffStrategy:       backoffStrategy,
				InitialDelaySeconds:   int(row.RetryInitialDelaySeconds),
				MaxDelaySeconds:       int(row.RetryMaxDelaySeconds),
				RetryableFailureKinds: retryableKinds,
				MaxElapsedSeconds:     optionalIntValue(row.RetryMaxElapsedSeconds),
			},
			ScheduleConfig: model.PipelineTriggerScheduleConfig{
				Cron:          row.ScheduleCron.String,
				Timezone:      row.ScheduleTimezone.String,
				MisfirePolicy: misfirePolicy,
			},
			TriggerCreatedAt:    row.TriggerCreatedAt.Time,
			LastScheduledFireAt: postgres.FromNullableTimestamptz(row.LastScheduledFireAt),
		})
	}
	return triggers, nil
}

// ClaimScheduleFire atomically inserts a schedule fire or returns the existing fire slug.
func (r *repository) ClaimScheduleFire(
	ctx context.Context,
	tx postgres.DBTX,
	claim ScheduleFireClaim,
) (ScheduleFireClaimResult, error) {
	row, err := dbgen.New(tx).ClaimScheduleFire(ctx, dbgen.ClaimScheduleFireParams{
		OrgSlug:         claim.OrgSlug,
		ProjectSlug:     claim.ProjectSlug,
		EnvironmentSlug: claim.EnvironmentSlug,
		TriggerSlug:     claim.TriggerSlug,
		TriggerRevision: claim.TriggerRevision,
		FireSlug:        claim.FireSlug().String(),
		ScheduledFireAt: postgres.ToTimestamptz(claim.ScheduledFireAt),
		ActualFireAt:    postgres.ToTimestamptz(claim.ActualFireAt),
		MisfireResult:   claim.MisfireResult.String(),
	})
	if err != nil {
		return ScheduleFireClaimResult{}, xerror.Unexpected().With(err)
	}
	if !row.FireSlug.Valid || row.FireSlug.String == "" {
		return ScheduleFireClaimResult{}, ErrPipelineTriggerNotFound
	}
	return ScheduleFireClaimResult{
		Claimed:  row.Claimed,
		FireSlug: slug.FromString(row.FireSlug.String),
	}, nil
}

// LoadClaimedScheduleFire returns the immutable trigger snapshot for one claimed fire.
func (r *repository) LoadClaimedScheduleFire(
	ctx context.Context,
	tx postgres.DBTX,
	ref ScheduleFireExecutionRef,
) (ClaimedScheduleFire, error) {
	row, err := dbgen.New(tx).LoadClaimedScheduleFire(ctx, dbgen.LoadClaimedScheduleFireParams{
		OrgSlug:         ref.OrgSlug,
		ProjectSlug:     ref.ProjectSlug,
		EnvironmentSlug: ref.EnvironmentSlug,
		TriggerSlug:     ref.TriggerSlug,
		TriggerRevision: ref.TriggerRevision,
		ScheduledFireAt: postgres.ToTimestamptz(ref.ScheduledFireAt),
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return ClaimedScheduleFire{}, ErrPipelineTriggerNotFound.With(err)
		}
		return ClaimedScheduleFire{}, xerror.Unexpected().With(err)
	}

	versionPolicyMode, err := model.PipelineTriggerVersionPolicyModeString(row.VersionPolicy)
	if err != nil {
		return ClaimedScheduleFire{}, xerror.Unexpected().With(err)
	}
	misfireResult, err := model.PipelineTriggerScheduleMisfireResultString(row.MisfireResult)
	if err != nil {
		return ClaimedScheduleFire{}, xerror.Unexpected().With(err)
	}
	misfirePolicy, err := model.PipelineTriggerScheduleMisfirePolicyString(row.ScheduleMisfirePolicy)
	if err != nil {
		return ClaimedScheduleFire{}, xerror.Unexpected().With(err)
	}
	backoffStrategy, err := model.PipelineTriggerBackoffStrategyString(row.RetryBackoffStrategy)
	if err != nil {
		return ClaimedScheduleFire{}, xerror.Unexpected().With(err)
	}
	retryableKinds, err := retryFailureKindsFromStrings(row.RetryableFailureKinds)
	if err != nil {
		return ClaimedScheduleFire{}, err
	}

	return ClaimedScheduleFire{
		Resuming: row.Status == "running",
		Trigger: DueScheduleTrigger{
			OrgSlug:         row.OrgSlug,
			ProjectSlug:     row.ProjectSlug,
			EnvironmentSlug: row.EnvironmentSlug,
			TriggerSlug:     row.TriggerSlug,
			TriggerRevision: row.TriggerRevision,
			Enabled:         true,
			PipelineSlug:    slug.FromString(row.PipelineSlug),
			VersionPolicy: model.PipelineTriggerVersionPolicy{
				Mode:              versionPolicyMode,
				PinnedVersionHash: optionalTextString(row.PinnedVersionHash),
			},
			RuntimeInputKeys: row.RuntimeInputKeys,
			RetryPolicy: model.PipelineTriggerRetryPolicy{
				Enabled:               row.RetryEnabled,
				MaxAttempts:           int(row.RetryMaxAttempts),
				BackoffStrategy:       backoffStrategy,
				InitialDelaySeconds:   int(row.RetryInitialDelaySeconds),
				MaxDelaySeconds:       int(row.RetryMaxDelaySeconds),
				RetryableFailureKinds: retryableKinds,
				MaxElapsedSeconds:     optionalIntValue(row.RetryMaxElapsedSeconds),
			},
			ScheduleConfig: model.PipelineTriggerScheduleConfig{
				Cron:          row.ScheduleCron,
				Timezone:      row.ScheduleTimezone,
				MisfirePolicy: misfirePolicy,
			},
			TriggerCreatedAt: row.TriggerCreatedAt.Time,
		},
		Claim: ScheduleFireClaim{
			OrgSlug:         row.OrgSlug,
			ProjectSlug:     row.ProjectSlug,
			EnvironmentSlug: row.EnvironmentSlug,
			TriggerSlug:     row.TriggerSlug,
			TriggerRevision: row.TriggerRevision,
			ScheduledFireAt: row.ScheduledFireAt.Time,
			ActualFireAt:    row.ActualFireAt.Time,
			MisfireResult:   misfireResult,
		},
		FireSlug: slug.FromString(row.FireSlug),
	}, nil
}

// MarkScheduleFireRunning claims execution ownership for one still-claimed fire.
func (r *repository) MarkScheduleFireRunning(
	ctx context.Context,
	tx postgres.DBTX,
	ref ScheduleFireExecutionRef,
) error {
	rowsAffected, err := dbgen.New(tx).MarkScheduleFireRunning(ctx, dbgen.MarkScheduleFireRunningParams{
		OrgSlug:         ref.OrgSlug,
		ProjectSlug:     ref.ProjectSlug,
		EnvironmentSlug: ref.EnvironmentSlug,
		TriggerSlug:     ref.TriggerSlug,
		TriggerRevision: ref.TriggerRevision,
		ScheduledFireAt: postgres.ToTimestamptz(ref.ScheduledFireAt),
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrPipelineTriggerNotFound
	}
	return nil
}

// CompleteScheduleFire records the terminal outcome for a claimed schedule fire.
func (r *repository) CompleteScheduleFire(
	ctx context.Context,
	tx postgres.DBTX,
	completion ScheduleFireCompletion,
) error {
	rowsAffected, err := dbgen.New(tx).CompleteScheduleFire(ctx, dbgen.CompleteScheduleFireParams{
		Status:          completion.Status,
		RunSlug:         postgres.ToNullableText(completion.RunSlug),
		SourceEventSlug: postgres.OptionalSlugToText(completion.SourceEventSlug),
		ErrorSummary:    postgres.ToNullableText(completion.ErrorSummary),
		OrgSlug:         completion.OrgSlug,
		ProjectSlug:     completion.ProjectSlug,
		EnvironmentSlug: completion.EnvironmentSlug,
		TriggerSlug:     completion.TriggerSlug,
		TriggerRevision: completion.TriggerRevision,
		ScheduledFireAt: postgres.ToTimestamptz(completion.ScheduledFireAt),
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrPipelineTriggerNotFound
	}
	return nil
}

// ListScheduleFiresForTrigger returns paginated schedule-fire diagnostics for one trigger.
func (r *repository) ListScheduleFiresForTrigger(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	req model.ListRequest,
) ([]ScheduleFireDiagnostic, error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortScheduledAt)
	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(queryReq.Cursor, normalized.SortBy)
	if err != nil {
		return nil, xerror.Wrapf("build pipeline schedule fire cursor values: %w", err)
	}
	rows, err := dbgen.New(tx).ListScheduleFiresForTrigger(ctx, dbgen.ListScheduleFiresForTriggerParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		TriggerSlug:     triggerSlug,
		CursorSlug:      cursorSlug,
		OrderDir:        pgtype.Text{String: queryReq.Order, Valid: queryReq.Order != ""},
		CursorTimestamp: cursorTimestamp,
		LimitCount:      int64(queryReq.Limit),
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	fires := make([]ScheduleFireDiagnostic, 0, len(rows))
	for _, row := range rows {
		fire, err := scheduleFireDiagnosticFromRow(row)
		if err != nil {
			return nil, err
		}
		fires = append(fires, fire)
	}
	return fires, nil
}

// scheduleFireDiagnosticFromRow maps persisted schedule-fire state to a safe read projection.
func scheduleFireDiagnosticFromRow(row dbgen.ListScheduleFiresForTriggerRow) (ScheduleFireDiagnostic, error) {
	status, err := model.PipelineScheduleFireStatusString(row.Status)
	if err != nil {
		return ScheduleFireDiagnostic{}, xerror.Unexpected().With(err)
	}
	misfireResult, err := model.PipelineTriggerScheduleMisfireResultString(row.MisfireResult)
	if err != nil {
		return ScheduleFireDiagnostic{}, xerror.Unexpected().With(err)
	}
	return ScheduleFireDiagnostic{
		FireSlug:        slug.FromString(row.FireSlug),
		TriggerSlug:     row.TriggerSlug,
		TriggerRevision: row.TriggerRevision,
		PipelineSlug:    slug.FromString(row.PipelineSlug),
		ScheduledFireAt: row.ScheduledFireAt.Time,
		ActualFireAt:    row.ActualFireAt.Time,
		Status:          status,
		MisfireResult:   misfireResult,
		RunSlug:         optionalTextSlug(row.RunSlug),
		SourceEventSlug: optionalTextSlug(row.SourceEventSlug),
		ErrorSummary:    optionalTextString(row.ErrorSummary),
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}, nil
}

// ListDelayedScheduleFiresForSystem is a system-only cross-tenant observation scan.
// Every candidate retains the tenant scope returned by this query.
func (r *repository) ListDelayedScheduleFiresForSystem(ctx context.Context, tx postgres.DBTX, before time.Time) ([]DelayedScheduleFire, error) {
	rows, err := dbgen.New(tx).ListDelayedScheduleFiresForSystem(ctx, postgres.ToTimestamptz(before))
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	fires := make([]DelayedScheduleFire, 0, len(rows))
	for _, row := range rows {
		fires = append(fires, DelayedScheduleFire{
			OrgSlug: row.OrgSlug, ProjectSlug: row.ProjectSlug, EnvironmentSlug: row.EnvironmentSlug,
			TriggerSlug: row.TriggerSlug, PipelineSlug: slug.Slug(row.PipelineSlug), TriggerRevision: row.TriggerRevision,
			ScheduledFireAt: row.ScheduledFireAt.Time, ActualFireAt: row.ActualFireAt.Time,
		})
	}
	return fires, nil
}
