package frontend

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectEnvironmentSelectedSlugFromRequestUsesEnableFormField(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Post("/", func(c fiber.Ctx) error {
		require.Equal(t, "stg", projectEnvironmentSelectedSlugFromRequest(c))
		return c.SendStatus(fiber.StatusNoContent)
	})

	form := url.Values{}
	form.Set("environment_slug", "stg")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, fiber.StatusNoContent, resp.StatusCode)
}

func TestRouter_HandleProjectEnvironmentsPage_RendersBindingMatrix(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments":
			writeJSON(
				w,
				`{"environments":[{"slug":"dev","name":"Development","description":"Developer target","protected":false,"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null},{"slug":"stg","name":"Staging","description":"Review target","protected":false,"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null},{"slug":"prod","name":"Production","description":"Customer target","protected":true,"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null},{"slug":"qa","name":"Quality Assurance","description":"QA target","protected":false,"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments":
			switch r.URL.Query().Get("cursor") {
			case "":
				writeJSON(
					w,
					`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":"Developer target","is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":true,"next_cursor":"environment-page-2","prev_cursor":null}`,
				)
			case "environment-page-2":
				writeJSON(
					w,
					`{"project_environments":[{"environment_slug":"prod","environment_name":"Production","environment_description":"Customer target","environment_protected":true,"is_default":false,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				)
			default:
				t.Errorf("unexpected project environments cursor %q", r.URL.Query().Get("cursor"))
				w.WriteHeader(http.StatusBadRequest)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/data-source-refs":
			switch r.URL.Query().Get("cursor") {
			case "":
				writeJSON(
					w,
					`{"data_source_refs":[{"pipeline_slug":"customer-sync","slug":"orders-db","name":"Orders DB","data_source_type":"postgres","required_capabilities":["pipeline_source"],"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":true,"next_cursor":"ref-page-2","prev_cursor":null}`,
				)
			case "ref-page-2":
				writeJSON(
					w,
					`{"data_source_refs":[{"pipeline_slug":"customer-sync","slug":"warehouse","name":"Warehouse","data_source_type":"postgres","required_capabilities":["pipeline_sink"],"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				)
			default:
				t.Errorf("unexpected data-source refs cursor %q", r.URL.Query().Get("cursor"))
				w.WriteHeader(http.StatusBadRequest)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/dev/data-source-bindings":
			switch r.URL.Query().Get("cursor") {
			case "":
				writeJSON(
					w,
					`{"data_source_bindings":[{"pipeline_slug":"customer-sync","data_source_ref_slug":"orders-db","data_source_ref_name":"Orders DB","environment_slug":"dev","data_source_slug":"orders-postgres-dev","data_source_name":"Orders Postgres Dev","data_source_type":"postgres","data_source_active":true,"data_source_read_only":false,"bound_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":true,"next_cursor":"binding-page-2","prev_cursor":null}`,
				)
			case "binding-page-2":
				writeJSON(
					w,
					`{"data_source_bindings":[{"pipeline_slug":"customer-sync","data_source_ref_slug":"warehouse","data_source_ref_name":"Warehouse","environment_slug":"dev","data_source_slug":"warehouse-dev","data_source_name":"Warehouse Dev","data_source_type":"postgres","data_source_active":true,"data_source_read_only":false,"bound_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				)
			default:
				t.Errorf("unexpected data-source bindings cursor %q", r.URL.Query().Get("cursor"))
				w.WriteHeader(http.StatusBadRequest)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/prod/data-source-bindings":
			writeJSON(
				w,
				`{"data_source_bindings":[{"pipeline_slug":"customer-sync","data_source_ref_slug":"orders-db","data_source_ref_name":"Orders DB","environment_slug":"prod","data_source_slug":"orders-postgres-prod","data_source_name":"Orders Postgres Prod","data_source_type":"postgres","data_source_active":true,"data_source_read_only":true,"bound_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/projects/data-migration/environments?environment_slug=prod&selected=prod&section=configuration",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Environments")
	require.Contains(t, string(body), `id="project-environments-workspace"`)
	require.Contains(t, string(body), "Project binding matrix")
	require.Contains(t, string(body), "Production")
	require.Contains(t, string(body), "production")
	require.NotContains(t, string(body), "protected production")
	require.NotContains(t, string(body), "staging review")
	require.NotContains(t, string(body), "Enable catalog environment")
	require.Contains(t, string(body), "Set default")
	require.Contains(t, string(body), "Disable")
	require.Contains(t, string(body), `hx-target="#project-environments-workspace"`)
	require.Contains(t, string(body), "Orders DB")
	require.Contains(t, string(body), "Warehouse")
	require.Contains(t, string(body), `Pipeline <span class="font-mono">customer-sync</span>`)
	require.Contains(t, string(body), "orders-postgres-dev")
	require.Contains(t, string(body), "orders-postgres-prod")
	require.Contains(t, string(body), "warehouse-dev")
	require.Contains(t, string(body), "missing binding")
	require.Contains(t, string(body), "1 missing project environment bindings need a data source target.")
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/data-migration/pipelines?environment_slug=prod"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources?environment_slug=prod"`)
	require.NotContains(t, string(body), `href="/orgs/acme-hq/data-sources">Manage data sources`)

	stagingReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/projects/data-migration/environments?environment_slug=prod&selected=stg&section=configuration",
		nil,
	)
	stagingReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, stagingReq)

	stagingResp, err := app.Test(stagingReq)
	require.NoError(t, err)
	defer func() { _ = stagingResp.Body.Close() }()

	stagingBody, err := io.ReadAll(stagingResp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, stagingResp.StatusCode)
	require.Contains(t, string(stagingBody), "Enable for project")
	require.Contains(t, string(stagingBody), `name="environment_slug" value="stg"`)
	require.Contains(t, string(stagingBody), `hx-target="#project-environments-workspace"`)
	require.NotContains(t, string(stagingBody), "Set default")
	require.NotContains(t, string(stagingBody), "/environments/stg/disable")
}

func TestRouter_HandleProjectEnvironmentsPage_BindingLookupFailureOmitsMatrix(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments":
			writeJSON(
				w,
				`{"environments":[{"slug":"dev","name":"Development","description":"Developer target","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null},{"slug":"prod","name":"Production","description":"Customer target","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments":
			writeJSON(
				w,
				`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":null,"is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"},{"environment_slug":"prod","environment_name":"Production","environment_description":null,"is_default":false,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/data-source-refs":
			writeJSON(
				w,
				`{"data_source_refs":[{"pipeline_slug":"customer-sync","slug":"orders-db","name":"Orders DB","data_source_type":"postgres","required_capabilities":["pipeline_source"],"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/dev/data-source-bindings":
			writeJSON(
				w,
				`{"data_source_bindings":[{"pipeline_slug":"customer-sync","data_source_ref_slug":"orders-db","data_source_ref_name":"Orders DB","environment_slug":"dev","data_source_slug":"orders-postgres-dev","data_source_name":"Orders Postgres Dev","data_source_type":"postgres","data_source_active":true,"data_source_read_only":false,"bound_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/prod/data-source-bindings":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, `{"error":{"code":"INTERNAL","message":"binding lookup failed"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/projects/data-migration/environments?section=configuration",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Contains(t, html, "binding lookup failed")
	require.Contains(t, html, "Binding matrix is unavailable")
	require.NotContains(t, html, "missing binding")
	require.NotContains(t, html, "orders-postgres-dev")
}

func TestRouter_HandleProjectEnvironmentsPage_HidesMutationFormsWithoutPermission(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"viewer","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/viewer":
			writeJSON(
				w,
				`{"slug":"viewer","name":"Viewer","permissions":[{"resource":"environments","action":"read"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments":
			writeJSON(
				w,
				`{"environments":[{"slug":"dev","name":"Development","description":"Developer target","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null},{"slug":"qa","name":"Quality Assurance","description":"QA target","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments":
			writeJSON(
				w,
				`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":"Developer target","is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/data-source-refs":
			writeJSON(
				w,
				`{"data_source_refs":[{"pipeline_slug":"customer-sync","slug":"orders-db","name":"Orders DB","data_source_type":"postgres","required_capabilities":["pipeline_source"],"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/dev/data-source-bindings":
			writeJSON(
				w,
				`{"data_source_bindings":[{"pipeline_slug":"customer-sync","data_source_ref_slug":"orders-db","data_source_ref_name":"Orders DB","environment_slug":"dev","data_source_slug":"orders-postgres-dev","data_source_name":"Orders Postgres Dev","data_source_type":"postgres","data_source_active":true,"data_source_read_only":false,"bound_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/projects/data-migration/environments?section=configuration",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Project binding matrix")
	require.Contains(t, html, "orders-postgres-dev")
	require.NotContains(t, html, "Enable for project")
	require.NotContains(t, html, "Set default")
	require.NotContains(t, html, ">Disable</button>")
}

func TestRouter_HandleOrganizationEnvironmentsPage_RendersRuntimeTargetManagement(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments":
			writeJSON(
				w,
				`{"environments":[{"slug":"dev","name":"Development","description":"Developer target","protected":false,"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null},{"slug":"prod","name":"Production","description":"Customer target","protected":true,"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments/dev/runtime-variables":
			writeJSON(
				w,
				`{"runtime_variables":[{"environment_slug":"dev","key":"s3_prefix","value_type":"text","value":"dev/orders","description":"Object prefix","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments/dev/secrets":
			writeJSON(
				w,
				`{"environment_secrets":[{"environment_slug":"dev","key":"API_TOKEN","description":"GitHub API token","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments/prod/runtime-variables":
			writeJSON(
				w,
				`{"runtime_variables":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments/prod/secrets":
			writeJSON(
				w,
				`{"environment_secrets":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/organizations/acme-hq/environments",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, `id="organization-environments-content"`)
	require.Contains(t, html, "Create environment")
	require.Contains(t, html, "Development")
	require.Contains(t, html, "development")
	require.Contains(t, html, "Environments")
	require.Contains(t, html, "Select an environment for a read-only quick look.")
	require.Contains(t, html, `href="/organizations/acme-hq/environments?environment_slug=dev"`)
	require.Contains(t, html, `hx-target="#organization-environments-content"`)
	require.Contains(t, html, `aria-label="Environments"`)
	require.NotContains(t, html, `action="/organizations/acme-hq/environments/dev/update"`)
	require.NotContains(t, html, `action="/organizations/acme-hq/environments/dev/delete"`)
	require.NotContains(t, html, `data-dirty-submit disabled`)

	selectedReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/organizations/acme-hq/environments/dev",
		nil,
	)
	selectedReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, selectedReq)

	selectedResp, err := app.Test(selectedReq)
	require.NoError(t, err)
	defer func() { _ = selectedResp.Body.Close() }()

	selectedBody, err := io.ReadAll(selectedResp.Body)
	require.NoError(t, err)
	selectedHTML := string(selectedBody)

	require.Equal(t, http.StatusOK, selectedResp.StatusCode)
	require.Contains(t, selectedHTML, `action="/organizations/acme-hq/environments/dev/update"`)
	require.Contains(t, selectedHTML, `action="/organizations/acme-hq/environments/dev/delete"`)
	require.Contains(t, selectedHTML, `data-protected-action-trigger="organization-env-dev-delete"`)
	require.Contains(t, selectedHTML, `id="organization-env-dev-delete"`)
	require.Contains(t, selectedHTML, "Delete runtime target")
	require.Contains(t, selectedHTML, `name="confirm_slug"`)

	require.NotContains(t, selectedHTML, `id="runtime-variables"`)
	require.NotContains(t, selectedHTML, `id="environment-secrets"`)
	renderSection := func(section string) string {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/environments/dev?section="+section, nil)
		req.Header.Set("Cookie", authCookie)
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return string(body)
	}
	variablesHTML := renderSection("variables")
	require.NotContains(t, variablesHTML, `id="environment-settings"`)
	require.NotContains(t, variablesHTML, `id="environment-secrets"`)
	require.Contains(t, variablesHTML, `action="/organizations/acme-hq/environments/dev/runtime-variables?section=variables"`)
	require.Contains(t, variablesHTML, "Runtime variables")
	require.Contains(t, variablesHTML, "s3_prefix")
	require.Contains(t, variablesHTML, "dev/orders")
	require.Contains(t, variablesHTML, `value="dev/orders"`)
	require.Contains(t, variablesHTML, "{{runtime.env.s3_prefix}}")
	require.Contains(t, variablesHTML, "Runtime key")
	require.Contains(t, variablesHTML, `data-toggle-target="organization-runtime-variable-dev-s3_prefix-update-panel"`)
	require.Contains(t, variablesHTML, `hx-confirm="Delete runtime variable &#34;s3_prefix&#34;? Pipelines using this variable will no longer resolve it."`)
	require.Contains(t, variablesHTML, `data-destructive-confirm="Delete runtime variable &#34;s3_prefix&#34;? Pipelines using this variable will no longer resolve it."`)
	require.Contains(t, variablesHTML, `id="organization-runtime-variable-dev-s3_prefix-update-panel" class="mt-3 hidden grid`)
	require.Contains(t, variablesHTML, `hx-swap="outerHTML show:none focus-scroll:false" data-preserve-scroll`)
	require.NotContains(t, variablesHTML, `rounded-md border border-base-300 bg-base-100`)
	require.Contains(t, variablesHTML, "Save variable")
	require.Contains(t, variablesHTML, "Update variable")

	secretsHTML := renderSection("secrets")
	require.NotContains(t, secretsHTML, `id="environment-settings"`)
	require.NotContains(t, secretsHTML, `id="runtime-variables"`)
	require.Contains(t, secretsHTML, "Environment secrets")
	require.Contains(t, secretsHTML, "API_TOKEN")
	require.Contains(t, secretsHTML, "GitHub API token")
	require.Contains(t, secretsHTML, "{{runtime.secrets.API_TOKEN}}")
	require.Contains(t, secretsHTML, `action="/organizations/acme-hq/environments/dev/secrets?section=secrets"`)
	require.Contains(t, secretsHTML, `data-toggle-target="organization-environment-secret-dev-API_TOKEN-update-panel"`)
	require.Contains(t, secretsHTML, `hx-confirm="Delete environment secret &#34;API_TOKEN&#34;? Pipelines using this secret will no longer resolve it."`)
	require.NotContains(t, secretsHTML, "ghp_super_secret")
	require.Contains(t, selectedHTML, `data-dirty-submit disabled`)

	protectedReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/organizations/acme-hq/environments/prod",
		nil,
	)
	protectedReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, protectedReq)

	protectedResp, err := app.Test(protectedReq)
	require.NoError(t, err)
	defer func() { _ = protectedResp.Body.Close() }()

	protectedBody, err := io.ReadAll(protectedResp.Body)
	require.NoError(t, err)
	protectedHTML := string(protectedBody)

	require.Equal(t, http.StatusOK, protectedResp.StatusCode)
	require.Contains(t, protectedHTML, `id="organization-environment-prod-update"`)
	require.Contains(t, protectedHTML, `data-protected-action-trigger="organization-protected-env-prod-save"`)
	require.Contains(t, protectedHTML, `id="organization-protected-env-prod-save"`)
	require.Contains(t, protectedHTML, `form="organization-environment-prod-update"`)
	require.Contains(t, protectedHTML, `data-protected-action-trigger="organization-env-prod-delete"`)
	require.Contains(t, protectedHTML, `id="organization-env-prod-delete"`)
	require.Contains(t, protectedHTML, `name="protected_environment_confirmation"`)
	require.Contains(t, protectedHTML, `name="confirm_slug"`)
	require.Contains(t, protectedHTML, `name="delete_reason"`)
	require.NotContains(t, protectedHTML, "Confirm protected change")
	require.NotContains(t, protectedHTML, ">catalog<")

	fullPageReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/organizations/acme-hq/environments/dev",
		nil,
	)
	fullPageReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, fullPageReq)

	fullPageResp, err := app.Test(fullPageReq)
	require.NoError(t, err)
	defer func() { _ = fullPageResp.Body.Close() }()

	fullPageBody, err := io.ReadAll(fullPageResp.Body)
	require.NoError(t, err)
	fullPageHTML := string(fullPageBody)

	require.Equal(t, http.StatusOK, fullPageResp.StatusCode)
	require.Contains(t, fullPageHTML, `id="organization-environment-full-page"`)
	require.Contains(t, fullPageHTML, `href="/organizations/acme-hq/environments?environment_slug=dev"`)
	require.Contains(t, fullPageHTML, `name="return_to" value="full"`)
	require.NotContains(t, fullPageHTML, `id="runtime-variables"`)
	require.NotContains(t, fullPageHTML, `id="environment-secrets"`)
}

func TestRouter_HandleOrganizationEnvironmentsPage_HidesMutationFormsWithoutPermission(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"viewer","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/viewer":
			writeJSON(
				w,
				`{"slug":"viewer","name":"Viewer","permissions":[{"resource":"environments","action":"read"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments":
			writeJSON(
				w,
				`{"environments":[{"slug":"dev","name":"Development","description":"Developer target","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments/dev/runtime-variables":
			writeJSON(
				w,
				`{"runtime_variables":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments/dev/secrets":
			writeJSON(
				w,
				`{"environment_secrets":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/organizations/acme-hq/environments",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Development")
	require.NotContains(t, html, "Create environment")
	require.NotContains(t, html, `action="/organizations/acme-hq/environments/dev/update"`)
	require.NotContains(t, html, `action="/organizations/acme-hq/environments/dev/delete"`)
}

func TestRouter_HandleCreateEnvironmentSubmit_CreatesCatalogEntry(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/api/organizations/acme-hq/environments" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Contains(t, string(body), `"name":"Staging"`)
		assert.Contains(t, string(body), `"slug":"stg"`)
		assert.Contains(t, string(body), `"description":"Promotion target"`)
		assert.Contains(t, string(body), `"color":"amber"`)
		w.WriteHeader(http.StatusCreated)
		writeJSON(
			w,
			`{"slug":"stg","name":"Staging","description":"Promotion target","color":"amber","protected":false,"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null}`,
		)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/environments",
		strings.NewReader("name=Staging&slug=stg&description=Promotion+target&color=amber"),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/environments", resp.Header.Get("Location"))
}

func TestRouter_HandleCreateEnvironmentSubmit_RendersFailureWithStickyForm(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/environments":
			w.WriteHeader(http.StatusConflict)
			writeJSON(w, `{"error":{"code":"CONFLICT","message":"environment slug already exists"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(w, `{"projects":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments":
			writeJSON(w, `{"environments":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/environments",
		strings.NewReader("name=Staging&slug=stg&description=Promotion+target&color=amber"),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, html, "environment slug already exists")
	require.Contains(t, html, `value="Staging"`)
	require.Contains(t, html, `value="stg"`)
	require.Contains(t, html, "Promotion target")
	require.Contains(t, html, `value="amber" selected`)
}

func TestRouter_HandleOrganizationEnvironmentSubmit_UpdatesAndDeletesCatalogEntry(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/environments/dev":
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.Contains(t, string(body), `"name":"Development"`)
			assert.Contains(t, string(body), `"description":"Developer target"`)
			assert.Contains(t, string(body), `"color":"blue"`)
			writeJSON(
				w,
				`{"slug":"dev","name":"Development","description":"Developer target","color":"blue","protected":false,"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null}`,
			)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/environments/dev":
			assert.Equal(t, "cleanup", r.URL.Query().Get("delete_reason"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/environments/prod":
			assert.Equal(t, "retired", r.URL.Query().Get("delete_reason"))
			assert.Equal(t, "prod", r.URL.Query().Get("protected_environment_confirmation"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments":
			writeJSON(
				w,
				`{"environments":[{"slug":"dev","name":"Development","description":"Developer target","protected":false,"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null},{"slug":"prod","name":"Production","description":"Customer target","protected":true,"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null,"delete_reason":null}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments/dev/runtime-variables":
			writeJSON(
				w,
				`{"runtime_variables":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments/dev/secrets":
			writeJSON(
				w,
				`{"environment_secrets":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	updateReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/environments/dev/update",
		strings.NewReader("name=Development&description=Developer+target&color=blue"),
	)
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, updateReq)

	updateResp, err := app.Test(updateReq)
	require.NoError(t, err)
	_ = updateResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, updateResp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/environments?environment_slug=dev", updateResp.Header.Get("Location"))

	fullPageInvalidReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/environments/dev/update",
		strings.NewReader("return_to=full&name="),
	)
	fullPageInvalidReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	fullPageInvalidReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, fullPageInvalidReq)

	fullPageInvalidResp, err := app.Test(fullPageInvalidReq)
	require.NoError(t, err)
	defer func() { _ = fullPageInvalidResp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, fullPageInvalidResp.StatusCode)

	fullPageInvalidBody, err := io.ReadAll(fullPageInvalidResp.Body)
	require.NoError(t, err)
	fullPageInvalidHTML := string(fullPageInvalidBody)
	require.Contains(t, fullPageInvalidHTML, `id="organization-environment-full-page"`)
	require.Contains(t, fullPageInvalidHTML, "Environment name is required.")
	require.NotContains(t, fullPageInvalidHTML, `id="organization-environments-content"`)

	deleteReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/environments/dev/delete",
		strings.NewReader("confirm_slug=dev&delete_reason=cleanup"),
	)
	deleteReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, deleteReq)

	deleteResp, err := app.Test(deleteReq)
	require.NoError(t, err)
	_ = deleteResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, deleteResp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/environments", deleteResp.Header.Get("Location"))

	protectedDeleteReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/environments/prod/delete",
		strings.NewReader("protected_environment_confirmation=prod&delete_reason=retired"),
	)
	protectedDeleteReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	protectedDeleteReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, protectedDeleteReq)

	protectedDeleteResp, err := app.Test(protectedDeleteReq)
	require.NoError(t, err)
	_ = protectedDeleteResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, protectedDeleteResp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/environments", protectedDeleteResp.Header.Get("Location"))
}

func TestRouter_HandleProjectEnvironmentSubmit_UpdatesEnablement(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/stg":
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.Contains(t, string(body), `"is_default":true`)
			writeJSON(
				w,
				`{"environment_slug":"stg","environment_name":"Staging","environment_description":null,"is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}`,
			)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/dev":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	enableReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/projects/data-migration/environments/enable",
		strings.NewReader("environment_slug=stg&is_default=on"),
	)
	enableReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	enableReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, enableReq)

	enableResp, err := app.Test(enableReq)
	require.NoError(t, err)
	_ = enableResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, enableResp.StatusCode)
	require.Equal(
		t,
		"/orgs/acme-hq/projects/data-migration/environments?environment_slug=stg",
		enableResp.Header.Get("Location"),
	)

	for _, activeField := range []string{"&active_environment_slug=dev", ""} {
		configurationReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
			"/orgs/acme-hq/projects/data-migration/environments/enable?section=configuration&selected=stg&environment_slug=dev",
			strings.NewReader("environment_slug=stg&is_default=on"+activeField))
		configurationReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		configurationReq.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, configurationReq)
		configurationResp, err := app.Test(configurationReq)
		require.NoError(t, err)
		_ = configurationResp.Body.Close()
		require.Equal(t, http.StatusSeeOther, configurationResp.StatusCode)
		location, err := url.Parse(configurationResp.Header.Get("Location"))
		require.NoError(t, err)
		require.Equal(t, "configuration", location.Query().Get("section"))
		require.Equal(t, "dev", location.Query().Get("environment_slug"))
		require.Equal(t, "stg", location.Query().Get("selected"))

	}

	disableReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/projects/data-migration/environments/dev/disable",
		nil,
	)
	disableReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, disableReq)

	disableResp, err := app.Test(disableReq)
	require.NoError(t, err)
	_ = disableResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, disableResp.StatusCode)
	require.Equal(
		t,
		"/orgs/acme-hq/projects/data-migration/environments",
		disableResp.Header.Get("Location"),
	)
}

func TestRouter_HandleEnvironmentRuntimeVariableSubmit_UpdatesVariables(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/environments/stg/runtime-variables/s3_prefix":
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.Contains(t, string(body), `"value_type":"text"`)
			assert.Contains(t, string(body), `"value":"stg/orders"`)
			assert.Contains(t, string(body), `"description":"Staging prefix"`)
			writeJSON(
				w,
				`{"environment_slug":"stg","key":"s3_prefix","value_type":"text","value":"stg/orders","description":"Staging prefix","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}`,
			)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/environments/stg/runtime-variables/s3_prefix":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	upsertReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/environments/stg/runtime-variables",
		strings.NewReader("key=s3_prefix&value_type=text&value=stg%2Forders&description=Staging+prefix"),
	)
	upsertReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	upsertReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, upsertReq)

	upsertResp, err := app.Test(upsertReq)
	require.NoError(t, err)
	_ = upsertResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, upsertResp.StatusCode)
	require.Equal(
		t,
		"/organizations/acme-hq/environments?environment_slug=stg",
		upsertResp.Header.Get("Location"),
	)

	deleteVariableReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/environments/stg/runtime-variables/s3_prefix/delete",
		nil,
	)
	deleteVariableReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteVariableReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, deleteVariableReq)

	deleteVariableResp, err := app.Test(deleteVariableReq)
	require.NoError(t, err)
	_ = deleteVariableResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, deleteVariableResp.StatusCode)
	require.Equal(
		t,
		"/organizations/acme-hq/environments?environment_slug=stg",
		deleteVariableResp.Header.Get("Location"),
	)
}

func TestRouter_HandleEnvironmentSecretSubmit_UpdatesWriteOnlySecrets(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/environments/stg/secrets/API_TOKEN":
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.Contains(t, string(body), `"value":"  ghp_super_secret  "`)
			assert.Contains(t, string(body), `"description":"GitHub token"`)
			writeJSON(
				w,
				`{"environment_slug":"stg","key":"API_TOKEN","description":"GitHub token","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}`,
			)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/environments/stg/secrets/API_TOKEN":
			assert.Equal(t, "stg", r.URL.Query().Get("protected_environment_confirmation"))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	upsertReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/environments/stg/secrets",
		strings.NewReader("key=API_TOKEN&value=%20%20ghp_super_secret%20%20&description=GitHub+token"),
	)
	upsertReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	upsertReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, upsertReq)

	upsertResp, err := app.Test(upsertReq)
	require.NoError(t, err)
	_ = upsertResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, upsertResp.StatusCode)
	require.Equal(
		t,
		"/organizations/acme-hq/environments?environment_slug=stg",
		upsertResp.Header.Get("Location"),
	)

	deleteReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/environments/stg/secrets/API_TOKEN/delete",
		strings.NewReader("protected_environment_confirmation=stg"),
	)
	deleteReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, deleteReq)

	deleteResp, err := app.Test(deleteReq)
	require.NoError(t, err)
	_ = deleteResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, deleteResp.StatusCode)
	require.Equal(
		t,
		"/organizations/acme-hq/environments?environment_slug=stg",
		deleteResp.Header.Get("Location"),
	)
}

func TestRouter_HandleEnvironmentRuntimeVariableSubmit_ClearsDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		formBody            string
		expectedPutContains string
		expectedLocation    string
	}{
		{
			name:                "ClearsDescription",
			formBody:            "key=s3_prefix&value_type=text&value=stg%2Forders&description=",
			expectedPutContains: `"description":""`,
			expectedLocation:    "/organizations/acme-hq/environments?environment_slug=stg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")

				switch {
				case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/environments/stg/runtime-variables/s3_prefix":
					body, err := io.ReadAll(r.Body)
					assert.NoError(t, err)
					assert.Contains(t, string(body), tt.expectedPutContains)
					writeJSON(
						w,
						`{"environment_slug":"stg","key":"s3_prefix","value_type":"text","value":"stg/orders","description":"","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}`,
					)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer apiServer.Close()

			store := session.NewStore()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)

			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})

			authCookie := getAuthenticatedSessionCookie(t, app)
			upsertReq := httptest.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				"/organizations/acme-hq/environments/stg/runtime-variables",
				strings.NewReader(tt.formBody),
			)
			upsertReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			upsertReq.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, upsertReq)

			upsertResp, err := app.Test(upsertReq)
			require.NoError(t, err)
			defer func() { _ = upsertResp.Body.Close() }()
			require.Equal(t, http.StatusSeeOther, upsertResp.StatusCode)
			require.Equal(t, tt.expectedLocation, upsertResp.Header.Get("Location"))
		})
	}
}

func TestRouter_HandleProjectEnvironmentsPage_RefLookupFailureMarksMatrixUnavailable(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments":
			writeJSON(
				w,
				`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":null,"is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/data-source-refs":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, `{"error":{"code":"INTERNAL","message":"ref lookup failed"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/projects/data-migration/environments?section=configuration",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Contains(t, html, "ref lookup failed")
	require.Contains(t, html, "Development")
	require.Contains(t, html, "Binding matrix is unavailable")
	require.NotContains(t, html, "No logical references yet")
	require.NotContains(t, html, "missing binding")
}

func TestRouter_HandleProjectEnvironmentsPage_ProjectEnvironmentLookupFailureMarksEnvironmentsUnavailable(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, `{"error":{"code":"INTERNAL","message":"project environments failed"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/projects/data-migration/environments",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Contains(t, html, "project environments failed")
	require.Contains(t, html, "Project environments are unavailable")
	require.NotContains(t, html, "No environments enabled")
}
