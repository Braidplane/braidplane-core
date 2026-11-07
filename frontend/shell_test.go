package frontend

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
)

func TestShellPreservesValidatedProjectAndRuntimeScope(t *testing.T) {
	t.Parallel()
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/organizations/acme/projects":
			writeJSON(w, `{"projects":[{"slug":"first","name":"First"},{"slug":"selected","name":"Selected"}],"has_more":false}`)
		case strings.HasSuffix(r.URL.Path, "/environments"):
			writeJSON(w, `{"project_environments":[{"environment_slug":"dev","is_default":true,"environment":{"slug":"dev","name":"Development"}},{"environment_slug":"stg","environment":{"slug":"stg","name":"Staging"}}]}`)
		default:
			t.Errorf("unexpected API request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	for _, tc := range []struct {
		name, projectCookie, environmentCookie, explicitProject, query, wantProject, wantEnvironment string
	}{
		{"remembered", "acme:selected", "acme:selected:stg", "", "", "selected", "stg"},
		{"other tenant", "other:selected", "other:selected:stg", "", "", "first", "dev"},
		{"deleted project", "acme:deleted", "acme:deleted:stg", "", "", "first", "dev"},
		{"explicit project", "acme:selected", "acme:selected:stg", "first", "", "first", "dev"},
		{"explicit target", "acme:selected", "acme:selected:stg", "", "?environment_slug=dev", "selected", "dev"},
		{"unavailable target", "acme:selected", "acme:selected:deleted", "", "", "selected", "dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New()
			app.Get("/catalog", func(c fiber.Ctx) error {
				shell := router.withProjectShellData(c, views.AuthenticatedShellData{ActiveOrganizationSlug: "acme"}, "", tc.explicitProject)
				require.Equal(t, tc.wantProject, shell.ActiveProjectSlug)
				require.Equal(t, tc.wantEnvironment, shell.ActiveEnvironmentSlug)
				return c.SendStatus(http.StatusOK)
			})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/catalog"+tc.query, nil)
			req.AddCookie(&http.Cookie{Name: dashboardScopeProjectCookieName, Value: tc.projectCookie})
			req.AddCookie(&http.Cookie{Name: dashboardScopeEnvironmentCookieName, Value: tc.environmentCookie})
			resp, err := app.Test(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			var sharedProject, expiredLegacyProject bool
			for _, cookie := range resp.Cookies() {
				if cookie.Name == dashboardScopeProjectCookieName && cookie.Path == "/dashboard" && cookie.MaxAge < 0 {
					expiredLegacyProject = true
				}
				if cookie.Name == dashboardScopeProjectCookieName && cookie.Path == "/" {
					sharedProject = true
					require.Equal(t, "acme:"+tc.wantProject, cookie.Value)
					require.True(t, cookie.HttpOnly)
				}
			}
			require.True(t, sharedProject, "scope must reach organization-level catalogs")
			require.True(t, expiredLegacyProject, "older dashboard-only scope must not shadow a new selection")
			app.Get("/orgs/:slug/projects/:projectSlug/runs", func(c fiber.Ctx) error {
				environment, _, err := router.resolveRuntimeEnvironmentSlug(c)
				require.NoError(t, err)
				require.Equal(t, tc.wantEnvironment, environment.String(), "page data and shell target must agree")
				return c.SendStatus(http.StatusOK)
			})
			app.Get("/environments/:environmentSlug", func(c fiber.Ctx) error {
				require.Equal(t, "dev", router.dashboardRequestedEnvironmentSlug(c, "acme", "selected"))
				return c.SendStatus(http.StatusOK)
			})
			pathRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/environments/dev", nil)
			pathRequest.AddCookie(&http.Cookie{Name: dashboardScopeEnvironmentCookieName, Value: "acme:selected:stg"})
			pathResponse, err := app.Test(pathRequest)
			require.NoError(t, err)
			require.NoError(t, pathResponse.Body.Close())
			req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orgs/acme/projects/"+tc.wantProject+"/runs"+tc.query, nil)
			req.AddCookie(&http.Cookie{Name: dashboardScopeEnvironmentCookieName, Value: tc.environmentCookie})
			resp, err = app.Test(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
		})
	}
}

func TestRouter_CanReadEventsForRoleUsesAuthoritativeEndpointForCustomRoles(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		role       string
		statusCode int
		want       bool
	}{
		{name: "Authorized", role: "events-reader", statusCode: http.StatusOK, want: true},
		{name: "Denied", role: "events-reader", statusCode: http.StatusForbidden, want: false},
		{name: "OwnerUsesAuthoritativeProbe", role: "owner", statusCode: http.StatusForbidden, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/organizations/acme-hq/events", r.URL.Path)
				assert.Equal(t, "1", r.URL.Query().Get("limit"))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.statusCode)
				if tc.statusCode == http.StatusOK {
					_, _ = w.Write([]byte(`{"events":[],"has_more":false}`))
				}
			}))
			defer apiServer.Close()

			router, err := NewRouter(
				apiServer.URL,
				slog.New(slog.DiscardHandler),
				session.NewStore(),
			)
			require.NoError(t, err)

			require.Equal(
				t,
				tc.want,
				router.canReadEventsForRole(context.Background(), "", "acme-hq", tc.role),
			)
		})
	}
}

func TestBuildProjectBreadcrumbsLinksProjectToPipelinesPage(t *testing.T) {
	t.Parallel()

	items := buildProjectBreadcrumbs(
		"/orgs/acme-hq/projects/data-migration/runs",
		"acme-hq",
		"Acme HQ",
		"data-migration",
		"Data Migration",
	)

	require.Len(t, items, 4)
	require.Equal(t, "Data Migration", items[2].Label)
	require.Equal(t, "/orgs/acme-hq/projects/data-migration/pipelines", items[2].Href)
	require.Equal(t, "Runs", items[3].Label)
	require.Empty(t, items[3].Href)
}

func TestBuildDashboardBreadcrumbs(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Builder", buildDashboardBreadcrumbs("/builder")[1].Label)
	require.Equal(t, "Organizations", buildDashboardBreadcrumbs("/organizations")[0].Label)

	createItems := buildDashboardBreadcrumbs("/organizations/new")
	require.Len(t, createItems, 2)
	require.Equal(t, "/organizations", createItems[0].Href)
	require.Equal(t, "Create organization", createItems[1].Label)

	securityItems := buildDashboardBreadcrumbs("/organizations/acme-hq/security")
	require.Len(t, securityItems, 3)
	require.Equal(t, "/organizations/acme-hq", securityItems[1].Href)
	require.Equal(t, "Security", securityItems[2].Label)

	require.Equal(t, "Dashboard", buildDashboardBreadcrumbs("/unknown")[0].Label)
}

func TestBuildOrganizationBreadcrumbs(t *testing.T) {
	t.Parallel()

	items := buildOrganizationBreadcrumbs("/orgs/acme-hq/data-sources", "acme-hq", "Acme HQ")
	require.Len(t, items, 3)
	require.Equal(t, "Organizations", items[0].Label)
	require.Equal(t, "/organizations", items[0].Href)
	require.Equal(t, "Acme HQ", items[1].Label)
	require.Equal(t, "/organizations/acme-hq", items[1].Href)
	require.Equal(t, "Data sources", items[2].Label)

	detailItems := buildOrganizationDetailBreadcrumbs(
		"Acme HQ",
		"acme-hq",
		"Queries",
		"/orgs/acme-hq/queries",
		"users-by-status",
	)
	require.Len(t, detailItems, 4)
	require.Equal(t, "Queries", detailItems[2].Label)
	require.Equal(t, "/orgs/acme-hq/queries", detailItems[2].Href)
	require.Equal(t, "users-by-status", detailItems[3].Label)
}

func TestBuildShellNavProjectScopedSections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		currentPath    string
		activeLabel    string
		expectedLabels map[string]struct {
			href    string
			enabled bool
		}
	}{
		{
			name:        "ProjectRunsKeepsProjectScopedSectionsEnabled",
			currentPath: "/orgs/acme-hq/projects/data-migration/runs",
			activeLabel: "Runs",
			expectedLabels: map[string]struct {
				href    string
				enabled bool
			}{
				"Projects":      {href: "/organizations/acme-hq/projects", enabled: true},
				"Pipelines":     {href: "/orgs/acme-hq/projects/data-migration/pipelines", enabled: true},
				"Schedules":     {href: "/orgs/acme-hq/projects/data-migration/schedules", enabled: true},
				"Runs":          {href: "/orgs/acme-hq/projects/data-migration/runs", enabled: true},
				"Notifications": {href: "/orgs/acme-hq/notifications", enabled: true},
				"Alerts":        {href: "/orgs/acme-hq/alerts", enabled: true},
			},
		},
		{
			name:        "NotificationsMarksNotificationsActive",
			currentPath: "/orgs/acme-hq/notifications",
			activeLabel: "Notifications",
		},
		{
			name:        "CreationHistoryMarksPipelinesActive",
			currentPath: "/orgs/acme-hq/projects/data-migration/pipeline-creations",
			activeLabel: "Pipelines",
		},
		{
			name:        "CreationProgressMarksPipelinesActive",
			currentPath: "/orgs/acme-hq/projects/data-migration/pipeline-creations/creation-one",
			activeLabel: "Pipelines",
		},
		{
			name:        "AlertsMarksAlertsActive",
			currentPath: "/orgs/acme-hq/alerts",
			activeLabel: "Alerts",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			items := buildShellNav(tc.currentPath, "acme-hq", "data-migration")
			byLabel := map[string]struct {
				href    string
				active  bool
				enabled bool
			}{}
			for _, item := range items {
				byLabel[item.Label] = struct {
					href    string
					active  bool
					enabled bool
				}{
					href:    item.Href,
					active:  item.IsActive,
					enabled: item.Enabled,
				}
			}

			for label, expected := range tc.expectedLabels {
				require.Equal(t, expected.href, byLabel[label].href)
				require.Equal(t, expected.enabled, byLabel[label].enabled)
			}
			require.True(t, byLabel[tc.activeLabel].active)
		})
	}
}

func TestBuildShellNavUsesDistinctIconKeysForEveryAuthenticatedDestination(t *testing.T) {
	t.Parallel()

	items := buildShellNav("/orgs/acme-hq/projects/data-migration/pipelines", "acme-hq", "data-migration")
	expectedLabels := []string{
		"Overview",
		"Projects",
		"Data Sources",
		"Queries",
		"Environments",
		"Pipelines",
		"Proof",
		"Schedules",
		"Runs",
		"Notifications",
		"Events",
		"Alerts",
		"Members",
		"Organization settings",
	}
	labels := make([]string, 0, len(items))
	labelsByIcon := map[string][]string{}
	missingIconLabels := []string{}

	for _, item := range items {
		labels = append(labels, item.Label)

		icon := strings.TrimSpace(item.Icon)
		if icon == "" {
			missingIconLabels = append(missingIconLabels, item.Label)
			continue
		}
		labelsByIcon[icon] = append(labelsByIcon[icon], item.Label)
	}

	duplicateIconAssignments := []string{}
	for icon, labelsForIcon := range labelsByIcon {
		if len(labelsForIcon) <= 1 {
			continue
		}
		sort.Strings(labelsForIcon)
		duplicateIconAssignments = append(
			duplicateIconAssignments,
			icon+": "+strings.Join(labelsForIcon, ", "),
		)
	}
	sort.Strings(duplicateIconAssignments)
	sort.Strings(missingIconLabels)

	require.ElementsMatch(t, expectedLabels, labels)
	require.Empty(t, missingIconLabels, "collapsed sidebar destinations need icon-only recognition")
	require.Empty(t, duplicateIconAssignments, "authenticated sidebar icons must be unique")
}

func TestBuildShellNavUsesConnectorSpecificDataSourcesIcon(t *testing.T) {
	t.Parallel()

	items := buildShellNav("/orgs/acme-hq/data-sources", "acme-hq", "data-migration")
	iconsByLabel := map[string]string{}
	for _, item := range items {
		iconsByLabel[item.Label] = strings.TrimSpace(item.Icon)
	}

	require.Equal(t, "data-source", iconsByLabel["Data Sources"])
	for _, label := range []string{"Queries", "Environments", "Pipelines", "Runs"} {
		require.NotEqual(t, iconsByLabel[label], iconsByLabel["Data Sources"])
	}
}

func TestAppendEnvironmentToProjectRuntimeNav(t *testing.T) {
	t.Parallel()

	items := appendEnvironmentToProjectRuntimeNav(
		buildShellNav("/orgs/acme-hq/projects/data-migration/runs", "acme-hq", "data-migration"),
		"prod",
	)
	byLabel := map[string]string{}
	for _, item := range items {
		byLabel[item.Label] = item.Href
	}

	require.Equal(t, "/orgs/acme-hq/projects/data-migration/pipelines?environment_slug=prod", byLabel["Pipelines"])
	require.Equal(t, "/orgs/acme-hq/projects/data-migration/schedules?environment_slug=prod", byLabel["Schedules"])
	require.Equal(t, "/orgs/acme-hq/projects/data-migration/runs?environment_slug=prod", byLabel["Runs"])
	require.Equal(t, "/organizations/acme-hq/environments?environment_slug=prod", byLabel["Environments"])
	require.NotContains(t, byLabel, "Activity")
}

func TestBuildShellNavDisablesProjectScopedSectionsWithoutProject(t *testing.T) {
	t.Parallel()

	items := buildShellNav("/organizations/acme-hq/projects", "acme-hq", "")
	byLabel := map[string]struct {
		href    string
		enabled bool
		active  bool
	}{}
	for _, item := range items {
		byLabel[item.Label] = struct {
			href    string
			enabled bool
			active  bool
		}{
			href:    item.Href,
			enabled: item.Enabled,
			active:  item.IsActive,
		}
	}

	require.Empty(t, byLabel["Pipelines"].href)
	require.False(t, byLabel["Pipelines"].enabled)
	require.Empty(t, byLabel["Schedules"].href)
	require.False(t, byLabel["Schedules"].enabled)
	require.Empty(t, byLabel["Runs"].href)
	require.False(t, byLabel["Runs"].enabled)
	require.Equal(t, "/organizations/acme-hq/environments", byLabel["Environments"].href)
	require.True(t, byLabel["Environments"].enabled)
	require.Equal(t, "/orgs/acme-hq/data-sources", byLabel["Data Sources"].href)
	require.True(t, byLabel["Data Sources"].enabled)

	detailItems := buildShellNav("/organizations/acme-hq/environments/prod", "acme-hq", "")
	detailByLabel := map[string]bool{}
	for _, item := range detailItems {
		detailByLabel[item.Label] = item.IsActive
	}
	require.True(t, detailByLabel["Environments"])
}

func TestEnvironmentSwitcherHrefDropsPaginationState(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"/orgs/acme-hq/projects/ops/runs?environment_slug=prod&pipeline_slug=users-sync&status=failed",
		environmentSwitcherHref(
			"/orgs/acme-hq/projects/ops/runs?cursor=page-2&direction=next&environment_slug=dev&pipeline_slug=users-sync&status=failed",
			"prod",
		),
	)
}

func TestProofReceiptEnvironmentSwitchReturnsToScopedHistory(t *testing.T) {
	t.Parallel()
	require.Equal(t, "/orgs/acme/projects/ops/proof-receipts?environment_slug=stage", environmentSwitcherHref("/orgs/acme/projects/ops/environments/prod/proof-receipts/receipt-one?cursor=old&direction=next", "stage"))
	items := appendEnvironmentToProjectRuntimeNav(buildShellNav("/orgs/acme/projects/ops/environments/prod/proof-receipts/receipt-one", "acme", "ops"), "prod")
	for _, item := range items {
		if item.Label == "Proof" {
			require.True(t, item.IsActive)
			require.True(t, item.Enabled)
			require.Equal(t, "/orgs/acme/projects/ops/proof-receipts?environment_slug=prod", item.Href)
			return
		}
	}
	t.Fatal("Proof navigation missing")
}

func TestEnvironmentSwitcherHrefUsesBuilderTargetEnvironmentParam(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"/builder?org_slug=acme-hq&project_slug=ops&target_environment_slug=prod",
		environmentSwitcherHref(
			"/builder?org_slug=acme-hq&project_slug=ops&target_environment_slug=dev&environment_slug=dev&cursor=page-2",
			"prod",
		),
	)
}

func TestEnvironmentSwitcherHrefClearsBuilderTargetParamOnProjectPages(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"/orgs/acme-hq/projects/ops/runs?environment_slug=prod",
		environmentSwitcherHref(
			"/orgs/acme-hq/projects/ops/runs?target_environment_slug=dev",
			"prod",
		),
	)
}

func TestEnvironmentSwitcherRequestURLReturnsGETPageForPipelineActions(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"/orgs/acme-hq/projects/ops/pipelines/users-sync",
		environmentSwitcherRequestURL(
			"POST",
			"/orgs/acme-hq/projects/ops/pipelines/users-sync/draft/delete?environment_slug=dev",
		),
	)
	require.Equal(
		t,
		"/orgs/acme-hq/projects/ops/pipelines",
		environmentSwitcherRequestURL(
			"POST",
			"/orgs/acme-hq/projects/ops/pipelines/save?environment_slug=dev",
		),
	)
}

func TestProjectScopedGETPathForActionPreservesDeploymentDetail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "DeploymentDetail",
			path: "/orgs/acme-hq/projects/ops/pipelines/users-sync/deployment",
			want: "/orgs/acme-hq/projects/ops/pipelines/users-sync/deployment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := projectScopedGETPathForAction(tt.path)

			require.NotEmpty(t, got)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEnvironmentSwitcherRequestURLReturnsRunsListForRunDetail(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"/orgs/acme-hq/projects/ops/runs",
		environmentSwitcherRequestURL(
			"GET",
			"/orgs/acme-hq/projects/ops/runs/run-001?environment_slug=dev",
		),
	)
}

func TestProjectEnvironmentSlugIsEnabled(t *testing.T) {
	t.Parallel()

	projectEnvironments := []api.ProjectEnvironment{
		{EnvironmentSlug: "dev"},
		{EnvironmentSlug: "prod"},
	}

	require.True(t, projectEnvironmentSlugIsEnabled(projectEnvironments, "prod"))
	require.False(t, projectEnvironmentSlugIsEnabled(projectEnvironments, "qa"))
	require.False(t, projectEnvironmentSlugIsEnabled(projectEnvironments, "not valid!"))
	require.False(t, projectEnvironmentSlugIsEnabled(projectEnvironments, ""))
}

func TestBuildEnvironmentSwitcherStateUsesSharedOptionFields(t *testing.T) {
	t.Parallel()

	projectEnvironments := []api.ProjectEnvironment{
		{
			EnvironmentSlug:      "prod",
			EnvironmentName:      "Production",
			EnvironmentColor:     api.EnvironmentColorRed,
			EnvironmentProtected: true,
			IsDefault:            true,
		},
	}

	state := buildEnvironmentSwitcherState(projectEnvironments, " prod ", func(environmentSlug string) string {
		return "/targets/" + environmentSlug
	})

	require.Equal(t, "prod", state.ActiveSlug)
	require.Equal(t, "Production", state.ActiveName)
	require.Equal(t, "red", state.ActiveColor)
	require.True(t, state.ActiveProtected)
	require.Len(t, state.DashboardOptions, 1)
	require.Len(t, state.ShellOptions, 1)

	dashboardOption := state.DashboardOptions[0]
	shellOption := state.ShellOptions[0]
	require.Equal(t, dashboardOption.Slug, shellOption.Slug)
	require.Equal(t, dashboardOption.Name, shellOption.Name)
	require.Equal(t, dashboardOption.Color, shellOption.Color)
	require.Equal(t, dashboardOption.Protected, shellOption.Protected)
	require.Equal(t, dashboardOption.Href, shellOption.Href)
	require.Equal(t, dashboardOption.IsActive, shellOption.IsActive)
	require.Equal(t, dashboardOption.IsDefault, shellOption.IsDefault)
}

func TestDocumentationURLIsIncludedInShellPresentation(t *testing.T) {
	r := &Router{documentationURL: "https://docs.example.test/"}
	data := r.withExternalAccessShellData(views.AuthenticatedShellData{})
	require.Equal(t, "https://docs.example.test/", data.DocumentationURL)
}

func TestShellTaskNavigationKeepsDetailAndBuilderContext(t *testing.T) {
	t.Parallel()
	for path, active := range map[string]string{
		"/builder":                                     "Pipelines",
		"/organizations/acme/projects/demo":            "Projects",
		"/orgs/acme/data-sources/source":               "Data Sources",
		"/orgs/acme/data-sources/pipeline-creations":   "Data Sources",
		"/orgs/acme/data-sources/source/queries/query": "Queries",
		"/organizations/acme/members":                  "Members",
	} {
		items := buildShellNav(path, "acme", "demo")
		for _, item := range items {
			require.NotEqual(t, "Builder", item.Label)
			if item.IsActive {
				require.Equal(t, active, item.Label)
			}
			if item.Label == active {
				require.True(t, item.IsActive)
				require.NotEmpty(t, item.Group)
			}
		}
	}
}

func TestShellNavigationWithoutTenantKeepsOrganizationPageTitle(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/organizations", "/organizations/new"} {
		items := buildShellNav(path, "", "")
		require.Equal(t, "Organizations", views.ShellTopbarTitle(path, items, "Organizations"))
	}
}

func TestMembersNavigationUsesTheActiveTenant(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path, org string
		active    bool
	}{
		{path: "/organizations/acme/members", org: "acme", active: true},
		{path: "/orgs/acme/members", org: "acme", active: true},
		{path: "/organizations/other/members", org: "acme"},
		{path: "/organizations/acme/members"},
	} {
		for _, item := range buildShellNav(tc.path, tc.org, "") {
			if item.Label == "Members" {
				require.Equal(t, tc.active, item.IsActive)
			}
		}
	}
}

func TestRecencyEnvironmentSwitchReplacesPathTarget(t *testing.T) {
	require.Equal(t, "/orgs/acme/projects/ops/pipelines/orders/environments/stage/recency", environmentSwitcherHref("/orgs/acme/projects/ops/pipelines/orders/environments/dev/recency?environment_slug=dev&cursor=old&direction=next", "stage"))
}
