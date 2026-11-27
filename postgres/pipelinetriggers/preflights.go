package pipelinetriggers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func (r *repository) ClaimSchedulePreflight(ctx context.Context, tx postgres.DBTX, ref ScheduleFireExecutionRef, preflightAt time.Time) (bool, error) {
	count, err := dbgen.New(tx).ClaimSchedulePreflight(ctx, dbgen.ClaimSchedulePreflightParams{
		OrgSlug: ref.OrgSlug, ProjectSlug: ref.ProjectSlug, EnvironmentSlug: ref.EnvironmentSlug,
		TriggerSlug: ref.TriggerSlug, TriggerRevision: ref.TriggerRevision,
		ScheduledFireAt: postgres.ToTimestamptz(ref.ScheduledFireAt), PreflightAt: postgres.ToTimestamptz(preflightAt),
	})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return count == 1, nil
}

func (r *repository) LoadPendingSchedulePreflight(ctx context.Context, tx postgres.DBTX, ref ScheduleFireExecutionRef) (*PendingSchedulePreflight, error) {
	row, err := dbgen.New(tx).LoadPendingSchedulePreflight(ctx, dbgen.LoadPendingSchedulePreflightParams{
		OrgSlug: ref.OrgSlug, ProjectSlug: ref.ProjectSlug, EnvironmentSlug: ref.EnvironmentSlug,
		TriggerSlug: ref.TriggerSlug, TriggerRevision: ref.TriggerRevision, ScheduledFireAt: postgres.ToTimestamptz(ref.ScheduledFireAt),
	})
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // A completed or superseded preflight needs no work.
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	mode, err := model.PipelineTriggerVersionPolicyModeString(row.VersionPolicy)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	pending := &PendingSchedulePreflight{
		PipelineSlug:  slug.FromString(row.PipelineSlug),
		VersionPolicy: model.PipelineTriggerVersionPolicy{Mode: mode, PinnedVersionHash: optionalTextString(row.PinnedVersionHash)},
		VersionHash:   optionalTextString(row.PipelineVersionHash), ResolutionDigest: optionalTextString(row.ResolutionDigest),
	}
	if len(row.SchemaRefreshes) > 0 {
		if err := json.Unmarshal(row.SchemaRefreshes, &pending.SchemaRefreshes); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
	}
	return pending, nil
}

func (r *repository) SaveSchedulePreflight(ctx context.Context, tx postgres.DBTX, ref ScheduleFireExecutionRef, progress SchedulePreflightProgress) error {
	if progress.Diagnostics == nil {
		progress.Diagnostics = []model.PipelineSchedulePreflightDiagnostic{}
	}
	diagnostics, err := json.Marshal(progress.Diagnostics)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	var refreshes []byte
	if progress.SchemaRefreshes != nil {
		refreshes, err = json.Marshal(progress.SchemaRefreshes)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
	}
	count, err := dbgen.New(tx).SaveSchedulePreflight(ctx, dbgen.SaveSchedulePreflightParams{
		OrgSlug: ref.OrgSlug, ProjectSlug: ref.ProjectSlug, EnvironmentSlug: ref.EnvironmentSlug,
		TriggerSlug: ref.TriggerSlug, TriggerRevision: ref.TriggerRevision, ScheduledFireAt: postgres.ToTimestamptz(ref.ScheduledFireAt),
		PipelineVersionHash: optionalString(progress.VersionHash), ResolutionDigest: optionalString(progress.ResolutionDigest),
		SchemaRefreshes: refreshes, Diagnostics: diagnostics, CompletedAt: postgres.ToNullableTimestamptz(progress.CompletedAt),
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if count != 1 {
		return xerror.Unexpected().Detail("pending schedule preflight changed before it could be saved")
	}
	return nil
}

func (r *repository) ListUpcomingSchedulePreflights(ctx context.Context, tx postgres.DBTX, org, project, environment, trigger slug.Slug) ([]*model.PipelineSchedulePreflight, error) {
	rows, err := dbgen.New(tx).ListUpcomingSchedulePreflights(ctx, dbgen.ListUpcomingSchedulePreflightsParams{
		OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, TriggerSlug: trigger,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	preflights := make([]*model.PipelineSchedulePreflight, 0, len(rows))
	for _, row := range rows {
		preflight, err := schedulePreflightFromRow(row.TriggerRevision, row.PipelineSlug, row.ScheduledFireAt, row.PreflightAt, row.CompletedAt, row.PipelineVersionHash, row.Diagnostics)
		if err != nil {
			return nil, err
		}
		preflights = append(preflights, preflight)
	}
	return preflights, nil
}

func schedulePreflightFromRow(revision int64, pipeline string, scheduled, due, completed pgtype.Timestamptz, version pgtype.Text, diagnostics []byte) (*model.PipelineSchedulePreflight, error) {
	preflight := &model.PipelineSchedulePreflight{
		TriggerRevision: revision, PipelineSlug: slug.FromString(pipeline),
		ScheduledFireAt: scheduled.Time, PreflightAt: due.Time,
		CompletedAt: postgres.FromNullableTimestamptz(completed), VersionHash: optionalTextString(version),
	}
	if err := json.Unmarshal(diagnostics, &preflight.Diagnostics); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return preflight, nil
}
