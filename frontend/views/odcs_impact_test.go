package views

import (
	"bytes"
	"context"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestODCSImpactShowsScopeGapsAndEscapesPaths(t *testing.T) {
	data := ODCSCatalogView{Org: "acme", Project: "etl", Contract: "orders", Revision: "v1", CompareRevision: "v2"}
	var out bytes.Buffer
	require.NoError(t, ODCSImpactComparison(data).Render(context.Background(), &out))
	require.Contains(t, out.String(), "Rich executable meaning requires both retained compiled plans")
	data.ImpactError = "Comparison unavailable. Check access."
	out.Reset()
	require.NoError(t, ODCSImpactComparison(data).Render(context.Background(), &out))
	require.Contains(t, out.String(), `role="alert"`)
	data.ImpactError = ""
	data.Impact = &api.ODCSRevisionImpact{Objects: []api.ODCSObjectImpact{{SchemaObjectId: "row", Comparison: "supported", ComparisonScope: new(api.ODCSObjectImpactComparisonScope("selected_projection")), Changes: []api.ODCSImpactChange{{Kind: "data_path_changed", Path: `$["<script>"]`, Severity: "breaking", ReasonCode: "structural_data_path_changed"}}}}, Consumers: []api.ODCSConsumerImpact{{PipelineSlug: "sync", StepSlug: "merge", Port: "input:orders"}}}
	out.Reset()
	require.NoError(t, ODCSImpactComparison(data).Render(context.Background(), &out))
	for _, value := range []string{"selected_projection", "breaking", "input:orders", "structural_data_path_changed", "&lt;script&gt;", "External consumers are unknown"} {
		require.Contains(t, out.String(), value)
	}
	require.NotContains(t, out.String(), "<script>")
}
