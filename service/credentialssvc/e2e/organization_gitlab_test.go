package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/stretchr/testify/require"
)

func TestOrganizationGitLabLifecycleKeepsExplicitSourceOwnership(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	pool := e2edb.OpenPostgresPool(t, "organization GitLab lifecycle")
	org, source := uniqueCredentialE2ESlug("org-gitlab"), uniqueCredentialE2ESlug("gitlab-source")
	seedCredentialLifecycleDataSource(t, pool, org, source)
	_, err := pool.Exec(t.Context(), `UPDATE data_sources SET type='gitlab', config='{"repository":{"id":"42"}}' WHERE slug=$1`, source)
	require.NoError(t, err)
	unlinked := slug.Slug(source.String() + "-unlinked")
	_, err = pool.Exec(t.Context(), `INSERT INTO data_sources (organization_id,credential_id,name,slug,type,config,is_active,read_only) SELECT organization_id,credential_id,'Unlinked',$2,type,config,true,true FROM data_sources WHERE slug=$1`, source, unlinked)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `WITH old AS (
UPDATE credentials SET kind='oauth2_grant', encrypted_payload=NULL
WHERE id=(SELECT credential_id FROM data_sources WHERE slug=$1) RETURNING id)
INSERT INTO credential_oauth_grants (credential_id,provider,encrypted_token_set,scopes,account)
SELECT id,'gitlab',decode('66616b65','hex'),ARRAY['read_repository'],'{}'::jsonb FROM old`, source)
	require.NoError(t, err)
	now := time.Now().UTC()
	provider := &fakeProviderAdapter{authURL: "https://gitlab.example/oauth", exchange: credentialssvc.OAuthTokenSet{AccessToken: "initial", RefreshToken: "refresh", ExpiresAt: now.Add(time.Hour), Scopes: []string{"read_repository"}}, refresh: credentialssvc.OAuthTokenSet{AccessToken: "refreshed", RefreshToken: "refresh-next", ExpiresAt: now.Add(2 * time.Hour), Scopes: []string{"read_repository"}}}
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	svc, err := credentialssvc.New(postgres.NewTransactionManager(pool), repocredentials.New(), cipher, credentialssvc.ProviderRegistry{"gitlab": provider}, testutil.FakeClock{NowFn: func() time.Time { return now }}, false)
	require.NoError(t, err)
	owner := credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindOrganizationGitLab, OrgSlug: org}
	connection, err := svc.CreateOrganizationGitLabConnection(t.Context(), credentialssvc.CreateOrganizationGitLabConnectionParams{OrganizationSlug: org, Provider: "gitlab"})
	require.NoError(t, err)
	err = postgres.NewTransactionManager(pool).RunInTx(t.Context(), func(ctx context.Context, tx postgres.DBTX) error {
		_, err := repocredentials.New().LockOAuthGrantIntent(ctx, tx, repocredentials.ResolveRuntimeCredentialParams{OwnerKind: repocredentials.OwnerKindDataSource, OrgSlug: org})
		return err
	})
	require.ErrorIs(t, err, repocredentials.ErrCredentialNotFound, "missing source must not select the organization credential")
	again, err := svc.CreateOrganizationGitLabConnection(t.Context(), credentialssvc.CreateOrganizationGitLabConnectionParams{OrganizationSlug: org, Provider: "gitlab"})
	require.NoError(t, err)
	require.Equal(t, connection.Credential.Slug, again.Credential.Slug)
	install, err := svc.StartOAuthInstall(t.Context(), credentialssvc.StartOAuthInstallParams{Owner: owner, Provider: "gitlab", Intent: credentialssvc.OAuthInstallIntentCreate, RequestedScopes: []string{"read_repository"}})
	require.NoError(t, err)
	found, err := svc.FindOAuthInstall(t.Context(), credentialssvc.FindOAuthInstallParams{Provider: "gitlab", State: install.State})
	require.NoError(t, err)
	require.Equal(t, owner, found.Owner)
	completed, err := svc.CompleteOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "gitlab", State: install.State, Code: "local-fake-code"})
	require.NoError(t, err)
	require.Equal(t, connection.Credential.Slug, completed.Credential.Slug)
	_, err = svc.CompleteOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "gitlab", State: install.State, Code: "replay"})
	require.ErrorIs(t, err, credentialssvc.ErrOAuthStateConsumed)
	attach := credentialssvc.AttachOrganizationGitLabConnectionParams{OrganizationSlug: org, DataSourceSlug: source, ExpectedCredentialSlug: completed.Credential.Slug, ExpectedRepositoryID: "wrong-repository"}
	require.Error(t, svc.AttachOrganizationGitLabConnection(t.Context(), attach))
	attach.ExpectedRepositoryID = "42"
	require.NoError(t, svc.AttachOrganizationGitLabConnection(t.Context(), attach))
	_, err = svc.Rotate(t.Context(), credentialssvc.RotateParams{Owner: credentialOwner(org, source)})
	require.Error(t, err)
	_, err = svc.Revoke(t.Context(), credentialssvc.RevokeParams{Owner: credentialOwner(org, source)})
	require.Error(t, err)
	_, err = svc.Refresh(t.Context(), credentialssvc.RefreshGrantParams{Owner: credentialOwner(org, source)})
	require.Error(t, err)
	_, err = svc.Refresh(t.Context(), credentialssvc.RefreshGrantParams{Owner: owner})
	require.NoError(t, err)
	runtime, err := svc.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: credentialOwner(org, source)})
	require.NoError(t, err)
	assertAccessToken(t, runtime.PlaintextJSON, "refreshed", []string{"read_repository"})
	rotation, err := svc.Rotate(t.Context(), credentialssvc.RotateParams{Owner: owner})
	require.NoError(t, err)
	staleRotation, err := svc.Rotate(t.Context(), credentialssvc.RotateParams{Owner: owner})
	require.NoError(t, err)
	rotated, err := svc.CompleteOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "gitlab", State: rotation.State, Code: "replacement"})
	require.NoError(t, err)
	_, err = svc.CompleteOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "gitlab", State: staleRotation.State, Code: "stale-replacement"})
	require.ErrorIs(t, err, credentialssvc.ErrOAuthIntentConflict)
	require.NotEqual(t, completed.Credential.Slug, rotated.Credential.Slug)
	var bound, unbound slug.Slug
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT c.slug FROM data_sources ds JOIN credentials c ON c.id=ds.credential_id WHERE ds.slug=$1`, source).Scan(&bound))
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT c.slug FROM data_sources ds JOIN credentials c ON c.id=ds.credential_id WHERE ds.slug=$1`, unlinked).Scan(&unbound))
	require.Equal(t, rotated.Credential.Slug, bound)
	require.NotEqual(t, bound, unbound)
	var oldStatus string
	var tokenBytes int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT c.status,octet_length(g.encrypted_token_set) FROM credentials c JOIN credential_oauth_grants g ON g.credential_id=c.id WHERE c.slug=$1`, unbound).Scan(&oldStatus, &tokenBytes))
	require.Equal(t, "active", oldStatus)
	require.Positive(t, tokenBytes)
	lastAttachment := attach
	lastAttachment.DataSourceSlug = unlinked
	lastAttachment.ExpectedCredentialSlug = rotated.Credential.Slug
	require.NoError(t, svc.AttachOrganizationGitLabConnection(t.Context(), lastAttachment))
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT c.status,octet_length(g.encrypted_token_set) FROM credentials c JOIN credential_oauth_grants g ON g.credential_id=c.id WHERE c.slug=$1`, unbound).Scan(&oldStatus, &tokenBytes))
	require.Equal(t, "rotated", oldStatus)
	require.Zero(t, tokenBytes)

	require.Error(t, svc.AttachOrganizationGitLabConnection(t.Context(), attach))
	metadata, err := svc.GetOrganizationGitLabConnection(t.Context(), org)
	require.NoError(t, err)
	require.Equal(t, []string{"read_repository"}, metadata.Credential.OAuth.Scopes)
	otherOrg := uniqueCredentialE2ESlug("other-gitlab")
	seedCredentialLifecycleDataSource(t, pool, otherOrg, source)
	_, err = svc.GetOrganizationGitLabConnection(t.Context(), otherOrg)
	require.ErrorIs(t, err, credentialssvc.ErrCredentialNotFound)
	wrongTenant := attach
	wrongTenant.OrganizationSlug = otherOrg
	wrongTenant.ExpectedCredentialSlug = rotated.Credential.Slug
	require.Error(t, svc.AttachOrganizationGitLabConnection(t.Context(), wrongTenant))
	_, err = svc.Revoke(t.Context(), credentialssvc.RevokeParams{Owner: owner})
	require.NoError(t, err)
	_, err = svc.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: credentialOwner(org, source)})
	require.Error(t, err)
	var active bool
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT is_active FROM data_sources WHERE slug=$1 AND organization_id=(SELECT id FROM organizations WHERE slug=$2)`, source, org).Scan(&active))
	require.False(t, active)
}
