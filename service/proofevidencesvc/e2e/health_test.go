package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceHealthReadsOnlyAggregatesAndTracksCleanupEligibility(t *testing.T) {
	f := newFixture(t)
	_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.NoError(t, err)
	tx, err := f.pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	before, err := f.repo.MaintenanceHealth(t.Context(), tx)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `UPDATE proof_observation_jobs SET deadline_at=statement_timestamp()-interval '4 minutes',evidence_retain_until=statement_timestamp()-interval '3 minutes',budget_expires_at=statement_timestamp()-interval '2 minutes',metadata_retain_until=statement_timestamp()-interval '1 minute' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	// A disabled environment needs cancellation even before the repair sweep disables its policy.
	_, err = tx.Exec(t.Context(), `UPDATE project_environments SET deleted_at=statement_timestamp() WHERE project_id=(SELECT p.id FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND p.slug=$2)`, f.scope.Organization, f.scope.Project)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `INSERT INTO river_job(kind,queue,args,max_attempts,state,finalized_at) VALUES
	('proof_observation_maintenance','event_maintenance','{}',1,'completed',statement_timestamp()),
	('proof_observation_maintenance','event_maintenance','{}',1,'discarded',statement_timestamp()),
	('proof_observation_maintenance','event_maintenance','{}',1,'discarded',statement_timestamp()),
	('proof_observation_maintenance','event_maintenance','{}',1,'discarded',statement_timestamp()-interval '11 minutes'),
	('event_prune','event_maintenance','{}',1,'discarded',statement_timestamp())`)
	require.NoError(t, err)
	health, err := f.repo.MaintenanceHealth(t.Context(), tx)
	require.NoError(t, err)
	require.Equal(t, before.CancellationDue+1, health.CancellationDue)
	require.Equal(t, before.ExpiryDue+1, health.ExpiryDue)
	require.Equal(t, before.EvidenceDue+1, health.EvidenceDue)
	require.Equal(t, before.MetadataDue, health.MetadataDue, "unfinished jobs retain their metadata")
	require.Equal(t, before.RecentFailures+2, health.RecentFailures)
	require.Greater(t, health.LastSuccessTimestamp, float64(time.Now().Add(-time.Minute).Unix()))
	_, err = tx.Exec(t.Context(), `UPDATE proof_observation_jobs SET state='finished',completed_at=statement_timestamp(),canceled_at=statement_timestamp(),target_ciphertext=NULL,snapshot_ciphertext=NULL WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	health, err = f.repo.MaintenanceHealth(t.Context(), tx)
	require.NoError(t, err)
	require.Equal(t, before.CancellationDue, health.CancellationDue)
	require.Equal(t, before.ExpiryDue, health.ExpiryDue)
	require.Equal(t, before.EvidenceDue, health.EvidenceDue)
	require.Equal(t, before.MetadataDue+1, health.MetadataDue)
	require.NoError(t, tx.Rollback(t.Context()))
	_, err = f.maintenance.Health(t.Context())
	require.NoError(t, err)
}

func TestMaintenanceHealthBoundsBlockedQueriesAndRedactsFailure(t *testing.T) {
	f := newFixture(t)
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(t.Context(), `LOCK TABLE proof_observation_jobs IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	started := time.Now()
	_, err = f.maintenance.Health(t.Context())
	require.ErrorContains(t, err, "Proof maintenance health unavailable")
	require.Less(t, time.Since(started), 4*time.Second)
	require.NotContains(t, err.Error(), "proof_observation_jobs")
	started = time.Now()
	_, err = f.maintenance.Activity(t.Context())
	require.ErrorContains(t, err, "Proof activity unavailable")
	require.Less(t, time.Since(started), 4*time.Second)
	require.NotContains(t, err.Error(), "proof_observation_jobs")
	require.NoError(t, tx.Rollback(t.Context()))
	_, err = f.maintenance.Health(t.Context())
	require.NoError(t, err)
	_, err = f.maintenance.Activity(t.Context())
	require.NoError(t, err)
}

func TestActivityFiltersTimeWindowAndClassifiesPrivateMalformedOutcomes(t *testing.T) {
	f := newFixture(t)
	_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.NoError(t, err)
	tx, err := f.pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	before, err := f.repo.Activity(t.Context(), tx)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `UPDATE proof_observation_jobs SET state='finished',completed_at=statement_timestamp(),started_at=statement_timestamp(),result='{"outcome":"private-outcome","schema":"private-target"}' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	activity, err := f.repo.Activity(t.Context(), tx)
	require.NoError(t, err)
	require.Equal(t, before.Starts+1, activity.Starts)
	require.Equal(t, before.Results[proofevidence.OutcomeUnknown]+1, activity.Results[proofevidence.OutcomeUnknown])
	for outcome := range activity.Results {
		require.True(t, outcome.IsAOutcome())
	}
	_, err = tx.Exec(t.Context(), `UPDATE proof_observation_jobs SET completed_at=statement_timestamp()-interval '11 minutes',started_at=statement_timestamp()-interval '11 minutes' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	activity, err = f.repo.Activity(t.Context(), tx)
	require.NoError(t, err)
	require.Equal(t, before, activity)
	_, err = tx.Exec(t.Context(), `UPDATE proof_observation_jobs SET completed_at=statement_timestamp()+interval '1 hour',started_at=statement_timestamp()+interval '1 hour' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	activity, err = f.repo.Activity(t.Context(), tx)
	require.NoError(t, err)
	require.Equal(t, before, activity)
	require.NoError(t, tx.Rollback(t.Context()))
}
