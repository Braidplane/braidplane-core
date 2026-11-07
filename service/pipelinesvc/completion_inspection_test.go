package pipelinesvc

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/stretchr/testify/require"
)

func TestCompletionInspectionPreservesPersistedDecisions(t *testing.T) {
	t.Parallel()
	repo := &fakeRunRepository{
		inspectLogicalRunFn: func(_ context.Context, _ postgres.DBTX, org, project, environment, run slug.Slug) (*model.PipelineRunCompletion, error) {
			require.Equal(t, []slug.Slug{"acme", "etl", "dev", "retry-2"}, []slug.Slug{org, project, environment, run})
			return &model.PipelineRunCompletion{RootRunSlug: "root", WaitingForRetry: true, Children: []model.PipelineCompletionDecision{}}, nil
		},
		listCompletionDecisionsFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) ([]model.PipelineCompletionDecision, error) {
			return []model.PipelineCompletionDecision{{IsParent: true, TriggerSlug: "parent", TriggerRevision: 3, SelectedVersionHash: new("immutable-parent-version")}, {TriggerSlug: "missing-deployment", State: "refused", DecisionReason: new("deployment_missing")}}, nil
		},
	}
	evidence, err := newTestService(t, repo).GetRunCompletion(context.Background(), "acme", "etl", "dev", "retry-2")
	require.NoError(t, err)
	require.True(t, evidence.WaitingForRetry)
	require.False(t, evidence.Closed)
	require.Nil(t, evidence.Outcome)
	require.Equal(t, int64(3), evidence.Parent.TriggerRevision)
	require.Equal(t, "immutable-parent-version", *evidence.Parent.SelectedVersionHash)
	require.Len(t, evidence.Children, 1)
	require.Nil(t, evidence.Children[0].ChildRunSlug)
	require.Equal(t, "deployment_missing", *evidence.Children[0].DecisionReason)
}

func TestCompletionInspectionDoesNotInventMissingRoot(t *testing.T) {
	t.Parallel()
	repo := &fakeRunRepository{inspectLogicalRunFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.PipelineRunCompletion, error) {
		return nil, pipelineruns.ErrRunNotFound
	}}
	_, err := newTestService(t, repo).GetRunCompletion(context.Background(), "acme", "etl", "dev", "absent")
	require.ErrorIs(t, err, ErrPipelineRunNotFound)
}
