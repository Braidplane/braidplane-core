package runrecency

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var ErrExpectationNotFound = xerror.NotFound(xerror.Pipeline).Detail("run recency expectation not found")

type Repository struct{}

func New() *Repository { return &Repository{} }

func (*Repository) RecordSuccess(ctx context.Context, tx postgres.DBTX, org, project, environment, run slug.Slug) error {
	err := dbgen.New(tx).RecordRunRecencySuccess(ctx, dbgen.RecordRunRecencySuccessParams{
		OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: run.String(),
	})
	if err != nil {
		return xerror.Wrapf("record successful-run recency evidence: %w", xerror.Unexpected().With(err))
	}
	return nil
}

func (*Repository) ListForSystem(ctx context.Context, tx postgres.DBTX) ([]Expectation, error) {
	rows, err := dbgen.New(tx).ListRunRecencyExpectationsForSystem(ctx)
	if err != nil {
		return nil, xerror.Wrapf("list run recency expectations: %w", xerror.Unexpected().With(err))
	}
	result := make([]Expectation, 0, len(rows))
	for _, r := range rows {
		result = append(result, Expectation{ScopeDisabled: r.ScopeDisabled.Bool, ID: int64(r.ID), Scope: Scope{Org: r.OrgSlug, Project: r.ProjectSlug, Environment: r.EnvironmentSlug, Pipeline: slug.Slug(r.PipelineSlug)}, EnvironmentID: r.ProjectEnvironmentID, Revision: r.Revision, MaximumAge: time.Duration(r.MaximumAgeSeconds) * time.Second, Enabled: r.Enabled, EnabledAt: r.EnabledAt.Time, DeploymentID: r.DeploymentID, DeploymentRevision: r.DeploymentRevision, VersionID: r.CurrentVersionID, ResolutionDigest: postgres.FromNullableText(r.CurrentResolutionDigest), DeployedAt: r.DeployedAt.Time, SuccessRun: postgres.FromNullableText(r.SuccessRunSlug), SucceededAt: postgres.FromNullableTimestamptz(r.SucceededAt)})
	}
	return result, nil
}

func (*Repository) ActiveEpisodes(ctx context.Context, tx postgres.DBTX, e Expectation) ([]Episode, error) {
	rows, err := dbgen.New(tx).ListActiveRunRecencyEpisodes(ctx, dbgen.ListActiveRunRecencyEpisodesParams{ProjectEnvironmentID: e.EnvironmentID, PipelineSlug: e.Pipeline.String()})
	if err != nil {
		return nil, xerror.Wrapf("list recency episodes: %w", xerror.Unexpected().With(err))
	}
	result := make([]Episode, 0, len(rows))
	for _, r := range rows {
		result = append(result, Episode{Slug: r.Slug, ExpectationID: r.ExpectationID, DeploymentID: r.DeploymentID, DeploymentRevision: r.DeploymentRevision, Deadline: r.DeadlineAt.Time, BreachedAt: r.BreachedAt.Time, LastSuccessAt: postgres.FromNullableTimestamptz(r.LastSuccessAt), LastSuccessRun: postgres.FromNullableText(r.LastSuccessRunSlug)})
	}
	return result, nil
}

func (*Repository) CreateEpisode(ctx context.Context, tx postgres.DBTX, e Expectation, record slug.Slug, deadline, now time.Time) error {
	_, err := dbgen.New(tx).CreateRunRecencyEpisode(ctx, dbgen.CreateRunRecencyEpisodeParams{Slug: record, ExpectationID: e.ID, DeploymentID: e.DeploymentID, DeploymentRevision: e.DeploymentRevision, PipelineVersionID: e.VersionID, DeploymentResolutionDigest: postgres.ToNullableText(e.ResolutionDigest), DeployedAt: postgres.ToTimestamptz(e.DeployedAt), DeadlineAt: postgres.ToTimestamptz(deadline), BreachedAt: postgres.ToTimestamptz(now), LastSuccessRunSlug: postgres.ToNullableText(e.SuccessRun), LastSuccessAt: postgres.ToNullableTimestamptz(e.SucceededAt)})
	if err != nil {
		return xerror.Wrapf("create recency episode: %w", xerror.Unexpected().With(err))
	}
	return nil
}

func (*Repository) ResolveEpisode(ctx context.Context, tx postgres.DBTX, e Expectation, episode Episode, reason string, now time.Time) error {
	var run *string
	var succeeded *time.Time
	if reason == "qualifying_success" {
		run = e.SuccessRun
		succeeded = e.SucceededAt
	}
	err := dbgen.New(tx).ResolveRunRecencyEpisode(ctx, dbgen.ResolveRunRecencyEpisodeParams{ProjectEnvironmentID: e.EnvironmentID, PipelineSlug: e.Pipeline.String(), Slug: episode.Slug, ResolvedAt: postgres.ToTimestamptz(now), ResolutionReason: postgres.ToText(reason), RecoveryRunSlug: postgres.ToNullableText(run), RecoverySucceededAt: postgres.ToNullableTimestamptz(succeeded)})
	if err != nil {
		return xerror.Wrapf("resolve recency episode: %w", xerror.Unexpected().With(err))
	}
	return nil
}

func (*Repository) LockScope(ctx context.Context, tx postgres.DBTX, scope Scope) error {
	err := dbgen.New(tx).LockRunRecencyScope(ctx, dbgen.LockRunRecencyScopeParams{OrgSlug: scope.Org.String(), ProjectSlug: scope.Project.String(), EnvironmentSlug: scope.Environment.String(), PipelineSlug: scope.Pipeline.String()})
	if err != nil {
		return xerror.Wrapf("lock recency expectation scope: %w", xerror.Unexpected().With(err))
	}
	return nil
}

func (*Repository) FindLatest(ctx context.Context, tx postgres.DBTX, scope Scope) (*Expectation, error) {
	r, err := dbgen.New(tx).FindLatestRunRecencyExpectation(ctx, dbgen.FindLatestRunRecencyExpectationParams{OrgSlug: scope.Org, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment, PipelineSlug: scope.Pipeline.String()})
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, ErrExpectationNotFound.With(err)
	}
	if err != nil {
		return nil, xerror.Wrapf("find recency expectation: %w", xerror.Unexpected().With(err))
	}
	return &Expectation{ID: int64(r.ID), Scope: scope, EnvironmentID: r.ProjectEnvironmentID, Revision: r.Revision, MaximumAge: time.Duration(r.MaximumAgeSeconds) * time.Second, Enabled: r.Enabled, EnabledAt: r.EnabledAt.Time}, nil
}

func (*Repository) CreateExpectation(ctx context.Context, tx postgres.DBTX, e Expectation) (*Expectation, error) {
	r, err := dbgen.New(tx).CreateRunRecencyExpectation(ctx, dbgen.CreateRunRecencyExpectationParams{OrgSlug: e.Org, ProjectSlug: e.Project, EnvironmentSlug: e.Environment, PipelineSlug: e.Pipeline.String(), Revision: e.Revision, MaximumAgeSeconds: int64(e.MaximumAge / time.Second), Enabled: e.Enabled, EnabledAt: postgres.ToTimestamptz(e.EnabledAt)})
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, xerror.NotFound(xerror.Pipeline).With(err).Detail("deployed pipeline not found in recency scope")
	}
	if err != nil {
		return nil, xerror.Wrapf("create recency expectation: %w", xerror.Unexpected().With(err))
	}
	e.ID = int64(r.ID)
	e.EnvironmentID = r.ProjectEnvironmentID
	return &e, nil
}

func (*Repository) FindCurrent(ctx context.Context, tx postgres.DBTX, scope Scope) (Expectation, error) {
	r, err := dbgen.New(tx).FindCurrentRunRecencyExpectationForSystem(ctx, dbgen.FindCurrentRunRecencyExpectationForSystemParams{OrgSlug: scope.Org, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment, PipelineSlug: scope.Pipeline.String()})
	if xerror.Is(err, pgx.ErrNoRows) {
		return Expectation{}, ErrExpectationNotFound.With(err)
	}
	if err != nil {
		return Expectation{}, xerror.Wrapf("find current recency evaluation: %w", xerror.Unexpected().With(err))
	}
	return Expectation{ScopeDisabled: r.ScopeDisabled.Bool, ID: int64(r.ID), Scope: Scope{Org: r.OrgSlug, Project: r.ProjectSlug, Environment: r.EnvironmentSlug, Pipeline: slug.Slug(r.PipelineSlug)}, EnvironmentID: r.ProjectEnvironmentID, Revision: r.Revision, MaximumAge: time.Duration(r.MaximumAgeSeconds) * time.Second, Enabled: r.Enabled, EnabledAt: r.EnabledAt.Time, DeploymentID: r.DeploymentID, DeploymentRevision: r.DeploymentRevision, VersionID: r.CurrentVersionID, ResolutionDigest: postgres.FromNullableText(r.CurrentResolutionDigest), DeployedAt: r.DeployedAt.Time, SuccessRun: postgres.FromNullableText(r.SuccessRunSlug), SucceededAt: postgres.FromNullableTimestamptz(r.SucceededAt)}, nil
}
