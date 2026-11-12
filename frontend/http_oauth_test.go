package frontend

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func httpOAuthTestForm(mode string) url.Values {
	return url.Values{"type": {"http"}, "http_auth_type": {mode}, "http_oauth_issuer": {"https://issuer.example"}, "http_oauth_authorization_endpoint": {"https://issuer.example/authorize"}, "http_oauth_token_endpoint": {"https://issuer.example/token"}, "http_oauth_client_id": {"customer-app"}, "http_oauth_client_secret": {" secret bytes "}, "http_oauth_scopes": {"records.read offline_access"}, "http_oauth_audience": {"api"}}
}

func TestHTTPOAuthFormParsesBothGrantsAndOmitsSecretsFromSSR(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"authorization_code", "client_credentials"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			app := fiber.New()
			app.Post("/form", func(c fiber.Ctx) error {
				credentials, errMsg := parseHTTPCredentialsForm(c)
				require.Empty(t, errMsg)
				require.NotNil(t, credentials.Oauth)
				require.Equal(t, mode, string(credentials.Oauth.GrantType))
				require.Equal(t, " secret bytes ", *credentials.Oauth.ClientSecret)
				require.Equal(t, []string{"records.read", "offline_access"}, *credentials.Oauth.Scopes)
				fields := dataSourceCreateFormDataFromRequest(c)
				require.Equal(t, "customer-app", fields.HTTPAuth.ClientID)
				var b bytes.Buffer
				require.NoError(t, views.HTTPAuthenticationFields(fields.HTTPAuth, "draft-http").Render(c.Context(), &b))
				require.NotContains(t, b.String(), "secret bytes")
				require.Contains(t, b.String(), `id="draft-http-http_oauth_client_secret"`)
				require.Contains(t, b.String(), `id="draft-http-secret_header_value_7"`)
				require.Contains(t, b.String(), `list="draft-http-header-suggestions"`)
				require.Contains(t, b.String(), "customer-app")
				return c.SendStatus(http.StatusOK)
			})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/form", strings.NewReader(httpOAuthTestForm(mode).Encode()))
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}
}

func TestHTTPOAuthFormRejectsInvalidAndMixedInput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(url.Values)
	}{
		{"unsupported grant", func(f url.Values) { f.Set("http_auth_type", "password") }},
		{"missing secret", func(f url.Values) { f.Del("http_oauth_client_secret") }},
		{"mixed bearer", func(f url.Values) { f.Set("bearer_token", "other-secret") }},
		{"mixed API key", func(f url.Values) { f.Set("api_key", "other-secret"); f.Set("api_key_header", "X-Api-Key") }},
		{"mixed basic", func(f url.Values) { f.Set("username", "user") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app := fiber.New()
			app.Post("/form", func(c fiber.Ctx) error {
				_, message := parseHTTPCredentialsForm(c)
				require.NotEmpty(t, message)
				require.NotContains(t, message, "secret bytes")
				return c.SendStatus(http.StatusOK)
			})
			form := httpOAuthTestForm("client_credentials")
			tc.change(form)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/form", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}
}

func TestRouterConnectHTTPOAuthUsesSessionAndRedirectsByGrantOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, response, target string
		status                 int
	}{
		{"consent", `{"data_source":{"slug":"http-app"},"install":{"authorization_url":"https://issuer.example/authorize?state=opaque"}}`, "https://issuer.example/authorize?state=opaque", http.StatusOK},
		{"machine", `{"data_source":{"slug":"http-app","is_active":true}}`, "provider_oauth=success", http.StatusOK},
		{"denied", `{"message":"Permission denied"}`, "error=", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/api/organizations/acme/data-sources/http-app/http-oauth/connect", r.URL.Path)
				assert.NotEmpty(t, r.Header.Get("Cookie"))
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.response)
			}))
			defer backend.Close()
			store := session.NewStore()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orgs/acme/data-sources/http-app/http/oauth", nil)
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			req.Header.Set("HX-Request", "true")
			addCSRFToken(t, app, req)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, 1, calls)
			require.Contains(t, resp.Header.Get("HX-Redirect"), tc.target)
		})
	}
}

func TestHTTPOAuthCallbackFeedbackRequiresActiveStoredGrant(t *testing.T) {
	t.Parallel()
	source := api.DataSource{Type: api.DataSourceTypeHttp, IsActive: true, Credential: &api.CredentialMetadata{Kind: api.Oauth2Grant, Status: api.CredentialStatusActive}}
	feedback := validatedDataSourceOAuthFeedback("success", &source)
	require.Equal(t, "HTTP API connected successfully.", feedback.Message)
	source.IsActive = false
	require.Empty(t, validatedDataSourceOAuthFeedback("success", &source).Message)
	require.Contains(t, validatedDataSourceOAuthFeedback("denied", &source).Message, "canceled")
}

func TestHTTPOAuthCreationOpensSettingsForConnection(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/organizations/acme/data-sources", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"slug":"http-app","name":"HTTP app","type":"http","config":{"base_url":"https://api.example.com/v1"},"credential":{"kind":"oauth2_grant","status":"pending"}}`)
	}))
	defer backend.Close()
	store := session.NewStore()
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	form := httpOAuthTestForm("client_credentials")
	form.Set("name", "HTTP app")
	form.Set("slug", "http-app")
	form.Set("base_url", "https://api.example.com/v1")
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orgs/acme/data-sources", strings.NewReader(form.Encode()))
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, "/orgs/acme/data-sources/http-app?section=settings", resp.Header.Get("HX-Redirect"))
}

func TestStaticHTTPSourceSuppressesOAuthFeedback(t *testing.T) {
	t.Parallel()
	source := api.DataSource{Type: api.DataSourceTypeHttp, IsActive: true}
	for _, outcome := range []string{"success", "denied", "expired", "error"} {
		require.Empty(t, validatedDataSourceOAuthFeedback(outcome, &source).Message)
	}
	source.Credential = &api.CredentialMetadata{Kind: api.StaticSecret, Status: api.CredentialStatusActive}
	for _, outcome := range []string{"success", "denied", "expired", "error"} {
		require.Empty(t, validatedDataSourceOAuthFeedback(outcome, &source).Message)
	}
}
