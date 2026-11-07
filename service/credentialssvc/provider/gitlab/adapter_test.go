package gitlab

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

func TestAdapterSupportsGitLabGrantLifecycle(t *testing.T) {
	t.Parallel()

	var grants []url.Values
	var revoked string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				return
			}
			form, err := url.ParseQuery(string(body))
			if !assert.NoError(t, err) {
				return
			}
			grants = append(grants, form)
			assert.Equal(t, "client-id", form.Get("client_id"))
			assert.Equal(t, "client-secret", form.Get("client_secret"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","expires_in":7200,"scope":"read_user api write_repository"}`))
		case "/api/v4/user":
			assert.Equal(t, "Bearer access", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"id":42,"username":"alice","name":"Alice Example","email":"alice@example.com"}`))
		case "/api/v4/projects":
			assert.Equal(t, "Bearer access", r.Header.Get("Authorization"))
			assert.Equal(t, "true", r.URL.Query().Get("membership"))
			_, _ = w.Write([]byte(`[{"id":42,"name":"Pipelines","path_with_namespace":"acme/pipelines","web_url":"https://gitlab.example/acme/pipelines","http_url_to_repo":"https://gitlab.example/acme/pipelines.git","default_branch":"main"}]`))
		case "/api/v4/projects/42":
			assert.Equal(t, "Bearer access", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"id":42,"name":"Pipelines","path_with_namespace":"acme/pipelines","web_url":"https://gitlab.example/acme/pipelines","http_url_to_repo":"https://gitlab.example/acme/pipelines.git","default_branch":"main"}`))
		case "/oauth/revoke":
			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				return
			}
			form, err := url.ParseQuery(string(body))
			if !assert.NoError(t, err) {
				return
			}
			revoked = form.Get("token")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	adapter, err := New(Config{ClientID: "client-id", ClientSecret: "client-secret", RedirectURI: "http://localhost/provider-connectors/gitlab/callback", BaseURL: server.URL, AllowPlaintext: true})
	require.NoError(t, err)

	readPlan := adapter.OAuthScopePlanForInstall(credentialssvc.OAuthScopePlanParams{})
	assert.Equal(t, []string{"read_user", "read_api", "read_repository"}, readPlan.Required)
	writePlan := adapter.OAuthScopePlanForInstall(credentialssvc.OAuthScopePlanParams{IncludeWriteScopes: true})
	assert.Equal(t, []string{"read_user", "api", "write_repository"}, writePlan.Required)
	authorizationURL, err := adapter.AuthorizationURL(t.Context(), credentialssvc.AuthorizationURLParams{State: "state", Scopes: writePlan.Required})
	require.NoError(t, err)
	parsed, err := url.Parse(authorizationURL)
	require.NoError(t, err)
	assert.Equal(t, "/oauth/authorize", parsed.Path)
	assert.Equal(t, "code", parsed.Query().Get("response_type"))
	assert.Equal(t, "read_user api write_repository", parsed.Query().Get("scope"))

	token, err := adapter.ExchangeCode(t.Context(), credentialssvc.ExchangeCodeParams{Code: "code"})
	require.NoError(t, err)
	assert.Equal(t, "42", token.Account.ID)
	assert.Equal(t, "Alice Example", token.Account.Name)
	assert.Equal(t, "alice@example.com", token.Account.Email)
	assert.Equal(t, []string{"read_user", "api", "write_repository"}, token.Scopes)
	_, err = adapter.Refresh(t.Context(), credentialssvc.RefreshParams{RefreshToken: "refresh"})
	require.NoError(t, err)
	require.Len(t, grants, 2)
	assert.Equal(t, "authorization_code", grants[0].Get("grant_type"))
	assert.Equal(t, "refresh_token", grants[1].Get("grant_type"))
	require.NoError(t, adapter.Revoke(t.Context(), credentialssvc.RevokeProviderGrantParams{AccessToken: "access"}))
	assert.Equal(t, "access", revoked)
	repositories, err := adapter.ListRepositories(t.Context(), "access")
	require.NoError(t, err)
	require.Len(t, repositories, 1)
	assert.Equal(t, "42", repositories[0].ID)
	assert.Equal(t, "acme/pipelines", repositories[0].FullName)
	selected, err := adapter.ResolveRepository(t.Context(), "access", "42")
	require.NoError(t, err)
	assert.Equal(t, "https://gitlab.example/acme/pipelines.git", selected.CloneURL)
}

func TestAdapterMapsInvalidGrantWithoutLeakingToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	t.Cleanup(server.Close)
	adapter, err := New(Config{ClientID: "client-id", ClientSecret: "client-secret", RedirectURI: "http://localhost/provider-connectors/gitlab/callback", BaseURL: server.URL, AllowPlaintext: true})
	require.NoError(t, err)

	_, err = adapter.Refresh(t.Context(), credentialssvc.RefreshParams{RefreshToken: "secret-refresh"})
	require.ErrorIs(t, err, credentialssvc.ErrProviderInvalidGrant)
	assert.NotContains(t, err.Error(), "secret-refresh")
}
