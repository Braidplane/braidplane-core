package pipelinesvc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
)

func TestPipelineSpecHashesMatch(t *testing.T) {
	t.Parallel()

	left := "spec-hash-1"
	right := "spec-hash-1"
	other := "spec-hash-2"

	require.True(t, pipelineSpecHashesMatch(nil, nil))
	require.True(t, pipelineSpecHashesMatch(&left, &right))
	require.False(t, pipelineSpecHashesMatch(&left, nil))
	require.False(t, pipelineSpecHashesMatch(nil, &right))
	require.False(t, pipelineSpecHashesMatch(&left, &other))
	require.Equal(t, "<nil>", formatOptionalSpecHash(nil))
	require.Equal(t, left, formatOptionalSpecHash(&left))
}

func mustMarshalPipelineForMergeTest(t *testing.T, pipeline *pipelinespec.Pipeline) []byte {
	t.Helper()

	payload, err := json.Marshal(pipeline)
	require.NoError(t, err)
	return payload
}
