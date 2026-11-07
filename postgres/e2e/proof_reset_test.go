package e2e

import (
	"os"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestProofContractResetIsBoundedAuditedAndPreservesAuthoring(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	pool := e2edb.OpenIsolatedPostgresPool(t, "Proof contract reset migration")
	var err error
	_, err = pool.Exec(t.Context(), `
 WITH org AS (INSERT INTO organizations(name,slug) VALUES ('First','first'),('Second','second') RETURNING id),
 credential AS (INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload) SELECT id,'git-credential','static_secret','git','active','fixture'::bytea FROM org RETURNING id,organization_id),
 source AS (INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config) SELECT organization_id,id,'Git','git','git','{}' FROM credential RETURNING id,organization_id),
 project AS (INSERT INTO projects(organization_id,slug,name,pipeline_backend_data_source_id,pipeline_backend_repo,pipeline_backend_tracked_branch,pipeline_backend_pipeline_dir) SELECT organization_id,'etl','ETL',id,'https://example.test/data.git','main','pipelines' FROM source RETURNING id,organization_id),
 environment AS (INSERT INTO environments(organization_id,slug,name) SELECT id,'staging','Staging' FROM org RETURNING id,organization_id),
 scope AS (INSERT INTO project_environments(organization_id,project_id,environment_id,is_default) SELECT project.organization_id,project.id,environment.id,true FROM project JOIN environment USING(organization_id) RETURNING id,project_id,organization_id),
 mapping AS (INSERT INTO proof_change_candidate_mappings(organization_id,project_id,project_environment_id,data_source_id,provider,provider_connection_id,repository_id,target_ref,pipeline_path) SELECT source.organization_id,scope.project_id,scope.id,source.id,'github','1','42','main','pipelines/test.json' FROM source JOIN scope USING(organization_id) RETURNING *),
 candidate AS (INSERT INTO proof_change_candidates(organization_id,mapping_id,slug,candidate_key,change_request_id,source_ref,head_sha,source_sha256,spec_hash,canonical_spec) SELECT organization_id,id,'candidate','candidate-'||organization_id,'1','feature','head','source','spec','{}' FROM mapping RETURNING id,organization_id,mapping_id,spec_hash),
 evidence AS (INSERT INTO proof_evidence(organization_id,project_id,project_environment_id,mapping_id,candidate_id,spec_hash,binding_fingerprint,capability_fingerprint,kind,digest,source_identifier,observed_at) SELECT m.organization_id,m.project_id,m.project_environment_id,m.id,c.id,c.spec_hash,'binding','capability','validation','digest','candidate',now() FROM mapping m JOIN candidate c ON c.mapping_id=m.id RETURNING *)
 INSERT INTO proof_receipts(organization_id,project_id,project_environment_id,mapping_id,candidate_id,evidence_id,slug,receipt_key,spec_hash,binding_fingerprint,capability_fingerprint,compiler_version,policy_version,aggregate_verdict,receipt,created_at,retain_until)
 SELECT organization_id,project_id,project_environment_id,mapping_id,candidate_id,id,'receipt','key',spec_hash,binding_fingerprint,capability_fingerprint,'compiler','policy','unverified','{}',now(),now()+interval '1 hour' FROM evidence;
 INSERT INTO organizations(name,slug) VALUES ('Empty','empty');
 INSERT INTO pipeline_specs(project_id,slug,spec_jsonb,source_path,source_ref,last_applied_commit) SELECT id,'test','{}','pipelines/test.json','main','head' FROM projects;
 INSERT INTO pipeline_versions(project_id,pipeline_slug,spec_hash) SELECT id,'test','spec' FROM projects;
 INSERT INTO pipeline_deployments(project_id,project_environment_id,pipeline_slug,current_version_id,deployed_by_actor_type) SELECT scope.project_id,scope.id,'test',version.id,'system' FROM project_environments scope JOIN pipeline_versions version USING(project_id);
 INSERT INTO proof_lifecycle_links(organization_id,project_id,project_environment_id,receipt_id,candidate_id,kind,identifier,deployment_id,spec_hash,binding_fingerprint,capability_fingerprint) SELECT receipt.organization_id,receipt.project_id,receipt.project_environment_id,receipt.id,receipt.candidate_id,'deployment','deployment',deployment.id,receipt.spec_hash,receipt.binding_fingerprint,receipt.capability_fingerprint FROM proof_receipts receipt JOIN pipeline_deployments deployment ON deployment.project_environment_id=receipt.project_environment_id;
 INSERT INTO proof_receipts(organization_id,project_id,project_environment_id,mapping_id,candidate_id,evidence_id,slug,receipt_key,spec_hash,binding_fingerprint,capability_fingerprint,compiler_version,policy_version,aggregate_verdict,receipt,supersedes_receipt_id,created_at,retain_until) SELECT organization_id,project_id,project_environment_id,mapping_id,candidate_id,evidence_id,'successor','successor-key',spec_hash,binding_fingerprint,capability_fingerprint,compiler_version,policy_version,aggregate_verdict,receipt,id,created_at,retain_until FROM proof_receipts;
 INSERT INTO proof_observation_policies(organization_id,project_id,project_environment_id,version,worker_identity,policy) SELECT organization_id,project_id,id,'old-policy','managed-connector-executor','{}' FROM project_environments;
 INSERT INTO proof_observation_jobs(organization_id,project_id,project_environment_id,candidate_id,data_source_id,slug,request_digest,request,authorization_decision,policy_version,worker_identity,connector_type,operation,reserved_requests,reserved_bytes,reserved_rows,reserved_cost,budget_expires_at,deadline_at,evidence_retain_until,metadata_retain_until) SELECT mapping.organization_id,mapping.project_id,mapping.project_environment_id,candidate.id,mapping.data_source_id,'old-job','digest','{}','{}','old-policy','managed-connector-executor','bigquery','bigquery.table_metadata',1,1,1,0,now()+interval '1 hour',now()+interval '1 minute',now()+interval '1 hour',now()+interval '1 day' FROM proof_change_candidate_mappings mapping JOIN proof_change_candidates candidate ON candidate.mapping_id=mapping.id;
 INSERT INTO execution_requests(id,organization_id,kind,request_ciphertext,deadline_at,expires_at) SELECT gen_random_uuid(),id,kind,'original'::bytea,now()+interval '1 minute',now()+interval '1 hour' FROM organizations CROSS JOIN (VALUES('proof_metadata'),('query')) AS kinds(kind) WHERE slug<>'empty';
 INSERT INTO events(organization_id,slug,actor_type,entity_type,entity_slug,action,event_class,schema_version,metadata) SELECT id,'evt-existing','system','organization',slug,'update','security_audit',1,'{}' FROM organizations WHERE slug='first';
 `)
	require.NoError(t, err)
	raw, err := os.ReadFile("testdata/historical/000045_reset_proof_metadata_contract.up.sql")
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `INSERT INTO execution_requests(id,organization_id,kind,request_ciphertext,deadline_at,expires_at) SELECT gen_random_uuid(),organization.id,'proof_metadata','bound-fixture'::bytea,now()+interval '1 minute',now()+interval '1 hour' FROM organizations organization CROSS JOIN generate_series(1,100001) WHERE organization.slug='first'`)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), string(raw))
	require.ErrorContains(t, err, "100000-record bound")
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_receipts`).Scan(&count))
	require.Equal(t, 4, count, "bound failure must roll back before deleting any Proof")
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&count))
	require.Equal(t, 1, count, "bound failure must not emit success audits")
	_, err = pool.Exec(t.Context(), `DELETE FROM execution_requests WHERE request_ciphertext='bound-fixture'::bytea`)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), string(raw))
	require.NoError(t, err)
	for _, table := range []string{"proof_receipts", "proof_evidence", "proof_lifecycle_links", "proof_observation_jobs", "proof_observation_policies"} {
		require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&count))
		require.Zero(t, count, table)
	}
	for _, table := range []string{"credentials", "data_sources", "projects", "environments", "project_environments", "proof_change_candidate_mappings", "proof_change_candidates", "pipeline_specs", "pipeline_versions", "pipeline_deployments"} {
		require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&count))
		require.Equal(t, 2, count, table)
	}
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM execution_requests WHERE kind='query'`).Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM execution_requests WHERE kind='proof_metadata'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE slug LIKE 'evt-proof-contract-reset-%' AND action='delete' AND event_class='security_audit' AND (metadata->>'receipts')::integer=2 AND (metadata->>'evidence')::integer=1 AND (metadata->>'lifecycle_links')::integer=1 AND (metadata->>'observation_jobs')::integer=1 AND (metadata->>'observation_policies')::integer=1 AND (metadata->>'proof_executions')::integer=1`).Scan(&count))
	require.Equal(t, 2, count, "one correctly scoped audit for each affected tenant")
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE slug='evt-existing'`).Scan(&count))
	require.Equal(t, 1, count)
}
