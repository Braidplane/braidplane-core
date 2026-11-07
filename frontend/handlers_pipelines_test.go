package frontend

import (
	"context"
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

func TestPipelineValidationReturnHref(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		hxCurrent  string
		wantReturn string
	}{
		{
			name:       "ExplicitReturnTarget",
			body:       "return_to=%2Forgs%2Facme%2Fprojects%2Fdefault%2Fpipelines%3Fenvironment_slug%3Dprod%26pipeline_slug%3Dusers-sync%26filter%3Dfailed",
			hxCurrent:  "http://app.test/orgs/acme/projects/default/pipelines?environment_slug=prod&pipeline_slug=users-sync",
			wantReturn: "/orgs/acme/projects/default/pipelines?environment_slug=prod&pipeline_slug=users-sync&filter=failed",
		},
		{
			name:       "HTMXCurrentURL",
			hxCurrent:  "http://app.test/orgs/acme/projects/default/pipelines?environment_slug=prod&pipeline_slug=users-sync&filter=failed",
			wantReturn: "/orgs/acme/projects/default/pipelines?environment_slug=prod&pipeline_slug=users-sync&filter=failed",
		},
		{
			name:       "FallbackDetail",
			body:       "return_to=https%3A%2F%2Fevil.example%2Fcapture",
			hxCurrent:  "https://evil.example/capture",
			wantReturn: "/orgs/acme/projects/default/pipelines/users-sync?environment_slug=prod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			var got string
			app.Post("/submit", func(c fiber.Ctx) error {
				got = pipelineValidationReturnHref(c, "acme", "default", "prod", "users-sync")
				return c.SendStatus(http.StatusNoContent)
			})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://app.test/submit", strings.NewReader(tt.body))
			req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationForm)
			if tt.hxCurrent != "" {
				req.Header.Set("HX-Current-URL", tt.hxCurrent)
			}
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusNoContent, resp.StatusCode)
			require.Equal(t, tt.wantReturn, got)
		})
	}
}

func TestSafeCurrentRequestPathAcceptsSameHostnameWithPort(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"/orgs/acme/projects/default/pipelines?environment_slug=prod",
		safeCurrentRequestPath(
			"http://app.test:8080/orgs/acme/projects/default/pipelines?environment_slug=prod",
			"app.test:3000",
		),
	)
	require.Empty(t, safeCurrentRequestPath("http://evil.example:8080/capture", "app.test:3000"))
}

func TestRouter_LatestPipelineRunsByPipelineViaAPI_ReturnsPartialSuccess(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet &&
			r.URL.Path == "/api/organizations/acme-hq/projects/ops/environments/prod/runs" &&
			r.URL.Query().Get("pipeline_slug") == "users-sync":
			_, _ = w.Write([]byte(
				`{"runs":[{"run_slug":"run-001","pipeline_slug":"users-sync","pipeline_name":"Users Sync","environment_slug":"prod","status":"succeeded","started_at":"2026-05-14T10:30:00Z","duration_ms":1250}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case r.Method == http.MethodGet &&
			r.URL.Path == "/api/organizations/acme-hq/projects/ops/environments/prod/runs" &&
			r.URL.Query().Get("pipeline_slug") == "orders-export":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"run lookup failed"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	latestRuns, status, errorResponse, err := router.latestPipelineRunsByPipelineViaAPI(
		context.Background(),
		"",
		"acme-hq",
		"ops",
		"prod",
		[]api.PipelineRef{
			{Slug: "users-sync"},
			{Slug: "orders-export"},
		},
	)

	require.Error(t, err)
	require.NotNil(t, errorResponse)
	require.Equal(t, "run lookup failed", errorResponse.Error.Message)
	require.Equal(t, http.StatusInternalServerError, status)
	require.Len(t, latestRuns, 1)
	require.Equal(t, "run-001", latestRuns["users-sync"].RunSlug)
	require.NotContains(t, latestRuns, "orders-export")
}

func TestRouter_ListAllPipelinesViaAPI_ReturnsErrorOnRepeatedCursor(t *testing.T) {
	t.Parallel()

	requests := 0
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.Equal(t, "/api/organizations/acme-hq/projects/default/pipelines", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("limit"))
		requests++
		if requests == 2 {
			assert.Equal(t, "stuck", r.URL.Query().Get("cursor"))
			assert.Equal(t, "next", r.URL.Query().Get("direction"))
		}
		_, _ = w.Write([]byte(`{"pipelines":[],"has_more":true,"next_cursor":"stuck","prev_cursor":null}`))
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	pipelines, status, errorResponse, err := router.listAllPipelinesViaAPI(
		t.Context(),
		"",
		"acme-hq",
		"default",
	)

	require.Error(t, err)
	require.Nil(t, pipelines)
	require.Nil(t, errorResponse)
	require.Equal(t, http.StatusInternalServerError, status)
	require.Contains(t, err.Error(), "repeated cursor")
	require.Equal(t, 2, requests)
}

func TestRouter_HandlePipelinesPage_RendersList(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write([]byte(
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/default/pipelines":
			assert.Equal(t, "20", r.URL.Query().Get("limit"))
			assert.Equal(t, "page-2", r.URL.Query().Get("cursor"))
			assert.Equal(t, "prev", r.URL.Query().Get("direction"))
			_, _ = w.Write(
				[]byte(
					`{"pipelines":[{"slug":"customer-sync","name":"Customer Sync","sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"},{"slug":"nightly-export","name":"Nightly Export","sync_status":"drifted","created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}],"has_more":true,"next_cursor":"page-3","prev_cursor":"page-1"}`,
				),
			)
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write([]byte(
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
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
		"/orgs/acme-hq/projects/default/pipelines?cursor=page-2&direction=prev",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Pipelines")
	require.Contains(t, string(body), "Customer Sync")
	require.Contains(t, string(body), "nightly-export")
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/pipelines?cursor=page-2&amp;direction=prev&amp;pipeline_slug=customer-sync"`)
	require.Contains(t, string(body), "Canonical pipeline catalog")
	require.Equal(t, "HX-Request, HX-Target, HX-History-Restore-Request", resp.Header.Get("Vary"))
	require.Contains(t, string(body), "Select a pipeline")
	require.Contains(
		t,
		string(body),
		`href="/orgs/acme-hq/projects/default/pipelines?cursor=page-1&amp;direction=prev"`,
	)
	require.Contains(
		t,
		string(body),
		`href="/orgs/acme-hq/projects/default/pipelines?cursor=page-3&amp;direction=next"`,
	)
}

func TestRouter_HandleProjectPipelinesPage_PreservesProjectContext(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write([]byte(
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write([]byte(
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/data-migration/pipelines":
			assert.Equal(t, "20", r.URL.Query().Get("limit"))
			assert.Equal(t, "page-2", r.URL.Query().Get("cursor"))
			assert.Equal(t, "prev", r.URL.Query().Get("direction"))
			_, _ = w.Write(
				[]byte(
					`{"pipelines":[{"slug":"customer-sync","name":"Customer Sync","sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":true,"next_cursor":"page-3","prev_cursor":"page-1"}`,
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
		"/orgs/acme-hq/projects/data-migration/pipelines?cursor=page-2&direction=prev",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Data migration")
	require.NotContains(t, string(body), `id="project-switcher"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/data-migration/pipelines?cursor=page-2&amp;direction=prev&amp;pipeline_slug=customer-sync"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/data-migration/runs"`)
	require.Contains(
		t,
		string(body),
		`href="/orgs/acme-hq/projects/data-migration/pipelines?cursor=page-1&amp;direction=prev"`,
	)
	require.Contains(
		t,
		string(body),
		`href="/orgs/acme-hq/projects/data-migration/pipelines?cursor=page-3&amp;direction=next"`,
	)
}

func TestRouter_HandleProjectPipelinesPage_PreservesEnvironmentInRunLinksAndShowsDeploymentLookupFailure(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write([]byte(
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write([]byte(
				`{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/data-migration/environments":
			_, _ = w.Write([]byte(
				`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":null,"is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"},{"environment_slug":"prod","environment_name":"Production","environment_description":null,"is_default":false,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/data-migration/pipelines":
			_, _ = w.Write([]byte(
				`{"pipelines":[{"slug":"customer-sync","name":"Customer Sync","sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":true,"next_cursor":"pipeline-page-2","prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/data-migration/pipelines/customer-sync":
			_, _ = w.Write([]byte(
				`{"slug":"customer-sync","name":"Customer Sync","sync_status":"synced","has_draft":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_spec_hash":"spec-hash-12","last_applied_commit":"abc123","source_path":"pipelines/test.pipeline.yaml","source_ref":"refs/heads/main","draft":null,"spec":{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[]}}`,
			))
		case "/api/organizations/acme-hq/projects/data-migration/environments/dev/pipeline-deployments":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"deployment metadata unavailable"}}`))
		case "/api/organizations/acme-hq/projects/data-migration/environments/prod/runs":
			assert.Equal(t, "1", r.URL.Query().Get("limit"))
			assert.Equal(t, "customer-sync", r.URL.Query().Get("pipeline_slug"))
			assert.Equal(t, "run_created_at", r.URL.Query().Get("sort_by"))
			assert.Equal(t, "desc", r.URL.Query().Get("order"))
			_, _ = w.Write([]byte(
				`{"runs":[{"pipeline_slug":"customer-sync","environment_slug":"prod","run_slug":"run-prod-001","status":"succeeded","duration_ms":1200,"step_count":2,"step_slug":"load-customers","started_at":"2026-03-04T00:00:00Z","ended_at":"2026-03-04T00:00:01Z","created_at":"2026-03-04T00:00:01Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
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
		"/orgs/acme-hq/projects/data-migration/pipelines?environment_slug=prod&pipeline_slug=customer-sync",
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
	require.Contains(t, html, "deployment metadata unavailable")
	require.Contains(t, html, "deployments unavailable")
	require.Contains(t, html, "Runtime target")
	require.Contains(t, html, "data-migration / prod")
	require.Contains(t, html, "run-prod-001")
	require.Contains(t, html, "succeeded")
	require.NotContains(t, html, ">not deployed<")
	require.NotContains(t, html, "Empty builder")
	require.NotContains(t, html, "draft_slug=draft-pipeline")
	require.Contains(t, html, `action="/orgs/acme-hq/projects/data-migration/pipelines?environment_slug=prod"`)
	require.Contains(t, html, `name="environment_slug" value="prod"`)
	require.Contains(t, html, `href="/orgs/acme-hq/projects/data-migration/pipelines/customer-sync?environment_slug=prod&amp;list_return=`)
	require.Contains(t, html, "Open full page")
	require.NotContains(t, html, "Deploy version")
	require.Contains(t, html, `href="/orgs/acme-hq/projects/data-migration/runs?environment_slug=prod"`)
	require.Contains(t, html, `href="/organizations/acme-hq/environments?environment_slug=prod"`)
	require.Contains(t, html, `href="/orgs/acme-hq/projects/data-migration/runs/run-prod-001?environment_slug=prod"`)
	require.Contains(t, html, `href="/orgs/acme-hq/projects/data-migration/pipelines?cursor=pipeline-page-2&amp;direction=next&amp;environment_slug=prod"`)
	require.Contains(t, html, `id="pipeline-detail-panel-slot"`)
	require.Contains(t, html, `hx-target="#pipeline-detail-panel-slot"`)
	require.Contains(t, html, `hx-select="#pipeline-detail-panel-slot"`)
}

func TestRouter_ProjectOwnedGetPagesRequireProjectRoute(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	setActiveOrganizationSlugInSession(t, app, authCookie, "acme-hq")
	for _, path := range []string{
		"/orgs/acme-hq/pipelines",
		"/orgs/acme-hq/pipelines/customer-sync",
		"/orgs/acme-hq/runs",
		"/orgs/acme-hq/runs/run-001",
	} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)

		resp, err := app.Test(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, resp.StatusCode, path)
	}
}

func TestRouter_HandlePipelinesPage_UnauthorizedRedirectsToLogin(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"auth required"}}`))
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/default/pipelines", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "/auth/login")
}

func TestRouter_HandlePipelineDetailPage_RendersMetadata(t *testing.T) {
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
		case "/api/organizations/acme-hq/projects/default/pipelines/customer-sync":
			_, _ = w.Write(
				[]byte(
					`{"slug":"customer-sync","name":"Customer Sync","sync_status":"synced","has_draft":true,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_spec_hash":"spec-hash-12","last_applied_commit":"abc123","source_path":"pipelines/test.pipeline.yaml","source_ref":"refs/heads/main","draft":{"pipeline_slug":"customer-sync","created_at":"2026-03-02T01:00:00Z","updated_at":"2026-03-02T01:00:00Z","spec":{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[]}},"spec":{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[{"kind":"query","slug":"load-customers","name":"Load customers","query":{"data_source_ref":"prod-db","operation_type":"postgres","operation_ref":"top-users","output_contract":{"name":"source-customers","representation":"rows","fields":[{"name":"id","type":"string","required":true},{"name":"email","type":"string","required":true,"validations":{"format":"email","min_length":1}},{"name":"status","type":"string","required":false,"validations":{"enum":["active","pending"]}}]}}},{"kind":"transform","slug":"normalize-customers","name":"Normalize customers","transform":{"inputs":{"input":"load-customers"},"sql":"SELECT id, email, status FROM input","output_kind":"rows","input_contract":{"name":"transform-input","representation":"rows","fields":[{"name":"id","type":"string","required":true},{"name":"email","type":"string","required":true}]},"output_contract":{"name":"normalized-customers","representation":"rows","fields":[{"name":"id","type":"string","required":true},{"name":"email","type":"string","required":true},{"name":"status","type":"string","required":false}]}}},{"kind":"sink","slug":"warehouse-load","name":"Warehouse load","sink":{"input":"normalize-customers","input_contract":{"name":"warehouse-input","representation":"rows","fields":[{"name":"id","type":"string","required":true},{"name":"email","type":"string","required":true}]},"data_source_ref":"warehouse-db","operation_type":"postgres","operation_ref":"upsert-customers","idempotency_class":"idempotent_with_key"}}]}}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments":
			_, _ = w.Write([]byte(
				`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":null,"is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"},{"environment_slug":"prod","environment_name":"Production","environment_description":null,"is_default":false,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/default/data-source-refs":
			_, _ = w.Write([]byte(
				`{"data_source_refs":[{"pipeline_slug":"customer-sync","name":"Production DB","slug":"prod-db","data_source_type":"postgres","required_capabilities":["pipeline_source"],"created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-03T00:00:00Z"},{"pipeline_slug":"customer-sync","name":"Warehouse DB","slug":"warehouse-db","data_source_type":"postgres","required_capabilities":["pipeline_sink"],"created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-03T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write([]byte(
				`{"data_sources":[{"name":"Production DB","slug":"prod-db","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"prod"},"is_active":true,"read_only":false,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Warehouse DB","slug":"warehouse-db","type":"postgres","config":{"host":"warehouse.example.com","port":5432,"database":"warehouse"},"is_active":true,"read_only":false,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/default/environments/dev/data-source-bindings":
			_, _ = w.Write([]byte(
				`{"data_source_bindings":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/default/environments/prod/data-source-bindings":
			_, _ = w.Write([]byte(
				`{"data_source_bindings":[{"pipeline_slug":"customer-sync","environment_slug":"prod","data_source_ref_slug":"prod-db","data_source_ref_name":"Production DB","data_source_slug":"prod-db","data_source_name":"Production DB","data_source_type":"postgres","data_source_active":true,"data_source_read_only":false,"bound_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-03T00:00:00Z"},{"pipeline_slug":"customer-sync","environment_slug":"prod","data_source_ref_slug":"warehouse-db","data_source_ref_name":"Warehouse DB","data_source_slug":"warehouse-db","data_source_name":"Warehouse DB","data_source_type":"postgres","data_source_active":true,"data_source_read_only":false,"bound_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-03T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/default/environments/dev/pipeline-deployments",
			"/api/organizations/acme-hq/projects/default/environments/prod/pipeline-deployments":
			_, _ = w.Write([]byte(`{"deployments":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
		case "/api/organizations/acme-hq/projects/default/environments/prod/runs":
			assert.Equal(t, "10", r.URL.Query().Get("limit"))
			assert.Equal(t, "customer-sync", r.URL.Query().Get("pipeline_slug"))
			_, _ = w.Write([]byte(
				`{"runs":[{"pipeline_slug":"customer-sync","environment_slug":"prod","run_slug":"run-prod-001","status":"failed","duration_ms":1290,"step_count":1,"step_slug":"load-customers","started_at":"2026-03-04T00:00:00Z","ended_at":"2026-03-04T00:00:01Z","created_at":"2026-03-04T00:00:01Z","error_summary":"warehouse timeout","failure_kind":"data_source"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/default/environments/prod/runs/run-prod-001":
			_, _ = w.Write([]byte(`{"metadata":{"pipeline_slug":"customer-sync","environment_slug":"prod","run_slug":"run-prod-001","status":"failed","duration_ms":1290,"started_at":"2026-03-04T00:00:00Z","error_summary":"warehouse timeout"},"timeline":{}}`))
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write([]byte(
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/default/pipelines/customer-sync?environment_slug=prod", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Contains(t, string(body), `id="pipeline-overview"`)
	require.Contains(t, string(body), "Load customers")
	require.Contains(t, string(body), "warehouse timeout")
	require.NotContains(t, string(body), "SELECT id, email, status FROM input")
	require.NotContains(t, string(body), "Canonical definition and provenance")
	require.NotContains(t, string(body), "Pipeline data-source requirements")
	allSections := string(body)
	for _, section := range []string{"workflow", "runs", "deployments", "git"} {
		sectionReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/default/pipelines/customer-sync?environment_slug=prod&section="+section, nil)
		sectionReq.Header.Set("Cookie", authCookie)
		sectionResp, sectionErr := app.Test(sectionReq)
		require.NoError(t, sectionErr)
		require.Equal(t, http.StatusOK, sectionResp.StatusCode)
		sectionBody, readErr := io.ReadAll(sectionResp.Body)
		require.NoError(t, readErr)
		require.NoError(t, sectionResp.Body.Close())
		require.Contains(t, string(sectionBody), `id="pipeline-`+section+`"`)
		allSections += string(sectionBody)
	}
	body = []byte(allSections)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Canonical definition and provenance")
	require.Contains(t, string(body), "Inspect definition")
	require.Contains(t, string(body), "Structured")
	require.Contains(t, string(body), "Serialized")
	require.Contains(t, string(body), "original spec order")
	require.Contains(t, string(body), "Customer Sync")
	require.Contains(t, string(body), "Load customers")
	require.Contains(t, string(body), "Normalize customers")
	require.Contains(t, string(body), "Warehouse load")
	require.Contains(t, string(body), "input &lt;- load-customers")
	require.Contains(t, string(body), "payload &lt;- normalize-customers")
	require.Contains(t, string(body), "SELECT id, email, status FROM input")
	require.Contains(t, string(body), "source-customers")
	require.Contains(t, string(body), "transform-input")
	require.Contains(t, string(body), "normalized-customers")
	require.Contains(t, string(body), "warehouse-input")
	require.Contains(t, string(body), "format email")
	require.Contains(t, string(body), "enum active, pending")
	require.Contains(t, string(body), "run-prod-001")
	require.Contains(t, string(body), "warehouse timeout")
	require.NotContains(t, string(body), "Propose metadata change")
	require.NotContains(t, string(body), `/pipelines/customer-sync/update`)
	require.Contains(t, string(body), `hx-target="#main-content"`)
	require.Contains(t, string(body), `hx-swap="outerHTML show:none focus-scroll:false"`)
	require.Contains(t, string(body), `data-preserve-scroll`)
	require.Contains(t, string(body), "Pipeline data-source requirements")
	require.Contains(t, string(body), "Production DB")
	require.Contains(t, string(body), "Warehouse DB")
	require.Contains(t, string(body), "prod-db")
	require.Contains(t, string(body), "warehouse-db")
	require.Contains(t, string(body), "bound")
	require.Contains(t, string(body), "missing")
	require.Contains(t, string(body), `pipelines/customer-sync/environments/prod/data-source-bindings/prod-db/upsert?environment_slug=prod`)
	require.Contains(t, string(body), `pipelines/customer-sync/environments/prod/data-source-bindings/prod-db/delete?environment_slug=prod`)
	require.NotContains(t, string(body), "Pipeline-scoped requirements are not available on this overview yet.")
	require.Contains(t, string(body), "Deployment pointers identify environment and version")
	require.Contains(t, string(body), "Target deployment")
	require.Contains(
		t,
		string(body),
		`href="/builder?org_slug=acme-hq&amp;pipeline_slug=customer-sync&amp;project_slug=default&amp;target_environment_slug=prod"`,
	)
	require.Contains(t, string(body), "Open Builder in prod")
	require.Contains(t, string(body), "View runs in prod")
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/runs?environment_slug=prod&amp;pipeline_slug=customer-sync"`)
	require.Contains(t, string(body), `name="environment_slug" value="prod"`)
	require.Contains(t, string(body), `action="/orgs/acme-hq/projects/default/pipelines/customer-sync/draft/delete?environment_slug=prod&amp;section=git"`)
	require.Contains(t, string(body), `hx-confirm="Discard draft for pipeline &#34;Customer Sync (customer-sync)&#34;? The saved draft changes will be lost."`)
	require.Contains(t, string(body), `data-destructive-confirm="Discard draft for pipeline &#34;Customer Sync (customer-sync)&#34;? The saved draft changes will be lost."`)
	require.NotContains(t, string(body), `/pipelines/customer-sync/delete`)
	require.NotContains(t, string(body), "Run and builder navigation")
}

func TestRouter_HandlePipelineDeploymentDetailPage_RendersPointerMetadata(t *testing.T) {
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
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write([]byte(
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/default/environments":
			_, _ = w.Write([]byte(
				`{"project_environments":[{"environment_slug":"prod","environment_name":"Production","environment_description":null,"is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/default/pipelines/customer-sync":
			_, _ = w.Write(
				[]byte(
					`{"slug":"customer-sync","name":"Customer Sync","sync_status":"synced","has_draft":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_spec_hash":"spec-hash-12","last_applied_commit":"abc123","source_path":"pipelines/test.pipeline.yaml","source_ref":"refs/heads/main","draft":null,"spec":{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[{"kind":"query","slug":"load-customers","query":{"data_source_ref":"prod-db","operation_type":"postgres","operation_ref":"top-users"}}]}}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments/prod/pipelines/customer-sync/deployment":
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = w.Write(
				[]byte(
					`{"pipeline_slug":"customer-sync","project_slug":"default","environment_slug":"prod","current_version_spec_hash":"0123456789abcdef0123456789abcdef","current_version_short_spec_hash":"0123456789ab","previous_version_spec_hash":"fedcba9876543210fedcba9876543210","previous_version_short_spec_hash":"fedcba987654","deployed_at":"2026-03-02T03:04:05Z","updated_at":"2026-03-02T03:05:06Z","deployed_by_actor_type":"user","deployed_by_actor_slug":"ada-admin","proof_link":{"kind":"deployment","receipt_id":"proof-123","identifier":"customer-sync:012345","linked_at":"2026-03-02T03:04:05Z","retain_until":"2026-06-01T00:00:00Z","status":"matched"}}`,
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
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/projects/default/pipelines/customer-sync/deployment?environment_slug=prod",
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
	require.Contains(t, html, "Pipeline deployment")
	require.Contains(t, html, "Proof receipt")
	require.Contains(t, html, "/proof-receipts/proof-123")
	require.Contains(t, html, "Customer Sync")
	require.Contains(t, html, "Runtime target")
	require.Contains(t, html, "prod")
	require.Contains(t, html, "customer-sync")
	require.Contains(t, html, "0123456789ab")
	require.Contains(t, html, "0123456789abcdef0123456789abcdef")
	require.Contains(t, html, "fedcba987654")
	require.Contains(t, html, "Deployed")
	require.Contains(t, html, "deployed pointer")
	require.Contains(t, html, "2026-03-02")
	require.Contains(t, html, "ada-admin")
	require.Contains(t, html, `href="/orgs/acme-hq/projects/default/pipelines/customer-sync?environment_slug=prod"`)
	require.Contains(t, html, `href="/orgs/acme-hq/projects/default/runs?environment_slug=prod&amp;pipeline_slug=customer-sync"`)
	require.NotContains(t, html, "run-prod-001")
}

func TestRouter_HandlePipelineDeploymentDetailPage_NotFoundRendersUnavailableState(t *testing.T) {
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
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write([]byte(
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/default/environments":
			_, _ = w.Write([]byte(
				`{"project_environments":[{"environment_slug":"prod","environment_name":"Production","environment_description":null,"is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/projects/default/pipelines/customer-sync":
			_, _ = w.Write(
				[]byte(
					`{"slug":"customer-sync","name":"Customer Sync","sync_status":"synced","has_draft":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_spec_hash":"spec-hash-12","last_applied_commit":"abc123","source_path":"pipelines/test.pipeline.yaml","source_ref":"refs/heads/main","draft":null,"spec":{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[]}}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments/prod/pipelines/customer-sync/deployment":
			assert.Equal(t, http.MethodGet, r.Method)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"deployment pointer not found"}}`))
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
		"/orgs/acme-hq/projects/default/pipelines/customer-sync/deployment?environment_slug=prod",
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

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, html, "Deployment details are unavailable")
	require.Contains(t, html, "deployment pointer not found")
	require.Contains(t, html, "customer-sync")
	require.Contains(t, html, "prod")
	require.Contains(t, html, "not deployed")
	require.Contains(t, html, `href="/orgs/acme-hq/projects/default/pipelines/customer-sync?environment_slug=prod"`)
	require.Contains(t, html, `href="/orgs/acme-hq/projects/default/runs?environment_slug=prod&amp;pipeline_slug=customer-sync"`)
}

func TestRouter_HandlePipelineDetailPage_HidesDiscardDraftWithoutUpdatePermission(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"viewer","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write([]byte(
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
		case "/api/organizations/acme-hq/roles/viewer":
			_, _ = w.Write(
				[]byte(
					`{"slug":"viewer","name":"Viewer","permissions":[{"resource":"pipelines","action":"read"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/pipelines/customer-sync":
			_, _ = w.Write(
				[]byte(
					`{"slug":"customer-sync","name":"Customer Sync","sync_status":"synced","has_draft":true,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_spec_hash":"spec-hash-12","last_applied_commit":"abc123","source_path":"pipelines/test.pipeline.yaml","source_ref":"refs/heads/main","draft":{"pipeline_slug":"customer-sync","created_at":"2026-03-02T01:00:00Z","updated_at":"2026-03-02T01:00:00Z","spec":{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[]}},"spec":{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[]}}`,
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/default/pipelines/customer-sync", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, string(body), "Discard draft")
	require.NotContains(t, string(body), "/draft/delete")
}

func TestRouter_HandlePipelineDetailPage_NotFoundRendersUnavailableState(t *testing.T) {
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
		case "/api/organizations/acme-hq/projects/default/pipelines/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"pipeline not found"}}`))
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write([]byte(
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			))
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/default/pipelines/missing", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, string(body), "Pipeline details are unavailable")
	require.Contains(t, string(body), "pipeline not found")
	require.Contains(t, string(body), "missing")
}
