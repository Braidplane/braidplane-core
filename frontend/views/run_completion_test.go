package views

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/runlineage"
	"github.com/stretchr/testify/require"
)

func TestAutomaticRetryDoesNotDisplayRetainedInputReplay(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, source string
		replay       bool
	}{
		{"automatic retry", `{"type":"completion","attempt_number":2,"root_run_slug":"root","retry_of_run_slug":"root"}`, false},
		{"explicit replay", `{"type":"manual","attempt_number":1,"root_run_slug":"replay"}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var source api.PipelineRunSourceSummary
			require.NoError(t, json.Unmarshal([]byte(tt.source), &source))
			run := api.PipelineRunRecord{}
			run.Metadata.Source = &source
			run.Metadata.ReplayOfRunSlug = new("root")
			var out strings.Builder
			require.NoError(t, PipelineRunDetailPageContent("acme", "etl", "dev", run, nil, false, "", runlineage.View{}, OrganizationPermissions{}, RunCompletionInspection{}).Render(t.Context(), &out))
			if tt.replay {
				require.Contains(t, out.String(), "Retained-input replay")
			} else {
				require.NotContains(t, out.String(), "Retained-input replay")
				require.Contains(t, out.String(), "Root root / retry of root")
			}
		})
	}
}

func TestCompletionInspectionUsesExactEvidence(t *testing.T) {
	t.Parallel()
	evidence := api.PipelineRunCompletion{Truncated: new(true), ContextUnavailable: new(true), RootRunSlug: "upstream-root", Closed: true, Outcome: new("succeeded"), FinalAttemptRunSlug: new("upstream-retry-2"), Attempts: []api.PipelineCompletionAttempt{{RunSlug: "upstream-root", AttemptNumber: 1, Status: "failed", VersionHash: "executed-v1"}, {RunSlug: "upstream-retry-2", AttemptNumber: 2, Status: "succeeded", VersionHash: "executed-v1"}}, Children: []api.PipelineCompletionDecision{{TriggerSlug: "after-upstream", TriggerRevision: 7, UpstreamPipelineSlug: "upstream", RootRunSlug: "upstream-root", FinalAttemptRunSlug: "upstream-retry-2", DownstreamPipelineSlug: "downstream", Condition: new(api.PipelineCompletionConditionSuccess), State: "refused", DecisionReason: new("deployment_missing")}}}
	var out bytes.Buffer
	require.NoError(t, PipelineRunCompletionPanel("acme", "etl", "dev", "upstream-root", RunCompletionInspection{Evidence: &evidence}).Render(t.Context(), &out))
	html := out.String()
	require.Contains(t, html, "Read limit reached.")
	require.Contains(t, html, "Some causal context is unavailable")
	require.Contains(t, html, "deployment_missing")
	require.Contains(t, html, "No child run accepted.")
	require.Contains(t, html, "executed-v1")
	require.Contains(t, html, "upstream-retry-2?environment_slug=dev&amp;section=completion")
	require.NotContains(t, html, "Accepted child:")
	require.NotContains(t, html, "Child logical outcome:")
	require.Equal(t, "waiting_for_retry", completionRootState(api.PipelineRunCompletion{WaitingForRetry: true}))
}

func TestCompletionSourceKeepsItsAttemptIdentity(t *testing.T) {
	t.Parallel()
	var source api.PipelineRunSourceSummary
	require.NoError(t, json.Unmarshal([]byte(`{"type":"completion","summary":"Completion-triggered run","entrypoint":"scheduler","actor_type":"system","attempt_number":2,"root_run_slug":"completion-root","retry_of_run_slug":"completion-root"}`), &source))
	view := apiRunSourceView(&source)
	require.Equal(t, "completion", view.sourceType)
	require.Equal(t, 2, view.attemptNumber)
	require.Equal(t, "completion-root", view.rootRunSlug)
	require.Equal(t, "Completion-triggered run via scheduler", runSourceLabel(view))
}

func TestCompletionConfigurationSeparatesCurrentPreview(t *testing.T) {
	t.Parallel()
	trigger := api.PipelineTrigger{Slug: "after-upstream", Revision: 9, PipelineSlug: "downstream", Completion: &api.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "upstream", Condition: api.PipelineCompletionConditionSuccess}, VersionPolicy: api.PipelineTriggerResolvedVersionPolicy{Mode: api.PipelineTriggerVersionPolicyModeLatestDeployed}}
	var out strings.Builder
	require.NoError(t, PipelineCompletionConfiguration("acme", "etl", "dev", trigger, PipelineScheduleSelectedDetail{CompletionPreview: &api.PipelineDeployment{Revision: 11, CurrentVersionSpecHash: "current-v3"}}).Render(t.Context(), &out))
	require.Contains(t, out.String(), "Disabled subscription")
	require.Contains(t, out.String(), "Current configuration preview")
	require.Contains(t, out.String(), "current-v3")
	require.Contains(t, out.String(), "Trigger revision 9")
	require.Contains(t, out.String(), "Exact selected deployment/version belongs to the persisted run decision")
}

func TestCompletionRefreshStopsAtLogicalOutcomes(t *testing.T) {
	t.Parallel()
	require.False(t, completionPending(nil))
	require.True(t, completionPending(&api.PipelineRunCompletion{WaitingForRetry: true}))
	for _, status := range []string{"queued", "running", "succeeded", "failed", "canceled"} {
		require.Equal(t, status == "queued" || status == "running", completionPending(&api.PipelineRunCompletion{
			Attempts: []api.PipelineCompletionAttempt{{Status: status}},
		}), status)
	}
	require.False(t, completionPending(&api.PipelineRunCompletion{}))
	evidence := &api.PipelineRunCompletion{Closed: true, Children: []api.PipelineCompletionDecision{{State: "refused"}}}
	require.False(t, completionPending(evidence))
	evidence.Children[0] = api.PipelineCompletionDecision{State: "accepted", ChildRunSlug: new("child"), ChildStatus: new("failed")}
	require.True(t, completionPending(evidence))
	evidence.Children[0].ChildOutcome = new("succeeded")
	require.False(t, completionPending(evidence))
}

func TestCompletionStatePermutationsDoNotInventChildren(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"waiting", "final_failure", "changed", "refused", "restricted", "none"} {
		t.Run(scenario, func(t *testing.T) {
			evidence := api.PipelineRunCompletion{RootRunSlug: "root"}
			inspection := RunCompletionInspection{Evidence: &evidence, CurrentSubscriptionsLoaded: true}
			want := "No completion trigger is currently configured"
			switch scenario {
			case "waiting":
				evidence.WaitingForRetry = true
				want = "waiting_for_retry"
			case "final_failure":
				evidence.Closed = true
				evidence.Outcome = new("failed")
				inspection.CurrentSubscriptions = []api.PipelineTrigger{{Slug: "after-upstream", Enabled: true, PipelineSlug: "normalize-orders", Completion: &api.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "daily-sync", Condition: api.PipelineCompletionConditionSuccess, ExecutionGrantEnabled: true}}}
				evidence.Children = []api.PipelineCompletionDecision{{State: "refused", UpstreamPipelineSlug: "daily-sync", DownstreamPipelineSlug: "normalize-orders", Condition: new(api.PipelineCompletionConditionFinalFailure)}}
				want = "Current condition does not match recorded closure"
			case "changed", "refused":
				reason := map[string]string{"changed": "trigger_changed", "refused": "deployment_missing"}[scenario]
				evidence.Children = []api.PipelineCompletionDecision{{State: "refused", DecisionReason: &reason}}
				want = reason
			case "restricted":
				inspection.CurrentSubscriptionsLoaded = false
				want = "No visible persisted dispatch decisions."
			}
			var out strings.Builder
			require.NoError(t, PipelineRunCompletionPanel("acme", "ops", "dev", "root", inspection).Render(t.Context(), &out))
			html := out.String()
			require.Contains(t, html, want)
			require.NotContains(t, html, "Accepted child:")
			require.NotContains(t, html, "completion-child")
			if scenario == "changed" || scenario == "refused" {
				require.Contains(t, html, "No child run accepted.")
			}
			if scenario == "final_failure" {
				require.Contains(t, html, "Closed · failed")
				require.Contains(t, html, "daily-sync → final_failure → normalize-orders")
			}
			if scenario == "waiting" {
				require.NotContains(t, html, "parent-final")
			}
			if scenario == "restricted" {
				require.NotContains(t, html, "after-upstream")
				require.NotContains(t, html, "normalize-orders")
			}
		})
	}
}
