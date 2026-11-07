package pipelineruntime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/stretchr/testify/require"
)

func TestRunCandidatesObserveContractFailureBeforeRunCompletes(t *testing.T) {
	at := time.Now().UTC()
	result := &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{RunSlug: "run-1", Status: "running"},
		Timeline: pipelinerun.RunTimeline{StructuralChecks: []pipelinerun.StructuralCheck{{
			ID: "check-1", Step: "supplier", Origin: "step", Declaration: "step/producer_output/products/v1/product", ProjectionDigest: "exact-projection", Status: pipelinerun.CheckStatusFailed, Phase: "producer_output", ReasonCode: "contract_violation", CompletedAt: &at,
			ContractFailure: &pipelinerun.ContractFailureDiagnostic{FieldName: "product_code", Rule: "required"},
		}}},
	}
	candidates, err := runNotificationCandidates("acme", "ops", "dev", "report", result)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, model.NotificationTypePipelineContractFailure, candidates[0].NotificationType)
	require.Equal(t, "run-1", candidates[0].SourceRefs["run_slug"])
	require.Equal(t, "supplier", candidates[0].SourceRefs["step_slug"])
	require.Equal(t, "product_code", candidates[0].SafeMetadata["field"])
	require.Equal(t, "products", candidates[0].SafeMetadata["contract_name"])
	require.Equal(t, "v1", candidates[0].SafeMetadata["contract_revision"])
	require.Equal(t, at, candidates[0].ObservedAt)
	result.Timeline.Failure = &pipelinerun.FailureContext{Error: "private row value must never enter a candidate"}
	candidates, err = runNotificationCandidates("acme", "ops", "dev", "report", result)
	require.NoError(t, err)
	encoded, err := json.Marshal(candidates)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private row")
	for _, status := range []pipelinerun.CheckStatus{pipelinerun.CheckStatusPassed, pipelinerun.CheckStatusSkipped, pipelinerun.CheckStatusUnavailable, pipelinerun.CheckStatusError} {
		result.Timeline.StructuralChecks[0].Status = status
		result.Timeline.Failure = nil
		candidates, err = runNotificationCandidates("acme", "ops", "dev", "report", result)
		require.NoError(t, err)
		require.Empty(t, candidates)
	}
}

func TestRunSuccessCandidatePreservesCompletionTime(t *testing.T) {
	completed := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	result := &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{RunSlug: "run-success", Status: "succeeded"},
		Timeline: pipelinerun.RunTimeline{Events: []pipelinerun.TimelineEvent{{At: completed.Add(-time.Minute)}, {At: completed}}},
	}
	candidates, err := runNotificationCandidates("acme", "ops", "dev", "report", result)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, completed, candidates[0].ObservedAt)
	require.Equal(t, model.NotificationLifecycleStateResolved, candidates[0].LifecycleState)
	result.Timeline.Events = nil
	candidates, err = runNotificationCandidates("acme", "ops", "dev", "report", result)
	require.NoError(t, err)
	require.Empty(t, candidates, "success without a recorded end time is not a completion observation")
}

func TestRunFailureCandidatesWaitForTriggerRetryDecision(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	result := &pipelinerun.RunResult{Metadata: pipelinerun.RunMetadata{RunSlug: "run-failed", Status: "failed"}, Timeline: pipelinerun.RunTimeline{Events: []pipelinerun.TimelineEvent{{At: at}}, Failure: &pipelinerun.FailureContext{Error: "private provider response"}}}
	candidates, err := runNotificationCandidates("acme", "ops", "dev", "report", result)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, model.NotificationTypePipelineRunFailed, candidates[0].NotificationType)
	require.Equal(t, at, candidates[0].ObservedAt)
	encoded, err := json.Marshal(candidates)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private provider response")
	result.Metadata.Source.Schedule = &pipelinerun.RunSourceScheduleSummary{}
	candidates, err = runNotificationCandidates("acme", "ops", "dev", "report", result)
	require.NoError(t, err)
	require.Empty(t, candidates)
	result.Metadata.Source.Schedule = nil
	result.Metadata.Source.Webhook = &pipelinerun.RunSourceWebhookSummary{}
	candidates, err = runNotificationCandidates("acme", "ops", "dev", "report", result)
	require.NoError(t, err)
	require.Empty(t, candidates)
	result.Metadata.Source.Webhook = nil
	for _, status := range []string{"running", "canceled", "partial", "queued"} {
		result.Metadata.Status = status
		candidates, err = runNotificationCandidates("acme", "ops", "dev", "report", result)
		require.NoError(t, err)
		require.Empty(t, candidates)
	}
}

func TestSuccessfulRunObservationIsIndependentOfOrigin(t *testing.T) {
	completed := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	for _, origin := range []model.PipelineRunSourceType{model.PipelineRunSourceTypeManual, model.PipelineRunSourceTypeAPI, model.PipelineRunSourceTypeSchedule, model.PipelineRunSourceTypeWebhook} {
		for _, status := range []string{"succeeded", "failed", "canceled", "running", "queued", "partial"} {
			t.Run(origin.String()+"/"+status, func(t *testing.T) {
				result := &pipelinerun.RunResult{Metadata: pipelinerun.RunMetadata{RunSlug: "run-one", Status: status, Source: pipelinerun.RunSourceSummary{Type: origin}}, Timeline: pipelinerun.RunTimeline{Events: []pipelinerun.TimelineEvent{{At: completed}}}}
				candidates, err := runNotificationCandidates("acme", "ops", "prod", "orders", result)
				require.NoError(t, err)
				successes := 0
				for _, candidate := range candidates {
					if candidate.NotificationType == model.NotificationTypePipelineRunSucceeded {
						successes++
						require.Equal(t, completed, candidate.ObservedAt)
					}
				}
				if status == "succeeded" {
					require.Equal(t, 1, successes)
				} else {
					require.Zero(t, successes)
				}
			})
		}
	}
}
