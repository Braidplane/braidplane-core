package views

import (
	"bytes"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/runlineage"
	"github.com/stretchr/testify/require"
)

func TestRunRecoveryShowsDeliveryEvidenceAndPermission(t *testing.T) {
	t.Parallel()
	kind := api.PipelineStepKindSink
	unknown := api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeUnknown
	strategy := api.PipelineRunStepInputSnapshotSinkReplayStrategyUnsafe
	run := api.PipelineRunRecord{Metadata: api.PipelineRunMetadata{Status: "failed", RunSlug: "run", PipelineSlug: "export"}, Timeline: api.PipelineRunTimeline{
		Failure: &api.PipelineRunFailureContext{Step: "write", Side: "sink"}, Snapshots: []api.PipelineRunStepSnapshot{{Step: "write", Kind: &kind, Input: api.PipelineRunStepInputSnapshot{SinkDeliveryOutcome: &unknown, SinkReplayStrategy: &strategy}}},
	}}
	for _, canExecute := range []bool{false, true} {
		permissions := DefaultOrganizationPermissions()
		if canExecute {
			permissions = NewOrganizationPermissions("pipelines:execute")
		}
		var output bytes.Buffer
		require.NoError(t, PipelineRunDetailPageContent("acme", "ops", "dev", run, nil, false, "", runlineage.View{}, permissions, RunCompletionInspection{}).Render(WithEntityPageURL(t.Context(), "/?section=attempts"), &output))
		html := output.String()
		require.Contains(t, html, "Unknown delivery")
		require.Contains(t, html, "Reconcile the provider outcome")
		require.Equal(t, canExecute, bytes.Contains(output.Bytes(), []byte("Recover sink delivery</button>")))
	}
}

func TestRunSinkOutcomeDoesNotInferCompletionFromRunStatus(t *testing.T) {
	t.Parallel()
	require.Equal(t, "Outcome unavailable", runSinkDeliveryLabel(api.PipelineRunStepInputSnapshot{}))
	noOp := api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeNoOp
	require.Equal(t, "No-op: empty input", runSinkDeliveryLabel(api.PipelineRunStepInputSnapshot{SinkDeliveryOutcome: &noOp}))
}

func TestWorkerLossRecoveryActionUsesExplicitReasonAndExecutePermission(t *testing.T) {
	for _, reason := range []api.PipelineRunFailureReasonCode{api.WorkerOwnershipLost, api.TransformRuntimeFailed} {
		for _, canExecute := range []bool{true, false} {
			run := api.PipelineRunRecord{Metadata: api.PipelineRunMetadata{Status: "failed", RunSlug: "run", PipelineSlug: "export"}, Timeline: api.PipelineRunTimeline{Failure: &api.PipelineRunFailureContext{Side: "transform", ReasonCode: &reason}}}
			permissions := DefaultOrganizationPermissions()
			if canExecute {
				permissions = NewOrganizationPermissions("pipelines:execute")
			}
			var output bytes.Buffer
			require.NoError(t, PipelineRunDetailPageContent("acme", "ops", "dev", run, nil, false, "", runlineage.View{}, permissions, RunCompletionInspection{}).Render(WithEntityPageURL(t.Context(), "/?section=attempts"), &output))
			require.Equal(t, canExecute && reason == api.WorkerOwnershipLost, strings.Contains(output.String(), "Recover sink delivery</button>"))
		}
	}
}

func TestLaterSuccessfulAttemptDoesNotReplaceSelectedFailure(t *testing.T) {
	run := api.PipelineRunRecord{Metadata: api.PipelineRunMetadata{Status: "failed", RunSlug: "original", PipelineSlug: "export", PipelineVersionSpecHash: "executed-v1"}, Timeline: api.PipelineRunTimeline{Failure: &api.PipelineRunFailureContext{Step: "write", Error: "original failure", Side: "sink"}}}
	later := api.Slug("later-success")
	attempts := []api.PipelineRunTriggerRetryAttempt{{AttemptNumber: 3, Status: "succeeded", RootRunSlug: "original", RetryOfRunSlug: "original", RunSlug: &later}}
	var out bytes.Buffer
	require.NoError(t, PipelineRunDetailPageContent("acme", "ops", "prod", run, attempts, false, "", runlineage.View{}, DefaultOrganizationPermissions(), RunCompletionInspection{}).Render(WithEntityPageURL(t.Context(), "/?section=attempts"), &out))
	for _, expected := range []string{"Failed at step write", "original failure", "executed-v1", "succeeded", "later-success", "Recorded status", "Later successful attempts do not change"} {
		require.Contains(t, out.String(), expected)
	}
}
