package datasourcessvc

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/stretchr/testify/require"
)

func httpOAuthCredentials() map[string]any {
	return map[string]any{"oauth": map[string]any{"issuer": "https://issuer.example", "token_endpoint": "https://issuer.example/token", "authorization_endpoint": "https://issuer.example/authorize", "client_id": "customer-app", "client_secret": "customer-secret", "grant_type": "client_credentials", "scopes": []string{"read"}}}
}

func TestHTTPOAuthInputRejectsMixedAndInternalSettings(t *testing.T) {
	t.Parallel()
	svc := &service{}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		config string
	}{
		{"mixed", func(c map[string]any) { c["bearer_token"] = "token" }, `{"base_url":"https://api.example/v1"}`},
		{"internal callback", func(c map[string]any) { c["oauth"].(map[string]any)["redirect_uri"] = "https://other.example/callback" }, `{"base_url":"https://api.example/v1"}`},
		{"private endpoint", func(c map[string]any) { c["oauth"].(map[string]any)["token_endpoint"] = "https://127.0.0.1/token" }, `{"base_url":"https://api.example/v1"}`},
		{"HTTP destination", func(map[string]any) {}, `{"base_url":"http://api.example/v1"}`},
		{"missing trusted callback", func(c map[string]any) { c["oauth"].(map[string]any)["grant_type"] = "authorization_code" }, `{"base_url":"https://api.example/v1"}`},
		{"deployment policy", func(map[string]any) {}, `{"base_url":"https://api.example/v1","allow_private_hosts":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := httpOAuthCredentials()
			tc.mutate(c)
			_, err := svc.httpOAuthApp(model.DataSourceTypeHTTP, []byte(tc.config), c)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "customer-secret")
		})
	}
}

func TestHTTPOAuthCreatePersistsPendingOwnerWithoutStaticSecretOrNetworkTest(t *testing.T) {
	t.Parallel()
	repo := testutil.NewFakeDataSourceRepo()
	var saved *repods.DataSource
	repo.CreateFn = func(_ context.Context, _ postgres.DBTX, org slug.Slug, p *repods.CreateParams) (*repods.DataSource, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("oauth-owner"), p.CredentialSlug)
		saved = &repods.DataSource{Slug: p.Slug, Name: p.Name, Type: p.Type, Config: p.ConfigJSON, ReadOnly: p.ReadOnly, Credential: &repods.CredentialMetadata{Slug: p.CredentialSlug, Kind: "oauth2_grant", Status: "pending", Provider: "http"}}
		return saved, nil
	}
	repo.FindBySlugFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repods.DataSource, error) {
		return saved, nil
	}
	svc := newTestService(repo, testutil.NewFakeSchemaMetadataRepo(), nil, func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		t.Fatal("pending OAuth must not create a connector")
		return testutil.NewFakeSQLConnector(), nil
	}).(*service)
	creds := svc.credentials.(*fakeCredentialService)
	creds.createPendingOAuthFn = func(_ context.Context, p credentialssvc.CreatePendingOAuthCredentialParams) (credentialssvc.CredentialMetadata, error) {
		require.Equal(t, "http", p.Provider)
		require.Equal(t, slug.Slug("acme"), p.Owner.OrgSlug)
		return credentialssvc.CredentialMetadata{Slug: "oauth-owner"}, nil
	}
	configured := false
	creds.ConfigureHTTPOAuthAppFn = func(_ context.Context, p credentialssvc.ConfigureHTTPOAuthAppParams) error {
		configured = true
		require.Equal(t, "customer-secret", p.App.ClientSecret)
		require.Equal(t, "https://api.example/v1", p.App.Destination)
		return nil
	}
	got, err := svc.Create(t.Context(), "acme", model.UserEventActor("alice"), &CreateParams{Name: "HTTP app", Slug: "http-app", Type: model.DataSourceTypeHTTP, ConfigJSON: []byte(`{"base_url":"https://api.example/v1"}`), Credentials: httpOAuthCredentials(), ReadOnly: true})
	require.NoError(t, err)
	require.True(t, configured)
	require.False(t, got.IsActive)
	require.Equal(t, credentialssvc.CredentialStatusPending, got.Credential.Status)
	require.Empty(t, creds.credentials)
}

func TestHTTPOAuthDraftTestUsesOnlyBoundRuntimeCredential(t *testing.T) {
	t.Parallel()
	svc := newTestService(testutil.NewFakeDataSourceRepo(), testutil.NewFakeSchemaMetadataRepo(), nil, func(_ context.Context, _ model.DataSourceType, _, c []byte) (connector.Connector, error) {
		require.JSONEq(t, `{"bearer_token":"draft-token","oauth_destination":"https://api.example/v1"}`, string(c))
		return testutil.NewFakeSQLConnector(), nil
	}).(*service)
	svc.credentials.(*fakeCredentialService).AcquireHTTPOAuthTestCredentialFn = func(_ context.Context, app credentialssvc.HTTPOAuthApp) (credentialssvc.RuntimeCredential, error) {
		require.Equal(t, "customer-secret", app.ClientSecret)
		return credentialssvc.RuntimeCredential{PlaintextJSON: []byte(`{"bearer_token":"draft-token","oauth_destination":"https://api.example/v1"}`)}, nil
	}
	require.NoError(t, svc.TestConnection(t.Context(), "acme", &TestConnectionParams{Type: model.DataSourceTypeHTTP, ConfigJSON: []byte(`{"base_url":"https://api.example/v1"}`), Credentials: httpOAuthCredentials()}))
}
