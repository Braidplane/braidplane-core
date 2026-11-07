package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/credentialrekey"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/service/credentialrekeysvc"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestRekeyPersistenceAndResolution(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "credential rekey")
	legacyKey, oldKey, currentKey := bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{4}, 32)
	old, err := crypto.NewCredentialKeyring("old", map[string][]byte{"old": oldKey}, legacyKey)
	require.NoError(t, err)
	current, err := crypto.NewCredentialKeyring("current", map[string][]byte{"old": oldKey, "current": currentKey}, legacyKey)
	require.NoError(t, err)
	strict, err := crypto.NewCredentialKeyring("current", map[string][]byte{"current": currentKey}, nil)
	require.NoError(t, err)
	fixture := seedRekeyFixture(t, pool, old, legacyKey)
	fixture.assertResolution(t, old)
	repo := credentialrekey.New()
	before, err := repo.List(ctx, pool, credentialrekey.Cursor{}, 100)
	require.NoError(t, err)
	require.Len(t, before, 8)
	families := map[int32]int{}
	for _, record := range before {
		require.Equal(t, fixture.org.String(), record.Tenant)
		families[record.Family]++
	}
	require.Equal(t, map[int32]int{1: 3, 2: 1, 3: 1, 4: 1, 5: 1, 6: 1}, families)
	metadata := fixture.metadata(t)
	txm := postgres.NewTransactionManager(pool)
	audit := eventssvc.New(txm, repoevents.New(), testutil.FakeClock{NowFn: time.Now})
	newSweep := func(ring *crypto.CredentialKeyring, events eventssvc.Service) *credentialrekeysvc.Service {
		svc, err := credentialrekeysvc.New(txm, repo, ring, events)
		require.NoError(t, err)
		return svc
	}

	// Both missing historical keys and wrong material fail before any committed replacement.
	_, err = newSweep(strict, audit).Batch(ctx, credentialrekey.Cursor{}, 100)
	require.Error(t, err)
	wrong, err := crypto.NewCredentialKeyring("current", map[string][]byte{"old": bytes.Repeat([]byte{9}, 32), "current": currentKey}, bytes.Repeat([]byte{8}, 32))
	require.NoError(t, err)
	_, err = newSweep(wrong, audit).Batch(ctx, credentialrekey.Cursor{}, 100)
	require.Error(t, err)
	unchanged, err := repo.List(ctx, pool, credentialrekey.Cursor{}, 100)
	require.NoError(t, err)
	require.Equal(t, before, unchanged)
	require.Zero(t, fixture.auditCount(t))

	// The delegate inserts an actual event, then fails: both event and ciphertext must roll back.
	_, err = newSweep(current, failingAudit{Service: audit}).Batch(ctx, credentialrekey.Cursor{}, 1)
	require.Error(t, err)
	unchanged, err = repo.List(ctx, pool, credentialrekey.Cursor{}, 100)
	require.NoError(t, err)
	require.Equal(t, before, unchanged)
	require.Zero(t, fixture.auditCount(t))

	sweep := newSweep(current, audit)
	cursor := credentialrekey.Cursor{}
	total := 0
	for {
		// Recreate the service for every batch to model resuming from the persisted cursor.
		result, err := newSweep(current, audit).Batch(ctx, cursor, 2)
		require.NoError(t, err)
		require.Zero(t, result.Conflicts)
		require.Zero(t, result.Current)
		total += result.Rekeyed
		cursor = result.After
		if result.Done {
			break
		}
	}
	require.Equal(t, 8, total)
	require.Equal(t, 8, fixture.auditCount(t))
	require.Equal(t, metadata, fixture.metadata(t), "rekey must preserve lifecycle metadata and source revisions")
	var tombstonesPreserved bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT
    (SELECT encrypted_payload IS NULL FROM credentials WHERE organization_id=o.id AND slug='null-tombstone')
    AND (SELECT octet_length(encrypted_payload)=0 FROM credentials WHERE organization_id=o.id AND slug='empty-tombstone')
    AND (SELECT octet_length(g.encrypted_token_set)=0 FROM credential_oauth_grants g JOIN credentials c ON c.id=g.credential_id WHERE c.organization_id=o.id AND c.slug='oauth-tombstone')
FROM organizations o WHERE o.slug=$1`, fixture.org).Scan(&tombstonesPreserved))
	require.True(t, tombstonesPreserved)
	var auditMetadata string
	require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_agg(metadata)::text FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, fixture.org).Scan(&auditMetadata))
	for _, secret := range []string{"synthetic-connector", "synthetic-environment", "synthetic-oauth", "synthetic-refresh", "synthetic-signing"} {
		require.NotContains(t, auditMetadata, secret)
	}
	fixture.assertResolution(t, strict)
	after, err := repo.List(ctx, pool, credentialrekey.Cursor{}, 100)
	require.NoError(t, err)
	for i, record := range after {
		scope := recordScope(t, record)
		want, err := old.Open(scope, before[i].Ciphertext)
		require.NoError(t, err)
		got, err := strict.Open(scope, record.Ciphertext)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	result, err := sweep.Batch(ctx, credentialrekey.Cursor{}, 100)
	require.NoError(t, err)
	require.Equal(t, 8, result.Current)
	require.Zero(t, result.Rekeyed)
	require.True(t, result.Done)
	require.Equal(t, 8, fixture.auditCount(t))
	second, err := repo.List(ctx, pool, credentialrekey.Cursor{}, 100)
	require.NoError(t, err)
	require.Equal(t, after, second)

	// A stale sweep cannot overwrite a newer token or resurrect a revoked static payload.
	stale := after[0]
	_, err = pool.Exec(ctx, `UPDATE credentials SET encrypted_payload=$2 WHERE id=$1`, stale.ID, []byte("synthetic-concurrent-replacement"))
	require.NoError(t, err)
	changed, err := repo.Replace(ctx, pool, stale, stale.Ciphertext)
	require.NoError(t, err)
	require.False(t, changed)
	_, err = pool.Exec(ctx, `UPDATE credentials SET status='revoked', encrypted_payload=NULL, revoked_at=NOW() WHERE id=$1`, stale.ID)
	require.NoError(t, err)
	changed, err = repo.Replace(ctx, pool, stale, stale.Ciphertext)
	require.NoError(t, err)
	require.False(t, changed)
	var tombstone bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT encrypted_payload IS NULL AND status='revoked' FROM credentials WHERE id=$1`, stale.ID).Scan(&tombstone))
	require.True(t, tombstone)
}

type failingAudit struct{ eventssvc.Service }

func (a failingAudit) CreateEvent(ctx context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
	if _, err := a.Service.CreateEvent(ctx, params); err != nil {
		return nil, err
	}
	return nil, xerror.Unexpected().Detail("synthetic audit failure")
}

func recordScope(t *testing.T, record credentialrekey.Record) crypto.CredentialScope {
	t.Helper()
	resource := record.Resource
	if record.Family == 6 {
		encoded, err := json.Marshal([2]string{record.Resource, record.ResourceAux})
		require.NoError(t, err)
		resource = string(encoded)
	}
	return crypto.CredentialScope{Tenant: record.Tenant, Resource: resource, Purpose: record.Purpose, SchemaVersion: 1}
}

type rekeyFixture struct {
	pool *pgxpool.Pool
	org  slug.Slug
}

func seedRekeyFixture(t *testing.T, pool *pgxpool.Pool, old *crypto.CredentialKeyring, legacyKey []byte) rekeyFixture {
	t.Helper()
	ctx := t.Context()
	f := rekeyFixture{pool: pool, org: slug.FromString(fmt.Sprintf("rekey-%d", time.Now().UnixNano()))}
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, query, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO organizations(name,slug) VALUES('Synthetic rekey organization',$1)`, f.org)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM environment_secrets WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.org)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM projects WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.org)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug=$1`, f.org)
		require.NoError(t, err)
	})
	exec(`INSERT INTO environments(organization_id,name,slug) SELECT id,'Synthetic environment','prod' FROM organizations WHERE slug=$1`, f.org)
	legacy, err := crypto.NewAES256(legacyKey)
	require.NoError(t, err)
	seal := func(resource, purpose, plaintext string, unversioned bool) []byte {
		t.Helper()
		if unversioned {
			result, err := legacy.Encrypt([]byte(plaintext))
			require.NoError(t, err)
			return result
		}
		result, err := old.Seal(crypto.CredentialScope{Tenant: f.org.String(), Resource: resource, Purpose: purpose, SchemaVersion: 1}, []byte(plaintext))
		require.NoError(t, err)
		return result
	}
	payload := func(value string) string { return base64.RawStdEncoding.EncodeToString([]byte(value)) }
	for _, row := range []struct {
		slug, kind, typ string
		cipher          []byte
	}{
		{"connector-credential", "static_secret", "postgres", seal("connector-credential", "static-secret", payload(`{"password":"synthetic-connector"}`), true)},
		{"environment-credential", "static_secret", "environment_secret", seal("environment-credential", "static-secret", payload(`{"value":"synthetic-environment"}`), false)},
		{"oauth-credential", "oauth2_grant", "", nil},
		{"webhook-credential", "static_secret", "pipeline_webhook", seal("webhook-credential", "pipeline-webhook-secret", "synthetic-webhook", false)},
		{"null-tombstone", "static_secret", "postgres", nil},
		{"empty-tombstone", "static_secret", "postgres", []byte{}},
		{"oauth-tombstone", "oauth2_grant", "", nil},
	} {
		exec(`INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload) SELECT id,$2,$3,NULLIF($4,''),'active',$5 FROM organizations WHERE slug=$1`, f.org, row.slug, row.kind, row.typ, row.cipher)
	}
	for _, source := range []struct{ slug, credential, typ string }{{"connector", "connector-credential", "postgres"}, {"oauth", "oauth-credential", "http"}, {"retired-http", "oauth-credential", "http"}} {
		exec(`INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active,read_only) SELECT o.id,c.id,'Synthetic source',$2,$4,'{}',true,true FROM organizations o JOIN credentials c ON c.organization_id=o.id AND c.slug=$3 WHERE o.slug=$1`, f.org, source.slug, source.credential, source.typ)
	}
	exec(`UPDATE data_sources SET deleted_at=NOW(),connection_test_revision=7 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='retired-http'`, f.org)
	exec(`INSERT INTO environment_secrets(organization_id,environment_id,credential_id,key) SELECT o.id,e.id,c.id,'API_TOKEN' FROM organizations o JOIN environments e ON e.organization_id=o.id JOIN credentials c ON c.organization_id=o.id AND c.slug='environment-credential' WHERE o.slug=$1`, f.org)
	tokens := payload(`{"access_token":"synthetic-oauth","refresh_token":"synthetic-refresh","expires_at":"2099-01-01T00:00:00Z","scopes":[]}`)
	exec(`INSERT INTO credential_oauth_grants(credential_id,provider,encrypted_token_set,access_token_expires_at) SELECT c.id,'fakecrm',$2,'2099-01-01' FROM credentials c JOIN organizations o ON o.id=c.organization_id WHERE o.slug=$1 AND c.slug='oauth-credential'`, f.org, seal("oauth-credential", "oauth-token-set", tokens, false))
	exec(`INSERT INTO credential_oauth_grants(credential_id,provider,encrypted_token_set) SELECT c.id,'fakecrm',decode('','hex') FROM credentials c JOIN organizations o ON o.id=c.organization_id WHERE o.slug=$1 AND c.slug='oauth-tombstone'`, f.org)
	exec(`INSERT INTO credential_oauth_installs(organization_id,data_source_id,slug,provider,state_hash,intent,actor_type,expires_at,consumed_at,expected_credential_id,expected_credential_status,encrypted_pkce_verifier) SELECT o.id,d.id,'expired-install','http','synthetic-state','rotate_grant','system',NOW()-INTERVAL '1 day',NOW(),d.credential_id,'active',$2 FROM organizations o JOIN data_sources d ON d.organization_id=o.id AND d.slug='retired-http' WHERE o.slug=$1`, f.org, seal("expired-install", "oauth-install-snapshot", payload(`{"verifier":"synthetic-verifier"}`), true))
	exec(`INSERT INTO credential_http_oauth_apps(organization_id,data_source_id,encrypted_config) SELECT o.id,d.id,$2 FROM organizations o JOIN data_sources d ON d.organization_id=o.id AND d.slug='retired-http' WHERE o.slug=$1`, f.org, seal("retired-http", "http-oauth-app", payload(`{"client_secret":"synthetic-app"}`), false))
	exec(`INSERT INTO gitlab_proof_webhooks(organization_id,data_source_id,project_id,hook_id,status,route_key_hash,encrypted_signing_token) SELECT o.id,d.id,1,0,'failed','synthetic-route',$2 FROM organizations o JOIN data_sources d ON d.organization_id=o.id AND d.slug='retired-http' WHERE o.slug=$1`, f.org, seal("retired-http", "gitlab-webhook-secret", `{"route_key":"synthetic-route","signing_token":"synthetic-signing"}`, true))
	exec(`WITH project AS(INSERT INTO projects(organization_id,slug,name) SELECT id,'project','Synthetic project' FROM organizations WHERE slug=$1 RETURNING id,organization_id), pe AS(INSERT INTO project_environments(organization_id,project_id,environment_id) SELECT p.organization_id,p.id,e.id FROM project p JOIN environments e ON e.organization_id=p.organization_id RETURNING id,project_id), pipeline AS(INSERT INTO pipeline_specs(project_id,slug,name,spec_jsonb,source_path,source_ref,last_applied_commit) SELECT id,'pipeline','Synthetic pipeline','{}','pipeline.json','refs/heads/main','synthetic-commit' FROM project RETURNING id,project_id,slug) INSERT INTO pipeline_triggers(project_id,project_environment_id,pipeline_id,pipeline_slug,slug,name,trigger_type,enabled,version_policy,receiver_id,webhook_verification_strategy,webhook_input_mappings,created_by_actor_type,updated_by_actor_type) SELECT p.project_id,pe.id,p.id,p.slug,'trigger','Synthetic trigger','webhook',true,'latest_deployed','synthetic-receiver','hmac_sha256','[]','system','system' FROM pipeline p JOIN pe ON pe.project_id=p.project_id`, f.org)
	exec(`INSERT INTO pipeline_webhook_deliveries(organization_id,project_id,project_environment_id,pipeline_trigger_id,receiver_id,delivery_id,payload_digest,payload_size_bytes,key_version,trigger_revision,pipeline_slug,version_policy,run_slug,status,encrypted_envelope,envelope_expires_at,received_at) SELECT o.id,p.id,pe.id,tr.id,'synthetic-receiver','synthetic-delivery',repeat('a',64),1,1,tr.revision,'pipeline','latest_deployed','synthetic-run','failed',$2,NOW()+INTERVAL '1 day',NOW() FROM organizations o JOIN projects p ON p.organization_id=o.id JOIN project_environments pe ON pe.project_id=p.id JOIN pipeline_triggers tr ON tr.project_environment_id=pe.id WHERE o.slug=$1`, f.org, seal(`["synthetic-receiver","synthetic-delivery"]`, "pipeline-webhook-envelope", `{"event_input":{},"runtime_input":{}}`, false))
	return f
}

func (f rekeyFixture) assertResolution(t *testing.T, ring *crypto.CredentialKeyring) {
	t.Helper()
	svc, err := credentialssvc.New(postgres.NewTransactionManager(f.pool), repocredentials.New(), ring, nil, testutil.FakeClock{NowFn: time.Now}, false)
	require.NoError(t, err)
	for _, test := range []struct {
		owner credentialssvc.CredentialOwner
		want  string
	}{
		{credentialssvc.CredentialOwner{OrgSlug: f.org, DataSourceSlug: "connector"}, "synthetic-connector"},
		{credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindEnvironmentSecret, OrgSlug: f.org, EnvironmentSlug: "prod", EnvironmentSecretKey: "API_TOKEN"}, "synthetic-environment"},
		{credentialssvc.CredentialOwner{OrgSlug: f.org, DataSourceSlug: "oauth"}, "synthetic-oauth"},
	} {
		got, err := svc.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: test.owner})
		require.NoError(t, err)
		require.Contains(t, string(got.PlaintextJSON), test.want)
	}
}

func (f rekeyFixture) auditCount(t *testing.T) int {
	t.Helper()
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND metadata->>'operation'='credential_encryption_rekey'`, f.org).Scan(&count))
	return count
}

func (f rekeyFixture) metadata(t *testing.T) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []struct{ name, column, order, filter string }{
		{"credentials", "encrypted_payload", "id", "organization_id=(SELECT id FROM organizations WHERE slug=$1)"},
		{"credential_oauth_grants", "encrypted_token_set", "id", "credential_id IN(SELECT id FROM credentials WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1))"},
		{"credential_oauth_installs", "encrypted_pkce_verifier", "id", "organization_id=(SELECT id FROM organizations WHERE slug=$1)"},
		{"credential_http_oauth_apps", "encrypted_config", "data_source_id", "organization_id=(SELECT id FROM organizations WHERE slug=$1)"},
		{"gitlab_proof_webhooks", "encrypted_signing_token", "id", "organization_id=(SELECT id FROM organizations WHERE slug=$1)"},
		{"pipeline_webhook_deliveries", "encrypted_envelope", "id", "organization_id=(SELECT id FROM organizations WHERE slug=$1)"},
		{"data_sources", "unused", "id", "organization_id=(SELECT id FROM organizations WHERE slug=$1)"},
	} {
		var value string
		require.NoError(t, f.pool.QueryRow(t.Context(), fmt.Sprintf(`SELECT jsonb_agg(to_jsonb(x)-'%s' ORDER BY %s)::text FROM %s x WHERE %s`, table.column, table.order, table.name, table.filter), f.org).Scan(&value))
		result[table.name] = value
	}
	return result
}
