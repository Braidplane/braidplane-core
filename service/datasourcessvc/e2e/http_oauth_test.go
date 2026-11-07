package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/testutil/datasourcee2e"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPOAuthPublicSetupConnectReplaceAndStaticFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	tokenCalls, healthCalls := 0, 0
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls++
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			assert.NoError(t, r.ParseForm())
			_, secret, ok := r.BasicAuth()
			assert.True(t, ok)
			assert.Equal(t, "customer-secret", secret)
			_, _ = fmt.Fprint(w, `{"access_token":"provider-token","refresh_token":"provider-refresh","token_type":"Bearer","expires_in":3600,"scope":"read"}`)
		case "/v1/health":
			healthCalls++
			assert.Contains(t, []string{"Bearer provider-token", "Bearer static-token"}, r.Header.Get("Authorization"))
			_, _ = fmt.Fprint(w, `{"ok":true}`)
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer provider.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = provider.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	pool := e2edb.OpenPostgresPool(t, "HTTP OAuth public data-source lifecycle")
	org := datasourcee2e.UniqueSlug(t, "http-oauth-org")
	other := datasourcee2e.UniqueSlug(t, "http-oauth-other-org")
	datasourcee2e.SeedOrganization(t, pool, org)
	datasourcee2e.SeedOrganization(t, pool, other)
	stack := datasourcee2e.NewStack(t, pool)
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	credentials, err := credentialssvc.New(stack.Txm, repocredentials.New(), cipher, nil, clock.New(), true)
	require.NoError(t, err)
	svc, err := datasourcessvc.New(stack.Txm, stack.Repo, stack.SchemaRepo, nil, credentials, datasourcessvc.NewNoopGitHubAppManager(), datasourcessvc.NewNoopGitLabWebhookManager(), stack.Admission, connector.NewConnectorWithTransportPolicy(connector.NewConnector, true), eventtest.NewProducer(nil), datasourcee2e.SchemaImportProducerStub{}, datasourcessvc.ServiceConfig{HTTPOAuthRedirectURI: "https://api.example/provider-connectors/http/callback", CustomerConfigPolicy: datasourcessvc.CustomerConfigPolicy{AllowPrivateDataSourceHosts: true, AllowPlaintextConnectors: true}})
	require.NoError(t, err)
	for _, grant := range []string{"client_credentials", "authorization_code"} {
		t.Run(grant, func(t *testing.T) {
			source := datasourcee2e.UniqueSlug(t, "http-oauth-source")
			config, err := json.Marshal(map[string]any{"base_url": provider.URL + "/v1", "health_path": "health", "allow_private_hosts": true})
			require.NoError(t, err)
			app := map[string]any{"issuer": provider.URL, "authorization_endpoint": provider.URL + "/authorize", "token_endpoint": provider.URL + "/token", "client_id": "customer-app", "client_secret": "customer-secret", "grant_type": grant, "scopes": []string{"read"}}
			input := map[string]any{"oauth": app}
			beforeTokens, beforeHealth := tokenCalls, healthCalls
			created, err := svc.Create(t.Context(), org, model.ExternalEventActor(), &datasourcessvc.CreateParams{Name: "HTTP OAuth", Slug: source, Type: model.DataSourceTypeHTTP, ConfigJSON: config, Credentials: input, ReadOnly: true})
			require.NoError(t, err)
			require.False(t, created.IsActive)
			require.Equal(t, credentialssvc.CredentialStatusPending, created.Credential.Status)
			require.Equal(t, beforeTokens, tokenCalls)
			require.Equal(t, beforeHealth, healthCalls)
			_, err = svc.ConnectHTTPOAuth(t.Context(), other, source, model.ExternalEventActor())
			require.Error(t, err)
			connect := func() {
				result, err := svc.ConnectHTTPOAuth(t.Context(), org, source, model.ExternalEventActor())
				require.NoError(t, err)
				if grant == "authorization_code" {
					require.NotNil(t, result.Install)
					authURL, err := url.Parse(result.Install.AuthorizationURL)
					require.NoError(t, err)
					require.NotEmpty(t, authURL.Query().Get("code_challenge"))
					require.Equal(t, "S256", authURL.Query().Get("code_challenge_method"))
					_, err = svc.CompleteProviderOAuthCallback(t.Context(), credentialssvc.CompleteOAuthCallbackParams{Provider: "http", State: authURL.Query().Get("state"), Code: "provider-code", Issuer: provider.URL, Actor: model.ExternalEventActor()})
					require.NoError(t, err)
				} else {
					require.Nil(t, result.Install)
					require.True(t, result.DataSource.IsActive)
				}
			}
			connect()
			active, err := svc.FindBySlug(t.Context(), org, source)
			require.NoError(t, err)
			require.True(t, active.IsActive)
			old := active.Credential.Slug
			replaced, err := svc.UpdateCredentials(t.Context(), org, source, model.ExternalEventActor(), &datasourcessvc.UpdateCredentialsParams{Credentials: input})
			require.NoError(t, err)
			require.False(t, replaced.IsActive)
			require.Equal(t, credentialssvc.CredentialStatusPending, replaced.Credential.Status)
			require.NotEqual(t, old, replaced.Credential.Slug)
			datasourcee2e.AssertStoredCredentialState(t, pool, org, old, "rotated", 0)
			connect()
			// Static HTTP authentication remains a supported replacement and retires the OAuth grant.
			static, err := svc.UpdateCredentials(t.Context(), org, source, model.ExternalEventActor(), &datasourcessvc.UpdateCredentialsParams{Credentials: map[string]any{"bearer_token": "static-token"}})
			require.NoError(t, err)
			require.True(t, static.IsActive)
			require.Equal(t, credentialssvc.CredentialKindStaticSecret, static.Credential.Kind)
			_, err = svc.ConnectHTTPOAuth(t.Context(), org, source, model.ExternalEventActor())
			require.Error(t, err)
		})
	}
}
