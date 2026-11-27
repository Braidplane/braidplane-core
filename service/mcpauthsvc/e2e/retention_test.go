package e2e

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/retention"
	"github.com/Braidplane/braidplane-core/postgres/wishlistregistrations"
	"github.com/Braidplane/braidplane-core/service/mcpauthsvc"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
)

func TestMCPOAuthRetentionPreservesLiveGrantRevocationAndReplay(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	db := e2edb.OpenIsolatedPostgresPool(t, "MCP OAuth retention")
	exec := func(query string, args ...any) {
		_, err := db.Exec(t.Context(), query, args...)
		require.NoError(t, err)
	}
	exec(`
INSERT INTO organizations (id,name,slug) VALUES (10,'First','first');
INSERT INTO roles (id,organization_id,name,slug) VALUES (13,10,'Reader','reader');
INSERT INTO users (id,slug,email,role,verified_at) VALUES (18,'member','member@example.test','user',now());
INSERT INTO organization_members (id,organization_id,user_id,role_id) VALUES (19,10,18,13);
INSERT INTO api_keys (id,organization_id,slug,name,token_hash,token_prefix,role_id,revoked_at)
VALUES (15,10,'worker','Worker','synthetic-worker','synthetic',13,NULL);
`)
	const (
		issuer   = "https://issuer.example.test/oauth"
		resource = "https://api.example.test/mcp"
		redirect = "https://client.example.test/callback"
		clientID = "console"
		verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		org      = "first"
	)
	txm := postgres.NewTransactionManager(db)
	deployment, err := mcpauthsvc.New(issuer, resource, []mcpauthsvc.Client{{ID: clientID, Name: "Console", RedirectURIs: []string{redirect}}})
	require.NoError(t, err)
	tokens, err := mcpauthsvc.NewTokenService(deployment, txm)
	require.NoError(t, err)
	browser := mcpauthsvc.BrowserIdentity{User: "member", Organization: org, SessionID: "synthetic-session", AuthenticationVersion: 1}
	begin := func() string {
		prompt, err := tokens.BeginUserConsent(t.Context(), mcpauthsvc.AuthorizationRequest{
			ClientID: clientID, RedirectURI: redirect, Resource: resource, ResponseType: "code",
			Challenge: oauthflow.S256Challenge(verifier), ChallengeMethod: oauthflow.PKCES256,
		}, "", browser)
		require.NoError(t, err)
		return prompt.ID
	}
	approve := func(nonce string) string {
		callback, err := tokens.CompleteUserConsent(t.Context(), nonce, browser, mcpauthsvc.ConsentReadOnly)
		require.NoError(t, err)
		uri, err := url.Parse(callback)
		require.NoError(t, err)
		code := uri.Query().Get("code")
		require.NotEmpty(t, code)
		return code
	}
	redeem := func(code string) mcpauthsvc.TokenPair {
		pair, err := tokens.RedeemUserCode(t.Context(), mcpauthsvc.CodeRedemption{
			Code: code, ClientID: clientID, RedirectURI: redirect, Resource: resource, Verifier: verifier,
		})
		require.NoError(t, err)
		return pair
	}
	registerMachine := func() mcpauthsvc.MachineRegistration {
		registration, err := tokens.RegisterMachineClient(t.Context(), org, "worker")
		require.NoError(t, err)
		return registration
	}
	issueMachine := func(registration mcpauthsvc.MachineRegistration) mcpauthsvc.TokenPair {
		pair, err := tokens.IssueMachineTokens(t.Context(), registration.MachineClientCredentials, resource)
		require.NoError(t, err)
		return pair
	}
	assertRow := func(query, value string, want bool) {
		var exists bool
		require.NoError(t, db.QueryRow(t.Context(), query, value).Scan(&exists))
		require.Equal(t, want, exists, "%s", query)
	}
	assertCredential := func(raw string, want bool) {
		assertRow(`SELECT EXISTS (SELECT 1 FROM mcp_oauth_credentials WHERE credential_hash=$1)`, oauthflow.HashState(raw), want)
	}

	usedCode := approve(begin())
	original := redeem(usedCode)
	rotated, err := tokens.RotateUserRefreshToken(t.Context(), original.RefreshToken, clientID, resource)
	require.NoError(t, err)
	accessFamily := redeem(approve(begin()))
	accessReplacement, err := tokens.RotateUserRefreshToken(t.Context(), accessFamily.RefreshToken, clientID, resource)
	require.NoError(t, err)
	unusedCode := approve(begin())
	exec(`UPDATE mcp_oauth_credentials SET expires_at=now()-interval '1 second' WHERE credential_hash=ANY($1)`, []string{
		oauthflow.HashState(usedCode), oauthflow.HashState(original.RefreshToken),
		oauthflow.HashState(accessFamily.AccessToken), oauthflow.HashState(unusedCode),
	})

	expiredFamily := redeem(approve(begin()))
	exec(`UPDATE mcp_oauth_grants g SET expires_at=now()-interval '1 second'
FROM mcp_oauth_credentials c WHERE c.grant_id=g.grant_id AND c.credential_hash=$1`, oauthflow.HashState(expiredFamily.AccessToken))
	revokedFamily := redeem(approve(begin()))
	require.NoError(t, tokens.RevokeUserToken(t.Context(), revokedFamily.AccessToken, clientID, resource))
	pending, expiredConsent := begin(), begin()
	exec(`UPDATE mcp_oauth_pending_consents SET expires_at=now()-interval '1 second' WHERE nonce_hash=$1`, oauthflow.HashState(expiredConsent))

	expiredClient := registerMachine()
	expiredMachinePair := issueMachine(expiredClient)
	exec(`UPDATE mcp_oauth_machine_clients SET expires_at=now()-interval '1 second' WHERE client_id=$1`, expiredClient.ClientID)
	liveClient := registerMachine()
	liveMachinePair := issueMachine(liveClient)
	disconnected := registerMachine()
	require.NoError(t, tokens.DisconnectMachineClient(t.Context(), org, "worker", disconnected.ClientID))

	maintenance := retentionsvc.New(txm, retention.New(), wishlistregistrations.NewRepository(), clock.New())
	job := retentionsvc.MaintenanceExecution{SourceJobID: 42, Attempt: 1, StartedAt: time.Now()}
	result, err := maintenance.PruneExpiredCustomerData(t.Context(), job)
	require.NoError(t, err)
	require.EqualValues(t, 5, result.MCPOAuthRowsDeleted)
	job.Attempt++
	result, err = maintenance.PruneExpiredCustomerData(t.Context(), job)
	require.NoError(t, err)
	require.Zero(t, result.MCPOAuthRowsDeleted)

	assertCredential(expiredFamily.RefreshToken, false)
	assertCredential(revokedFamily.RefreshToken, false)
	assertCredential(expiredMachinePair.AccessToken, false)
	assertCredential(unusedCode, false)
	assertRow(`SELECT EXISTS (SELECT 1 FROM mcp_oauth_machine_clients WHERE client_id=$1)`, expiredClient.ClientID, false)
	assertRow(`SELECT EXISTS (SELECT 1 FROM mcp_oauth_pending_consents WHERE nonce_hash=$1)`, oauthflow.HashState(expiredConsent), false)
	redeem(approve(pending))
	_, err = tokens.AuthenticateMachineAccessToken(t.Context(), liveMachinePair.AccessToken, resource, org)
	require.NoError(t, err)
	require.NoError(t, tokens.DisconnectMachineClient(t.Context(), org, "worker", disconnected.ClientID))

	assertCredential(usedCode, true)
	assertCredential(original.RefreshToken, true)
	_, err = tokens.AuthenticateUserAccessToken(t.Context(), rotated.AccessToken, resource, org)
	require.NoError(t, err)
	_, err = tokens.RotateUserRefreshToken(t.Context(), original.RefreshToken, clientID, resource)
	require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
	_, err = tokens.AuthenticateUserAccessToken(t.Context(), rotated.AccessToken, resource, org)
	require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)

	assertCredential(accessFamily.AccessToken, true)
	_, err = tokens.AuthenticateUserAccessToken(t.Context(), accessReplacement.AccessToken, resource, org)
	require.NoError(t, err)
	require.NoError(t, tokens.RevokeUserToken(t.Context(), accessFamily.AccessToken, clientID, resource))
	_, err = tokens.AuthenticateUserAccessToken(t.Context(), accessReplacement.AccessToken, resource, org)
	require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
}
