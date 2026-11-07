package e2e

import (
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/stretchr/testify/require"
)

func TestHTTPOAuthAppStorageIsTenantAndSourceScoped(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "HTTP OAuth app tenant isolation")
	org := uniqueCredentialE2ESlug("http-oauth-org")
	otherOrg := uniqueCredentialE2ESlug("http-oauth-other")
	source := uniqueCredentialE2ESlug("http-oauth-source")
	seedCredentialLifecycleDataSource(t, pool, org, source)
	seedCredentialLifecycleDataSource(t, pool, otherOrg, source)
	_, err := pool.Exec(ctx, `UPDATE data_sources SET type = 'http' WHERE organization_id IN (SELECT id FROM organizations WHERE slug IN ($1, $2)) AND slug = $3`, org, otherOrg, source)
	require.NoError(t, err)
	repo := repocredentials.New()
	owner := repocredentials.ResolveRuntimeCredentialParams{OrgSlug: org, DataSourceSlug: source}
	otherOwner := repocredentials.ResolveRuntimeCredentialParams{OrgSlug: otherOrg, DataSourceSlug: source}
	encrypted := []byte("encrypted-app-fixture")
	require.NoError(t, repo.SaveHTTPOAuthApp(ctx, pool, owner, encrypted))
	stored, err := repo.FindHTTPOAuthApp(ctx, pool, owner)
	require.NoError(t, err)
	require.Equal(t, encrypted, stored)
	_, err = repo.FindHTTPOAuthApp(ctx, pool, otherOwner)
	require.ErrorIs(t, err, repocredentials.ErrCredentialNotFound)
	require.NoError(t, repo.SaveHTTPOAuthApp(ctx, pool, otherOwner, []byte("other-encrypted-app")))
	require.NoError(t, repo.SaveHTTPOAuthApp(ctx, pool, owner, []byte("rotated-app")))
	stored, err = repo.FindHTTPOAuthApp(ctx, pool, otherOwner)
	require.NoError(t, err)
	require.Equal(t, []byte("other-encrypted-app"), stored)
	_, err = pool.Exec(ctx, `UPDATE data_sources SET deleted_at = NOW() WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1) AND slug = $2`, org, source)
	require.NoError(t, err)
	_, err = repo.FindHTTPOAuthApp(ctx, pool, owner)
	require.ErrorIs(t, err, repocredentials.ErrCredentialNotFound)
	require.ErrorIs(t, repo.SaveHTTPOAuthApp(ctx, pool, owner, encrypted), repocredentials.ErrCredentialNotFound)
	_, err = pool.Exec(ctx, `UPDATE data_sources SET type = 'hubspot' WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1) AND slug = $2`, otherOrg, source)
	require.NoError(t, err)
	_, err = repo.FindHTTPOAuthApp(ctx, pool, otherOwner)
	require.ErrorIs(t, err, repocredentials.ErrCredentialNotFound)
	require.ErrorIs(t, repo.SaveHTTPOAuthApp(ctx, pool, otherOwner, encrypted), repocredentials.ErrCredentialNotFound)
}

func TestOAuthInstallPersistsEncryptedPKCEThroughConsumption(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "HTTP OAuth PKCE install storage")
	org := uniqueCredentialE2ESlug("http-pkce-org")
	source := uniqueCredentialE2ESlug("http-pkce-source")
	seedCredentialLifecycleDataSource(t, pool, org, source)
	repo := repocredentials.New()
	state, err := oauthflow.RandomState()
	require.NoError(t, err)
	encrypted := []byte("encrypted-verifier-fixture")
	now := time.Now()
	install, err := repo.CreateOAuthInstall(ctx, pool, repocredentials.CredentialInstall{
		Slug: uniqueCredentialE2ESlug("http-pkce-install"), OrgSlug: org, DataSourceSlug: source, Provider: "http", StateHash: oauthflow.HashState(state), EncryptedPKCEVerifier: encrypted, Intent: "rotate_grant", ActorType: "system", RequestedScopes: []string{"read"}, ExpiresAt: now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, encrypted, install.EncryptedPKCEVerifier)
	found, err := repo.FindOAuthInstallByState(ctx, pool, "http", oauthflow.HashState(state), now)
	require.NoError(t, err)
	require.Equal(t, encrypted, found.EncryptedPKCEVerifier)
	consumed, err := repo.ConsumeOAuthInstall(ctx, pool, "http", oauthflow.HashState(state), now)
	require.NoError(t, err)
	require.Equal(t, encrypted, consumed.EncryptedPKCEVerifier)
	_, err = repo.ConsumeOAuthInstall(ctx, pool, "http", oauthflow.HashState(state), now)
	require.ErrorIs(t, err, repocredentials.ErrOAuthStateConsumed)
}
