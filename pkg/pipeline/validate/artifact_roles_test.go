package validate

import (
	"testing"

	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/stretchr/testify/require"
)

func TestArtifactRoles(t *testing.T) {
	sink := &pipelinespec.SinkSpec{ArtifactDelivery: true, IdempotencyClass: pipelinespec.SinkIdempotencyClassUnsafe, OperationType: "ftp", OperationRef: "write", Config: map[string]any{"path": "reserved/orders.csv", "format": "csv"}}
	query := &pipelinespec.QueryRef{ArtifactInput: true, OperationType: "ftp", OperationRef: "read"}
	require.NoError(t, validateArtifactRoles([]pipelinespec.Step{{Sink: sink}}))
	require.NoError(t, validateArtifactRoles([]pipelinespec.Step{{Query: query}, {Sink: &pipelinespec.SinkSpec{}}}))
	for _, name := range []string{"orders.csv", "../orders.csv", "/reserved/orders.csv", "reserved/{{run}}.csv", "reserved/orders.json"} {
		sink.Config["path"] = name
		require.Error(t, validateArtifactRoles([]pipelinespec.Step{{Sink: sink}}), name)
	}
	sink.Config["path"] = "reserved/orders.csv"
	sink.Config["overwrite"] = true
	require.Error(t, validateArtifactRoles([]pipelinespec.Step{{Sink: sink}}))
	delete(sink.Config, "overwrite")
	require.Error(t, validateArtifactRoles([]pipelinespec.Step{{Sink: sink}, {Sink: &pipelinespec.SinkSpec{}}}))
	query.Parameters = map[string]any{"path": "other.csv"}
	require.Error(t, validateArtifactRoles([]pipelinespec.Step{{Query: query}, {Sink: &pipelinespec.SinkSpec{}}}))
}
