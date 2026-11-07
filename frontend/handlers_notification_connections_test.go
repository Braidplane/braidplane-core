package frontend

import (
	"context"
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

func TestNotificationConnectionsPageShowsChannelAndLifecycleControls(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/organizations/acme-hq/notification-connections" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"slug":"ops-slack","name":"Operations Slack","provider":"slack","status":"active","metadata":{"workspace_id":"T123","workspace_name":"Test workspace","channel_id":"C123","channel_name":"#alerts"},"updated_at":"2026-10-19T10:00:00Z"}]`)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/alerts/connections?provider_oauth=success", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	response, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	for _, expected := range []string{"Notification connections", "Operations Slack", "Test workspace", "#alerts", "Reconnect / change target", "Disconnect", "/alerts/connections/ops-slack/connect", "/alerts/connections/ops-slack/disconnect", "Slack connected."} {
		require.Contains(t, string(body), expected)
	}
	require.NotContains(t, string(body), "hooks.slack.com")
}

func TestNotificationConnectionsPreservesErrorStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, method, body        string
		backendStatus, wantStatus int
	}{
		{name: "PermissionDenied", method: http.MethodGet, backendStatus: http.StatusForbidden, wantStatus: http.StatusForbidden},
		{name: "MissingOrganization", method: http.MethodGet, backendStatus: http.StatusNotFound, wantStatus: http.StatusNotFound},
		{name: "InvalidSlugPreservedDuringOutage", method: http.MethodPost, body: "name=Operations&slug=invalid+slug&provider=slack", backendStatus: http.StatusServiceUnavailable, wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/organizations/acme-hq/notification-connections" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.backendStatus)
					_, _ = io.WriteString(w, `{}`)
					return
				}
				if !handleAlertsCommonTestAPI(w, r, "admin") {
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer backend.Close()
			store := session.NewStore()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			req := httptest.NewRequestWithContext(t.Context(), tc.method, "/orgs/acme-hq/alerts/connections", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			addCSRFToken(t, app, req)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, tc.wantStatus, resp.StatusCode)
		})
	}
}

func TestCreateNotificationConnectionIdentifiers(t *testing.T) {
	for _, custom := range []string{"", "custom-id", "INVALID"} {
		t.Run(custom, func(t *testing.T) {
			called := false
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/organizations/acme-hq/notification-connections" {
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodPost {
						called = true
						var body map[string]any
						assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
						if custom == "" {
							assert.NotContains(t, body, "slug")
						} else {
							assert.Equal(t, custom, body["slug"])
						}
						w.WriteHeader(http.StatusCreated)
						_, _ = io.WriteString(w, `{"slug":"created","name":"Operations","provider":"slack","status":"pending","metadata":{},"updated_at":"2026-10-19T10:00:00Z"}`)
					} else {
						_, _ = io.WriteString(w, `[]`)
					}
					return
				}
				if !handleAlertsCommonTestAPI(w, r, "admin") {
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer backend.Close()
			store := session.NewStore()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orgs/acme-hq/alerts/connections", strings.NewReader("name=Operations&provider=slack&slug="+custom))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			addCSRFToken(t, app, req)
			response, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			if custom == "INVALID" {
				require.False(t, called)
				require.Equal(t, http.StatusBadRequest, response.StatusCode)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.Contains(t, string(body), `value="Operations"`)
				require.Contains(t, string(body), `value="INVALID"`)
				require.Contains(t, string(body), `<details class="min-w-0" open>`)
			} else {
				require.True(t, called)
				require.Equal(t, http.StatusSeeOther, response.StatusCode)
			}
		})
	}
}
