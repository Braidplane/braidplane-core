package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

func TestCompletionInspectionEndpoint(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"readable", "restricted_run", "restricted_trigger"} {
		restricted := name != "readable"
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			child := slug.Slug("private-child")
			decision := model.PipelineCompletionDecision{TriggerSlug: "private-trigger", TriggerRevision: 9, UpstreamPipelineSlug: "private-upstream", RootRunSlug: "private-root", FinalAttemptRunSlug: "private-final", DownstreamPipelineSlug: "private-downstream", Condition: new(model.PipelineCompletionConditionSuccess), State: "accepted", ChildRunSlug: &child, SelectedVersionHash: new("selected-v2"), SelectedDeploymentRevision: new(int64(4))}
			checks := 0
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					checks++
					require.Contains(t, []model.Resource{model.ResourcePipelines, model.ResourcePipelineTriggers}, input.Resource)
					return name != "restricted_trigger" || input.Resource != model.ResourcePipelineTriggers, nil
				}},
				pipelinesService: &fakePipelinesService{
					GetRunFn: func(_ context.Context, org, project, environment slug.Slug, run string) (*pipelinesvc.PipelineRunDetail, error) {
						require.Equal(t, []slug.Slug{"acme", "etl", "dev"}, []slug.Slug{org, project, environment})
						if name == "restricted_run" && run != "selected" {
							return nil, xerror.Forbidden(xerror.PipelineRun)
						}
						return &pipelinesvc.PipelineRunDetail{}, nil
					},
					GetRunCompletionFn: func(_ context.Context, org, project, environment, run slug.Slug) (*model.PipelineRunCompletion, error) {
						require.Equal(t, slug.Slug("selected"), run)
						return &model.PipelineRunCompletion{RootRunSlug: "selected", Closed: true, Outcome: new("succeeded"), Parent: &decision, Children: []model.PipelineCompletionDecision{decision}, Attempts: []model.PipelineCompletionAttempt{{RunSlug: "selected", AttemptNumber: 1, Status: "succeeded", VersionHash: "original-v1"}}}, nil
					},
				},
			})
			req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/etl/environments/dev/runs/selected/completion", nil)
			response := authenticatedRequest(t, app, store, "test-user", req)
			defer func() { _ = response.Body.Close() }()
			require.Equal(t, http.StatusOK, response.StatusCode)
			bytes, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			var body api.PipelineRunCompletion
			require.NoError(t, json.Unmarshal(bytes, &body))
			require.True(t, body.Closed)
			require.Equal(t, "original-v1", body.Attempts[0].VersionHash)
			if restricted {
				require.Nil(t, body.Parent)
				require.Empty(t, body.Children)
				require.NotContains(t, string(bytes), "private-")
			} else {
				require.Equal(t, "selected-v2", *body.Parent.SelectedVersionHash)
				require.Equal(t, int64(4), *body.Children[0].SelectedDeploymentRevision)
			}
			require.Greater(t, checks, 1)
		})
	}
}

func TestConnectedInspectionDoesNotTraverseHiddenBranches(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(fmt.Sprint(restricted), func(t *testing.T) {
			calls := []slug.Slug{}
			child := slug.Slug("private-child")
			decision := model.PipelineCompletionDecision{RootRunSlug: "root", FinalAttemptRunSlug: "root-retry-2", TriggerSlug: "after-root", DownstreamPipelineSlug: "child-pipeline", ChildRunSlug: &child, State: "accepted"}
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }},
				pipelinesService: &fakePipelinesService{
					GetRunFn: func(_ context.Context, org, project, environment slug.Slug, run string) (*pipelinesvc.PipelineRunDetail, error) {
						require.Equal(t, []slug.Slug{"acme", "etl", "dev"}, []slug.Slug{org, project, environment})
						if restricted && run == "private-child" {
							return nil, xerror.Forbidden(xerror.PipelineRun)
						}
						return &pipelinesvc.PipelineRunDetail{}, nil
					},
					GetRunCompletionFn: func(_ context.Context, org, project, environment, run slug.Slug) (*model.PipelineRunCompletion, error) {
						calls = append(calls, run)
						if run == "private-child" {
							return &model.PipelineRunCompletion{PipelineSlug: "child-pipeline", RootRunSlug: "private-child", Parent: &decision, Children: []model.PipelineCompletionDecision{}, Closed: true, Outcome: new("failed")}, nil
						}
						return &model.PipelineRunCompletion{PipelineSlug: "root-pipeline", RootRunSlug: "root", Children: []model.PipelineCompletionDecision{decision}, Closed: true, Outcome: new("succeeded"), FinalAttemptRunSlug: new(slug.Slug("root-retry-2"))}, nil
					},
				},
			})
			response := authenticatedRequest(t, app, store, "test-user", jsonRequest(t, http.MethodGet, "/organizations/acme/projects/etl/environments/dev/runs/root/connected", nil))
			defer func() { require.NoError(t, response.Body.Close()) }()
			require.Equal(t, http.StatusOK, response.StatusCode)
			raw, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			var result api.PipelineRunConnected
			require.NoError(t, json.Unmarshal(raw, &result))
			if restricted {
				require.Len(t, result.Nodes, 1)
				require.True(t, result.ContextUnavailable)
				require.NotContains(t, string(raw), "private-child")
				require.Equal(t, []slug.Slug{"root"}, calls)
			} else {
				require.Len(t, result.Nodes, 2)
				require.False(t, result.ContextUnavailable)
				require.Equal(t, "succeeded", *result.Nodes[0].Completion.Outcome)
				require.Equal(t, "failed", *result.Nodes[1].Completion.Outcome)
				require.Equal(t, "root-retry-2", result.Nodes[0].Completion.Children[0].FinalAttemptRunSlug)
			}
		})
	}
}

func TestConnectedInspectionKeepsRevisionAuthorizationIndependent(t *testing.T) {
	publicChild, unlinkedChild := slug.Slug("public-child"), slug.Slug("visible-but-unlinked")
	visible := model.PipelineCompletionDecision{RootRunSlug: "root", FinalAttemptRunSlug: "public-final", TriggerSlug: "reused-trigger", TriggerRevision: 1, ChildRunSlug: &publicChild, State: "accepted"}
	denied := visible
	denied.TriggerRevision = 2
	denied.FinalAttemptRunSlug = "private-final"
	denied.ChildRunSlug = &unlinkedChild
	reads := []slug.Slug{}
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }},
		pipelinesService: &fakePipelinesService{
			GetRunFn: func(_ context.Context, org, project, environment slug.Slug, run string) (*pipelinesvc.PipelineRunDetail, error) {
				if run == "private-final" {
					return nil, xerror.Forbidden(xerror.PipelineRun)
				}
				return &pipelinesvc.PipelineRunDetail{}, nil
			},
			GetRunCompletionFn: func(_ context.Context, org, project, environment, run slug.Slug) (*model.PipelineRunCompletion, error) {
				reads = append(reads, run)
				evidence := &model.PipelineRunCompletion{PipelineSlug: "pipeline", RootRunSlug: run, Children: []model.PipelineCompletionDecision{}}
				if run == "root" {
					evidence.Children = []model.PipelineCompletionDecision{visible, denied}
				}
				return evidence, nil
			},
		},
	})
	response := authenticatedRequest(t, app, store, "test-user", jsonRequest(t, http.MethodGet, "/organizations/acme/projects/etl/environments/dev/runs/root/connected", nil))
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "visible-but-unlinked")
	require.NotContains(t, string(raw), "private-final")
	require.Equal(t, []slug.Slug{"root", "public-child"}, reads)
	var result api.PipelineRunConnected
	require.NoError(t, json.Unmarshal(raw, &result))
	require.True(t, result.ContextUnavailable)
	require.Len(t, result.Nodes, 2)
	require.Len(t, result.Nodes[0].Completion.Children, 1)
	require.Equal(t, int64(1), result.Nodes[0].Completion.Children[0].TriggerRevision)
}

func TestConnectedInspectionCapsReusedTriggerRevisions(t *testing.T) {
	runReads := 0
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }},
		pipelinesService: &fakePipelinesService{
			GetRunFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*pipelinesvc.PipelineRunDetail, error) {
				runReads++
				return &pipelinesvc.PipelineRunDetail{}, nil
			},
			GetRunCompletionFn: func(_ context.Context, org, project, environment, run slug.Slug) (*model.PipelineRunCompletion, error) {
				evidence := &model.PipelineRunCompletion{PipelineSlug: "pipeline", RootRunSlug: run, Children: []model.PipelineCompletionDecision{}}
				for revision := int64(1); revision <= 101; revision++ {
					child := slug.Slug(fmt.Sprintf("child-%d", revision))
					evidence.Children = append(evidence.Children, model.PipelineCompletionDecision{RootRunSlug: run, FinalAttemptRunSlug: run, TriggerSlug: "reused-trigger", TriggerRevision: revision, ChildRunSlug: &child, State: "accepted"})
				}
				return evidence, nil
			},
		},
	})
	response := authenticatedRequest(t, app, store, "test-user", jsonRequest(t, http.MethodGet, "/organizations/acme/projects/etl/environments/dev/runs/root/connected", nil))
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var result api.PipelineRunConnected
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
	require.Equal(t, 341, runReads) // Selected run, 40 node reads, and three endpoint checks for 100 decisions.
	require.True(t, result.Truncated)
	require.Len(t, result.Nodes, 40)
	require.Len(t, result.Nodes[0].Completion.Children, 100)
	require.Empty(t, result.Nodes[1].Completion.Children)
	for _, decision := range result.Nodes[0].Completion.Children {
		require.LessOrEqual(t, decision.TriggerRevision, int64(100))
	}
}
