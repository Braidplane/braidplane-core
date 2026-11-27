package e2e

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/mcpauthsvc"
)

func TestUserTokenLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	db := e2edb.OpenIsolatedPostgresPool(t, "MCP user token lifecycle")
	exec := func(t *testing.T, statement string, args ...any) {
		t.Helper()
		_, err := db.Exec(t.Context(), statement, args...)
		require.NoError(t, err)
	}
	exec(t, `
INSERT INTO organizations (id,name,slug) VALUES (10,'First','first'),(20,'Second','second');
INSERT INTO roles (id,organization_id,name,slug) VALUES (13,10,'First','first'),(23,20,'Second','second');
INSERT INTO users (id,slug,email,role,verified_at) VALUES
    (18,'member','member@example.test','super_admin',now()),
    (28,'outsider','outsider@example.test','super_admin',now()),
    (38,'unverified','unverified@example.test','user',NULL);
INSERT INTO organization_members (id,organization_id,user_id,role_id) VALUES (19,10,18,13),(29,20,18,23);
`)
	const (
		issuer   = "https://auth.example.test/oauth"
		resource = "https://core.example.test/mcp"
		redirect = "https://client.example.test/callback"
		clientID = "console"
		verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	)
	clients := []mcpauthsvc.Client{{ID: clientID, Name: "Console", RedirectURIs: []string{redirect}}}
	deployment, err := mcpauthsvc.New(issuer, resource, clients)
	require.NoError(t, err)
	txm := postgres.NewTransactionManager(db)
	svc, err := mcpauthsvc.NewTokenService(deployment, txm)
	require.NoError(t, err)
	authorization := mcpauthsvc.AuthorizationRequest{
		ClientID: clientID, RedirectURI: redirect, Resource: resource, ResponseType: "code",
		Challenge: oauthflow.S256Challenge(verifier), ChallengeMethod: oauthflow.PKCES256,
	}
	consent := mcpauthsvc.BrowserIdentity{User: "member", Organization: "first", SessionID: "synthetic-session", AuthenticationVersion: 1}
	newRedemption := func(t *testing.T, org slug.Slug) mcpauthsvc.CodeRedemption {
		t.Helper()
		requestConsent := consent
		requestConsent.Organization = org
		prompt, err := svc.BeginUserConsent(t.Context(), authorization, "", requestConsent)
		require.NoError(t, err)
		callback, err := svc.CompleteUserConsent(t.Context(), prompt.ID, requestConsent, true)
		require.NoError(t, err)
		uri, err := url.Parse(callback)
		require.NoError(t, err)
		code := uri.Query().Get("code")
		require.NotEmpty(t, code)
		return mcpauthsvc.CodeRedemption{Code: code, ClientID: clientID, RedirectURI: redirect, Resource: resource, Verifier: verifier}
	}
	redeem := func(t *testing.T, request mcpauthsvc.CodeRedemption) mcpauthsvc.TokenPair {
		t.Helper()
		tokens, err := svc.RedeemUserCode(t.Context(), request)
		require.NoError(t, err)
		require.Greater(t, time.Until(tokens.AccessExpiresAt), time.Duration(0))
		return tokens
	}
	assertInactive := func(t *testing.T, tokens mcpauthsvc.TokenPair, org slug.Slug) {
		t.Helper()
		_, err := svc.AuthenticateUserAccessToken(t.Context(), tokens.AccessToken, resource, org)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
		_, err = svc.RotateUserRefreshToken(t.Context(), tokens.RefreshToken, clientID, resource)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
	}

	t.Run("verified session and immutable tenant scope", func(t *testing.T) {
		for _, denied := range []mcpauthsvc.BrowserIdentity{
			{User: "outsider", Organization: "first", AuthenticationVersion: 1},
			{User: "unverified", AuthenticationVersion: 1},
			{User: consent.User, Organization: consent.Organization, AuthenticationVersion: 2},
			{User: consent.User, Organization: "missing", AuthenticationVersion: 1},
		} {
			denied.SessionID = consent.SessionID
			_, err := svc.BeginUserConsent(t.Context(), authorization, "", denied)
			require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
		}
		request := newRedemption(t, "first")
		for _, mutate := range []func(*mcpauthsvc.CodeRedemption){
			func(r *mcpauthsvc.CodeRedemption) { r.ClientID = "wrong-client" },
			func(r *mcpauthsvc.CodeRedemption) { r.RedirectURI += "?wrong=1" },
			func(r *mcpauthsvc.CodeRedemption) { r.Verifier += "x" },
		} {
			invalid := request
			mutate(&invalid)
			_, err := svc.RedeemUserCode(t.Context(), invalid)
			require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
		}
		invalid := request
		invalid.Resource = issuer
		_, err := svc.RedeemUserCode(t.Context(), invalid)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidTarget)
		tokens := redeem(t, request)
		principal, err := svc.AuthenticateUserAccessToken(t.Context(), tokens.AccessToken, resource, "first")
		require.NoError(t, err)
		require.Equal(t, consent.User, principal.User)
		require.Equal(t, "first", principal.Organization.String())
		require.EqualValues(t, 19, principal.MemberID)
		_, err = svc.AuthenticateUserAccessToken(t.Context(), tokens.AccessToken, resource, "second")
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
		_, err = svc.AuthenticateUserAccessToken(t.Context(), tokens.RefreshToken, resource, "first")
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
		_, err = svc.AuthenticateUserAccessToken(t.Context(), tokens.AccessToken, issuer, "first")
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)

		global := redeem(t, newRedemption(t, ""))
		principal, err = svc.AuthenticateUserAccessToken(t.Context(), global.AccessToken, resource, "")
		require.NoError(t, err)
		require.Zero(t, principal.MemberID)
		_, err = svc.AuthenticateUserAccessToken(t.Context(), global.AccessToken, resource, "first")
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
	})

	t.Run("credentials are usable only at their issuer and resource", func(t *testing.T) {
		const otherIssuer, otherResource = "https://other.example.test/oauth", "https://other.example.test/mcp"
		request := newRedemption(t, "first")
		tokens := redeem(t, request)
		for _, tc := range []struct{ issuer, audience string }{
			{otherIssuer, resource}, {issuer, otherResource},
		} {
			otherDeployment, err := mcpauthsvc.New(tc.issuer, tc.audience, clients)
			require.NoError(t, err)
			other, err := mcpauthsvc.NewTokenService(otherDeployment, txm)
			require.NoError(t, err)
			_, err = other.AuthenticateUserAccessToken(t.Context(), tokens.AccessToken, tc.audience, "first")
			require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
			_, err = other.RotateUserRefreshToken(t.Context(), tokens.RefreshToken, clientID, tc.audience)
			require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
		}
	})

	t.Run("consent survives issuance rollback and only one approval succeeds", func(t *testing.T) {
		prompt, err := svc.BeginUserConsent(t.Context(), authorization, "", consent)
		require.NoError(t, err)
		exec(t, "ALTER TABLE mcp_oauth_credentials ADD CONSTRAINT fixture_reject_code CHECK (kind <> 'code') NOT VALID")
		callback, err := svc.CompleteUserConsent(t.Context(), prompt.ID, consent, true)
		exec(t, "ALTER TABLE mcp_oauth_credentials DROP CONSTRAINT fixture_reject_code")
		require.Error(t, err)
		require.Empty(t, callback)

		type outcome struct {
			callback string
			err      error
		}
		start, results := make(chan struct{}), make(chan outcome, 2)
		for range 2 {
			go func() {
				<-start
				location, err := svc.CompleteUserConsent(t.Context(), prompt.ID, consent, true)
				results <- outcome{callback: location, err: err}
			}()
		}
		close(start)
		var codes []string
		for range 2 {
			result := <-results
			if result.err != nil {
				require.ErrorIs(t, result.err, mcpauthsvc.ErrInvalidGrant)
				require.Empty(t, result.callback)
				continue
			}
			location, err := url.Parse(result.callback)
			require.NoError(t, err)
			codes = append(codes, location.Query().Get("code"))
		}
		require.Len(t, codes, 1)
		redeem(t, mcpauthsvc.CodeRedemption{Code: codes[0], ClientID: clientID, RedirectURI: redirect, Resource: resource, Verifier: verifier})
	})

	t.Run("code replay revokes only after proof verification", func(t *testing.T) {
		request := newRedemption(t, "first")
		pair := redeem(t, request)
		invalid := request
		invalid.Verifier += "x"
		_, err := svc.RedeemUserCode(t.Context(), invalid)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
		_, err = svc.AuthenticateUserAccessToken(t.Context(), pair.AccessToken, resource, "first")
		require.NoError(t, err)
		_, err = svc.RedeemUserCode(t.Context(), request)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
		assertInactive(t, pair, "first")
	})

	t.Run("concurrent refresh reuse revokes the entire family", func(t *testing.T) {
		first := redeem(t, newRedemption(t, "first"))
		type outcome struct {
			tokens mcpauthsvc.TokenPair
			err    error
		}
		start, results := make(chan struct{}), make(chan outcome, 2)
		for range 2 {
			go func() {
				<-start
				tokens, err := svc.RotateUserRefreshToken(t.Context(), first.RefreshToken, clientID, resource)
				results <- outcome{tokens: tokens, err: err}
			}()
		}
		close(start)
		var issued []mcpauthsvc.TokenPair
		for range 2 {
			result := <-results
			if result.err != nil {
				require.ErrorIs(t, result.err, mcpauthsvc.ErrInvalidGrant)
				continue
			}
			issued = append(issued, result.tokens)
		}
		require.Len(t, issued, 1)
		require.NotEqual(t, first.RefreshToken, issued[0].RefreshToken)
		assertInactive(t, first, "first")
		assertInactive(t, issued[0], "first")
	})

	t.Run("expiry is enforced in PostgreSQL", func(t *testing.T) {
		expire := func(raw string) {
			exec(t, "UPDATE mcp_oauth_credentials SET expires_at=now()-interval '1 second' WHERE credential_hash=$1", oauthflow.HashState(raw))
		}
		request := newRedemption(t, "first")
		expire(request.Code)
		_, err := svc.RedeemUserCode(t.Context(), request)
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidGrant)
		pair := redeem(t, newRedemption(t, "first"))
		expire(pair.AccessToken)
		expire(pair.RefreshToken)
		assertInactive(t, pair, "first")
	})

	t.Run("disconnect and revocation are scoped to the user tenant and client", func(t *testing.T) {
		first := redeem(t, newRedemption(t, "first"))
		second := redeem(t, newRedemption(t, "second"))
		require.NoError(t, svc.DisconnectUserClient(t.Context(), "outsider", "first", clientID))
		require.NoError(t, svc.RevokeUserToken(t.Context(), first.RefreshToken, "wrong-client", resource))
		_, err := svc.AuthenticateUserAccessToken(t.Context(), first.AccessToken, resource, "first")
		require.NoError(t, err)
		require.NoError(t, svc.DisconnectUserClient(t.Context(), consent.User, "first", clientID))
		assertInactive(t, first, "first")
		_, err = svc.AuthenticateUserAccessToken(t.Context(), second.AccessToken, resource, "second")
		require.NoError(t, err)
		require.NoError(t, svc.RevokeUserToken(t.Context(), second.AccessToken, clientID, resource))
		require.NoError(t, svc.RevokeUserToken(t.Context(), second.RefreshToken, clientID, resource))
		assertInactive(t, second, "second")
	})

	t.Run("redemption rolls back when token issuance fails", func(t *testing.T) {
		request := newRedemption(t, "first")
		exec(t, "ALTER TABLE mcp_oauth_credentials ADD CONSTRAINT fixture_reject_refresh CHECK (kind <> 'refresh') NOT VALID")
		tokens, err := svc.RedeemUserCode(t.Context(), request)
		exec(t, "ALTER TABLE mcp_oauth_credentials DROP CONSTRAINT fixture_reject_refresh")
		require.Error(t, err)
		require.False(t, xerror.Is(err, mcpauthsvc.ErrInvalidGrant))
		require.Empty(t, tokens)
		var issued int
		err = db.QueryRow(t.Context(), `
SELECT count(*) FROM mcp_oauth_credentials
WHERE grant_id = (SELECT grant_id FROM mcp_oauth_credentials WHERE credential_hash=$1) AND kind <> 'code'
`, oauthflow.HashState(request.Code)).Scan(&issued)
		require.NoError(t, err)
		require.Zero(t, issued)
		redeem(t, request)
	})

	t.Run("membership replacement and authentication version invalidate grants", func(t *testing.T) {
		first := redeem(t, newRedemption(t, "first"))
		second := redeem(t, newRedemption(t, "second"))
		exec(t, `
DELETE FROM organization_members WHERE id=19;
INSERT INTO organization_members (id,organization_id,user_id,role_id) VALUES (119,10,18,13);
`)
		assertInactive(t, first, "first")
		_, err := svc.AuthenticateUserAccessToken(t.Context(), second.AccessToken, resource, "second")
		require.NoError(t, err)
		exec(t, "UPDATE users SET authentication_version=authentication_version+1 WHERE id=18")
		assertInactive(t, second, "second")
	})
}
