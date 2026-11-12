package frontend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
)

func TestRouter_HandleQueriesPage_RendersList(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources/prod-db/queries":
			_, _ = w.Write(
				[]byte(
					`{"queries":[{"slug":"top-users","name":"Top users","description":"top users","data_source_slug":"prod-db","row_limit":100,"timeout_seconds":30,"query_statement":{"type":"postgres","sql":"select 1"},"parameters":[],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/queries?ds=prod-db&query=top-users", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Reusable connector operations")
	require.Contains(t, string(body), "Acme HQ")
	require.Contains(t, string(body), `<span class="sr-only">Switch organization: </span>`)
	require.Contains(t, string(body), `href="/organizations/acme-hq"`)
	require.Contains(t, string(body), `aria-label="Breadcrumb"`)
	require.Contains(t, string(body), `href="/dashboard"`)
	require.Contains(t, string(body), "Top users")
	require.Contains(t, string(body), `href="/orgs/acme-hq/queries?ds=prod-db&amp;query=top-users"`)
	require.Contains(t, string(body), `id="queries-workspace"`)
	require.Contains(t, string(body), `hx-target="#queries-workspace"`)
	require.Contains(t, string(body), `hx-target="#query-side-panel"`)
	require.Contains(t, string(body), `hx-swap="outerHTML show:none focus-scroll:false"`)
	require.Contains(t, string(body), `data-query-card`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources/prod-db/queries/top-users?list_return=`)
	require.NotContains(t, string(body), "Edit query definition")
	require.NotContains(t, string(body), `hx-confirm="Delete query &#34;Top users (top-users)&#34;? It will move to deleted queries."`)
	require.NotContains(t, string(body), `data-destructive-confirm="Delete query &#34;Top users (top-users)&#34;? It will move to deleted queries."`)
	require.NotContains(t, string(body), `data-dirty-submit disabled`)
	require.NotContains(t, string(body), `name="return_to" value="list"`)
	require.NotContains(t, string(body), `>Back to list<`)
	require.Contains(t, string(body), "selected")
	for _, queryString := range []string{"focus=top-users", "query=missing-query"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/queries?ds=prod-db&"+queryString, nil)
		req.Header.Set("Cookie", authCookie)
		resp, err := app.Test(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, queryString == "query=missing-query", strings.Contains(string(body), "Selected query is unavailable"))
	}
}

func TestRouter_HandleQueriesPage_RendersDeletedQueryRecoveryActions(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources/prod-db/queries":
			if r.URL.Query().Get("include_deleted") != "true" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"VALIDATION_FAILED","message":"include_deleted=true is required for the deleted query recovery view"}}`))
				return
			}
			_, _ = w.Write(
				[]byte(
					`{"queries":[{"slug":"top-users","name":"Top users","description":"top users","data_source_slug":"prod-db","row_limit":100,"timeout_seconds":30,"query_statement":{"type":"postgres","sql":"select 1"},"parameters":[],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","deleted_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/queries?ds=prod-db&deleted=true&query=top-users", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, bodyText, "Deleted queries")
	require.Contains(t, bodyText, `id="query-quick-look-heading"`)
	require.NotContains(t, bodyText, "Selected query is unavailable")
	require.Contains(t, bodyText, "Top users")
	require.Contains(t, bodyText, `action="/orgs/acme-hq/data-sources/prod-db/queries/top-users/restore"`)
	require.Contains(t, bodyText, `action="/orgs/acme-hq/data-sources/prod-db/queries/top-users/purge"`)
	require.Contains(t, bodyText, `hx-target="#queries-workspace"`)
	require.Contains(t, bodyText, `hx-select="#queries-workspace"`)
	require.Contains(t, bodyText, `hx-swap="outerHTML show:none focus-scroll:false"`)
	require.Contains(t, bodyText, `data-preserve-scroll`)
	require.Contains(t, bodyText, `hx-confirm="Purge query &#34;Top users (top-users)&#34;? This cannot be undone."`)
	require.Contains(t, bodyText, `data-destructive-confirm="Purge query &#34;Top users (top-users)&#34;? This cannot be undone."`)
	require.Contains(t, bodyText, ">Restore<")
	require.Contains(t, bodyText, ">Purge<")
	require.NotContains(t, bodyText, `href="/orgs/acme-hq/data-sources/prod-db/queries/top-users"`)
}

func TestRouter_HandleQueryDetailPage_RendersBreadcrumbs(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			_, _ = w.Write(
				[]byte(
					`{"slug":"top-users","name":"Top users","description":"top users","data_source_slug":"prod-db","row_limit":100,"timeout_seconds":30,"query_statement":{"type":"postgres","sql":"select 1"},"parameters":[],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `aria-label="Breadcrumb"`)
	require.Contains(t, string(body), `href="/dashboard"`)
	require.Contains(t, string(body), `href="/organizations"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/queries?ds=prod-db"`)
	require.Contains(t, string(body), "Top users")
	require.Contains(t, string(body), `aria-current="page"`)
	require.Contains(t, string(body), `>Back to list<`)
	require.NotContains(t, string(body), `>Back</a>`)
	require.Contains(t, string(body), `data-query-detail`)
	require.Contains(t, string(body), `data-query-detail-scroll-form`)
	require.Contains(t, string(body), `hx-target="#main-content"`)
	require.Contains(t, string(body), `hx-swap="outerHTML show:none focus-scroll:false"`)
	require.Contains(t, string(body), `data-dirty-submit disabled`)
}

func TestRouter_HandleQueryDetailPage_ForbiddenStaysOnDetailView(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"FORBIDDEN","message":"You don't have permission to read queries"}}`,
				),
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, string(body), "Query details")
	require.Contains(t, string(body), "top-users")
	require.Contains(t, string(body), "The full query details are unavailable.")
	require.Contains(t, string(body), "You don&#39;t have permission to read queries")
	require.NotContains(t, string(body), "No data sources")
	require.NotContains(t, string(body), "No queries found")
	require.Contains(t, string(body), `href="/orgs/acme-hq/queries?ds=prod-db"`)
}

func TestRouter_HandleQueriesPage_SelectedHTTPDataSource_IsSupported(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"slug":"api-http","name":"External API","type":"http","config":{"base_url":"https://api.example.com","health_path":"/health","timeout_seconds":30},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources/api-http":
			_, _ = w.Write([]byte(`{"slug":"api-http","name":"External API","type":"http","schema":{"schema_type":"http","operations":[{"id":"getUser","method":"GET","path":"/users/{id}","draft":{"statement":{"type":"http","method":"GET","path":"/users/{{path_id}}"},"parameters":[{"name":"path_id","data_type":"text","is_required":true}]}}]}}`))
		case "/api/organizations/acme-hq/data-sources/api-http/queries":
			_, _ = w.Write(
				[]byte(`{"queries":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/queries?ds=api-http&openapi_operation=getUser", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "External API")
	require.Contains(t, string(body), "Data source type: http")
	require.Contains(t, string(body), "New query")
	require.Contains(t, string(body), `value="getUser"`)
	require.Contains(t, string(body), `value="/users/{{path_id}}"`)
	require.Contains(t, string(body), `value="path_id"`)
	require.NotContains(t, string(body), `id="create-query-form" class="mt-6 rounded-xl border border-base-300 bg-base-100 p-5 scroll-mt-6 hidden"`)
}

func TestRouter_HandleQueriesPage_SelectedHubSpotDataSource_IsSupported(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"slug":"hubspot-crm","name":"HubSpot CRM","type":"hubspot","config":{"timeout_seconds":15},"is_active":true,"read_only":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources/hubspot-crm/queries":
			_, _ = w.Write(
				[]byte(`{"queries":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/queries?ds=hubspot-crm", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "HubSpot CRM")
	require.Contains(t, string(body), "Data source type: hubspot")
	require.Contains(t, string(body), "New query")
	require.Contains(t, string(body), "Read CRM rows. Row limit caps returned rows.")
	require.NotContains(t, string(body), "key-based upsert sink")
	require.NotContains(t, string(body), `value="upsert"`)
}

func TestRouter_HandleExecuteQuerySubmit_RendersBackendErrorInPopup(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			_, _ = w.Write(
				[]byte(
					`{"slug":"top-users","name":"Top users","description":"top users","data_source_slug":"prod-db","row_limit":100,"timeout_seconds":30,"query_statement":{"type":"postgres","sql":"select * from users where id = :id"},"parameters":[{"name":"id","data_type":"integer","is_required":true}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users/execute":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"QUERY_EXECUTION_FAILED","message":"syntax error at or near FROM"}}`,
				),
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
	for _, tc := range []struct {
		name    string
		input   string
		status  int
		message string
	}{
		{"backend failure", "exec_id=1", http.StatusUnprocessableEntity, "syntax error at or near FROM"},
		{"validation failure", "exec_id=invalid", http.StatusBadRequest, "Parameter id is invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
				"/orgs/acme-hq/data-sources/prod-db/queries/top-users/execute", strings.NewReader(tc.input))
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.status, resp.StatusCode)
			require.Contains(t, string(body), tc.message)
			require.Regexp(t, `id="query-results-tab"[^>]*aria-selected="true"`, string(body))
			require.Contains(t, string(body), `data-query-execution-feedback="true"`)
			require.NotContains(t, string(body), "No results yet")
			require.NotContains(t, string(body), "Execution result (")
			require.NotContains(t, string(body), "Duration: 0ms")
			if tc.status == http.StatusUnprocessableEntity {
				require.Contains(t, string(body), "query-execution-error-popup")
			}
		})
	}
}

func TestRouter_HandleExecuteQuerySubmit_HTMXRendersBackendErrorInSlot(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			_, _ = w.Write(
				[]byte(
					`{"slug":"top-users","name":"Top users","description":"top users","data_source_slug":"prod-db","row_limit":100,"timeout_seconds":30,"query_statement":{"type":"postgres","sql":"select * from users where id = :id"},"parameters":[{"name":"id","data_type":"integer","is_required":true}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users/execute":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"QUERY_EXECUTION_FAILED","message":"syntax error at or near FROM"}}`,
				),
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users/execute",
		strings.NewReader("exec_id=1"))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "syntax error at or near FROM")
	require.Contains(t, string(body), "query-execution-feedback-slot")
	require.NotContains(t, string(body), "alert alert-warning mt-4 py-2 text-sm")
	require.Contains(t, string(body), "query-execution-error-popup")
	require.Regexp(t, `id="query-results-tab"[^>]*aria-selected="true"`, string(body))
	require.NotContains(t, string(body), "Execution result (")
	require.NotContains(t, string(body), "Duration: 0ms")
	require.Contains(t, string(body), "More details")
	require.Contains(t, string(body), "execute query")
	require.Contains(t, string(body), "backend_message=syntax error at or near FROM")
}

func TestRouter_HandleExecuteQuerySubmit_HTMXQueryLoadFailureRendersFeedbackSlot(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"UPSTREAM_FAILED","message":"query load failed","detailed_error":"safe query load detail"}}`,
				),
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users/execute",
		strings.NewReader("exec_id=1"))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, bodyText, `id="query-execution-feedback-slot"`)
	require.Contains(t, bodyText, `id="query-execution-error-popup"`)
	require.Contains(t, bodyText, "query load failed")
	require.Contains(t, bodyText, "safe query load detail")
	require.NotContains(t, bodyText, `id="queries-workspace"`)
}

func TestRouter_HandleUpdateQuerySubmit_SendsUpdatedParameterType(t *testing.T) {
	t.Parallel()

	var captured api.UpdateQuery

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case (r.Method == http.MethodPut || r.Method == http.MethodGet) && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			defer func() { _ = r.Body.Close() }()
			if r.Method == http.MethodPut {
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
			}
			_, _ = w.Write(
				[]byte(
					`{"slug":"top-users","name":"Top users","description":"top users","data_source_slug":"prod-db","expected_input_contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row","representation":"rows"},"output_contract":{"name":"orders","representation":"rows","fields":[{"name":"id","type":"integer","required":true}]},"row_limit":100,"timeout_seconds":30,"query_statement":{"type":"postgres","sql":"select * from users where id = {{id}}"},"parameters":[{"name":"id","data_type":"integer","is_required":true}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
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
	form := strings.NewReader(strings.Join([]string{
		"name=Top+users",
		"description=top+users",
		"statement=select+*+from+users+where+id+%3D+%7B%7Bid%7D%7D",
		"row_limit=100",
		"timeout_seconds=30",
		"param_name=id",
		"param_type=integer",
		"param_required=true",
		"param_default=",
		"param_description=",
		"param_source=manual",
	}, "&"))
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users/update",
		form)

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Len(t, *captured.Parameters, 1)
	require.Equal(t, api.QueryParameterDataTypeInteger, (*captured.Parameters)[0].DataType)
	require.True(t, *(*captured.Parameters)[0].IsRequired)
	require.NotNil(t, captured.ExpectedInputContractRef)
	require.NotNil(t, captured.OutputContract)
	require.Equal(t, "orders", captured.ExpectedInputContractRef.ContractSlug)
}

func TestRouter_HandleCreateQuerySubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(
				[]byte(
					`{"slug":"top-users","name":"Top users","description":"top users","data_source_slug":"prod-db","row_limit":100,"timeout_seconds":30,"query_statement":{"type":"postgres","sql":"select 1"},"parameters":[],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
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
	form := strings.NewReader(strings.Join([]string{
		"ds=prod-db",
		"statement_type=postgres",
		"name=Top+users",
		"description=top+users",
		"statement=select+1",
		"row_limit=100",
		"timeout_seconds=30",
	}, "&"))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/queries", form)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/queries?ds=prod-db", resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleCreateQuerySubmit_HTMXRendersBackendValidationError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"VALIDATION_FAILED","message":"query SQL is required"}}`,
				),
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
	form := strings.NewReader(strings.Join([]string{
		"ds=prod-db",
		"statement_type=postgres",
		"name=Top+users",
		"description=top+users",
		"statement=select+1",
		"row_limit=100",
		"timeout_seconds=30",
	}, "&"))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/queries", form)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "query SQL is required")
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleUpdateQuerySubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case (r.Method == http.MethodPut || r.Method == http.MethodGet) && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			_, _ = w.Write(
				[]byte(
					`{"slug":"top-users","name":"Top users","description":"top users","data_source_slug":"prod-db","row_limit":100,"timeout_seconds":30,"query_statement":{"type":"postgres","sql":"select 1"},"parameters":[],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
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
	form := strings.NewReader(strings.Join([]string{
		"statement_type=postgres",
		"name=Top+users",
		"description=top+users",
		"statement=select+1",
		"row_limit=100",
		"timeout_seconds=30",
	}, "&"))
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users/update",
		form)

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(
		t,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users",
		resp.Header.Get("HX-Redirect"),
	)
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleUpdateQuerySubmit_HTMXReturnToListRendersSidePanel(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			_, _ = w.Write(
				[]byte(
					`{"slug":"prod-db","name":"Production DB","type":"postgres","config":{"host":"db","port":5432,"database":"app"},"is_active":true,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case (r.Method == http.MethodPut || r.Method == http.MethodGet) && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			_, _ = w.Write(
				[]byte(
					`{"slug":"top-users","name":"Top users edited","description":"top users","data_source_slug":"prod-db","row_limit":100,"timeout_seconds":30,"query_statement":{"type":"postgres","sql":"select 1"},"parameters":[],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
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
	form := strings.NewReader(strings.Join([]string{
		"return_to=list",
		"statement_type=postgres",
		"name=Top+users+edited",
		"description=top+users",
		"statement=select+1",
		"row_limit=100",
		"timeout_seconds=30",
	}, "&"))
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users/update",
		form)

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
	require.Contains(t, bodyText, `id="query-side-panel"`)
	require.Contains(t, bodyText, "Query saved.")
	require.Contains(t, bodyText, "Top users edited")
	require.Contains(t, bodyText, "Open full page")
	require.NotContains(t, bodyText, `name="return_to" value="list"`)
}

func TestRouter_HandleUpdateQuerySubmit_HTMXDataSourceFailureRendersSidePanel(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"UPSTREAM_FAILED","message":"selected data source unavailable","detailed_error":"safe upstream detail from connector"}}`,
				),
			)
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"UNEXPECTED_PUT","message":"update should not run after data source lookup failure"}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"NOT_FOUND","message":"query unavailable"}}`,
				),
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
	form := strings.NewReader(strings.Join([]string{
		"return_to=list",
		"statement_type=postgres",
		"name=Top+users+edited",
		"description=top+users",
		"statement=select+1",
		"row_limit=100",
		"timeout_seconds=30",
	}, "&"))
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users/update",
		form)

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
	require.Contains(t, bodyText, `id="query-side-panel"`)
	require.Contains(t, bodyText, `id="query-side-panel-feedback"`)
	require.Contains(t, bodyText, "selected data source unavailable")
	require.Contains(t, bodyText, "safe upstream detail from connector")
	require.NotContains(t, bodyText, "query unavailable")
	require.NotContains(t, bodyText, "update should not run")
}

func TestRouter_HandleDeleteQuerySubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users/delete",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(
		t,
		"/orgs/acme-hq/queries?ds=prod-db&focus=top-users",
		resp.Header.Get("HX-Redirect"),
	)
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleDeleteQuerySubmit_HTMXFailureRendersSidePanel(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"QUERY_IN_USE","message":"query is still referenced","detailed_error":"pipeline customer-sync still references this query"}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users":
			_, _ = w.Write(
				[]byte(
					`{"slug":"top-users","name":"Top users","description":"top users","data_source_slug":"prod-db","row_limit":100,"timeout_seconds":30,"query_statement":{"type":"postgres","sql":"select 1"},"parameters":[],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users/delete",
		strings.NewReader("return_to=list"))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
	require.Contains(t, bodyText, `id="query-side-panel"`)
	require.Contains(t, bodyText, `id="query-side-panel-feedback"`)
	require.Contains(t, bodyText, "query is still referenced")
	require.Contains(t, bodyText, "pipeline customer-sync still references this query")
	require.Contains(t, bodyText, "Top users")
}

func TestRouter_HandleRestoreQuerySubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users/restore":
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users/restore",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(
		t,
		"/orgs/acme-hq/queries?ds=prod-db&focus=top-users",
		resp.Header.Get("HX-Redirect"),
	)
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandlePurgeQuerySubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/data-sources/prod-db/queries/top-users/purge":
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/prod-db/queries/top-users/purge",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(
		t,
		"/orgs/acme-hq/queries?ds=prod-db&deleted=true",
		resp.Header.Get("HX-Redirect"),
	)
	require.Empty(t, resp.Header.Get("Location"))
}

func TestBuildCreateQueryPayload_HTTP(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(strings.Join([]string{
			"name=Users+HTTP",
			"description=Fetch+users+from+API",
			"http_method=GET",
			"http_path=%2Fusers",
			"http_query_param_key=status",
			"http_query_param_value=active",
			"http_header_key=x-api-key",
			"http_header_value=%7B%7Bapi_key%7D%7D",
			"http_body=%7B%22page%22%3A1%7D",
			"timeout_seconds=15",
			"param_name=api_key",
			"param_type=text",
			"param_required=true",
			"param_default=",
			"param_description=",
			"param_source=manual",
		}, "&")))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var payload api.CreateQueryJSONRequestBody
	var payloadErr string

	app.Post("/", func(c fiber.Ctx) error {
		payload, payloadErr = buildCreateQueryPayload(c, api.DataSourceTypeHttp)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, payloadErr)
	require.Equal(t, "Users HTTP", payload.Name)
	require.Nil(t, payload.RowLimit)
	require.NotNil(t, payload.TimeoutSeconds)
	require.Equal(t, 15, *payload.TimeoutSeconds)

	httpStatement, err := payload.QueryStatement.AsHTTPStatement()
	require.NoError(t, err)
	require.Equal(t, api.HTTPStatementMethodGET, httpStatement.Method)
	require.Equal(t, "/users", httpStatement.Path)
	require.NotNil(t, httpStatement.QueryParams)
	require.Equal(t, "active", (*httpStatement.QueryParams)["status"])
	require.NotNil(t, httpStatement.Headers)
	require.Equal(t, "{{api_key}}", (*httpStatement.Headers)["x-api-key"])
	require.NotNil(t, httpStatement.Body)
	if bodyMap, ok := httpStatement.Body.(map[string]any); ok {
		require.InEpsilon(t, 1.0, bodyMap["page"], 0.000001)
	} else {
		t.Fatalf("expected HTTP body to decode as object map")
	}
}

func TestBuildCreateQueryPayload_MongoDB(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(strings.Join([]string{
			"name=Orders+find",
			"description=Find+orders+by+status",
			"mongo_collection=orders",
			"mongo_operation=find",
			"mongo_filter=%7B%22status%22%3A%22%7B%7Bstatus%7D%7D%22%7D",
			"row_limit=50",
			"timeout_seconds=25",
			"param_name=status",
			"param_type=text",
			"param_required=true",
			"param_default=",
			"param_description=",
			"param_source=manual",
		}, "&")))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var payload api.CreateQueryJSONRequestBody
	var payloadErr string

	app.Post("/", func(c fiber.Ctx) error {
		payload, payloadErr = buildCreateQueryPayload(c, api.DataSourceTypeMongodb)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, payloadErr)
	require.Equal(t, "Orders find", payload.Name)
	require.NotNil(t, payload.RowLimit)
	require.Equal(t, 50, *payload.RowLimit)
	require.NotNil(t, payload.TimeoutSeconds)
	require.Equal(t, 25, *payload.TimeoutSeconds)

	mongoStatement, err := payload.QueryStatement.AsMongoDBStatement()
	require.NoError(t, err)
	require.Equal(t, api.MongoDBStatementOperationFind, mongoStatement.Operation)
	require.Equal(t, "orders", mongoStatement.Collection)
	require.NotNil(t, mongoStatement.Filter)
	require.Equal(t, "{{status}}", (*mongoStatement.Filter)["status"])
}

func TestBuildCreateQueryPayload_HubSpot(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(strings.Join([]string{
			"name=HubSpot+contacts",
			"description=Search+CRM+contacts",
			"hubspot_object_type=contacts",
			"hubspot_operation=search",
			"hubspot_properties=email,firstname",
			"hubspot_query=%7B%7Bsearch_term%7D%7D",
			"hubspot_after=200",
			"hubspot_sorts=-createdate",
			"hubspot_filter_groups=%5B%7B%22filters%22%3A%5B%7B%22property_name%22%3A%22email%22%2C%22operator%22%3A%22EQ%22%2C%22value%22%3A%22%7B%7Bsearch_term%7D%7D%22%7D%5D%7D%5D",
			"row_limit=100",
			"timeout_seconds=25",
			"param_name=search_term",
			"param_type=text",
			"param_required=true",
			"param_default=",
			"param_description=",
			"param_source=manual",
		}, "&")))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var payload api.CreateQueryJSONRequestBody
	var payloadErr string

	app.Post("/", func(c fiber.Ctx) error {
		payload, payloadErr = buildCreateQueryPayload(c, api.DataSourceTypeHubspot)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, payloadErr)
	require.Equal(t, "HubSpot contacts", payload.Name)
	require.NotNil(t, payload.RowLimit)
	require.Equal(t, 100, *payload.RowLimit)
	require.NotNil(t, payload.TimeoutSeconds)
	require.Equal(t, 25, *payload.TimeoutSeconds)

	hubspotStatement, err := payload.QueryStatement.AsHubSpotStatement()
	require.NoError(t, err)
	require.Equal(t, "contacts", hubspotStatement.ObjectType)
	require.NotNil(t, hubspotStatement.Operation)
	require.Equal(t, api.HubSpotStatementOperationSearch, *hubspotStatement.Operation)
	require.NotNil(t, hubspotStatement.Properties)
	require.Equal(t, []string{"email", "firstname"}, *hubspotStatement.Properties)
	require.NotNil(t, hubspotStatement.Query)
	require.Equal(t, "{{search_term}}", *hubspotStatement.Query)
	require.NotNil(t, hubspotStatement.After)
	require.Equal(t, "200", *hubspotStatement.After)
	require.NotNil(t, hubspotStatement.Sorts)
	require.Equal(t, []string{"-createdate"}, *hubspotStatement.Sorts)
	require.NotNil(t, hubspotStatement.FilterGroups)
	require.Len(t, *hubspotStatement.FilterGroups, 1)
	require.Len(t, (*hubspotStatement.FilterGroups)[0].Filters, 1)
	require.Equal(t, "email", (*hubspotStatement.FilterGroups)[0].Filters[0].PropertyName)
}

func TestBuildCreateQueryPayload_HubSpotCustomObject(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(strings.Join([]string{
			"name=HubSpot+cars",
			"hubspot_object_type=2-7282133",
			"hubspot_operation=list",
		}, "&")),
	)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var payload api.CreateQueryJSONRequestBody
	var payloadErr string
	app.Post("/", func(c fiber.Ctx) error {
		payload, payloadErr = buildCreateQueryPayload(c, api.DataSourceTypeHubspot)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, payloadErr)

	statement, err := payload.QueryStatement.AsHubSpotStatement()
	require.NoError(t, err)
	require.Equal(t, "2-7282133", statement.ObjectType)
}

func TestBuildCreateQueryPayload_HubSpotUpsert(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	form := url.Values{}
	form.Set("name", "Upsert HubSpot contacts")
	form.Set("hubspot_object_type", "contacts")
	form.Set("hubspot_operation", "upsert")
	form.Set("hubspot_id_property", "external_customer_id")
	form.Set("hubspot_property_mapping", `{"customer_id":"external_customer_id","email":"email"}`)
	form.Set("hubspot_rows", "{{input}}")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var payload api.CreateQueryJSONRequestBody
	var payloadErr string
	app.Post("/", func(c fiber.Ctx) error {
		payload, payloadErr = buildCreateQueryPayload(c, api.DataSourceTypeHubspot)
		return c.SendStatus(http.StatusOK)
	})
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Empty(t, payloadErr)

	statement, err := payload.QueryStatement.AsHubSpotStatement()
	require.NoError(t, err)
	require.Equal(t, api.HubSpotStatementOperationUpsert, *statement.Operation)
	require.Equal(t, "external_customer_id", *statement.IdProperty)
	require.Equal(t, map[string]string{
		"customer_id": "external_customer_id",
		"email":       "email",
	}, *statement.PropertyMapping)
	require.NotNil(t, statement.Rows)
	rows, err := statement.Rows.AsHubSpotStatementRows2()
	require.NoError(t, err)
	require.Equal(t, "{{input}}", rows)
}

func TestBuildCreateQueryPayload_RowLimitUpperBound(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(strings.Join([]string{
			"name=Orders+find",
			"statement=select+1",
			"row_limit=10001",
			"timeout_seconds=25",
		}, "&")))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var payloadErr string

	app.Post("/", func(c fiber.Ctx) error {
		_, payloadErr = buildCreateQueryPayload(c, api.DataSourceTypePostgres)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Row limit must be less than or equal to 10000.", payloadErr)
}

func TestBuildCreateQueryPayload_TimeoutUpperBound(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(strings.Join([]string{
			"name=Orders+find",
			"statement=select+1",
			"row_limit=100",
			"timeout_seconds=301",
		}, "&")))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var payloadErr string

	app.Post("/", func(c fiber.Ctx) error {
		_, payloadErr = buildCreateQueryPayload(c, api.DataSourceTypePostgres)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Timeout must be less than or equal to 300.", payloadErr)
}

func TestBuildCreateQueryPayload_ParameterCountUpperBound(t *testing.T) {
	t.Parallel()

	values := []string{
		"name=Orders+find",
		"statement=select+1",
		"row_limit=100",
		"timeout_seconds=25",
	}
	for i := range 101 {
		values = append(values, "param_name=p"+strconv.Itoa(i))
		values = append(values, "param_type=text")
		values = append(values, "param_required=false")
		values = append(values, "param_default=")
		values = append(values, "param_description=")
		values = append(values, "param_source=manual")
	}

	app := fiber.New()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(strings.Join(values, "&")))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var payloadErr string

	app.Post("/", func(c fiber.Ctx) error {
		_, payloadErr = buildCreateQueryPayload(c, api.DataSourceTypePostgres)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Too many parameters. Maximum allowed is 100.", payloadErr)
}

func TestRouter_HandleUpdateQuerySubmit_HTTP_SendsHTTPStatement(t *testing.T) {
	t.Parallel()

	var captured api.UpdateQuery

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/api-http":
			_, _ = w.Write(
				[]byte(
					`{"slug":"api-http","name":"External API","type":"http","config":{"base_url":"https://api.example.com"},"is_active":true,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case (r.Method == http.MethodPut || r.Method == http.MethodGet) && r.URL.Path == "/api/organizations/acme-hq/data-sources/api-http/queries/users-http":
			defer func() { _ = r.Body.Close() }()
			if r.Method == http.MethodPut {
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
			}
			_, _ = w.Write(
				[]byte(
					`{"slug":"users-http","name":"Users HTTP","description":"updated","data_source_slug":"api-http","timeout_seconds":20,"query_statement":{"type":"http","method":"POST","path":"/users/search","headers":{"content-type":"application/json"},"body":{"q":"{{query}}"}},"parameters":[{"name":"query","data_type":"text","is_required":true}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
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
	form := strings.NewReader(strings.Join([]string{
		"name=Users+HTTP",
		"description=updated",
		"statement_type=http",
		"http_method=POST",
		"http_path=%2Fusers%2Fsearch",
		"http_header_key=content-type",
		"http_header_value=application%2Fjson",
		"http_body=%7B%22q%22%3A%22%7B%7Bquery%7D%7D%22%7D",
		"timeout_seconds=20",
		"param_name=query",
		"param_type=text",
		"param_required=true",
		"param_default=",
		"param_description=",
		"param_source=manual",
	}, "&"))
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/data-sources/api-http/queries/users-http/update",
		form)

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(
		t,
		"/orgs/acme-hq/data-sources/api-http/queries/users-http",
		resp.Header.Get("Location"),
	)

	httpStatement, err := captured.QueryStatement.AsHTTPStatement()
	require.NoError(t, err)
	require.Equal(t, api.HTTPStatementMethodPOST, httpStatement.Method)
	require.Equal(t, "/users/search", httpStatement.Path)
	require.NotNil(t, httpStatement.Headers)
	require.Equal(t, "application/json", (*httpStatement.Headers)["content-type"])
	require.NotNil(t, httpStatement.Body)
	if bodyMap, ok := httpStatement.Body.(map[string]any); ok {
		require.Equal(t, "{{query}}", bodyMap["q"])
	} else {
		t.Fatalf("expected HTTP body to decode as object map")
	}
}

func TestValidateQueryParametersAgainstStatement_UsesDoubleBracePlaceholders(t *testing.T) {
	t.Parallel()

	parameters := []api.QueryParameter{
		{
			Name:       "user_id",
			DataType:   api.QueryParameterDataTypeInteger,
			IsRequired: new(true),
		},
	}

	require.Empty(t, validateQueryParametersAgainstStatement(
		parameters,
		"select * from users where id = {{user_id}}",
	))

	require.Equal(
		t,
		"Parameter account_id is referenced in statement but not configured.",
		validateQueryParametersAgainstStatement(
			parameters,
			"select * from users where account_id = {{account_id}}",
		),
	)

	require.Nil(t, extractStatementPlaceholders("select * from users where id = :legacy_param"))
}

func TestValidateQueryParametersAgainstStatement_AllowsReservedSinkInput(t *testing.T) {
	t.Parallel()

	require.Empty(t, validateQueryParametersAgainstStatement(
		nil,
		`{"rows":"{{input}}"}`,
	))
	require.Empty(t, validateQueryParametersAgainstStatement(
		[]api.QueryParameter{{Name: sinkbinding.Input, DataType: api.QueryParameterDataTypeText}},
		`{"rows":"{{input}}"}`,
	))
}

func TestBuildQueryStatementFromRequest_HTTP(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(
			"http_acknowledgement_enabled=on&http_acknowledgement_status_codes=200&http_acknowledgement_path=status.code&http_acknowledgement_equals=%22OK%22&http_pagination="+url.QueryEscape(`{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.next","max_pages":3,"timeout_seconds":2}`)+"&http_method=GET&http_path=%2Fusers%2F%7B%7Buser_id%7D%7D&http_query_param_key=active&http_query_param_value=%7B%7Bactive%7D%7D",
		))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var gotStatement api.QueryStatement
	var gotErrMsg string

	app.Post("/", func(c fiber.Ctx) error {
		gotStatement, gotErrMsg = buildQueryStatementFromRequest(c, api.DataSourceTypeHttp)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, gotErrMsg)

	httpStatement, err := gotStatement.AsHTTPStatement()
	require.NoError(t, err)
	require.Equal(t, api.HTTPStatementTypeHttp, httpStatement.Type)
	require.Equal(t, api.HTTPStatementMethodGET, httpStatement.Method)
	require.Equal(t, "/users/{{user_id}}", httpStatement.Path)
	require.NotNil(t, httpStatement.Acknowledgement)
	require.JSONEq(t, `"OK"`, string(httpStatement.Acknowledgement.Success[0].Equals))
	require.NotNil(t, httpStatement.Pagination)
	require.NotNil(t, httpStatement.Pagination.CursorParam)
	require.Equal(t, "after", *httpStatement.Pagination.CursorParam)
	require.NotNil(t, httpStatement.QueryParams)
	require.Equal(t, "{{active}}", (*httpStatement.QueryParams)["active"])
}

func TestBuildQueryStatementFromRequest_HTTP_PathQueryStringHydratesQueryParams(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader("http_method=GET&http_path=%2Fusers%3Fparam1%3Dhello%26param2%3Dworld"))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var gotStatement api.QueryStatement
	var gotErrMsg string

	app.Post("/", func(c fiber.Ctx) error {
		gotStatement, gotErrMsg = buildQueryStatementFromRequest(c, api.DataSourceTypeHttp)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, gotErrMsg)

	httpStatement, err := gotStatement.AsHTTPStatement()
	require.NoError(t, err)
	require.Equal(t, "/users", httpStatement.Path)
	require.NotNil(t, httpStatement.QueryParams)
	require.Equal(t, "hello", (*httpStatement.QueryParams)["param1"])
	require.Equal(t, "world", (*httpStatement.QueryParams)["param2"])
}

func TestBuildQueryStatementFromRequest_HTTP_PathQueryMergedWithEditorParams(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(
			"http_method=GET&http_path=%2Fusers%3Fparam1%3Dhello%26param2%3Dworld&http_query_param_key=param2&http_query_param_value=updated&http_query_param_key=param3&http_query_param_value=third",
		))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var gotStatement api.QueryStatement
	var gotErrMsg string

	app.Post("/", func(c fiber.Ctx) error {
		gotStatement, gotErrMsg = buildQueryStatementFromRequest(c, api.DataSourceTypeHttp)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, gotErrMsg)

	httpStatement, err := gotStatement.AsHTTPStatement()
	require.NoError(t, err)
	require.Equal(t, "/users", httpStatement.Path)
	require.NotNil(t, httpStatement.QueryParams)
	require.Equal(t, "hello", (*httpStatement.QueryParams)["param1"])
	require.Equal(t, "updated", (*httpStatement.QueryParams)["param2"])
	require.Equal(t, "third", (*httpStatement.QueryParams)["param3"])
}

func TestBuildQueryStatementFromRequest_HTTP_PathQueryRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader("http_method=GET&http_path=%2Fusers%3Fparam1%3Da%26param1%3Db"))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var gotErrMsg string

	app.Post("/", func(c fiber.Ctx) error {
		_, gotErrMsg = buildQueryStatementFromRequest(c, api.DataSourceTypeHttp)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "HTTP path query params contains duplicate key param1.", gotErrMsg)
}

func TestBuildQueryStatementFromRequest_MongoDB(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/",
		strings.NewReader(
			"mongo_collection=users&mongo_operation=find&mongo_filter=%7B%22org_id%22%3A%22%7B%7Borg_id%7D%7D%22%7D",
		))

	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

	var gotStatement api.QueryStatement
	var gotErrMsg string

	app.Post("/", func(c fiber.Ctx) error {
		gotStatement, gotErrMsg = buildQueryStatementFromRequest(c, api.DataSourceTypeMongodb)
		return c.SendStatus(http.StatusOK)
	})

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, gotErrMsg)

	mongoStatement, err := gotStatement.AsMongoDBStatement()
	require.NoError(t, err)
	require.Equal(t, api.MongoDBStatementTypeMongodb, mongoStatement.Type)
	require.Equal(t, api.MongoDBStatementOperationFind, mongoStatement.Operation)
	require.Equal(t, "users", mongoStatement.Collection)
	require.NotNil(t, mongoStatement.Filter)
	require.Equal(t, "{{org_id}}", (*mongoStatement.Filter)["org_id"])
}

func TestBuildQueryStatementFromRequest_FTP(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		formBody   string
		assertStmt func(t *testing.T, stmt api.FTPStatement)
		wantErrMsg string
	}{
		{
			name:     "MinimalPathOnly",
			formBody: "ftp_path=exports%2Fcustomers%2F%7B%7Bruntime.run.date%7D%7D.csv",
			assertStmt: func(t *testing.T, stmt api.FTPStatement) {
				t.Helper()
				require.Equal(t, api.FTPStatementTypeFtp, stmt.Type)
				require.Equal(t, "exports/customers/{{runtime.run.date}}.csv", stmt.Path)
			},
		},
		{
			name:     "WriteOperationWithOverwrite",
			formBody: "ftp_path=%2Fout%2Ffile.csv&ftp_operation=write&ftp_overwrite=on",
			assertStmt: func(t *testing.T, stmt api.FTPStatement) {
				t.Helper()
				require.Equal(t, "/out/file.csv", stmt.Path)
				require.NotNil(t, stmt.Operation)
				require.Equal(t, api.FTPStatementOperationWrite, *stmt.Operation)
				require.NotNil(t, stmt.Overwrite)
				require.True(t, *stmt.Overwrite)
			},
		},
		{
			name:     "CSVFormatWithDelimiterAndHeader",
			formBody: "ftp_path=%2Fdata%2Freport.csv&ftp_format=csv&ftp_delimiter=%3B&ftp_has_header=on",
			assertStmt: func(t *testing.T, stmt api.FTPStatement) {
				t.Helper()
				require.NotNil(t, stmt.Format)
				require.Equal(t, api.FTPStatementFormatCsv, *stmt.Format)
				require.NotNil(t, stmt.Delimiter)
				require.Equal(t, ";", *stmt.Delimiter)
				require.NotNil(t, stmt.HasHeader)
				require.True(t, *stmt.HasHeader)
			},
		},
		{
			name:     "SpreadsheetFormatWithSheetAndRange",
			formBody: "ftp_path=%2Fdata%2Freport.xlsx&ftp_format=spreadsheet&ftp_sheet=Orders&ftp_range=A1%3AD20&ftp_has_header=on",
			assertStmt: func(t *testing.T, stmt api.FTPStatement) {
				t.Helper()
				require.NotNil(t, stmt.Format)
				require.Equal(t, api.FTPStatementFormatSpreadsheet, *stmt.Format)
				require.NotNil(t, stmt.Sheet)
				require.Equal(t, "Orders", *stmt.Sheet)
				require.NotNil(t, stmt.Range)
				require.Equal(t, "A1:D20", *stmt.Range)
				require.NotNil(t, stmt.HasHeader)
				require.True(t, *stmt.HasHeader)
			},
		},
		{
			name:     "SpreadsheetFormatWithoutHeaderPreservesAutoDetection",
			formBody: "ftp_path=%2Fdata%2Freport.xlsx&ftp_format=spreadsheet&ftp_sheet=Orders&ftp_range=A1%3AD20",
			assertStmt: func(t *testing.T, stmt api.FTPStatement) {
				t.Helper()
				require.NotNil(t, stmt.Format)
				require.Equal(t, api.FTPStatementFormatSpreadsheet, *stmt.Format)
				require.Nil(t, stmt.HasHeader)
			},
		},
		{
			name:     "AutoSpreadsheetPathWithoutHeaderPreservesAutoDetection",
			formBody: "ftp_path=%2Fdata%2Freport.xlsx&ftp_format=auto",
			assertStmt: func(t *testing.T, stmt api.FTPStatement) {
				t.Helper()
				require.NotNil(t, stmt.Format)
				require.Equal(t, api.FTPStatementFormatAuto, *stmt.Format)
				require.Nil(t, stmt.HasHeader)
			},
		},
		{
			name:       "MissingPathReturnsError",
			formBody:   "ftp_path=%20%20",
			wantErrMsg: "Path is required.",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(tc.formBody))
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

			var gotStatement api.QueryStatement
			var gotErrMsg string

			app.Post("/", func(c fiber.Ctx) error {
				gotStatement, gotErrMsg = buildQueryStatementFromRequest(c, api.DataSourceTypeFtp)
				return c.SendStatus(http.StatusOK)
			})

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusOK, resp.StatusCode)
			if tc.wantErrMsg != "" {
				require.Equal(t, tc.wantErrMsg, gotErrMsg)
				return
			}
			require.Empty(t, gotErrMsg)
			ftpStatement, err := gotStatement.AsFTPStatement()
			require.NoError(t, err)
			tc.assertStmt(t, ftpStatement)
		})
	}
}

func TestBuildQueryStatementFromRequest_Git(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		formBody   string
		assertStmt func(t *testing.T, stmt api.GitStatement)
		wantErrMsg string
	}{
		{
			name:     "MinimalPathOnly",
			formBody: "git_path=data%2F%7B%7Brelease_date%7D%7D%2Fmanifest.json",
			assertStmt: func(t *testing.T, stmt api.GitStatement) {
				t.Helper()
				require.Equal(t, api.GitStatementTypeGit, stmt.Type)
				require.Equal(t, "data/{{release_date}}/manifest.json", stmt.Path)
			},
		},
		{
			name:     "WriteWithBranchAndMessage",
			formBody: "git_path=output%2Freport.csv&git_operation=write&git_branch=main&git_message=Update+report",
			assertStmt: func(t *testing.T, stmt api.GitStatement) {
				t.Helper()
				require.Equal(t, "output/report.csv", stmt.Path)
				require.NotNil(t, stmt.Operation)
				require.Equal(t, api.GitStatementOperationWrite, *stmt.Operation)
				require.NotNil(t, stmt.Branch)
				require.Equal(t, "main", *stmt.Branch)
				require.NotNil(t, stmt.Message)
				require.Equal(t, "Update report", *stmt.Message)
			},
		},
		{
			name:     "CSVFormatWithDelimiterAndHeader",
			formBody: "git_path=data%2Freport.csv&git_format=csv&git_delimiter=%2C&git_has_header=on",
			assertStmt: func(t *testing.T, stmt api.GitStatement) {
				t.Helper()
				require.NotNil(t, stmt.Format)
				require.Equal(t, api.GitStatementFormatCsv, *stmt.Format)
				require.NotNil(t, stmt.Delimiter)
				require.Equal(t, ",", *stmt.Delimiter)
				require.NotNil(t, stmt.HasHeader)
				require.True(t, *stmt.HasHeader)
			},
		},
		{
			name:     "NativeFileFormat",
			formBody: "git_path=data%2Farchive.bin&git_format=file",
			assertStmt: func(t *testing.T, stmt api.GitStatement) {
				t.Helper()
				require.NotNil(t, stmt.Format)
				require.Equal(t, api.GitStatementFormatFile, *stmt.Format)
			},
		},
		{
			name:     "SpreadsheetFormatWithSheetAndRange",
			formBody: "git_path=data%2Freport.xlsx&git_format=spreadsheet&git_sheet=Orders&git_range=A1%3AD20&git_has_header=on",
			assertStmt: func(t *testing.T, stmt api.GitStatement) {
				t.Helper()
				require.NotNil(t, stmt.Format)
				require.Equal(t, api.GitStatementFormatSpreadsheet, *stmt.Format)
				require.NotNil(t, stmt.Sheet)
				require.Equal(t, "Orders", *stmt.Sheet)
				require.NotNil(t, stmt.Range)
				require.Equal(t, "A1:D20", *stmt.Range)
				require.NotNil(t, stmt.HasHeader)
				require.True(t, *stmt.HasHeader)
			},
		},
		{
			name:     "SpreadsheetFormatWithoutHeaderPreservesAutoDetection",
			formBody: "git_path=data%2Freport.xlsx&git_format=spreadsheet&git_sheet=Orders&git_range=A1%3AD20",
			assertStmt: func(t *testing.T, stmt api.GitStatement) {
				t.Helper()
				require.NotNil(t, stmt.Format)
				require.Equal(t, api.GitStatementFormatSpreadsheet, *stmt.Format)
				require.Nil(t, stmt.HasHeader)
			},
		},
		{
			name:     "AutoSpreadsheetPathWithoutHeaderPreservesAutoDetection",
			formBody: "git_path=data%2Freport.xlsx&git_format=auto",
			assertStmt: func(t *testing.T, stmt api.GitStatement) {
				t.Helper()
				require.NotNil(t, stmt.Format)
				require.Equal(t, api.GitStatementFormatAuto, *stmt.Format)
				require.Nil(t, stmt.HasHeader)
			},
		},
		{
			name:       "MissingPathReturnsError",
			formBody:   "git_path=%20%20",
			wantErrMsg: "Path is required.",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(tc.formBody))
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

			var gotStatement api.QueryStatement
			var gotErrMsg string

			app.Post("/", func(c fiber.Ctx) error {
				gotStatement, gotErrMsg = buildQueryStatementFromRequest(c, api.DataSourceTypeGit)
				return c.SendStatus(http.StatusOK)
			})

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusOK, resp.StatusCode)
			if tc.wantErrMsg != "" {
				require.Equal(t, tc.wantErrMsg, gotErrMsg)
				return
			}
			require.Empty(t, gotErrMsg)
			gitStatement, err := gotStatement.AsGitStatement()
			require.NoError(t, err)
			tc.assertStmt(t, gitStatement)
		})
	}
}

func TestBuildQueryStatementFromRequest_FTPAndGitLikeRequirePath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		dataSourceType api.DataSourceType
		formBody       string
	}{
		{name: "FTP", dataSourceType: api.DataSourceTypeFtp, formBody: "ftp_path=%20%20"},
		{name: "Git", dataSourceType: api.DataSourceTypeGit, formBody: "git_path=%20%20"},
		{name: "GitHub", dataSourceType: api.DataSourceTypeGithub, formBody: "git_path=%20%20"},
		{name: "GitLab", dataSourceType: api.DataSourceTypeGitlab, formBody: "git_path=%20%20"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(testCase.formBody))
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)

			var gotErrMsg string

			app.Post("/", func(c fiber.Ctx) error {
				_, gotErrMsg = buildQueryStatementFromRequest(c, testCase.dataSourceType)
				return c.SendStatus(http.StatusOK)
			})

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, "Path is required.", gotErrMsg)
		})
	}
}

func TestS3QueryStatementEditorPreservesCompleteOperation(t *testing.T) {
	t.Parallel()
	const saved = `{"type":"s3","operation":"write","bucket":"fixture","key":"exports/{{day}}.csv","format":"csv","content":"{{input}}","overwrite":false,"content_type":"text/csv","metadata":{"source":"fixture"},"delimiter":";","has_header":true,"sheet":"Orders","range":"A1:C9"}`
	for _, raw := range []string{saved, `{"type":"s3","operation":"read","key":"orders.json"}`, `{`, `[]`, `null`, `{"type":"http","method":"GET","path":"/"}`} {
		t.Run(raw, func(t *testing.T) {
			app := fiber.New()
			app.Post("/", func(c fiber.Ctx) error {
				statement, message := buildQueryStatementFromRequest(c, api.DataSourceTypeS3)
				if raw == saved || strings.Contains(raw, `"operation":"read"`) {
					require.Empty(t, message)
					encoded, err := json.Marshal(statement)
					require.NoError(t, err)
					require.JSONEq(t, raw, string(encoded))
				} else {
					require.Equal(t, "Enter an S3 operation as a JSON object with type s3.", message)
				}
				return c.SendStatus(http.StatusOK)
			})
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(url.Values{"statement": {raw}, "s3_key": {"guided.csv"}, "s3_operation": {"read"}}.Encode()))
			request.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			response, err := app.Test(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			require.Equal(t, http.StatusOK, response.StatusCode)
		})
	}
}

func TestBuildQueryStatementFromRequest_S3Read(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	var gotStatement api.QueryStatement
	var gotErrMsg string
	app.Post("/", func(c fiber.Ctx) error {
		gotStatement, gotErrMsg = buildQueryStatementFromRequest(c, api.DataSourceTypeS3)
		return c.SendStatus(http.StatusOK)
	})

	values := url.Values{
		"s3_bucket":     {"archive"},
		"s3_key":        {"exports/{{runtime.run.date}}/customers.xlsx"},
		"s3_operation":  {"read"},
		"s3_format":     {"spreadsheet"},
		"s3_delimiter":  {";"},
		"s3_has_header": {"on"},
		"s3_sheet":      {"Customers"},
		"s3_range":      {"A1:D20"},
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, gotErrMsg)

	statement, err := gotStatement.AsS3Statement()
	require.NoError(t, err)
	require.Equal(t, "archive", *statement.Bucket)
	require.Equal(t, "exports/{{runtime.run.date}}/customers.xlsx", *statement.Key)
	require.Equal(t, api.S3StatementOperationRead, *statement.Operation)
	require.Equal(t, api.S3StatementFormatSpreadsheet, *statement.Format)
	require.Equal(t, ";", *statement.Delimiter)
	require.True(t, *statement.HasHeader)
	require.Equal(t, "Customers", *statement.Sheet)
	require.Equal(t, "A1:D20", *statement.Range)
}

func TestBuildQueryStatementFromRequest_S3RejectsWrite(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	var gotErrMsg string
	app.Post("/", func(c fiber.Ctx) error {
		_, gotErrMsg = buildQueryStatementFromRequest(c, api.DataSourceTypeS3)
		return c.SendStatus(http.StatusOK)
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader("s3_key=exports%2Fcustomers.json&s3_operation=write"))
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "S3 queries support read operations only.", gotErrMsg)
}

func TestIsQueryEditorSupportedDataSourceType_IncludesFTPAndGitLike(t *testing.T) {
	t.Parallel()

	require.True(t, isQueryEditorSupportedDataSourceType(api.DataSourceTypeFtp))
	require.True(t, isQueryEditorSupportedDataSourceType(api.DataSourceTypeGit))
	require.True(t, isQueryEditorSupportedDataSourceType(api.DataSourceTypeGithub))
	require.True(t, isQueryEditorSupportedDataSourceType(api.DataSourceTypeGitlab))
	require.True(t, isQueryEditorSupportedDataSourceType(api.DataSourceTypeS3))
}

func TestQueryExecutionResultToView_HTTPIncludesStatusAndBody(t *testing.T) {
	t.Parallel()

	httpExecution := api.HTTPExecutionResult{
		DurationMs: 21,
		Payload: struct {
			Body       any                      `json:"body,omitempty"`
			File       *api.FilePayloadMetadata `json:"file,omitempty"`
			Headers    *map[string]string       `json:"headers,omitempty"`
			StatusCode int                      `json:"status_code"`
		}{
			Body: map[string]any{
				"error": "not found",
			},
			StatusCode: 404,
		},
		ResultType: api.HTTPExecutionResultResultTypeHttp,
		Success:    true,
		Type:       "http",
	}

	result := api.QueryExecutionResult{}
	require.NoError(t, result.FromHTTPExecutionResult(httpExecution))

	view := queryExecutionResultToView("fake-query", result)
	require.NotNil(t, view)
	require.Equal(t, "http", view.Type)
	require.True(t, view.Success)
	require.Equal(t, 21, view.DurationMs)
	require.Equal(t, []string{"Status code: 404"}, view.Summary)
	require.Contains(t, view.Raw, `"error": "not found"`)
}

func TestQueryExecutionResultToView_HTTPIncludesFileMetadata(t *testing.T) {
	t.Parallel()

	filename := "users.pdf"
	httpExecution := api.HTTPExecutionResult{
		DurationMs: 21,
		Payload: struct {
			Body       any                      `json:"body,omitempty"`
			File       *api.FilePayloadMetadata `json:"file,omitempty"`
			Headers    *map[string]string       `json:"headers,omitempty"`
			StatusCode int                      `json:"status_code"`
		}{
			File: &api.FilePayloadMetadata{
				Digest:          "abc123",
				DigestAlgorithm: "sha256",
				Filename:        &filename,
				MimeType:        "application/pdf",
				SizeBytes:       42,
			},
			StatusCode: 200,
		},
		ResultType: api.HTTPExecutionResultResultTypeHttp,
		Success:    true,
		Type:       "http",
	}

	result := api.QueryExecutionResult{}
	require.NoError(t, result.FromHTTPExecutionResult(httpExecution))

	view := queryExecutionResultToView("fake-query", result)
	require.NotNil(t, view)
	require.Equal(t, []string{
		"Status code: 200",
		"Filename: users.pdf",
		"MIME type: application/pdf",
		"Size: 42 bytes",
	}, view.Summary)
	require.Contains(t, view.Raw, `"filename": "users.pdf"`)
	require.Contains(t, view.Raw, `"mime_type": "application/pdf"`)
	require.NotEqual(t, "null", strings.TrimSpace(view.Raw))
}

func TestQueryExecutionResultToView_MongoDBIncludesRawPayload(t *testing.T) {
	t.Parallel()

	matchedCount := int64(3)
	modifiedCount := int64(2)

	mongoExecution := api.MongoDBExecutionResult{
		DurationMs: 17,
		ResultType: api.MongoDBExecutionResultResultTypeMongodb,
		Success:    true,
		Type:       "mongodb",
	}
	mongoExecution.Payload.OperationType = api.MongoDBExecutionResultPayloadOperationTypeUpdateMany
	mongoExecution.Payload.MatchedCount = &matchedCount
	mongoExecution.Payload.ModifiedCount = &modifiedCount

	result := api.QueryExecutionResult{}
	require.NoError(t, result.FromMongoDBExecutionResult(mongoExecution))

	view := queryExecutionResultToView("orders-update", result)
	require.NotNil(t, view)
	require.Equal(t, "mongodb", view.Type)
	require.True(t, view.Success)
	require.Equal(t, 17, view.DurationMs)
	require.Contains(t, view.Raw, `"matched_count": 3`)
	require.Contains(t, view.Raw, `"modified_count": 2`)
}

func TestQueryExecutionResultToView_ObjectRowsPreservesStructuredCells(t *testing.T) {
	t.Parallel()
	execution := api.ObjectRowsExecutionResult{Type: "google_workspace", Success: true, ResultType: api.ObjectRowsExecutionResultResultTypeObjectRows}
	execution.Payload.ColumnNames = []string{"content", "tabs", "name", "count", "editable"}
	execution.Payload.Rows = []map[string]any{{"content": map[string]any{"documentId": "synthetic", "body": map[string]any{"text": "Synthetic <content>"}}, "tabs": []any{map[string]any{"title": "Tab 1"}}, "name": "Synthetic", "count": 2, "editable": true}}
	execution.Payload.RowCount = 1
	var result api.QueryExecutionResult
	require.NoError(t, result.FromObjectRowsExecutionResult(execution))
	view := queryExecutionResultToView("native-read", result)
	require.NotNil(t, view)
	require.Len(t, view.Rows, 1)
	require.JSONEq(t, `{"documentId":"synthetic","body":{"text":"Synthetic <content>"}}`, view.Rows[0]["content"])
	require.JSONEq(t, `[{"title":"Tab 1"}]`, view.Rows[0]["tabs"])
	require.Equal(t, "Synthetic", view.Rows[0]["name"])
	require.Equal(t, "2", view.Rows[0]["count"])
	require.Equal(t, "true", view.Rows[0]["editable"])
}

func TestQueryExecutionResultToView_ObjectRowsIncludesRowsAndPagination(t *testing.T) {
	t.Parallel()

	nextCursor := "200"
	operation := "search"
	objectRowsExecution := api.ObjectRowsExecutionResult{
		DurationMs: 34,
		Payload: struct {
			ColumnNames      []string                 `json:"column_names"`
			HasMore          bool                     `json:"has_more"`
			NextCursor       *string                  `json:"next_cursor"`
			ObjectType       string                   `json:"object_type"`
			Operation        *string                  `json:"operation,omitempty"`
			ProviderMetadata *map[string]interface{}  `json:"provider_metadata,omitempty"`
			RowCount         int                      `json:"row_count"`
			Rows             []map[string]interface{} `json:"rows"`
		}{
			ColumnNames: []string{"hs_object_id", "email"},
			HasMore:     true,
			NextCursor:  &nextCursor,
			ObjectType:  "contacts",
			Operation:   &operation,
			RowCount:    1,
			Rows: []map[string]any{{
				"hs_object_id": "101",
				"email":        "alice@example.com",
			}},
		},
		ResultType: api.ObjectRowsExecutionResultResultTypeObjectRows,
		Success:    true,
		Type:       "hubspot",
	}

	result := api.QueryExecutionResult{}
	require.NoError(t, result.FromObjectRowsExecutionResult(objectRowsExecution))

	view := queryExecutionResultToView("hubspot-contacts", result)
	require.NotNil(t, view)
	require.Equal(t, "hubspot", view.Type)
	require.True(t, view.Success)
	require.Equal(t, 34, view.DurationMs)
	require.Equal(t, []string{"hs_object_id", "email"}, view.Columns)
	require.Len(t, view.Rows, 1)
	require.Equal(t, "alice@example.com", view.Rows[0]["email"])
	require.Contains(t, view.Summary, "Object type: contacts")
	require.Contains(t, view.Summary, "Row count: 1")
	require.Contains(t, view.Summary, "Has more: true")
	require.Contains(t, view.Summary, "Operation: search")
	require.Contains(t, view.Summary, "Next cursor: 200")
}

func TestQueryExecutionResultToView_S3IncludesOperationSummaryAndRawPayload(t *testing.T) {
	t.Parallel()

	rowCount := 2
	targetPath := "exports/users.json"
	writtenRows := 2
	writtenBytes := 75

	s3Execution := api.S3ExecutionResult{
		DurationMs: 42,
		ResultType: api.S3ExecutionResultResultTypeS3,
		Success:    true,
		Type:       "s3",
	}
	s3Execution.Payload.Format = api.S3ExecutionResultPayloadFormatJson
	s3Execution.Payload.Operation = api.S3ExecutionResultPayloadOperationWrite
	s3Execution.Payload.RowCount = &rowCount
	s3Execution.Payload.TargetPath = &targetPath
	s3Execution.Payload.WrittenRows = &writtenRows
	s3Execution.Payload.WrittenBytes = &writtenBytes

	result := api.QueryExecutionResult{}
	require.NoError(t, result.FromS3ExecutionResult(s3Execution))

	view := queryExecutionResultToView("s3-export", result)
	require.NotNil(t, view)
	require.Equal(t, "s3", view.Type)
	require.True(t, view.Success)
	require.Equal(t, 42, view.DurationMs)
	require.Contains(t, view.Summary, "Operation: write")
	require.Contains(t, view.Summary, "Format: json")
	require.Contains(t, view.Summary, "Key: exports/users.json")
	require.Contains(t, view.Summary, "Row count: 2")
	require.Contains(t, view.Raw, `"written_rows": 2`)
	require.Contains(t, view.Raw, `"written_bytes": 75`)
}

func TestFileWriteEditorPreservesStatementAndStickyValues(t *testing.T) {
	for _, connector := range []api.DataSourceType{api.DataSourceTypeFtp, api.DataSourceTypeGit} {
		t.Run(string(connector), func(t *testing.T) {
			prefix := string(connector)
			values := url.Values{
				"statement_type": {prefix}, prefix + "_path": {"exports/users.json"},
				prefix + "_operation": {"write"}, prefix + "_content": {`[{"id":1}]`},
				prefix + "_format": {"json"}, "git_message": {"Export users"}, "ftp_overwrite": {"on"},
			}
			app := fiber.New()
			var statement api.QueryStatement
			var message string
			var sticky views.QueryEditorFormData
			app.Post("/", func(c fiber.Ctx) error {
				statement, message = buildQueryStatementFromRequest(c, connector)
				sticky = queryEditorFormDataFromRequest(c)
				return c.SendStatus(http.StatusOK)
			})
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(values.Encode()))
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			resp, err := app.Test(req)
			require.NoError(t, err)
			_ = resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Empty(t, message)
			query := api.Query{Name: "Write users", Slug: "write-users", QueryStatement: statement}
			form := views.QueryEditorFormDataFromQuery(query)
			if connector == api.DataSourceTypeGit {
				require.Equal(t, "write", form.GitOperation)
				require.JSONEq(t, values.Get("git_content"), form.GitContent)
				require.JSONEq(t, form.GitContent, sticky.GitContent)
				require.Equal(t, "Export users", sticky.GitMessage)
			} else {
				require.Equal(t, "write", form.FTPOperation)
				require.JSONEq(t, values.Get("ftp_content"), form.FTPContent)
				require.JSONEq(t, form.FTPContent, sticky.FTPContent)
				require.True(t, sticky.FTPOverwrite)
			}
		})
	}
}

func TestWorkspaceOperationFormValidatesAndPreservesOrderedRows(t *testing.T) {
	for _, tc := range []struct{ name, operation, area, columns, rows, want string }{
		{"read", "read", "A1:B3", "", "", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"read","worksheet_id":12,"range":"A1:B3"}`},
		{"trimmed read", " read ", "A1:B3", "", "", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"read","worksheet_id":12,"range":"A1:B3"}`},
		{"empty operation", "", "A1:B3", "", "", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"read","worksheet_id":12,"range":"A1:B3"}`},
		{"blank operation", " \t ", "A1:B3", "", "", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"read","worksheet_id":12,"range":"A1:B3"}`},
		{"input update", "update", "B2:C3", `["customer","amount"]`, "{{input}}", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"update","worksheet_id":12,"range":"B2:C3","columns":["customer","amount"],"rows":"{{input}}"}`},
		{"padded input update", "update", "B2:C3", `["customer","amount"]`, " \n{{input}}\t ", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"update","worksheet_id":12,"range":"B2:C3","columns":["customer","amount"],"rows":"{{input}}"}`},
		{"trimmed update", " update ", "B2:C3", `["customer","amount"]`, "{{input}}", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"update","worksheet_id":12,"range":"B2:C3","columns":["customer","amount"],"rows":"{{input}}"}`},
		{"literal update", "update", "B2:C2", `["customer","amount"]`, `[{"customer":"Ada","amount":2}]`, `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"update","worksheet_id":12,"range":"B2:C2","columns":["customer","amount"],"rows":[{"customer":"Ada","amount":2}]}`},
		{"unbounded", "read", "A:B", "", "", ""},
		{"wrong height", "update", "B2:C3", `["customer","amount"]`, `[{"customer":"Ada","amount":2}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New()
			app.Post("/operation", func(c fiber.Ctx) error {
				statement, message := buildQueryStatementFromRequest(c, api.DataSourceTypeGoogleWorkspace)
				if message != "" {
					return c.Status(400).SendString(message)
				}
				return c.JSON(statement)
			})
			values := url.Values{"workspace_resource": {"spreadsheet"}, "workspace_spreadsheet_id": {"book"}, "workspace_worksheet_id": {"12"}, "workspace_operation": {tc.operation}, "workspace_range": {tc.area}, "workspace_columns": {tc.columns}, "workspace_rows": {tc.rows}}
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/operation", strings.NewReader(values.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response, err := app.Test(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			if tc.want == "" {
				require.Equal(t, 400, response.StatusCode)
			} else {
				require.Equal(t, 200, response.StatusCode)
				require.JSONEq(t, tc.want, string(body))
			}
		})
	}
}

func TestNativeWorkspaceOperationFormRoundTrip(t *testing.T) {
	for _, resource := range []string{"document", "presentation"} {
		for _, operation := range []string{"read", "create", "update", "export"} {
			t.Run(resource+operation, func(t *testing.T) {
				app := fiber.New()
				app.Post("/operation", func(c fiber.Ctx) error {
					statement, message := buildQueryStatementFromRequest(c, api.DataSourceTypeGoogleWorkspace)
					require.Empty(t, message)
					raw, err := statement.MarshalJSON()
					require.NoError(t, err)
					decoded, err := workspace.DecodeStatement(raw)
					require.NoError(t, err)
					require.Equal(t, resource, decoded.Resource)
					require.Equal(t, operation, decoded.Operation)
					form := views.QueryEditorFormDataFromQuery(api.Query{QueryStatement: statement})
					require.Equal(t, resource, form.WorkspaceResource)
					if operation == "update" {
						require.Equal(t, "{{name}}", form.WorkspaceMatchText)
						require.JSONEq(t, `"Alice"`, form.WorkspaceContent)
					}
					return c.JSON(statement)
				})
				values := url.Values{"workspace_resource": {resource}, "workspace_operation": {operation}, "workspace_file_id": {"native"}, "workspace_folder_id": {"folder"}, "workspace_name": {"Blank"}, "workspace_match_text": {"{{name}}"}, "workspace_content": {`"Alice"`}, "workspace_format": {"json"}}
				request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/operation", strings.NewReader(values.Encode()))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				response, err := app.Test(request)
				require.NoError(t, err)
				defer func() { _ = response.Body.Close() }()
				require.Equal(t, http.StatusOK, response.StatusCode)
			})
		}
	}
}

func TestNativeWorkspaceEmptyReplacementForm(t *testing.T) {
	app := fiber.New()
	app.Post("/operation", func(c fiber.Ctx) error {
		statement, message := buildQueryStatementFromRequest(c, api.DataSourceTypeGoogleWorkspace)
		require.Empty(t, message)
		raw, err := statement.MarshalJSON()
		require.NoError(t, err)
		decoded, err := workspace.DecodeStatement(raw)
		require.NoError(t, err)
		require.JSONEq(t, `""`, string(decoded.Content))
		return c.SendStatus(http.StatusOK)
	})
	values := url.Values{"workspace_resource": {"document"}, "workspace_operation": {"update"}, "workspace_file_id": {"native"}, "workspace_match_text": {"marker"}, "workspace_content": {""}}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/operation", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := app.Test(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusOK, response.StatusCode)
}

func TestBuildQueryStatementFromRequest_HTTPRejectsInvalidPagination(t *testing.T) {
	for _, pagination := range []string{
		`null`, `[]`, `{} trailing`,
		`{"cursor_param":"after","records_path":"items","next_cursor_path":"next","max_pages":3,"timeout_seconds":2,"unsupported":true}`,
	} {
		t.Run(pagination, func(t *testing.T) {
			app := fiber.New()
			var diagnostic string
			app.Post("/", func(c fiber.Ctx) error {
				_, diagnostic = buildQueryStatementFromRequest(c, api.DataSourceTypeHttp)
				return c.SendStatus(http.StatusOK)
			})
			values := url.Values{"http_method": {"GET"}, "http_path": {"/items"}, "http_pagination": {pagination}}
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(values.Encode()))
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			response, err := app.Test(req)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Contains(t, diagnostic, "HTTP pagination")
		})
	}
}

func TestQueryExecutionResultToView_SQLPreservesNullAndZero(t *testing.T) {
	t.Parallel()
	var result api.QueryExecutionResult
	require.NoError(t, json.Unmarshal([]byte(`{"result_type":"sql","type":"postgres","success":true,"duration_ms":1,"payload":{"row_count":1,"column_names":["missing","zero","empty","text"],"rows":[{"missing":null,"zero":0,"empty":"","text":"<nil>"}]}}`), &result))
	view := queryExecutionResultToView("inspect-comparisons", result)
	require.NotNil(t, view)
	require.Equal(t, []map[string]string{{"missing": "NULL", "zero": "0", "empty": "", "text": "<nil>"}}, view.Rows)
}

func TestSQLFileResultViewShowsMetadataWithoutEmptyRows(t *testing.T) {
	t.Parallel()
	var result api.QueryExecutionResult
	require.NoError(t, json.Unmarshal([]byte(`{"result_type":"sql","type":"postgres","success":true,"duration_ms":1,"payload":{"row_count":1000000,"column_names":["order_id"],"rows":[],"file":{"mime_type":"application/json","size_bytes":1078777880,"digest_algorithm":"sha256","digest":"synthetic-digest"}}}`), &result))
	view := queryExecutionResultToView("export-records", result)
	require.Empty(t, view.Columns)
	require.Empty(t, view.Rows)
	require.Contains(t, view.Summary, "Serialized row count: 1000000")
	require.Contains(t, view.Raw, "synthetic-digest")
}

func TestPostgresQueryFormPreservesAndValidatesFormat(t *testing.T) {
	for _, format := range []string{"rows", "file", "invalid"} {
		t.Run(format, func(t *testing.T) {
			app := fiber.New()
			app.Post("/", func(c fiber.Ctx) error {
				statement, diagnostic := buildQueryStatementFromRequest(c, api.DataSourceTypePostgres)
				if format == "invalid" {
					require.NotEmpty(t, diagnostic)
					return c.SendStatus(http.StatusBadRequest)
				}
				require.Empty(t, diagnostic)
				postgres, err := statement.AsPostgresStatement()
				require.NoError(t, err)
				require.NotNil(t, postgres.Format)
				require.Equal(t, format, string(*postgres.Format))
				return c.SendStatus(http.StatusOK)
			})
			values := url.Values{"statement": {"SELECT 1"}, "sql_format": {format}}
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(values.Encode()))
			request.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			response, err := app.Test(request)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
		})
	}
}

func TestBuildHTTPStatementPreservesAcknowledgementNumber(t *testing.T) {
	app := fiber.New()
	app.Post("/", func(c fiber.Ctx) error {
		statement, diagnostic := buildHTTPStatementFromRequest(c)
		require.Empty(t, diagnostic)
		encoded, err := json.Marshal(statement)
		require.NoError(t, err)
		require.Contains(t, string(encoded), `"equals":1.0`)
		return c.SendStatus(http.StatusOK)
	})
	values := url.Values{"http_method": {"POST"}, "http_path": {"/records"}, "http_acknowledgement_enabled": {"on"}, "http_acknowledgement_status_codes": {"200"}, "http_acknowledgement_path": {"code"}, "http_acknowledgement_equals": {"1.0"}}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	response, err := app.Test(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
}
