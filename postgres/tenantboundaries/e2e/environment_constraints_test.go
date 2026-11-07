package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/tenantfixture"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestEnvironmentRelationshipsRejectCrossOrganizationPairings(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "environment tenant constraints")
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedEnvironmentBoundary(t, tx, true)
	tests := []struct {
		name, sql, constraint string
		args                  []any
	}{
		{"insert_project_owner", `INSERT INTO project_environments(organization_id,project_id,environment_id) VALUES ($1,$2,$3)`, "fk_project_environments_org_project", []any{f.Org[0], f.Project[1], f.Environment[0]}},
		{"insert_project_environment", `INSERT INTO project_environments(organization_id,project_id,environment_id) VALUES ($1,$2,$3)`, "fk_project_environments_org_environment", []any{f.Org[0], f.Project[0], f.Environment[1]}},
		{"insert_secret_environment", `INSERT INTO environment_secrets(organization_id,environment_id,credential_id,key) VALUES ($1,$2,$3,'OTHER')`, "fk_environment_secrets_org_environment", []any{f.Org[0], f.Environment[1], f.Credential[0]}},
		{"insert_secret_credential", `INSERT INTO environment_secrets(organization_id,environment_id,credential_id,key) VALUES ($1,$2,$3,'OTHER')`, "fk_environment_secrets_org_credential", []any{f.Org[0], f.Environment[0], f.Credential[1]}},
		{"insert_runtime_variable", `INSERT INTO environment_runtime_variables(organization_id,environment_id,key,value_type,value_text) VALUES ($1,$2,'OTHER','string','value')`, "fk_environment_runtime_variables_org_environment", []any{f.Org[0], f.Environment[1]}},
		{"insert_project_backend", `INSERT INTO projects(organization_id,slug,name,pipeline_backend_data_source_id) VALUES ($1,'other','Other',$2)`, "fk_projects_org_backend_data_source", []any{f.Org[0], f.Source[1]}},
		{"update_project_owner", `UPDATE project_environments SET project_id=$1 WHERE id=$2`, "fk_project_environments_org_project", []any{f.Project[1], f.Enabled}},
		{"update_project_environment", `UPDATE project_environments SET environment_id=$1 WHERE id=$2`, "fk_project_environments_org_environment", []any{f.Environment[1], f.Enabled}},
		{"update_secret_environment", `UPDATE environment_secrets SET environment_id=$1 WHERE id=$2`, "fk_environment_secrets_org_environment", []any{f.Environment[1], f.Secret}},
		{"update_secret_credential", `UPDATE environment_secrets SET credential_id=$1 WHERE id=$2`, "fk_environment_secrets_org_credential", []any{f.Credential[1], f.Secret}},
		{"update_runtime_variable", `UPDATE environment_runtime_variables SET environment_id=$1 WHERE id=$2`, "fk_environment_runtime_variables_org_environment", []any{f.Environment[1], f.Variable}},
		{"update_project_backend", `UPDATE projects SET pipeline_backend_data_source_id=$1 WHERE id=$2`, "fk_projects_org_backend_data_source", []any{f.Source[1], f.Project[0]}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attempt, err := tx.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = attempt.Rollback(context.Background()) }()
			_, err = attempt.Exec(t.Context(), tc.sql, tc.args...)
			var pgError *pgconn.PgError
			require.ErrorAs(t, err, &pgError)
			require.Equal(t, "23503", pgError.Code)
			require.Equal(t, tc.constraint, pgError.ConstraintName)
		})
	}
	// Isolate each parent reference so its ownership mutation proves this slice's FK.
	for _, tc := range []struct {
		name, preparation, sql, constraint string
		args                               []any
	}{
		{"project_parent", `UPDATE projects SET pipeline_backend_data_source_id=NULL WHERE organization_id=$1`, `UPDATE projects SET organization_id=$1,slug='moved' WHERE id=$2`, "fk_project_environments_org_project", []any{f.Org[1], f.Project[0]}},
		{"environment_parent", `DELETE FROM environment_secrets WHERE organization_id=$1; DELETE FROM environment_runtime_variables WHERE organization_id=$1`, `UPDATE environments SET organization_id=$1,slug='moved' WHERE id=$2`, "fk_project_environments_org_environment", []any{f.Org[1], f.Environment[0]}},
		{"secret_environment_parent", `DELETE FROM project_environments WHERE organization_id=$1; DELETE FROM environment_runtime_variables WHERE organization_id=$1`, `UPDATE environments SET organization_id=$1,slug='moved' WHERE id=$2`, "fk_environment_secrets_org_environment", []any{f.Org[1], f.Environment[0]}},
		{"credential_parent", `UPDATE projects SET pipeline_backend_data_source_id=NULL WHERE organization_id=$1; DELETE FROM data_sources WHERE organization_id=$1`, `UPDATE credentials SET organization_id=$1,slug='moved' WHERE id=$2`, "fk_environment_secrets_org_credential", []any{f.Org[1], f.Credential[0]}},
		{"source_parent", "", `UPDATE data_sources SET organization_id=$1,credential_id=$2,slug='moved' WHERE id=$3`, "fk_projects_org_backend_data_source", []any{f.Org[1], f.Credential[1], f.Source[0]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt, err := tx.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = attempt.Rollback(context.Background()) }()
			if tc.preparation != "" {
				for _, statement := range strings.Split(tc.preparation, ";") {
					_, err = attempt.Exec(t.Context(), statement, f.Org[0])
					require.NoError(t, err)
				}
			}
			_, err = attempt.Exec(t.Context(), tc.sql, tc.args...)
			var pgError *pgconn.PgError
			require.ErrorAs(t, err, &pgError)
			require.Equal(t, "23503", pgError.Code)
			require.Equal(t, tc.constraint, pgError.ConstraintName)
		})
	}
}

func TestEnvironmentRelationshipsPreserveValidLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "environment tenant lifecycle")
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedEnvironmentBoundary(t, tx, true)
	for _, tc := range []struct {
		query, constraint string
		id                int
	}{
		{`DELETE FROM credentials WHERE id=$1`, "fk_environment_secrets_org_credential", f.Credential[0]},
		{`DELETE FROM environments WHERE id=$1`, "fk_project_environments_org_environment", f.Environment[0]},
		{`DELETE FROM data_sources WHERE id=$1`, "fk_projects_org_backend_data_source", f.Source[0]},
	} {
		attempt, err := tx.Begin(t.Context())
		require.NoError(t, err)
		// Remove the unrelated source reference before testing secret credential restriction.
		if tc.constraint == "fk_environment_secrets_org_credential" {
			_, err = attempt.Exec(t.Context(), `UPDATE projects SET pipeline_backend_data_source_id=NULL WHERE organization_id=$1`, f.Org[0])
			require.NoError(t, err)
			_, err = attempt.Exec(t.Context(), `DELETE FROM data_sources WHERE organization_id=$1`, f.Org[0])
			require.NoError(t, err)
		}
		_, err = attempt.Exec(t.Context(), tc.query, tc.id)
		var pgError *pgconn.PgError
		require.ErrorAs(t, err, &pgError)
		require.Equal(t, "23001", pgError.Code)
		require.Equal(t, tc.constraint, pgError.ConstraintName)
		require.NoError(t, attempt.Rollback(context.Background()))
	}
	_, err = tx.Exec(t.Context(), `UPDATE projects SET pipeline_backend_data_source_id=NULL WHERE id=$1`, f.Project[0])
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `INSERT INTO projects(organization_id,slug,name) VALUES ($1,'no-backend','No backend')`, f.Org[0])
	require.NoError(t, err)
	var replacement int
	require.NoError(t, tx.QueryRow(t.Context(), `INSERT INTO credentials(organization_id,slug,kind,credential_type,status) VALUES ($1,'replacement','static_secret','environment_secret','active') RETURNING id`, f.Org[0]).Scan(&replacement))
	_, err = tx.Exec(t.Context(), `UPDATE environment_secrets SET credential_id=$1 WHERE id=$2`, replacement, f.Secret)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `UPDATE project_environments SET deleted_at=NOW() WHERE id=$1`, f.Enabled)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `INSERT INTO project_environments(organization_id,project_id,environment_id) VALUES ($1,$2,$3) ON CONFLICT(project_id,environment_id) DO UPDATE SET deleted_at=NULL`, f.Org[0], f.Project[0], f.Environment[0])
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `DELETE FROM projects WHERE id=$1`, f.Project[0])
	require.NoError(t, err)
	var count int
	require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM project_environments WHERE id=$1`, f.Enabled).Scan(&count))
	require.Zero(t, count)
	_, err = tx.Exec(t.Context(), `DELETE FROM environments WHERE id=$1`, f.Environment[0])
	require.NoError(t, err)
	require.NoError(t, tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM environment_secrets WHERE id=$1)+(SELECT count(*) FROM environment_runtime_variables WHERE id=$2)`, f.Secret, f.Variable).Scan(&count))
	require.Zero(t, count)
}
