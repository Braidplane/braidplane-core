package frontend

import (
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
)

func TestManagedGitActionsForwardSettingsAndPreserveContext(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, form, backendPath, location string
		backendStatus, status                   int
	}{
		{"connect", "/organizations/acme/connect", "include_write_scopes=on", "/api/organizations/acme/gitlab-connection/authorization", "https://gitlab.com/oauth/authorize", 201, 303},
		{"connect failure", "/organizations/acme/connect", "", "/api/organizations/acme/gitlab-connection/authorization", "", 403, 503},
		{"disconnect", "/organizations/acme/disconnect", "", "/api/organizations/acme/gitlab-connection", "/organizations/acme/connections?provider_oauth=disconnected", 200, 303},
		{"disconnect forbidden", "/organizations/acme/disconnect", "", "/api/organizations/acme/gitlab-connection", "", 403, 403},
		{"attach", "/orgs/acme/data-sources/repo/attach", "environment_slug=prod", "/api/organizations/acme/data-sources/repo/gitlab-connection", "/orgs/acme/data-sources/repo?environment_slug=prod", 204, 303},
		{"attach conflict", "/orgs/acme/data-sources/repo/attach", "", "/api/organizations/acme/data-sources/repo/gitlab-connection", "", 409, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				assert.Equal(t, tc.backendPath, r.URL.Path)
				if tc.name == "connect" {
					var body map[string]bool
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					assert.True(t, body["include_write_scopes"])
				}
				if tc.backendStatus != 204 {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(tc.backendStatus)
				if tc.backendStatus == 201 {
					_, _ = io.WriteString(w, `{"authorization_url":"https://gitlab.com/oauth/authorize"}`)
				} else if tc.backendStatus != 204 {
					_, _ = io.WriteString(w, `{"message":"Repository changed"}`)
				}
			}))
			defer backend.Close()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			app.Post("/organizations/:slug/connect", router.handleOrganizationGitLabConnect)
			app.Post("/organizations/:slug/disconnect", router.handleOrganizationGitLabDisconnect)
			app.Post("/orgs/:slug/data-sources/:dsSlug/attach", router.handleAttachOrganizationGitLabConnection)
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tc.path, strings.NewReader(tc.form))
			request.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			response, err := app.Test(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			require.Equal(t, tc.status, response.StatusCode)
			require.Equal(t, tc.location, response.Header.Get("Location"))
			require.True(t, called)
		})
	}
}

func TestProofSaveForwardsExplicitTargetsAndRepositoryConfirmation(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/organizations/acme/data-sources/repo/proof", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"repository_id":"new-repository","available_targets":[{"project_slug":"project","environment_slug":"prod","target_ref":"refs/heads/main"}]}`)
			return
		}
		assert.Equal(t, http.MethodPut, r.Method)
		var body struct {
			Enabled  bool   `json:"enabled"`
			Expected string `json:"expected_repository_id"`
			Targets  []struct {
				Ref  string `json:"target_ref"`
				Path string `json:"pipeline_path"`
			} `json:"targets"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.True(t, body.Enabled)
		assert.Equal(t, "confirmed-repository", body.Expected)
		assert.Len(t, body.Targets, 2)
		assert.Equal(t, "refs/heads/release", body.Targets[0].Ref)
		assert.Equal(t, "pipelines/a.yml", body.Targets[0].Path)
		assert.Equal(t, "pipelines/b.yml", body.Targets[1].Path)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer backend.Close()
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	app.Post("/organizations/:slug/:dsSlug", router.handleProofConfigurationSave)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/repo", strings.NewReader("enabled=on&expected_repository_id=confirmed-repository&ref:project:prod=refs/heads/release&paths:project:prod=+pipelines/a.yml+%0A%0A+pipelines/b.yml&return_to=organization"))
	request.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	response, err := app.Test(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	require.Equal(t, "/organizations/acme/connections?saved=true#proof", response.Header.Get("Location"))
}

func TestFailedProofSavePreservesConfirmedRepositoryAndDraft(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusBadRequest, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPut {
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"error":{"message":"Repository changed. Reload and confirm the new repository."}}`)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/proof") {
					_, _ = io.WriteString(w, `{"data_source_slug":"repo","provider":"github","repository_id":"new-repository","enabled":false,"targets":[],"available_targets":[{"project_slug":"ops","environment_slug":"prod","target_ref":"main"}]}`)
					return
				}
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{}`)
			}))
			defer backend.Close()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			app.Post("/orgs/:slug/data-sources/:dsSlug/proof", router.handleProofConfigurationSave)
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orgs/acme/data-sources/repo/proof", strings.NewReader("enabled=on&publish_checks=on&expected_repository_id=confirmed-repository&paths:ops:prod=pipelines/orders.yaml&ref:ops:prod=main"))
			request.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			response, err := app.Test(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, status, response.StatusCode)
			for _, text := range []string{
				`name="expected_repository_id" value="confirmed-repository"`,
				`name="enabled" checked`, `name="publish_checks" checked`,
				`<details class="border-t border-base-300 pt-3" open`,
				"pipelines/orders.yaml", "Repository changed",
			} {
				require.Contains(t, string(body), text)
			}
			require.NotContains(t, string(body), `value="new-repository"`)
		})
	}
}

func TestManagedGitPagesShowSharedConnectionAndProofState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, expected string }{
		{"/organizations/acme/connections?provider_oauth=denied", "GitLab authorization was declined"},
		{"/organizations/acme/connections?provider_oauth=success", "GitLab connected"},
		{"/organizations/acme/connections?provider_oauth=expired", "GitLab authorization expired"},
		{"/organizations/acme/connections?provider_oauth=error", "GitLab authorization could not be completed"},
		{"/organizations/acme/connections?provider_oauth=disconnected", "GitLab disconnected"},
		{"/organizations/acme/connections?saved=true#proof", "Proof configuration saved"},
		{"/orgs/acme/data-sources/repo/proof", "Repository operations remain available"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/gitlab-connection") {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				configuration := `{"data_source_slug":"repo","name":"Repository","provider":"gitlab","repository_id":"42","enabled":false,"publish_checks":false,"status":"Repository operations remain available","targets":[],"available_targets":[]}`
				switch {
				case strings.HasSuffix(r.URL.Path, "/proof-configurations"):
					_, _ = io.WriteString(w, "["+configuration+"]")
				case strings.HasSuffix(r.URL.Path, "/proof"):
					_, _ = io.WriteString(w, configuration)
				default:
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{}`)
				}
			}))
			defer backend.Close()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			app.Get("/organizations/:slug/connections", router.handleProviderConnectionsPage)
			app.Get("/organizations/:slug/proof", router.handleProofConfigurationPage)
			app.Get("/orgs/:slug/data-sources/:dsSlug/proof", router.handleProofConfigurationPage)
			response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil))
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Contains(t, string(body), tc.expected)
		})
	}
}

func TestGitIntegrationsKeepsSectionReadPermissionsIndependent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                       string
		connections, proof, status int
		visible, absent            string
	}{
		{"proof only", 403, 200, 200, "Create a managed GitHub or GitLab data source", "Manage GitHub connection"},
		{"connections only", 204, 403, 200, "Manage GitHub connection", "Add data source"},
		{"neither", 403, 403, 403, "Forbidden", "GitLab OAuth"},
		{"proof unavailable", 204, 503, 200, "Proof settings could not be loaded", "Add data source"},
		{"connections unavailable", 503, 200, 200, "GitLab connection could not be loaded", "No GitLab account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/gitlab-connection"):
					w.WriteHeader(tc.connections)
				case strings.HasSuffix(r.URL.Path, "/proof-configurations"):
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.proof)
					if tc.proof == http.StatusOK {
						_, _ = io.WriteString(w, `[]`)
					} else {
						_, _ = io.WriteString(w, `{}`)
					}
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer backend.Close()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			app.Get("/organizations/:slug/connections", router.handleProviderConnectionsPage)
			response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme/connections", nil))
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, tc.status, response.StatusCode)
			require.Contains(t, string(body), tc.visible)
			require.NotContains(t, string(body), tc.absent)
			if tc.name == "proof unavailable" {
				require.Contains(t, string(body), "Manage GitHub connection")
			}
			if tc.name == "connections unavailable" {
				require.Contains(t, string(body), "Add data source")
			}
		})
	}
}

func TestOrganizationProofLinkRedirectsToGitIntegrations(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	router := &Router{}
	app.Get("/organizations/:slug/proof", router.handleProofConfigurationPage)
	response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme/proof?saved=true", nil))
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	require.Equal(t, "/organizations/acme/connections?saved=true#proof", response.Header.Get("Location"))
}
