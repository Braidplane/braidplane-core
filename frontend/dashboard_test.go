package frontend

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
)

func TestRouter_LoadDashboardPipelineData_UsesUnavailableDeploymentMetrics(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations/acme/projects/default/pipelines":
			writeJSON(w, `{"pipelines":[{"name":"Sync customers","slug":"sync-customers","has_draft":false,"sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/environments/dev/pipeline-deployments":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"DEPLOYMENTS_UNAVAILABLE","message":"deployment lookup failed"}}`))
		default:
			t.Fatalf("unexpected API request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	data := views.DashboardData{}
	unauthorized := router.loadDashboardPipelineData(
		t.Context(),
		"",
		views.AuthenticatedShellData{
			ActiveOrganizationSlug: "acme",
			ActiveProjectSlug:      "default",
			ActiveEnvironmentSlug:  "dev",
		},
		&data,
	)

	require.False(t, unauthorized)
	require.True(t, data.PipelineStatus.DeploymentsUnavailable)
	require.Equal(t, "deployment lookup failed", data.PipelineStatus.Message)
	requireDashboardMetric(t, data.PipelineStatus.Metrics, "Deployed here", "N/A", "neutral")
	requireDashboardMetric(t, data.PipelineStatus.Metrics, "Not deployed", "N/A", "neutral")
	requireDashboardMetric(t, data.PipelineStatus.Metrics, "Pipelines", "1", "success")
}

func TestRouter_LoadDashboardRunData_UsesScopedSucceededRunEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		succeededStatus     int
		succeededRuns       string
		recentRuns          string
		wantSuccess         bool
		wantFilteredRequest bool
	}{
		{
			name:                "failed only",
			recentRuns:          `{"runs":[{"run_slug":"failed-run","status":"failed"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			succeededRuns:       `{"runs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			wantFilteredRequest: true,
		},
		{
			name:                "successful run",
			recentRuns:          `{"runs":[{"run_slug":"failed-run","status":"failed"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			succeededRuns:       `{"runs":[{"run_slug":"successful-run","status":"succeeded"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			wantSuccess:         true,
			wantFilteredRequest: true,
		},
		{
			name:                "success evidence unavailable",
			succeededStatus:     http.StatusServiceUnavailable,
			recentRuns:          `{"runs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			succeededRuns:       `{"error":{"message":"runs unavailable"}}`,
			wantFilteredRequest: true,
		},
		{
			name:                "older success found by status filter",
			recentRuns:          `{"runs":[{"run_slug":"recent-failure","status":"failed"}],"has_more":true,"next_cursor":"next","prev_cursor":null}`,
			succeededRuns:       `{"runs":[{"run_slug":"older-success","status":"succeeded"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			wantSuccess:         true,
			wantFilteredRequest: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sawSucceededFilter bool
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "session=present", r.Header.Get("Cookie"))
				assert.Equal(t, "/api/organizations/acme/projects/default/environments/dev/runs", r.URL.Path)
				query := r.URL.Query()
				assert.Equal(t, "run_created_at", query.Get("sort_by"))
				assert.Equal(t, "desc", query.Get("order"))
				switch query.Get("status") {
				case "succeeded":
					sawSucceededFilter = true
					assert.Equal(t, "1", query.Get("limit"))
					if tt.succeededStatus != 0 {
						w.WriteHeader(tt.succeededStatus)
						_, _ = w.Write([]byte(tt.succeededRuns))
						return
					}
					writeJSON(w, tt.succeededRuns)
				case "failed":
					writeJSON(w, `{"runs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				default:
					writeJSON(w, tt.recentRuns)
				}
			}))
			defer apiServer.Close()

			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			data := views.DashboardData{}
			unauthorized := router.loadDashboardRunData(
				t.Context(),
				"session=present",
				views.AuthenticatedShellData{
					ActiveOrganizationSlug: "acme",
					ActiveProjectSlug:      "default",
					ActiveEnvironmentSlug:  "dev",
				},
				&data,
			)

			require.False(t, unauthorized)
			require.Equal(t, tt.wantSuccess, data.Onboarding.HasSuccessfulPipelineRun)
			require.Equal(t, tt.wantFilteredRequest, sawSucceededFilter)
		})
	}
}

func TestRouter_LoadDashboardFirstResultUsesRecordedMetadata(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		status           int
		body             string
		wantResult       bool
		wantUnavailable  bool
		wantUnauthorized bool
	}{
		{"successful result", http.StatusOK, `{"successful_execution":{"execution_slug":"exec-one","data_source_slug":"warehouse","query_slug":"read-users","executed_at":"2026-10-20T12:00:00Z"}}`, true, false, false},
		{"no recorded result", http.StatusOK, `{"successful_execution":null}`, false, false, false},
		{"unavailable", http.StatusServiceUnavailable, `{"error":{"message":"unavailable"}}`, false, true, false},
		{"unauthorized", http.StatusUnauthorized, `{"error":{"message":"unauthorized"}}`, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/organizations/acme/query-execution-summary", r.URL.Path)
				assert.Equal(t, "session=present", r.Header.Get("Cookie"))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			data := views.DashboardData{}
			unauthorized := router.loadDashboardFirstResultData(t.Context(), "session=present", views.AuthenticatedShellData{ActiveOrganizationSlug: "acme"}, &data)
			require.Equal(t, tc.wantUnauthorized, unauthorized)
			require.Equal(t, tc.wantUnavailable, data.Onboarding.FirstResultUnavailable)
			require.Equal(t, tc.wantResult, data.Onboarding.HasSuccessfulResult)
			if tc.wantResult {
				require.Equal(t, "/orgs/acme/data-sources/warehouse/queries/read-users", data.Onboarding.QueriesHref)
			}
		})
	}
}

func TestRouter_LoadDashboardData_LoadsPanelsConcurrently(t *testing.T) {
	t.Parallel()

	gate := newDashboardPanelGate()
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects":
			writeJSON(w, `{"projects":[{"name":"Default","slug":"default","pipeline_backend":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/query-execution-summary":
			writeJSON(w, `{"successful_execution":null}`)
		case "/api/organizations/acme/roles/admin":
			writeJSON(w, `{"slug":"admin","name":"Admin","permissions":[{"resource":"*","action":"*"}]}`)
		case "/api/organizations/acme/projects/default/environments":
			writeJSON(w, `{"project_environments":[{"environment_name":"Development","environment_slug":"dev","environment_description":null,"environment_protected":false,"is_default":true,"enabled_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/environments/dev/runs":
			gate.enter(r.Context())
			writeJSON(w, `{"runs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/pipelines":
			gate.enter(r.Context())
			writeJSON(w, `{"pipelines":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/data-sources":
			gate.enter(r.Context())
			writeJSON(w, `{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/data-source-refs":
			gate.enter(r.Context())
			writeJSON(w, `{"data_source_refs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/environments/dev/pipeline-deployments":
			writeJSON(w, `{"deployments":[]}`)
		case "/api/organizations/acme/projects/default/environments/dev/data-source-bindings":
			writeJSON(w, `{"data_source_bindings":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		default:
			t.Fatalf("unexpected API request %s %s", r.Method, r.URL.Path)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard?project_slug=default&environment_slug=dev", nil)
	req.Header.Set("Cookie", authCookie)

	type dashboardResponse struct {
		status int
		err    error
	}
	result := make(chan dashboardResponse, 1)
	go func() {
		resp, err := app.Test(req, fiber.TestConfig{Timeout: 3 * time.Second, FailOnTimeout: true})
		status := 0
		if resp != nil {
			status = resp.StatusCode
			_ = resp.Body.Close()
		}
		result <- dashboardResponse{status: status, err: err}
	}()

	requireDashboardGateArrival(t, gate)
	secondPanelStarted := false
	select {
	case <-gate.arrivals:
		secondPanelStarted = true
	case <-time.After(200 * time.Millisecond):
	}
	close(gate.release)

	got := <-result
	require.NoError(t, got.err)
	require.Equal(t, http.StatusOK, got.status)
	require.True(t, secondPanelStarted, "expected a second panel API request to start before the first was released")
	require.GreaterOrEqual(t, gate.max.Load(), int32(2))
}

func TestRouter_FilterDataSourcesForList_LoadsQueryInventoryConcurrently(t *testing.T) {
	t.Parallel()

	gate := newDashboardPanelGate()
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations/acme/data-sources/with-queries/queries":
			gate.enter(r.Context())
			writeJSON(w, `{"queries":[{}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/data-sources/without-queries/queries":
			gate.enter(r.Context())
			writeJSON(w, `{"queries":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		default:
			t.Fatalf("unexpected API request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	dataSources := []api.DataSource{
		{Slug: "with-queries", Name: "With queries"},
		{Slug: "without-queries", Name: "Without queries"},
	}

	type filterResult struct {
		dataSources []api.DataSource
		status      int
		err         error
	}
	result := make(chan filterResult, 1)
	go func() {
		filtered, status, _, err := router.filterDataSourcesForList(
			t.Context(),
			"",
			"acme",
			dataSources,
			views.DataSourceListFilterWithQueries,
		)
		result <- filterResult{dataSources: filtered, status: status, err: err}
	}()

	requireDashboardGateArrival(t, gate)
	secondLookupStarted := false
	select {
	case <-gate.arrivals:
		secondLookupStarted = true
	case <-time.After(200 * time.Millisecond):
	}
	close(gate.release)

	got := <-result
	require.NoError(t, got.err)
	require.Equal(t, http.StatusOK, got.status)
	require.Len(t, got.dataSources, 1)
	require.Equal(t, "with-queries", got.dataSources[0].Slug)
	require.True(t, secondLookupStarted, "expected both data-source query lookups to start before release")
	require.Equal(t, int32(2), gate.max.Load())
}

func TestRouter_HandleDashboardPage_EnablesProjectRuntimeNavFromDefaultScope(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects":
			writeJSON(w, `{"projects":[{"name":"Default","slug":"default","pipeline_backend":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/query-execution-summary":
			writeJSON(w, `{"successful_execution":null}`)
		case "/api/organizations/acme/roles/admin":
			writeJSON(w, `{"slug":"admin","name":"Admin","permissions":[{"resource":"*","action":"*"}]}`)
		case "/api/organizations/acme/projects/default/environments":
			writeJSON(w, `{"project_environments":[{"environment_name":"Development","environment_slug":"dev","environment_description":null,"environment_protected":false,"is_default":true,"enabled_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/environments/dev/runs":
			writeJSON(w, `{"runs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/pipelines":
			writeJSON(w, `{"pipelines":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/environments/dev/pipeline-deployments":
			writeJSON(w, `{"deployments":[]}`)
		case "/api/organizations/acme/data-sources":
			writeJSON(w, `{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/data-source-refs":
			writeJSON(w, `{"data_source_refs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/environments/dev/data-source-bindings":
			writeJSON(w, `{"data_source_bindings":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		default:
			t.Fatalf("unexpected API request %s %s", r.Method, r.URL.Path)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", nil)
	req.Header.Set("Cookie", authCookie)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	body := string(bodyBytes)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `href="/orgs/acme/projects/default/pipelines?environment_slug=dev"`)
	require.Contains(t, body, `href="/orgs/acme/projects/default/schedules?environment_slug=dev"`)
	require.Contains(t, body, `href="/orgs/acme/projects/default/runs?environment_slug=dev"`)
	require.NotContains(t, body, `title="Pipelines requires an active project"`)
	require.NotContains(t, body, `title="Schedules requires an active project"`)
	require.NotContains(t, body, `title="Runs requires an active project"`)
}

func TestRouter_HandleDashboardPage_FallsBackFromStaleRememberedProjectScope(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects":
			writeJSON(w, `{"projects":[{"name":"Default","slug":"default","pipeline_backend":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/query-execution-summary":
			writeJSON(w, `{"successful_execution":null}`)
		case "/api/organizations/acme/roles/admin":
			writeJSON(w, `{"slug":"admin","name":"Admin","permissions":[{"resource":"*","action":"*"}]}`)
		case "/api/organizations/acme/projects/default/environments":
			writeJSON(w, `{"project_environments":[{"environment_name":"Development","environment_slug":"dev","environment_description":null,"environment_protected":false,"is_default":true,"enabled_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/environments/dev/runs":
			writeJSON(w, `{"runs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/pipelines":
			writeJSON(w, `{"pipelines":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/environments/dev/pipeline-deployments":
			writeJSON(w, `{"deployments":[]}`)
		case "/api/organizations/acme/data-sources":
			writeJSON(w, `{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/data-source-refs":
			writeJSON(w, `{"data_source_refs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme/projects/default/environments/dev/data-source-bindings":
			writeJSON(w, `{"data_source_bindings":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		default:
			t.Fatalf("unexpected API request %s %s", r.Method, r.URL.Path)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", nil)
	req.Header.Set("Cookie", strings.Join([]string{
		authCookie,
		dashboardScopeProjectCookieName + "=acme:retired-project",
		dashboardScopeEnvironmentCookieName + "=acme:retired-project:retired-env",
	}, "; "))

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	body := string(bodyBytes)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `href="/orgs/acme/projects/default/pipelines?environment_slug=dev"`)
	require.Contains(t, body, `href="/orgs/acme/projects/default/schedules?environment_slug=dev"`)
	require.Contains(t, body, `href="/orgs/acme/projects/default/runs?environment_slug=dev"`)
	require.NotContains(t, body, "Selected project is not available in this organization.")

	foundProjectScopeCookie := false
	foundEnvironmentScopeCookie := false
	for _, cookie := range resp.Cookies() {
		switch cookie.Name {
		case dashboardScopeProjectCookieName:
			foundProjectScopeCookie = foundProjectScopeCookie || cookie.Value == "acme:default"
		case dashboardScopeEnvironmentCookieName:
			foundEnvironmentScopeCookie = foundEnvironmentScopeCookie || cookie.Value == "acme:default:dev"
		}
	}
	require.True(t, foundProjectScopeCookie)
	require.True(t, foundEnvironmentScopeCookie)

	explicitReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard?project_slug=retired-project", nil)
	explicitReq.Header.Set("Cookie", authCookie)

	explicitResp, err := app.Test(explicitReq)
	require.NoError(t, err)
	defer func() { _ = explicitResp.Body.Close() }()
	explicitBodyBytes, err := io.ReadAll(explicitResp.Body)
	require.NoError(t, err)
	explicitBody := string(explicitBodyBytes)

	require.Equal(t, http.StatusOK, explicitResp.StatusCode)
	require.Contains(t, explicitBody, `title="Pipelines requires an active project"`)
	require.Contains(t, explicitBody, `title="Schedules requires an active project"`)
	require.Contains(t, explicitBody, `title="Runs requires an active project"`)
	require.NotContains(t, explicitBody, `href="/orgs/acme/projects/default/pipelines?environment_slug=dev"`)

	emptyReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard?project_slug=", nil)
	emptyReq.Header.Set("Cookie", authCookie)
	emptyResp, err := app.Test(emptyReq)
	require.NoError(t, err)
	defer func() { _ = emptyResp.Body.Close() }()
	emptyBody, err := io.ReadAll(emptyResp.Body)
	require.NoError(t, err)
	require.Contains(t, string(emptyBody), `<option selected value="">No project selected</option>`)
	require.Contains(t, string(emptyBody), `aria-label="Dashboard runtime target unavailable"`)
	require.Contains(t, string(emptyBody), `title="Pipelines requires an active project"`)
}

func requireDashboardMetric(t *testing.T, metrics []views.DashboardMetric, label, value, kind string) {
	t.Helper()

	for _, metric := range metrics {
		if metric.Label == label {
			require.Equal(t, value, metric.Value)
			require.Equal(t, kind, metric.Kind)
			return
		}
	}
	require.Failf(t, "missing dashboard metric", "label %q was not found", label)
}

type dashboardPanelGate struct {
	active   atomic.Int32
	max      atomic.Int32
	arrivals chan struct{}
	release  chan struct{}
}

func newDashboardPanelGate() *dashboardPanelGate {
	return &dashboardPanelGate{
		arrivals: make(chan struct{}, 8),
		release:  make(chan struct{}),
	}
}

func (g *dashboardPanelGate) enter(ctx context.Context) {
	active := g.active.Add(1)
	for {
		maxActive := g.max.Load()
		if active <= maxActive || g.max.CompareAndSwap(maxActive, active) {
			break
		}
	}
	select {
	case g.arrivals <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
	g.active.Add(-1)
}

func requireDashboardGateArrival(t *testing.T, gate *dashboardPanelGate) {
	t.Helper()

	select {
	case <-gate.arrivals:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for dashboard API request")
	}
}
