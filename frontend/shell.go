package frontend

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const builderPath = "/builder"

func (r *Router) buildShellDataForDashboard(
	c fiber.Ctx,
	title string,
	organizations []api.OrganizationWithRole,
	pageError string,
	pageErrorDetails string,
) views.AuthenticatedShellData {
	activeOrganizationSlug := reqctx.GetActiveOrganizationSlug(c, r.sessionStore).String()
	activeOrganizationName := ""
	activeOrganizationRole := ""
	if activeOrganizationSlug == "" && len(organizations) > 0 {
		activeOrganizationSlug = organizations[0].Slug
	}

	orgOptions := make([]views.ShellOrganizationOption, 0, len(organizations))
	for _, org := range organizations {
		if org.Slug == activeOrganizationSlug {
			activeOrganizationName = org.Name
			activeOrganizationRole = org.MemberRole
		}
		orgOptions = append(orgOptions, views.ShellOrganizationOption{
			Slug:     org.Slug,
			Name:     org.Name,
			IsActive: org.Slug == activeOrganizationSlug,
		})
	}

	if activeOrganizationName == "" {
		activeOrganizationName = activeOrganizationSlug
	}

	shellData := views.AuthenticatedShellData{
		Title:                  title,
		NotificationReturnHref: notificationReturnTo(c.Query("notification_return"), activeOrganizationSlug),
		ActiveOrganizationName: activeOrganizationName,
		CurrentPath:            c.Path(),
		ActiveOrganizationSlug: activeOrganizationSlug,
		Organizations:          orgOptions,
		User: views.ShellUserSummary{
			DisplayName: reqctx.GetUserSlug(c, r.sessionStore).String(),
		},
		NavItems:         buildShellNav(c.Path(), activeOrganizationSlug, ""),
		Breadcrumbs:      buildDashboardBreadcrumbs(c.Path()),
		PageError:        pageError,
		PageErrorDetails: pageErrorDetails,
	}
	if !r.canReadEventsForRole(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		activeOrganizationSlug,
		activeOrganizationRole,
	) {
		shellData.NavItems = removeShellNavItem(shellData.NavItems, "Events")
	}

	return r.withExternalAccessShellData(shellData)
}

func (r *Router) buildShellDataForOrganizationPage(c fiber.Ctx, title, organizationSlug, pageError, pageErrorDetails, cookieHeader string) views.AuthenticatedShellData {
	return r.buildShellData(c, title, organizationSlug, "", pageError, pageErrorDetails, cookieHeader)
}

func (r *Router) buildShellData(
	c fiber.Ctx,
	title string,
	organizationSlug string,
	activeProjectSlug string,
	pageError string,
	pageErrorDetails string,
	cookieHeader string,
) views.AuthenticatedShellData {
	activeOrganizationSlug := strings.TrimSpace(organizationSlug)
	activeOrganizationName := ""
	activeOrganizationRole := ""

	organizations := []views.ShellOrganizationOption{}

	if activeOrganizationSlug != "" {
		params := &api.ListOrganizationsParams{}
		limit := api.LimitParam(100)
		params.Limit = &limit

		organizationList, _, _, err := r.listOrganizationsViaAPI(
			c.Context(),
			cookieHeader,
			params,
		)
		if err == nil {
			for _, org := range organizationList.Organizations {
				if org.Slug == activeOrganizationSlug {
					activeOrganizationName = org.Name
					activeOrganizationRole = org.MemberRole
				}
				organizations = append(organizations, views.ShellOrganizationOption{
					Slug:     org.Slug,
					Name:     org.Name,
					IsActive: org.Slug == activeOrganizationSlug,
				})
			}
		}

		if activeOrganizationName == "" {
			activeOrganizationName = activeOrganizationSlug
		}

		if len(organizations) == 0 {
			organizations = append(organizations, views.ShellOrganizationOption{
				Slug:     activeOrganizationSlug,
				Name:     activeOrganizationName,
				IsActive: true,
			})
		}
	}

	shellData := views.AuthenticatedShellData{
		Title:                  title,
		NotificationReturnHref: notificationReturnTo(c.Query("notification_return"), activeOrganizationSlug),
		ActiveOrganizationName: activeOrganizationName,
		CurrentPath:            c.Path(),
		ActiveOrganizationSlug: activeOrganizationSlug,
		Organizations:          organizations,
		User: views.ShellUserSummary{
			DisplayName: reqctx.GetUserSlug(c, r.sessionStore).String(),
		},
		NavItems: buildShellNav(c.Path(), activeOrganizationSlug, ""),
		Breadcrumbs: buildOrganizationBreadcrumbs(
			c.Path(),
			activeOrganizationSlug,
			activeOrganizationName,
		),
		PageError:        pageError,
		PageErrorDetails: pageErrorDetails,
	}

	shellData = r.withProjectShellData(c, r.withExternalAccessShellData(shellData), cookieHeader, activeProjectSlug)
	if !r.canReadEventsForRole(
		c.Context(),
		cookieHeader,
		activeOrganizationSlug,
		activeOrganizationRole,
	) {
		shellData.NavItems = removeShellNavItem(shellData.NavItems, "Events")
	}
	return shellData
}

func (r *Router) withExternalAccessShellData(data views.AuthenticatedShellData) views.AuthenticatedShellData {
	data.DocumentationURL = r.documentationURL
	return data
}

func removeShellNavItem(items []views.ShellNavItem, label string) []views.ShellNavItem {
	filtered := make([]views.ShellNavItem, 0, len(items))
	for _, item := range items {
		if item.Label != label {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (r *Router) buildShellDataForProjectPage(
	c fiber.Ctx,
	title string,
	organizationSlug string,
	projectSlug string,
	pageError string,
	pageErrorDetails string,
	cookieHeader string,
) views.AuthenticatedShellData {
	shellData := r.buildShellData(
		c,
		title,
		organizationSlug,
		projectSlug,
		pageError,
		pageErrorDetails,
		cookieHeader,
	)
	eventsVisible := shellNavContains(shellData.NavItems, "Events")

	activeProjectSlug := strings.TrimSpace(projectSlug)
	activeProjectName := shellData.ActiveProjectName
	if activeProjectName == "" {
		activeProjectName = activeProjectSlug
	}
	if len(shellData.Projects) == 0 && activeProjectSlug != "" {
		projectOptions := []views.ShellProjectOption{{
			Slug:     activeProjectSlug,
			Name:     activeProjectName,
			IsActive: true,
		}}
		shellData.ActiveProjectSlug = activeProjectSlug
		shellData.ActiveProjectName = activeProjectName
		shellData.Projects = projectOptions
		shellData.NavItems = buildShellNav(c.Path(), shellData.ActiveOrganizationSlug, activeProjectSlug)
	}
	if !eventsVisible {
		shellData.NavItems = removeShellNavItem(shellData.NavItems, "Events")
	}

	shellData.Breadcrumbs = buildProjectBreadcrumbs(
		c.Path(),
		shellData.ActiveOrganizationSlug,
		shellData.ActiveOrganizationName,
		shellData.ActiveProjectSlug,
		activeProjectName,
	)
	return shellData
}

func shellNavContains(items []views.ShellNavItem, label string) bool {
	for _, item := range items {
		if item.Label == label {
			return true
		}
	}
	return false
}

func (r *Router) canReadEventsForRole(
	ctx context.Context,
	cookieHeader, organizationSlug, roleSlug string,
) bool {
	if strings.TrimSpace(organizationSlug) == "" {
		return false
	}
	switch strings.TrimSpace(roleSlug) {
	case "admin", "viewer":
		return true
	}
	limit := api.LimitParam(1)
	_, status, _, err := r.listEventsViaAPI(
		ctx,
		cookieHeader,
		organizationSlug,
		&api.ListEventsParams{Limit: &limit},
	)
	return err == nil && status == http.StatusOK
}

func (r *Router) withProjectShellData(
	c fiber.Ctx,
	shellData views.AuthenticatedShellData,
	cookieHeader string,
	activeProjectSlug string,
) views.AuthenticatedShellData {
	if shellData.ActiveOrganizationSlug == "" {
		return shellData
	}

	projectList, _, _, err := r.listProjectsViaAPI(
		c.Context(),
		cookieHeader,
		shellData.ActiveOrganizationSlug,
	)
	if err != nil || projectList == nil || len(projectList.Projects) == 0 {
		return shellData
	}

	activeProjectSlug = strings.TrimSpace(activeProjectSlug)
	if activeProjectSlug == "" {
		organization, rememberedProject := dashboardRememberedProjectScope(c)
		if organization == shellData.ActiveOrganizationSlug {
			for _, project := range projectList.Projects {
				if project.Slug == rememberedProject {
					activeProjectSlug = rememberedProject
					break
				}
			}
		}
	}
	if activeProjectSlug == "" {
		activeProjectSlug = selectDefaultProjectSlug(projectList.Projects)
	}

	activeProjectName := activeProjectSlug
	projectOptions := make([]views.ShellProjectOption, 0, len(projectList.Projects))
	for _, project := range projectList.Projects {
		if project.Slug == activeProjectSlug {
			activeProjectName = project.Name
			r.rememberDashboardProjectScope(c, shellData.ActiveOrganizationSlug, activeProjectSlug)
		}
		projectOptions = append(projectOptions, views.ShellProjectOption{
			Slug:     project.Slug,
			Name:     project.Name,
			IsActive: project.Slug == activeProjectSlug,
		})
	}

	shellData.ActiveProjectSlug = activeProjectSlug
	shellData.ActiveProjectName = activeProjectName
	shellData.Projects = projectOptions
	if activeProjectSlug != "" {
		shellData = r.withEnvironmentShellData(c, shellData, cookieHeader, activeProjectSlug)
	}
	shellData.NavItems = buildShellNav(c.Path(), shellData.ActiveOrganizationSlug, activeProjectSlug)
	shellData.NavItems = appendEnvironmentToProjectRuntimeNav(shellData.NavItems, shellData.ActiveEnvironmentSlug)
	return shellData
}

func selectDefaultProjectSlug(projects []api.Project) string {
	return projects[0].Slug
}

func (r *Router) withEnvironmentShellData(
	c fiber.Ctx,
	shellData views.AuthenticatedShellData,
	cookieHeader string,
	activeProjectSlug string,
) views.AuthenticatedShellData {
	activeProjectSlug = strings.TrimSpace(activeProjectSlug)
	if shellData.ActiveOrganizationSlug == "" || activeProjectSlug == "" {
		return shellData
	}

	projectEnvironments, _, _, err := r.listProjectEnvironmentsViaAPI(
		c.Context(),
		cookieHeader,
		shellData.ActiveOrganizationSlug,
		activeProjectSlug,
	)
	if err != nil || projectEnvironments == nil || len(projectEnvironments.ProjectEnvironments) == 0 {
		return shellData
	}

	requestedEnvironmentSlug := r.dashboardRequestedEnvironmentSlug(c, shellData.ActiveOrganizationSlug, activeProjectSlug)
	requestURL := environmentSwitcherRequestURL(c.Method(), c.OriginalURL())
	switcherState := buildEnvironmentSwitcherState(
		projectEnvironments.ProjectEnvironments,
		requestedEnvironmentSlug,
		func(environmentSlug string) string {
			return environmentSwitcherHref(requestURL, environmentSlug)
		},
	)

	shellData.ActiveEnvironmentSlug = switcherState.ActiveSlug
	shellData.ActiveEnvironmentName = switcherState.ActiveName
	shellData.ActiveEnvironmentColor = switcherState.ActiveColor
	shellData.ActiveEnvironmentProtected = switcherState.ActiveProtected
	shellData.Environments = switcherState.ShellOptions
	if switcherState.ActiveSlug != "" {
		r.rememberDashboardEnvironmentScope(c, shellData.ActiveOrganizationSlug, activeProjectSlug, switcherState.ActiveSlug)
	}
	return shellData
}

func selectDefaultProjectEnvironmentSlug(projectEnvironments []api.ProjectEnvironment) string {
	if len(projectEnvironments) == 0 {
		return ""
	}
	for _, environment := range projectEnvironments {
		if environment.IsDefault {
			return environment.EnvironmentSlug
		}
	}
	return projectEnvironments[0].EnvironmentSlug
}

func projectEnvironmentSlugIsEnabled(projectEnvironments []api.ProjectEnvironment, environmentSlug string) bool {
	trimmedEnvironmentSlug := strings.TrimSpace(environmentSlug)
	if trimmedEnvironmentSlug == "" {
		return false
	}
	if _, err := slug.New(trimmedEnvironmentSlug); err != nil {
		return false
	}
	for _, environment := range projectEnvironments {
		if environment.EnvironmentSlug == trimmedEnvironmentSlug {
			return true
		}
	}
	return false
}

// environmentSwitcherHref replaces the project runtime target while preserving stable filters.
func environmentSwitcherHref(originalURL, environmentSlug string) string {
	trimmedEnvironmentSlug := strings.TrimSpace(environmentSlug)
	if trimmedEnvironmentSlug == "" {
		return strings.TrimSpace(originalURL)
	}

	parsed, err := url.Parse(strings.TrimSpace(originalURL))
	if err != nil {
		return strings.TrimSpace(originalURL)
	}

	values := parsed.Query()
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) == 8 && segments[0] == "orgs" && segments[2] == "projects" && segments[4] == "environments" && segments[6] == "proof-receipts" {
		parsed.Path = "/" + strings.Join(segments[:4], "/") + "/proof-receipts"
	}
	switch {
	case len(segments) == 9 && segments[0] == "orgs" && segments[2] == "projects" && segments[4] == "pipelines" && segments[6] == "environments" && segments[8] == "recency":
		segments[7] = trimmedEnvironmentSlug
		parsed.Path = "/" + strings.Join(segments, "/")
		values.Del("environment_slug")
		values.Del("target_environment_slug")
	case parsed.Path == builderPath:
		values.Set("target_environment_slug", trimmedEnvironmentSlug)
		values.Del("environment_slug")
	default:
		values.Set("environment_slug", trimmedEnvironmentSlug)
		values.Del("target_environment_slug")
	}
	values.Del("cursor")
	values.Del("direction")
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func environmentSwitcherRequestURL(method, originalURL string) string {
	trimmedOriginalURL := strings.TrimSpace(originalURL)
	parsed, err := url.Parse(trimmedOriginalURL)
	if err != nil {
		return trimmedOriginalURL
	}

	if method == http.MethodGet {
		if path := projectScopedRunListPathForRunDetail(parsed.Path); path != parsed.Path {
			parsed.Path = path
			parsed.RawQuery = ""
			return parsed.String()
		}
		return trimmedOriginalURL
	}

	parsed.Path = projectScopedGETPathForAction(parsed.Path)
	parsed.RawQuery = ""
	return parsed.String()
}

func projectScopedGETPathForAction(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < 5 ||
		segments[0] != "orgs" ||
		segments[2] != "projects" ||
		segments[4] != "pipelines" {
		return path
	}

	if len(segments) == 7 && segments[6] == "deployment" {
		return "/" + strings.Join(segments[:7], "/")
	}

	if len(segments) >= 7 {
		return "/" + strings.Join(segments[:6], "/")
	}

	if len(segments) == 6 && segments[5] == "save" {
		return "/" + strings.Join(segments[:5], "/")
	}

	return path
}

func projectScopedRunListPathForRunDetail(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) != 6 ||
		segments[0] != "orgs" ||
		segments[2] != "projects" ||
		segments[4] != "runs" {
		return path
	}

	return "/" + strings.Join(segments[:5], "/")
}

func (r *Router) listProjectsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
) (*api.ProjectListResponse, int, *api.ErrorResponse, error) {
	params := &api.ListProjectsParams{}
	limit := api.LimitParam(100)
	params.Limit = &limit
	sortBy := api.ListProjectsParamsSortByProjectName
	params.SortBy = &sortBy
	order := api.ListProjectsParamsOrderAsc
	params.Order = &order

	projects := []api.Project{}
	for {
		resp, err := r.apiClient.ListProjectsWithResponse(ctx, organizationSlug, params, frontendRequestEditor(ctx, cookieHeader))
		if err != nil {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
		}
		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			return nil, resp.StatusCode(), parseAPIErrorResponse(resp.Body), xerror.Unexpected().Detailf("list projects api returned status %d", resp.StatusCode())
		}
		projects = append(projects, resp.JSON200.Projects...)
		if !resp.JSON200.HasMore {
			return &api.ProjectListResponse{Projects: projects}, http.StatusOK, nil, nil
		}
		if resp.JSON200.NextCursor == nil || *resp.JSON200.NextCursor == "" || (params.Cursor != nil && *params.Cursor == *resp.JSON200.NextCursor) {
			return nil, http.StatusBadGateway, nil, xerror.Unexpected().Detail("project pagination did not advance")
		}
		params.Cursor = resp.JSON200.NextCursor
	}
}

func (r *Router) listProjectEnvironmentsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
) (*api.ProjectEnvironmentListResponse, int, *api.ErrorResponse, error) {
	params := &api.ListProjectEnvironmentsParams{}
	limit := api.LimitParam(100)
	params.Limit = &limit
	sortBy := api.ListProjectEnvironmentsParamsSortByEnvironmentName
	params.SortBy = &sortBy
	order := api.ListProjectEnvironmentsParamsOrderAsc
	params.Order = &order

	projectEnvironments := []api.ProjectEnvironment{}
	for {
		resp, err := r.apiClient.ListProjectEnvironmentsWithResponse(
			ctx,
			organizationSlug,
			projectSlugForAPI(projectSlug),
			params,
			frontendRequestEditor(ctx, cookieHeader),
		)
		if err != nil {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
		}

		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			errResp := parseAPIErrorResponse(resp.Body)
			return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
				"list project environments api returned status %d",
				resp.StatusCode(),
			)
		}

		projectEnvironments = append(projectEnvironments, resp.JSON200.ProjectEnvironments...)
		if !resp.JSON200.HasMore || resp.JSON200.NextCursor == nil {
			return &api.ProjectEnvironmentListResponse{
				ProjectEnvironments: projectEnvironments,
				HasMore:             false,
				NextCursor:          nil,
				PrevCursor:          nil,
			}, http.StatusOK, nil, nil
		}

		cursor := *resp.JSON200.NextCursor
		params.Cursor = &cursor
	}
}

func buildDashboardBreadcrumbs(currentPath string) []views.BreadcrumbItem {
	if currentPath == builderPath {
		return []views.BreadcrumbItem{
			{Label: "Dashboard", Href: defaultPostLoginPath},
			{Label: "Builder"},
		}
	}

	if currentPath == organizationsPath {
		return []views.BreadcrumbItem{{Label: "Organizations"}}
	}

	if currentPath == "/organizations/new" {
		return []views.BreadcrumbItem{
			{Label: "Organizations", Href: organizationsPath},
			{Label: "Create organization"},
		}
	}

	if strings.HasPrefix(currentPath, "/organizations/") {
		orgSlug := strings.TrimPrefix(currentPath, "/organizations/")
		orgSlug = strings.TrimSuffix(orgSlug, "/security")
		if orgSlug != "" {
			items := []views.BreadcrumbItem{
				{Label: "Organizations", Href: organizationsPath},
				{Label: orgSlug},
			}
			if strings.HasSuffix(currentPath, "/security") {
				items[1].Href = "/organizations/" + orgSlug
				items = append(items, views.BreadcrumbItem{Label: "Security"})
			}
			return items
		}
	}

	return []views.BreadcrumbItem{{Label: "Dashboard"}}
}

func buildOrganizationBreadcrumbs(
	currentPath string,
	activeOrganizationSlug string,
	activeOrganizationName string,
) []views.BreadcrumbItem {
	items := buildOrganizationBaseBreadcrumbs(activeOrganizationName, activeOrganizationSlug)

	switch {
	case strings.HasSuffix(currentPath, "/members"):
		items = append(items, views.BreadcrumbItem{Label: "Members"})
	case strings.HasSuffix(currentPath, "/pipelines"):
		items = append(items, views.BreadcrumbItem{Label: "Pipelines"})
	case strings.HasSuffix(currentPath, "/data-sources"):
		items = append(items, views.BreadcrumbItem{Label: "Data sources"})
	case strings.HasSuffix(currentPath, "/queries"):
		items = append(items, views.BreadcrumbItem{Label: "Queries"})
	case strings.HasSuffix(currentPath, "/runs"):
		items = append(items, views.BreadcrumbItem{Label: "Runs"})
	case strings.HasSuffix(currentPath, "/events"):
		items = append(items, views.BreadcrumbItem{Label: "Events"})
	case strings.HasSuffix(currentPath, "/notifications"):
		items = append(items, views.BreadcrumbItem{Label: "Notifications"})
	case strings.HasSuffix(currentPath, "/alerts"):
		items = append(items, views.BreadcrumbItem{Label: "Alerts"})
	}

	return items
}

func buildOrganizationDetailBreadcrumbs(
	orgName string,
	orgSlug string,
	sectionLabel string,
	sectionHref string,
	currentLabel string,
) []views.BreadcrumbItem {
	items := buildOrganizationBaseBreadcrumbs(orgName, orgSlug)
	items = append(items, views.BreadcrumbItem{Label: sectionLabel, Href: sectionHref})
	items = append(items, views.BreadcrumbItem{Label: currentLabel})
	return items
}

func buildOrganizationBaseBreadcrumbs(orgName, orgSlug string) []views.BreadcrumbItem {
	organizationLabel := strings.TrimSpace(orgName)
	if organizationLabel == "" {
		organizationLabel = strings.TrimSpace(orgSlug)
	}

	items := []views.BreadcrumbItem{{Label: "Organizations", Href: organizationsPath}}
	if organizationLabel != "" {
		items = append(items, views.BreadcrumbItem{
			Label: organizationLabel,
			Href:  "/organizations/" + strings.TrimSpace(orgSlug),
		})
	}
	return items
}

func buildProjectBreadcrumbs(
	currentPath string,
	activeOrganizationSlug string,
	activeOrganizationName string,
	activeProjectSlug string,
	activeProjectName string,
) []views.BreadcrumbItem {
	items := buildOrganizationBaseBreadcrumbs(activeOrganizationName, activeOrganizationSlug)
	projectLabel := strings.TrimSpace(activeProjectName)
	if projectLabel == "" {
		projectLabel = strings.TrimSpace(activeProjectSlug)
	}
	if projectLabel != "" {
		items = append(items, views.BreadcrumbItem{
			Label: projectLabel,
			Href: "/orgs/" + strings.TrimSpace(activeOrganizationSlug) + "/projects/" +
				strings.TrimSpace(activeProjectSlug) + "/pipelines",
		})
	}

	switch {
	case strings.Contains(currentPath, "/proof-receipts"):
		items = append(items, views.BreadcrumbItem{Label: "Proof"})
	case strings.Contains(currentPath, "/pipelines"):
		items = append(items, views.BreadcrumbItem{Label: "Pipelines"})
	case strings.Contains(currentPath, "/environments"):
		items = append(items, views.BreadcrumbItem{Label: "Environments"})
	case strings.Contains(currentPath, "/schedules"):
		items = append(items, views.BreadcrumbItem{Label: "Schedules"})
	case strings.Contains(currentPath, "/runs"):
		items = append(items, views.BreadcrumbItem{Label: "Runs"})
	}

	return items
}

func buildShellNav(currentPath, activeOrganizationSlug, activeProjectSlug string) []views.ShellNavItem {
	projectsPath := ""
	projectsEnabled := false
	pipelinesPath := ""
	pipelinesEnabled := false
	schedulesPath := ""
	schedulesEnabled := false
	dataSourcesPath := ""
	dataSourcesEnabled := false
	queriesPath := ""
	queriesEnabled := false
	notificationsPath := ""
	notificationsEnabled := false
	alertsPath := ""
	alertsEnabled := false
	eventsPath := ""
	eventsEnabled := false
	runsPath := ""
	runsEnabled := false
	proofReceiptsPath := ""
	environmentsPath := ""
	environmentsEnabled := false
	if activeOrganizationSlug != "" {
		projectsPath = "/organizations/" + activeOrganizationSlug + "/projects"
		projectsEnabled = true
		environmentsPath = "/organizations/" + activeOrganizationSlug + "/environments"
		environmentsEnabled = true
		if activeProjectSlug != "" {
			pipelinesPath = "/orgs/" + activeOrganizationSlug + "/projects/" + activeProjectSlug + "/pipelines"
			schedulesPath = "/orgs/" + activeOrganizationSlug + "/projects/" + activeProjectSlug + "/schedules"
			runsPath = "/orgs/" + activeOrganizationSlug + "/projects/" + activeProjectSlug + "/runs"
			proofReceiptsPath = "/orgs/" + activeOrganizationSlug + "/projects/" + activeProjectSlug + "/proof-receipts"
			pipelinesEnabled = true
			schedulesEnabled = true
			runsEnabled = true
		}
		dataSourcesPath = "/orgs/" + activeOrganizationSlug + "/data-sources"
		dataSourcesEnabled = true
		queriesPath = "/orgs/" + activeOrganizationSlug + "/queries"
		queriesEnabled = true
		notificationsPath = "/orgs/" + activeOrganizationSlug + "/notifications"
		notificationsEnabled = true
		alertsPath = "/orgs/" + activeOrganizationSlug + "/alerts"
		alertsEnabled = true
		eventsPath = "/orgs/" + activeOrganizationSlug + "/events"
		eventsEnabled = true
	}

	return []views.ShellNavItem{
		{
			Label:    "Overview",
			Icon:     "home",
			Href:     defaultPostLoginPath,
			IsActive: currentPath == defaultPostLoginPath,
			Enabled:  true,
		},
		{
			Group: "Build",
			Label: "Projects",
			Icon:  "project",
			Href:  projectsPath,
			IsActive: projectsEnabled && strings.HasPrefix(currentPath, "/organizations/") &&
				(strings.HasSuffix(currentPath, "/projects") || strings.HasPrefix(currentPath, projectsPath+"/")),
			Enabled: projectsEnabled,
		},
		{
			Group: "Build",
			Label: "Data Sources",
			Icon:  "data-source",
			Href:  dataSourcesPath,
			IsActive: strings.HasPrefix(currentPath, "/orgs/") &&
				(strings.HasSuffix(currentPath, "/data-sources") || (strings.HasPrefix(currentPath, dataSourcesPath+"/") && !strings.Contains(currentPath, "/queries/"))),
			Enabled: dataSourcesEnabled,
		},
		{
			Group: "Build",
			Label: "Queries",
			Icon:  "query",
			Href:  queriesPath,
			IsActive: strings.HasPrefix(currentPath, "/orgs/") &&
				(strings.HasSuffix(currentPath, "/queries") || strings.Contains(currentPath, "/queries/")),
			Enabled: queriesEnabled,
		},
		{
			Group: "Admin",
			Label: "Environments",
			Icon:  "environment",
			Href:  environmentsPath,
			IsActive: strings.HasPrefix(currentPath, "/organizations/") &&
				(strings.HasSuffix(currentPath, "/environments") ||
					strings.Contains(currentPath, "/environments/")),
			Enabled: environmentsEnabled,
		},
		{
			Group: "Build",
			Label: "Pipelines",
			Icon:  "pipeline",
			Href:  pipelinesPath,
			IsActive: currentPath == builderPath || (strings.HasPrefix(currentPath, "/orgs/") &&
				(strings.Contains(currentPath, "/pipelines") || strings.HasPrefix(currentPath, "/orgs/"+activeOrganizationSlug+"/projects/"+activeProjectSlug+"/pipeline-creations"))),
			Enabled: pipelinesEnabled,
		},
		{
			Group:    "Operate",
			Label:    "Proof",
			Icon:     "proof",
			Href:     proofReceiptsPath,
			IsActive: strings.Contains(currentPath, "/proof-receipts"),
			Enabled:  pipelinesEnabled,
		},
		{
			Group: "Operate",
			Label: "Schedules",
			Icon:  "calendar",
			Href:  schedulesPath,
			IsActive: strings.HasPrefix(currentPath, "/orgs/") &&
				strings.Contains(currentPath, "/schedules"),
			Enabled: schedulesEnabled,
		},
		{
			Group: "Operate",
			Label: "Runs",
			Icon:  "run",
			Href:  runsPath,
			IsActive: strings.HasPrefix(currentPath, "/orgs/") &&
				strings.Contains(currentPath, "/runs"),
			Enabled: runsEnabled,
		},
		{
			Group: "Operate",
			Label: "Notifications",
			Icon:  "notification",
			Href:  notificationsPath,
			IsActive: strings.HasPrefix(currentPath, "/orgs/") &&
				strings.Contains(currentPath, "/notifications"),
			Enabled: notificationsEnabled,
		},
		{
			Group: "Operate",
			Label: "Events",
			Icon:  "event",
			Href:  eventsPath,
			IsActive: strings.HasPrefix(currentPath, "/orgs/") &&
				strings.Contains(currentPath, "/events"),
			Enabled: eventsEnabled,
		},
		{
			Group: "Operate",
			Label: "Alerts",
			Icon:  "alert",
			Href:  alertsPath,
			IsActive: strings.HasPrefix(currentPath, "/orgs/") &&
				strings.Contains(currentPath, "/alerts"),
			Enabled: alertsEnabled,
		},
		{
			Group: "Admin",
			Label: "Members",
			Icon:  "users",
			Href:  "/organizations/" + activeOrganizationSlug + "/members",
			IsActive: activeOrganizationSlug != "" && (currentPath == "/organizations/"+activeOrganizationSlug+"/members" ||
				currentPath == "/orgs/"+activeOrganizationSlug+"/members"),
			Enabled: activeOrganizationSlug != "",
		},
		{
			Group:    "Admin",
			Label:    "Organization settings",
			Icon:     "building",
			Href:     views.ShellOrganizationSettingsHref(activeOrganizationSlug),
			IsActive: activeOrganizationSlug != "" && currentPath == views.ShellOrganizationSettingsHref(activeOrganizationSlug),
			Enabled:  activeOrganizationSlug != "",
		},
	}
}

func appendEnvironmentToProjectRuntimeNav(navItems []views.ShellNavItem, environmentSlug string) []views.ShellNavItem {
	environmentSlug = strings.TrimSpace(environmentSlug)
	if environmentSlug == "" {
		return navItems
	}

	for i := range navItems {
		if !navItems[i].Enabled {
			continue
		}
		switch navItems[i].Label {
		case "Environments", "Pipelines", "Proof", "Schedules", "Runs":
			navItems[i].Href = environmentSwitcherHref(navItems[i].Href, environmentSlug)
		}
	}
	return navItems
}
