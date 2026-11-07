package frontend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestDataSourceBindingUpsertPayloadFromRequest(t *testing.T) {
	t.Parallel()

	form := url.Values{}
	form.Set("data_source_slug", "warehouse-prod")
	form.Set("protected_environment_confirmation", "prod")

	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		payload, validationErr := dataSourceBindingUpsertPayloadFromRequest(c)

		require.Empty(t, validationErr)
		require.Equal(t, api.Slug("warehouse-prod"), payload.DataSourceSlug)
		require.NotNil(t, payload.ProtectedEnvironmentConfirmation)
		require.Equal(t, api.NullableSlug("prod"), *payload.ProtectedEnvironmentConfirmation)
	})
}

func TestDataSourceBindingDeleteParamsFromRequest(t *testing.T) {
	t.Parallel()

	form := url.Values{}
	form.Set("protected_environment_confirmation", "prod")

	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		params := dataSourceBindingDeleteParamsFromRequest(c)

		require.NotNil(t, params.ProtectedEnvironmentConfirmation)
		require.Equal(t, api.ProtectedEnvironmentConfirmationParam("prod"), *params.ProtectedEnvironmentConfirmation)
	})
}

func TestPipelineBindingReturnHrefPreservesListContext(t *testing.T) {
	t.Parallel()

	form := url.Values{}
	form.Set("return_to", "list")
	form.Set("active_environment_slug", "prod")

	withDatasourceFormContext(t, form, func(c fiber.Ctx) {
		href := pipelineBindingReturnHref(c, "acme-hq", "default", "customer-sync")

		require.Equal(t, "/orgs/acme-hq/projects/default/pipelines?environment_slug=prod&pipeline_slug=customer-sync", href)
	})
}

func TestRouter_HandleDataSourceBindingSubmit_ForwardsContext(t *testing.T) {
	t.Parallel()

	bindingUpsertResponse := `{"pipeline_slug":"customer-sync","environment_slug":"prod","data_source_ref_slug":"orders-db","data_source_ref_name":"Orders DB","data_source_slug":"warehouse-prod","data_source_name":"Warehouse Prod","data_source_type":"postgres","data_source_active":true,"data_source_read_only":false,"bound_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-03T00:00:00Z"}`
	for _, tt := range []bindingActionSubmitCase{
		{
			name:          "ProjectOverviewUpsertForwardsProjectContext",
			method:        http.MethodPost,
			path:          "/organizations/acme-hq/projects/default/environments/prod/data-source-bindings/orders-db/upsert?pipeline_slug=customer-sync&return_to=full",
			form:          url.Values{"data_source_slug": {"warehouse-prod"}, "protected_environment_confirmation": {"prod"}},
			apiMethod:     http.MethodPut,
			apiPath:       "/api/organizations/acme-hq/projects/default/pipelines/customer-sync/environments/prod/data-source-bindings/orders-db",
			apiResponse:   bindingUpsertResponse,
			reloadProject: true,
			wantStatus:    http.StatusOK,
			wantBody:      "Data-source binding saved.",
			assertCaptured: func(t *testing.T, captured bindingAPIRequest) {
				t.Helper()

				require.Equal(t, http.MethodPut, captured.Method)
				require.Equal(t, "/api/organizations/acme-hq/projects/default/pipelines/customer-sync/environments/prod/data-source-bindings/orders-db", captured.Path)
				require.Empty(t, captured.Query)
				require.Equal(t, "warehouse-prod", captured.DataSourceSlug)
				require.Equal(t, "prod", captured.ProtectedEnvironmentConfirmation)
			},
		},
		{
			name:        "PipelineUpsertForwardsBindingAndReturnContext",
			method:      http.MethodPost,
			path:        "/orgs/acme-hq/projects/default/pipelines/customer-sync/environments/prod/data-source-bindings/orders-db/upsert?environment_slug=dev&return_to=list",
			form:        url.Values{"data_source_slug": {"warehouse-prod"}, "protected_environment_confirmation": {"prod"}},
			htmx:        true,
			apiMethod:   http.MethodPut,
			apiPath:     "/api/organizations/acme-hq/projects/default/pipelines/customer-sync/environments/prod/data-source-bindings/orders-db",
			apiResponse: bindingUpsertResponse,
			wantStatus:  http.StatusOK,
			wantHeaders: map[string]string{
				"HX-Redirect": "/orgs/acme-hq/projects/default/pipelines?environment_slug=dev&pipeline_slug=customer-sync",
			},
			assertCaptured: func(t *testing.T, captured bindingAPIRequest) {
				t.Helper()

				require.Equal(t, http.MethodPut, captured.Method)
				require.Equal(t, "/api/organizations/acme-hq/projects/default/pipelines/customer-sync/environments/prod/data-source-bindings/orders-db", captured.Path)
				require.Empty(t, captured.Query)
				require.Equal(t, "warehouse-prod", captured.DataSourceSlug)
				require.Equal(t, "prod", captured.ProtectedEnvironmentConfirmation)
			},
		},
		{
			name:       "PipelineDeleteForwardsDeleteConfirmationAndReturnContext",
			method:     http.MethodPost,
			path:       "/orgs/acme-hq/projects/default/pipelines/customer-sync/environments/prod/data-source-bindings/orders-db/delete?environment_slug=dev",
			form:       url.Values{"protected_environment_confirmation": {"prod"}},
			htmx:       true,
			apiMethod:  http.MethodDelete,
			apiPath:    "/api/organizations/acme-hq/projects/default/pipelines/customer-sync/environments/prod/data-source-bindings/orders-db",
			apiStatus:  http.StatusNoContent,
			wantStatus: http.StatusOK,
			wantHeaders: map[string]string{
				"HX-Redirect": "/orgs/acme-hq/projects/default/pipelines/customer-sync?environment_slug=dev&section=deployments#pipeline-bindings",
			},
			assertCaptured: func(t *testing.T, captured bindingAPIRequest) {
				t.Helper()

				require.Equal(t, http.MethodDelete, captured.Method)
				require.Equal(t, "/api/organizations/acme-hq/projects/default/pipelines/customer-sync/environments/prod/data-source-bindings/orders-db", captured.Path)
				require.Equal(t, "prod", captured.QueryValues.Get("protected_environment_confirmation"))
				require.Empty(t, captured.DataSourceSlug)
				require.Empty(t, captured.ProtectedEnvironmentConfirmation)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runBindingActionSubmitCase(t, tt)
		})
	}
}

func TestRouter_HandleDataSourceBindingSubmit_RejectsInvalidProjectPipelineSlug(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		path string
		form url.Values
	}{
		{
			name: "ProjectOverviewUpsertMissingPipelineSlug",
			path: "/organizations/acme-hq/projects/default/environments/prod/data-source-bindings/orders-db/upsert",
			form: url.Values{"data_source_slug": {"warehouse-prod"}},
		},
		{
			name: "ProjectOverviewUpsertMalformedPipelineSlug",
			path: "/organizations/acme-hq/projects/default/environments/prod/data-source-bindings/orders-db/upsert",
			form: url.Values{"pipeline_slug": {"bad slug"}, "data_source_slug": {"warehouse-prod"}},
		},
		{
			name: "ProjectOverviewDeleteMissingPipelineSlug",
			path: "/organizations/acme-hq/projects/default/environments/prod/data-source-bindings/orders-db/delete",
			form: url.Values{},
		},
		{
			name: "ProjectOverviewDeleteMalformedPipelineSlug",
			path: "/organizations/acme-hq/projects/default/environments/prod/data-source-bindings/orders-db/delete?pipeline_slug=bad%20slug",
			form: url.Values{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var apiCalled atomic.Bool
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				apiCalled.Store(true)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer apiServer.Close()

			app, authCookie := setupBindingActionTestApp(t, apiServer.URL)
			req := httptest.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				tt.path,
				strings.NewReader(tt.form.Encode()),
			)
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			require.Contains(t, string(body), "invalid binding context")
			require.False(t, apiCalled.Load())
		})
	}
}

type bindingActionSubmitCase struct {
	name           string
	method         string
	path           string
	form           url.Values
	htmx           bool
	apiMethod      string
	apiPath        string
	apiStatus      int
	apiResponse    string
	reloadProject  bool
	wantStatus     int
	wantHeaders    map[string]string
	wantBody       string
	assertCaptured func(*testing.T, bindingAPIRequest)
}

// runBindingActionSubmitCase exercises one SSR binding form submit against a fake API server.
func runBindingActionSubmitCase(t *testing.T, tt bindingActionSubmitCase) {
	t.Helper()

	var captured bindingAPIRequest
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		if r.Method == tt.apiMethod && r.URL.Path == tt.apiPath {
			var err error
			captured, err = captureBindingAPIRequest(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if tt.apiResponse != "" {
				writeJSON(w, tt.apiResponse)
				return
			}
			w.WriteHeader(tt.apiStatus)
			return
		}
		if tt.reloadProject {
			writeProjectBindingReloadFixture(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	app, authCookie := setupBindingActionTestApp(t, apiServer.URL)

	req := httptest.NewRequestWithContext(
		context.Background(),
		tt.method,
		tt.path,
		strings.NewReader(tt.form.Encode()),
	)
	req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
	req.Header.Set("Cookie", authCookie)
	if tt.htmx {
		req.Header.Set("HX-Request", "true")
	}
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, tt.wantStatus, resp.StatusCode)
	for name, value := range tt.wantHeaders {
		require.Equal(t, value, resp.Header.Get(name))
	}
	if tt.wantBody != "" {
		require.Contains(t, string(body), tt.wantBody)
	}
	tt.assertCaptured(t, captured)
}

type bindingAPIRequest struct {
	Method                           string
	Path                             string
	Query                            string
	QueryValues                      url.Values
	DataSourceSlug                   string
	ProtectedEnvironmentConfirmation string
}

func setupBindingActionTestApp(t *testing.T, apiBaseURL string) (*fiber.App, string) {
	t.Helper()

	store := session.NewStore()
	router, err := NewRouter(apiBaseURL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	setActiveOrganizationSlugInSession(t, app, authCookie, "acme-hq")
	return app, authCookie
}

func captureBindingAPIRequest(r *http.Request) (bindingAPIRequest, error) {
	captured := bindingAPIRequest{
		Method:      r.Method,
		Path:        r.URL.Path,
		Query:       r.URL.RawQuery,
		QueryValues: r.URL.Query(),
	}
	if r.Method == http.MethodPut {
		var body api.UpsertDataSourceBindingJSONRequestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return bindingAPIRequest{}, fmt.Errorf("decode binding request: %w", err)
		}
		captured.DataSourceSlug = body.DataSourceSlug
		if body.ProtectedEnvironmentConfirmation != nil {
			captured.ProtectedEnvironmentConfirmation = *body.ProtectedEnvironmentConfirmation
		}
	}
	return captured, nil
}

func writeProjectBindingReloadFixture(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
		writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
		writeJSON(w, `{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
		writeJSON(w, `{"projects":[{"name":"Default project","slug":"default","pipeline_backend":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default":
		writeJSON(w, `{"name":"Default project","slug":"default","pipeline_backend":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments":
		writeJSON(w, `{"environments":[{"name":"Production","slug":"prod","description":"Customer target","protected":true,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments":
		writeJSON(w, `{"project_environments":[{"environment_slug":"prod","environment_name":"Production","environment_description":"Customer target","environment_protected":true,"is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines":
		writeJSON(w, `{"pipelines":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/data-source-refs":
		writeJSON(w, `{"data_source_refs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
		writeJSON(w, `{"data_sources":[{"name":"Warehouse Prod","slug":"warehouse-prod","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"warehouse"},"is_active":true,"read_only":false,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/data-source-bindings":
		writeJSON(w, `{"data_source_bindings":[{"pipeline_slug":"customer-sync","environment_slug":"prod","data_source_ref_slug":"orders-db","data_source_ref_name":"Orders DB","data_source_slug":"warehouse-prod","data_source_name":"Warehouse Prod","data_source_type":"postgres","data_source_active":true,"data_source_read_only":false,"bound_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-03T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestPipelineBindingReturnHrefPreservesOnlyCurrentPipeline(t *testing.T) {
	for _, current := range []string{
		"http://example.com/orgs/acme-hq/projects/default/pipelines/customer-sync?environment_slug=prod&section=deployments&list_return=%2Forgs%2Facme-hq%2Fprojects%2Fdefault%2Fpipelines%3Fq%3Dcustomers",
		"http://example.com/orgs/other/projects/default/pipelines/customer-sync?section=git",
		"https://evil.test/orgs/acme-hq/projects/default/pipelines/customer-sync?section=git",
	} {
		withDatasourceFormContext(t, url.Values{}, func(c fiber.Ctx) {
			c.Request().Header.Set("Host", "example.com")
			c.Request().Header.Set("HX-Current-URL", current)
			result := pipelineBindingReturnHref(c, "acme-hq", "default", "customer-sync")
			if strings.Contains(current, "list_return=") {
				require.Equal(t, strings.TrimPrefix(current, "http://example.com"), result)
			} else {
				require.Equal(t, "/orgs/acme-hq/projects/default/pipelines/customer-sync?section=deployments#pipeline-bindings", result)
			}
		})
	}
}
