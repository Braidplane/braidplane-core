package frontend

import (
	"fmt"
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

func TestWorkspacePrivacyURLUsesDistributionNotice(t *testing.T) {
	t.Parallel()
	router := &Router{workspacePrivacyURL: "https://instance.example.test/workspace-privacy/"}
	const want = "https://instance.example.test/workspace-privacy/"
	require.Equal(t, want, router.defaultDataSourceCreateFormData().WorkspacePrivacyURL)
	require.Equal(t, want, router.dataSourceUpdateFormDataFromDataSource(&api.DataSource{Type: api.DataSourceTypeGoogleWorkspace}).WorkspacePrivacyURL)
	app := fiber.New()
	app.Post("/form", func(c fiber.Ctx) error {
		require.Equal(t, want, router.dataSourceCreateFormDataFromRequest(c).WorkspacePrivacyURL)
		return c.SendStatus(http.StatusOK)
	})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/form", strings.NewReader("type=google_workspace&workspace_privacy_url=https://attacker.example"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := app.Test(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
}

func TestWorkspaceRoutesRejectInvalidSlugsBeforeForwarding(t *testing.T) {
	router := &Router{}
	for _, endpoint := range []struct {
		method, suffix string
		handler        fiber.Handler
	}{
		{http.MethodGet, "spreadsheets", router.handleWorkspaceDiscovery},
		{http.MethodGet, "worksheets", router.handleWorkspaceWorksheets},
		{http.MethodGet, "picker", router.handleWorkspacePicker},
		{http.MethodPost, "verify", router.handleWorkspaceVerify},
	} {
		for _, pair := range []string{"bad!/sheet", "acme/bad!"} {
			t.Run(endpoint.suffix+"/"+pair, func(t *testing.T) {
				app := fiber.New()
				app.Add([]string{endpoint.method}, "/:slug/:dsSlug/"+endpoint.suffix, endpoint.handler)
				response, err := app.Test(httptest.NewRequestWithContext(t.Context(), endpoint.method, "/"+pair+"/"+endpoint.suffix, nil))
				require.NoError(t, err)
				defer func() { _ = response.Body.Close() }()
				require.Equal(t, http.StatusBadRequest, response.StatusCode)
			})
		}
	}
}

func TestWorkspaceReconnectRejectsInvalidConsentMode(t *testing.T) {
	installs := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme/data-sources/sheet" {
			_, _ = w.Write([]byte(`{"slug":"sheet","name":"Sheet","type":"google_workspace","config":{"access_mode":"selected_files"},"read_only":true,"is_active":true}`))
			return
		}
		installs++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	store := session.NewStore()
	router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orgs/acme/data-sources/sheet/google_workspace/oauth", strings.NewReader("workspace_access_mode=invalid"))
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	response, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Zero(t, installs)
}

func TestWorkspaceOnboardingInvalidModeRendersFormError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write([]byte(`{"data_sources":[],"has_more":false}`))
		case "/api/organizations":
			_, _ = w.Write([]byte(`{"organizations":[{"name":"Acme","slug":"acme-hq","member_role":"admin"}],"has_more":false}`))
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write([]byte(`{"projects":[],"has_more":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	store := session.NewStore()
	router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orgs/acme-hq/data-sources/google_workspace/oauth", strings.NewReader("name=Sheet&type=google_workspace&workspace_access_mode=invalid"))
	request.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, request)
	request.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	response, err := app.Test(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Equal(t, "outerHTML", response.Header.Get("HX-Reswap"))
	require.Contains(t, string(body), "Validation: Google Workspace requires selected-file access.")
	require.Contains(t, string(body), "create-data-source")
}

func TestWorkspaceMetadataProxiesHandleBackendErrors(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []struct {
		name, message, method string
		handler               func(*Router) fiber.Handler
	}{
		{"spreadsheets", "File discovery unavailable", http.MethodGet, func(r *Router) fiber.Handler { return r.handleWorkspaceDiscovery }},
		{"worksheets", "Worksheet metadata unavailable", http.MethodGet, func(r *Router) fiber.Handler { return r.handleWorkspaceWorksheets }},
		{"picker", "Google Picker unavailable", http.MethodGet, func(r *Router) fiber.Handler { return r.handleWorkspacePicker }},
		{"verify", "File verification unavailable", http.MethodPost, func(r *Router) fiber.Handler { return r.handleWorkspaceVerify }},
	} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable, http.StatusOK} {
			t.Run(endpoint.name+"/"+http.StatusText(status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = w.Write([]byte(`{"result":"metadata"}`))
					} else {
						_, _ = w.Write([]byte(`{"message":"private provider diagnostic"}`))
					}
				}))
				defer server.Close()
				router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), session.NewStore())
				require.NoError(t, err)
				app := fiber.New()
				app.Add([]string{endpoint.method}, "/:slug/:dsSlug/"+endpoint.name, endpoint.handler(router))
				var bodyReader io.Reader
				if endpoint.method == http.MethodPost {
					bodyReader = strings.NewReader(`{"file_ids":["selected"],"expected_revision":7}`)
				}
				request := httptest.NewRequestWithContext(t.Context(), endpoint.method, "/acme/sheet/"+endpoint.name, bodyReader)
				request.Header.Set("Content-Type", fiber.MIMEApplicationJSON)
				response, err := app.Test(request)
				require.NoError(t, err)
				defer func() { _ = response.Body.Close() }()
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				if status == http.StatusUnauthorized {
					require.Equal(t, http.StatusSeeOther, response.StatusCode)
					require.Contains(t, response.Header.Get("Location"), "/auth/login")
				} else {
					require.Equal(t, status, response.StatusCode)
					if status == http.StatusOK {
						require.JSONEq(t, `{"result":"metadata"}`, string(body))
					} else {
						require.JSONEq(t, `{"message":"`+endpoint.message+`"}`, string(body))
					}
				}
				require.NotContains(t, string(body), "private provider")
			})
		}
	}
}

func TestWorkspaceReconnectInvalidConfigRedirectsWithFeedback(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"sheet","name":"Sheet","type":"google_workspace","config":{"access_mode":1},"read_only":true,"is_active":true}`))
	}))
	defer server.Close()
	router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	app.Post("/:slug/:dsSlug/oauth", router.handleStartExistingWorkspaceOAuthSubmit)
	response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/acme/sheet/oauth", nil))
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	require.Contains(t, response.Header.Get("Location"), "error=Failed+to+load+the+Workspace+configuration.")
	require.NotContains(t, response.Header.Get("Location"), "unmarshal")
}

func TestWorkspaceVerificationRequiresCSRFAndForwardsRevision(t *testing.T) {
	for _, withCSRF := range []bool{false, true} {
		t.Run(fmt.Sprint(withCSRF), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if handleTestAdminRolePermissions(w, req) {
					return
				}
				calls++
				assert.Equal(t, "/api/organizations/acme/data-sources/sheet/google-workspace/files/verify", req.URL.Path)
				assert.Equal(t, http.MethodPost, req.Method)
				assert.NotEmpty(t, req.Header.Get("X-CSRF-Token"))
				body, err := io.ReadAll(req.Body)
				assert.NoError(t, err)
				assert.JSONEq(t, `{"file_ids":["selected"],"expected_revision":7}`, string(body))
				_, _ = w.Write([]byte(`{"files":[{"file_id":"selected","name":"Selected","mime_type":"application/json"}]}`))
			}))
			defer server.Close()
			store := session.NewStore()
			router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orgs/acme/data-sources/sheet/google_workspace/files/verify", strings.NewReader(`{"file_ids":["selected"],"expected_revision":7}`))
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)
			if withCSRF {
				addCSRFToken(t, app, req)
			}
			response, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			if withCSRF {
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.Equal(t, 1, calls)
				require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
			} else {
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				require.Zero(t, calls)
			}
		})
	}
}
