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
)

func TestRouter_HandleNotificationsPage_RendersNotificationInbox(t *testing.T) {
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
				`{"projects":[{"name":"Ops","slug":"ops","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/notifications":
			assert.Equal(t, "resolved", r.URL.Query().Get("lifecycle_state"))
			assert.Equal(t, "critical", r.URL.Query().Get("severity"))
			writeJSON(
				w,
				`{"notifications":[{"slug":"notif-001","notification_type":"pipeline.run_failed","severity":"critical","lifecycle_state":"resolved","summary":"Pipeline users-sync failed in prod","source_event_slug":"evt-run-failed","source_entity_type":"pipeline_run","source_action":"execute","source_refs":{"project_slug":"ops","environment_slug":"prod","run_slug":"run-001","pipeline_slug":"users-sync"},"safe_metadata":{"summary":"Pipeline users-sync failed in prod","step":"load-users"},"occurrence_count":3,"first_seen_at":"2026-06-30T08:15:00Z","last_seen_at":"2026-06-30T10:15:00Z","last_occurrence_at":"2026-06-30T10:10:00Z","dispatch_statuses":[{"destination_slug":"ops-email","status":"queued","queued_at":"2026-06-30T10:16:00Z","job_kind":"notification_delivery","job_queue":"notification_delivery","job_state":"queued"}],"acknowledged_at":null,"resolved_at":"2026-06-30T10:20:00Z","created_at":"2026-06-30T08:15:00Z","updated_at":"2026-06-30T10:20:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(apiServer.Close)

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
		"/orgs/acme-hq/notifications?state=resolved&severity=critical",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyText := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, bodyText, "Notifications")
	require.Contains(t, bodyText, "Pipeline users-sync failed in prod")
	require.Contains(t, bodyText, "2026-06-30T10:20:00Z")
	require.Contains(t, bodyText, "ops-email: Queued")
	require.Contains(t, bodyText, "Safe metadata")
	require.Contains(t, bodyText, "load-users")
	require.Contains(t, bodyText, "/orgs/acme-hq/projects/ops/runs/run-001?environment_slug=prod")
	require.Contains(t, bodyText, "/orgs/acme-hq/projects/ops/pipelines/users-sync?environment_slug=prod")
}

func TestRouter_HandleNotificationsPage_RendersWorkspaceError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		status  int
		body    string
		message string
	}{
		{"Unavailable", http.StatusInternalServerError, `{"error":{"code":"INTERNAL_SERVER_ERROR","message":"notification backend unavailable"}}`, "notification backend unavailable"},
		{"Denied", http.StatusForbidden, `{"error":{"code":"FORBIDDEN","message":"notification access denied"}}`, "notification access denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
						`{"projects":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
					)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/notifications":
					w.WriteHeader(tc.status)
					writeJSON(w, tc.body)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(apiServer.Close)

			store := session.NewStore()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)

			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})

			authCookie := getAuthenticatedSessionCookie(t, app)
			assertWorkspaceErrorResponse := func(t *testing.T, htmx bool, wantStatus int) {
				t.Helper()

				req := httptest.NewRequestWithContext(
					context.Background(),
					http.MethodGet,
					"/orgs/acme-hq/notifications?state=resolved&severity=critical",
					nil,
				)
				req.Header.Set("Cookie", authCookie)
				if htmx {
					req.Header.Set("HX-Request", "true")
				}
				addCSRFToken(t, app, req)

				resp, err := app.Test(req)
				require.NoError(t, err)
				defer func() { _ = resp.Body.Close() }()

				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				bodyText := string(body)

				require.Equal(t, wantStatus, resp.StatusCode)
				require.Contains(t, bodyText, `id="notifications-workspace"`)
				require.Contains(t, bodyText, "Unable to refresh notifications")
				require.Contains(t, bodyText, tc.message)
				require.NotContains(t, bodyText, "No resolved notifications")
				require.NotContains(t, bodyText, "No notifications match these filters")
				require.NotContains(t, bodyText, "No active notifications")
			}

			t.Run("HTMXWorkspaceErrorReturnsOK", func(t *testing.T) {
				t.Parallel()
				assertWorkspaceErrorResponse(t, true, http.StatusOK)
			})
			t.Run("FullPageWorkspaceErrorReturnsBackendStatus", func(t *testing.T) {
				t.Parallel()
				assertWorkspaceErrorResponse(t, false, tc.status)
			})
		})
	}
}

func TestRouter_HandleAcknowledgeNotificationSubmit_RedirectsToFilteredList(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/notifications/notif-001/acknowledge":
			writeJSON(
				w,
				`{"slug":"notif-001","notification_type":"pipeline.run_failed","severity":"critical","lifecycle_state":"acknowledged","summary":"Pipeline users-sync failed in prod","source_event_slug":null,"source_entity_type":"pipeline_run","source_action":"execute","source_refs":{},"safe_metadata":{},"occurrence_count":1,"first_seen_at":"2026-06-30T08:15:00Z","last_seen_at":"2026-06-30T10:15:00Z","last_occurrence_at":"2026-06-30T10:10:00Z","dispatch_statuses":[],"acknowledged_at":"2026-06-30T10:20:00Z","resolved_at":null,"created_at":"2026-06-30T08:15:00Z","updated_at":"2026-06-30T10:20:00Z"}`,
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
		http.MethodPost,
		"/orgs/acme-hq/notifications/notif-001/acknowledge",
		strings.NewReader("state=active&severity=critical"),
	)
	req.Header.Set("Cookie", authCookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/notifications?severity=critical", resp.Header.Get("HX-Redirect"))
}

func TestRouter_HandleResolveNotificationSubmit_RedirectsToFilteredList(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/notifications/notif-001/resolve":
			writeJSON(
				w,
				`{"slug":"notif-001","notification_type":"pipeline.run_failed","severity":"warning","lifecycle_state":"resolved","summary":"Pipeline users-sync failed in prod","source_event_slug":null,"source_entity_type":"pipeline_run","source_action":"execute","source_refs":{},"safe_metadata":{},"occurrence_count":1,"first_seen_at":"2026-06-30T08:15:00Z","last_seen_at":"2026-06-30T10:15:00Z","last_occurrence_at":"2026-06-30T10:10:00Z","dispatch_statuses":[],"acknowledged_at":null,"resolved_at":"2026-06-30T10:20:00Z","created_at":"2026-06-30T08:15:00Z","updated_at":"2026-06-30T10:20:00Z"}`,
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
		http.MethodPost,
		"/orgs/acme-hq/notifications/notif-001/resolve",
		strings.NewReader("state=resolved&severity=warning"),
	)
	req.Header.Set("Cookie", authCookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/notifications?severity=warning&state=resolved", resp.Header.Get("HX-Redirect"))
}

func TestNotificationsRecencyCapability(t *testing.T) {
	for _, capability := range []string{"enabled", "disabled", "unavailable"} {
		t.Run(capability, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/organizations/acme-hq/pipeline-operational-capabilities":
					if capability == "unavailable" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					enabled := "false"
					if capability == "enabled" {
						enabled = "true"
					}
					writeJSON(w, `{"successful_run_recency":`+enabled+`}`)
				case "/api/organizations/acme-hq/notifications":
					assert.Equal(t, "pipeline.freshness_sla_breached", r.URL.Query().Get("notification_type"))
					writeJSON(w, `{"notifications":[{"slug":"recency-notice","notification_type":"pipeline.freshness_sla_breached","severity":"warning","lifecycle_state":"resolved","summary":"Recency window exceeded","source_entity_type":"pipeline","source_action":"execute","source_refs":{"project_slug":"ops","environment_slug":"dev","pipeline_slug":"orders"},"occurrence_count":1}],"has_more":false}`)
				case "/api/organizations/acme-hq/alert-delivery-attempts":
					writeJSON(w, `{"alert_delivery_attempts":[],"has_more":false}`)
				default:
					if !handleAlertsCommonTestAPI(w, r, "admin") {
						w.WriteHeader(http.StatusNotFound)
					}
				}
			}))
			defer backend.Close()
			store := session.NewStore()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orgs/acme-hq/notifications?state=resolved&notification_type=pipeline.freshness_sla_breached", nil)
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			response, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, response.Body.Close()) }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			html := string(body)
			require.Contains(t, html, "/pipelines/orders/environments/dev/recency")
			if capability == "enabled" {
				require.Contains(t, html, `selected value="pipeline.freshness_sla_breached"`)
				require.Contains(t, html, ">Configure alerts</a>")
				require.NotContains(t, html, "This historical condition is not currently monitored")
			} else {
				require.NotContains(t, html, `value="pipeline.freshness_sla_breached"`)
				require.NotContains(t, html, ">Configure alerts</a>")
				require.Contains(t, html, "This historical condition is not currently monitored")
			}
		})
	}
}
