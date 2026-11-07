package pipelineruntime

import (
	"context"
	"encoding/json"
	"testing"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestDestinationConstraintFailurePreservesSiblingEffectAndSafeDiagnosis(t *testing.T) {
	t.Parallel()
	req := checkTestRequest(t)
	first := req.Pipeline.Steps[2]
	first.Slug = "deliver-a"
	second := first
	second.Slug = "deliver-b"
	req.Pipeline.Steps[2] = first
	req.Pipeline.Steps = append(req.Pipeline.Steps, second)
	store, saved := durableCheckStore(t, nil)
	writes := map[string]int{}
	runner := checkTestRunner(t, store, &fakeSinkRuntime{executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
		if input.SinkStepSlug == "deliver-b" {
			return xerror.DestinationConstraintViolation().Detail("private-row-value")
		}
		writes[input.SinkStepSlug.String()]++
		return nil
	}})
	run, err := runner.Run(t.Context(), req)
	require.ErrorIs(t, err, xerror.DestinationConstraintViolation())
	require.NotNil(t, run)
	require.Equal(t, map[string]int{"deliver-a": 1}, writes)
	var durable pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
	require.Equal(t, "failed", durable.Status)
	require.Equal(t, pipelinerun.FailureReasonCodeDestinationConstraintViolation, durable.Failure.ReasonCode)
	require.Equal(t, "deliver-b", durable.Failure.Step.String())
	require.Equal(t, "sink", durable.Failure.Side)
	require.Equal(t, "data_source", durable.Failure.Kind)
	require.Equal(t, xerror.DestinationConstraintViolation().Message(), durable.Failure.Error)
	require.Contains(t, durable.Failure.ActionHint, "destination constraints")
	require.Contains(t, durable.Failure.ActionHint, "does not prove rollback")
	require.NotContains(t, string((*saved)[len(*saved)-1]), "private-row-value")
}
