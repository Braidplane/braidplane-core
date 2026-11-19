package e2e

import (
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/stretchr/testify/require"
)

func TestStripeRefreshClaimFencesWorkersAndTenants(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "Stripe refresh claims")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	repo := credentials.New()
	owner := credentials.ResolveRuntimeCredentialParams{OrgSlug: "stripe-claim-org", DataSourceSlug: "source", OwnerKind: credentials.OwnerKindDataSource}
	grant := credentials.CredentialGrant{OrgSlug: owner.OrgSlug, DataSourceSlug: owner.DataSourceSlug, Slug: "grant", Provider: "stripe_test"}
	require.NoError(t, tx.QueryRow(ctx, `
WITH org AS (
    INSERT INTO organizations (slug, name) VALUES ($1, 'Fixture') RETURNING id
), cred AS (
    INSERT INTO credentials (organization_id, slug, kind, status)
    SELECT id, $2, 'oauth2_grant', 'active' FROM org RETURNING id, organization_id
), source AS (
    INSERT INTO data_sources (credential_id, organization_id, name, slug, type, config, read_only)
    SELECT id, organization_id, 'Fixture', $3, 'stripe', '{"mode":"test"}', true FROM cred
)
INSERT INTO credential_oauth_grants (credential_id, provider, encrypted_token_set)
SELECT id, 'stripe_test', 'fixture' FROM cred RETURNING updated_at`, grant.OrgSlug, grant.Slug, grant.DataSourceSlug).Scan(&grant.GrantUpdatedAt))
	state, err := repo.LockOAuthGrantIntent(ctx, tx, owner)
	require.NoError(t, err)
	require.Equal(t, "stripe", state.SourceType)
	require.Equal(t, "test", state.SourceMode)

	claim := func(g credentials.CredentialGrant, id string, want bool) {
		t.Helper()
		got, err := repo.ClaimHTTPOAuthRefresh(ctx, tx, g, id)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	stale := grant
	stale.GrantUpdatedAt = stale.GrantUpdatedAt.Add(-time.Second)
	claim(stale, "stale", false)
	otherMode := grant
	otherMode.Provider = "stripe_live"
	claim(otherMode, "wrong-mode", false)
	claim(grant, "first", true)
	otherOrg := owner
	otherOrg.OrgSlug = "other-tenant"
	require.NoError(t, repo.ReleaseHTTPOAuthRefresh(ctx, tx, otherOrg, "first"))
	require.NoError(t, repo.ReleaseHTTPOAuthRefresh(ctx, tx, owner, "wrong-worker"))
	claim(grant, "second", false)
	_, err = tx.Exec(ctx, `UPDATE credential_oauth_grants SET refresh_claim_expires_at = NOW() - INTERVAL '1 second' WHERE credential_id = (SELECT c.id FROM credentials c JOIN organizations o ON c.organization_id = o.id WHERE o.slug = $1 AND c.slug = $2)`, owner.OrgSlug, grant.Slug)
	require.NoError(t, err)
	claim(grant, "second", true)
	require.NoError(t, repo.ReleaseHTTPOAuthRefresh(ctx, tx, owner, "first"))
	claim(grant, "third", false)
	require.NoError(t, repo.ReleaseHTTPOAuthRefresh(ctx, tx, owner, "second"))
	claim(grant, "third", true)
}
