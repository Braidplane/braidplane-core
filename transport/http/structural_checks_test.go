package http

import (
	"encoding/json"
	"testing"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/stretchr/testify/require"
)

func TestStructuralCheckReadPreservesStatesAndMissingEvidence(t *testing.T) {
	require.Nil(t, toAPIStructuralChecks(nil))
	for _, status := range pipelinerun.CheckStatusValues() {
		checks := toAPIStructuralChecks([]pipelinerun.StructuralCheck{{ID: "check-one", Step: "deliver", Status: status, ReasonCode: "not_reached", Phase: "pre_sink", Origin: "step", AttemptNumber: 2, ProjectionDigest: "projection", CompiledPlanDigest: "plan", DeploymentResolutionDigest: "resolution", Evaluator: "structural-v1", Policy: "policy-v1", Evidence: &pipelinerun.CheckEvidence{Rows: 1, BatchScope: "observed_batch_completeness_unknown"}, Window: pipelinerun.CheckWindow{Kind: "backfill", HasLowerBound: true, HasUpperBound: true, BackfillSeriesSlug: "backfill-one", BackfillChunkIndex: 1}}})
		require.Len(t, *checks, 1)
		require.Equal(t, status.String(), string((*checks)[0].Status))
		require.Equal(t, 2, (*checks)[0].AttemptNumber)
		raw, err := json.Marshal(checks)
		require.NoError(t, err)
		require.Contains(t, string(raw), `"compiled_plan_digest":"plan"`)
		require.NotContains(t, string(raw), "pipeline_version_id")
	}
}

func TestTimelinePreservesRecordedResolutionWithoutChecks(t *testing.T) {
	require.Nil(t, toAPIPipelineRunTimeline(pipelinerun.RunTimeline{}).DeploymentResolutionDigest)
	timeline := toAPIPipelineRunTimeline(pipelinerun.RunTimeline{DeploymentResolutionDigest: "sha256:recorded"})
	require.Equal(t, "sha256:recorded", *timeline.DeploymentResolutionDigest)
	require.Nil(t, timeline.StructuralChecks)
}
