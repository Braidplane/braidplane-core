package hubspot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

// TestAdapterAuthorizationURLUsesAccessModeScopePlan verifies write scopes require writable access.
func TestAdapterAuthorizationURLUsesAccessModeScopePlan(t *testing.T) {
	t.Parallel()

	adapter, err := New(Config{ClientID: "client-id", ClientSecret: "client-secret", RedirectURI: "http://localhost:3001/provider-connectors/hubspot/callback"})
	require.NoError(t, err)

	for _, tt := range []struct {
		name               string
		includeWriteScopes bool
		optionalScopes     string
	}{
		{name: "read only", optionalScopes: "crm.objects.companies.read crm.objects.deals.read crm.objects.custom.read"},
		{name: "writable", includeWriteScopes: true, optionalScopes: "crm.objects.companies.read crm.objects.deals.read crm.objects.custom.read crm.objects.contacts.write crm.objects.companies.write crm.objects.deals.write crm.objects.custom.write"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plan := adapter.OAuthScopePlanForInstall(credentialssvc.OAuthScopePlanParams{IncludeWriteScopes: tt.includeWriteScopes})
			authorizationURL, err := adapter.AuthorizationURL(t.Context(), credentialssvc.AuthorizationURLParams{State: "state-1", Scopes: plan.Required, OptionalScopes: plan.Optional})
			require.NoError(t, err)
			parsed, err := url.Parse(authorizationURL)
			require.NoError(t, err)
			assert.Equal(t, "client-id", parsed.Query().Get("client_id"))
			assert.Equal(t, "app.hubspot.com", parsed.Host)
			assert.Equal(t, "/oauth/authorize", parsed.Path)
			assert.Equal(t, "state-1", parsed.Query().Get("state"))
			assert.Equal(t, "oauth crm.objects.contacts.read", parsed.Query().Get("scope"))
			assert.Equal(t, tt.optionalScopes, parsed.Query().Get("optional_scopes"))
		})
	}
}

// TestAdapterExchangeAndRefreshNormalizeGrantedScopes verifies token operations retain granted CRM scopes.
func TestAdapterExchangeAndRefreshNormalizeGrantedScopes(t *testing.T) {
	t.Parallel()

	requests := make([]url.Values, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/oauth/2026-03/token", r.URL.Path)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request body: %v", err)
			return
		}
		requests = append(requests, mustParseForm(t, string(body)))
		_, _, hasBasicAuth := r.BasicAuth()
		assert.False(t, hasBasicAuth)
		assert.Equal(t, "client-id", requests[len(requests)-1].Get("client_id"))
		assert.Equal(t, "client-secret", requests[len(requests)-1].Get("client_secret"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","expires_in":1800,"scopes":["oauth","crm.objects.contacts.read","crm.objects.deals.read","crm.objects.custom.read"],"hub_id":12345}`))
	}))
	t.Cleanup(server.Close)

	adapter, err := New(Config{ClientID: "client-id", ClientSecret: "client-secret", RedirectURI: "http://localhost:3001/provider-connectors/hubspot/callback", BaseURL: server.URL, AllowPlaintext: true})
	require.NoError(t, err)

	exchanged, err := adapter.ExchangeCode(context.Background(), credentialssvc.ExchangeCodeParams{Code: "code"})
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{
			"oauth",
			"crm.objects.contacts.read",
			"crm.objects.deals.read",
			"crm.objects.custom.read",
		},
		exchanged.Scopes,
	)
	assert.Equal(t, "12345", exchanged.Account.ID)

	_, err = adapter.Refresh(context.Background(), credentialssvc.RefreshParams{RefreshToken: "refresh"})
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, "authorization_code", requests[0].Get("grant_type"))
	assert.Equal(t, "refresh_token", requests[1].Get("grant_type"))
}

// TestAdapterRevokeUsesVersionedFormEndpoint verifies revocation uses HubSpot's versioned form endpoint.
func TestAdapterRevokeUsesVersionedFormEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/oauth/2026-03/token/revoke", r.URL.Path)
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		form := mustParseForm(t, string(body))
		assert.Equal(t, "client-id", form.Get("client_id"))
		assert.Equal(t, "client-secret", form.Get("client_secret"))
		assert.Equal(t, "refresh", form.Get("token"))
		assert.Equal(t, "refresh_token", form.Get("token_type_hint"))
		_, _, hasBasicAuth := r.BasicAuth()
		assert.False(t, hasBasicAuth)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	adapter, err := New(Config{ClientID: "client-id", ClientSecret: "client-secret", RedirectURI: "http://localhost:3001/provider-connectors/hubspot/callback", BaseURL: server.URL, AllowPlaintext: true})
	require.NoError(t, err)
	require.NoError(t, adapter.Revoke(t.Context(), credentialssvc.RevokeProviderGrantParams{RefreshToken: "refresh"}))
}

// TestAdapterDoesNotMapEmptyBadRequestToInvalidGrant preserves non-grant bad-request diagnostics.
func TestAdapterDoesNotMapEmptyBadRequestToInvalidGrant(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	t.Cleanup(server.Close)
	adapter, err := New(Config{ClientID: "client-id", ClientSecret: "client-secret", RedirectURI: "http://localhost:3001/provider-connectors/hubspot/callback", BaseURL: server.URL, AllowPlaintext: true})
	require.NoError(t, err)

	_, err = adapter.Refresh(context.Background(), credentialssvc.RefreshParams{RefreshToken: "not-for-output"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not-for-output")
	assert.True(t, xerror.Is(err, xerror.InvalidInput(xerror.DataSource)))
	assert.False(t, xerror.Is(err, credentialssvc.ErrProviderInvalidGrant))
}

// mustParseForm parses an HTTP form payload and fails the calling test on invalid encoding.
func mustParseForm(t *testing.T, body string) url.Values {
	t.Helper()
	form, err := url.ParseQuery(body)
	require.NoError(t, err)
	return form
}
