package e2e

import (
	"context"
	"fmt"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/tenantfixture"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestControlRelationshipsRejectCrossOrganizationPairings(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "control tenant constraints")
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedControlBoundary(t, tx, true)
	for _, tc := range tenantfixture.ControlMismatches(f) {
		t.Run(tc.Table+"_"+tc.Column, func(t *testing.T) {
			attempt, err := tx.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = attempt.Rollback(context.Background()) }()
			// Isolate organization validation from the older enabled-pair constraint.
			if tc.Column == "environment_id" {
				_, err = attempt.Exec(t.Context(), fmt.Sprintf(`ALTER TABLE %s DROP CONSTRAINT fk_%s_project_environment`, tc.Table, tc.Table))
				require.NoError(t, err)
			}
			// Remove the paired environment so project ownership fails independently.
			if tc.Column == "project_id" {
				_, err = attempt.Exec(t.Context(), fmt.Sprintf(`UPDATE %s SET environment_id=NULL WHERE id=$1`, tc.Table), tc.Child)
				require.NoError(t, err)
			}
			_, err = attempt.Exec(t.Context(), fmt.Sprintf(`UPDATE %s SET %s=$1 WHERE id=$2`, tc.Table, tc.Column), tc.Parent, tc.Child)
			var pgError *pgconn.PgError
			require.ErrorAs(t, err, &pgError)
			require.Equal(t, "23503", pgError.Code)
			require.Equal(t, tc.Constraint, pgError.ConstraintName)
		})
	}
	// Direct inserts cannot choose independent owners either.
	for _, tc := range []struct {
		statement, constraint string
		args                  []any
	}{
		{`INSERT INTO alert_policies(organization_id,slug,name,scope,project_id) VALUES ($1,'bad','Bad','project',$2)`, "fk_alert_policies_org_project", []any{f.Org[0], f.Project[1]}},
		{`INSERT INTO alert_policies(organization_id,slug,name,scope,project_id,environment_id) VALUES ($1,'bad','Bad','environment',$2,$3)`, "fk_alert_policies_org_environment", []any{f.Org[0], f.Project[0], f.Environment[1]}},
		{`INSERT INTO alert_policy_destinations(organization_id,alert_policy_id,destination_id) VALUES ($1,$2,$3)`, "fk_alert_policy_destinations_org_policy", []any{f.Org[0], f.Policy[1], f.Destination[0]}},
		{`INSERT INTO alert_policy_destinations(organization_id,alert_policy_id,destination_id) VALUES ($1,$2,$3)`, "fk_alert_policy_destinations_org_destination", []any{f.Org[0], f.Policy[0], f.Destination[1]}},
		{`INSERT INTO notification_records(organization_id,slug,project_id,notification_type,severity,lifecycle_state,dedupe_fingerprint,source_entity_type,source_action) VALUES ($1,'bad',$2,'pipeline.run_failed','critical','open','bad','pipeline_run','fail')`, "fk_notification_records_org_project", []any{f.Org[0], f.Project[1]}},
		{`INSERT INTO notification_records(organization_id,slug,project_id,environment_id,notification_type,severity,lifecycle_state,dedupe_fingerprint,source_entity_type,source_action) VALUES ($1,'bad',$2,$3,'pipeline.run_failed','critical','open','bad','pipeline_run','fail')`, "fk_notification_records_org_environment", []any{f.Org[0], f.Project[0], f.Environment[1]}},
		{`INSERT INTO execution_requests(id,organization_id,data_source_id,kind,request_ciphertext,deadline_at,expires_at) VALUES ($1,$2,$3,'query','encrypted',NOW(),NOW())`, "fk_execution_requests_org_data_source", []any{uuid.New().String(), f.Org[0], f.Source[1]}},
	} {
		t.Run("insert_"+tc.constraint, func(t *testing.T) {
			attempt, err := tx.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = attempt.Rollback(context.Background()) }()
			if tc.constraint == "fk_alert_policies_org_environment" || tc.constraint == "fk_notification_records_org_environment" {
				table := "alert_policies"
				if tc.constraint == "fk_notification_records_org_environment" {
					table = "notification_records"
				}
				_, err = attempt.Exec(t.Context(), fmt.Sprintf(`ALTER TABLE %s DROP CONSTRAINT fk_%s_project_environment`, table, table))
				require.NoError(t, err)
			}
			_, err = attempt.Exec(t.Context(), tc.statement, tc.args...)
			var pgError *pgconn.PgError
			require.ErrorAs(t, err, &pgError)
			require.Equal(t, "23503", pgError.Code)
			require.Equal(t, tc.constraint, pgError.ConstraintName)
		})
	}
	for _, tc := range []struct {
		record, policy, destination int
		constraint                  string
	}{
		{f.Record[1], f.Policy[0], f.Destination[0], "fk_alert_delivery_attempts_org_record"},
		{f.Record[0], f.Policy[1], f.Destination[0], "fk_alert_delivery_attempts_org_policy"},
		{f.Record[0], f.Policy[0], f.Destination[1], "fk_alert_delivery_attempts_org_destination"},
	} {
		t.Run("insert_"+tc.constraint, func(t *testing.T) {
			attempt, err := tx.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = attempt.Rollback(context.Background()) }()
			_, err = attempt.Exec(t.Context(), `INSERT INTO alert_delivery_attempts(organization_id,slug,notification_record_id,alert_policy_id,destination_id,destination_type,status,job_kind,job_queue,job_state,queued_at) VALUES ($1,'bad',$2,$3,$4,'email','queued','notification_delivery','notification_delivery','queued',NOW())`, f.Org[0], tc.record, tc.policy, tc.destination)
			var pgError *pgconn.PgError
			require.ErrorAs(t, err, &pgError)
			require.Equal(t, "23503", pgError.Code)
			require.Equal(t, tc.constraint, pgError.ConstraintName)
		})
	}
}

func TestControlRelationshipsPreserveValidLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "control tenant lifecycle")
	for _, parent := range []string{"alert_policies", "alert_destinations", "notification_records", "projects", "environments", "data_sources"} {
		t.Run(parent, func(t *testing.T) {
			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(context.Background()) }()
			f := tenantfixture.SeedControlBoundary(t, tx, true)
			id := map[string]int{"alert_policies": f.Policy[0], "alert_destinations": f.Destination[0], "notification_records": f.Record[0], "projects": f.Project[0], "environments": f.Environment[0], "data_sources": f.Source[0]}[parent]
			if parent == "environments" {
				_, err = tx.Exec(t.Context(), `DELETE FROM project_environments WHERE organization_id=$1`, f.Org[0])
				require.NoError(t, err)
			}
			if parent == "data_sources" {
				_, err = tx.Exec(t.Context(), `UPDATE projects SET pipeline_backend_data_source_id=NULL WHERE organization_id=$1`, f.Org[0])
				require.NoError(t, err)
			}
			_, err = tx.Exec(t.Context(), fmt.Sprintf(`DELETE FROM %s WHERE id=$1`, parent), id)
			require.NoError(t, err)
			var count int
			switch parent {
			case "alert_policies", "alert_destinations":
				column := "alert_policy_id"
				if parent == "alert_destinations" {
					column = "destination_id"
				}
				require.NoError(t, tx.QueryRow(t.Context(), fmt.Sprintf(`SELECT count(*) FROM alert_delivery_attempts WHERE id=$1 AND organization_id=$2 AND %s IS NULL`, column), f.Attempt, f.Org[0]).Scan(&count))
				require.Equal(t, 1, count)
				require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM alert_policy_destinations WHERE id=$1`, f.Binding).Scan(&count))
				require.Zero(t, count)
			case "data_sources":
				require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM execution_requests WHERE id=$1`, f.Request).Scan(&count))
				require.Zero(t, count)
			default:
				require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM alert_delivery_attempts WHERE id=$1`, f.Attempt).Scan(&count))
				require.Zero(t, count)
			}
		})
	}
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedControlBoundary(t, tx, true)
	for _, table := range []string{"alert_policies", "notification_records"} {
		_, err = tx.Exec(t.Context(), fmt.Sprintf(`UPDATE %s SET project_id=NULL,environment_id=NULL WHERE organization_id=$1`, table), f.Org[0])
		require.NoError(t, err)
	}
	_, err = tx.Exec(t.Context(), `UPDATE alert_delivery_attempts SET alert_policy_id=NULL,destination_id=NULL WHERE id=$1`, f.Attempt)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `UPDATE execution_requests SET data_source_id=NULL WHERE id=$1`, f.Request)
	require.NoError(t, err)
}
