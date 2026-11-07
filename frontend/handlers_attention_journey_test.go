package frontend

import (
	"context"
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
)

func TestAttentionJourneyUsesExactAuthorizedResourcesWithoutSending(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, route, apiPath, response, want string
		status                               int
	}{
		{"policy", "alerts?tab=policies&policy_slug=recorded-policy", "alert-policies/recorded-policy", `{"slug":"recorded-policy","name":"Recorded routing","scope":"organization","notification_type":"pipeline.retry_exhausted","destination_slugs":["ops-email"],"enabled":false}`, "Recorded routing", 200},
		{"destination", "alerts?tab=destinations&destination_slug=recorded-destination", "alert-destinations/recorded-destination", `{"slug":"recorded-destination","name":"Recorded target","type":"email","non_secret_config":{"recipients":["synthetic@example.com"]},"enabled":false}`, "Recorded target", 200},
		{"deleted-policy", "alerts?tab=policies&policy_slug=deleted", "alert-policies/deleted", `{"error":{"code":"NOT_FOUND"}}`, "Alert policy details are unavailable", 404},
		{"denied-destination", "alerts?tab=destinations&destination_slug=foreign", "alert-destinations/foreign", `{"error":{"code":"FORBIDDEN"}}`, "Alert destination details are unavailable", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var called atomic.Bool
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("read-only navigation attempted %s", r.Method)
				}
				if r.URL.Path == "/api/organizations/acme-hq/"+tc.apiPath {
					called.Store(true)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.response)
					return
				}
				if handleAlertsCommonTestAPI(w, r, "admin") {
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer backend.Close()
			store := session.NewStore()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			cookie := getAuthenticatedSessionCookie(t, app)
			inbox := "/orgs/acme-hq/notifications?state=resolved&severity=warning#notification-original"
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/"+tc.route+"&notification_return="+url.QueryEscape(inbox), nil)
			req.Header.Set("Cookie", cookie)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.True(t, called.Load())
			require.Equal(t, tc.status, resp.StatusCode)
			require.Contains(t, string(body), tc.want)
			require.Contains(t, string(body), "Back to notification")
			require.Contains(t, string(body), "#notification-original")
			if strings.Contains(tc.name, "deleted") {
				require.NotContains(t, string(body), ">Critical pipeline failures</p>")
			}
		})
	}
}

func TestAttentionSetupRejectsUnavailableConditionsAndPreservesScope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"notification_type=pipeline.retry_exhausted&project_slug=ops&environment_slug=prod", 200},
		{"notification_type=pipeline.run_succeeded&project_slug=ops", 200},
		{"notification_type=pipeline.run_failed&project_slug=ops&environment_slug=prod", 200},
		{"notification_type=pipeline.scheduled_run_delayed&project_slug=ops&environment_slug=prod", 200},
		{"notification_type=pipeline.scheduled_run_missing&project_slug=ops&environment_slug=prod", 200},
		{"notification_type=pipeline.freshness_sla_breached", 400},
		{"environment_slug=prod", 400},
		{"project_slug=../other", 400},
	} {
		t.Run(tc.query, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("read-only setup attempted %s", r.Method)
				}
				if handleAlertsCommonTestAPI(w, r, "admin") {
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer backend.Close()
			store := session.NewStore()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			cookie := getAuthenticatedSessionCookie(t, app)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/alerts?tab=policies&"+tc.query+"&notification_return="+url.QueryEscape("/orgs/acme-hq/notifications#notification-original"), nil)
			req.Header.Set("Cookie", cookie)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.status, resp.StatusCode)
			if tc.status == 200 {
				require.Contains(t, string(body), "Policy review")
				require.Contains(t, string(body), `value="ops" selected`)
				require.Contains(t, string(body), `name="notification_return"`)
			}
		})
	}
}

func TestAttentionPolicyReaderDoesNotLoadDestinationConfig(t *testing.T) {
	t.Parallel()
	var destinationRequests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "alert-destinations") || strings.Contains(r.URL.Path, "notification-connections") {
			destinationRequests.Add(1)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == "/api/organizations/acme-hq/roles/viewer" {
			writeJSON(w, `{"slug":"viewer","permissions":[{"resource":"notifications","action":"read"},{"resource":"alert_policies","action":"read"},{"resource":"alert_policies","action":"create"}]}`)
			return
		}
		if handleAlertsCommonTestAPI(w, r, "viewer") {
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer backend.Close()
	store := session.NewStore()
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/alerts?tab=policies", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Zero(t, destinationRequests.Load())
	require.Contains(t, string(body), "Critical pipeline failures")
	require.Contains(t, string(body), "Destination configuration is restricted")
	require.NotContains(t, string(body), "ops@example.com")
	require.NotContains(t, string(body), "No alert destinations exist yet.")
	require.Contains(t, string(body), "Destination choices are restricted or unavailable.")
	require.NotContains(t, string(body), ">Destination: ops-email</a>")
}

func TestAttentionDestinationErrorsRetainPendingPolicySetup(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"validation", "backend"} {
		t.Run(name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/alert-destinations" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					_, _ = io.WriteString(w, `{"error":{"code":"CONFLICT","message":"Synthetic duplicate destination"}}`)
					return
				}
				if handleAlertsCommonTestAPI(w, r, "admin") {
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer backend.Close()
			store := session.NewStore()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			fields := url.Values{"name": {"Operations"}, "slug": {"ops-email"}, "type": {"email"}, "to": {"synthetic@example.com"}, "notification_return": {"/orgs/acme-hq/notifications#notification-original"}, "notification_setup": {"/orgs/acme-hq/alerts?tab=policies&notification_type=pipeline.run_succeeded&project_slug=ops&environment_slug=prod"}}
			if name == "validation" {
				fields.Set("name", "")
			}
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/alerts/destinations", strings.NewReader(fields.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			addCSRFToken(t, app, req)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.GreaterOrEqual(t, resp.StatusCode, 400)
			require.Contains(t, string(body), "Return to policy setup")
			require.Contains(t, string(body), "notification_type=pipeline.run_succeeded")
			require.Contains(t, string(body), "project_slug=ops")
			require.Contains(t, string(body), "environment_slug=prod")
		})
	}
}

func TestAttentionLifecycleFailureUsesThePreservedPageCursor(t *testing.T) {
	t.Parallel()
	var observed atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/organizations/acme-hq/notifications/original/resolve" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"code":"CONFLICT","message":"Synthetic lifecycle conflict"}}`)
			return
		}
		if r.URL.Path == "/api/organizations/acme-hq/notifications" {
			if r.URL.Query().Get("cursor") != "older-page" || r.URL.Query().Get("direction") != "next" {
				t.Errorf("render requested a different page: %s", r.URL.RawQuery)
			}
			observed.Store(true)
			writeJSON(w, `{"notifications":[],"has_more":false}`)
			return
		}
		if handleAlertsCommonTestAPI(w, r, "admin") {
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer backend.Close()
	store := session.NewStore()
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/notifications/original/resolve", strings.NewReader("state=active&cursor=older-page&direction=next&severity=warning"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.True(t, observed.Load())
}
