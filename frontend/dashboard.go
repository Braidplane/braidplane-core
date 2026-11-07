package frontend

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"golang.org/x/sync/errgroup"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	dashboardRecentRunLimit             api.LimitParam = 20
	dashboardRecentActivityLimit                       = 5
	dashboardFailedRunLimit             api.LimitParam = 5
	dashboardIssueLimit                                = 4
	dashboardNoActiveProjectMessage                    = "Choose a project to view project operations."
	dashboardNoRuntimeTargetMessage                    = "No runtime target is enabled for this project."
	dashboardScopeCookieMaxAge                         = 60 * 60 * 24 * 30
	dashboardScopeCookiePath                           = "/"
	dashboardScopeProjectCookieName                    = "braidplane_dashboard_project_scope"
	dashboardScopeEnvironmentCookieName                = "braidplane_dashboard_environment_scope"
	dashboardQueryLookupConcurrency                    = 4
)

var errDashboardUnauthorized = xerror.New(
	xerror.KindUnauthorized,
	"DASHBOARD_UNAUTHORIZED",
	"dashboard data unauthorized",
)

// loadDashboardData builds the command-center aggregate data from existing backend APIs.
func (r *Router) loadDashboardData(c fiber.Ctx, shellData views.AuthenticatedShellData) (views.DashboardData, views.AuthenticatedShellData, bool) {
	data := views.DashboardData{}
	ctx := c.Context()
	cookieHeader := c.Get(fiber.HeaderCookie)
	scopedShellData, unauthorized := r.loadDashboardProjectScope(ctx, cookieHeader, c, shellData, &data)
	if unauthorized {
		return data, scopedShellData, true
	}
	data.Permissions = r.resolveOrganizationPermissions(ctx, cookieHeader, scopedShellData.ActiveOrganizationSlug)
	data.Onboarding.ProjectsHref = views.DashboardProjectsHref(scopedShellData)
	data.Onboarding.ProjectSetupHref = views.DashboardProjectSetupHref(scopedShellData)
	data.Onboarding.DataSourcesHref = views.DashboardDataSourcesHref(scopedShellData)
	data.Onboarding.QueriesHref = views.DashboardQueriesHref(scopedShellData)
	data.Onboarding.PipelinesHref = views.DashboardPipelinesHref(scopedShellData)
	data.Onboarding.TutorialHref = views.DashboardFirstResultTutorialHref(scopedShellData)

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return dashboardPanelLoadError(r.loadDashboardRunData(groupCtx, cookieHeader, scopedShellData, &data))
	})
	group.Go(func() error {
		return dashboardPanelLoadError(r.loadDashboardPipelineData(groupCtx, cookieHeader, scopedShellData, &data))
	})
	group.Go(func() error {
		return dashboardPanelLoadError(r.loadDashboardInventoryData(groupCtx, cookieHeader, shellData, &data))
	})
	group.Go(func() error {
		return dashboardPanelLoadError(r.loadDashboardContractData(groupCtx, cookieHeader, scopedShellData, &data))
	})

	if err := group.Wait(); err != nil {
		if xerror.Is(err, errDashboardUnauthorized) {
			return data, scopedShellData, true
		}
		return data, scopedShellData, false
	}

	return data, scopedShellData, false
}

// dashboardPanelLoadError preserves the dashboard handler's existing unauthorized short-circuit contract.
func dashboardPanelLoadError(unauthorized bool) error {
	if unauthorized {
		return errDashboardUnauthorized
	}
	return nil
}

// loadDashboardProjectScope resolves the dashboard-local project and runtime target scope.
func (r *Router) loadDashboardProjectScope(
	ctx context.Context,
	cookieHeader string,
	c fiber.Ctx,
	shellData views.AuthenticatedShellData,
	data *views.DashboardData,
) (views.AuthenticatedShellData, bool) {
	scopedShellData := shellData

	if strings.TrimSpace(shellData.ActiveOrganizationSlug) == "" {
		data.ProjectScope.Message = "No active organization is selected."
		return scopedShellData, false
	}

	projectList, status, errResp, err := r.listProjectsViaAPI(
		ctx,
		cookieHeader,
		shellData.ActiveOrganizationSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return scopedShellData, true
		}
		data.ProjectScope.ProjectsUnavailable = true
		data.ProjectScope.Message = dashboardErrorMessage("Project scope is unavailable.", errResp)
		return scopedShellData, false
	}

	requestedProjectSlug, explicitProjectSlug := r.dashboardRequestedProjectSlug(c, shellData.ActiveOrganizationSlug)
	projectOptions := make([]views.DashboardScopeProjectOption, 0, len(projectList.Projects))
	shellProjectOptions := make([]views.ShellProjectOption, 0, len(projectList.Projects))
	projectMatched := false
	for _, project := range projectList.Projects {
		isActive := project.Slug == requestedProjectSlug
		projectOptions = append(projectOptions, views.DashboardScopeProjectOption{
			Slug:     project.Slug,
			Name:     project.Name,
			Href:     views.DashboardScopeHref(project.Slug, ""),
			IsActive: isActive,
		})
		shellProjectOptions = append(shellProjectOptions, views.ShellProjectOption{
			Slug:     project.Slug,
			Name:     project.Name,
			IsActive: isActive,
		})
		if isActive {
			projectMatched = true
			r.rememberDashboardProjectScope(c, shellData.ActiveOrganizationSlug, project.Slug)
			scopedShellData.ActiveProjectSlug = project.Slug
			scopedShellData.ActiveProjectName = project.Name
			data.ProjectScope.ActiveProjectSlug = project.Slug
			data.ProjectScope.ActiveProjectName = project.Name
			data.ProjectScope.HasPipelineBackend = project.PipelineBackend != nil
		}
	}
	data.ProjectScope.Projects = projectOptions
	data.Onboarding.HasProject = len(projectList.Projects) > 0

	if len(projectList.Projects) == 0 {
		data.ProjectScope.Message = "No projects exist in this organization."
		return scopedShellData, false
	}
	if !explicitProjectSlug && (requestedProjectSlug == "" || !projectMatched) {
		if requestedProjectSlug != "" {
			expireDashboardScopeCookie(c, dashboardScopeProjectCookieName)
			expireDashboardScopeCookie(c, dashboardScopeEnvironmentCookieName)
		}
		requestedProjectSlug = selectDefaultProjectSlug(projectList.Projects)
		r.rememberDashboardProjectScope(c, shellData.ActiveOrganizationSlug, requestedProjectSlug)
		scopedShellData.ActiveProjectSlug = requestedProjectSlug
		scopedShellData.ActiveProjectName = requestedProjectSlug
		data.ProjectScope.ActiveProjectSlug = requestedProjectSlug
		data.ProjectScope.ActiveProjectName = requestedProjectSlug
		for i, project := range projectList.Projects {
			if project.Slug == requestedProjectSlug {
				scopedShellData.ActiveProjectName = project.Name
				data.ProjectScope.ActiveProjectName = project.Name
				projectOptions[i].IsActive = true
				shellProjectOptions[i].IsActive = true
				data.ProjectScope.HasPipelineBackend = project.PipelineBackend != nil
				break
			}
		}
		data.ProjectScope.Projects = projectOptions
	}
	if scopedShellData.ActiveProjectSlug == "" {
		data.ProjectScope.Message = "Selected project is not available in this organization."
		if requestedProjectSlug == "" {
			data.ProjectScope.Message = dashboardNoActiveProjectMessage
		}
		return scopedShellData, false
	}
	scopedShellData.Projects = shellProjectOptions

	var unauthorized bool
	scopedShellData, unauthorized = r.loadDashboardRuntimeScope(ctx, cookieHeader, c, scopedShellData, data)
	if unauthorized {
		return scopedShellData, true
	}
	scopedShellData.NavItems = buildShellNav(c.Path(), scopedShellData.ActiveOrganizationSlug, scopedShellData.ActiveProjectSlug)
	scopedShellData.NavItems = appendEnvironmentToProjectRuntimeNav(scopedShellData.NavItems, scopedShellData.ActiveEnvironmentSlug)
	return scopedShellData, false
}

// loadDashboardRuntimeScope resolves the selected project's local runtime target.
func (r *Router) loadDashboardRuntimeScope(
	ctx context.Context,
	cookieHeader string,
	c fiber.Ctx,
	scopedShellData views.AuthenticatedShellData,
	data *views.DashboardData,
) (views.AuthenticatedShellData, bool) {
	projectEnvironments, status, errResp, err := r.listProjectEnvironmentsViaAPI(
		ctx,
		cookieHeader,
		scopedShellData.ActiveOrganizationSlug,
		scopedShellData.ActiveProjectSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return scopedShellData, true
		}
		data.ProjectScope.EnvironmentsUnavailable = true
		data.ProjectScope.EnvironmentMessage = dashboardErrorMessage("Runtime targets are unavailable.", errResp)
		return scopedShellData, false
	}
	if projectEnvironments == nil || len(projectEnvironments.ProjectEnvironments) == 0 {
		data.ProjectScope.EnvironmentMessage = dashboardNoRuntimeTargetMessage
		return scopedShellData, false
	}

	requestedEnvironmentSlug := r.dashboardRequestedEnvironmentSlug(c, scopedShellData.ActiveOrganizationSlug, scopedShellData.ActiveProjectSlug)
	switcherState := buildEnvironmentSwitcherState(
		projectEnvironments.ProjectEnvironments,
		requestedEnvironmentSlug,
		func(environmentSlug string) string {
			return views.DashboardScopeHref(scopedShellData.ActiveProjectSlug, environmentSlug)
		},
	)
	if switcherState.ActiveSlug != "" {
		r.rememberDashboardEnvironmentScope(c, scopedShellData.ActiveOrganizationSlug, scopedShellData.ActiveProjectSlug, switcherState.ActiveSlug)
	}

	scopedShellData.ActiveEnvironmentSlug = switcherState.ActiveSlug
	scopedShellData.ActiveEnvironmentName = switcherState.ActiveName
	scopedShellData.ActiveEnvironmentColor = switcherState.ActiveColor
	scopedShellData.ActiveEnvironmentProtected = switcherState.ActiveProtected
	scopedShellData.Environments = switcherState.ShellOptions
	data.ProjectScope.ActiveEnvironmentSlug = switcherState.ActiveSlug
	data.ProjectScope.ActiveEnvironmentName = switcherState.ActiveName
	data.ProjectScope.ActiveEnvironmentColor = switcherState.ActiveColor
	data.ProjectScope.ActiveEnvironmentProtected = switcherState.ActiveProtected
	data.ProjectScope.Environments = switcherState.DashboardOptions
	return scopedShellData, false
}

// dashboardRequestedProjectSlug reads the explicit or remembered dashboard project scope.
func (r *Router) dashboardRequestedProjectSlug(c fiber.Ctx, activeOrganizationSlug string) (string, bool) {
	if dashboardQueryHas(c, "project_slug") {
		projectSlug := strings.TrimSpace(c.Query("project_slug"))
		if projectSlug == "" {
			expireDashboardScopeCookie(c, dashboardScopeProjectCookieName)
			expireDashboardScopeCookie(c, dashboardScopeEnvironmentCookieName)
		}
		return projectSlug, true
	}

	organizationSlug, projectSlug := dashboardRememberedProjectScope(c)
	if organizationSlug == strings.TrimSpace(activeOrganizationSlug) {
		return projectSlug, false
	}
	return "", false
}

// dashboardRequestedEnvironmentSlug reads the explicit or remembered dashboard runtime scope.
func (r *Router) dashboardRequestedEnvironmentSlug(c fiber.Ctx, activeOrganizationSlug, activeProjectSlug string) string {
	if requestRuntimeEnvironmentSlug(c) != "" || dashboardQueryHas(c, "environment_slug") || dashboardQueryHas(c, "target_environment_slug") {
		environmentSlug := strings.TrimSpace(requestRuntimeEnvironmentSlug(c))
		if environmentSlug == "" {
			expireDashboardScopeCookie(c, dashboardScopeEnvironmentCookieName)
		}
		return environmentSlug
	}

	organizationSlug, projectSlug, environmentSlug := dashboardRememberedEnvironmentScope(c)
	if organizationSlug == strings.TrimSpace(activeOrganizationSlug) &&
		projectSlug == strings.TrimSpace(activeProjectSlug) {
		return environmentSlug
	}
	return ""
}

// rememberDashboardProjectScope preserves project context across dashboard and organization pages.
func (r *Router) rememberDashboardProjectScope(c fiber.Ctx, organizationSlug, projectSlug string) {
	setDashboardScopeCookie(c, dashboardScopeProjectCookieName, strings.Join([]string{
		strings.TrimSpace(organizationSlug),
		strings.TrimSpace(projectSlug),
	}, ":"))
}

// rememberDashboardEnvironmentScope preserves the selected project's runtime context.
func (r *Router) rememberDashboardEnvironmentScope(c fiber.Ctx, organizationSlug, projectSlug, environmentSlug string) {
	setDashboardScopeCookie(c, dashboardScopeEnvironmentCookieName, strings.Join([]string{
		strings.TrimSpace(organizationSlug),
		strings.TrimSpace(projectSlug),
		strings.TrimSpace(environmentSlug),
	}, ":"))
}

// dashboardRememberedProjectScope returns a previously selected dashboard project scope.
func dashboardRememberedProjectScope(c fiber.Ctx) (string, string) {
	parts := strings.Split(c.Cookies(dashboardScopeProjectCookieName), ":")
	if len(parts) != 2 {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

// dashboardRememberedEnvironmentScope returns a previously selected dashboard runtime scope.
func dashboardRememberedEnvironmentScope(c fiber.Ctx) (string, string, string) {
	parts := strings.Split(c.Cookies(dashboardScopeEnvironmentCookieName), ":")
	if len(parts) != 3 {
		return "", "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
}

// dashboardQueryHas reports whether the dashboard request explicitly carried a query key.
func dashboardQueryHas(c fiber.Ctx, key string) bool {
	return c.Request().URI().QueryArgs().Has(key)
}

// setDashboardScopeCookie writes remembered scope shared by the application shell.
func setDashboardScopeCookie(c fiber.Ctx, name, value string) {
	c.Cookie(&fiber.Cookie{
		Name:     name,
		Value:    value,
		Path:     dashboardScopeCookiePath,
		MaxAge:   dashboardScopeCookieMaxAge,
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteLaxMode,
	})
	expireLegacyDashboardScopeCookie(c, name)
}

// expireDashboardScopeCookie clears a remembered dashboard scope cookie.
func expireDashboardScopeCookie(c fiber.Ctx, name string) {
	c.Cookie(&fiber.Cookie{
		Name:    name,
		Value:   "",
		Path:    dashboardScopeCookiePath,
		Expires: time.Now().Add(-time.Hour),
		MaxAge:  -1,
	})
	expireLegacyDashboardScopeCookie(c, name)
}

func expireLegacyDashboardScopeCookie(c fiber.Ctx, name string) {
	// Fiber's Cookie replaces by name, so append the old path's deletion separately.
	c.Response().Header.Add(fiber.HeaderSetCookie, (&http.Cookie{
		Name: name, Path: "/dashboard", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}).String())
}

// loadDashboardRunData loads recent and failed run panels for the selected runtime target.
func (r *Router) loadDashboardRunData(ctx context.Context, cookieHeader string, shellData views.AuthenticatedShellData, data *views.DashboardData) bool {
	data.RecentRuns.Href = views.DashboardRunsHref(shellData)
	data.IncidentQueue.Href = views.DashboardFailedRunsHref(shellData)
	data.RecentRuns.WindowLabel = "No run window loaded"

	if strings.TrimSpace(shellData.ActiveProjectSlug) == "" {
		message := dashboardNoActiveProjectMessage
		data.RecentRuns.Message = message
		data.IncidentQueue.Message = message
		return false
	}
	if strings.TrimSpace(shellData.ActiveEnvironmentSlug) == "" {
		message := dashboardNoRuntimeTargetMessage
		data.RecentRuns.Message = message
		data.IncidentQueue.Message = message
		return false
	}

	recentRuns, status, errResp, err := r.dashboardRuns(
		ctx,
		cookieHeader,
		shellData,
		"",
		dashboardRecentRunLimit,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return true
		}
		data.RecentRuns.Unavailable = true
		data.RecentRuns.Message = dashboardErrorMessage("Recent runs are unavailable.", errResp)
	} else {
		data.RecentRuns.WindowLabel = "Showing up to " + strconv.Itoa(dashboardRecentActivityLimit) + " recent runs; counts cover the latest " + strconv.FormatInt(int64(dashboardRecentRunLimit), 10) + " records."
		data.RecentRuns.HasMore = recentRuns.HasMore
		data.RecentRuns.Metrics = dashboardRecentRunMetrics(shellData, recentRuns.Runs)
		for _, run := range recentRuns.Runs[:min(len(recentRuns.Runs), dashboardRecentActivityLimit)] {
			data.RecentRuns.Runs = append(data.RecentRuns.Runs, dashboardRunSummary(shellData, run))
		}
		if len(recentRuns.Runs) == 0 {
			data.RecentRuns.Message = "No runs recorded for this runtime target."
		}
	}

	// Query succeeded runs independently of the recent activity window so an older
	// successful run still completes onboarding after newer failed or pending runs.
	succeededRuns, status, _, err := r.dashboardRuns(
		ctx,
		cookieHeader,
		shellData,
		string(api.PipelineRunMetadataStatusSucceeded),
		api.LimitParam(1),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return true
		}
	} else {
		data.Onboarding.HasSuccessfulPipelineRun = len(succeededRuns.Runs) > 0
	}

	failedRuns, status, errResp, err := r.dashboardRuns(
		ctx,
		cookieHeader,
		shellData,
		string(api.PipelineRunMetadataStatusFailed),
		dashboardFailedRunLimit,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return true
		}
		message := dashboardErrorMessage("Failed runs are unavailable.", errResp)
		data.IncidentQueue.Unavailable = true
		data.IncidentQueue.Message = message
		return false
	}

	data.IncidentQueue.HasMore = failedRuns.HasMore
	data.IncidentQueue.Runs = make([]views.DashboardRunSummary, 0, len(failedRuns.Runs))
	for _, run := range failedRuns.Runs {
		data.IncidentQueue.Runs = append(data.IncidentQueue.Runs, dashboardRunSummary(shellData, run))
	}
	if len(data.IncidentQueue.Runs) == 0 {
		data.IncidentQueue.Message = "No failed runs returned for this runtime target."
		return false
	}

	return false
}

// loadDashboardPipelineData loads project pipeline and deployment state for the selected runtime target.
func (r *Router) loadDashboardPipelineData(ctx context.Context, cookieHeader string, shellData views.AuthenticatedShellData, data *views.DashboardData) bool {
	data.PipelineStatus.Href = views.DashboardPipelinesHref(shellData)

	if strings.TrimSpace(shellData.ActiveProjectSlug) == "" {
		data.PipelineStatus.Message = dashboardNoActiveProjectMessage
		return false
	}

	pipelines, status, errResp, err := r.listAllPipelinesViaAPI(
		ctx,
		cookieHeader,
		shellData.ActiveOrganizationSlug,
		shellData.ActiveProjectSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return true
		}
		data.PipelineStatus.Unavailable = true
		data.PipelineStatus.Message = dashboardErrorMessage("Pipeline status is unavailable.", errResp)
		return false
	}

	deployedToTarget := 0
	deploymentsByPipeline := map[string]struct{}{}
	if strings.TrimSpace(shellData.ActiveEnvironmentSlug) == "" {
		data.PipelineStatus.DeploymentsUnavailable = true
	} else {
		deployments, status, errResp, err := r.listPipelineDeploymentsViaAPI(
			ctx,
			cookieHeader,
			shellData.ActiveOrganizationSlug,
			shellData.ActiveProjectSlug,
			shellData.ActiveEnvironmentSlug,
		)
		if err != nil {
			if status == fiber.StatusUnauthorized {
				return true
			}
			data.PipelineStatus.DeploymentsUnavailable = true
			data.PipelineStatus.Message = dashboardErrorMessage("Deployment pointers are unavailable.", errResp)
		} else {
			for _, deployment := range deployments.Deployments {
				deploymentsByPipeline[strings.TrimSpace(deployment.PipelineSlug)] = struct{}{}
			}
		}
	}

	draftCount := 0
	syncAttentionCount := 0
	for _, pipeline := range pipelines {
		if pipeline.HasDraft {
			draftCount++
		}
		if strings.TrimSpace(string(pipeline.SyncStatus)) != "" && pipeline.SyncStatus != api.Synced {
			syncAttentionCount++
		}
		if _, ok := deploymentsByPipeline[strings.TrimSpace(pipeline.Slug)]; ok {
			deployedToTarget++
		}
	}

	deployedMetric := dashboardMetricWithHref(
		"Deployed here",
		deployedToTarget,
		"Selected runtime target",
		dashboardMetricKind(deployedToTarget > 0, len(pipelines) > 0 && deployedToTarget == 0),
		views.DashboardPipelinesFilterHref(shellData, "deployment", "deployed"),
	)
	notDeployed := len(pipelines) - deployedToTarget
	notDeployedMetric := dashboardMetricWithHref(
		"Not deployed",
		notDeployed,
		"Selected runtime target",
		dashboardMetricKind(notDeployed == 0, notDeployed > 0),
		views.DashboardPipelinesFilterHref(shellData, "deployment", "not_deployed"),
	)
	if data.PipelineStatus.DeploymentsUnavailable {
		deployedMetric = dashboardUnavailableMetric("Deployed here", "Deployment pointers unavailable", data.PipelineStatus.Href)
		notDeployedMetric = dashboardUnavailableMetric("Not deployed", "Deployment pointers unavailable", data.PipelineStatus.Href)
	}
	data.PipelineStatus.Metrics = []views.DashboardMetric{
		dashboardMetricWithHref("Pipelines", len(pipelines), "Project inventory", dashboardMetricKind(len(pipelines) > 0, false), views.DashboardPipelinesHref(shellData)),
		deployedMetric,
		notDeployedMetric,
		dashboardMetricWithHref("Drafts", draftCount, "Saved member drafts", dashboardMetricKind(draftCount == 0, draftCount > 0), views.DashboardPipelinesFilterHref(shellData, "draft", "draft")),
		dashboardMetricWithHref("Sync attention", syncAttentionCount, "Drifted pipeline refs", dashboardMetricKind(syncAttentionCount == 0, syncAttentionCount > 0), views.DashboardPipelinesFilterHref(shellData, "sync_status", string(api.Drifted))),
	}
	if len(pipelines) == 0 && data.PipelineStatus.Message == "" {
		data.PipelineStatus.Message = "No pipelines exist in this project."
	}
	return false
}

// loadDashboardInventoryData loads organization data-source readiness and saved-query coverage.
func (r *Router) loadDashboardInventoryData(ctx context.Context, cookieHeader string, shellData views.AuthenticatedShellData, data *views.DashboardData) bool {
	data.DataSourceReadiness.Href = views.DashboardDataSourcesHref(shellData)
	data.QueryInventory.Href = views.DashboardQueriesHref(shellData)

	if strings.TrimSpace(shellData.ActiveOrganizationSlug) == "" {
		message := "No active organization is selected."
		data.DataSourceReadiness.Message = message
		data.QueryInventory.Message = message
		return false
	}

	dataSources, status, errResp, err := r.listAllDataSourcesViaAPI(
		ctx,
		cookieHeader,
		shellData.ActiveOrganizationSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return true
		}
		message := dashboardErrorMessage("Data-source readiness is unavailable.", errResp)
		data.DataSourceReadiness.Unavailable = true
		data.DataSourceReadiness.Message = message
		data.QueryInventory.Unavailable = true
		data.QueryInventory.Message = "Query inventory depends on data-source inventory."
		return false
	}

	data.DataSourceReadiness = dashboardDataSourceReadiness(shellData, dataSources)
	for _, dataSource := range dataSources {
		if views.DataSourceStatusLabel(dataSource) == "ready" && views.DataSourceSupportsQueryEditor(dataSource.Type) {
			data.Onboarding.HasSuccessfulConnection = true
			data.Onboarding.QueriesHref = views.DashboardQueriesForDataSourceHref(shellData, dataSource.Slug)
			break
		}
	}
	if data.Permissions.Can("queries", "read") && r.loadDashboardFirstResultData(ctx, cookieHeader, shellData, data) {
		return true
	}
	queryMetrics, unavailable, message, unauthorized := r.dashboardQueryInventory(ctx, cookieHeader, shellData, dataSources)
	if unauthorized {
		return true
	}
	data.QueryInventory.Href = views.DashboardQueriesHref(shellData)
	data.QueryInventory.Unavailable = unavailable
	data.QueryInventory.Message = message
	data.QueryInventory.Metrics = queryMetrics
	return false
}

func (r *Router) loadDashboardFirstResultData(ctx context.Context, cookieHeader string, shellData views.AuthenticatedShellData, data *views.DashboardData) bool {
	resp, err := r.apiClient.GetQueryExecutionSummaryWithResponse(ctx, shellData.ActiveOrganizationSlug, frontendRequestEditor(ctx, cookieHeader))
	if err != nil || resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		data.Onboarding.FirstResultUnavailable = true
		return err == nil && resp.StatusCode() == http.StatusUnauthorized
	}
	if execution := resp.JSON200.SuccessfulExecution; execution != nil {
		data.Onboarding.HasSuccessfulResult = true
		data.Onboarding.QueriesHref = "/orgs/" + shellData.ActiveOrganizationSlug + "/data-sources/" + execution.DataSourceSlug + "/queries/" + execution.QuerySlug
	}
	return false
}

// loadDashboardContractData loads logical data-source ref binding coverage for the active target.
func (r *Router) loadDashboardContractData(ctx context.Context, cookieHeader string, shellData views.AuthenticatedShellData, data *views.DashboardData) bool {
	data.ContractChecks.Href = views.DashboardEnvironmentBindingMatrixHref(shellData)

	if strings.TrimSpace(shellData.ActiveProjectSlug) == "" {
		data.ContractChecks.Message = dashboardNoActiveProjectMessage
		return false
	}
	if strings.TrimSpace(shellData.ActiveEnvironmentSlug) == "" {
		data.ContractChecks.Message = dashboardNoRuntimeTargetMessage
		return false
	}

	refs, status, errResp, err := r.listDataSourceRefsViaAPI(
		ctx,
		cookieHeader,
		shellData.ActiveOrganizationSlug,
		shellData.ActiveProjectSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return true
		}
		data.ContractChecks.Unavailable = true
		data.ContractChecks.Message = dashboardErrorMessage("Logical data-source refs are unavailable.", errResp)
		return false
	}

	bindings, status, errResp, err := r.listDataSourceBindingsViaAPI(
		ctx,
		cookieHeader,
		shellData.ActiveOrganizationSlug,
		shellData.ActiveProjectSlug,
		shellData.ActiveEnvironmentSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return true
		}
		data.ContractChecks.Unavailable = true
		data.ContractChecks.Message = dashboardErrorMessage("Runtime bindings are unavailable.", errResp)
		return false
	}

	data.ContractChecks = dashboardContractChecks(shellData, refs.DataSourceRefs, bindings.DataSourceBindings)
	return false
}

// dashboardRuns queries the run API with a consistent latest-first dashboard window.
func (r *Router) dashboardRuns(
	ctx context.Context,
	cookieHeader string,
	shellData views.AuthenticatedShellData,
	statusFilter string,
	limitValue api.LimitParam,
) (*api.PipelineRunListResponse, int, *api.ErrorResponse, error) {
	limit := limitValue
	sortBy := api.ListPipelineRunsParamsSortByRunCreatedAt
	order := api.ListPipelineRunsParamsOrderDesc
	params := &api.ListPipelineRunsParams{
		Limit:  &limit,
		SortBy: &sortBy,
		Order:  &order,
	}
	if strings.TrimSpace(statusFilter) != "" {
		status := api.ListPipelineRunsParamsStatus(statusFilter)
		params.Status = &status
	}

	return r.listPipelineRunsViaAPI(
		ctx,
		cookieHeader,
		shellData.ActiveOrganizationSlug,
		shellData.ActiveProjectSlug,
		shellData.ActiveEnvironmentSlug,
		params,
	)
}

// dashboardRecentRunMetrics counts the loaded recent-run window without implying all-time totals.
func dashboardRecentRunMetrics(shellData views.AuthenticatedShellData, runs []api.PipelineRunMetadata) []views.DashboardMetric {
	succeeded := 0
	failed := 0
	pending := 0
	for _, run := range runs {
		switch strings.TrimSpace(string(run.Status)) {
		case string(api.PipelineRunMetadataStatusSucceeded):
			succeeded++
		case string(api.PipelineRunMetadataStatusFailed):
			failed++
		default:
			pending++
		}
	}

	return []views.DashboardMetric{
		dashboardMetricWithHref("Loaded", len(runs), "Recent run window", dashboardMetricKind(len(runs) > 0, false), views.DashboardRunsHref(shellData)),
		dashboardMetricWithHref("Succeeded", succeeded, "Recent run window", dashboardMetricKind(succeeded > 0, false), views.DashboardRunsStatusHref(shellData, string(api.PipelineRunMetadataStatusSucceeded))),
		dashboardMetricWithHref("Failed", failed, "Recent run window", dashboardMetricKind(failed == 0, failed > 0), views.DashboardFailedRunsHref(shellData)),
		dashboardMetricWithHref("Other", pending, "Queued, running, or canceled", dashboardMetricKind(pending == 0, pending > 0), views.DashboardRunsHref(shellData)),
	}
}

// dashboardRunSummary converts API run metadata into route-backed dashboard row data.
func dashboardRunSummary(shellData views.AuthenticatedShellData, run api.PipelineRunMetadata) views.DashboardRunSummary {
	errorSummary := ""
	if run.ErrorSummary != nil {
		errorSummary = strings.TrimSpace(*run.ErrorSummary)
	}
	failureKind := ""
	if run.FailureKind != nil {
		failureKind = strings.TrimSpace(string(*run.FailureKind))
	}

	return views.DashboardRunSummary{
		RunSlug:       run.RunSlug,
		PipelineSlug:  run.PipelineSlug,
		Status:        run.Status,
		StartedAt:     run.StartedAt,
		DurationMs:    run.DurationMs,
		ErrorSummary:  errorSummary,
		FailureKind:   failureKind,
		SourceSummary: views.DashboardRunSourceSummary(run.Source),
		Href:          views.DashboardRunDetailHref(shellData, run.RunSlug),
		PipelineHref:  views.DashboardPipelineDetailHref(shellData, run.PipelineSlug),
	}
}

// dashboardDataSourceReadiness summarizes active connection-test state from data-source records.
func dashboardDataSourceReadiness(shellData views.AuthenticatedShellData, dataSources []api.DataSource) views.DashboardDataSourceReadiness {
	readiness := views.DashboardDataSourceReadiness{
		Href: views.DashboardDataSourcesHref(shellData),
	}

	active := 0
	readOnly := 0
	attention := 0
	failing := 0
	untested := 0
	for _, dataSource := range dataSources {
		dataSourceAttention := false
		if dataSource.IsActive {
			active++
		} else {
			dataSourceAttention = true
			readiness.Issues, readiness.HiddenIssueCount = dashboardAppendIssue(readiness.Issues, readiness.HiddenIssueCount, views.DashboardInventoryIssue{
				Label:  dataSource.Name,
				Detail: "inactive data source",
				Kind:   "error",
				Href:   views.DashboardDataSourceDetailHref(shellData, dataSource.Slug),
			})
		}
		if dataSource.ReadOnly {
			readOnly++
		}
		if dataSource.TestError != nil && strings.TrimSpace(*dataSource.TestError) != "" {
			failing++
			dataSourceAttention = true
			readiness.Issues, readiness.HiddenIssueCount = dashboardAppendIssue(readiness.Issues, readiness.HiddenIssueCount, views.DashboardInventoryIssue{
				Label:  dataSource.Name,
				Detail: "last connection test failed",
				Kind:   "error",
				Href:   views.DashboardDataSourceDetailHref(shellData, dataSource.Slug),
			})
		}
		if dataSource.LastTestedAt == nil {
			untested++
			dataSourceAttention = true
			readiness.Issues, readiness.HiddenIssueCount = dashboardAppendIssue(readiness.Issues, readiness.HiddenIssueCount, views.DashboardInventoryIssue{
				Label:  dataSource.Name,
				Detail: "no connection test timestamp",
				Kind:   "warning",
				Href:   views.DashboardDataSourceDetailHref(shellData, dataSource.Slug),
			})
		}
		if dataSourceAttention {
			attention++
		}
	}

	readiness.Metrics = []views.DashboardMetric{
		dashboardMetricWithHref("Data sources", len(dataSources), "Organization inventory", dashboardMetricKind(len(dataSources) > 0, false), readiness.Href),
		dashboardMetricWithHref("Active", active, "Usable connection records", dashboardMetricKind(active == len(dataSources), active == 0 && len(dataSources) > 0), views.DashboardDataSourcesFilterHref(shellData, views.DataSourceListFilterActive)),
		dashboardMetricWithHref("Attention", attention, "Inactive, failing, or untested", dashboardMetricKind(attention == 0, attention > 0), views.DashboardDataSourcesFilterHref(shellData, views.DataSourceListFilterAttention)),
		dashboardMetricWithHref("Read-only", readOnly, "Protected from sink writes", "neutral", views.DashboardDataSourcesFilterHref(shellData, views.DataSourceListFilterReadOnly)),
		dashboardMetricWithHref("Failed tests", failing, "Latest connection test error", dashboardMetricKind(failing == 0, failing > 0), views.DashboardDataSourcesFilterHref(shellData, views.DataSourceListFilterFailed)),
		dashboardMetricWithHref("Untested", untested, "Missing test timestamp", dashboardMetricKind(untested == 0, untested > 0), views.DashboardDataSourcesFilterHref(shellData, views.DataSourceListFilterUntested)),
	}
	if len(dataSources) == 0 {
		readiness.Message = "No data sources exist in this organization."
	}
	return readiness
}

// dashboardQueryInventory counts saved queries across the loaded data-source inventory.
func (r *Router) dashboardQueryInventory(
	ctx context.Context,
	cookieHeader string,
	shellData views.AuthenticatedShellData,
	dataSources []api.DataSource,
) ([]views.DashboardMetric, bool, string, bool) {
	queriesHref := views.DashboardQueriesHref(shellData)
	if len(dataSources) == 0 {
		return []views.DashboardMetric{
			dashboardMetricWithHref("Queries", 0, "No data sources available", "neutral", queriesHref),
			dashboardMetricWithHref("Sources with queries", 0, "No data sources available", "neutral", views.DashboardDataSourcesFilterHref(shellData, views.DataSourceListFilterWithQueries)),
			dashboardMetricWithHref("Sources without queries", 0, "No data sources available", "neutral", views.DashboardDataSourcesFilterHref(shellData, views.DataSourceListFilterWithoutQueries)),
		}, false, "No data sources exist for query inventory.", false
	}

	queryCount := 0
	withQueries := 0
	withoutQueries := 0
	queryCounts, status, errResp, err := r.loadDataSourceQueryCounts(
		ctx,
		cookieHeader,
		shellData.ActiveOrganizationSlug,
		dataSources,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return nil, true, "", true
		}
		return nil, true, dashboardErrorMessage("Query inventory is unavailable.", errResp), false
	}
	for _, dataSource := range dataSources {
		count := queryCounts[dataSource.Slug]
		queryCount += count
		if count > 0 {
			withQueries++
		} else {
			withoutQueries++
		}
	}

	return []views.DashboardMetric{
		dashboardMetricWithHref("Queries", queryCount, "Saved source operations", dashboardMetricKind(queryCount > 0, false), queriesHref),
		dashboardMetricWithHref("Sources with queries", withQueries, "Data sources with reusable operations", dashboardMetricKind(withQueries > 0, false), views.DashboardDataSourcesFilterHref(shellData, views.DataSourceListFilterWithQueries)),
		dashboardMetricWithHref("Sources without queries", withoutQueries, "No saved operations", dashboardMetricKind(withoutQueries == 0, withoutQueries > 0), views.DashboardDataSourcesFilterHref(shellData, views.DataSourceListFilterWithoutQueries)),
	}, false, "", false
}

// loadDataSourceQueryCounts loads saved-query counts with bounded fan-out for dashboard and list filters.
func (r *Router) loadDataSourceQueryCounts(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSources []api.DataSource,
) (map[string]int, int, *api.ErrorResponse, error) {
	queryCounts := make(map[string]int, len(dataSources))
	if len(dataSources) == 0 {
		return queryCounts, http.StatusOK, nil, nil
	}

	var mu sync.Mutex
	firstStatus := http.StatusOK
	var firstErrorResponse *api.ErrorResponse

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(dashboardQueryLookupConcurrency)
	for _, dataSource := range dataSources {
		group.Go(func() error {
			queries, status, errorResponse, err := r.listAllQueriesForDataSourceViaAPI(
				groupCtx,
				cookieHeader,
				organizationSlug,
				dataSource.Slug,
			)
			if err != nil {
				mu.Lock()
				if firstErrorResponse == nil && firstStatus == http.StatusOK {
					if status == 0 {
						status = http.StatusInternalServerError
					}
					firstStatus = status
					firstErrorResponse = errorResponse
				}
				mu.Unlock()
				return err
			}

			mu.Lock()
			queryCounts[dataSource.Slug] = len(queries)
			mu.Unlock()
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		mu.Lock()
		status := firstStatus
		errorResponse := firstErrorResponse
		mu.Unlock()
		if status == http.StatusOK {
			status = http.StatusInternalServerError
		}
		return nil, status, errorResponse, xerror.Wrapf("load data-source query counts: %w", err)
	}

	return queryCounts, http.StatusOK, nil, nil
}

// listAllQueriesForDataSourceViaAPI walks query pagination for a single data source.
func (r *Router) listAllQueriesForDataSourceViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
) ([]api.Query, int, *api.ErrorResponse, error) {
	limit := api.LimitParam(100)
	direction := api.ListQueriesParamsDirectionNext
	params := &api.ListQueriesParams{
		Limit: &limit,
	}
	queries := []api.Query{}
	seenCursors := map[string]struct{}{}

	for {
		queryList, status, errorResponse, err := r.listQueriesViaAPI(
			ctx,
			cookieHeader,
			organizationSlug,
			dataSourceSlug,
			params,
		)
		if err != nil {
			return nil, status, errorResponse, err
		}

		queries = append(queries, queryList.Queries...)
		if !queryList.HasMore || queryList.NextCursor == nil {
			return queries, http.StatusOK, nil, nil
		}
		nextCursor := strings.TrimSpace(*queryList.NextCursor)
		if nextCursor == "" {
			return queries, http.StatusOK, nil, nil
		}
		if _, exists := seenCursors[nextCursor]; exists {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().
				Detailf("list queries pagination returned repeated cursor %q", nextCursor)
		}
		seenCursors[nextCursor] = struct{}{}

		params = &api.ListQueriesParams{
			Limit:     &limit,
			Cursor:    &nextCursor,
			Direction: &direction,
		}
	}
}

// dashboardContractChecks summarizes binding coverage for logical refs in the selected environment.
func dashboardContractChecks(shellData views.AuthenticatedShellData, refs []api.DataSourceRef, bindings []api.DataSourceBinding) views.DashboardContractChecks {
	checks := views.DashboardContractChecks{
		Href: views.DashboardEnvironmentBindingMatrixHref(shellData),
	}

	bindingsByRef := map[string]api.DataSourceBinding{}
	for _, binding := range bindings {
		bindingsByRef[strings.TrimSpace(binding.DataSourceRefSlug)] = binding
	}

	missing := 0
	inactive := 0
	readOnlySink := 0
	for _, ref := range refs {
		binding, ok := bindingsByRef[strings.TrimSpace(ref.Slug)]
		if !ok {
			missing++
			checks.Issues, checks.HiddenIssueCount = dashboardAppendIssue(checks.Issues, checks.HiddenIssueCount, views.DashboardInventoryIssue{
				Label:  ref.Name,
				Detail: "missing binding in " + shellData.ActiveEnvironmentSlug,
				Kind:   "warning",
				Href:   checks.Href,
			})
			continue
		}
		if !binding.DataSourceActive {
			inactive++
			checks.Issues, checks.HiddenIssueCount = dashboardAppendIssue(checks.Issues, checks.HiddenIssueCount, views.DashboardInventoryIssue{
				Label:  binding.DataSourceRefName,
				Detail: "bound data source is inactive",
				Kind:   "error",
				Href:   views.DashboardDataSourceDetailHref(shellData, binding.DataSourceSlug),
			})
		}
		if dashboardRefRequiresSink(ref) && binding.DataSourceReadOnly {
			readOnlySink++
			checks.Issues, checks.HiddenIssueCount = dashboardAppendIssue(checks.Issues, checks.HiddenIssueCount, views.DashboardInventoryIssue{
				Label:  binding.DataSourceRefName,
				Detail: "sink ref is bound to a read-only data source",
				Kind:   "error",
				Href:   views.DashboardDataSourceDetailHref(shellData, binding.DataSourceSlug),
			})
		}
	}

	attention := missing + inactive + readOnlySink
	checks.Metrics = []views.DashboardMetric{
		dashboardMetricWithHref("Logical refs", len(refs), "Project data-source contract", dashboardMetricKind(len(refs) > 0, false), checks.Href),
		dashboardMetricWithHref("Bound here", len(bindings), "Selected runtime target", dashboardMetricKind(len(bindings) == len(refs), len(refs) > 0 && len(bindings) == 0), checks.Href),
		dashboardMetricWithHref("Missing", missing, "No target binding", dashboardMetricKind(missing == 0, missing > 0), checks.Href),
		dashboardMetricWithHref("Binding attention", attention, "Missing, inactive, or read-only sink", dashboardMetricKind(attention == 0, attention > 0), checks.Href),
	}
	if len(refs) == 0 {
		checks.Message = "No logical data-source refs are declared for this project."
	}
	return checks
}

// dashboardRefRequiresSink reports whether a logical ref requires sink capability.
func dashboardRefRequiresSink(ref api.DataSourceRef) bool {
	for _, capability := range ref.RequiredCapabilities {
		if capability == api.DataSourceRefCapabilityPipelineSink {
			return true
		}
	}
	return false
}

// dashboardMetricWithHref creates a metric that navigates to its owning detail surface.
func dashboardMetricWithHref(label string, value int, detail, kind, href string) views.DashboardMetric {
	return views.DashboardMetric{
		Label:  label,
		Value:  strconv.Itoa(value),
		Detail: detail,
		Kind:   kind,
		Href:   strings.TrimSpace(href),
	}
}

// dashboardUnavailableMetric creates a neutral placeholder when a dependent API failed.
func dashboardUnavailableMetric(label, detail, href string) views.DashboardMetric {
	return views.DashboardMetric{
		Label:  label,
		Value:  "N/A",
		Detail: detail,
		Kind:   "neutral",
		Href:   strings.TrimSpace(href),
	}
}

// dashboardMetricKind returns success or warning/error styling for count metrics.
func dashboardMetricKind(success, attention bool) string {
	switch {
	case attention:
		return "warning"
	case success:
		return "success"
	default:
		return "neutral"
	}
}

// dashboardAppendIssue adds a bounded issue row and tracks overflow for hidden review context.
func dashboardAppendIssue(
	issues []views.DashboardInventoryIssue,
	hiddenIssueCount int,
	issue views.DashboardInventoryIssue,
) ([]views.DashboardInventoryIssue, int) {
	if len(issues) >= dashboardIssueLimit {
		return issues, hiddenIssueCount + 1
	}
	return append(issues, issue), hiddenIssueCount
}

// dashboardErrorMessage prefers API error response copy over generic panel fallback text.
func dashboardErrorMessage(fallback string, errResp *api.ErrorResponse) string {
	message := messageFromErrorResponse(errResp)
	if strings.TrimSpace(message) == "" {
		return fallback
	}
	return message
}
