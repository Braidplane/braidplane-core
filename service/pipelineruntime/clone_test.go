package pipelineruntime

import (
	"encoding/json"
	"testing"

	pipelineruntimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/stretchr/testify/require"
)

func TestClonePipelineRetainsRuntimeNumericTypesAndPrecision(t *testing.T) {
	values := map[string]any{
		"limit":   100,
		"wide":    json.Number("9007199254740993"),
		"decimal": json.Number("0.123456789012345678901234567890"),
		"nested":  []any{json.Number("9223372036854775808"), json.Number("9007199254740993e0")},
	}
	original := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{
		{Query: &pipelinespec.QueryRef{Parameters: values, OperationInline: values}},
		{Sink: &pipelinespec.SinkSpec{Config: values, OperationInline: values}},
	}}
	cloned, err := clonePipeline(original)
	require.NoError(t, err)
	want, err := json.Marshal(values)
	require.NoError(t, err)
	for _, actual := range []map[string]any{cloned.Steps[0].Query.Parameters, cloned.Steps[0].Query.OperationInline, cloned.Steps[1].Sink.Config, cloned.Steps[1].Sink.OperationInline} {
		require.Equal(t, 100, actual["limit"])
		require.Equal(t, values["decimal"], actual["decimal"])
		encoded, err := json.Marshal(actual)
		require.NoError(t, err)
		require.Equal(t, string(want), string(encoded))
	}
	values["limit"] = 200
	require.Equal(t, 100, cloned.Steps[0].Query.Parameters["limit"])
}

func TestClonePipelineRetainsLiteralRuntimeSyntax(t *testing.T) {
	original := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{{Query: &pipelinespec.QueryRef{Parameters: map[string]any{
		"as_of": pipelineruntimevars.LiteralString("{{runtime.run.date | add_days:-1}}"),
	}}}}}
	cloned, err := clonePipeline(original)
	require.NoError(t, err)
	require.Equal(t, pipelineruntimevars.LiteralString("{{runtime.run.date | add_days:-1}}"), cloned.Steps[0].Query.Parameters["as_of"])
}
