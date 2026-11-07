package metrics

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestProofMaintenanceScrapeReportsFailureWithoutStaleOrPrivateValues(t *testing.T) {
	meter, err := New(Config{Enabled: true, ServiceName: "backend", Environment: "test"})
	require.NoError(t, err)
	failed := false
	meter.RegisterProofMaintenanceHealth(func(context.Context) (model.ProofMaintenanceHealth, error) {
		if failed {
			return model.ProofMaintenanceHealth{}, xerror.Unexpected().Detail("private-database-diagnostic")
		}
		return model.ProofMaintenanceHealth{LastSuccessTimestamp: 1234, RecentFailures: 2, CancellationDue: 1, ExpiryDue: 2, EvidenceDue: 3, MetadataDue: 4}, nil
	})
	body := scrapeMetrics(t, meter)
	require.Contains(t, body, `braidplane_proof_maintenance_health_available{deployment_environment="test",service_name="backend"} 1`)
	require.Contains(t, body, `braidplane_proof_maintenance_last_success_timestamp_seconds{deployment_environment="test",service_name="backend"} 1234`)
	require.Contains(t, body, `braidplane_proof_maintenance_recent_failures{deployment_environment="test",service_name="backend"} 2`)
	for phase, count := range map[string]string{"cancellation": "1", "expiry": "2", "evidence": "3", "metadata": "4"} {
		require.Contains(t, body, `braidplane_proof_maintenance_due_jobs{deployment_environment="test",phase="`+phase+`",service_name="backend"} `+count)
	}
	failed = true
	body = scrapeMetrics(t, meter)
	require.Contains(t, body, `braidplane_proof_maintenance_health_available{deployment_environment="test",service_name="backend"} 0`)
	for _, absent := range []string{"private-", "braidplane_proof_maintenance_due_jobs", "braidplane_proof_maintenance_recent_failures", "braidplane_proof_maintenance_last_success_timestamp_seconds"} {
		require.NotContains(t, body, absent)
	}
	failed = false
	require.Contains(t, scrapeMetrics(t, meter), `braidplane_proof_maintenance_health_available{deployment_environment="test",service_name="backend"} 1`)
}

func TestProofControlsAndActivityHaveClosedLabelsAndRecoverAfterProbeFailure(t *testing.T) {
	meter, err := New(Config{Enabled: true, ServiceName: "backend", Environment: "test"})
	require.NoError(t, err)
	meter.RecordProofControl(model.EventActionAccept, model.ProofControlOutcomeLimited)
	meter.RecordProofControl(model.EventAction(-987), model.ProofControlOutcome(-654))
	failed := false
	meter.RegisterProofActivity(func(context.Context) (proofevidence.ActivitySnapshot, error) {
		if failed {
			return proofevidence.ActivitySnapshot{}, xerror.Unexpected().Detail("private-provider-diagnostic")
		}
		return proofevidence.ActivitySnapshot{Starts: 3, Results: map[proofevidence.Outcome]int64{proofevidence.OutcomeWorkerLost: 2, proofevidence.Outcome(-987): 999}}, nil
	})
	body := scrapeMetrics(t, meter)
	require.Contains(t, body, `braidplane_proof_control_total{action="confirm",deployment_environment="test",outcome="limited",service_name="backend"} 1`)
	require.Contains(t, body, `braidplane_proof_control_total{action="unknown",deployment_environment="test",outcome="unknown",service_name="backend"} 1`)
	require.Contains(t, body, `braidplane_proof_recent_starts{deployment_environment="test",service_name="backend"} 3`)
	require.Contains(t, body, `braidplane_proof_recent_results{deployment_environment="test",outcome="worker_lost",service_name="backend"} 2`)
	require.Contains(t, body, `braidplane_proof_recent_results{deployment_environment="test",outcome="complete",service_name="backend"} 0`)
	require.NotContains(t, body, "-987")
	failed = true
	body = scrapeMetrics(t, meter)
	require.Contains(t, body, `braidplane_proof_activity_available{deployment_environment="test",service_name="backend"} 0`)
	for _, absent := range []string{"braidplane_proof_recent_starts", "braidplane_proof_recent_results", "private-"} {
		require.NotContains(t, body, absent)
	}
	failed = false
	require.Contains(t, scrapeMetrics(t, meter), `braidplane_proof_activity_available{deployment_environment="test",service_name="backend"} 1`)
}
