package frontend

import (
	"context"
	"encoding/json"
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

func TestRouter_HandlePipelineRunsPage_RendersList(t *testing.T) {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/prod/runs":
			writeJSON(
				w,
				`{"runs":[{"pipeline_slug":"acme-users-sync","pipeline_version_spec_hash":"6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8","run_slug":"run-001","source":{"type":"manual","summary":"Manual run","entrypoint":"ui","actor_type":"user","actor_slug":"alice","attempt_number":1,"root_run_slug":"run-001","retry_of_run_slug":null,"request_id":"req-123","trace_id":null,"parameter_keys":["run_slug"],"source_event_slug":"evt-pipeline-run-execute-001"},"status":"failed","duration_ms":1290,"step_count":2,"step_slug":"transform-users","incremental":{"mode":"incremental","checkpoint_status":"blocked","replay_safety_status":"blocked","source_step_slug":"fetch-users","blocked_reason":"sink checkpoint blocked","replay_blocked_reason":"","backfill_series_slug":"","backfill_chunk_index":0,"backfill_chunk_count":0},"started_at":"2026-03-16T11:00:00Z","ended_at":"2026-03-16T11:00:01Z","created_at":"2026-03-16T11:00:01Z","error_summary":"upstream validation failed","failure_kind":"data_source","request_id":"req-123"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/prod/runs/run-001":
			writeJSON(
				w,
				`{"metadata":{"pipeline_slug":"acme-users-sync","pipeline_version_spec_hash":"6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8","run_slug":"run-001","source":{"type":"manual","summary":"Manual run","entrypoint":"ui","actor_type":"user","actor_slug":"alice","attempt_number":1,"root_run_slug":"run-001","retry_of_run_slug":null,"request_id":"req-123","trace_id":null,"parameter_keys":["run_slug"],"source_event_slug":"evt-pipeline-run-execute-001"},"status":"failed","duration_ms":1290,"step_count":2,"step_slug":"transform-users","incremental":{"mode":"incremental","checkpoint_status":"blocked","replay_safety_status":"blocked","source_step_slug":"fetch-users","blocked_reason":"sink checkpoint blocked","replay_blocked_reason":"","backfill_series_slug":"","backfill_chunk_index":0,"backfill_chunk_count":0},"started_at":"2026-03-16T11:00:00Z","ended_at":"2026-03-16T11:00:01Z","created_at":"2026-03-16T11:00:01Z","error_summary":"upstream validation failed","failure_kind":"data_source","request_id":"req-123"},"timeline":{"run_slug":"run-001","pipeline_slug":"acme-users-sync","status":"failed","events":[],"snapshots":[],"failure":{"step":"transform-users","error":"upstream validation failed","at":"2026-03-16T11:00:01Z","kind":"data_source","side":"source","data_source_slug":"source-postgres"}},"incremental_diagnostics":{"replay_safety":{"status":"blocked","blocked_reason":"sink checkpoint blocked","blocked_sink":"warehouse-postgres"},"sources":[]}}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/prod/runs/run-001/trigger-retry-attempts":
			writeJSON(
				w,
				`{"retry_attempts":[]}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/pipelines/acme-users-sync/versions/6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8":
			writeJSON(
				w,
				`{"spec_hash":"6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8","short_spec_hash":"6d7b6f5f","pipeline_slug":"acme-users-sync","created_at":"2026-03-16T10:00:00Z","spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Fetch users","query":{"data_source_ref":"source-postgres","operation_type":"sql","operation_ref":"list-users"}},{"kind":"transform","slug":"transform-users","name":"Transform users","transform":{"sql":"SELECT * FROM users","output_kind":"rows","inputs":{"users":"fetch-users"}}}]}}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/data-migration/runs?environment_slug=prod", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Pipeline runs")
	require.Contains(t, string(body), "Runtime target")
	require.Contains(t, string(body), "visible runs in target prod")
	require.Contains(t, string(body), "run-001")
	require.Contains(t, string(body), "acme-users-sync")
	require.Contains(t, string(body), "upstream validation failed")
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/data-migration/runs?environment_slug=prod"`)
	require.Contains(t, string(body), "Open full page")
	require.Contains(t, string(body), "list_return=")
	require.NotContains(t, string(body), "Trigger retry attempts")
	require.NotContains(t, string(body), "Run lineage")
	require.NotContains(t, string(body), "Execution timeline")
}

func TestAppendRunsEnvironmentQueryReplacesExistingEnvironmentSlug(t *testing.T) {
	t.Parallel()

	href := appendRunsEnvironmentQuery("/orgs/acme-hq/projects/data-migration/runs?environment_slug=dev&pipeline_slug=users", "prod")

	require.Equal(t, "/orgs/acme-hq/projects/data-migration/runs?environment_slug=prod&pipeline_slug=users", href)
	require.NotContains(t, href, "environment_slug=dev&environment_slug=prod")
}

func TestRequestRuntimeEnvironmentSlugUsesPathCanonicalParameter(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/builder", func(c fiber.Ctx) error {
		return c.SendString(requestRuntimeEnvironmentSlug(c))
	})
	app.Get("/orgs/:slug/projects/:project_slug/runs", func(c fiber.Ctx) error {
		return c.SendString(requestRuntimeEnvironmentSlug(c))
	})

	builderReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/builder?environment_slug=dev&target_environment_slug=prod",
		nil,
	)
	builderResp, err := app.Test(builderReq)
	require.NoError(t, err)
	defer func() { _ = builderResp.Body.Close() }()

	builderBody, err := io.ReadAll(builderResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, builderResp.StatusCode)
	require.Equal(t, "prod", string(builderBody))

	runsReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/projects/data-migration/runs?environment_slug=prod&target_environment_slug=dev",
		nil,
	)
	runsResp, err := app.Test(runsReq)
	require.NoError(t, err)
	defer func() { _ = runsResp.Body.Close() }()

	runsBody, err := io.ReadAll(runsResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, runsResp.StatusCode)
	require.Equal(t, "prod", string(runsBody))
}

func TestRouter_HandlePipelineRunsPage_UsesDefaultEnvironmentWhenEnvironmentSlugMissing(t *testing.T) {
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
				`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":"Local validation target","is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"},{"environment_slug":"prod","environment_name":"Production","environment_description":"Production target","is_default":false,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/dev/runs":
			writeJSON(w, `{"runs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/prod/runs":
			t.Errorf("env-less runs page should use default dev environment, not prod")
			w.WriteHeader(http.StatusInternalServerError)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/data-migration/runs", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Pipeline runs")
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/data-migration/runs?environment_slug=dev"`)
	require.Contains(t, string(body), `option selected value="/orgs/acme-hq/projects/data-migration/runs?environment_slug=dev"`)
	require.Contains(t, string(body), ">Target: Development</option>")
}

func TestRouter_HandlePipelineRunsPage_DefaultEnvironmentLookupFailureRendersErrorPage(t *testing.T) {
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
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/runs"):
			t.Errorf("runs endpoint should not be called when default environment lookup fails")
			w.WriteHeader(http.StatusInternalServerError)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/data-migration/runs", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Contains(t, html, "Pipeline runs")
	require.Contains(t, html, projectEnvironmentsLoadErrorMessage)
	require.NotEqual(t, "project environments failed", html)
}

func TestRouter_HandlePipelineRunsPage_RejectsMalformedEnvironmentSlug(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("malformed environment_slug should fail before backend calls, got %s", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
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
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/projects/data-migration/runs?environment_slug=not%20valid%21",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "slug")
}

func TestRouter_HandlePipelineRunsPage_RendersProjectDashboardTriageAffordances(t *testing.T) {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/prod/runs":
			if got := r.URL.Query().Get("status"); got != "failed" {
				t.Errorf("status query = %q, want failed", got)
			}
			writeJSON(
				w,
				`{"runs":[{"pipeline_slug":"acme-users-sync","pipeline_version_spec_hash":"6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8","run_slug":"run-002","source":{"type":"schedule","summary":"Scheduled retry","entrypoint":"scheduler","actor_type":"system","actor_slug":null,"attempt_number":2,"root_run_slug":"run-001","retry_of_run_slug":"run-001","request_id":"req-456","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","parameter_keys":["trigger_slug"],"source_event_slug":"evt-schedule-fire-001","schedule":{"trigger_slug":"nightly-users","trigger_revision":3,"cron":"0 2 * * *","timezone":"UTC","scheduled_fire_at":"2026-03-16T02:00:00Z","actual_fire_at":"2026-03-16T02:00:05Z","misfire_result":"on_time"}},"status":"failed","duration_ms":2500,"step_count":3,"step_slug":"write-users","incremental":{"mode":"incremental","checkpoint_status":"blocked","replay_safety_status":"blocked","source_step_slug":"fetch-users","blocked_reason":"duplicate key blocked checkpoint","replay_blocked_reason":"sink write is not replay safe","backfill_series_slug":"","backfill_chunk_index":0,"backfill_chunk_count":0},"started_at":"2026-03-16T11:00:00Z","ended_at":"2026-03-16T11:00:02Z","created_at":"2026-03-16T11:00:02Z","error_summary":"HubSpot request failed: query invalid input","failure_kind":"data_source","request_id":"req-456","replay_of_run_slug":"run-001","replay_from_step":"normalize-users"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/prod/runs/run-002":
			writeJSON(
				w,
				`{"metadata":{"pipeline_slug":"acme-users-sync","pipeline_version_spec_hash":"6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8","run_slug":"run-002","source":{"type":"schedule","summary":"Scheduled retry","entrypoint":"scheduler","actor_type":"system","actor_slug":null,"attempt_number":2,"root_run_slug":"run-001","retry_of_run_slug":"run-001","request_id":"req-456","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","parameter_keys":["trigger_slug"],"source_event_slug":"evt-schedule-fire-001","schedule":{"trigger_slug":"nightly-users","trigger_revision":3,"cron":"0 2 * * *","timezone":"UTC","scheduled_fire_at":"2026-03-16T02:00:00Z","actual_fire_at":"2026-03-16T02:00:05Z","misfire_result":"on_time"}},"status":"failed","duration_ms":2500,"step_count":3,"step_slug":"write-users","incremental":{"mode":"incremental","checkpoint_status":"blocked","replay_safety_status":"blocked","source_step_slug":"fetch-users","blocked_reason":"duplicate key blocked checkpoint","replay_blocked_reason":"sink write is not replay safe","backfill_series_slug":"","backfill_chunk_index":0,"backfill_chunk_count":0},"started_at":"2026-03-16T11:00:00Z","ended_at":"2026-03-16T11:00:02Z","created_at":"2026-03-16T11:00:02Z","error_summary":"HubSpot request failed: query invalid input","failure_kind":"data_source","request_id":"req-456","replay_of_run_slug":"run-001","replay_from_step":"normalize-users"},"timeline":{"run_slug":"run-002","pipeline_slug":"acme-users-sync","status":"failed","replay_of_run_slug":"run-001","events":[{"at":"2026-03-16T11:00:00Z","type":"run_started","status":"running","step":"fetch-users"},{"at":"2026-03-16T11:00:02Z","type":"step_failed","status":"failed","step":"write-users"}],"snapshots":[{"step":"write-users","input":{"organization_slug":"acme-hq","data_source_slug":"warehouse-postgres","operation_type":"sql","operation_ref":"upsert-users","uses_inline":false,"parameter_keys":["tenant_id"]},"output":{"result_type":"sql","vendor_type":"postgres","duration_ms":800,"operation_type":"upsert","details":{"kind":"rows","row_count":0,"column_names":["id","email"]}}}],"failure":{"step":"write-users","error":"HubSpot request failed: query invalid input","at":"2026-03-16T11:00:02Z","kind":"data_source","side":"sink","data_source_slug":"warehouse-postgres","action_hint":"Review the sink write and retry policy."}},"incremental_diagnostics":{"replay_safety":{"status":"blocked","blocked_reason":"sink write is not replay safe","blocked_sink":"warehouse-postgres"},"sources":[]}}`,
			)
		case strings.Contains(r.URL.Path, "/trigger-retry-attempts") || strings.Contains(r.URL.Path, "/versions/"):
			t.Errorf("preview loaded diagnostic API: %s", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
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
		t.Context(),
		http.MethodGet,
		"/orgs/acme-hq/projects/data-migration/runs?environment_slug=prod&status=failed",
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
	require.Contains(t, html, "Pipeline runs across this project")
	require.Contains(t, html, "Failed runs")
	require.Contains(t, html, "Pipeline")
	require.Contains(t, html, "Started")
	require.Contains(t, html, "HubSpot request failed: query invalid input")
	require.Contains(t, html, "Open full page")
	require.Contains(t, html, "list_return=")
	require.NotContains(t, html, "Trigger retry attempts")
	require.NotContains(t, html, "Run lineage")
	require.NotContains(t, html, "Execution timeline")
	require.NotContains(t, html, "Retry group")
	require.Contains(t, html, "Loading…")
}

func TestRouter_HandlePipelineRunsPage_RendersEmptyDashboardState(t *testing.T) {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/prod/runs":
			writeJSON(w, `{"runs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
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
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orgs/acme-hq/projects/data-migration/runs?environment_slug=prod", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "No project runs yet")
	require.Contains(t, html, "Runs will appear here after a pipeline executes in this runtime target.")
	require.Contains(t, html, `href="/orgs/acme-hq/projects/data-migration/pipelines?environment_slug=prod"`)
	require.Contains(t, html, "View pipelines in prod")
}

func TestRouter_HandlePipelineRunsPage_RendersFilteredEmptyDashboardState(t *testing.T) {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/data-migration/environments/prod/runs":
			if got := r.URL.Query().Get("status"); got != "failed" {
				t.Errorf("status query = %q, want failed", got)
			}
			if got := r.URL.Query().Get("pipeline_slug"); got != "missing-pipeline" {
				t.Errorf("pipeline_slug query = %q, want missing-pipeline", got)
			}
			writeJSON(w, `{"runs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
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
		t.Context(),
		http.MethodGet,
		"/orgs/acme-hq/projects/data-migration/runs?environment_slug=prod&status=failed&pipeline_slug=missing-pipeline",
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
	require.Contains(t, html, "No runs match these filters")
	require.Contains(t, html, "Adjust the status or pipeline filter to see other project runs.")
	require.Contains(t, html, `href="/orgs/acme-hq/projects/data-migration/runs?environment_slug=prod"`)
	require.NotContains(t, html, "No project runs yet")
}

func TestRouter_HandlePipelineRunDetailPage_RendersTimeline(t *testing.T) {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/runs/run-001":
			writeJSON(
				w,
				`{"metadata":{"pipeline_slug":"acme-users-sync","pipeline_version_spec_hash":"missing-version-hash","run_slug":"run-001","status":"failed","duration_ms":1290,"step_count":2,"step_slug":"transform-users","started_at":"2026-03-16T11:00:00Z","ended_at":"2026-03-16T11:00:01Z","created_at":"2026-03-16T11:00:01Z","error_summary":"upstream validation failed","request_id":"req-123"},"timeline":{"run_slug":"run-001","pipeline_slug":"acme-users-sync","status":"failed","events":[{"at":"2026-03-16T11:00:00Z","type":"run_started","status":"running","step":"fetch-users"},{"at":"2026-03-16T11:00:01Z","type":"step_failed","status":"failed","step":"transform-users"}],"snapshots":[],"failure":{"step":"transform-users","error":"contract mismatch","at":"2026-03-16T11:00:01Z"}}}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/runs/run-001/trigger-retry-attempts":
			writeJSON(w, `{"retry_attempts":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/default/runs/run-001?environment_slug=prod&section=steps", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Run details")
	require.Contains(t, string(body), "run-001")
	require.Contains(t, string(body), "Execution timeline")
	require.Contains(t, string(body), `id="run-outputs"`)
	require.Contains(t, string(body), "Step failed")
	require.Contains(t, string(body), "contract mismatch")
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/runs?environment_slug=prod"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/runs?environment_slug=prod">Runs</a>`)
	require.NotContains(t, string(body), "Trigger retry attempts")
	require.NotContains(t, string(body), `href="/builder?org_slug=acme-hq`)
}

func TestRouter_HandlePipelineRunDetailPage_RendersSinkOutputMetadata(t *testing.T) {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/runs/run-sink-output":
			writeJSON(
				w,
				`{"metadata":{"pipeline_slug":"acme-users-sync","run_slug":"run-sink-output","status":"succeeded","duration_ms":1290,"step_count":3,"step_slug":"write-users","started_at":"2026-03-16T11:00:00Z","ended_at":"2026-03-16T11:00:01Z","created_at":"2026-03-16T11:00:01Z","request_id":"req-123"},"timeline":{"run_slug":"run-sink-output","pipeline_slug":"acme-users-sync","status":"succeeded","events":[{"at":"2026-03-16T11:00:00Z","type":"run_started","status":"running","step":"fetch-users"},{"at":"2026-03-16T11:00:01Z","type":"step_succeeded","status":"succeeded","step":"write-users"}],"snapshots":[{"step":"write-users","kind":"sink","input":{"organization_slug":"acme-hq","environment_slug":"prod","data_source_slug":"archive-objects","operation_type":"s3","operation_ref":"write-users-json","uses_inline":false,"parameter_keys":["key","format"]},"output":{"result_type":"S3","vendor_type":"s3","duration_ms":240,"operation_type":"write","details":{"kind":"file_write","target_path":"exports/users/run-sink-output.json","written_rows":2,"file":{"filename":"users.json","mime_type":"application/json","size_bytes":128,"digest_algorithm":"sha256","digest":"abc123"}}}}]},"incremental_diagnostics":{"replay_safety":{"status":"safe","blocked_reason":"","blocked_sink":""},"sources":[]}}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/runs/run-sink-output/trigger-retry-attempts":
			writeJSON(w, `{"retry_attempts":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/default/runs/run-sink-output?environment_slug=prod&section=outputs", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Sink output metadata")
	require.Contains(t, html, "metadata only")
	require.Contains(t, html, "write-users")
	require.Contains(t, html, "archive-objects")
	require.Contains(t, html, "s3 / write-users-json")
	require.Contains(t, html, "Object key")
	require.Contains(t, html, "exports/users/run-sink-output.json")
	require.Contains(t, html, "users.json / application/json / 128 bytes")
	require.Contains(t, html, "Object body and credential values are not shown.")
	require.NotContains(t, html, "secret-access-key")
	require.NotContains(t, html, "raw object body")
}

func TestRouter_HandlePipelineRunDetailPage_RendersFailureTriageView(t *testing.T) {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/runs/run-002":
			writeJSON(
				w,
				`{"metadata":{"pipeline_slug":"acme-users-sync","pipeline_version_spec_hash":"6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8","run_slug":"run-002","source":{"type":"manual","summary":"Manual run","entrypoint":"ui","actor_type":"user","actor_slug":"alice","request_id":"req-456","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","parameter_keys":["run_slug","protected_environment_confirmation"],"source_event_slug":"evt-pipeline-run-execute-001"},"status":"failed","duration_ms":2500,"step_count":3,"step_slug":"write-users","started_at":"2026-03-16T11:00:00Z","ended_at":"2026-03-16T11:00:02Z","created_at":"2026-03-16T11:00:02Z","error_summary":"contract field validation failed","failure_kind":"platform","request_id":"req-456","replay_of_run_slug":"run-001","replay_from_step":"normalize-users"},"timeline":{"run_slug":"run-002","pipeline_slug":"acme-users-sync","status":"failed","replay_of_run_slug":"run-001","events":[{"at":"2026-03-16T11:00:00Z","type":"run_started","status":"running","step":"fetch-users"},{"at":"2026-03-16T11:00:02Z","type":"step_failed","status":"failed","step":"write-users"}],"snapshots":[{"step":"write-users","input":{"organization_slug":"acme-hq","data_source_slug":"warehouse-postgres","operation_type":"sql","operation_ref":"upsert-users","uses_inline":false,"parameter_keys":["tenant_id"]},"output":{"result_type":"sql","vendor_type":"postgres","duration_ms":800,"operation_type":"upsert","details":{"kind":"rows","row_count":0,"column_names":["id","email"]}}}],"failure":{"step":"write-users","error":"contract \"users_rows\" field \"email\" failed format validation (2 violation(s))","at":"2026-03-16T11:00:02Z","kind":"platform","side":"sink","reason_code":"sink_contract_mismatch","data_source_slug":"warehouse-postgres","action_hint":"Review the reported contract field and validation rule.","contract":{"contract_name":"users_rows","field_name":"email","rule":"format","violation_count":2,"reason_code":"contract_field_validation_failed"}}}}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync/versions/6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8":
			writeJSON(
				w,
				`{"spec_hash":"6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8","short_spec_hash":"6d7b6f5f","pipeline_slug":"acme-users-sync","created_at":"2026-03-16T10:00:00Z","spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Fetch users","query":{"data_source_ref":"source-postgres","operation_type":"sql","operation_ref":"list-users"}},{"kind":"transform","slug":"normalize-users","name":"Normalize users","transform":{"sql":"SELECT * FROM users","output_kind":"rows","inputs":{"users":"fetch-users"}}},{"kind":"sink","slug":"write-users","name":"Write users","sink":{"input":"normalize-users","data_source_ref":"warehouse-postgres","operation_type":"sql","operation_ref":"upsert-users","idempotency_class":"idempotent_with_key"}}]}}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
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
	var html string
	for _, section := range []string{"outcome", "steps", "outputs", "attempts", "metadata"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orgs/acme-hq/projects/default/runs/run-002?environment_slug=prod&section="+section, nil)
		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		resp, err := app.Test(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusOK, resp.StatusCode)
		html += string(body)
	}

	require.Contains(t, html, "Selected step evidence")
	require.Contains(t, html, "Run lineage")
	require.Contains(t, html, "Exact pipeline version topology")
	require.Contains(t, html, "Fetch users")
	require.Contains(t, html, "Normalize users")
	require.Contains(t, html, "Write users")
	require.Contains(t, html, ">Source<")
	require.Contains(t, html, ">Transform<")
	require.Contains(t, html, ">Sink<")
	require.Contains(t, html, "Planned step")
	require.Contains(t, html, "Observed run")
	require.Contains(t, html, "Destination ref")
	require.Contains(t, html, "warehouse-postgres")
	require.Contains(t, html, "tenant_id")
	require.Contains(t, html, "Raw payload values are not stored.")
	require.Contains(t, html, "No recorded execution")
	require.Contains(t, html, "6d7b6f5f")
	require.Contains(t, html, `lineage_step=write-users`)
	require.Contains(t, html, `aria-current="true"`)
	require.NotContains(t, html, `aria-current=""`)
	require.NotContains(t, html, "Execution topology unavailable")
	require.Contains(t, html, "Run source")
	require.Contains(t, html, "Manual run via ui")
	require.Contains(t, html, "user alice")
	require.Contains(t, html, "Request req-456")
	require.Contains(t, html, "Trace 4bf92f3577b34da6a3ce929d0e0e4736")
	require.Contains(t, html, "run_slug, protected_environment_confirmation")
	require.Contains(t, html, "write-users")
	require.Contains(t, html, "Data source")
	require.Contains(t, html, "warehouse-postgres")
	require.Contains(t, html, "Platform error")
	require.Contains(t, html, "sink_contract_mismatch")
	require.Contains(t, html, "Sink step")
	require.Contains(t, html, "Review the reported contract field and validation rule.")
	require.Contains(t, html, "Contract")
	require.Contains(t, html, "users_rows")
	require.Contains(t, html, "Field")
	require.Contains(t, html, "email")
	require.Contains(t, html, "format")
	require.Contains(t, html, "Violations")
	require.Contains(t, html, "2")
	require.Contains(t, html, "Technical diagnostic")
	require.Contains(t, html, "contract_field_validation_failed")
	require.Contains(t, html, "Operation")
	require.Contains(t, html, "sql / upsert-users")
	require.Contains(t, html, "Replay context")
	require.Contains(t, html, "Replay of run-001 from normalize-users")
	require.Contains(t, html, `href="/orgs/acme-hq/projects/default/pipelines/acme-users-sync?environment_slug=prod"`)
	require.NotContains(t, html, "Lineage unavailable")
}

func TestRouter_HandlePipelineRunDetailPage_NotFoundRendersUnavailable(t *testing.T) {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/runs/missing-run":
			w.WriteHeader(http.StatusNotFound)
			writeJSON(
				w,
				`{"error":{"code":"RUN_NOT_FOUND","message":"Pipeline run not found"}}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(
				w,
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/default/runs/missing-run?environment_slug=prod", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, string(body), "The full run details are unavailable.")
	require.Contains(t, string(body), "Pipeline run not found")
	require.Contains(t, string(body), "missing-run")
}

func TestHandleRecoverPipelineRunSubmit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		allowed bool
		refusal string
		status  int
		detail  string
	}{
		{"allowed", true, "", http.StatusOK, ""},
		{"execute denied", false, "", http.StatusForbidden, ""},
		{"pruned inputs", true, "Retained inputs were pruned", http.StatusConflict, ""},
		{"unsafe replay", true, "Sink delivery outcome is unknown and replay is unsafe", http.StatusConflict, ""},
		{"protected target", true, "Protected environment confirmation required", http.StatusForbidden, ""},
		{"metadata-only file", true, "unsupported replay input mode", http.StatusUnprocessableEntity, "Retained file bytes are unavailable. Reconcile provider effects before a new evaluation, which reads sources again."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed := tc.allowed
			calls := 0
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/organizations" {
					writeJSON(w, `{"organizations":[{"slug":"acme-hq","member_role":"admin"}]}`)
					return
				}
				if r.URL.Path == "/api/organizations/acme-hq/roles/admin" {
					action := "read"
					if allowed {
						action = "execute"
					}
					writeJSON(w, `{"slug":"admin","permissions":[{"resource":"pipelines","action":"`+action+`"}]}`)
					return
				}
				if r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/runs/original:recover" {
					calls++
					assert.Contains(t, r.Header.Get("Cookie"), "session_id")
					body, err := io.ReadAll(r.Body)
					assert.NoError(t, err)
					assert.JSONEq(t, `{"protected_environment_confirmation":"prod"}`, string(body))
					if tc.refusal != "" {
						w.WriteHeader(tc.status)
						assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "recovery_refused", "message": tc.refusal, "detailed_error": tc.detail}}))
						return
					}
					writeJSON(w, `{"metadata":{"run_slug":"recovered","status":"succeeded"},"timeline":{"events":[],"snapshots":[]}}`)
					return
				}
				http.NotFound(w, r)
			}))
			defer apiServer.Close()
			store := session.NewStore()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orgs/acme-hq/projects/default/runs/original/recover", strings.NewReader("environment_slug=prod&protected_environment_confirmation=prod"))
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			addCSRFToken(t, app, req)
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			response, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			switch {
			case tc.refusal != "":
				require.Equal(t, http.StatusSeeOther, response.StatusCode)
				location, err := url.Parse(response.Header.Get("Location"))
				require.NoError(t, err)
				require.Equal(t, "/orgs/acme-hq/projects/default/runs/original", location.Path)
				require.Equal(t, "prod", location.Query().Get("environment_slug"))
				require.Equal(t, "attempts", location.Query().Get("section"))
				message := tc.refusal
				if tc.detail != "" {
					message += ": " + tc.detail
				}
				require.Equal(t, message, location.Query().Get("recovery_error"))
				require.Equal(t, 1, calls)
			case allowed:
				require.Equal(t, http.StatusSeeOther, response.StatusCode)
				require.Equal(t, "/orgs/acme-hq/projects/default/runs/recovered?environment_slug=prod", response.Header.Get("Location"))
				require.Equal(t, 1, calls)
			default:
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				require.Zero(t, calls)
			}
		})
	}
}
