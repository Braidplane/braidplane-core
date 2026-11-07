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
	"github.com/stretchr/testify/require"
)

type pipelineSchedulesMockRoute struct {
	method string
	path   string
	body   string
	status int
}

func TestWebhookDeliveryIDPattern(t *testing.T) {
	t.Parallel()

	require.True(t, webhookDeliveryIDPattern.MatchString("delivery_01:retry-2.json"))
	require.False(t, webhookDeliveryIDPattern.MatchString("delivery id"))
	require.False(t, webhookDeliveryIDPattern.MatchString(strings.Repeat("x", 129)))
}

func TestRouter_HandlePipelineSchedulesPage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		routes     []pipelineSchedulesMockRoute
		assertHTML func(*testing.T, int, string)
	}{
		{
			name: "RendersDetailAndFireHistory",
			routes: []pipelineSchedulesMockRoute{
				{
					method: http.MethodGet,
					path:   "/api/organizations",
					body:   `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				},
				{
					method: http.MethodGet,
					path:   "/api/organizations/acme-hq/projects",
					body:   `{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				},
				{
					method: http.MethodGet,
					path:   "/api/organizations/acme-hq/projects/data-migration/environments",
					body:   `{"project_environments":[{"environment_slug":"prod","environment_name":"Production","environment_description":"Production target","is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				},
				{
					method: http.MethodGet,
					path:   "/api/organizations/acme-hq/projects/data-migration/environments/prod/pipeline-triggers",
					body:   `{"pipeline_triggers":[{"slug":"nightly-users","name":"Nightly Users","type":"schedule","enabled":true,"revision":3,"environment_slug":"prod","pipeline_slug":"acme-users-sync","version_policy":{"mode":"latest_deployed"},"runtime_input_keys":["tenant_id","dry_run"],"retry_policy":{"enabled":true,"max_attempts":3,"backoff_strategy":"exponential","initial_delay_seconds":60,"max_delay_seconds":3600,"jitter":true,"retryable_failure_kinds":["data_source"],"max_elapsed_seconds":86400},"schedule":{"cron":"0 2 * * *","timezone":"UTC","misfire_policy":"skip"},"created_by_actor_type":"user","created_by_actor_slug":"alice","updated_by_actor_type":"user","updated_by_actor_slug":"alice","disabled_at":null,"created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				},
				{
					method: http.MethodGet,
					path:   "/api/organizations/acme-hq/projects/data-migration/environments/prod/pipeline-triggers/nightly-users",
					body:   `{"slug":"nightly-users","name":"Nightly Users","type":"schedule","enabled":true,"revision":3,"environment_slug":"prod","pipeline_slug":"acme-users-sync","version_policy":{"mode":"latest_deployed"},"runtime_input_keys":["tenant_id","dry_run"],"retry_policy":{"enabled":true,"max_attempts":3,"backoff_strategy":"exponential","initial_delay_seconds":60,"max_delay_seconds":3600,"jitter":true,"retryable_failure_kinds":["data_source"],"max_elapsed_seconds":86400},"schedule":{"cron":"0 2 * * *","timezone":"UTC","misfire_policy":"skip"},"created_by_actor_type":"user","created_by_actor_slug":"alice","updated_by_actor_type":"user","updated_by_actor_slug":"alice","disabled_at":null,"created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}`,
				},
				{
					method: http.MethodGet,
					path:   "/api/organizations/acme-hq/projects/data-migration/environments/prod/pipeline-triggers/nightly-users/fires",
					body:   `{"schedule_fires":[{"fire_slug":"fire-nightly-users-20260316t020000z","trigger_slug":"nightly-users","trigger_revision":3,"pipeline_slug":"acme-users-sync","scheduled_fire_at":"2026-03-16T02:00:00Z","actual_fire_at":"2026-03-16T02:00:05Z","status":"succeeded","misfire_result":"on_time","run_slug":"run-001","source_event_slug":"evt-schedule-001","error_summary":null,"created_at":"2026-03-16T02:00:00Z","updated_at":"2026-03-16T02:00:05Z"},{"fire_slug":"fire-nightly-users-20260317t020000z","trigger_slug":"nightly-users","trigger_revision":3,"pipeline_slug":"acme-users-sync","scheduled_fire_at":"2026-03-17T02:00:00Z","actual_fire_at":"2026-03-17T02:00:05Z","status":"failed","misfire_result":"fire_once","run_slug":null,"source_event_slug":"evt-schedule-002","error_summary":"connector timeout","created_at":"2026-03-17T02:00:00Z","updated_at":"2026-03-17T02:00:05Z"},{"fire_slug":"fire-nightly-users-20260318t020000z","trigger_slug":"nightly-users","trigger_revision":3,"pipeline_slug":"acme-users-sync","scheduled_fire_at":"2026-03-18T02:00:00Z","actual_fire_at":"2026-03-18T02:10:00Z","status":"skipped","misfire_result":"skipped","run_slug":null,"source_event_slug":null,"error_summary":null,"created_at":"2026-03-18T02:10:00Z","updated_at":"2026-03-18T02:10:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				},
			},
			assertHTML: func(t *testing.T, statusCode int, html string) {
				t.Helper()

				require.Equal(t, http.StatusOK, statusCode)
				require.Contains(t, html, "Pipeline schedules")
				require.Contains(t, html, "Nightly Users")
				require.Contains(t, html, "0 2 * * *")
				require.Contains(t, html, "Open full page")
				require.Contains(t, html, "data-entity-preview")
				require.NotContains(t, html, "tenant_id, dry_run")
				require.NotContains(t, html, "Scheduled-fire history")
			},
		},
		{
			name: "RendersEmptyState",
			routes: []pipelineSchedulesMockRoute{
				{
					method: http.MethodGet,
					path:   "/api/organizations",
					body:   `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				},
				{
					method: http.MethodGet,
					path:   "/api/organizations/acme-hq/projects",
					body:   `{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				},
				{
					method: http.MethodGet,
					path:   "/api/organizations/acme-hq/projects/data-migration/environments/prod/pipeline-triggers",
					body:   `{"pipeline_triggers":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				},
			},
			assertHTML: func(t *testing.T, statusCode int, html string) {
				t.Helper()

				require.Equal(t, http.StatusOK, statusCode)
				require.Contains(t, html, "No pipeline triggers yet")
				require.Contains(t, html, `href="/orgs/acme-hq/projects/data-migration/pipelines?environment_slug=prod"`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, authCookie := newPipelineSchedulesTestApp(t, tt.routes)
			statusCode, html := requestPipelineSchedulesPage(t, app, authCookie)
			tt.assertHTML(t, statusCode, html)
		})
	}
}

// newPipelineSchedulesTestApp builds a frontend router backed by per-test API fixtures.
func newPipelineSchedulesTestApp(t *testing.T, routes []pipelineSchedulesMockRoute) (*fiber.App, string) {
	t.Helper()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		for _, route := range routes {
			if r.Method == route.method && r.URL.Path == route.path {
				if route.status != 0 {
					w.WriteHeader(route.status)
				}
				writeJSON(w, route.body)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
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

// requestPipelineSchedulesPage renders the schedule page through the authenticated test app.
func requestPipelineSchedulesPage(t *testing.T, app *fiber.App, authCookie string) (int, string) {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/projects/data-migration/schedules?environment_slug=prod", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func TestTriggerDetailRoutePreservesFailuresAndSeparatesLoading(t *testing.T) {
	t.Parallel()
	base := "/api/organizations/acme-hq/projects/data-migration/environments/prod/pipeline-triggers/nightly"
	for _, tc := range []struct {
		name, section string
		status        int
		want          string
	}{
		{"configuration", "configuration", 200, "Version policy"},
		{"history", "history", 200, "Scheduled-fire history"},
		{"forbidden", "configuration", 403, "Access denied"},
		{"missing", "configuration", 404, "Access denied"},
		{"expired", "configuration", 401, ""},
		{"defaultEnvironment", "configuration", 500, projectEnvironmentsLoadErrorMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"slug":"nightly","name":"Nightly","type":"schedule","enabled":false,"pipeline_slug":"sync","schedule":{"cron":"0 2 * * *","timezone":"UTC","misfire_policy":"skip"}}`
			if tc.status != 200 {
				body = `{"error":{"code":"FORBIDDEN","message":"Access denied"}}`
			}
			app, cookie := newPipelineSchedulesTestApp(t, []pipelineSchedulesMockRoute{
				{method: http.MethodGet, path: "/api/organizations", body: `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin"}],"has_more":false}`},
				{method: http.MethodGet, path: "/api/organizations/acme-hq/projects", body: `{"projects":[{"name":"Data migration","slug":"data-migration"}],"has_more":false}`},
				{method: http.MethodGet, path: base, body: body, status: tc.status},
			})
			target := "/orgs/acme-hq/projects/data-migration/schedules/nightly?environment_slug=prod&section=" + tc.section
			if tc.name == "defaultEnvironment" {
				target = "/orgs/acme-hq/projects/data-migration/schedules/nightly"
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
			req.Header.Set("Cookie", cookie)
			addCSRFToken(t, app, req)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			if tc.status == 401 {
				require.Equal(t, http.StatusSeeOther, resp.StatusCode)
				require.Contains(t, resp.Header.Get("Location"), "/auth/login")
				return
			}
			require.Equal(t, tc.status, resp.StatusCode)
			html, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Contains(t, string(html), tc.want)
			if tc.section == "configuration" && tc.status == 200 {
				require.NotContains(t, string(html), "Scheduled-fire history")
			}
			if tc.section == "history" {
				require.Contains(t, string(html), "Schedule fire history is unavailable.")
				require.NotContains(t, string(html), "Version policy")
			}
		})
	}
}
