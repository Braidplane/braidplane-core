package pipelinetriggers

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type CompletionEdge struct {
	TriggerSlug  slug.Slug
	PipelineSlug slug.Slug
	Enabled      bool
	Config       model.PipelineTriggerCompletionConfig
}

type CompletionDispatchRef struct {
	ID              int
	OrgSlug         slug.Slug
	ProjectSlug     slug.Slug
	EnvironmentSlug slug.Slug
	ExecutionArgs   []byte
	FailedRunSlug   slug.Slug
}

func (r *repository) FenceCompletionRetryDecision(ctx context.Context, db postgres.DBTX, org slug.Slug, id int) (time.Time, error) {
	at, err := dbgen.New(db).FenceCompletionRetryDecision(ctx, dbgen.FenceCompletionRetryDecisionParams{OrgSlug: org, SnapshotID: id})
	if xerror.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrPipelineTriggerNotFound.With(err)
	}
	if err != nil {
		return time.Time{}, xerror.Unexpected().With(err)
	}
	return at.Time, nil
}

func (r *repository) ListCompletionRunFinalizationsForSystem(ctx context.Context, db postgres.DBTX) ([]CompletionDispatchRef, error) {
	rows, err := dbgen.New(db).ListCompletionRunFinalizationsForSystem(ctx)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	refs := make([]CompletionDispatchRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, CompletionDispatchRef{ID: row.ID, OrgSlug: row.OrgSlug, ProjectSlug: row.ProjectSlug, EnvironmentSlug: row.EnvironmentSlug, FailedRunSlug: slug.Slug(row.FailedRunSlug)})
	}
	return refs, nil
}

func (r *repository) SaveCompletionRetryExecution(ctx context.Context, db postgres.DBTX, org slug.Slug, id int, run slug.Slug, args []byte) error {
	rows, err := dbgen.New(db).SaveCompletionRetryExecution(ctx, dbgen.SaveCompletionRetryExecutionParams{OrgSlug: org, SnapshotID: id, RunSlug: run.String(), ExecutionArgs: args})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows != 1 {
		return ErrPipelineTriggerNotFound
	}
	return nil
}

func (r *repository) DeferCompletionRunFinalization(ctx context.Context, db postgres.DBTX, org slug.Slug, id int, run slug.Slug, reason string) error {
	_, err := dbgen.New(db).DeferCompletionRunFinalization(ctx, dbgen.DeferCompletionRunFinalizationParams{OrgSlug: org, SnapshotID: id, RunSlug: run.String(), Reason: optionalString(&reason)})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *repository) ListPendingCompletionsForSystem(ctx context.Context, db postgres.DBTX) ([]CompletionDispatchRef, error) {
	rows, err := dbgen.New(db).ListPendingPipelineCompletionsForSystem(ctx)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	refs := make([]CompletionDispatchRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, CompletionDispatchRef{ID: row.ID, OrgSlug: row.OrgSlug, ProjectSlug: row.ProjectSlug, EnvironmentSlug: row.EnvironmentSlug})
	}
	return refs, nil
}

func (r *repository) ListCompletionRepairsForSystem(ctx context.Context, db postgres.DBTX) ([]CompletionDispatchRef, error) {
	rows, err := dbgen.New(db).ListPipelineCompletionRepairsForSystem(ctx)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	refs := make([]CompletionDispatchRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, CompletionDispatchRef{ID: row.ID, OrgSlug: row.OrgSlug, ProjectSlug: row.ProjectSlug, EnvironmentSlug: row.EnvironmentSlug, ExecutionArgs: row.ExecutionArgs})
	}
	return refs, nil
}

func (r *repository) AcceptCompletionSnapshot(ctx context.Context, db postgres.DBTX, org slug.Slug, id int, child slug.Slug, args []byte) error {
	rows, err := dbgen.New(db).AcceptPipelineCompletionSnapshot(ctx, dbgen.AcceptPipelineCompletionSnapshotParams{
		OrgSlug: org, SnapshotID: id, ChildRunSlug: optionalSlugText(&child), ExecutionArgs: args,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows != 1 {
		return ErrPipelineTriggerNotFound
	}
	return nil
}

func (r *repository) DeferCompletionSnapshot(ctx context.Context, db postgres.DBTX, org slug.Slug, id int, reason string) error {
	_, err := dbgen.New(db).DeferPipelineCompletionSnapshot(ctx, dbgen.DeferPipelineCompletionSnapshotParams{
		OrgSlug: org, SnapshotID: id, Reason: optionalString(&reason),
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *repository) CompleteCompletionSnapshotsForSystem(ctx context.Context, db postgres.DBTX) error {
	if err := dbgen.New(db).CompletePipelineCompletionSnapshotsForSystem(ctx); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

// LockPipelineTriggerTopology serializes edits for the whole project environment.
func (r *repository) LockPipelineTriggerTopology(ctx context.Context, tx postgres.DBTX, org, project, environment slug.Slug) (bool, error) {
	protected, err := dbgen.New(tx).LockPipelineTriggerTopology(ctx, dbgen.LockPipelineTriggerTopologyParams{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment})
	if xerror.Is(err, sql.ErrNoRows) {
		return false, ErrPipelineTriggerDependencyMissing.With(err)
	}
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return protected, nil
}

func (r *repository) ListPipelineCompletionEdges(ctx context.Context, tx postgres.DBTX, org, project, environment slug.Slug) ([]CompletionEdge, error) {
	rows, err := dbgen.New(tx).ListPipelineCompletionEdges(ctx, dbgen.ListPipelineCompletionEdgesParams{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	edges := make([]CompletionEdge, 0, len(rows))
	for _, row := range rows {
		edge := CompletionEdge{TriggerSlug: row.Slug, PipelineSlug: slug.Slug(row.PipelineSlug), Enabled: row.Enabled}
		if err := json.Unmarshal(row.CompletionConfig, &edge.Config); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		edges = append(edges, edge)
	}
	return edges, nil
}

func (r *repository) LoadCompletionSnapshot(ctx context.Context, db postgres.DBTX, org, project, environment slug.Slug, id int) (*model.PipelineCompletionSnapshot, error) {
	row, err := dbgen.New(db).LoadPipelineCompletionSnapshot(ctx, dbgen.LoadPipelineCompletionSnapshotParams{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, SnapshotID: id})
	if xerror.Is(err, sql.ErrNoRows) {
		return nil, ErrPipelineTriggerNotFound.With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	snapshot := &model.PipelineCompletionSnapshot{
		VersionID: row.SelectedVersionID.Int64, DeploymentID: row.SelectedDeploymentID.Int64,
		ChildRunSlug: slug.Slug(row.ChildRunSlug.String), AncestorPipelines: row.AncestorPipelineSlugs,
		ID: row.ID, TriggerSlug: slug.Slug(row.TriggerSlug), TriggerRevision: row.TriggerRevision,
		PipelineSlug: slug.Slug(row.DownstreamPipelineSlug), RootRunSlug: slug.Slug(row.RootRunSlug), FinalAttemptRunSlug: slug.Slug(row.FinalAttemptRunSlug),
		VersionHash: row.SelectedVersionHash.String, DeploymentRevision: row.SelectedDeploymentRevision.Int64,
		ResolutionDigest: row.SelectedResolutionDigest.String, Materialization: row.SelectedMaterialization,
		CausalDepth: int(row.CausalDepth), State: row.State, DecisionReason: row.DecisionReason.String,
	}
	if err := json.Unmarshal(row.RetryPolicy, &snapshot.RetryPolicy); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if snapshot.DecisionReason == "" || snapshot.DecisionReason == "capacity_waiting" || snapshot.DecisionReason == "dispatch_retry_pending" {
		snapshot.DecisionReason = row.ResourceRefusal
	}
	if len(row.ClosureAuthority) > 0 {
		if err := json.Unmarshal(row.ClosureAuthority, &snapshot.ClosureAuthority); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
	}
	if err := json.Unmarshal(row.CurrentAuthority, &snapshot.CurrentAuthority); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return snapshot, nil
}

func (r *repository) RefuseCompletionSnapshot(ctx context.Context, db postgres.DBTX, org slug.Slug, id int, reason string) error {
	_, err := dbgen.New(db).RefusePipelineCompletionSnapshot(ctx, dbgen.RefusePipelineCompletionSnapshotParams{OrgSlug: org, SnapshotID: id, Reason: optionalString(&reason)})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}
