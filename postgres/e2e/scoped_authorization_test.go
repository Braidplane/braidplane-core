package e2e

import (
	"context"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/authorization"
	"github.com/Braidplane/braidplane-core/postgres/pipelinewebhooks"
)

// Upgrade an installation that already has organization-wide user and key roles.
func TestScopedAuthorizationMigrationAndRevocation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	admin := e2edb.OpenPostgresPool(t, "authorization upgrade")
	ctx := t.Context()
	db := fmt.Sprintf("authorization_upgrade_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{db}.Sanitize()
	_, err := admin.Exec(ctx, "CREATE DATABASE "+identifier)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		require.NoError(t, err)
	})
	cfg := admin.Config().ConnConfig.Copy()
	cfg.Database = db
	conn, err := pgx.ConnectConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close(context.Background())) })
	exec := func(sql string) {
		t.Helper()
		_, err := conn.Exec(ctx, sql)
		require.NoError(t, err)
	}
	catalog, err := postgres.EmbeddedMigrationCatalog()
	require.NoError(t, err)
	files, err := fs.Glob(catalog, "*.up.sql")
	require.NoError(t, err)
	apply := func(name string) {
		t.Helper()
		data, err := fs.ReadFile(catalog, name)
		require.NoError(t, err)
		exec(string(data))
	}
	for _, name := range files {
		if name >= "000009" {
			break
		}
		apply(name)
	}
	exec(`
INSERT INTO organizations (id, name, slug) VALUES (10,'Example','example'),(20,'Other','other');
INSERT INTO projects (id, organization_id, name, slug) VALUES (11,10,'Primary','primary'),(21,20,'Other','other');
INSERT INTO environments (id, organization_id, name, slug) VALUES (12,10,'Production','prod');
INSERT INTO project_environments (organization_id, project_id, environment_id) VALUES (10,11,12);
INSERT INTO roles (id, organization_id, name, slug) VALUES (13,10,'Broad','broad'),(14,10,'Scoped','scoped'),(15,10,'Empty','empty');
INSERT INTO permissions (id, resource, action) VALUES (16,'*','*'),(17,'projects','read');
INSERT INTO role_permissions (role_id, permission_id) VALUES (13,16),(14,17);
INSERT INTO users (id,slug,email,role,verified_at) VALUES (18,'member','member@example.test','user',now());
INSERT INTO organization_members (id,organization_id,user_id,role_id) VALUES (19,10,18,13);
INSERT INTO api_keys (id,organization_id,name,slug,token_hash,token_prefix,role_id)
VALUES (30,10,'Key','machine','synthetic','example',13);
`)
	apply("000009_scoped_authorization.up.sql")
	project := model.AuthorizationTarget{Kind: "projects", Slug: "primary"}
	read := func(org, user, key slug.Slug, target model.AuthorizationTarget) model.AuthorizationFacts {
		t.Helper()
		facts, err := authorization.Resolve(ctx, conn, org, user, key, target)
		require.NoError(t, err)
		return facts
	}
	broad := model.AuthorizationBinding{Scope: "organization", ScopeID: "10", Permissions: []string{"*:*"}}
	projectGrant := model.AuthorizationBinding{Scope: "project", ScopeID: "11", Permissions: []string{"projects:read"}}
	resourceGrant := model.AuthorizationBinding{Scope: "resource", ScopeID: "projects:11", Permissions: []string{"projects:read"}}
	envGrant := model.AuthorizationBinding{Scope: "environment", ScopeID: "12", Permissions: []string{"projects:read"}}
	subject := func() model.AuthorizationFacts { return read("example", "member", "", project) }
	machine := func() model.AuthorizationFacts { return read("example", "", "machine", project) }
	initial := subject()
	require.True(t, initial.Valid)
	require.Equal(t, "projects:11", initial.TargetID)
	require.Equal(t, []model.AuthorizationBinding{broad}, initial.Bindings)
	require.Equal(t, "member:19", initial.PrincipalID)
	machineInitial := machine()
	require.Equal(t, "api_key:30", machineInitial.PrincipalID)
	require.Equal(t, slug.Slug("broad"), machineInitial.APIKeyRoleSlug)
	require.Equal(t, initial.Bindings, machineInitial.Bindings)
	keyTarget := model.AuthorizationTarget{Kind: "api_keys", Slug: "machine"}
	require.False(t, read("example", "", "machine", keyTarget).CanManageAPIKeys)
	exec(`UPDATE api_keys SET can_manage_api_keys=true WHERE id=30;
	INSERT INTO api_keys (organization_id,name,slug,token_hash,token_prefix,role_id)
		VALUES (10,'Other role','other-role','other-synthetic','example',14)`)
	require.True(t, read("example", "", "machine", keyTarget).CanManageAPIKeys)
	keyTarget.Slug = "other-role"
	require.False(t, read("example", "", "machine", keyTarget).CanManageAPIKeys)
	exec("UPDATE api_keys SET can_manage_api_keys=false WHERE id=30")
	require.False(t, read("other", "member", "", model.AuthorizationTarget{}).Valid)
	require.False(t, read("example", "member", "machine", project).Valid)
	require.False(t, read("example", "member", "", model.AuthorizationTarget{Kind: "unmapped", Slug: "primary"}).Valid)
	_, err = conn.Exec(ctx, `INSERT INTO authorization_scoped_grants (organization_id,member_id,role_id,scope,target_kind,target_id)
		VALUES (10,19,14,'project','projects',21)`)
	require.ErrorContains(t, err, "outside the organization")
	exec(`INSERT INTO authorization_scoped_grants (organization_id,member_id,role_id,scope,target_kind,target_id) VALUES
		(10,19,14,'project','projects',11),(10,19,14,'resource','projects',11);
	INSERT INTO authorization_scoped_grants (organization_id,api_key_id,role_id,scope,target_kind,target_id)
		VALUES (10,30,14,'environment','environments',12)`)
	require.ElementsMatch(t, []model.AuthorizationBinding{broad, projectGrant, resourceGrant}, subject().Bindings)
	require.Equal(t, []model.AuthorizationBinding{broad}, machine().Bindings)
	association := model.AuthorizationTarget{Kind: "project_environments", ProjectSlug: "primary", EnvironmentSlug: "prod"}
	contextFacts := read("example", "", "machine", association)
	require.True(t, contextFacts.Valid)
	require.Equal(t, "11", contextFacts.ProjectID)
	require.Equal(t, "12", contextFacts.EnvironmentID)
	require.ElementsMatch(t, []model.AuthorizationBinding{broad, envGrant}, contextFacts.Bindings)
	exec(`INSERT INTO credentials (organization_id,slug,kind,credential_type,status,encrypted_payload)
        VALUES (10,'source-credential','static_secret','git','active','synthetic');
    INSERT INTO data_sources (organization_id,credential_id,slug,name,type,config)
        SELECT 10,id,'shared','Shared','git','{}' FROM credentials WHERE slug='source-credential';
    UPDATE projects SET pipeline_backend_data_source_id=(SELECT id FROM data_sources WHERE slug='shared'),
        pipeline_backend_repo='synthetic',pipeline_backend_tracked_branch='main' WHERE id=11;
    INSERT INTO queries (data_source_id,slug,name,data)
        SELECT id,'stored','Stored','{}' FROM data_sources WHERE slug='shared';
    INSERT INTO pipeline_specs (project_id,slug,spec_jsonb,source_path,source_ref,last_applied_commit)
        VALUES (11,'daily','{}','daily.json','main','one');
    INSERT INTO pipeline_versions (project_id,pipeline_slug,spec_hash) VALUES (11,'daily','one'),(11,'daily','two');
    INSERT INTO pipeline_deployments (project_id,project_environment_id,pipeline_slug,current_version_id,deployed_by_actor_type)
        SELECT pe.project_id,pe.id,'daily',v.id,'system' FROM project_environments pe
        JOIN pipeline_versions v ON v.project_id=pe.project_id WHERE v.spec_hash='one'`)
	source := model.AuthorizationTarget{Kind: "datasources", Slug: "shared"}
	require.True(t, read("example", "member", "", source).Valid)
	source.ProjectSlug = "primary"
	require.False(t, read("example", "member", "", source).Valid)
	query := model.AuthorizationTarget{Kind: "queries", Slug: "stored", DataSourceSlug: "shared"}
	require.True(t, read("example", "member", "", query).Valid)
	query.DataSourceSlug = "different"
	require.False(t, read("example", "member", "", query).Valid)
	logical := model.AuthorizationTarget{Kind: "pipelines", Slug: "daily", ProjectSlug: "primary"}
	pipe := read("example", "", "machine", logical)
	require.True(t, pipe.Valid)
	require.Equal(t, "11", pipe.ProjectID)
	require.Empty(t, pipe.EnvironmentID)
	deployed := model.AuthorizationTarget{Kind: "deployed_pipelines", Slug: "daily", ProjectSlug: "primary", EnvironmentSlug: "prod"}
	deployment := read("example", "", "machine", deployed)
	require.True(t, deployment.Valid)
	require.Equal(t, pipe.TargetID, deployment.TargetID)
	require.Equal(t, "12", deployment.EnvironmentID)
	exec("UPDATE pipeline_deployments SET current_version_id=(SELECT id FROM pipeline_versions WHERE spec_hash='two')")
	require.Equal(t, deployment, read("example", "", "machine", deployed))
	exec(`DELETE FROM pipeline_specs WHERE project_id=11 AND slug='daily';
	INSERT INTO pipeline_specs (project_id,slug,spec_jsonb,source_path,source_ref,last_applied_commit)
		VALUES (11,'daily','{}','daily.json','main','replacement')`)
	require.False(t, read("example", "", "machine", deployed).Valid)
	rebuiltPipeline := read("example", "", "machine", logical)
	require.True(t, rebuiltPipeline.Valid)
	require.NotEqual(t, pipe.TargetID, rebuiltPipeline.TargetID)
	// The persisted trigger remains the owner when it is retargeted. A delivery never inherits from the new pipeline.
	exec(`INSERT INTO pipeline_triggers (project_id,project_environment_id,pipeline_id,pipeline_slug,slug,name,trigger_type,version_policy,receiver_id,webhook_verification_strategy,webhook_input_mappings,created_by_actor_type,updated_by_actor_type)
		SELECT 11,pe.id,s.id,s.slug,'hook','Hook','webhook','latest_deployed','receiver','hmac_sha256','[]','system','system'
		FROM project_environments pe, pipeline_specs s WHERE pe.project_id=11 AND s.project_id=11 AND s.slug='daily';
	INSERT INTO pipeline_webhook_deliveries (organization_id,project_id,project_environment_id,pipeline_trigger_id,receiver_id,delivery_id,payload_digest,payload_size_bytes,key_version,trigger_revision,pipeline_slug,version_policy,run_slug,status,received_at)
		SELECT 10,11,t.project_environment_id,t.id,'receiver','attempt',repeat('a',64),1,1,t.revision,t.pipeline_slug,'latest_deployed','run','failed',now()
		FROM pipeline_triggers t WHERE t.project_id=11 AND t.slug='hook'`)
	delivery := model.AuthorizationTarget{Kind: "webhook_deliveries", Slug: "attempt", ProjectSlug: "primary", EnvironmentSlug: "prod", ParentSlug: "hook"}
	originalDelivery := read("example", "member", "", delivery)
	require.True(t, originalDelivery.Valid)
	require.Contains(t, originalDelivery.AncestorIDs, rebuiltPipeline.TargetID)
	exec(`INSERT INTO pipeline_specs (project_id, slug, spec_jsonb, source_path, source_ref, last_applied_commit)
		VALUES (11,'other-pipeline','{}','other.json','main','another');
	UPDATE pipeline_triggers t SET pipeline_id=s.id,pipeline_slug=s.slug,revision=t.revision+1
		FROM pipeline_specs s WHERE s.project_id=11 AND s.slug='other-pipeline' AND t.slug='hook'`)
	changedDelivery := read("example", "member", "", delivery)
	require.True(t, changedDelivery.Valid)
	require.Equal(t, originalDelivery.TargetID, changedDelivery.TargetID)
	trigger := read("example", "member", "", model.AuthorizationTarget{Kind: "pipeline_triggers", Slug: "hook", ProjectSlug: "primary", EnvironmentSlug: "prod"})
	require.Equal(t, []string{trigger.TargetID}, changedDelivery.AncestorIDs)
	exec(`INSERT INTO pipeline_webhook_deliveries (organization_id,project_id,project_environment_id,pipeline_trigger_id,receiver_id,delivery_id,payload_digest,payload_size_bytes,key_version,trigger_revision,pipeline_slug,version_policy,run_slug,status,received_at)
		SELECT 10,11,t.project_environment_id,t.id,'receiver','current',repeat('a',64),1,1,t.revision,t.pipeline_slug,'latest_deployed','current-run','failed',now()
		FROM pipeline_triggers t WHERE t.project_id=11 AND t.slug='hook'`)
	req := model.ListRequest{Limit: 1, Order: "asc"}
	repo := pipelinewebhooks.New()
	for _, fullHistory := range []bool{false, true} {
		rows, err := repo.ListForTrigger(ctx, conn, "example", "primary", "prod", "hook", fullHistory, req)
		require.NoError(t, err)
		page, more := model.FinalizePage(rows, req)
		require.Len(t, page, 1)
		require.Equal(t, fullHistory, more)
		if fullHistory {
			require.Equal(t, "attempt", page[0].DeliveryID)
		} else {
			require.Equal(t, "current", page[0].DeliveryID)
		}
	}
	firstDeploy := deployed
	firstDeploy.Kind = "deployment_targets"
	require.Equal(t, rebuiltPipeline.TargetID, read("example", "", "machine", firstDeploy).TargetID)
	exec(`INSERT INTO data_source_refs (pipeline_spec_id,slug,name,data_source_type,required_capabilities)
		SELECT id,'input','Input','git','{}' FROM pipeline_specs WHERE slug='daily';
	INSERT INTO authorization_scoped_grants (organization_id,member_id,role_id,scope,target_kind,target_id)
		SELECT 10,19,14,'resource','data_source_refs',id FROM data_source_refs WHERE slug='input'`)
	binding := model.AuthorizationTarget{Kind: "data_source_bindings_upsert", Slug: "input", ParentSlug: "daily", ProjectSlug: "primary", EnvironmentSlug: "prod"}
	newBinding := read("example", "member", "", binding)
	require.True(t, newBinding.Valid)
	require.Contains(t, newBinding.AncestorIDs, rebuiltPipeline.TargetID)
	exec(`INSERT INTO drift_impact_reports (data_source_id,slug,report_type)
		SELECT id,'source-report','source_drift' FROM data_sources WHERE slug='shared'`)
	report := model.AuthorizationTarget{Kind: "drift_impact_reports", Slug: "source-report", ProjectSlug: "primary"}
	require.False(t, read("example", "member", "", report).Valid)
	exec(`INSERT INTO data_source_bindings (organization_id,project_id,project_environment_id,pipeline_spec_id,data_source_ref_id,data_source_id)
		SELECT 10,11,pe.id,r.pipeline_spec_id,r.id,d.id FROM data_source_refs r, project_environments pe, data_sources d
		WHERE r.slug='input' AND pe.project_id=11 AND d.slug='shared'`)
	storedBinding := read("example", "member", "", binding)
	require.True(t, storedBinding.Valid)
	require.NotEqual(t, newBinding.TargetID, storedBinding.TargetID)
	require.Contains(t, storedBinding.AncestorIDs, newBinding.TargetID)
	require.Contains(t, storedBinding.Bindings, model.AuthorizationBinding{Scope: "resource", ScopeID: newBinding.TargetID, Permissions: []string{"projects:read"}})
	source.ProjectSlug = ""
	require.Equal(t, []model.AuthorizationBinding{broad}, read("example", "member", "", source).Bindings)
	sourceFacts := read("example", "member", "", source)
	reportFacts := read("example", "member", "", report)
	require.True(t, reportFacts.Valid)
	require.Empty(t, reportFacts.ProjectID)
	require.Empty(t, reportFacts.EnvironmentID)
	require.Equal(t, []string{sourceFacts.TargetID}, reportFacts.AncestorIDs)
	require.Equal(t, []model.AuthorizationBinding{broad}, reportFacts.Bindings)
	exec("UPDATE project_environments SET deleted_at=now() WHERE project_id=11")
	require.False(t, read("example", "member", "", report).Valid)
	exec("UPDATE project_environments SET deleted_at=NULL WHERE project_id=11")
	binding.ParentSlug = "other"
	require.False(t, read("example", "member", "", binding).Valid)
	// Lookup keys never carry authority beyond a purged object's ID.
	definition := model.AuthorizationTarget{Kind: "runtime_variable_definitions_upsert", Slug: "REGION"}
	missing := read("example", "member", "", definition)
	require.True(t, missing.Valid)
	require.Empty(t, missing.TargetID)
	exec(`INSERT INTO runtime_variable_definitions (organization_id,key,value_type) VALUES (10,'REGION','text');
	INSERT INTO authorization_scoped_grants (organization_id,member_id,role_id,scope,target_kind,target_id)
		SELECT 10,19,14,'resource','runtime_variable_definitions',authorization_id
		FROM runtime_variable_definitions WHERE key='REGION'`)
	previous := read("example", "member", "", definition)
	require.NotEmpty(t, previous.TargetID)
	require.Contains(t, previous.Bindings, model.AuthorizationBinding{
		Scope: "resource", ScopeID: previous.TargetID, Permissions: []string{"projects:read"},
	})
	exec(`DELETE FROM runtime_variable_definitions WHERE key='REGION';
	INSERT INTO runtime_variable_definitions (organization_id,key,value_type) VALUES (10,'REGION','text')`)
	renewed := read("example", "member", "", definition)
	require.True(t, renewed.Valid)
	require.NotEqual(t, previous.TargetID, renewed.TargetID)
	require.Equal(t, []model.AuthorizationBinding{broad}, renewed.Bindings)
	exec("DELETE FROM authorization_scoped_grants WHERE target_kind='runtime_variable_definitions'")
	secret := model.AuthorizationTarget{Kind: "environment_secrets_upsert", Slug: "SERVICE_KEY", EnvironmentSlug: "prod"}
	emptySecret := read("example", "member", "", secret)
	require.True(t, emptySecret.Valid)
	require.Equal(t, "environments:12", emptySecret.TargetID)
	exec(`INSERT INTO environment_secrets (organization_id, environment_id, key, credential_id)
		SELECT 10,12,'SERVICE_KEY',id FROM credentials WHERE slug='source-credential'`)
	resolved := read("example", "member", "", secret)
	require.True(t, resolved.Valid)
	require.NotEqual(t, emptySecret.TargetID, resolved.TargetID)
	require.Contains(t, resolved.AncestorIDs, emptySecret.TargetID)
	secret.EnvironmentSlug = "other"
	require.False(t, read("example", "member", "", secret).Valid)
	exec(`INSERT INTO query_executions (query_id, slug, status, result)
		SELECT id,'attempt','completed','{}' FROM queries WHERE slug='stored'`)
	attempt := model.AuthorizationTarget{Kind: "query_executions", Slug: "attempt", DataSourceSlug: "shared", ParentSlug: "stored"}
	actual := read("example", "member", "", attempt)
	require.True(t, actual.Valid)
	query.DataSourceSlug = "shared"
	require.Contains(t, actual.AncestorIDs, read("example", "member", "", query).TargetID)
	attempt.ParentSlug = "different"
	require.False(t, read("example", "member", "", attempt).Valid)
	association.ProjectSlug = "other"
	require.False(t, read("example", "", "machine", association).Valid)
	exec(`UPDATE organization_members SET role_id=15 WHERE id=19;
	UPDATE api_keys SET role_id=15 WHERE id=30`)
	require.ElementsMatch(t, []model.AuthorizationBinding{projectGrant, resourceGrant}, subject().Bindings)
	require.Empty(t, machine().Bindings)
	require.Equal(t, slug.Slug("empty"), machine().APIKeyRoleSlug)
	machineEnv := func() model.AuthorizationFacts {
		return read("example", "", "machine", model.AuthorizationTarget{Kind: "environments", Slug: "prod"})
	}
	require.Equal(t, []model.AuthorizationBinding{envGrant}, machineEnv().Bindings)
	exec(`DELETE FROM authorization_scoped_grants WHERE scope='project';
	DELETE FROM projects WHERE id=11;
	INSERT INTO projects (id,organization_id,name,slug) VALUES (31,10,'Replacement','primary')`)
	replacement := subject()
	require.Equal(t, "projects:31", replacement.TargetID)
	require.Empty(t, replacement.Bindings)
	exec("DELETE FROM role_permissions WHERE role_id=14")
	require.Empty(t, machineEnv().Bindings)
	exec("INSERT INTO role_permissions (role_id, permission_id) VALUES (14,17)")
	require.Equal(t, []model.AuthorizationBinding{envGrant}, machineEnv().Bindings)
	exec("UPDATE roles SET deleted_at=now() WHERE id=14")
	require.Empty(t, machineEnv().Bindings)
	// The key can expire after the admission statement starts, while a worker waits for locks.
	exec(`DO $$ BEGIN
		UPDATE api_keys SET expires_at=clock_timestamp() WHERE id=30;
		IF COALESCE((authorization_facts('example','','machine','environments','prod','','','')->>'valid')::boolean, false) THEN
			RAISE EXCEPTION 'expired key authorized after statement start';
		END IF;
	END $$`)
	require.False(t, machineEnv().Valid)
	exec("UPDATE api_keys SET expires_at=NULL WHERE id=30")
	require.True(t, machineEnv().Valid)
	exec("DELETE FROM organization_members WHERE id=19; UPDATE api_keys SET revoked_at=now() WHERE id=30")
	require.False(t, subject().Valid)
	require.False(t, machine().Valid)
}
