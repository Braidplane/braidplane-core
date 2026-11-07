package http

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

func TestRunCompositionMapsOnlyInspectionMetadata(t *testing.T) {
	require.Nil(t, toAPIRunComposition(nil))
	for _, status := range []string{"available", "not_recorded", "unavailable", "unsupported"} {
		value := toAPIRunComposition(&pipelinesvc.RunCompositionInspection{Status: status, TemplateDigest: "template", CompiledPlanDigest: "compiled", ResolutionDigest: "resolution", Pins: []pipelinesvc.RunContractPinInspection{{Step: "merge", Port: "input:orders", Origin: "operation", Contract: "orders", Revision: "v1", Object: "row", Digest: "pin"}}, Operations: []pipelinesvc.RunOperationInspection{{Step: "merge", Operation: "read-orders", Revision: "old-revision", ResourceScope: &resourcescope.Identity{PolicyID: resourcescope.S3PolicyID, DataSource: "logical", Operation: "read-orders", Contract: "orders", Revision: "v1", ObjectID: "row", Representation: "rows"}}}, Targets: []pipelinesvc.RunTargetInspection{{Step: "merge", Reference: "logical", DataSource: "old-target"}}})
		require.Equal(t, status, string(value.Status))
		require.Equal(t, "input:orders", value.Pins[0].Port)
		require.Equal(t, "operation", string(value.Pins[0].Origin))
		require.Equal(t, "old-revision", value.Operations[0].Revision)
		require.Equal(t, resourcescope.S3PolicyID, value.Operations[0].ResourceScope.PolicyId)
		require.Equal(t, "old-target", value.Targets[0].DataSourceSlug)
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		for _, absent := range []string{"document", "content", "parameters", "credential", "statement", "resolution_inputs"} {
			require.NotContains(t, string(raw), absent)
		}
	}
}
