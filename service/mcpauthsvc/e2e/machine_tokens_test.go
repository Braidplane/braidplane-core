package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	pgauth "github.com/Braidplane/braidplane-core/postgres/authorization"
	"github.com/Braidplane/braidplane-core/service/mcpauthsvc"
)

func TestMachineTokenLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	db := e2edb.OpenIsolatedPostgresPool(t, "MCP machine token lifecycle")
	exec := func(t *testing.T, statement string, args ...any) {
		t.Helper()
		_, err := db.Exec(t.Context(), statement, args...)
		require.NoError(t, err)
	}
	exec(t, `
INSERT INTO organizations (id,name,slug) VALUES (10,'First','first'),(20,'Second','second');
INSERT INTO roles (id,organization_id,name,slug) VALUES
    (13,10,'Primary','first'),(14,10,'Replacement','replacement'),(23,20,'Second','second');
INSERT INTO api_keys (id,organization_id,slug,name,token_hash,token_prefix,role_id,expires_at) VALUES
    (15,10,'worker','First worker','synthetic-first','synthetic',13,now()+interval '2 hours'),
    (25,20,'worker','Second worker','synthetic-second','synthetic',23,NULL);
`)
	const issuer, resource = "https://auth.example.test/oauth", "https://core.example.test/mcp"
	deployment, err := mcpauthsvc.New(issuer, resource, nil)
	require.NoError(t, err)
	txm := postgres.NewTransactionManager(db)
	svc, err := mcpauthsvc.NewTokenService(deployment, txm)
	require.NoError(t, err)
	register := func(t *testing.T, org slug.Slug) mcpauthsvc.MachineRegistration {
		t.Helper()
		reg, err := svc.RegisterMachineClient(t.Context(), org, "worker")
		require.NoError(t, err)
		return reg
	}
	mint := func(t *testing.T, reg mcpauthsvc.MachineRegistration) mcpauthsvc.TokenPair {
		t.Helper()
		pair, err := svc.IssueMachineTokens(t.Context(), reg.MachineClientCredentials, resource)
		require.NoError(t, err)
		return pair
	}
	verifyInactive := func(t *testing.T, pair mcpauthsvc.TokenPair, org slug.Slug) {
		t.Helper()
		_, err := svc.AuthenticateMachineAccessToken(t.Context(), pair.AccessToken, resource, org)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
	}

	t.Run("issuer tenant and principal isolation", func(t *testing.T) {
		_, err := svc.RegisterMachineClient(t.Context(), "missing", "worker")
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
		first, second := register(t, "first"), register(t, "second")
		require.NotEqual(t, first.ClientID, second.ClientID)
		require.NotEqual(t, first.ClientSecret, second.ClientSecret)
		require.WithinDuration(t, time.Now().Add(2*time.Hour), first.ExpiresAt, time.Minute)
		var digest string
		err = db.QueryRow(t.Context(), "SELECT client_secret_hash FROM mcp_oauth_machine_clients WHERE client_id=$1", first.ClientID).Scan(&digest)
		require.NoError(t, err)
		require.Equal(t, oauthflow.HashState(first.ClientSecret), digest)
		wrong := mcpauthsvc.MachineClientCredentials{ClientID: first.ClientID, ClientSecret: second.ClientSecret}
		_, err = svc.IssueMachineTokens(t.Context(), wrong, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidClient)
		_, err = svc.IssueMachineTokens(t.Context(), first.MachineClientCredentials, issuer)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidTarget)
		pair := mint(t, first)
		principal, err := svc.AuthenticateMachineAccessToken(t.Context(), pair.AccessToken, resource, "first")
		require.NoError(t, err)
		require.Equal(t, slug.Slug("worker"), principal.APIKey)
		require.Equal(t, slug.Slug("first"), principal.Organization)
		require.EqualValues(t, 15, principal.APIKeyID)
		verifyInactive(t, pair, "second")
		verifyInactive(t, pair, "")
		_, err = svc.AuthenticateUserAccessToken(t.Context(), pair.AccessToken, resource, "first")
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
		_, err = svc.RotateUserRefreshToken(t.Context(), pair.RefreshToken, first.ClientID, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
		_, err = svc.RotateMachineRefreshToken(t.Context(), second.MachineClientCredentials, pair.RefreshToken, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
		require.NoError(t, svc.RevokeUserToken(t.Context(), pair.AccessToken, first.ClientID, resource))
		_, err = svc.AuthenticateMachineAccessToken(t.Context(), pair.AccessToken, resource, "first")
		require.NoError(t, err)
		otherDeployment, err := mcpauthsvc.New("https://other.example.test/oauth", resource, nil)
		require.NoError(t, err)
		other, err := mcpauthsvc.NewTokenService(otherDeployment, txm)
		require.NoError(t, err)
		_, err = other.AuthenticateMachineAccessToken(t.Context(), pair.AccessToken, resource, "first")
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
	})

	t.Run("rotation requires client proof and revokes on verified replay", func(t *testing.T) {
		reg := register(t, "first")
		pair := mint(t, reg)
		replacement, err := svc.RotateMachineRefreshToken(t.Context(), reg.MachineClientCredentials, pair.RefreshToken, resource)
		require.NoError(t, err)
		require.NotEqual(t, pair.RefreshToken, replacement.RefreshToken)
		_, err = svc.RotateMachineRefreshToken(t.Context(), mcpauthsvc.MachineClientCredentials{ClientID: reg.ClientID}, pair.RefreshToken, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidClient)
		_, err = svc.AuthenticateMachineAccessToken(t.Context(), replacement.AccessToken, resource, "first")
		require.NoError(t, err)
		_, err = svc.RotateMachineRefreshToken(t.Context(), reg.MachineClientCredentials, pair.RefreshToken, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
		verifyInactive(t, pair, "first")
		verifyInactive(t, replacement, "first")
		_, err = svc.RotateMachineRefreshToken(t.Context(), reg.MachineClientCredentials, replacement.RefreshToken, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
	})

	t.Run("revocation and disconnect have different scopes", func(t *testing.T) {
		reg := register(t, "first")
		other := register(t, "first")
		first, second := mint(t, reg), mint(t, reg)
		independent := mint(t, other)
		require.NoError(t, svc.RevokeMachineToken(t.Context(), other.MachineClientCredentials, first.AccessToken, resource))
		_, err := svc.AuthenticateMachineAccessToken(t.Context(), first.AccessToken, resource, "first")
		require.NoError(t, err)
		require.NoError(t, svc.RevokeMachineToken(t.Context(), reg.MachineClientCredentials, first.RefreshToken, resource))
		verifyInactive(t, first, "first")
		require.ErrorIs(t, svc.DisconnectMachineClient(t.Context(), "second", "worker", reg.ClientID), mcpauthsvc.ErrInvalidClient)
		_, err = svc.AuthenticateMachineAccessToken(t.Context(), second.AccessToken, resource, "first")
		require.NoError(t, err)
		require.NoError(t, svc.DisconnectMachineClient(t.Context(), "first", "worker", reg.ClientID))
		require.NoError(t, svc.DisconnectMachineClient(t.Context(), "first", "worker", reg.ClientID))
		verifyInactive(t, second, "first")
		_, err = svc.IssueMachineTokens(t.Context(), reg.MachineClientCredentials, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidClient)
		_, err = svc.AuthenticateMachineAccessToken(t.Context(), independent.AccessToken, resource, "first")
		require.NoError(t, err)
	})

	t.Run("authenticates the same key with its current role", func(t *testing.T) {
		reg := register(t, "first")
		pair := mint(t, reg)
		exec(t, "UPDATE api_keys SET role_id=14 WHERE id=15")
		principal, err := svc.AuthenticateMachineAccessToken(t.Context(), pair.AccessToken, resource, "first")
		require.NoError(t, err)
		facts, err := pgauth.Resolve(t.Context(), db, principal.Organization, "", principal.APIKey, model.AuthorizationTarget{})
		require.NoError(t, err)
		require.True(t, facts.Valid)
		require.Equal(t, slug.Slug("replacement"), facts.APIKeyRoleSlug)
		exec(t, "UPDATE roles SET deleted_at=now() WHERE id=14")
		verifyInactive(t, pair, "first")
		_, err = svc.RotateMachineRefreshToken(t.Context(), reg.MachineClientCredentials, pair.RefreshToken, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidClient)
		exec(t, "UPDATE api_keys SET role_id=13 WHERE id=15")
	})

	t.Run("client expiry and key rotation invalidate machine grants", func(t *testing.T) {
		expired := register(t, "first")
		pair := mint(t, expired)
		exec(t, "UPDATE mcp_oauth_machine_clients SET expires_at=now()-interval '1 second' WHERE client_id=$1", expired.ClientID)
		verifyInactive(t, pair, "first")
		_, err := svc.IssueMachineTokens(t.Context(), expired.MachineClientCredentials, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidClient)
		old := register(t, "first")
		pair = mint(t, old)
		exec(t, "UPDATE api_keys SET token_hash='synthetic-rotated' WHERE id=15")
		verifyInactive(t, pair, "first")
		_, err = svc.IssueMachineTokens(t.Context(), old.MachineClientCredentials, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidClient)
		current := register(t, "first")
		pair = mint(t, current)
		exec(t, "UPDATE api_keys SET revoked_at=now() WHERE id=15")
		verifyInactive(t, pair, "first")
		exec(t, `
DELETE FROM api_keys WHERE id=15;
INSERT INTO api_keys (id,organization_id,slug,name,token_hash,token_prefix,role_id)
VALUES (115,10,'worker','Replacement','synthetic-replacement','synthetic',13);
`)
		_, err = svc.IssueMachineTokens(t.Context(), current.MachineClientCredentials, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidClient)
		verifyInactive(t, pair, "first")
	})
}
