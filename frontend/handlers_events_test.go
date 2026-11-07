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
	"github.com/Braidplane/braidplane-core/frontend/views"
)

func TestRouter_HandleEventsPage_RendersActivity(t *testing.T) {
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
				`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/events":
			assert.Equal(t, "pipeline", r.URL.Query().Get("entity_type"))
			assert.Equal(t, "update", r.URL.Query().Get("action"))
			writeJSON(
				w,
				`{"events":[{"slug":"event-001","actor_type":"user","actor_slug":"jane-admin","entity_type":"pipeline","entity_slug":"users-sync","action":"update","occurred_at":"2026-05-22T12:30:00Z","metadata":{"pipeline_slug":"users-sync","source":"ui"},"created_at":"2026-05-22T12:30:01Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
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
		"/orgs/acme-hq/events?entity_type=pipeline&action=update",
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
	require.Contains(t, string(body), "Events")
	require.Contains(t, string(body), "event-001")
	require.Contains(t, string(body), "jane-admin")
	require.Contains(t, string(body), "users-sync")
	require.Contains(t, string(body), "View metadata")
	require.Contains(t, string(body), "pipeline_slug")
}

func TestRouter_HandleEventsPage_UsesSecurityAuditContractAndFilters(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations":
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false}`)
		case "/api/organizations/acme-hq/projects":
			writeJSON(w, `{"projects":[],"has_more":false}`)
		case "/api/organizations/acme-hq/security-audit-events":
			assert.Equal(t, "req-123", r.URL.Query().Get("request_id"))
			assert.Equal(t, "2026-07-01T00:00:00Z", r.URL.Query().Get("occurred_from"))
			assert.Equal(t, "next", r.URL.Query().Get("direction"))
			assert.Equal(t, "cursor-1", r.URL.Query().Get("cursor"))
			writeJSON(w, `{"events":[{"schema_version":1,"event_class":"security_audit","slug":"event-001","actor_type":"user","actor_slug":"jane-admin","entity_type":"api_key","entity_slug":"prod-key","action":"rotate","request_id":"req-row-456","occurred_at":"2026-07-01T12:00:00Z","metadata":{"role_slug":"admin","access_token":"must-not-render"},"created_at":"2026-07-01T12:00:01Z"}],"has_more":false,"event_log_retention_days":21}`)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/events?event_class=security_audit&request_id=req-123&occurred_from=2026-07-01T00%3A00%3A00Z&cursor=cursor-1&direction=next", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Security audit")
	require.Contains(t, html, "for 21 days from occurrence")
	require.Contains(t, html, "Schema v1")
	require.Contains(t, html, "req-123")
	require.Less(t, strings.Index(html, "prod-key"), strings.Index(html, "req-row-456"))
	require.Contains(t, html, "role_slug")
	require.NotContains(t, html, "must-not-render")
	require.Contains(t, html, "10,000")
}

func TestRouter_ListEventsForViewReturnsSecurityDateValidationMessage(t *testing.T) {
	t.Parallel()

	router := &Router{}
	_, status, response, err := router.listEventsForView(
		context.Background(),
		"",
		"acme-hq",
		views.EventFilters{
			EventClass:     views.EventClassSecurityAudit,
			OccurredFrom:   "2026-08-01T00:00:00Z",
			OccurredBefore: "2026-07-01T00:00:00Z",
		},
		api.LimitParam(20),
		"",
		"",
	)

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.NotNil(t, response)
	require.Equal(t, "Occurred before must be later than occurred from.", response.Error.Message)
}

func TestRouter_HandleSecurityAuditEventsExport_ProxiesCSV(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false}`)
		case "/api/organizations/acme-hq/security-audit-events/export":
			assert.Equal(t, "req-123", r.URL.Query().Get("request_id"))
			assert.Equal(t, "2026-07-01T00:00:00Z", r.URL.Query().Get("occurred_from"))
			assert.Equal(t, "2026-08-01T00:00:00Z", r.URL.Query().Get("occurred_before"))
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("Content-Disposition", `attachment; filename="braidplane-security-audit-events-acme-hq.csv"`)
			_, _ = w.Write([]byte("schema_version,event_slug\n1,event-001\n"))
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
		"/orgs/acme-hq/events/export?request_id=req-123&occurred_from=2026-07-01T00%3A00%3A00Z&occurred_before=2026-08-01T00%3A00%3A00Z",
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
	require.Equal(t, `attachment; filename="braidplane-security-audit-events-acme-hq.csv"`, resp.Header.Get("Content-Disposition"))
	require.Equal(t, "schema_version,event_slug\n1,event-001\n", string(body))
}
