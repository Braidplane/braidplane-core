package retention

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type OrganizationSettings struct {
	Slug     slug.Slug
	Settings []byte
}

type Repository interface {
	PruneCompletionDetailsBefore(context.Context, postgres.DBTX, slug.Slug, time.Time) error
	ListOrganizationSettings(context.Context, postgres.DBTX) ([]OrganizationSettings, error)
	ListRuns(context.Context, postgres.DBTX, slug.Slug) ([]model.OrganizationRetentionRun, error)
	HasSucceededRunForJob(context.Context, postgres.DBTX, slug.Slug, int64) (bool, error)
	CreateRun(context.Context, postgres.DBTX, slug.Slug, int64, model.OrganizationRetentionRun) error
	DeleteRunsBefore(context.Context, postgres.DBTX, slug.Slug, time.Time) (int64, error)
	DeleteQueryExecutionsBefore(context.Context, postgres.DBTX, slug.Slug, time.Time) (int64, error)
	DeleteReplayCheckpointsBefore(context.Context, postgres.DBTX, slug.Slug, time.Time) (int64, error)
	DeletePipelineRunsBefore(context.Context, postgres.DBTX, slug.Slug, time.Time) (int64, error)
	DeleteEventsBefore(context.Context, postgres.DBTX, slug.Slug, time.Time) (int64, error)
	DeleteAbuseControlBucketsBefore(context.Context, postgres.DBTX, time.Time) (int64, error)
}

type repository struct{}

var ErrOrganizationNotFound = xerror.NotFound(xerror.Organization)

func New() Repository { return &repository{} }

func (r *repository) ListOrganizationSettings(ctx context.Context, tx postgres.DBTX) ([]OrganizationSettings, error) {
	rows, err := dbgen.New(tx).ListOrganizationRetentionSettings(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]OrganizationSettings, 0, len(rows))
	for _, row := range rows {
		result = append(result, OrganizationSettings{Slug: row.Slug, Settings: row.Settings})
	}
	return result, nil
}

func (r *repository) ListRuns(
	ctx context.Context,
	tx postgres.DBTX,
	org slug.Slug,
) ([]model.OrganizationRetentionRun, error) {
	rows, err := dbgen.New(tx).ListOrganizationRetentionRuns(ctx, org)
	if err != nil {
		return nil, mapError(err)
	}
	runs := make([]model.OrganizationRetentionRun, 0, len(rows))
	for _, row := range rows {
		var policy *model.OrganizationRetentionPolicy
		if len(row.PolicySnapshot) > 0 && string(row.PolicySnapshot) != "null" {
			policy = &model.OrganizationRetentionPolicy{}
			if err := json.Unmarshal(row.PolicySnapshot, policy); err != nil {
				return nil, xerror.Unexpected().With(err).Detail("unmarshal organization retention policy snapshot")
			}
		}
		runs = append(runs, model.OrganizationRetentionRun{
			Slug:                     row.Slug,
			Attempt:                  int(row.Attempt),
			Status:                   model.OrganizationRetentionRunStatus(row.Status),
			PolicySnapshot:           policy,
			QueryExecutionsDeleted:   row.QueryExecutionsDeleted,
			ReplayCheckpointsDeleted: row.ReplayCheckpointsDeleted,
			PipelineRunsDeleted:      row.PipelineRunsDeleted,
			EventsDeleted:            row.EventsDeleted,
			FailureCode:              model.OrganizationRetentionRunFailureCode(row.FailureCode.String),
			StartedAt:                row.StartedAt.Time,
			CompletedAt:              row.CompletedAt.Time,
		})
	}
	return runs, nil
}

func (r *repository) HasSucceededRunForJob(
	ctx context.Context,
	tx postgres.DBTX,
	org slug.Slug,
	sourceJobID int64,
) (bool, error) {
	succeeded, err := dbgen.New(tx).HasSucceededOrganizationRetentionRunForJob(
		ctx,
		dbgen.HasSucceededOrganizationRetentionRunForJobParams{
			OrgSlug:     org,
			SourceJobID: sourceJobID,
		},
	)
	if err != nil {
		return false, mapError(err)
	}
	return succeeded, nil
}

func (r *repository) CreateRun(
	ctx context.Context,
	tx postgres.DBTX,
	org slug.Slug,
	sourceJobID int64,
	run model.OrganizationRetentionRun,
) error {
	attempt, err := retentionRunAttemptInt32(run.Attempt)
	if err != nil {
		return err
	}
	policySnapshot, err := json.Marshal(run.PolicySnapshot)
	if err != nil {
		return xerror.Unexpected().With(err).Detail("marshal organization retention policy snapshot")
	}
	created, err := dbgen.New(tx).CreateOrganizationRetentionRun(
		ctx,
		dbgen.CreateOrganizationRetentionRunParams{
			OrgSlug:                  org,
			Slug:                     run.Slug,
			SourceJobID:              sourceJobID,
			Attempt:                  attempt,
			Status:                   string(run.Status),
			PolicySnapshot:           policySnapshot,
			QueryExecutionsDeleted:   run.QueryExecutionsDeleted,
			ReplayCheckpointsDeleted: run.ReplayCheckpointsDeleted,
			PipelineRunsDeleted:      run.PipelineRunsDeleted,
			EventsDeleted:            run.EventsDeleted,
			FailureCode:              postgres.ToText(string(run.FailureCode)),
			StartedAt:                postgres.ToTimestamptz(run.StartedAt),
			CompletedAt:              postgres.ToTimestamptz(run.CompletedAt),
		},
	)
	if err != nil {
		return mapError(err)
	}
	if created != 1 {
		return ErrOrganizationNotFound
	}
	return nil
}

func retentionRunAttemptInt32(attempt int) (int32, error) {
	if attempt < 1 || attempt > math.MaxInt32 {
		return 0, xerror.ValidationFailed().Detail("organization retention run attempt must fit PostgreSQL int4")
	}
	return int32(attempt), nil
}

func (r *repository) DeleteRunsBefore(
	ctx context.Context,
	tx postgres.DBTX,
	org slug.Slug,
	cutoff time.Time,
) (int64, error) {
	deleted, err := dbgen.New(tx).DeleteExpiredOrganizationRetentionRuns(
		ctx,
		dbgen.DeleteExpiredOrganizationRetentionRunsParams{
			OrgSlug: org,
			Cutoff:  postgres.ToTimestamptz(cutoff),
		},
	)
	if err != nil {
		return 0, mapError(err)
	}
	return deleted, nil
}

func (r *repository) DeleteQueryExecutionsBefore(ctx context.Context, tx postgres.DBTX, org slug.Slug, cutoff time.Time) (int64, error) {
	deleted, err := dbgen.New(tx).DeleteExpiredOrganizationQueryExecutions(ctx, dbgen.DeleteExpiredOrganizationQueryExecutionsParams{OrgSlug: org, Cutoff: postgres.ToTimestamptz(cutoff)})
	if err != nil {
		return 0, mapError(err)
	}
	return deleted, nil
}

func (r *repository) DeleteReplayCheckpointsBefore(ctx context.Context, tx postgres.DBTX, org slug.Slug, cutoff time.Time) (int64, error) {
	deleted, err := dbgen.New(tx).DeleteExpiredOrganizationReplayCheckpoints(ctx, dbgen.DeleteExpiredOrganizationReplayCheckpointsParams{OrgSlug: org, Cutoff: postgres.ToTimestamptz(cutoff)})
	if err != nil {
		return 0, mapError(err)
	}
	return deleted, nil
}

func (r *repository) DeletePipelineRunsBefore(ctx context.Context, tx postgres.DBTX, org slug.Slug, cutoff time.Time) (int64, error) {
	deleted, err := dbgen.New(tx).DeleteExpiredOrganizationPipelineRuns(ctx, dbgen.DeleteExpiredOrganizationPipelineRunsParams{OrgSlug: org, Cutoff: postgres.ToTimestamptz(cutoff)})
	if err != nil {
		return 0, mapError(err)
	}
	return deleted, nil
}

func (r *repository) DeleteEventsBefore(ctx context.Context, tx postgres.DBTX, org slug.Slug, cutoff time.Time) (int64, error) {
	deleted, err := dbgen.New(tx).DeleteExpiredOrganizationEvents(ctx, dbgen.DeleteExpiredOrganizationEventsParams{OrgSlug: org, Cutoff: postgres.ToTimestamptz(cutoff)})
	if err != nil {
		return 0, mapError(err)
	}
	return deleted, nil
}

func (r *repository) DeleteAbuseControlBucketsBefore(
	ctx context.Context,
	tx postgres.DBTX,
	cutoff time.Time,
) (int64, error) {
	deleted, err := dbgen.New(tx).DeleteExpiredAbuseControlBuckets(ctx, postgres.ToTimestamptz(cutoff))
	if err != nil {
		return 0, mapError(err)
	}
	return deleted, nil
}

func mapError(err error) error {
	return xerror.Unexpected().With(err)
}

func (r *repository) PruneCompletionDetailsBefore(ctx context.Context, db postgres.DBTX, org slug.Slug, cutoff time.Time) error {
	_, err := dbgen.New(db).PrunePipelineCompletionDetails(ctx, dbgen.PrunePipelineCompletionDetailsParams{OrgSlug: org, Cutoff: postgres.ToTimestamptz(cutoff)})
	if err != nil {
		return mapError(err)
	}
	return nil
}
