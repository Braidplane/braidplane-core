package e2e

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/tenantfixture"
)

func assertPipelineForeignKey(t *testing.T, err error, constraint string) {
	t.Helper()
	var pgError *pgconn.PgError
	require.ErrorAs(t, err, &pgError)
	require.Equal(t, "23503", pgError.Code)
	require.Equal(t, constraint, pgError.ConstraintName)
}

func TestPipelineRelationshipsRejectMismatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "pipeline tenant constraints")
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedPipelineBoundary(t, tx, true)
	for other := 1; other < 4; other++ {
		for _, tc := range tenantfixture.PipelineMismatches(f, other) {
			t.Run(fmt.Sprintf("scope_%d_%s_%s", other, tc.Table, tc.Column), func(t *testing.T) {
				attempt, err := tx.Begin(t.Context())
				require.NoError(t, err)
				defer func() { _ = attempt.Rollback(context.Background()) }()
				_, err = attempt.Exec(t.Context(), fmt.Sprintf(`UPDATE %s SET %s=$1 WHERE id=$2`, tc.Table, tc.Column), tc.Parent, tc.Child)
				assertPipelineForeignKey(t, err, tc.Constraint)
			})
		}
	}
	t.Run("report_without_owner", func(t *testing.T) {
		attempt, err := tx.Begin(t.Context())
		require.NoError(t, err)
		defer func() { _ = attempt.Rollback(context.Background()) }()
		_, err = attempt.Exec(t.Context(), `UPDATE pipeline_validation_results SET impact_pipeline_id=NULL WHERE id=$1`, f.Validation)
		assertPipelineForeignKey(t, err, "fk_pipeline_validation_results_report")
	})
}

func TestPipelineRelationshipsPreserveLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "pipeline tenant constraints")
	for _, parent := range []string{"pipeline_versions", "pipeline_specs", "drift_impact_reports", "pipeline_proposal_authorities", "organization_members", "project_environments", "organizations"} {
		t.Run(parent, func(t *testing.T) {
			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(context.Background()) }()
			f := tenantfixture.SeedPipelineBoundary(t, tx, true)
			ids := map[string]any{"pipeline_versions": f.Versions[0], "pipeline_specs": f.Specs[0], "drift_impact_reports": f.Reports[0], "pipeline_proposal_authorities": f.Authorities[0], "organization_members": f.Members[0], "project_environments": f.Enabled, "organizations": f.Org[0]}
			column := "id"
			if parent == "organizations" {
				_, err = tx.Exec(t.Context(), `DELETE FROM environment_secrets WHERE organization_id=$1;`, f.Org[0])
				require.NoError(t, err)
				_, err = tx.Exec(t.Context(), `DELETE FROM project_environments WHERE organization_id=$1`, f.Org[0])
				require.NoError(t, err)
			}
			if parent == "drift_impact_reports" {
				column = "slug"
			}
			_, err = tx.Exec(t.Context(), fmt.Sprintf(`DELETE FROM %s WHERE %s=$1`, parent, column), ids[parent])
			require.NoError(t, err)
			var count int
			switch parent {
			case "pipeline_versions":
				for _, table := range []string{"pipeline_runs", "pipeline_deployments", "pipeline_incremental_checkpoints"} {
					require.NoError(t, tx.QueryRow(t.Context(), fmt.Sprintf(`SELECT count(*) FROM %s WHERE pipeline_slug='pipeline-0'`, table)).Scan(&count))
					require.Zero(t, count, table)
				}
				require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM pipeline_validation_results WHERE id=$1 AND project_id=$2 AND pipeline_id=$3 AND pipeline_version_id IS NULL AND impact_report_slug=$4`, f.Validation, f.Projects[0], f.Specs[0], f.Reports[0]).Scan(&count))
				require.Equal(t, 1, count)
				for _, table := range []string{"pipeline_specs", "pipeline_references", "pipeline_drafts"} {
					column := "current_version_id"
					if table == "pipeline_drafts" {
						column = "based_on_version_id"
					}
					require.NoError(t, tx.QueryRow(t.Context(), fmt.Sprintf(`SELECT count(*) FROM %s WHERE project_id=$1 AND %s IS NULL`, table, column), f.Projects[0]).Scan(&count))
					require.Positive(t, count, table)
				}
			case "pipeline_specs", "drift_impact_reports":
				require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM pipeline_validation_results WHERE id=$1 AND project_id=$2 AND impact_report_slug IS NULL AND impact_pipeline_id IS NULL`, f.Validation, f.Projects[0]).Scan(&count))
				require.Equal(t, 1, count)
				if parent == "pipeline_specs" {
					require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM pipeline_validation_results WHERE id=$1 AND pipeline_id IS NULL AND pipeline_version_id IS NULL`, f.Validation).Scan(&count))
					require.Equal(t, 1, count)
					require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM data_source_bindings WHERE id=$1`, f.Binding).Scan(&count))
					require.Zero(t, count)
				}
			case "pipeline_proposal_authorities":
				require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM pipeline_drafts WHERE id=$1 AND proposal_authority_id IS NULL AND organization_id=$2 AND project_id=$3`, f.Draft, f.Org[0], f.Projects[0]).Scan(&count))
				require.Equal(t, 1, count)
			case "organization_members":
				require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM pipeline_drafts WHERE id=$1`, f.Draft).Scan(&count))
				require.Zero(t, count)
			default:
				for _, table := range []string{"pipeline_runs", "pipeline_deployments", "pipeline_validation_results", "data_source_bindings"} {
					require.NoError(t, tx.QueryRow(t.Context(), fmt.Sprintf(`SELECT count(*) FROM %s WHERE project_environment_id=$1`, table), f.Enabled).Scan(&count))
					require.Zero(t, count, table)
				}
			}
			_, err = tx.Exec(t.Context(), `SET CONSTRAINTS ALL IMMEDIATE`)
			require.NoError(t, err)
		})
	}
}

func TestPipelineScopeKeysAndParentMutations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "pipeline tenant constraints")
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedPipelineBoundary(t, tx, true)
	_, err = tx.Exec(t.Context(), `INSERT INTO drift_impact_reports(data_source_id,slug,report_type) VALUES ($1,'source-owned','data_source')`, f.Source[0])
	require.NoError(t, err)
	// Keep other parents valid so each mutation identifies one foreign key.
	for _, tc := range []struct {
		sql, constraint string
		args            []any
	}{
		{`UPDATE data_source_bindings SET organization_id=$1,data_source_id=$3 WHERE id=$2`, "fk_data_source_bindings_scope", []any{f.Org[1], f.Binding, f.Source[1]}},
		{`UPDATE data_source_bindings SET pipeline_spec_id=$1,data_source_ref_id=$3 WHERE id=$2`, "fk_data_source_bindings_project_pipeline", []any{f.Specs[2], f.Binding, f.Refs[2]}},
		{`UPDATE pipeline_runs SET project_environment_id=$1 WHERE id=$2`, "fk_pipeline_runs_project_environment", []any{f.EnabledScopes[2], f.Run}},
		{`UPDATE pipeline_deployments SET project_environment_id=$1 WHERE id=$2`, "fk_pipeline_deployments_project_environment", []any{f.EnabledScopes[2], f.Deployment}},
		{`UPDATE pipeline_validation_results SET project_environment_id=$1 WHERE id=$2`, "fk_pipeline_validation_results_project_environment", []any{f.EnabledScopes[2], f.Validation}},
		{`UPDATE pipeline_drafts SET organization_id=$1,member_id=$3,proposal_authority_id=NULL WHERE id=$2`, "fk_pipeline_drafts_org_project", []any{f.Org[1], f.Draft, f.Members[1]}},
		{`UPDATE data_source_refs SET pipeline_spec_id=$1,slug='moved-ref' WHERE id=$2`, "fk_data_source_bindings_pipeline_ref", []any{f.Specs[1], f.Refs[0]}},
		{`UPDATE drift_impact_reports SET pipeline_spec_id=$1 WHERE slug=$2`, "fk_pipeline_validation_results_report", []any{f.Specs[1], f.Reports[0]}},
		{`INSERT INTO pipeline_validation_results(project_id,project_environment_id,pipeline_slug,validation_slug,source,valid,impact_report_slug) VALUES ($1,$2,'pipeline-0','missing-owner','manual',true,$3)`, "fk_pipeline_validation_results_report", []any{f.Projects[0], f.Enabled, f.Reports[1]}},
		{`INSERT INTO pipeline_validation_results(project_id,project_environment_id,pipeline_slug,validation_slug,source,valid,impact_pipeline_id,impact_report_slug) VALUES ($1,$2,'pipeline-0','wrong-owner','manual',true,$3,$4)`, "fk_pipeline_validation_results_report_pipeline", []any{f.Projects[0], f.Enabled, f.Specs[1], f.Reports[1]}},
		{`INSERT INTO pipeline_validation_results(project_id,project_environment_id,pipeline_slug,validation_slug,source,valid,impact_pipeline_id,impact_report_slug) VALUES ($1,$2,'pipeline-0','source-report','manual',true,$3,'source-owned')`, "fk_pipeline_validation_results_report", []any{f.Projects[0], f.Enabled, f.Specs[0]}},
		{`INSERT INTO data_source_bindings(organization_id,project_id,pipeline_spec_id,project_environment_id,data_source_ref_id,data_source_id) VALUES ($1,$2,$3,$4,$5,$6)`, "fk_data_source_bindings_pipeline_ref", []any{f.Org[0], f.Projects[0], f.Specs[0], f.Enabled, f.Refs[1], f.Source[0]}},
	} {
		t.Run(tc.constraint, func(t *testing.T) {
			attempt, err := tx.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = attempt.Rollback(context.Background()) }()
			_, err = attempt.Exec(t.Context(), tc.sql, tc.args...)
			if tc.constraint == "fk_pipeline_validation_results_report_pipeline" && err == nil {
				_, err = attempt.Exec(t.Context(), `SET CONSTRAINTS fk_pipeline_validation_results_report_pipeline IMMEDIATE`)
			}
			assertPipelineForeignKey(t, err, tc.constraint)
		})
	}
	_, err = tx.Exec(t.Context(), `UPDATE pipeline_validation_results SET impact_report_slug=NULL,impact_pipeline_id=NULL,pipeline_id=NULL,pipeline_version_id=NULL WHERE id=$1`, f.Validation)
	require.NoError(t, err)
}
