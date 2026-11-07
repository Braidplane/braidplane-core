package pipelineruns

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func (r *repository) InspectLogicalRun(ctx context.Context, db postgres.DBTX, org, project, environment, run slug.Slug) (*model.PipelineRunCompletion, error) {
	rows, err := dbgen.New(db).InspectPipelineLogicalRun(ctx, dbgen.InspectPipelineLogicalRunParams{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: run.String()})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if len(rows) == 0 {
		return nil, ErrRunNotFound
	}
	root := rows[0]
	evidence := &model.PipelineRunCompletion{PipelineSlug: slug.Slug(root.PipelineSlug), Truncated: len(rows) > 10, RootRunSlug: slug.Slug(root.RootRunSlug), Closed: root.Closed.Bool, WaitingForRetry: root.WaitingForRetry, Outcome: postgres.FromNullableText(root.Outcome), FinalAttemptRunSlug: nullableTextSlug(root.FinalAttemptRunSlug), DecisionReason: postgres.FromNullableText(root.DecisionReason), Attempts: make([]model.PipelineCompletionAttempt, 0, len(rows)), Children: []model.PipelineCompletionDecision{}}
	if len(rows) > 10 {
		rows = rows[:10]
	}
	for _, row := range rows {
		evidence.Attempts = append(evidence.Attempts, model.PipelineCompletionAttempt{RunSlug: slug.Slug(row.RunSlug), AttemptNumber: int(row.SourceAttemptNumber), Status: row.Status, VersionHash: row.PipelineVersionSpecHash.String, DeploymentRevision: postgres.FromNullableInt64(row.DeploymentRevision)})
	}
	return evidence, nil
}

func (r *repository) ListCompletionDecisions(ctx context.Context, db postgres.DBTX, org, project, environment, run slug.Slug) ([]model.PipelineCompletionDecision, error) {
	rows, err := dbgen.New(db).ListPipelineRunCompletionDecisions(ctx, dbgen.ListPipelineRunCompletionDecisionsParams{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: run.String()})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	decisions := make([]model.PipelineCompletionDecision, 0, len(rows))
	for _, row := range rows {
		var condition *model.PipelineCompletionCondition
		if row.Condition != "" {
			parsed, err := model.PipelineCompletionConditionString(row.Condition)
			if err != nil {
				return nil, xerror.Unexpected().With(err)
			}
			condition = &parsed
		}
		decisions = append(decisions, model.PipelineCompletionDecision{IsParent: row.IsParent, TriggerSlug: slug.Slug(row.TriggerSlug), TriggerRevision: row.TriggerRevision, UpstreamPipelineSlug: slug.Slug(row.UpstreamPipelineSlug), RootRunSlug: slug.Slug(row.RootRunSlug), FinalAttemptRunSlug: slug.Slug(row.FinalAttemptRunSlug), DownstreamPipelineSlug: slug.Slug(row.DownstreamPipelineSlug), Condition: condition, State: row.State, DecisionReason: postgres.FromNullableText(row.DecisionReason), ChildRunSlug: nullableTextSlug(row.ChildRunSlug), ChildStatus: postgres.FromNullableText(row.ChildStatus), ChildOutcome: postgres.FromNullableText(row.ChildOutcome), SelectedVersionHash: postgres.FromNullableText(row.SelectedVersionHash), SelectedDeploymentRevision: postgres.FromNullableInt64(row.SelectedDeploymentRevision), SelectedResolutionDigest: postgres.FromNullableText(row.SelectedResolutionDigest)})
	}
	return decisions, nil
}
