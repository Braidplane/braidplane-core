package pipelinesvc

import (
	"fmt"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestConnectedRunsFromChildGroupsRetriesAndKeepsRefusedBranches(t *testing.T) {
	child := slug.Slug("b")
	ab := model.PipelineCompletionDecision{RootRunSlug: "a", FinalAttemptRunSlug: "a-retry-2", TriggerSlug: "ab", ChildRunSlug: &child, State: "accepted"}
	d := slug.Slug("d")
	bd := model.PipelineCompletionDecision{RootRunSlug: "b", TriggerSlug: "bd", ChildRunSlug: &d, State: "accepted"}
	records := map[slug.Slug]*model.PipelineRunCompletion{
		"a": {RootRunSlug: "a", Closed: true, Outcome: new("succeeded"), Attempts: []model.PipelineCompletionAttempt{{RunSlug: "a"}, {RunSlug: "a-retry-2"}}, Children: []model.PipelineCompletionDecision{ab, {RootRunSlug: "a", TriggerSlug: "ac", State: "refused"}}},
		"b": {RootRunSlug: "b", Parent: &ab, Children: []model.PipelineCompletionDecision{bd}},
		"d": {RootRunSlug: "d", Closed: true, Outcome: new("failed"), Parent: &bd},
	}
	reads := 0
	result, err := InspectConnectedRuns(t.Context(), "b", func(s slug.Slug) (*model.PipelineRunCompletion, error) {
		reads++
		require.NotEqual(t, slug.Slug("unrelated"), s)
		return records[s], nil
	}, connectedDecisionVisible)
	require.NoError(t, err)
	require.Equal(t, 3, reads)
	require.Len(t, result.Nodes, 3)
	require.Len(t, result.Nodes[1].Attempts, 2)
	require.Equal(t, "succeeded", *result.Nodes[1].Outcome)
	require.Nil(t, result.Nodes[1].Children[1].ChildRunSlug)
	require.False(t, result.Truncated)
}

func TestConnectedRunsBoundsTraversalAndUnavailableContext(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		reads := 0
		result, err := InspectConnectedRuns(t.Context(), "run-0", func(s slug.Slug) (*model.PipelineRunCompletion, error) {
			reads++
			if unavailable && reads == 2 {
				return nil, ErrPipelineRunNotFound
			}
			child := slug.Slug(fmt.Sprintf("run-%d", reads))
			return &model.PipelineRunCompletion{RootRunSlug: s, Children: []model.PipelineCompletionDecision{{RootRunSlug: s, TriggerSlug: "next", ChildRunSlug: &child}}}, nil
		}, connectedDecisionVisible)
		require.NoError(t, err)
		if unavailable {
			require.True(t, result.ContextUnavailable)
			require.Equal(t, 2, reads)
		} else {
			require.True(t, result.Truncated)
			require.Equal(t, 40, reads)
			require.Len(t, result.Nodes, 40)
		}
	}
}

func TestConnectedRunsDeduplicatesAndBoundsDecisions(t *testing.T) {
	result, err := InspectConnectedRuns(t.Context(), "root", func(s slug.Slug) (*model.PipelineRunCompletion, error) {
		children := []model.PipelineCompletionDecision{}
		for i := 0; i < 101; i++ {
			children = append(children, model.PipelineCompletionDecision{RootRunSlug: s, TriggerSlug: slug.Slug(fmt.Sprintf("trigger-%d", i)), State: "refused"})
		}
		return &model.PipelineRunCompletion{RootRunSlug: s, Children: children}, nil
	}, connectedDecisionVisible)
	require.NoError(t, err)
	require.True(t, result.Truncated)
	require.Len(t, result.Nodes[0].Children, 100)
}

func TestConnectedRunsPropagatesReadFailure(t *testing.T) {
	failure := xerror.Unexpected().Detail("database unavailable")
	result, err := InspectConnectedRuns(t.Context(), "root", func(slug.Slug) (*model.PipelineRunCompletion, error) { return nil, failure }, connectedDecisionVisible)
	require.ErrorIs(t, err, failure)
	require.Nil(t, result)
}

func connectedDecisionVisible(model.PipelineCompletionDecision) (bool, error) { return true, nil }

func TestConnectedRunsBoundsVisibilityWorkIncludingDeniedDecisions(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprint(denied), func(t *testing.T) {
			checks, reads := 0, 0
			result, err := InspectConnectedRuns(t.Context(), "root", func(run slug.Slug) (*model.PipelineRunCompletion, error) {
				reads++
				children := []model.PipelineCompletionDecision{}
				for i := 0; i < 101; i++ {
					child := slug.Slug(fmt.Sprintf("%s-child-%d", run, i))
					d := model.PipelineCompletionDecision{RootRunSlug: run, TriggerSlug: "trigger", TriggerRevision: int64(i), ChildRunSlug: &child}
					children = append(children, d, d)
				}
				return &model.PipelineRunCompletion{RootRunSlug: run, Children: children}, nil
			}, func(model.PipelineCompletionDecision) (bool, error) {
				checks++
				return !denied, nil
			})
			require.NoError(t, err)
			require.Equal(t, 100, checks)
			require.True(t, result.Truncated)
			require.Equal(t, denied, result.ContextUnavailable)
			if denied {
				require.Equal(t, 1, reads)
				require.Empty(t, result.Nodes[0].Children)
			} else {
				require.Equal(t, 40, reads)
				require.Empty(t, result.Nodes[1].Children)
			}
		})
	}
}

func TestConnectedRunsPropagatesVisibilityFailure(t *testing.T) {
	failure := xerror.Unexpected().Detail("authorization unavailable")
	result, err := InspectConnectedRuns(t.Context(), "root", func(run slug.Slug) (*model.PipelineRunCompletion, error) {
		return &model.PipelineRunCompletion{RootRunSlug: run, Children: []model.PipelineCompletionDecision{{RootRunSlug: run}}}, nil
	}, func(model.PipelineCompletionDecision) (bool, error) { return false, failure })
	require.ErrorIs(t, err, failure)
	require.Nil(t, result)
}

func TestConnectedRunsRechecksChangedDecisionEndpoints(t *testing.T) {
	child := slug.Slug("child")
	pending := model.PipelineCompletionDecision{RootRunSlug: "root", TriggerSlug: "trigger", TriggerRevision: 1}
	accepted := pending
	accepted.ChildRunSlug = &child
	checks := 0
	result, err := InspectConnectedRuns(t.Context(), "root", func(run slug.Slug) (*model.PipelineRunCompletion, error) {
		if run == "root" {
			return &model.PipelineRunCompletion{RootRunSlug: run, Children: []model.PipelineCompletionDecision{pending, accepted}}, nil
		}
		t.Fatal("must not traverse the newly denied child")
		return nil, ErrPipelineRunNotFound
	}, func(d model.PipelineCompletionDecision) (bool, error) {
		checks++
		return d.ChildRunSlug == nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, checks)
	require.True(t, result.ContextUnavailable)
	require.Len(t, result.Nodes[0].Children, 1)
	require.Nil(t, result.Nodes[0].Children[0].ChildRunSlug)
}
