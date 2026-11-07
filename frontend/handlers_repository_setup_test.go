package frontend

import (
	"context"
	"encoding/json"
	"io"
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

func TestRouter_ManagedRepositoryCreateForwardsRequestAndStartsGitHubAuthorization(t *testing.T) {
	t.Parallel()

	var created api.CreateProjectRepositoryRequest
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/organizations/acme-hq/projects/default/repository-setup":
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&created))
			writeJSON(w, `{"slug":"repo-setup","project_slug":"default","data_source_slug":"repo-setup","status":"pending","request":{"provider":"github","owner":"acme","repository_name":"pipelines","tracked_branch":"main","pipeline_dir":"pipelines","create_private_repository":true},"authorization_url":"https://github.com/apps/braidplane/installations/new?state=safe"}`)
		default:
			t.Fatalf("unexpected API request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("mode=managed_create&repository_provider=github&provider_owner=acme&repository_name=pipelines&tracked_branch=main&pipeline_dir=pipelines&create_private_repository=on")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/organizations/acme-hq/projects/default/settings/pipeline-backend?return_to=full&section=repository&repository_path=create", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "https://github.com/apps/braidplane/installations/new?state=safe", resp.Header.Get("Location"))
	require.Equal(t, api.CreateProjectRepositoryRequestProviderGithub, created.Provider)
	require.Equal(t, "acme", created.Owner)
	require.Equal(t, "pipelines", created.RepositoryName)
	require.True(t, bool(created.CreatePrivateRepository))
	require.NotNil(t, created.TrackedBranch)
	require.Equal(t, "main", *created.TrackedBranch)
	require.NotNil(t, created.PipelineDir)
	require.Equal(t, "pipelines", *created.PipelineDir)
	cookies := strings.Join(resp.Header.Values("Set-Cookie"), "\n")
	require.Contains(t, cookies, githubInstallOrganizationCookie+"=acme-hq")
	require.Contains(t, cookies, githubInstallProjectCookie+"=default")
}

func TestRouter_ManagedGitLabCreateRedirectsToConsentWithoutGitHubCookies(t *testing.T) {
	t.Parallel()
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		assert.Equal(t, "POST /api/organizations/acme-hq/projects/default/repository-setup", r.Method+" "+r.URL.Path)
		var request api.CreateProjectRepositoryRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.Equal(t, api.CreateProjectRepositoryRequestProviderGitlab, request.Provider)
		writeJSON(w, `{"slug":"repo-setup","project_slug":"default","data_source_slug":"repo-setup","status":"pending","request":{"provider":"gitlab","owner":"acme","repository_name":"pipelines","create_private_repository":true},"authorization_url":"https://gitlab.com/oauth/authorize?state=opaque-state"}`)
	}))
	t.Cleanup(apiServer.Close)
	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme-hq/projects/default/settings/pipeline-backend?return_to=full&section=repository&repository_path=create", strings.NewReader("mode=managed_create&repository_provider=gitlab&provider_owner=acme&repository_name=pipelines&create_private_repository=on"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "https://gitlab.com/oauth/authorize?state=opaque-state", resp.Header.Get("Location"))
	cookies := strings.Join(resp.Header.Values("Set-Cookie"), "\n")
	require.NotContains(t, cookies, githubInstallOrganizationCookie+"=")
	require.NotContains(t, cookies, githubInstallProjectCookie+"=")
}

func TestRouter_RepositorySetupExplainsInterruptedGitLabConsent(t *testing.T) {
	t.Parallel()
	for _, result := range []struct{ status, message string }{
		{"denied", "GitLab authorization was declined."},
		{"expired", "GitLab authorization expired."},
		{"stale", "This GitLab authorization belongs to an earlier setup."},
		{"error", "GitLab authorization or repository setup could not be completed."},
	} {
		t.Run(result.status, func(t *testing.T) {
			app, cookie := repositorySetupPageApp(t, http.StatusOK, `{"slug":"repo-setup","project_slug":"default","data_source_slug":"repo-setup","status":"pending","request":{"provider":"gitlab","owner":"acme","repository_name":"pipelines","create_private_repository":true}}`, false)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme-hq/projects/default?section=repository&repository_path=create&gitlab_setup="+result.status, nil)
			req.Header.Set("Cookie", cookie)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Contains(t, string(body), result.message)
			require.Contains(t, string(body), "Resume setup")
		})
	}
}

func TestRouter_ChangeRepositoryTargetArchivesExactAttemptAndReturnsToFreshForm(t *testing.T) {
	t.Parallel()
	var request api.AbandonProjectRepositorySetupRequest
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		requirePath := "POST /api/organizations/acme-hq/projects/default/repository-setup/abandon"
		if !assert.Equal(t, requirePath, r.Method+" "+r.URL.Path) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		writeJSON(w, `{"slug":"repo-old","project_slug":"default","data_source_slug":"repo-old","status":"pending","abandoned_at":"2026-10-20T18:00:00Z","request":{"provider":"github","owner":"acme","repository_name":"old","create_private_repository":true}}`)
	}))
	t.Cleanup(apiServer.Close)
	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme-hq/projects/default/settings/pipeline-backend?return_to=full&section=repository&repository_path=create", strings.NewReader("mode=managed_abandon&setup_slug=repo-old&preserve_resources=on"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	response, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	require.Equal(t, "/organizations/acme-hq/projects/default?section=repository&repository_path=create&previous_setup=repo-old", response.Header.Get("Location"))
	require.Equal(t, "repo-old", request.SetupSlug)
	require.True(t, bool(request.PreserveResources))
}

func TestRouter_NewTargetFormShowsPreservedAttemptWithoutReusingItsTarget(t *testing.T) {
	t.Parallel()
	app, authCookie := repositorySetupPageApp(t, http.StatusNoContent, "", false)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme-hq/projects/default?section=repository&repository_path=create&previous_setup=repo-old", nil)
	req.Header.Set("Cookie", authCookie)
	response, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, string(body), "Previous target kept")
	require.Contains(t, string(body), "acme/old-pipelines")
	require.Contains(t, string(body), "https://github.com/acme/old-pipelines.git")
	require.NotContains(t, string(body), `value="old-pipelines"`)
	require.NotContains(t, string(body), `name="mode" value="managed_abandon"`)
}

func TestRouter_ManagedRepositoryCreatePreservesFormValuesOnProviderError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/organizations/acme-hq/projects/default/repository-setup":
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, `{"error":{"message":"provider access denied"}}`)
		case "GET /api/organizations/acme-hq":
			writeJSON(w, `{"name":"Acme HQ","slug":"acme-hq"}`)
		case "GET /api/organizations":
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin"}],"has_more":false}`)
		case "GET /api/organizations/acme-hq/projects/default":
			writeJSON(w, `{"name":"Default","slug":"default"}`)
		case "GET /api/organizations/acme-hq/projects":
			writeJSON(w, `{"projects":[{"name":"Default","slug":"default"}],"has_more":false}`)
		case "GET /api/organizations/acme-hq/data-sources":
			writeJSON(w, `{"data_sources":[],"has_more":false}`)
		case "GET /api/organizations/acme-hq/projects/default/repository-setup":
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/organizations/acme-hq/projects/default/environments":
			writeJSON(w, `{"project_environments":[],"has_more":false}`)
		case "GET /api/organizations/acme-hq/environments":
			writeJSON(w, `{"environments":[],"has_more":false}`)
		case "GET /api/organizations/acme-hq/projects/default/pipelines":
			writeJSON(w, `{"pipelines":[],"has_more":false}`)
		case "GET /api/organizations/acme-hq/projects/default/data-source-refs":
			writeJSON(w, `{"data_source_refs":[],"has_more":false}`)
		default:
			t.Fatalf("unexpected API request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	form := strings.NewReader("mode=managed_create&repository_provider=gitlab&provider_owner=acme%2Fteam&repository_name=customer-pipelines&tracked_branch=release&pipeline_dir=definitions%2Fpipelines&create_private_repository=on")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/organizations/acme-hq/projects/default/settings/pipeline-backend?return_to=full&section=repository&repository_path=create", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, string(body), "provider access denied")
	require.NotContains(t, string(body), "data-error-popup")
	require.Contains(t, string(body), `value="acme/team"`)
	require.Contains(t, string(body), `value="customer-pipelines"`)
	require.Contains(t, string(body), `value="release"`)
	require.Contains(t, string(body), `value="definitions/pipelines"`)
}

func TestRouter_RepositorySetupProgressRendersSavedTargetOrStopsCreation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		setupCode int
		setupBody string
		want      string
		canSubmit bool
	}{
		{
			name:      "saved setup resumes exact target",
			setupCode: http.StatusOK,
			setupBody: `{"slug":"repo-setup","project_slug":"default","data_source_slug":"repo-setup","status":"pending","failure_code":"repository_bootstrap_failed","request":{"provider":"gitlab","owner":"acme/platform","repository_name":"pipelines","tracked_branch":"release","pipeline_dir":"definitions/pipelines","create_private_repository":true}}`,
			want:      "Resume repository setup",
			canSubmit: true,
		},
		{
			name:      "unavailable progress stops creation",
			setupCode: http.StatusServiceUnavailable,
			setupBody: `{"error":{"message":"temporary failure"}}`,
			want:      "Setup progress unavailable",
			canSubmit: false,
		},
		{
			name:      "declined authorization is recoverable",
			setupCode: http.StatusOK,
			setupBody: `{"slug":"repo-setup","project_slug":"default","data_source_slug":"repo-setup","status":"pending","failure_code":"authorization_denied","request":{"provider":"gitlab","owner":"acme/platform","repository_name":"pipelines","tracked_branch":"release","pipeline_dir":"definitions/pipelines","create_private_repository":true}}`,
			want:      "Authorization was declined.",
			canSubmit: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, authCookie := repositorySetupPageApp(t, tc.setupCode, tc.setupBody, false)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/projects/default?section=repository&repository_path=create", nil)
			req.Header.Set("Cookie", authCookie)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Contains(t, string(body), tc.want)
			require.Equal(t, tc.canSubmit, strings.Contains(string(body), `name="mode" value="managed_create"`))
			if tc.setupCode == http.StatusOK {
				require.Contains(t, string(body), `value="acme/platform" required maxlength="255" readonly`)
				require.Contains(t, string(body), `value="pipelines" required maxlength="100" readonly`)
				require.Contains(t, string(body), `value="release"`)
				require.Contains(t, string(body), `value="definitions/pipelines"`)
			}
		})
	}
}

func TestRouter_RepositorySetupCreationIsHiddenFromViewer(t *testing.T) {
	t.Parallel()

	app, authCookie := repositorySetupPageApp(t, http.StatusNoContent, "", true)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/projects/default?section=repository&repository_path=create", nil)
	req.Header.Set("Cookie", authCookie)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "View only")
	require.NotContains(t, string(body), `name="mode" value="managed_create"`)
}

func TestRouter_RepositorySetupExplainsInterruptedGitHubAuthorization(t *testing.T) {
	t.Parallel()
	app, authCookie := repositorySetupPageApp(t, http.StatusOK, `{"slug":"repo-setup","project_slug":"default","data_source_slug":"repo-setup","status":"pending","request":{"provider":"github","owner":"acme","repository_name":"pipelines","tracked_branch":"main","pipeline_dir":"pipelines","create_private_repository":true}}`, false)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme-hq/projects/default?section=repository&repository_path=create&repository_setup=failed", nil)
	req.Header.Set("Cookie", authCookie)
	response, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, string(body), "GitHub authorization could not be completed.")
	require.Contains(t, string(body), "Resume setup")
}

func repositorySetupPageApp(t *testing.T, setupCode int, setupBody string, viewer bool) (*fiber.App, string) {
	t.Helper()
	role := "admin"
	permissions := `[{"resource":"*","action":"*"}]`
	if viewer {
		role = "viewer"
		permissions = `[{"resource":"projects","action":"read"}]`
	}
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/organizations/acme-hq":
			writeJSON(w, `{"name":"Acme HQ","slug":"acme-hq"}`)
		case "GET /api/organizations":
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"`+role+`"}],"has_more":false}`)
		case "GET /api/organizations/acme-hq/roles/" + role:
			writeJSON(w, `{"slug":"`+role+`","name":"`+role+`","permissions":`+permissions+`}`)
		case "GET /api/organizations/acme-hq/projects/default":
			writeJSON(w, `{"name":"Default","slug":"default"}`)
		case "GET /api/organizations/acme-hq/projects":
			writeJSON(w, `{"projects":[{"name":"Default","slug":"default"}],"has_more":false}`)
		case "GET /api/organizations/acme-hq/data-sources":
			writeJSON(w, `{"data_sources":[],"has_more":false}`)
		case "GET /api/organizations/acme-hq/projects/default/repository-setup":
			if setupCode != http.StatusOK {
				w.WriteHeader(setupCode)
			}
			if setupBody != "" {
				writeJSON(w, setupBody)
			}
		case "GET /api/organizations/acme-hq/projects/default/repository-setups/repo-old":
			writeJSON(w, `{"slug":"repo-old","project_slug":"default","data_source_slug":"repo-old","status":"pending","abandoned_at":"2026-10-20T18:00:00Z","repository_url":"https://github.com/acme/old-pipelines.git","request":{"provider":"github","owner":"acme","repository_name":"old-pipelines","create_private_repository":true}}`)
		case "GET /api/organizations/acme-hq/environments":
			writeJSON(w, `{"environments":[],"has_more":false}`)
		case "GET /api/organizations/acme-hq/projects/default/environments":
			writeJSON(w, `{"project_environments":[],"has_more":false}`)
		case "GET /api/organizations/acme-hq/projects/default/pipelines":
			writeJSON(w, `{"pipelines":[],"has_more":false}`)
		case "GET /api/organizations/acme-hq/projects/default/data-source-refs":
			writeJSON(w, `{"data_source_refs":[],"has_more":false}`)
		default:
			t.Fatalf("unexpected API request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(apiServer.Close)
	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	return app, getAuthenticatedSessionCookie(t, app)
}
