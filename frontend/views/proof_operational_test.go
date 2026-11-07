package views

import (
	"bytes"
	"context"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestOperationalProofSeparatesCapturedVerdictAndCurrentEvidence(t *testing.T) {
	for _, status := range []api.ProofReceiptOperationalEvidenceStatus{"available", "missing", "expired", "changed"} {
		var out bytes.Buffer
		receipt := &api.ProofReceipt{AggregateVerdict: "passed", OperationalEvidenceStatus: &status, OperationalCurrentVerdict: new(api.ProofVerdictUnverified), Candidate: api.ProofCandidateIdentity{OrganizationSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "dev"}, Operational: &api.ProofOperationalCapture{SourceReceiptId: "static-receipt", PolicyVersion: "policy-v1", RecordDigest: "captured-digest", Selection: api.ProofOperationalSelection{Run: "old-run", Attempt: 2, Step: "store", Input: "orders", Origin: "operation", Phase: "pre_sink", Declaration: "operation/pre_sink", CompiledPlanDigest: "old-plan", DeploymentResolutionDigest: "old-resolution"}}}
		require.NoError(t, ProofOperationalContext(receipt).Render(context.Background(), &out))
		for _, value := range []string{"Captured verdict", "Current supporting verdict", "passed", "unverified", "old-plan", "old-resolution", "attempt 2", "captured-digest"} {
			require.Contains(t, out.String(), value)
		}
		require.Contains(t, out.String(), ProofOperationalAvailabilityLabel(&status))
		require.Equal(t, ProofOperationalAvailabilityLabel(&status), ProofFreshnessLabel(*receipt))
	}
	var out bytes.Buffer
	require.NoError(t, ProofOperationalContext(&api.ProofReceipt{}).Render(context.Background(), &out))
	require.Contains(t, out.String(), "no admitted operational capture")
}
