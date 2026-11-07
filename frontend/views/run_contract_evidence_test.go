package views

import (
	"bytes"
	"context"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestRunContractEvidencePreservesStatesAndEscapesDeclarations(t *testing.T) {
	for _, status := range []api.PipelineRunStructuralCheckStatus{"passed", "failed", "skipped", "unavailable", "error"} {
		t.Run(string(status), func(t *testing.T) {
			run := api.PipelineRunRecord{Timeline: api.PipelineRunTimeline{
				DeploymentResolutionDigest: new("sha256:historical"),
				StructuralChecks:           &[]api.PipelineRunStructuralCheck{{Step: "merge", Input: new("orders"), Origin: "operation", Phase: "normalized_input", Status: status, ReasonCode: "not_reached", Declaration: "<script>unsafe</script>", AttemptNumber: 2, CompiledPlanDigest: new("sha256:executed-plan")}},
			}}
			var out bytes.Buffer
			require.NoError(t, PipelineRunContractEvidence(run).Render(context.Background(), &out))
			for _, expected := range []string{string(status), "operation", "normalized_input", "input orders", "sha256:historical", "sha256:executed-plan", "Quality and freshness remain unverified", "&lt;script&gt;"} {
				require.Contains(t, out.String(), expected)
			}
			require.NotContains(t, out.String(), "<script>")
		})
	}
	var out bytes.Buffer
	require.NoError(t, PipelineRunContractEvidence(api.PipelineRunRecord{}).Render(context.Background(), &out))
	require.Contains(t, out.String(), "No structural checks recorded")
	require.Contains(t, out.String(), "Unavailable")
}

func TestRunCompositionInspectionStates(t *testing.T) {
	for _, status := range []api.PipelineRunCompositionInspectionStatus{"available", "not_recorded", "unavailable", "unsupported"} {
		var out bytes.Buffer
		run := api.PipelineRunRecord{Composition: &api.PipelineRunCompositionInspection{Status: status}}
		if status == "available" {
			run.Composition.Pins = []api.PipelineContractPinInspection{{Step: "merge", Port: "input:orders", Origin: "step", ContractSlug: "orders", RevisionSlug: "old-revision", SchemaObjectId: "row", Digest: "exact-pin"}}
			run.Composition.Targets = []api.PipelineTargetInspection{{Step: "merge", Reference: "logical", DataSourceSlug: "old-target", BindingRevision: "old-binding"}}
		}
		require.NoError(t, PipelineRunContractEvidence(run).Render(context.Background(), &out))
		require.Contains(t, out.String(), string(status))
		if status == "available" {
			for _, fact := range []string{"old-revision", "input:orders", "old-target", "old-binding", "exact-pin"} {
				require.Contains(t, out.String(), fact)
			}
		} else {
			require.Contains(t, out.String(), "cannot reconstruct historical evidence")
		}
	}
}
