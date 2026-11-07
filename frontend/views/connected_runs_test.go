package views

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestConnectedGraphGroupsAttemptsAndUsesHistoricalEdges(t *testing.T) {
	ab := api.PipelineCompletionDecision{RootRunSlug: "a", FinalAttemptRunSlug: "a-retry-2", TriggerSlug: "ab", ChildRunSlug: new("b"), Condition: new(api.PipelineCompletionConditionSuccess), State: "accepted", SelectedVersionHash: new("historic-v1")}
	bd := api.PipelineCompletionDecision{RootRunSlug: "b", TriggerSlug: "bd", ChildRunSlug: new("d"), State: "accepted"}
	data := &api.PipelineRunConnected{SelectedRunSlug: "b", Nodes: []api.PipelineConnectedRun{
		{PipelineSlug: "b-pipeline", Completion: api.PipelineRunCompletion{RootRunSlug: "b", Parent: &ab, Children: []api.PipelineCompletionDecision{bd}, Attempts: []api.PipelineCompletionAttempt{{RunSlug: "b", Status: "running"}}}},
		{PipelineSlug: "a-pipeline", Completion: api.PipelineRunCompletion{RootRunSlug: "a", Closed: true, Outcome: new("succeeded"), Children: []api.PipelineCompletionDecision{ab, {RootRunSlug: "a", TriggerSlug: "ac", State: "refused"}}, Attempts: []api.PipelineCompletionAttempt{{RunSlug: "a"}, {RunSlug: "a-retry-2"}}}},
		{PipelineSlug: "d-pipeline", Completion: api.PipelineRunCompletion{RootRunSlug: "d", Parent: &bd, Closed: true, Outcome: new("failed")}},
	}}
	graph := connectedGraph(data, "b", "/runs/b?section=connected")
	require.Len(t, graph.Nodes, 3)
	require.Len(t, graph.Edges, 2)
	require.Len(t, graph.Decisions, 3)
	require.Less(t, graph.Nodes[0].X, graph.Nodes[1].X)
	require.Less(t, graph.Nodes[1].X, graph.Nodes[2].X)
	require.Equal(t, "a", graph.Anchor)
	require.Contains(t, graph.Nodes[1].Href, "/runs/b?")
	require.Equal(t, "b", graph.Selected.Completion.RootRunSlug)
	require.True(t, connectedPending(data))
	var out strings.Builder
	require.NoError(t, PipelineConnectedRuns("acme", "etl", "dev", "b", RunCompletionInspection{Connected: data}).Render(t.Context(), &out))
	require.Contains(t, out.String(), "historic-v1")
	require.Contains(t, out.String(), "No child run accepted.")
	require.Contains(t, out.String(), "Connected logical runs and outcomes")
	require.Contains(t, out.String(), "connected_run=b")
	require.Contains(t, out.String(), "section=steps")
	require.Contains(t, out.String(), "Closed · succeeded")
	require.Equal(t, "a", connectedSelectedRoot(data, "", "a-retry-2"))
}

func TestConnectedRunStatesDoNotInferGlobalOutcome(t *testing.T) {
	require.Equal(t, "Retry waiting", connectedRunState(api.PipelineRunCompletion{WaitingForRetry: true}))
	require.Equal(t, "Executing", connectedRunState(api.PipelineRunCompletion{WaitingForRetry: true, Attempts: []api.PipelineCompletionAttempt{{Status: "running"}}}))
	require.Equal(t, "Queued", connectedRunState(api.PipelineRunCompletion{Attempts: []api.PipelineCompletionAttempt{{Status: "queued"}}}))
	require.Contains(t, connectedRunState(api.PipelineRunCompletion{Attempts: []api.PipelineCompletionAttempt{{Status: "failed"}}}), "closure unavailable")
}

func TestConnectedGraphFitsBoundedLargeChain(t *testing.T) {
	data := &api.PipelineRunConnected{Truncated: true}
	for i := 0; i < 40; i++ {
		root := fmt.Sprintf("root-%d", i)
		evidence := api.PipelineRunCompletion{RootRunSlug: root}
		if i < 39 {
			evidence.Children = []api.PipelineCompletionDecision{{RootRunSlug: root, ChildRunSlug: new(fmt.Sprintf("root-%d", i+1)), TriggerSlug: "next"}}
		}
		data.Nodes = append(data.Nodes, api.PipelineConnectedRun{PipelineSlug: "pipeline", Completion: evidence})
	}
	graph := connectedGraph(data, "root-39", "/run?section=connected")
	require.Len(t, graph.Nodes, 40)
	require.Len(t, graph.Edges, 39)
	for _, node := range graph.Nodes {
		require.Greater(t, graph.Width, node.X+260)
		require.Greater(t, graph.Height, node.Y+110)
	}
	require.Equal(t, "root-39", graph.Selected.Completion.RootRunSlug)
}

func TestConnectedRunNavigationUsesRetainedAttempt(t *testing.T) {
	evidence := api.PipelineRunCompletion{RootRunSlug: "expired-root", Closed: true, Outcome: new("succeeded"), FinalAttemptRunSlug: new("retry-2"), Attempts: []api.PipelineCompletionAttempt{{RunSlug: "retry-2", AttemptNumber: 2, Status: "succeeded", VersionHash: "immutable-v1"}}}
	data := &api.PipelineRunConnected{SelectedRunSlug: "retry-2", Nodes: []api.PipelineConnectedRun{{PipelineSlug: "pipeline", Completion: evidence}}}
	graph := connectedGraph(data, "expired-root", "/orgs/acme/projects/etl/runs/retry-2?environment_slug=dev&section=connected")
	selected, err := url.Parse(graph.Nodes[0].Href)
	require.NoError(t, err)
	require.Equal(t, "/orgs/acme/projects/etl/runs/retry-2", selected.Path)
	require.Equal(t, "expired-root", selected.Query().Get("connected_run"))
	var out strings.Builder
	require.NoError(t, PipelineConnectedRuns("acme", "etl", "dev", "retry-2", RunCompletionInspection{Connected: data}).Render(t.Context(), &out))
	require.Contains(t, out.String(), "Root attempt evidence is unavailable")
	require.NotContains(t, out.String(), "/runs/expired-root?")
	require.Contains(t, out.String(), "/runs/retry-2?")
}

func TestConnectedRunFinalAttemptCanOutliveItsDetail(t *testing.T) {
	evidence := api.PipelineRunCompletion{RootRunSlug: "root", Closed: true, Outcome: new("succeeded"), FinalAttemptRunSlug: new("expired-final"), Attempts: []api.PipelineCompletionAttempt{{RunSlug: "root", Status: "failed"}}}
	data := &api.PipelineRunConnected{SelectedRunSlug: "root", Nodes: []api.PipelineConnectedRun{{PipelineSlug: "pipeline", Completion: evidence}}}
	var out strings.Builder
	require.NoError(t, PipelineConnectedRuns("acme", "etl", "dev", "root", RunCompletionInspection{Connected: data}).Render(t.Context(), &out))
	require.Contains(t, out.String(), "expired-final")
	require.Contains(t, out.String(), "Evidence unavailable")
	require.NotContains(t, out.String(), "/runs/expired-final?")
	require.Equal(t, "root", connectedRunTarget(evidence))
}
