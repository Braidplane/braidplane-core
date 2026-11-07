package runrecency

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
	"github.com/jackc/pgx/v5"
)

func (*Repository) Inspect(ctx context.Context, db postgres.DBTX, scope Scope) (Expectation, error) {
	r, err := dbgen.New(db).InspectRunRecency(ctx, dbgen.InspectRunRecencyParams{OrgSlug: scope.Org, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment, PipelineSlug: scope.Pipeline})
	if xerror.Is(err, pgx.ErrNoRows) {
		return Expectation{}, xerror.NotFound(xerror.Pipeline).With(err)
	}
	if err != nil {
		return Expectation{}, xerror.Wrapf("inspect recency: %w", xerror.Unexpected().With(err))
	}
	return Expectation{Scope: scope, ID: r.ExpectationID, EnvironmentID: int64(r.ProjectEnvironmentID), Revision: r.Revision, MaximumAge: time.Duration(r.MaximumAgeSeconds) * time.Second, Enabled: r.Enabled, EnabledAt: r.EnabledAt.Time, DeploymentID: r.DeploymentID, DeploymentRevision: r.DeploymentRevision, VersionID: r.CurrentVersionID, VersionSpecHash: postgres.FromNullableText(r.VersionSpecHash), ResolutionDigest: postgres.FromNullableText(r.CurrentResolutionDigest), DeployedAt: r.DeployedAt.Time, SuccessRun: postgres.FromNullableText(r.SuccessRunSlug), SucceededAt: postgres.FromNullableTimestamptz(r.SucceededAt), ScopeDisabled: r.ScopeDisabled.Bool}, nil
}

func (*Repository) InspectEpisodes(ctx context.Context, db postgres.DBTX, scope Scope) ([]EpisodeEvidence, error) {
	rows, err := dbgen.New(db).InspectRunRecencyEpisodes(ctx, dbgen.InspectRunRecencyEpisodesParams{OrgSlug: scope.Org, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment, PipelineSlug: scope.Pipeline.String()})
	if err != nil {
		return nil, xerror.Wrapf("inspect recency episodes: %w", xerror.Unexpected().With(err))
	}
	result := make([]EpisodeEvidence, 0, len(rows))
	for _, r := range rows {
		result = append(result, EpisodeEvidence{Episode: Episode{Slug: r.Slug, ExpectationID: r.ExpectationID, DeploymentID: r.DeploymentID, DeploymentRevision: r.DeploymentRevision, Deadline: r.DeadlineAt.Time, BreachedAt: r.BreachedAt.Time, LastSuccessAt: postgres.FromNullableTimestamptz(r.LastSuccessAt), LastSuccessRun: postgres.FromNullableText(r.LastSuccessRunSlug)}, ExpectationRevision: r.ExpectationRevision, MaximumAge: time.Duration(r.MaximumAgeSeconds) * time.Second, EnabledAt: r.EnabledAt.Time, VersionSpecHash: r.VersionSpecHash, ResolutionDigest: postgres.FromNullableText(r.DeploymentResolutionDigest), DeployedAt: r.DeployedAt.Time, ResolvedAt: postgres.FromNullableTimestamptz(r.ResolvedAt), ResolutionReason: postgres.FromNullableText(r.ResolutionReason), RecoveryRun: postgres.FromNullableText(r.RecoveryRunSlug), RecoverySucceededAt: postgres.FromNullableTimestamptz(r.RecoverySucceededAt)})
	}
	return result, nil
}
