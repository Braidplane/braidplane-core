package tenantfixture

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/postgres"
	migrate "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type EnvironmentBoundaryFixture struct {
	Org, Project, Environment, Credential, Source [2]int
	Enabled, Secret, Variable                     int
}

type ControlBoundaryFixture struct {
	EnvironmentBoundaryFixture
	Policy, Destination, Record [2]int
	Binding, Attempt            int
	Request                     string
}

func SeedControlBoundary(t testing.TB, tx pgx.Tx, currentSchema bool) ControlBoundaryFixture {
	t.Helper()
	ctx := t.Context()
	f := ControlBoundaryFixture{EnvironmentBoundaryFixture: SeedEnvironmentBoundary(t, tx, true), Request: uuid.New().String()}
	_, err := tx.Exec(ctx, `INSERT INTO project_environments(organization_id,project_id,environment_id) VALUES ($1,$2,$3)`, f.Org[1], f.Project[1], f.Environment[1])
	require.NoError(t, err)
	for i := range 2 {
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO alert_destinations(organization_id,slug,name,type) VALUES ($1,'destination','Destination','email') RETURNING id`, f.Org[i]).Scan(&f.Destination[i]))
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO alert_policies(organization_id,slug,name,scope,project_id,environment_id) VALUES ($1,'policy','Policy','environment',$2,$3) RETURNING id`, f.Org[i], f.Project[i], f.Environment[i]).Scan(&f.Policy[i]))
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO notification_records(organization_id,slug,project_id,environment_id,notification_type,severity,lifecycle_state,dedupe_fingerprint,source_entity_type,source_action) VALUES ($1,'record',$2,$3,'pipeline.run_failed','critical','open','fingerprint','pipeline_run','fail') RETURNING id`, f.Org[i], f.Project[i], f.Environment[i]).Scan(&f.Record[i]))
	}
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO alert_policy_destinations(organization_id,alert_policy_id,destination_id) VALUES ($1,$2,$3) RETURNING id`, f.Org[0], f.Policy[0], f.Destination[0]).Scan(&f.Binding))
	statement := `INSERT INTO alert_delivery_attempts(slug,notification_record_id,alert_policy_id,destination_id,destination_type,status,job_kind,job_queue,job_state,queued_at) VALUES ($1,$2,$3,$4,'email','queued','notification_delivery','notification_delivery','queued',NOW()) RETURNING id`
	args := []any{f.Request, f.Record[0], f.Policy[0], f.Destination[0]}
	if currentSchema {
		statement = `INSERT INTO alert_delivery_attempts(organization_id,slug,notification_record_id,alert_policy_id,destination_id,destination_type,status,job_kind,job_queue,job_state,queued_at) VALUES ($5,$1,$2,$3,$4,'email','queued','notification_delivery','notification_delivery','queued',NOW()) RETURNING id`
		args = append(args, f.Org[0])
	}
	require.NoError(t, tx.QueryRow(ctx, statement, args...).Scan(&f.Attempt))
	_, err = tx.Exec(ctx, `INSERT INTO execution_requests(id,organization_id,data_source_id,kind,request_ciphertext,deadline_at,expires_at) VALUES ($1,$2,$3,'query','encrypted',NOW()+INTERVAL '1 minute',NOW()+INTERVAL '1 hour')`, f.Request, f.Org[0], f.Source[0])
	require.NoError(t, err)
	return f
}

type ControlMismatch struct {
	Table, Column, Constraint string
	Parent, Child             any
}

func ControlMismatches(f ControlBoundaryFixture) []ControlMismatch {
	return []ControlMismatch{
		{"alert_policies", "project_id", "fk_alert_policies_org_project", f.Project[1], f.Policy[0]},
		{"alert_policies", "environment_id", "fk_alert_policies_org_environment", f.Environment[1], f.Policy[0]},
		{"alert_policy_destinations", "alert_policy_id", "fk_alert_policy_destinations_org_policy", f.Policy[1], f.Binding},
		{"alert_policy_destinations", "destination_id", "fk_alert_policy_destinations_org_destination", f.Destination[1], f.Binding},
		{"notification_records", "project_id", "fk_notification_records_org_project", f.Project[1], f.Record[0]},
		{"notification_records", "environment_id", "fk_notification_records_org_environment", f.Environment[1], f.Record[0]},
		{"alert_delivery_attempts", "notification_record_id", "fk_alert_delivery_attempts_org_record", f.Record[1], f.Attempt},
		{"alert_delivery_attempts", "alert_policy_id", "fk_alert_delivery_attempts_org_policy", f.Policy[1], f.Attempt},
		{"alert_delivery_attempts", "destination_id", "fk_alert_delivery_attempts_org_destination", f.Destination[1], f.Attempt},
		{"execution_requests", "data_source_id", "fk_execution_requests_org_data_source", f.Source[1], f.Request},
	}
}

func SeedEnvironmentBoundary(t testing.TB, tx pgx.Tx, currentSchema bool) EnvironmentBoundaryFixture {
	t.Helper()
	ctx := t.Context()
	f := EnvironmentBoundaryFixture{}
	prefix := fmt.Sprintf("tenant-fixture-%d", time.Now().UnixNano())
	for i := range 2 {
		name := fmt.Sprintf("%s-%d", prefix, i)
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO organizations(slug,name) VALUES ($1,$1) RETURNING id`, name).Scan(&f.Org[i]))
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO credentials(organization_id,slug,kind,credential_type,status) VALUES ($1,'secret','static_secret','environment_secret','active') RETURNING id`, f.Org[i]).Scan(&f.Credential[i]))
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO data_sources(organization_id,credential_id,slug,name,type,config) VALUES ($1,$2,'source','Source','postgres','{}') RETURNING id`, f.Org[i], f.Credential[i]).Scan(&f.Source[i]))
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO projects(organization_id,slug,name,pipeline_backend_data_source_id) VALUES ($1,'project','Project',$2) RETURNING id`, f.Org[i], f.Source[i]).Scan(&f.Project[i]))
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO environments(organization_id,slug,name) VALUES ($1,'dev','Dev') RETURNING id`, f.Org[i]).Scan(&f.Environment[i]))
	}
	enabledSQL := `INSERT INTO project_environments(project_id,environment_id) VALUES ($1,$2) RETURNING id`
	// #nosec G101 -- Synthetic environment-secret lookup key for disposable fixtures.
	secretSQL := `INSERT INTO environment_secrets(environment_id,credential_id,key) VALUES ($1,$2,'SECRET') RETURNING id`
	enabledArgs := []any{f.Project[0], f.Environment[0]}
	secretArgs := []any{f.Environment[0], f.Credential[0]}
	if currentSchema {
		enabledSQL = `INSERT INTO project_environments(organization_id,project_id,environment_id) VALUES ($3,$1,$2) RETURNING id`
		// #nosec G101 -- Synthetic environment-secret lookup key for disposable fixtures.
		secretSQL = `INSERT INTO environment_secrets(organization_id,environment_id,credential_id,key) VALUES ($3,$1,$2,'SECRET') RETURNING id`
		enabledArgs = append(enabledArgs, f.Org[0])
		secretArgs = append(secretArgs, f.Org[0])
	}
	require.NoError(t, tx.QueryRow(ctx, enabledSQL, enabledArgs...).Scan(&f.Enabled))
	require.NoError(t, tx.QueryRow(ctx, secretSQL, secretArgs...).Scan(&f.Secret))
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO environment_runtime_variables(organization_id,environment_id,key,value_type,value_text) VALUES ($1,$2,'VALUE','string','value') RETURNING id`, f.Org[0], f.Environment[0]).Scan(&f.Variable))
	return f
}

type PipelineBoundaryFixture struct {
	EnvironmentBoundaryFixture
	Projects, EnabledScopes, Specs, Versions, Refs, Authorities        [4]int
	Members                                                            [2]int
	Reports                                                            [4]string
	Binding, Run, Deployment, Validation, Reference, Draft, Checkpoint int
}

type PipelineMismatch struct {
	Table, Column, Constraint string
	Parent                    any
	Child                     int
}

func PipelineMismatches(f PipelineBoundaryFixture, other int) []PipelineMismatch {
	tests := []PipelineMismatch{
		{"data_source_bindings", "data_source_ref_id", "fk_data_source_bindings_pipeline_ref", f.Refs[other], f.Binding},
		{"pipeline_runs", "pipeline_version_id", "fk_pipeline_runs_pipeline_version", f.Versions[other], f.Run},
		{"pipeline_deployments", "current_version_id", "fk_pipeline_deployments_current_version", f.Versions[other], f.Deployment},
		{"pipeline_deployments", "previous_version_id", "fk_pipeline_deployments_previous_version", f.Versions[other], f.Deployment},
		{"pipeline_validation_results", "pipeline_id", "fk_pipeline_validation_results_pipeline", f.Specs[other], f.Validation},
		{"pipeline_validation_results", "pipeline_version_id", "fk_pipeline_validation_results_version", f.Versions[other], f.Validation},
		{"pipeline_validation_results", "impact_report_slug", "fk_pipeline_validation_results_report", f.Reports[other], f.Validation},
		{"pipeline_specs", "current_version_id", "fk_pipeline_specs_current_version", f.Versions[other], f.Specs[0]},
		{"pipeline_references", "current_version_id", "fk_pipeline_references_current_version", f.Versions[other], f.Reference},
		{"pipeline_drafts", "based_on_version_id", "fk_pipeline_drafts_based_on_version", f.Versions[other], f.Draft},
		{"pipeline_incremental_checkpoints", "pipeline_version_id", "fk_pipeline_incremental_checkpoints_version", f.Versions[other], f.Checkpoint},
	}
	if other > 1 {
		tests = append(tests, PipelineMismatch{"pipeline_drafts", "proposal_authority_id", "fk_pipeline_drafts_project_authority", f.Authorities[other], f.Draft})
	}
	if other == 3 {
		tests = append(tests,
			PipelineMismatch{"data_source_bindings", "data_source_id", "fk_data_source_bindings_org_source", f.Source[1], f.Binding},
			PipelineMismatch{"pipeline_drafts", "member_id", "fk_pipeline_drafts_org_member", f.Members[1], f.Draft},
		)
	}
	return tests
}

func SeedPipelineBoundary(t testing.TB, tx pgx.Tx, current bool) PipelineBoundaryFixture {
	t.Helper()
	ctx := t.Context()
	f := PipelineBoundaryFixture{EnvironmentBoundaryFixture: SeedEnvironmentBoundary(t, tx, true)}
	f.Projects[0], f.Projects[1], f.Projects[3] = f.Project[0], f.Project[0], f.Project[1]
	f.EnabledScopes[0], f.EnabledScopes[1] = f.Enabled, f.Enabled
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO projects(organization_id,slug,name) VALUES ($1,'second','Second') RETURNING id`, f.Org[0]).Scan(&f.Projects[2]))
	for _, i := range []int{2, 3} {
		orgIndex := i / 3
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO project_environments(organization_id,project_id,environment_id) VALUES ($1,$2,$3) RETURNING id`, f.Org[orgIndex], f.Projects[i], f.Environment[orgIndex]).Scan(&f.EnabledScopes[i]))
	}
	for i := range 2 {
		var user, role int
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO users(slug,email,role) VALUES ($1::text,$1::text||'@example.test','user') RETURNING id`, fmt.Sprintf("member-%d-%d", f.Org[i], i)).Scan(&user))
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO roles(organization_id,slug,name) VALUES ($1,'member','Member') RETURNING id`, f.Org[i]).Scan(&role))
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO organization_members(organization_id,user_id,role_id) VALUES ($1,$2,$3) RETURNING id`, f.Org[i], user, role).Scan(&f.Members[i]))
	}
	for i := range 4 {
		pipeline := fmt.Sprintf("pipeline-%d", i)
		orgIndex := i / 3
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO pipeline_specs(project_id,slug,spec_jsonb,source_path,source_ref,last_applied_commit) VALUES ($1,$2,'{}','pipelines/test.json','main','commit') RETURNING id`, f.Projects[i], pipeline).Scan(&f.Specs[i]))
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO pipeline_versions(project_id,pipeline_slug,spec_jsonb) VALUES ($1,$2,'{}') RETURNING id`, f.Projects[i], pipeline).Scan(&f.Versions[i]))
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO data_source_refs(pipeline_spec_id,slug,name,data_source_type,required_capabilities) VALUES ($1,'source','Source','postgres','{}') RETURNING id`, f.Specs[i]).Scan(&f.Refs[i]))
		f.Reports[i] = fmt.Sprintf("report-%d", f.Specs[i])
		_, err := tx.Exec(ctx, `INSERT INTO drift_impact_reports(pipeline_spec_id,slug,report_type) VALUES ($1,$2,'pipeline')`, f.Specs[i], f.Reports[i])
		require.NoError(t, err)
		if i == 1 {
			f.Authorities[i] = f.Authorities[0]
			continue
		}
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO pipeline_proposal_authorities(organization_id,project_id,data_source_id,credential_id,publication_mode,provider_host,provider_account_id,provider_repository_id,provider_credential_id,executing_principal_id,executing_principal_display,authority_source) VALUES ($1,$2,$3,$4,'branch','example.test','account','repo','credential','principal','Principal','fixture') RETURNING id`, f.Org[orgIndex], f.Projects[i], f.Source[orgIndex], f.Credential[orgIndex]).Scan(&f.Authorities[i]))
	}
	_, err := tx.Exec(ctx, `UPDATE pipeline_specs SET current_version_id=$1 WHERE id=$2`, f.Versions[0], f.Specs[0])
	require.NoError(t, err)
	// The added scope columns are populated from the same canonical parents as production writes.
	statements := []struct {
		legacy, scoped string
		id             *int
	}{
		{`INSERT INTO data_source_bindings(project_environment_id,data_source_ref_id,data_source_id) VALUES (@environment,@ref,@source) RETURNING id`, `INSERT INTO data_source_bindings(project_environment_id,data_source_ref_id,data_source_id,project_id,organization_id,pipeline_spec_id) VALUES (@environment,@ref,@source,@project,@organization,@pipeline) RETURNING id`, &f.Binding},
		{`INSERT INTO pipeline_runs(project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline) VALUES (@environment,@version,'hash','v1','pipeline-0','run','dev','success',1,'step','{}') RETURNING id`, `INSERT INTO pipeline_runs(project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,project_id) VALUES (@environment,@version,'hash','v1','pipeline-0','run','dev','success',1,'step','{}',@project) RETURNING id`, &f.Run},
		{`INSERT INTO pipeline_deployments(project_environment_id,pipeline_slug,current_version_id,previous_version_id,deployed_by_actor_type) VALUES (@environment,'pipeline-0',@version,@version,'member') RETURNING id`, `INSERT INTO pipeline_deployments(project_environment_id,pipeline_slug,current_version_id,previous_version_id,deployed_by_actor_type,project_id) VALUES (@environment,'pipeline-0',@version,@version,'member',@project) RETURNING id`, &f.Deployment},
		{`INSERT INTO pipeline_validation_results(project_environment_id,pipeline_id,pipeline_version_id,validation_slug,pipeline_slug,source,valid,impact_report_slug) VALUES (@environment,@pipeline,@version,'validation','pipeline-0','manual',true,@report) RETURNING id`, `INSERT INTO pipeline_validation_results(project_environment_id,pipeline_id,pipeline_version_id,validation_slug,pipeline_slug,source,valid,impact_report_slug,project_id,impact_pipeline_id) VALUES (@environment,@pipeline,@version,'validation','pipeline-0','manual',true,@report,@project,@pipeline) RETURNING id`, &f.Validation},
		{`INSERT INTO pipeline_references(project_id,slug,name,current_version_id) VALUES (@project,'pipeline-0','Pipeline',@version) RETURNING id`, `INSERT INTO pipeline_references(project_id,slug,name,current_version_id) VALUES (@project,'pipeline-0','Pipeline',@version) RETURNING id`, &f.Reference},
		{`INSERT INTO pipeline_drafts(project_id,pipeline_slug,member_id,based_on_version_id,spec_jsonb,proposal_authority_id) VALUES (@project,'pipeline-0',@member,@version,'{}',@authority) RETURNING id`, `INSERT INTO pipeline_drafts(project_id,pipeline_slug,member_id,based_on_version_id,spec_jsonb,proposal_authority_id,organization_id) VALUES (@project,'pipeline-0',@member,@version,'{}',@authority,@organization) RETURNING id`, &f.Draft},
		{`INSERT INTO pipeline_incremental_checkpoints(project_id,project_environment_id,pipeline_slug,pipeline_version_id,source_step_slug,checkpoint_kind,data_source_slug,operation_type,operation_identity,commit_status,observed_run_slug,observed_at) VALUES (@project,@environment,'pipeline-0',@version,'step','cursor','source','query','identity','observed','run',NOW()) RETURNING id`, `INSERT INTO pipeline_incremental_checkpoints(project_id,project_environment_id,pipeline_slug,pipeline_version_id,source_step_slug,checkpoint_kind,data_source_slug,operation_type,operation_identity,commit_status,observed_run_slug,observed_at) VALUES (@project,@environment,'pipeline-0',@version,'step','cursor','source','query','identity','observed','run',NOW()) RETURNING id`, &f.Checkpoint},
	}
	args := pgx.NamedArgs{"environment": f.Enabled, "ref": f.Refs[0], "source": f.Source[0], "project": f.Projects[0], "organization": f.Org[0], "pipeline": f.Specs[0], "version": f.Versions[0], "report": f.Reports[0], "member": f.Members[0], "authority": f.Authorities[0]}
	for _, statement := range statements {
		query := statement.legacy
		if current {
			query = statement.scoped
		}
		require.NoError(t, tx.QueryRow(ctx, query, args).Scan(statement.id))
	}
	return f
}

func OpenDatabase(t testing.TB) *pgxpool.Pool {
	t.Helper()
	admin := e2edb.OpenPostgresPool(t, "tenant migration")
	database := "tenant_migration_" + uuid.New().String()
	identifier := pgx.Identifier{database}.Sanitize()
	_, err := admin.Exec(t.Context(), "CREATE DATABASE "+identifier)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+identifier)
		require.NoError(t, err)
	})
	config := admin.Config().Copy()
	config.ConnConfig.Database = database
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func OpenAt(t testing.TB, catalog fs.FS, ledger string, version uint) (*pgxpool.Pool, *migrate.Migrate) {
	t.Helper()
	pool := OpenDatabase(t)
	databaseURL, err := url.Parse(e2edb.PostgresConfigFromEnv().DatabaseURL)
	require.NoError(t, err)
	databaseURL.Path = "/" + pool.Config().ConnConfig.Database
	selectedURL, err := postgres.MigrationDatabaseURL(databaseURL.String(), ledger)
	require.NoError(t, err)

	source, err := iofs.New(catalog, ".")
	require.NoError(t, err)
	runner, err := migrate.NewWithSourceInstance("iofs", source, selectedURL)
	require.NoError(t, err)
	t.Cleanup(func() {
		sourceErr, databaseErr := runner.Close()
		require.NoError(t, sourceErr)
		require.NoError(t, databaseErr)
	})
	require.NoError(t, runner.Migrate(version))
	return pool, runner
}
