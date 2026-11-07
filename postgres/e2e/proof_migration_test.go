package e2e

import (
	"os"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/stretchr/testify/require"
)

func TestProofMigrationPreservesManualRepositorySources(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	pool := e2edb.OpenIsolatedPostgresPool(t, "manual Git source migration")
	// Reconstruct the pre-transition shape in this empty, isolated test database.
	_, err := pool.Exec(t.Context(), `
 DROP VIEW active_proof_candidate_mappings CASCADE;
 DROP FUNCTION invalidate_data_source_proof() CASCADE;
 ALTER TABLE data_sources DROP COLUMN organization_gitlab_connection_id CASCADE, DROP COLUMN proof_enabled CASCADE, DROP COLUMN proof_checks_enabled CASCADE;
 ALTER TABLE credential_oauth_installs DROP COLUMN organization_gitlab_connection_id CASCADE, ALTER COLUMN data_source_id SET NOT NULL;
 DROP TABLE organization_gitlab_connections CASCADE;
 ALTER TABLE proof_change_candidate_mappings DROP COLUMN disabled_at CASCADE;
 `)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `
INSERT INTO organizations(name,slug) VALUES ('Migration fixture','migration-fixture');
INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload)
SELECT id,'manual-grant','static_secret','git','active',decode('01020304','hex') FROM organizations WHERE slug='migration-fixture';
INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active,read_only)
SELECT c.organization_id,c.id,'Manual GitHub','github','github','{"repo_url":"ssh://git@github.com/acme/data.git","base_path":"exports"}',true,true FROM credentials c WHERE c.slug='manual-grant';
INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active,read_only)
SELECT c.organization_id,c.id,'Manual GitLab','gitlab','gitlab','{"repository":{"id":"42","web_url":"https://gitlab.com/acme/data","default_branch":"main"},"base_path":"exports"}',true,false FROM credentials c WHERE c.slug='manual-grant';
INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active,read_only)
SELECT c.organization_id,c.id,'Managed GitHub','managed','github','{"repository":{"id":"99","web_url":"https://github.com/acme/data"}}',true,true FROM credentials c WHERE c.slug='manual-grant';
CREATE TABLE migration_source_snapshot AS SELECT id,slug,credential_id,read_only,is_active FROM data_sources;
`)
	require.NoError(t, err)
	data, err := os.ReadFile("testdata/historical/000039_data_source_proof_configuration.up.sql")
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), string(data))
	require.NoError(t, err)
	var mismatches int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM migration_source_snapshot before LEFT JOIN data_sources after ON after.id=before.id WHERE after.id IS NULL OR after.slug<>before.slug OR after.credential_id<>before.credential_id OR after.read_only<>before.read_only OR after.is_active<>before.is_active`).Scan(&mismatches))
	require.Zero(t, mismatches)
	var sourceType, repoURL, payload string
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT type, config->>'repo_url' FROM data_sources WHERE slug='github'`).Scan(&sourceType, &repoURL))
	require.Equal(t, "git", sourceType)
	require.Equal(t, "ssh://git@github.com/acme/data.git", repoURL)
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT type, config->>'repo_url' FROM data_sources WHERE slug='gitlab'`).Scan(&sourceType, &repoURL))
	require.Equal(t, "git", sourceType)
	require.Equal(t, "https://gitlab.com/acme/data.git", repoURL)
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT type FROM data_sources WHERE slug='managed'`).Scan(&sourceType))
	require.Equal(t, "github", sourceType)
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT encode(encrypted_payload,'hex') FROM credentials WHERE slug='manual-grant'`).Scan(&payload))
	require.Equal(t, "01020304", payload)
}

func TestProofReceiptRetentionMigrationAuditsPolicyWithoutRewritingDecisions(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	pool := e2edb.OpenIsolatedPostgresPool(t, "Proof receipt retention migration")
	_, err := pool.Exec(t.Context(), `
 DROP INDEX idx_proof_receipts_retention;
 DROP INDEX idx_proof_receipts_supersedes;
 DROP INDEX idx_proof_receipts_evidence;
 DROP INDEX idx_events_proof_receipt_deletion;
 `)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `
 WITH org AS (INSERT INTO organizations (name,slug) VALUES ('Retained','retained') RETURNING id),
 credential AS (INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload) SELECT id,'git-credential','static_secret','git','active','fixture'::bytea FROM org RETURNING id,organization_id),
 source AS (INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config) SELECT organization_id,id,'Git','git','git','{}' FROM credential RETURNING id,organization_id),
 project AS (INSERT INTO projects(organization_id,slug,name,pipeline_backend_data_source_id,pipeline_backend_repo,pipeline_backend_tracked_branch,pipeline_backend_pipeline_dir) SELECT organization_id,'etl','ETL',id,'https://example.test/data.git','main','pipelines' FROM source RETURNING id,organization_id),
 environment AS (INSERT INTO environments(organization_id,slug,name) SELECT id,'staging','Staging' FROM org RETURNING id),
 scope AS (INSERT INTO project_environments(organization_id,project_id,environment_id,is_default) SELECT project.organization_id,project.id,environment.id,true FROM project,environment RETURNING id,project_id),
 mapping AS (INSERT INTO proof_change_candidate_mappings(organization_id,project_id,project_environment_id,data_source_id,provider,provider_connection_id,repository_id,target_ref,pipeline_path) SELECT source.organization_id,scope.project_id,scope.id,source.id,'github','1','42','main','pipelines/test.json' FROM source,scope RETURNING *),
 candidate AS (INSERT INTO proof_change_candidates(organization_id,mapping_id,slug,candidate_key,change_request_id,source_ref,head_sha,source_sha256,spec_hash,canonical_spec) SELECT organization_id,id,'candidate','candidate-key','1','feature','head','source','spec','{}' FROM mapping RETURNING id,organization_id,mapping_id,spec_hash),
 evidence AS (INSERT INTO proof_evidence(organization_id,project_id,project_environment_id,mapping_id,candidate_id,spec_hash,binding_fingerprint,capability_fingerprint,kind,digest,source_identifier,observed_at) SELECT m.organization_id,m.project_id,m.project_environment_id,m.id,c.id,c.spec_hash,'binding','capability','validation','digest','candidate',now() FROM mapping m,candidate c RETURNING *)
 INSERT INTO proof_receipts(organization_id,project_id,project_environment_id,mapping_id,candidate_id,evidence_id,slug,receipt_key,spec_hash,binding_fingerprint,capability_fingerprint,compiler_version,policy_version,aggregate_verdict,receipt,created_at,retain_until)
 SELECT organization_id,project_id,project_environment_id,mapping_id,candidate_id,id,'receipt','key',spec_hash,binding_fingerprint,capability_fingerprint,'compiler','policy','unverified','{"historical":true}',now()-interval '8 days',now()+interval '82 days' FROM evidence;
 INSERT INTO organizations(name,slug) VALUES ('Empty','empty');
 CREATE TABLE original_retention AS SELECT receipt,created_at,retain_until FROM proof_receipts;
 `)
	require.NoError(t, err)
	raw, err := os.ReadFile("testdata/historical/000040_proof_receipt_retention.up.sql")
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), string(raw))
	require.NoError(t, err)
	var unchanged bool
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT r.receipt=o.receipt AND r.created_at=o.created_at AND r.retain_until=o.retain_until FROM proof_receipts r,original_retention o`).Scan(&unchanged))
	require.True(t, unchanged)
	var organization, class, actor, action string
	var days int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT o.slug,e.event_class,e.actor_type,e.action,(e.metadata->>'proof_receipt_retention_days')::integer FROM events e JOIN organizations o ON o.id=e.organization_id`).Scan(&organization, &class, &actor, &action, &days))
	require.Equal(t, "retained", organization)
	require.Equal(t, "security_audit", class)
	require.Equal(t, "system", actor)
	require.Equal(t, "update", action)
	require.Equal(t, 7, days)
}
