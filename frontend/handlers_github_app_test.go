package frontend

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestGitHubAppAndProofReceiptRoutesAreAvailable(t *testing.T) {
	t.Parallel()
	router, err := NewRouter("http://localhost:3001", slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})
	routes := map[string]bool{}
	for _, route := range app.GetRoutes() {
		if route.Method == http.MethodGet {
			routes[route.Path] = true
		}
	}
	require.True(t, routes["/organizations/:slug/github"])
	require.True(t, routes["/orgs/:slug/projects/:projectSlug/environments/:environmentSlug/proof-receipts/:receiptSlug"])
}

func TestRepositoryGitHubDeniedCallbackRecoversCLIProjectWithoutCookies(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/github-app-connection/callback", request.URL.Path)
		assert.Equal(t, "synthetic-state", request.URL.Query().Get("state"))
		assert.Equal(t, "access_denied", request.URL.Query().Get("error"))
		assert.Empty(t, request.URL.Query().Get("code"))
		assert.Empty(t, request.URL.Query().Get("installation_id"))
		assert.NotContains(t, request.URL.RawQuery, "private-provider-detail")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/organizations/acme/projects/pipelines?section=repository&repository_path=create&repository_setup=failed")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Authorization declined"}}`))
	}))
	defer backend.Close()
	client, err := api.NewClientWithResponses(backend.URL)
	require.NoError(t, err)
	router := &Router{apiClient: client, secureCookies: true}
	app := fiber.New()
	app.Get("/github/callback", router.handleOrganizationGitHubCallback)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/github/callback?state=synthetic-state&error=access_denied&error_description=private-provider-detail", nil)
	response, err := app.Test(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	require.Equal(t, "/organizations/acme/projects/pipelines?section=repository&repository_path=create&repository_setup=failed", response.Header.Get("Location"))
	for _, cookie := range response.Cookies() {
		if cookie.Name == githubInstallProjectCookie || cookie.Name == githubInstallOrganizationCookie {
			require.Negative(t, cookie.MaxAge)
		}
	}
}

func TestRepositorySetupFailureRedirectOnlyUsesLocalProjectIdentity(t *testing.T) {
	want := "/organizations/acme/projects/pipelines?section=repository&repository_path=create&repository_setup=failed"
	for _, target := range []string{"/organizations/acme/projects/pipelines?token=private", "https://app.example.test/organizations/acme/projects/pipelines"} {
		require.Equal(t, want, repositorySetupFailureRedirect(target, "app.example.test:8443"))
	}
	for _, target := range []string{"https://evil.test/organizations/acme/projects/pipelines", "//evil.test/organizations/acme/projects/pipelines", "/account", "/organizations/acme/projects/pipelines/delete", "/organizations/acme/projects/Invalid", "/organizations/acme/projects/../account"} {
		require.Empty(t, repositorySetupFailureRedirect(target, "app.example.test"), target)
	}
}

func TestOrganizationGitHubCallbackRedirectsCompletionFailureToOrganization(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	t.Cleanup(backend.Close)
	client, err := api.NewClientWithResponses(backend.URL)
	require.NoError(t, err)
	router := &Router{apiClient: client, secureCookies: true}
	app := fiber.New()
	app.Get("/github/callback", router.handleOrganizationGitHubCallback)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/github/callback?installation_id=9&state=state&code=code", nil)
	req.AddCookie(&http.Cookie{Name: githubInstallOrganizationCookie, Value: "acme"})
	response, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	require.Equal(t, "/organizations/acme/github?connect_failed=true", response.Header.Get("Location"))
	require.Contains(t, response.Header.Get("Set-Cookie"), "secure")
	require.NoError(t, response.Body.Close())

	response, err = app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/github/callback?installation_id=bad", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.NoError(t, response.Body.Close())
}

func TestOrganizationGitHubCallbackAllowsExistingInstallationWithoutID(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/github-app-connection/callback", request.URL.Path)
		assert.Equal(t, "state", request.URL.Query().Get("state"))
		assert.Equal(t, "code", request.URL.Query().Get("code"))
		assert.Empty(t, request.URL.Query().Get("installation_id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"organization_slug":"acme","installation_id":9,"account_id":2,"account_login":"acme","account_type":"Organization","repository_selection":"selected","repositories":[]}`))
	}))
	t.Cleanup(backend.Close)
	client, err := api.NewClientWithResponses(backend.URL)
	require.NoError(t, err)
	router := &Router{apiClient: client, secureCookies: true}
	app := fiber.New()
	app.Get("/github/callback", router.handleOrganizationGitHubCallback)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/github/callback?state=state&code=code", nil)
	req.AddCookie(&http.Cookie{Name: githubInstallOrganizationCookie, Value: "acme"})
	response, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	require.Equal(t, "/organizations/acme/github?connected=true", response.Header.Get("Location"))
}

func TestOrganizationGitHubAuthorizeExistingPostsAccountLogin(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/organizations/acme/github-app-connection/authorize", request.URL.Path)
		var body struct {
			AccountLogin string `json:"account_login"`
		}
		assert.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		assert.Equal(t, "Acme", body.AccountLogin)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_url":"https://github.com/login/oauth/authorize?state=state"}`))
	}))
	t.Cleanup(backend.Close)
	client, err := api.NewClientWithResponses(backend.URL)
	require.NoError(t, err)
	router := &Router{apiClient: client, secureCookies: true}
	app := fiber.New()
	app.Post("/organizations/:slug/github/authorize-existing", router.handleOrganizationGitHubAuthorizeExisting)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/github/authorize-existing", strings.NewReader("account_login=Acme"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	require.Equal(t, "https://github.com/login/oauth/authorize?state=state", response.Header.Get("Location"))
	for _, cookie := range response.Cookies() {
		if cookie.Name == githubInstallProjectCookie {
			require.Negative(t, cookie.MaxAge)
			require.True(t, cookie.HttpOnly)
			require.True(t, cookie.Secure)
			return
		}
	}
	t.Fatal("existing-install authorization must clear stale repository setup context")
}

func TestOrganizationGitHubAuthorizeExistingPreservesClientErrors(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			t.Cleanup(backend.Close)
			client, err := api.NewClientWithResponses(backend.URL)
			require.NoError(t, err)
			router := &Router{apiClient: client}
			app := fiber.New()
			app.Post("/organizations/:slug/github/authorize-existing", router.handleOrganizationGitHubAuthorizeExisting)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/github/authorize-existing", strings.NewReader("account_login=Acme"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, response.Body.Close()) }()
			want := status
			if status == http.StatusInternalServerError {
				want = http.StatusServiceUnavailable
			}
			require.Equal(t, want, response.StatusCode)
		})
	}
}

func TestRepositoryGitHubCallbackReturnsToProject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		location  string
		noCookies bool
	}{
		{"interrupted consent", http.StatusBadRequest, `{}`, "/organizations/acme/projects/pipelines?section=repository&repository_path=create&repository_setup=failed", false},
		{"attached repository", http.StatusOK, `{"organization_slug":"acme","repository_setup":{"project_slug":"pipelines","status":"attached"}}`, "/organizations/acme/projects/pipelines?section=repository&repository_setup=complete", false},
		{"CLI-started repository without UI context cookies", http.StatusOK, `{"organization_slug":"acme","repository_setup":{"project_slug":"pipelines","status":"attached"}}`, "/organizations/acme/projects/pipelines?section=repository&repository_setup=complete", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(backend.Close)
			client, err := api.NewClientWithResponses(backend.URL)
			require.NoError(t, err)
			router := &Router{apiClient: client, secureCookies: true}
			app := fiber.New()
			app.Get("/github/callback", router.handleOrganizationGitHubCallback)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/github/callback?installation_id=9&state=state&code=code", nil)
			if !tc.noCookies {
				req.AddCookie(&http.Cookie{Name: githubInstallOrganizationCookie, Value: "acme"})
				req.AddCookie(&http.Cookie{Name: githubInstallProjectCookie, Value: "pipelines"})
			}
			response, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			require.Equal(t, http.StatusSeeOther, response.StatusCode)
			require.Equal(t, tc.location, response.Header.Get("Location"))
			for _, cookie := range response.Cookies() {
				if cookie.Name == githubInstallProjectCookie || cookie.Name == githubInstallOrganizationCookie {
					require.Negative(t, cookie.MaxAge)
					require.True(t, cookie.HttpOnly)
					require.True(t, cookie.Secure)
				}
			}
		})
	}
}
