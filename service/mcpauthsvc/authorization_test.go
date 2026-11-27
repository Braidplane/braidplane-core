package mcpauthsvc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
)

const (
	testIssuer    = "https://auth.example.test/oauth"
	testResource  = "https://core.example.test/product/mcp"
	testRedirect  = "http://127.0.0.1:8123/callback"
	testVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	testChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

func TestDeploymentDiscovery(t *testing.T) {
	d, err := New(testIssuer, testResource, nil)
	require.NoError(t, err)
	location, resource := d.ProtectedResourceDiscovery()
	require.Equal(t, "https://core.example.test/.well-known/oauth-protected-resource/product/mcp", location)
	require.Equal(t, testResource, resource.Resource)
	require.Equal(t, []string{testIssuer}, resource.AuthorizationServers)
	require.Equal(t, []string{"header"}, resource.BearerMethods)
	location, issuer := d.IssuerDiscovery()
	require.Equal(t, "https://auth.example.test/.well-known/oauth-authorization-server/oauth", location)
	require.Equal(t, testIssuer, issuer.Issuer)
	require.Equal(t, testIssuer+"/authorize", issuer.AuthorizationEndpoint)
	require.Equal(t, testIssuer+"/token", issuer.TokenEndpoint)
	require.Equal(t, testIssuer+"/revoke", issuer.RevocationEndpoint)
	require.Equal(t, []string{"authorization_code", "refresh_token", "client_credentials"}, issuer.GrantTypes)
	require.Equal(t, []string{"none", "client_secret_basic"}, issuer.RevocationAuthMethods)
	require.Equal(t, []string{oauthflow.PKCES256}, issuer.CodeChallengeMethods)
	require.True(t, issuer.ClientIDMetadataSupported)
	require.True(t, issuer.AuthorizationResponseIssuerSupported)

	d, err = New("https://core.example.test/", testResource+"/", nil)
	require.NoError(t, err)
	location, resource = d.ProtectedResourceDiscovery()
	require.Equal(t, "https://core.example.test/.well-known/oauth-protected-resource/product/mcp/", location)
	require.Equal(t, testResource+"/", resource.Resource)
	location, _ = d.IssuerDiscovery()
	require.Equal(t, "https://core.example.test/.well-known/oauth-authorization-server", location)
}

func TestDeploymentBindsAuthorizationAndCodeExchange(t *testing.T) {
	registered := []Client{{ID: "console", Name: "Console", RedirectURIs: []string{testRedirect}}}
	d, err := New(testIssuer, testResource, registered)
	require.NoError(t, err)
	registered[0].RedirectURIs[0] = "https://other.example.test/callback"
	request := AuthorizationRequest{
		ClientID: "console", RedirectURI: testRedirect, Resource: "HTTPS://CORE.EXAMPLE.TEST/product/mcp",
		ResponseType: "code", Challenge: testChallenge, ChallengeMethod: oauthflow.PKCES256,
	}
	client, binding, err := d.ValidateAuthorization(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, testResource, binding.resource)
	require.Equal(t, testIssuer, binding.issuer)
	require.NoError(t, d.ValidateCodeExchange(binding, request.ClientID, testRedirect, testResource, testVerifier))
	client.RedirectURIs[0] = "https://other.example.test/callback"
	_, _, err = d.ValidateAuthorization(t.Context(), request)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		edit func(*AuthorizationRequest)
		want error
	}{
		{"wrong resource path", func(r *AuthorizationRequest) { r.Resource += "/" }, ErrInvalidTarget},
		{"missing resource", func(r *AuthorizationRequest) { r.Resource = "" }, ErrInvalidTarget},
		{"wrong resource origin", func(r *AuthorizationRequest) { r.Resource = testIssuer }, ErrInvalidTarget},
		{"resource query", func(r *AuthorizationRequest) { r.Resource = testResource + "?" }, ErrInvalidTarget},
		{"wrong loopback port", func(r *AuthorizationRequest) { r.RedirectURI = "http://127.0.0.1:8124/callback" }, ErrInvalidRequest},
		{"unregistered client", func(r *AuthorizationRequest) { r.ClientID = "other" }, ErrInvalidClient},
		{"plain challenge", func(r *AuthorizationRequest) { r.ChallengeMethod = "plain" }, ErrInvalidRequest},
		{"malformed challenge", func(r *AuthorizationRequest) { r.Challenge = strings.Repeat("?", 43) }, ErrInvalidRequest},
		{"implicit grant", func(r *AuthorizationRequest) { r.ResponseType = "token" }, ErrInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := request
			tc.edit(&invalid)
			_, _, err := d.ValidateAuthorization(t.Context(), invalid)
			require.ErrorIs(t, err, tc.want)
		})
	}

	require.ErrorIs(t, d.ValidateCodeExchange(binding, "other", testRedirect, testResource, testVerifier), ErrInvalidGrant)
	require.ErrorIs(t, d.ValidateCodeExchange(binding, request.ClientID, testRedirect+"?x=1", testResource, testVerifier), ErrInvalidGrant)
	require.ErrorIs(t, d.ValidateCodeExchange(binding, request.ClientID, testRedirect, testIssuer, testVerifier), ErrInvalidTarget)
	require.ErrorIs(t, d.ValidateCodeExchange(binding, request.ClientID, testRedirect, testResource, strings.Repeat("a", 43)), ErrInvalidGrant)
	require.ErrorIs(t, d.ValidateCodeExchange(binding, request.ClientID, testRedirect, testResource, "short"), ErrInvalidGrant)

	other, err := New("https://other.example.test/oauth", testResource, nil)
	require.NoError(t, err)
	require.ErrorIs(t, other.ValidateCodeExchange(binding, request.ClientID, testRedirect, testResource, testVerifier), ErrInvalidGrant)
}

func TestDeploymentRejectsInvalidRegistration(t *testing.T) {
	for _, redirect := range []string{
		"http://service.example.test/callback",
		"javascript:alert(1)",
		"https://user@service.example.test/callback",
		"https://service.example.test/callback#",
		"https://service.example.test/callback?%63ode=old",
	} {
		_, err := New(testIssuer, testResource, []Client{{ID: "console", Name: "Console", RedirectURIs: []string{redirect}}})
		require.ErrorIs(t, err, ErrInvalidClient, "%s", redirect)
	}
	_, err := New("https://auth.example.test/oauth?tenant=other", testResource, nil)
	require.ErrorIs(t, err, ErrInvalidRequest)
	_, err = New(testIssuer, "http://core.example.test/mcp", nil)
	require.ErrorIs(t, err, ErrInvalidTarget)
	_, err = New(testIssuer, testResource, []Client{{ID: "mcp-machine-reserved", Name: "Console", RedirectURIs: []string{testRedirect}}})
	require.ErrorIs(t, err, ErrInvalidClient)
}
