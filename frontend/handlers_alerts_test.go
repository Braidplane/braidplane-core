package frontend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouter_HandleAlertsPage_RequiresAuth(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/alerts", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login?return_to=%2Forgs%2Facme-hq%2Falerts", resp.Header.Get("Location"))
}

func TestRouter_HandleAlertsPage_RendersAlertManagement(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAlertsCommonTestAPI(w, r, "admin") {
			return
		}
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/alerts", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, `id="alerts-workspace"`)
	require.Contains(t, html, `role="tablist"`)
	require.Contains(t, html, `href="/orgs/acme-hq/alerts?tab=destinations"`)
	require.Contains(t, html, `href="/orgs/acme-hq/alerts?tab=policies"`)
	require.Contains(t, html, "Alert destinations")
	require.Contains(t, html, "Ops email")
	require.Contains(t, html, "ops@example.com")
	require.Contains(t, html, `action="/orgs/acme-hq/alerts/destinations"`)
	require.Contains(t, html, `action="/orgs/acme-hq/alerts/destinations/ops-email/test"`)
	require.NotContains(t, html, "Alert policies")
	require.NotContains(t, html, "Critical pipeline failures")
	require.NotContains(t, html, `action="/orgs/acme-hq/alerts/policies"`)
	require.NotContains(t, html, `name="destination_slug"`)
	require.NotContains(t, html, `data-confirm-slug="critical-pipeline-failures"`)
	require.Contains(t, html, `hx-target="#alerts-workspace"`)
}

func TestRouter_HandleAlertsPage_RendersAlertPolicyTab(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAlertsCommonTestAPI(w, r, "admin") {
			return
		}
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/alerts?tab=policies", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, `id="alerts-workspace"`)
	require.Contains(t, html, `id="alerts-policies-tab"`)
	require.Contains(t, html, "Alert policies")
	require.Contains(t, html, "Critical pipeline failures")
	require.Contains(t, html, "ops-email, data-team-email")
	require.Contains(t, html, `action="/orgs/acme-hq/alerts/policies"`)
	require.Contains(t, html, `name="destination_slug"`)
	require.Contains(t, html, `value="ops-email"`)
	require.Contains(t, html, `data-confirm-slug="critical-pipeline-failures"`)
	require.NotContains(t, html, "Alert destinations")
	require.NotContains(t, html, `action="/orgs/acme-hq/alerts/destinations"`)
	require.Contains(t, html, `hx-target="#alerts-workspace"`)
}

func TestRouter_HandleAlertPolicySelectorOptions_RendersScopedDropdowns(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAlertsCommonTestAPI(w, r, "admin") {
			return
		}
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
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/alerts/policies/selector-options?selector_target_id=test-selectors&policy_kind=notification_type&scope=project_environment&project_slug=ops&environment_slug=prod",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, `id="test-selectors"`)
	require.Contains(t, html, `hx-sync="closest form:replace"`)
	require.Contains(t, html, `<option value="pipeline.retry_exhausted" selected>Pipeline retry exhausted</option>`)
	require.Contains(t, html, `value="pipeline.run_failed"`)
	require.Contains(t, html, `value="pipeline.scheduled_run_delayed"`)
	require.Contains(t, html, `value="pipeline.scheduled_run_missing"`)
	require.NotContains(t, html, `value="pipeline.freshness_sla_breached"`)
	require.Contains(t, html, `select class="select select-bordered select-sm w-full" name="project_slug"`)
	require.Contains(t, html, `<option value="ops" selected>Ops (ops)</option>`)
	require.Contains(t, html, `select class="select select-bordered select-sm w-full" name="environment_slug"`)
	require.Contains(t, html, `<option value="prod" selected>Production (prod) (default)</option>`)
	require.Contains(t, html, `name="source_kind"`)
	require.Contains(t, html, `<option value="project">Project</option>`)
	require.Contains(t, html, `<option value="pipeline_run">Pipeline run</option>`)
	require.NotContains(t, html, `input class="input input-bordered input-sm w-full font-mono" type="text" name="project_slug"`)
	require.NotContains(t, html, `input class="input input-bordered input-sm w-full font-mono" type="text" name="environment_slug"`)
}

func TestRouter_HandleAlertPolicySelectorOptions_UsesMetadataEndpointForSourceRefs(t *testing.T) {
	t.Parallel()

	var projectAPICalls atomic.Int64
	var metadataAPICalls atomic.Int64
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/projects") {
			projectAPICalls.Add(1)
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/alert-policy-source-metadata" {
			metadataAPICalls.Add(1)
			assert.Equal(t, "organization", r.URL.Query().Get("scope"))
			assert.Equal(t, "pipeline_run", r.URL.Query().Get("source_kind"))
		}
		if handleAlertsCommonTestAPI(w, r, "admin") {
			return
		}
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
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/alerts/policies/selector-options?selector_target_id=test-selectors&policy_kind=notification_type&scope=organization&source_kind=pipeline_run",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, int64(0), projectAPICalls.Load())
	require.Equal(t, int64(1), metadataAPICalls.Load())
	require.Contains(t, html, `id="test-selectors"`)
	require.Contains(t, html, `select class="select select-bordered select-sm w-full font-mono" name="source_ref"`)
	require.Contains(t, html, `<option value="" selected>Any source ref</option>`)
	require.Contains(t, html, `<option value="daily-ledger">Daily ledger</option>`)
}

func TestRouter_HandleAlertsPage_GatesActionsFromPermissions(t *testing.T) {
	t.Parallel()

	var mutationCalled atomic.Bool
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAlertsCommonTestAPI(w, r, "viewer") {
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/alert-policies/critical-pipeline-failures" {
			mutationCalled.Store(true)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":"FORBIDDEN","message":"forbidden"}}`)
			return
		}
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/alerts?tab=policies", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Critical pipeline failures")
	require.Contains(t, html, "No actions")
	require.NotContains(t, html, "Create destination")
	require.NotContains(t, html, "Create policy")
	require.NotContains(t, html, `/alerts/destinations/ops-email/test`)
	require.NotContains(t, html, `data-dirty-form="alert-destination-ops-email-update"`)
	require.NotContains(t, html, `data-protected-action-modal`)

	form := strings.NewReader("name=Critical+pipeline+failures&scope=project_environment&project_slug=ops&environment_slug=prod&notification_type=pipeline.run_failed&severity=critical&lifecycle_state=open&destination_slug=ops-email&enabled=on")
	mutationReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/alerts/policies/critical-pipeline-failures/update",
		form,
	)
	mutationReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mutationReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, mutationReq)

	mutationResp, err := app.Test(mutationReq)
	require.NoError(t, err)
	defer func() { _ = mutationResp.Body.Close() }()

	mutationBody, err := io.ReadAll(mutationResp.Body)
	require.NoError(t, err)
	mutationHTML := string(mutationBody)

	require.Equal(t, http.StatusForbidden, mutationResp.StatusCode)
	require.True(t, mutationCalled.Load())
	require.Contains(t, mutationHTML, "forbidden")
	require.Contains(t, mutationHTML, "status=403")
	require.Contains(t, mutationHTML, "backend_code=FORBIDDEN")
}

func TestRouter_HandleAlertsPage_RendersUnavailableAlertState(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/alert-destinations":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"code":"INTERNAL","message":"destinations unavailable"}}`)
			return
		case handleAlertsCommonTestAPI(w, r, "admin"):
			return
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/alerts", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Contains(t, html, "Alert destinations are unavailable.")
	require.NotContains(t, html, "No alert destinations")
}

func TestRouter_HandleAlertsPage_DisablesPolicyUpdateWhenNoDestinationsExist(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/alert-destinations":
			writeJSON(w, `{"alert_destinations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
			return
		case handleAlertsCommonTestAPI(w, r, "admin"):
			return
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/alerts?tab=policies", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "No alert destinations exist yet.")
	require.Contains(t, html, "Create a destination first")
	require.Contains(t, html, `href="/orgs/acme-hq/alerts?tab=destinations"`)
	require.NotContains(t, html, `data-dirty-form="alert-policy-critical-pipeline-failures-update"`)
	require.Contains(t, html, `disabled>Save policy</button>`)
}

func TestRouter_HandleCreateAlertDestinationSubmit_CreatesDestination(t *testing.T) {
	t.Parallel()

	created := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAlertsCommonTestAPI(w, r, "admin") {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/alert-destinations":
			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var payload map[string]any
			if !assert.NoError(t, json.Unmarshal(body, &payload)) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			assert.Equal(t, "On-call email", payload["name"])
			assert.Equal(t, "on-call-email", payload["slug"])
			assert.Equal(t, "email", payload["type"])
			assert.NotContains(t, payload, "credential_slug")
			config, ok := payload["non_secret_config"].(map[string]any)
			if assert.True(t, ok) {
				assert.Equal(t, []any{"on-call@example.com", "escalation@example.com"}, config["recipients"])
			}

			created = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"slug":"on-call-email","name":"On-call email","type":"email","non_secret_config":{"recipients":["on-call@example.com","escalation@example.com"]},"enabled":true,"created_at":"2026-06-30T10:00:00Z","updated_at":"2026-06-30T10:00:00Z"}`)
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
	form := strings.NewReader("name=On-call+email&slug=on-call-email&type=email&to=on-call%40example.com%2C+escalation%40example.com")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/alerts/destinations", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/alerts?tab=destinations", resp.Header.Get("Location"))
	require.True(t, created)
}

func TestRouter_HandleTestAlertDestinationSubmit_RendersAcceptedResult(t *testing.T) {
	t.Parallel()

	tested := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAlertsCommonTestAPI(w, r, "admin") {
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/alert-destinations/ops-email/test" {
			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			assert.Empty(t, strings.TrimSpace(string(body)))

			tested = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"status":"accepted","destination_slug":"ops-email","destination_type":"email","recipient_count":2,"diagnostics_persisted":false,"enqueued_jobs":[{"queue":"email","recipient":"ops@example.com"},{"queue":"email","recipient":"oncall@example.com"}]}`)
			return
		}
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
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/alerts/destinations/ops-email/test",
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
	require.True(t, tested)
	require.Contains(t, html, "Alert destination test accepted for 2 recipients and 2 delivery jobs.")
	require.Contains(t, html, `id="alerts-workspace"`)
	require.NotContains(t, html, "credential")
	require.NotContains(t, html, "provider_token")
}

func TestRouter_HandleTestAlertDestinationSubmit_RendersBackendError(t *testing.T) {
	t.Parallel()

	tested := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAlertsCommonTestAPI(w, r, "admin") {
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/alert-destinations/data-team-email/test" {
			tested = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"ALERT_DESTINATION_DISABLED","message":"alert destination disabled"}}`)
			return
		}
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
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/alerts/destinations/data-team-email/test",
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

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.True(t, tested)
	require.Contains(t, html, "alert destination disabled")
	require.Contains(t, html, "backend_code=ALERT_DESTINATION_DISABLED")
	require.NotContains(t, html, "credential")
	require.NotContains(t, html, "provider_token")
}

func TestRouter_HandleCreateAlertPolicySubmit_CreatesPolicyWithDestinationBindings(t *testing.T) {
	t.Parallel()

	created := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAlertsCommonTestAPI(w, r, "admin") {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/alert-policies":
			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var payload map[string]any
			if !assert.NoError(t, json.Unmarshal(body, &payload)) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			assert.Equal(t, "Critical pipeline failures", payload["name"])
			assert.Equal(t, "critical-pipeline-failures", payload["slug"])
			assert.Equal(t, "project_environment", payload["scope"])
			assert.Equal(t, "ops", payload["project_slug"])
			assert.Equal(t, "prod", payload["environment_slug"])
			assert.Equal(t, "pipeline.run_failed", payload["notification_type"])
			assert.Equal(t, "critical", payload["severity"])
			assert.Equal(t, "open", payload["lifecycle_state"])
			assert.Equal(t, "pipeline_run", payload["source_kind"])
			assert.Equal(t, "daily-ledger", payload["source_ref"])
			destinationSlugs, ok := payload["destination_slugs"].([]any)
			if assert.True(t, ok) {
				assert.Equal(t, []any{"ops-email", "data-team-email"}, destinationSlugs)
			}

			created = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"slug":"critical-pipeline-failures","name":"Critical pipeline failures","scope":"project_environment","project_slug":"ops","environment_slug":"prod","notification_type":"pipeline.run_failed","severity":"critical","lifecycle_state":"open","source_kind":"pipeline_run","source_ref":"daily-ledger","destination_slugs":["ops-email","data-team-email"],"enabled":true,"created_at":"2026-06-30T10:00:00Z","updated_at":"2026-06-30T10:00:00Z"}`)
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
	form := strings.NewReader("name=Critical+pipeline+failures&slug=critical-pipeline-failures&scope=project_environment&project_slug=ops&environment_slug=prod&notification_type=pipeline.run_failed&severity=critical&lifecycle_state=open&source_kind=pipeline_run&source_ref=daily-ledger&destination_slug=ops-email&destination_slug=data-team-email")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/alerts/policies", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/acme-hq/alerts?tab=policies", resp.Header.Get("Location"))
	require.True(t, created)
}

func TestRouter_HandleDeleteAlertPolicySubmit_RequiresTypedConfirmation(t *testing.T) {
	t.Parallel()

	deleted := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAlertsCommonTestAPI(w, r, "admin") {
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/alert-policies/critical-pipeline-failures" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
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
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/alerts/policies/critical-pipeline-failures/delete",
		strings.NewReader("policy_confirmation=wrong"),
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

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, html, "Alert policy could not be deleted.")
	require.Contains(t, html, "Validation: type the policy slug to confirm deletion.")
	require.False(t, deleted)
}

func TestRouter_HandleAlertMutationSubmit_ForwardsRequestAndRedirects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		route          string
		form           string
		apiMethod      string
		apiPath        string
		apiStatus      int
		apiResponse    string
		assertPayload  func(t *testing.T, payload map[string]any)
		assertNoBody   bool
		expectedStatus int
		expectedPath   string
	}{
		{
			name:           "UpdateDestinationForwardsMutablePayload",
			route:          "/orgs/acme-hq/alerts/destinations/ops-email/update",
			form:           "name=Ops+pager&type=email&to=pager%40example.com%2C+escalation%40example.com&enabled=on",
			apiMethod:      http.MethodPut,
			apiPath:        "/api/organizations/acme-hq/alert-destinations/ops-email",
			apiStatus:      http.StatusOK,
			apiResponse:    `{"slug":"ops-email","name":"Ops pager","type":"email","non_secret_config":{"recipients":["pager@example.com"]},"enabled":true,"created_at":"2026-06-30T10:00:00Z","updated_at":"2026-06-30T11:00:00Z"}`,
			expectedStatus: http.StatusSeeOther,
			expectedPath:   "/orgs/acme-hq/alerts?tab=destinations",
			assertPayload: func(t *testing.T, payload map[string]any) {
				t.Helper()
				assert.Equal(t, "Ops pager", payload["name"])
				assert.Equal(t, "email", payload["type"])
				assert.NotContains(t, payload, "credential_slug")
				assert.Equal(t, true, payload["enabled"])
				assert.NotContains(t, payload, "slug")
				config, ok := payload["non_secret_config"].(map[string]any)
				if assert.True(t, ok) {
					assert.Equal(t, []any{"pager@example.com", "escalation@example.com"}, config["recipients"])
				}
			},
		},
		{
			name:           "DeleteDestinationForwardsTypedConfirmation",
			route:          "/orgs/acme-hq/alerts/destinations/ops-email/delete",
			form:           "destination_confirmation=ops-email",
			apiMethod:      http.MethodDelete,
			apiPath:        "/api/organizations/acme-hq/alert-destinations/ops-email",
			apiStatus:      http.StatusNoContent,
			assertNoBody:   true,
			expectedStatus: http.StatusSeeOther,
			expectedPath:   "/orgs/acme-hq/alerts?tab=destinations",
		},
		{
			name:           "UpdatePolicyForwardsSelectorAndBindings",
			route:          "/orgs/acme-hq/alerts/policies/critical-pipeline-failures/update",
			form:           "name=Critical+pipeline+failures&scope=project_environment&project_slug=ops&environment_slug=prod&notification_type=pipeline.run_failed&severity=critical&lifecycle_state=acknowledged&source_kind=pipeline_run&source_ref=daily-ledger&destination_slug=ops-email&destination_slug=data-team-email&enabled=on",
			apiMethod:      http.MethodPut,
			apiPath:        "/api/organizations/acme-hq/alert-policies/critical-pipeline-failures",
			apiStatus:      http.StatusOK,
			apiResponse:    `{"slug":"critical-pipeline-failures","name":"Critical pipeline failures","scope":"project_environment","project_slug":"ops","environment_slug":"prod","notification_type":"pipeline.run_failed","severity":"critical","lifecycle_state":"acknowledged","source_kind":"pipeline_run","source_ref":"daily-ledger","destination_slugs":["ops-email","data-team-email"],"enabled":true,"created_at":"2026-06-30T10:20:00Z","updated_at":"2026-06-30T11:20:00Z"}`,
			expectedStatus: http.StatusSeeOther,
			expectedPath:   "/orgs/acme-hq/alerts?tab=policies",
			assertPayload: func(t *testing.T, payload map[string]any) {
				t.Helper()
				assert.Equal(t, "Critical pipeline failures", payload["name"])
				assert.Equal(t, "project_environment", payload["scope"])
				assert.Equal(t, "ops", payload["project_slug"])
				assert.Equal(t, "prod", payload["environment_slug"])
				assert.Equal(t, "pipeline.run_failed", payload["notification_type"])
				assert.Equal(t, "critical", payload["severity"])
				assert.Equal(t, "acknowledged", payload["lifecycle_state"])
				assert.Equal(t, "pipeline_run", payload["source_kind"])
				assert.Equal(t, "daily-ledger", payload["source_ref"])
				assert.Equal(t, true, payload["enabled"])
				assert.Equal(t, []any{"ops-email", "data-team-email"}, payload["destination_slugs"])
				assert.NotContains(t, payload, "slug")
			},
		},
		{
			name:           "DeletePolicyForwardsTypedConfirmation",
			route:          "/orgs/acme-hq/alerts/policies/critical-pipeline-failures/delete",
			form:           "policy_confirmation=critical-pipeline-failures",
			apiMethod:      http.MethodDelete,
			apiPath:        "/api/organizations/acme-hq/alert-policies/critical-pipeline-failures",
			apiStatus:      http.StatusNoContent,
			assertNoBody:   true,
			expectedStatus: http.StatusSeeOther,
			expectedPath:   "/orgs/acme-hq/alerts?tab=policies",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			called := false
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if handleAlertsCommonTestAPI(w, r, "admin") {
					return
				}
				if r.Method != tc.apiMethod || r.URL.Path != tc.apiPath {
					w.WriteHeader(http.StatusNotFound)
					return
				}

				called = true
				body, err := io.ReadAll(r.Body)
				if !assert.NoError(t, err) {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if tc.assertNoBody {
					assert.Empty(t, strings.TrimSpace(string(body)))
				}
				if tc.assertPayload != nil {
					var payload map[string]any
					if !assert.NoError(t, json.Unmarshal(body, &payload)) {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					tc.assertPayload(t, payload)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.apiStatus)
				if tc.apiResponse != "" {
					_, _ = io.WriteString(w, tc.apiResponse)
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
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, tc.route, strings.NewReader(tc.form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, tc.expectedStatus, resp.StatusCode)
			require.Equal(t, tc.expectedPath, resp.Header.Get("Location"))
			require.True(t, called)
		})
	}
}

func TestRouter_HandleAlertUpdateSubmit_PreservesSubmittedEditValuesOnBackendError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		route     string
		form      string
		apiPath   string
		marker    string
		wantHTML  []string
		errorText string
	}{
		{
			name:      "DestinationEditForm",
			route:     "/orgs/acme-hq/alerts/destinations/ops-email/update",
			form:      "name=Ops+pager&type=email&to=pager%40example.com&enabled=on",
			apiPath:   "/api/organizations/acme-hq/alert-destinations/ops-email",
			marker:    `data-dirty-form="alert-destination-ops-email-update"`,
			wantHTML:  []string{`value="Ops pager"`, `value="pager@example.com"`},
			errorText: "update rejected",
		},
		{
			name:      "PolicyEditForm",
			route:     "/orgs/acme-hq/alerts/policies/critical-pipeline-failures/update",
			form:      "name=Critical+latency&scope=project_environment&project_slug=payments&environment_slug=staging&notification_type=pipeline.run_failed&severity=critical&lifecycle_state=acknowledged&destination_slug=data-team-email&enabled=on",
			apiPath:   "/api/organizations/acme-hq/alert-policies/critical-pipeline-failures",
			marker:    `data-dirty-form="alert-policy-critical-pipeline-failures-update"`,
			wantHTML:  []string{`value="Critical latency"`, `value="payments"`, `value="staging"`},
			errorText: "update rejected",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			called := false
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if handleAlertsCommonTestAPI(w, r, "admin") {
					return
				}
				if r.Method == http.MethodPut && r.URL.Path == tc.apiPath {
					called = true
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, `{"error":{"code":"INTERNAL","message":"update rejected"}}`)
					return
				}
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
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, tc.route, strings.NewReader(tc.form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			html := string(body)
			editFormHTML := htmlAfterMarker(t, html, tc.marker)

			require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
			require.True(t, called)
			require.Contains(t, html, tc.errorText)
			for _, want := range tc.wantHTML {
				require.Contains(t, editFormHTML, want)
			}
		})
	}
}

func TestRouter_HandleAlertUpdateSubmit_PreservesSubmittedEditValuesOnValidationError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		route     string
		form      string
		marker    string
		wantHTML  []string
		errorText string
	}{
		{
			name:      "DestinationEditForm",
			route:     "/orgs/acme-hq/alerts/destinations/ops-email/update",
			form:      "name=Ops+pager&type=sms&to=pager%40example.com&enabled=on",
			marker:    `data-dirty-form="alert-destination-ops-email-update"`,
			wantHTML:  []string{`value="Ops pager"`, `value="pager@example.com"`},
			errorText: "Validation: destination type is invalid.",
		},
		{
			name:      "PolicyEditForm",
			route:     "/orgs/acme-hq/alerts/policies/critical-pipeline-failures/update",
			form:      "name=Critical+latency&scope=project_environment&project_slug=payments&environment_slug=staging&notification_type=pipeline.run_failed&severity=critical&lifecycle_state=acknowledged&destination_slug=Invalid_Slug&enabled=on",
			marker:    `data-dirty-form="alert-policy-critical-pipeline-failures-update"`,
			wantHTML:  []string{`value="Critical latency"`, `value="payments"`, `value="staging"`},
			errorText: "Validation: destination slugs must use lowercase letters, digits, and hyphens only.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if handleAlertsCommonTestAPI(w, r, "admin") {
					return
				}
				if r.Method == http.MethodPut {
					t.Errorf("unexpected backend update request to %s", r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
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
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, tc.route, strings.NewReader(tc.form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			html := string(body)
			editFormHTML := htmlAfterMarker(t, html, tc.marker)

			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			require.Contains(t, html, tc.errorText)
			if tc.name == "DestinationEditForm" {
				require.Contains(t, editFormHTML, `aria-invalid="true"`)
				require.Contains(t, editFormHTML, `data-dirty-unsaved`)
				require.Contains(t, editFormHTML, `data-form-error-focus`)
			}
			for _, want := range tc.wantHTML {
				require.Contains(t, editFormHTML, want)
			}
		})
	}
}

func htmlAfterMarker(t *testing.T, html, marker string) string {
	t.Helper()

	index := strings.Index(html, marker)
	require.NotEqual(t, -1, index)
	return html[index:]
}

func TestRouter_HandleCreateAlertPolicySubmit_RejectsInvalidSourceRefSlug(t *testing.T) {
	t.Parallel()

	mutationCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAlertsCommonTestAPI(w, r, "admin") {
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/alert-policies" {
			mutationCalled = true
		}
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
	form := strings.NewReader("name=Critical+pipeline+failures&slug=critical-pipeline-failures&scope=organization&notification_type=pipeline.run_failed&severity=critical&lifecycle_state=open&source_kind=pipeline_run&source_ref=Daily+Ledger&destination_slug=ops-email")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/alerts/policies", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, html, "Alert policy could not be created.")
	require.Contains(t, html, "Validation: source ref slug must use lowercase letters, digits, and hyphens only.")
	require.False(t, mutationCalled)
}

// handleAlertsCommonTestAPI serves shared organization and alert fixtures for alert page tests.
func handleAlertsCommonTestAPI(w http.ResponseWriter, r *http.Request, currentRole string) bool {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/notification-connections":
		writeJSON(w, `[]`)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
		writeJSON(
			w,
			`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"`+currentRole+`","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
		writeJSON(
			w,
			`{"projects":[{"name":"Ops","slug":"ops","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/ops/environments":
		writeJSON(
			w,
			`{"project_environments":[{"environment_name":"Production","environment_slug":"prod","environment_description":"Production runtime","environment_protected":true,"is_default":true,"enabled_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/alert-policy-source-metadata":
		sourceRefs := `[]`
		if strings.TrimSpace(r.URL.Query().Get("source_kind")) != "" {
			sourceRefs = `[{"value":"daily-ledger","label":"Daily ledger"}]`
		}
		writeJSON(
			w,
			`{"sources":[{"source_kind":"pipeline_run","source_actions":["execute"]},{"source_kind":"project","source_actions":["create","update","delete"]}],"source_refs":`+sourceRefs+`}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/admin":
		writeJSON(
			w,
			`{"slug":"admin","name":"Admin","permissions":[{"resource":"*","action":"*"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/viewer":
		writeJSON(
			w,
			`{"slug":"viewer","name":"Viewer","permissions":[{"resource":"notifications","action":"read"},{"resource":"alert_policies","action":"read"},{"resource":"alert_destinations","action":"read"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/alert-destinations":
		writeJSON(
			w,
			`{"alert_destinations":[{"slug":"ops-email","name":"Ops email","type":"email","non_secret_config":{"recipients":["ops@example.com"]},"enabled":true,"created_at":"2026-06-30T10:00:00Z","updated_at":"2026-06-30T10:00:00Z"},{"slug":"data-team-email","name":"Data team email","type":"email","non_secret_config":{"recipients":["data@example.com"]},"enabled":false,"created_at":"2026-06-30T10:10:00Z","updated_at":"2026-06-30T10:10:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/alert-policies":
		writeJSON(
			w,
			`{"alert_policies":[{"slug":"critical-pipeline-failures","name":"Critical pipeline failures","scope":"project_environment","project_slug":"ops","environment_slug":"prod","notification_type":"pipeline.run_failed","severity":"critical","lifecycle_state":"open","destination_slugs":["ops-email","data-team-email"],"enabled":true,"created_at":"2026-06-30T10:20:00Z","updated_at":"2026-06-30T10:20:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
		)
		return true
	default:
		return false
	}
}

func TestAlertsPageReportsNotificationConnectionFailures(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/organizations/acme-hq/notification-connections" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = io.WriteString(w, "invalid-json")
					} else {
						_, _ = io.WriteString(w, `{}`)
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
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orgs/acme-hq/alerts", nil)
			request.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			response, err := app.Test(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			if status == http.StatusUnauthorized {
				require.Equal(t, http.StatusSeeOther, response.StatusCode)
				require.Contains(t, response.Header.Get("Location"), "/auth/login?return_to=")
				return
			}
			wantStatus := status
			if status == http.StatusOK {
				wantStatus = http.StatusServiceUnavailable
			}
			require.Equal(t, wantStatus, response.StatusCode)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Contains(t, string(body), "Notification connections are temporarily unavailable.")
			require.NotContains(t, string(body), "invalid-json")
		})
	}
}

func TestAlertCreateOptionalIdentifiers(t *testing.T) {
	for _, value := range []string{"", "  ", "custom-id", "INVALID_ID"} {
		t.Run(value, func(t *testing.T) {
			destination, destinationError := alertCreateDestinationPayloadFromForm(views.AlertDestinationFormData{Name: "Ops", Slug: value, Type: "email", To: "ops@example.com"})
			policy, policyError := alertCreatePolicyPayloadFromForm(views.AlertPolicyFormData{Name: "Failures", Slug: value, Scope: "organization", DestinationSlugs: []string{"ops"}})
			if value == "INVALID_ID" {
				require.Contains(t, destinationError, "slug")
				require.Contains(t, policyError, "slug")
				return
			}
			require.Empty(t, destinationError)
			require.Empty(t, policyError)
			for _, payload := range []any{destination, policy} {
				raw, err := json.Marshal(payload)
				require.NoError(t, err)
				var fields map[string]any
				require.NoError(t, json.Unmarshal(raw, &fields))
				if value == "custom-id" {
					require.Equal(t, value, fields["slug"])
				} else {
					require.NotContains(t, fields, "slug")
				}
			}
		})
	}
}
