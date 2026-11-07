package credentialssvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/httpoauth"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHTTPTestService(t *testing.T, handler http.HandlerFunc, grantType string) (*service, *credentialRepo, HTTPOAuthApp) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	transport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = transport })
	repo := newCredentialRepo()
	svc, err := New(testutil.FakeTransactionManager{}, repo, testutil.NewFakeEncryptor(), nil, testutil.FakeClock{NowFn: time.Now}, true)
	require.NoError(t, err)
	app := HTTPOAuthApp{Config: httpoauth.Config{Issuer: server.URL, AuthorizationEndpoint: server.URL + "/authorize", TokenEndpoint: server.URL + "/token", RevocationEndpoint: server.URL + "/revoke", ClientID: "client", GrantType: grantType, Scopes: []string{"read"}}, ClientSecret: "app-secret", Destination: "https://api.example/v1", RedirectURI: "https://braidplane.example/callback", AllowPrivateHosts: true}
	return svc.(*service), repo, app
}

func TestHTTPOAuthCallbackPinsAppAndKeepsRuntimeSecretFree(t *testing.T) {
	calls := 0
	svc, repo, app := newHTTPTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		assert.NoError(t, r.ParseForm())
		_, secret, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "app-secret", secret)
		if r.URL.Path == "/revoke" {
			assert.Equal(t, "refresh-token", r.Form.Get("token"))
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Form.Get("grant_type") == "authorization_code" {
			assert.NotEmpty(t, r.Form.Get("code_verifier"))
		}
		_, _ = fmt.Fprint(w, `{"access_token":"access-token","refresh_token":"refresh-token","token_type":"Bearer","expires_in":60,"scope":"read"}`)
	}, "authorization_code")
	owner := testOwner()
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), ConfigureHTTPOAuthAppParams{Owner: owner, App: app}))
	install, err := svc.ConnectHTTPOAuth(t.Context(), StartOAuthInstallParams{Owner: owner, Provider: "http"})
	require.NoError(t, err)
	require.NotContains(t, install.AuthorizationURL, "app-secret")
	require.NotContains(t, string(repo.installs[0].EncryptedPKCEVerifier), "app-secret")
	_, err = svc.CompleteOAuthCallback(t.Context(), CompleteOAuthCallbackParams{Provider: "http", State: install.State, Code: "provider-code", Issuer: app.Config.Issuer})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	runtime, err := svc.ResolveRuntimeCredential(t.Context(), ResolveRuntimeCredentialParams{Owner: owner})
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(runtime.PlaintextJSON, &payload))
	require.Equal(t, map[string]any{"bearer_token": "access-token", "oauth_destination": app.Destination}, payload)
	require.NotContains(t, string(runtime.PlaintextJSON), "app-secret")
	require.NotContains(t, string(runtime.PlaintextJSON), "refresh-token")
	require.NotContains(t, string(repo.grant.EncryptedTokenSet), "app-secret")
	// Desired settings must never redirect an active grant's refresh or revocation tokens.
	changed := app
	changed.Config.TokenEndpoint = "https://other.example/token"
	changed.Config.RevocationEndpoint = "https://other.example/revoke"
	changed.ClientSecret = "different-secret"
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), ConfigureHTTPOAuthAppParams{Owner: owner, App: changed}))
	repo.grant.AccessTokenExpires = time.Now().Add(-time.Minute)
	_, err = svc.ResolveRuntimeCredential(t.Context(), ResolveRuntimeCredentialParams{Owner: owner})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	_, err = svc.Revoke(t.Context(), RevokeParams{Owner: owner})
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}

func TestHTTPOAuthCallbackRejectsIssuerReplayAndChangedApp(t *testing.T) {
	calls := 0
	svc, _, app := newHTTPTestService(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }, "authorization_code")
	owner := testOwner()
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), ConfigureHTTPOAuthAppParams{Owner: owner, App: app}))
	start := func() ProviderConnectorInstall {
		install, err := svc.StartOAuthInstall(t.Context(), StartOAuthInstallParams{Owner: owner, Provider: "http"})
		require.NoError(t, err)
		return install
	}
	install := start()
	_, err := svc.CompleteOAuthCallback(t.Context(), CompleteOAuthCallbackParams{Provider: "http", State: install.State, Code: "code", Issuer: "https://wrong.example"})
	require.ErrorIs(t, err, ErrProviderInvalidGrant)
	require.Zero(t, calls)
	_, err = svc.ConsumeOAuthInstall(t.Context(), ConsumeOAuthInstallParams{Provider: "http", State: install.State})
	require.NoError(t, err)
	_, err = svc.CompleteOAuthCallback(t.Context(), CompleteOAuthCallbackParams{Provider: "http", State: install.State, Code: "code", Issuer: app.Config.Issuer})
	require.ErrorIs(t, err, ErrOAuthStateConsumed)
	install = start()
	app.ClientSecret = "rotated-secret"
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), ConfigureHTTPOAuthAppParams{Owner: owner, App: app}))
	_, err = svc.CompleteOAuthCallback(t.Context(), CompleteOAuthCallbackParams{Provider: "http", State: install.State, Code: "code", Issuer: app.Config.Issuer})
	require.ErrorIs(t, err, ErrOAuthIntentConflict)
	require.Zero(t, calls)
}

func TestHTTPOAuthMachineGrantRenewalAndPolicy(t *testing.T) {
	calls := 0
	svc, repo, app := newHTTPTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		assert.NoError(t, r.ParseForm())
		assert.Equal(t, "client_credentials", r.Form.Get("grant_type"))
		_, _ = fmt.Fprint(w, `{"access_token":"machine-token","token_type":"Bearer","expires_in":60}`)
	}, "client_credentials")
	owner := testOwner()
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), ConfigureHTTPOAuthAppParams{Owner: owner, App: app}))
	install, err := svc.ConnectHTTPOAuth(t.Context(), StartOAuthInstallParams{Owner: owner, Provider: "http"})
	require.NoError(t, err)
	require.Nil(t, install)
	require.NotEmpty(t, repo.grant.EncryptedTokenSet)
	require.Equal(t, 1, calls)
	_, err = svc.StartOAuthInstall(t.Context(), StartOAuthInstallParams{Owner: owner, Provider: "http"})
	require.True(t, xerror.Is(err, ErrInvalidInput))
	repo.grant.AccessTokenExpires = time.Now().Add(10 * time.Second)
	_, err = svc.ResolveRuntimeCredential(t.Context(), ResolveRuntimeCredentialParams{Owner: owner})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	svc.allowPrivateDataSourceHosts = false
	repo.grant.AccessTokenExpires = time.Now().Add(-time.Minute)
	_, err = svc.ResolveRuntimeCredential(t.Context(), ResolveRuntimeCredentialParams{Owner: owner})
	require.True(t, xerror.Is(err, ErrInvalidInput))
	require.Equal(t, 2, calls)
	require.ErrorIs(t, svc.ConfigureHTTPOAuthApp(t.Context(), ConfigureHTTPOAuthAppParams{Owner: owner, App: app}), ErrInvalidInput)
}

func TestHTTPOAuthAppOwnerAndEncryption(t *testing.T) {
	svc, repo, app := newHTTPTestService(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected provider request") }, "authorization_code")
	owner := testOwner()
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), ConfigureHTTPOAuthAppParams{Owner: owner, App: app}))
	encrypted := repo.httpApps[toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: owner})]
	require.NotContains(t, string(encrypted), "app-secret")
	other := owner
	other.OrgSlug = "other"
	_, err := svc.StartOAuthInstall(t.Context(), StartOAuthInstallParams{Owner: other, Provider: "http"})
	require.ErrorIs(t, err, repocredentials.ErrCredentialNotFound)
	for _, invalid := range []CredentialOwner{{}, testEnvironmentSecretOwner(), {Kind: CredentialOwnerKindOrganizationGitLab, OrgSlug: "acme"}} {
		require.ErrorIs(t, svc.ConfigureHTTPOAuthApp(t.Context(), ConfigureHTTPOAuthAppParams{Owner: invalid, App: app}), ErrInvalidInput)
	}
	data, err := marshalOAuthTokenSet(OAuthTokenSet{AccessToken: "token"})
	require.NoError(t, err)
	require.NotContains(t, string(data), "http_app")
}

func TestHTTPOAuthDraftAcquisitionDoesNotPersist(t *testing.T) {
	calls := 0
	svc, repo, app := newHTTPTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = fmt.Fprint(w, `{"access_token":"draft-token","token_type":"Bearer","expires_in":60}`)
	}, "client_credentials")
	runtime, err := svc.AcquireHTTPOAuthTestCredential(t.Context(), app)
	require.NoError(t, err)
	require.JSONEq(t, `{"bearer_token":"draft-token","oauth_destination":"https://api.example/v1"}`, string(runtime.PlaintextJSON))
	require.Empty(t, repo.httpApps)
	require.Empty(t, repo.installs)
	require.Equal(t, 1, calls)
	app.Config.GrantType = "authorization_code"
	_, err = svc.AcquireHTTPOAuthTestCredential(t.Context(), app)
	require.ErrorIs(t, err, ErrInvalidInput)
	require.Equal(t, 1, calls)
}

// A failed source lock must prevent an install from capturing an app without its revision.
func TestHTTPOAuthInstallRejectsUnavailableSourceSnapshot(t *testing.T) {
	svc, repo, app := newHTTPTestService(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected provider request") }, "authorization_code")
	owner := testOwner()
	require.NoError(t, svc.ConfigureHTTPOAuthApp(t.Context(), ConfigureHTTPOAuthAppParams{Owner: owner, App: app}))
	svc.repo = unavailableHTTPSourceRepo{Repository: repo}
	_, err := svc.StartOAuthInstall(t.Context(), StartOAuthInstallParams{Owner: owner, Provider: "http"})
	require.ErrorIs(t, err, repocredentials.ErrCredentialNotFound)
	require.Empty(t, repo.installs)
}

type unavailableHTTPSourceRepo struct{ repocredentials.Repository }

func (unavailableHTTPSourceRepo) LockOAuthGrantIntent(context.Context, postgres.DBTX, repocredentials.ResolveRuntimeCredentialParams) (repocredentials.OAuthGrantIntentState, error) {
	return repocredentials.OAuthGrantIntentState{}, repocredentials.ErrCredentialNotFound
}
