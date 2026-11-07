package frontend

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	organizationNotFoundMessage            = "organization not found"
	maxProjectPipelineDetailLookupParallel = 4
)

func (r *Router) handleDashboardPage(c fiber.Ctx) error {
	organizations := []api.OrganizationWithRole{}
	pageError := ""
	params := &api.ListOrganizationsParams{}

	limit := api.LimitParam(20)
	params.Limit = &limit

	cursor := strings.TrimSpace(c.Query("cursor"))
	if cursor != "" {
		cursorParam := cursor
		params.Cursor = &cursorParam
	}

	orgList, status, errorResponse, err := r.listOrganizationsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		params,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = fmt.Sprintf("Failed to load organizations (status %d).", status)
		}

		pageError = message
		shellData := r.buildShellDataForDashboard(
			c,
			"Dashboard",
			organizations,
			pageError,
			errorPopupDetails(errorResponse, ""),
		)
		dashboardData := views.DashboardData{}
		return renderTempl(
			c,
			views.DashboardPage(shellData, dashboardData),
		)
	}

	organizations = orgList.Organizations

	shellData := r.buildShellDataForDashboard(c, "Dashboard", organizations, pageError, "")
	dashboardData, shellData, unauthorized := r.loadDashboardData(c, shellData)
	if unauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}

	return renderTempl(
		c,
		views.DashboardPage(shellData, dashboardData),
	)
}

func (r *Router) handleOrganizationsPage(c fiber.Ctx) error {
	organizations := []api.OrganizationWithRole{}
	hasMore := false
	nextCursor := ""
	prevCursor := ""
	pageError := ""
	params := &api.ListOrganizationsParams{}

	limit := api.LimitParam(20)
	params.Limit = &limit

	cursor := strings.TrimSpace(c.Query("cursor"))
	if cursor != "" {
		cursorParam := cursor
		params.Cursor = &cursorParam
	}

	orgList, status, errorResponse, err := r.listOrganizationsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		params,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = fmt.Sprintf("Failed to load organizations (status %d).", status)
		}

		pageError = message
		shellData := r.buildShellDataForDashboard(
			c,
			"Organizations",
			organizations,
			pageError,
			errorPopupDetails(errorResponse, ""),
		)
		shellData.Subtitle = "Select an organization to manage members, data sources, queries, and runs."
		return renderTempl(
			c,
			views.OrganizationsPage(
				shellData,
				organizations,
				hasMore,
				nextCursor,
				prevCursor,
				shellData.ActiveOrganizationSlug,
			),
		)
	}

	organizations = orgList.Organizations
	hasMore = orgList.HasMore
	if orgList.NextCursor != nil {
		nextCursor = *orgList.NextCursor
	}
	if orgList.PrevCursor != nil {
		prevCursor = *orgList.PrevCursor
	}

	shellData := r.buildShellDataForDashboard(c, "Organizations", organizations, pageError, "")
	shellData.Subtitle = "Select an organization to manage members, data sources, queries, and runs."

	return renderTempl(
		c,
		views.OrganizationsPage(
			shellData,
			organizations,
			hasMore,
			nextCursor,
			prevCursor,
			shellData.ActiveOrganizationSlug,
		),
	)
}

func (r *Router) handleOrganizationDetailPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	activeOrganizationSlug := reqctx.GetActiveOrganizationSlug(c, r.sessionStore).String()

	organization, status, errorResponse, getErr := r.getOrganizationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if status < 400 || status >= 600 {
			status = fiber.StatusInternalServerError
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			if status == fiber.StatusNotFound {
				message = organizationNotFoundMessage
			} else {
				message = fmt.Sprintf("Failed to load organization (status %d).", status)
			}
		}

		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Organization details",
			orgSlug.String(),
			message,
			errorPopupDetails(errorResponse, ""),
			c.Get(fiber.HeaderCookie),
		)
		shellData.Breadcrumbs = []views.BreadcrumbItem{
			{Label: "Organizations", Href: organizationsPath},
			{Label: orgSlug.String(), Href: "/organizations/" + orgSlug.String()},
			{Label: "Overview"},
		}

		c.Status(status)
		return renderTempl(
			c,
			views.OrganizationDetailUnavailablePage(shellData, orgSlug.String(), message),
		)
	}
	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Organization details",
		orgSlug.String(),
		"",
		"",
		c.Get(fiber.HeaderCookie),
	)
	shellData.Breadcrumbs = []views.BreadcrumbItem{
		{Label: "Organizations", Href: organizationsPath},
		{Label: organization.Name, Href: "/organizations/" + organization.Slug},
		{Label: "Overview"},
	}

	return renderTempl(
		c,
		views.OrganizationDetailPage(
			shellData,
			*organization,
			activeOrganizationSlug,
			r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String()).Can("organizations", "delete"),
		),
	)
}

func (r *Router) handleOrganizationRetentionPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	organization, status, errorResponse, getErr := r.getOrganizationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if status < 400 || status >= 600 {
			status = fiber.StatusInternalServerError
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			if status == fiber.StatusNotFound {
				message = organizationNotFoundMessage
			} else {
				message = fmt.Sprintf("Failed to load organization (status %d).", status)
			}
		}
		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Organization retention",
			orgSlug.String(),
			message,
			errorPopupDetails(errorResponse, ""),
			c.Get(fiber.HeaderCookie),
		)
		shellData.Breadcrumbs = []views.BreadcrumbItem{
			{Label: "Organizations", Href: organizationsPath},
			{Label: orgSlug.String(), Href: "/organizations/" + orgSlug.String()},
			{Label: "Retention"},
		}
		c.Status(status)
		return renderTempl(c, views.OrganizationDetailUnavailablePage(shellData, orgSlug.String(), message))
	}

	cookieHeader := c.Get(fiber.HeaderCookie)
	retentionPolicy, policyStatus, _, policyErr := r.getOrganizationRetentionPolicyViaAPI(
		c.Context(),
		cookieHeader,
		orgSlug.String(),
	)
	if policyErr != nil && policyStatus == fiber.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	policyError := ""
	retentionSaved := c.Query("retention") == "saved"
	if c.Query("retention") == "error" {
		policyError = strings.TrimSpace(c.Query("retention_error"))
		if policyError == "" {
			policyError = "Failed to update retention policy."
		}
	}
	if policyErr != nil {
		policyError = "Retention policy is temporarily unavailable."
		retentionSaved = false
	}

	retentionRuns, runsStatus, _, runsErr := r.listOrganizationRetentionRunsViaAPI(
		c.Context(),
		cookieHeader,
		orgSlug.String(),
	)
	if runsErr != nil && runsStatus == fiber.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	runsError := ""
	if runsErr != nil {
		runsError = "Maintenance history is temporarily unavailable."
	}

	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		cookieHeader,
		orgSlug.String(),
	)
	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Organization retention",
		orgSlug.String(),
		"",
		"",
		cookieHeader,
	)
	shellData.Subtitle = "Manage organization retention windows and review system-managed maintenance."
	shellData.Breadcrumbs = []views.BreadcrumbItem{
		{Label: "Organizations", Href: organizationsPath},
		{Label: organization.Name, Href: "/organizations/" + organization.Slug},
		{Label: "Retention"},
	}

	return renderTempl(
		c,
		views.OrganizationRetentionPage(
			shellData,
			*organization,
			retentionPolicy,
			retentionRuns,
			policyError,
			runsError,
			retentionSaved,
			permissions.Can("organizations", "update"),
		),
	)
}

func (r *Router) handleOrganizationRetentionSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	parseDays := func(name string) (int, error) {
		value, err := strconv.Atoi(strings.TrimSpace(c.FormValue(name)))
		if err != nil {
			return 0, xerror.InvalidInput(xerror.Organization).Detailf("%s must be a number", name)
		}
		return value, nil
	}
	queryExecutionDays, err := parseDays("query_execution_retention_days")
	if err != nil {
		return redirectToOrganizationRetentionError(c, orgSlug, err.Error())
	}
	replayCheckpointDays, err := parseDays("replay_checkpoint_retention_days")
	if err != nil {
		return redirectToOrganizationRetentionError(c, orgSlug, err.Error())
	}
	runHistoryDays, err := parseDays("run_history_retention_days")
	if err != nil {
		return redirectToOrganizationRetentionError(c, orgSlug, err.Error())
	}
	eventLogDays, err := parseDays("event_log_retention_days")
	if err != nil {
		return redirectToOrganizationRetentionError(c, orgSlug, err.Error())
	}
	policy := api.OrganizationRetentionPolicy{
		QueryExecutionRetentionDays:   queryExecutionDays,
		ReplayCheckpointRetentionDays: replayCheckpointDays,
		RunHistoryRetentionDays:       runHistoryDays,
		EventLogRetentionDays:         eventLogDays,
	}
	_, status, errorResponse, updateErr := r.updateOrganizationRetentionPolicyViaAPI(
		c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), policy,
	)
	if updateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Failed to update retention policy."
		}
		return redirectToOrganizationRetentionError(c, orgSlug, message)
	}
	return c.Redirect().Status(fiber.StatusSeeOther).To("/organizations/" + orgSlug.String() + "/retention?retention=saved")
}

func redirectToOrganizationRetentionError(c fiber.Ctx, orgSlug slug.Slug, message string) error {
	query := url.Values{
		"retention":       {"error"},
		"retention_error": {message},
	}
	return c.Redirect().Status(fiber.StatusSeeOther).To("/organizations/" + orgSlug.String() + "/retention?" + query.Encode())
}

func (r *Router) handleOrganizationProjectsPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	selectedProjectSlug := strings.TrimSpace(c.Query("project_slug"))
	if isHTMXRequest(c) && c.Get("HX-History-Restore-Request") != "true" && c.Get("HX-Target") == "project-quick-look" {
		organization := api.Organization{Slug: orgSlug.String()}
		if selectedProjectSlug == "" {
			return renderTempl(c, views.OrganizationProjectDetailPanelSlot(organization, nil, "", views.ProjectPipelineBackendControls{}, views.ProjectOperationalContext{}, views.DefaultOrganizationPermissions()))
		}
		if _, err := slug.New(selectedProjectSlug); err != nil {
			return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
		}
		project, status, errorResponse, err := r.getProjectViaAPI(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), selectedProjectSlug)
		if err != nil {
			if status == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}
			if status < fiber.StatusBadRequest {
				status = fiber.StatusInternalServerError
			}
			setHTMLResponseStatus(c, status)
			return renderTempl(c, views.ProjectQuickLookUnavailable(messageFromErrorResponse(errorResponse)))
		}
		projectContext := views.ProjectOperationalContext{}
		environments, status, _, err := r.listProjectEnvironmentsViaAPI(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), selectedProjectSlug)
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if err != nil {
			projectContext.EnvironmentsUnavailable = true
		} else if environments != nil {
			projectContext.Environments = environments.ProjectEnvironments
		}
		permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String())
		return renderTempl(c, views.OrganizationProjectDetailPanelSlot(organization, []api.Project{*project}, selectedProjectSlug, views.ProjectPipelineBackendControls{}, projectContext, permissions))
	}

	return r.renderOrganizationProjectsPage(
		c,
		orgSlug,
		http.StatusOK,
		selectedProjectSlug,
		strings.TrimSpace(c.Query("data_source_slug")),
		strings.TrimSpace(c.Query("tracked_branch")),
		strings.TrimSpace(c.Query("error")),
		strings.TrimSpace(c.Query("error_details")),
		"",
		views.DefaultProjectCreateFormData(),
	)
}

// handleOrganizationProjectDetailPage renders the full-page project operational context.
func (r *Router) handleOrganizationProjectDetailPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	return r.renderOrganizationProjectFullPage(
		c,
		orgSlug,
		projectSlug,
		http.StatusOK,
		strings.TrimSpace(c.Query("data_source_slug")),
		strings.TrimSpace(c.Query("tracked_branch")),
		strings.TrimSpace(c.Query("error")),
		strings.TrimSpace(c.Query("error_details")),
		"",
	)
}

// renderOrganizationProjectFullPage loads one project and renders its full-page operational context.
func (r *Router) renderOrganizationProjectFullPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	statusCode int,
	selectedBackendDataSourceSlug string,
	selectedTrackedBranch string,
	contentErrorSummary string,
	contentErrorDetails string,
	contentSuccessSummary string,
) error {
	if c.Method() == fiber.MethodGet && c.Query("repository_setup") == "failed" && contentErrorSummary == "" {
		contentErrorSummary = "GitHub authorization could not be completed. Your repository target is saved. Resume setup to try again."
	}
	if c.Method() == fiber.MethodGet && contentErrorSummary == "" {
		switch c.Query("gitlab_setup") {
		case "denied":
			contentErrorSummary = "GitLab authorization was declined. Your repository target is saved; resume setup to try again."
		case "expired":
			contentErrorSummary = "GitLab authorization expired. Resume setup to authorize again."
		case "stale":
			contentErrorSummary = "This GitLab authorization belongs to an earlier setup. Resume the current repository target."
		case "error":
			contentErrorSummary = "GitLab authorization or repository setup could not be completed. Resume setup to continue."
		}
	}
	organization, status, errorResponse, getOrgErr := r.getOrganizationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if getOrgErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if status < 400 || status >= 600 {
			status = fiber.StatusInternalServerError
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			if status == fiber.StatusNotFound {
				message = organizationNotFoundMessage
			} else {
				message = fmt.Sprintf("Failed to load organization (status %d).", status)
			}
		}
		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Project details",
			orgSlug.String(),
			message,
			backendErrorDetails("get organization", status, errorResponse, getOrgErr),
			c.Get(fiber.HeaderCookie),
		)
		c.Status(status)
		return renderTempl(c, views.OrganizationDetailUnavailablePage(shellData, orgSlug.String(), message))
	}

	project, status, errorResponse, getProjectErr := r.getProjectViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug.String(),
	)
	if getProjectErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if status < 400 || status >= 600 {
			status = fiber.StatusInternalServerError
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			if status == fiber.StatusNotFound {
				message = "project not found"
			} else {
				message = fmt.Sprintf("Failed to load project (status %d).", status)
			}
		}
		shellData := r.buildShellDataForProjectPage(
			c,
			"Project details",
			orgSlug.String(),
			projectSlug.String(),
			message,
			backendErrorDetails("get project", status, errorResponse, getProjectErr),
			c.Get(fiber.HeaderCookie),
		)
		c.Status(status)
		return renderTempl(c, views.OrganizationDetailUnavailablePage(shellData, orgSlug.String(), message))
	}

	shellData := r.buildShellDataForProjectPage(
		c,
		project.Name,
		orgSlug.String(),
		projectSlug.String(),
		"",
		"",
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Review pipeline backend, runtime enablement, pipelines, and logical bindings for this project."
	shellData.Breadcrumbs = []views.BreadcrumbItem{
		{Label: "Organizations", Href: organizationsPath},
		{Label: organization.Name, Href: "/organizations/" + organization.Slug},
		{Label: "Projects", Href: "/organizations/" + organization.Slug + "/projects"},
		{Label: project.Name},
	}

	projects := []api.Project{*project}
	backendControls, backendDataSources, unauthorized := r.loadProjectPipelineBackendControls(
		c,
		orgSlug,
		projects,
		projectSlug.String(),
		selectedBackendDataSourceSlug,
		selectedTrackedBranch,
	)
	if unauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	backendControls.PipelineDir = "pipelines"
	if project.PipelineBackend != nil && strings.TrimSpace(project.PipelineBackend.PipelineDir) != "" {
		backendControls.PipelineDir = project.PipelineBackend.PipelineDir
	}
	if value := strings.TrimSpace(c.FormValue("pipeline_dir", c.Query("pipeline_dir"))); value != "" {
		backendControls.PipelineDir = value
	}
	backendControls.ProviderOwner = strings.TrimSpace(c.FormValue("provider_owner"))
	backendControls.RepositoryName = strings.TrimSpace(c.FormValue("repository_name"))
	backendControls.CreationBranch = "main"
	if value := strings.TrimSpace(c.FormValue("tracked_branch")); (c.FormValue("mode") == string(api.SetProjectPipelineBackendRequestModeCreateFromTemplate) || c.FormValue("mode") == "managed_create") && value != "" {
		backendControls.CreationBranch = value
	}
	if project.PipelineBackend == nil {
		r.loadManagedRepositorySetup(c, orgSlug.String(), projectSlug.String(), &backendControls)
	}
	projectContext, unauthorized := r.loadProjectOperationalContext(c, orgSlug, projectSlug.String(), backendDataSources, backendControls.DataSourceLoadError != "")
	if unauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	setHTMLResponseStatus(c, statusCode)
	return renderTempl(
		c,
		views.OrganizationProjectFullPage(
			shellData,
			*organization,
			*project,
			backendControls,
			projectContext,
			permissions,
			contentErrorSummary,
			contentErrorDetails,
			contentSuccessSummary,
		),
	)
}

// handleCreateProjectSubmit validates the SSR create form, creates an organization project, and selects it.
func (r *Router) handleCreateProjectSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	formData := projectCreateFormDataFromRequest(c)
	if strings.TrimSpace(formData.Name) == "" {
		return r.renderOrganizationProjectsPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			"",
			"",
			"",
			"Project could not be created.",
			"Project name is required.",
			"",
			formData,
		)
	}

	request := api.NewProject{Name: formData.Name}
	if formData.Slug != "" {
		projectSlug, slugErr := slug.New(formData.Slug)
		if slugErr != nil {
			return r.renderOrganizationProjectsPage(
				c,
				orgSlug,
				http.StatusBadRequest,
				"",
				"",
				"",
				"Project could not be created.",
				"Project slug must use lowercase letters, digits, and hyphens only.",
				"",
				formData,
			)
		}
		apiSlug := projectSlug.String()
		request.Slug = &apiSlug
	}

	project, status, errorResponse, createErr := r.createProjectViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		request,
	)
	if createErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Project could not be created."
		}
		return r.renderOrganizationProjectsPage(
			c,
			orgSlug,
			status,
			"",
			"",
			"",
			message,
			backendErrorDetails("create project", status, errorResponse, createErr),
			"",
			formData,
		)
	}

	selectedProjectSlug := ""
	if project != nil {
		selectedProjectSlug = project.Slug
	}
	return redirectHTMXOrSeeOther(c, organizationProjectsSelectionHref(orgSlug.String(), selectedProjectSlug))
}

// handleDeleteProjectSubmit removes an organization project and returns the portfolio to its unselected state.
func (r *Router) handleDeleteProjectSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	status, errorResponse, deleteErr := r.deleteProjectViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug.String(),
	)
	if deleteErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Project could not be deleted."
		}
		if projectReturnToFullPage(c) {
			return r.renderOrganizationProjectFullPage(
				c,
				orgSlug,
				projectSlug,
				status,
				"",
				"",
				message,
				backendErrorDetails("delete project", status, errorResponse, deleteErr),
				"",
			)
		}
		return r.renderOrganizationProjectsPage(
			c,
			orgSlug,
			status,
			projectSlug.String(),
			"",
			"",
			message,
			backendErrorDetails("delete project", status, errorResponse, deleteErr),
			"",
			views.DefaultProjectCreateFormData(),
		)
	}

	return redirectHTMXOrSeeOther(c, views.ProjectDeletedListHref(views.WithEntityPageURL(c.Context(), c.OriginalURL()), orgSlug.String()))
}

// renderOrganizationProjectsPage loads project portfolio data and renders the shared project workspace region.
func (r *Router) renderOrganizationProjectsPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	statusCode int,
	selectedProjectSlug string,
	selectedBackendDataSourceSlug string,
	selectedTrackedBranch string,
	contentErrorSummary string,
	contentErrorDetails string,
	contentSuccessSummary string,
	createFormData views.ProjectCreateFormData,
) error {
	organization, status, errorResponse, getErr := r.getOrganizationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if status < 400 || status >= 600 {
			status = fiber.StatusInternalServerError
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			if status == fiber.StatusNotFound {
				message = organizationNotFoundMessage
			} else {
				message = fmt.Sprintf("Failed to load organization (status %d).", status)
			}
		}

		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Projects",
			orgSlug.String(),
			message,
			errorPopupDetails(errorResponse, ""),
			c.Get(fiber.HeaderCookie),
		)
		shellData.Subtitle = "Choose the project scope for pipelines, runs, runtime environments, and logical binding context."

		c.Status(status)
		return renderTempl(
			c,
			views.OrganizationDetailUnavailablePage(shellData, orgSlug.String(), message),
		)
	}

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Projects",
		orgSlug.String(),
		"",
		"",
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Choose the project scope for pipelines, runs, runtime environments, and logical binding context."

	projects := []api.Project{}
	projectList, projectStatus, projectErrorResponse, projectErr := r.listProjectsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if projectErr != nil {
		if projectStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		contentErrorSummary = messageFromErrorResponse(projectErrorResponse)
		if contentErrorSummary == "" {
			contentErrorSummary = "Projects are temporarily unavailable."
		}
		contentErrorDetails = backendErrorDetails(
			"list projects",
			projectStatus,
			projectErrorResponse,
			projectErr,
		)
		if statusCode == http.StatusOK {
			statusCode = projectStatus
		}
	} else if projectList != nil {
		projects = projectList.Projects
	}

	selectedProjectSlug = normalizeSelectedProjectSlug(projects, selectedProjectSlug)

	backendControls, backendDataSources, unauthorized := r.loadProjectPipelineBackendControls(
		c,
		orgSlug,
		projects,
		selectedProjectSlug,
		selectedBackendDataSourceSlug,
		selectedTrackedBranch,
	)
	if unauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	projectContext, unauthorized := r.loadProjectOperationalContext(c, orgSlug, selectedProjectSlug, backendDataSources, backendControls.DataSourceLoadError != "")
	if unauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	setHTMLResponseStatus(c, statusCode)
	return renderTempl(
		c,
		views.OrganizationProjectsPage(
			shellData,
			*organization,
			projects,
			selectedProjectSlug,
			backendControls,
			projectContext,
			permissions,
			createFormData,
			contentErrorSummary,
			contentErrorDetails,
			contentSuccessSummary,
		),
	)
}

// loadProjectPipelineBackendControls loads source choices without waiting for remote branches.
func (r *Router) loadProjectPipelineBackendControls(
	c fiber.Ctx,
	orgSlug slug.Slug,
	projects []api.Project,
	selectedProjectSlug string,
	requestedDataSourceSlug string,
	requestedTrackedBranch string,
) (views.ProjectPipelineBackendControls, []api.DataSource, bool) {
	controls := views.ProjectPipelineBackendControls{}
	selectedProject := organizationProjectBySlug(projects, selectedProjectSlug)
	if selectedProject == nil {
		return controls, nil, false
	}

	controls.SelectedDataSource = views.ProjectPipelineBackendSelectedDataSourceSlug(
		*selectedProject,
		requestedDataSourceSlug,
	)
	controls.SelectedBranch = views.ProjectPipelineBackendSelectedTrackedBranch(
		*selectedProject,
		nil,
		controls.SelectedDataSource,
		requestedTrackedBranch,
	)

	dataSources, status, errorResponse, err := r.listProjectPipelineBackendDataSourcesViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return controls, nil, true
		}
		controls.DataSourceLoadError = "Git backend sources are temporarily unavailable."
		if message := messageFromErrorResponse(errorResponse); message != "" {
			controls.DataSourceLoadError = message
		}
		controls.DataSourceLoadDetails = backendErrorDetails(
			"list project backend data sources",
			status,
			errorResponse,
			err,
		)
		return controls, nil, false
	}

	controls.DataSourceOptions = views.ProjectPipelineBackendDataSourceOptions(dataSources)
	if controls.SelectedDataSource == "" {
		return controls, dataSources, false
	}
	if !projectBackendDataSourceOptionExists(controls.DataSourceOptions, controls.SelectedDataSource) {
		selectedDataSource, selectedStatus, selectedErrorResponse, selectedErr := r.getDataSourceViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			controls.SelectedDataSource,
		)
		if selectedErr != nil {
			if selectedStatus == fiber.StatusUnauthorized {
				return controls, dataSources, true
			}
			controls.BranchLoadError = "Selected Git data source is not available."
			controls.BranchLoadDetails = backendErrorDetails(
				"load selected project backend data source",
				selectedStatus,
				selectedErrorResponse,
				selectedErr,
			)
			return controls, dataSources, false
		}
		selectedOptions := views.ProjectPipelineBackendDataSourceOptions([]api.DataSource{*selectedDataSource})
		if len(selectedOptions) == 0 {
			controls.BranchLoadError = "Selected Git data source is not available."
			return controls, dataSources, false
		}
		controls.DataSourceOptions = append(controls.DataSourceOptions, selectedOptions[0])
	}

	controls.BranchesPending = true
	return controls, dataSources, false
}

func (r *Router) handleProjectBackendBranches(c fiber.Ctx) error {
	orgSlug, projectSlug, ok := projectActionSlugs(c)
	if !ok {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	project, status, _, err := r.getProjectViaAPI(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), projectSlug.String())
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if status < 400 || status >= 600 {
			status = fiber.StatusInternalServerError
		}
		return c.SendStatus(status)
	}
	controls := views.ProjectPipelineBackendControls{}
	controls.SelectedDataSource = views.ProjectPipelineBackendSelectedDataSourceSlug(*project, c.Query("data_source_slug"))
	controls.SelectedBranch = views.ProjectPipelineBackendSelectedTrackedBranch(*project, nil, controls.SelectedDataSource, c.Query("tracked_branch"))
	if controls.SelectedDataSource != "" {
		if _, err := slug.New(controls.SelectedDataSource); err != nil {
			return c.SendStatus(fiber.StatusBadRequest)
		}
		var unauthorized bool
		controls, unauthorized = r.loadProjectBackendBranches(c, orgSlug, *project, controls, c.Query("tracked_branch"))
		if unauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
	}
	return renderTempl(c, views.ProjectBackendBranchControl(orgSlug.String(), *project, controls))
}

func (r *Router) loadProjectBackendBranches(c fiber.Ctx, orgSlug slug.Slug, project api.Project, controls views.ProjectPipelineBackendControls, requestedTrackedBranch string) (views.ProjectPipelineBackendControls, bool) {
	branchList, branchStatus, branchErrorResponse, branchErr := r.listDataSourceBranchesViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		controls.SelectedDataSource,
	)
	if branchErr != nil {
		if branchStatus == fiber.StatusUnauthorized {
			return controls, true
		}
		controls.BranchLoadError = "Branches are temporarily unavailable for the selected data source."
		if message := messageFromErrorResponse(branchErrorResponse); message != "" {
			controls.BranchLoadError = message
		}
		controls.BranchLoadDetails = backendErrorDetails(
			"list project backend branches",
			branchStatus,
			branchErrorResponse,
			branchErr,
		)
		return controls, false
	}

	controls.Branches = branchList.Branches
	branchOptions := views.ProjectPipelineBackendBranchOptions(
		project,
		controls,
		controls.SelectedDataSource,
	)
	controls.SelectedBranch = views.ProjectPipelineBackendSelectedTrackedBranch(
		project,
		branchOptions,
		controls.SelectedDataSource,
		requestedTrackedBranch,
	)
	return controls, false
}

// listProjectPipelineBackendDataSourcesViaAPI loads every data-source page used by the backend selector.
func (r *Router) listProjectPipelineBackendDataSourcesViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
) ([]api.DataSource, int, *api.ErrorResponse, error) {
	return r.listAllDataSourcesViaAPI(ctx, cookieHeader, organizationSlug)
}

// loadProjectOperationalContext loads read-only project summary data for the selected project panels.
func (r *Router) loadProjectOperationalContext(
	c fiber.Ctx,
	orgSlug slug.Slug,
	projectSlug string,
	dataSources []api.DataSource,
	dataSourcesUnavailable bool,
) (views.ProjectOperationalContext, bool) {
	context := views.ProjectOperationalContext{
		Bindings:                       map[string][]api.DataSourceBinding{},
		BindingUnavailableEnvironments: map[string]bool{},
	}
	projectSlug = strings.TrimSpace(projectSlug)
	if projectSlug == "" {
		return context, false
	}

	catalog, status, _, err := r.listEnvironmentsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return context, true
		}
		context.CatalogUnavailable = true
	} else if catalog != nil {
		context.Catalog = catalog.Environments
	}

	projectEnvironments, status, _, err := r.listProjectEnvironmentsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return context, true
		}
		context.EnvironmentsUnavailable = true
		context.BindingsUnavailable = true
	} else if projectEnvironments != nil {
		context.Environments = projectEnvironments.ProjectEnvironments
	}

	pipelines, status, _, err := r.listAllPipelinesViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return context, true
		}
		context.PipelinesUnavailable = true
		context.PipelineDetailsUnavailable = true
	} else if pipelines != nil {
		context.Pipelines = pipelines
		if len(context.Pipelines) > 5 {
			context.Pipelines = context.Pipelines[:5]
		}
		details, detailsUnavailable, detailsUnauthorized := r.projectPipelineDetailsViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			projectSlug,
			pipelines,
		)
		if detailsUnauthorized {
			return context, true
		}
		context.PipelineDetails = details
		context.PipelineDetailsUnavailable = detailsUnavailable
	}

	refs, status, _, err := r.listDataSourceRefsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return context, true
		}
		context.BindingsUnavailable = true
		return context, false
	}
	if refs != nil {
		context.DataSourceRefs = refs.DataSourceRefs
	}

	context.DataSources = dataSources
	context.DataSourcesUnavailable = dataSourcesUnavailable

	if context.EnvironmentsUnavailable {
		context.BindingsUnavailable = true
		return context, false
	}
	for _, environment := range context.Environments {
		bindings, status, _, err := r.listDataSourceBindingsViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			projectSlug,
			environment.EnvironmentSlug,
		)
		if err != nil {
			if status == fiber.StatusUnauthorized {
				return context, true
			}
			context.BindingUnavailableEnvironments[environment.EnvironmentSlug] = true
			continue
		}
		if bindings != nil {
			context.Bindings[environment.EnvironmentSlug] = bindings.DataSourceBindings
		}
	}
	return context, false
}

// projectPipelineDetailsViaAPI loads canonical pipeline specs with bounded request-path concurrency.
func (r *Router) projectPipelineDetailsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	pipelines []api.PipelineRef,
) ([]api.Pipeline, bool, bool) {
	type detailTask struct {
		index        int
		pipelineSlug string
	}
	type detailResult struct {
		index    int
		pipeline *api.Pipeline
		status   int
		err      error
	}

	workerCount := maxProjectPipelineDetailLookupParallel
	if len(pipelines) < workerCount {
		workerCount = len(pipelines)
	}
	if workerCount == 0 {
		return nil, false, false
	}

	tasks := make(chan detailTask)
	results := make(chan detailResult)
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range tasks {
				pipeline, status, _, err := r.getPipelineViaAPI(
					ctx,
					cookieHeader,
					organizationSlug,
					projectSlug,
					task.pipelineSlug,
				)
				results <- detailResult{
					index:    task.index,
					pipeline: pipeline,
					status:   status,
					err:      err,
				}
			}
		}()
	}
	go func() {
		for index, pipelineRef := range pipelines {
			pipelineSlug := strings.TrimSpace(pipelineRef.Slug)
			if pipelineSlug == "" {
				continue
			}
			tasks <- detailTask{
				index:        index,
				pipelineSlug: pipelineSlug,
			}
		}
		close(tasks)
		wg.Wait()
		close(results)
	}()

	detailsByIndex := map[int]api.Pipeline{}
	detailsUnavailable := false
	detailsUnauthorized := false
	for result := range results {
		if result.err != nil {
			if result.status == fiber.StatusUnauthorized {
				detailsUnauthorized = true
				continue
			}
			detailsUnavailable = true
			continue
		}
		if result.pipeline != nil {
			detailsByIndex[result.index] = *result.pipeline
		}
	}

	details := make([]api.Pipeline, 0, len(detailsByIndex))
	for index := range pipelines {
		if pipeline, ok := detailsByIndex[index]; ok {
			details = append(details, pipeline)
		}
	}
	return details, detailsUnavailable, detailsUnauthorized
}

// organizationProjectBySlug finds the selected project without changing empty-selection semantics.
func organizationProjectBySlug(projects []api.Project, selectedProjectSlug string) *api.Project {
	selectedProjectSlug = strings.TrimSpace(selectedProjectSlug)
	if selectedProjectSlug == "" {
		return nil
	}
	for idx := range projects {
		if projects[idx].Slug == selectedProjectSlug {
			return &projects[idx]
		}
	}
	return nil
}

func normalizeSelectedProjectSlug(projects []api.Project, selectedProjectSlug string) string {
	selectedProject := organizationProjectBySlug(projects, selectedProjectSlug)
	if selectedProject == nil {
		return ""
	}
	return selectedProject.Slug
}

// projectBackendDataSourceOptionExists reports whether a selected source is an available Git backend option.
func projectBackendDataSourceOptionExists(
	options []views.ProjectPipelineBackendDataSourceOption,
	selectedDataSourceSlug string,
) bool {
	selectedDataSourceSlug = strings.TrimSpace(selectedDataSourceSlug)
	for _, option := range options {
		if option.Slug == selectedDataSourceSlug {
			return true
		}
	}
	return false
}

// projectCreateFormDataFromRequest normalizes create-project form values before rendering or API submission.
func projectCreateFormDataFromRequest(c fiber.Ctx) views.ProjectCreateFormData {
	return views.ProjectCreateFormData{
		Name: strings.TrimSpace(c.FormValue("name")),
		Slug: strings.TrimSpace(c.FormValue("slug")),
	}
}

// organizationProjectsSelectionHref builds the canonical portfolio URL with an optional selected project.
func organizationProjectsSelectionHref(orgSlug, projectSlug string) string {
	base := "/organizations/" + strings.TrimSpace(orgSlug) + "/projects"
	if strings.TrimSpace(projectSlug) == "" {
		return base
	}
	values := url.Values{}
	values.Set("project_slug", strings.TrimSpace(projectSlug))
	return base + "?" + values.Encode()
}

func (r *Router) handleUpdateProjectPipelineBackendSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	renderResponse := func(statusCode int, selectedDataSourceSlug, selectedBranch, errorSummary, errorDetails, successSummary string) error {
		if projectReturnToFullPage(c) {
			return r.renderOrganizationProjectFullPage(
				c,
				orgSlug,
				projectSlug,
				statusCode,
				selectedDataSourceSlug,
				selectedBranch,
				errorSummary,
				errorDetails,
				successSummary,
			)
		}
		return r.renderOrganizationProjectsPage(
			c,
			orgSlug,
			statusCode,
			projectSlug.String(),
			selectedDataSourceSlug,
			selectedBranch,
			errorSummary,
			errorDetails,
			successSummary,
			views.DefaultProjectCreateFormData(),
		)
	}

	if c.FormValue("mode") == "managed_abandon" {
		setup, err := slug.New(c.FormValue("setup_slug"))
		if err != nil {
			return renderResponse(http.StatusBadRequest, "", "", "Choose the saved repository setup before changing its target.", "", "")
		}
		response, err := r.apiClient.AbandonProjectRepositorySetupWithResponse(c.Context(), orgSlug.String(), projectSlug.String(), api.AbandonProjectRepositorySetupRequest{
			SetupSlug: setup.String(), PreserveResources: api.AbandonProjectRepositorySetupRequestPreserveResources(c.FormValue("preserve_resources") == "on"),
		}, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
		if err != nil {
			return renderResponse(http.StatusBadGateway, "", "", "The target could not be changed. Reload this page to check setup progress.", "", "")
		}
		if response.StatusCode() == http.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if response.StatusCode() != http.StatusOK || response.JSON200 == nil || response.JSON200.AbandonedAt == nil {
			message := messageFromErrorResponse(parseAPIErrorResponse(response.Body))
			if message == "" {
				message = "The target could not be changed. A running or connected setup must finish before choosing another target."
			}
			status := response.StatusCode()
			if status < 400 {
				status = http.StatusBadGateway
			}
			return renderResponse(status, "", "", message, "", "")
		}
		return redirectHTMXOrSeeOther(c, "/organizations/"+orgSlug.String()+"/projects/"+projectSlug.String()+"?section=repository&repository_path=create&previous_setup="+setup.String())
	}

	if c.FormValue("mode") == "managed_create" {
		provider := api.CreateProjectRepositoryRequestProvider(c.FormValue("repository_provider"))
		trackedBranch, pipelineDir := strings.TrimSpace(c.FormValue("tracked_branch")), strings.TrimSpace(c.FormValue("pipeline_dir"))
		request := api.CreateProjectRepositoryRequest{Provider: provider, Owner: strings.TrimSpace(c.FormValue("provider_owner")), RepositoryName: strings.TrimSpace(c.FormValue("repository_name")), TrackedBranch: &trackedBranch, PipelineDir: &pipelineDir, CreatePrivateRepository: api.CreateProjectRepositoryRequestCreatePrivateRepository(c.FormValue("create_private_repository") == "on")}
		response, err := r.apiClient.CreateProjectRepositoryWithResponse(c.Context(), orgSlug.String(), projectSlug.String(), request, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
		if err != nil {
			return renderResponse(http.StatusBadGateway, "", "", "Repository setup could not be completed.", "Try again with the same details to resume.", "")
		}
		if response.StatusCode() == http.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
			message := messageFromErrorResponse(parseAPIErrorResponse(response.Body))
			if message == "" {
				message = "Repository setup could not be completed. Retry with the same details to resume."
			}
			status := response.StatusCode()
			if status < 400 {
				status = http.StatusBadGateway
			}
			return renderResponse(status, "", "", message, "", "")
		}
		setup := response.JSON200
		if setup.AuthorizationUrl != nil && *setup.AuthorizationUrl != "" {
			authorizationURL, parseErr := url.Parse(*setup.AuthorizationUrl)
			providerHost := "github.com"
			if provider == api.CreateProjectRepositoryRequestProviderGitlab {
				providerHost = "gitlab.com"
			}
			if parseErr != nil || authorizationURL.Scheme != "https" || authorizationURL.Host != providerHost || authorizationURL.User != nil {
				return renderResponse(http.StatusBadGateway, "", "", "Provider authorization is unavailable.", "", "")
			}
			if provider == api.CreateProjectRepositoryRequestProviderGithub {
				c.Cookie(&fiber.Cookie{Name: githubInstallOrganizationCookie, Value: orgSlug.String(), Path: "/github/callback", MaxAge: 600, HTTPOnly: true, Secure: r.secureCookies, SameSite: fiber.CookieSameSiteLaxMode})
				c.Cookie(&fiber.Cookie{Name: githubInstallProjectCookie, Value: projectSlug.String(), Path: "/github/callback", MaxAge: 600, HTTPOnly: true, Secure: r.secureCookies, SameSite: fiber.CookieSameSiteLaxMode})
			}
			return redirectHTMXOrSeeOther(c, *setup.AuthorizationUrl)
		}
		if setup.Status != api.ProjectRepositorySetupStatusAttached {
			return renderResponse(http.StatusConflict, "", "", "Repository setup is incomplete.", "Retry with the same details to resume.", "")
		}
		return renderResponse(http.StatusOK, "", "", "", "", "Private repository created and connected. You can now create a pipeline.")
	}

	rawDataSourceSlug := strings.TrimSpace(c.FormValue("data_source_slug"))
	if rawDataSourceSlug == "" {
		return renderResponse(
			http.StatusBadRequest,
			rawDataSourceSlug,
			strings.TrimSpace(c.FormValue("tracked_branch")),
			"Pipeline backend settings could not be updated.",
			"Data source slug is required.",
			"",
		)
	}
	validatedDataSourceSlug, slugErr := slug.New(rawDataSourceSlug)
	if slugErr != nil {
		return renderResponse(
			http.StatusBadRequest,
			rawDataSourceSlug,
			strings.TrimSpace(c.FormValue("tracked_branch")),
			"Pipeline backend settings could not be updated.",
			"Invalid data source slug.",
			"",
		)
	}

	trackedBranch := strings.TrimSpace(c.FormValue("tracked_branch"))
	if trackedBranch == "" {
		return renderResponse(
			http.StatusBadRequest,
			rawDataSourceSlug,
			trackedBranch,
			"Pipeline backend settings could not be updated.",
			"Tracked branch is required.",
			"",
		)
	}

	mode := strings.TrimSpace(c.FormValue("mode"))
	if mode == "" {
		mode = string(api.SetProjectPipelineBackendRequestModeAdoptExisting)
	}
	pipelineDir := strings.TrimSpace(c.FormValue("pipeline_dir"))
	if pipelineDir == "" {
		return renderResponse(
			http.StatusBadRequest,
			rawDataSourceSlug,
			trackedBranch,
			"Pipeline backend settings could not be updated.",
			"Pipeline path is required.",
			"",
		)
	}

	payload := api.SetProjectPipelineBackendJSONRequestBody{
		DataSourceSlug: api.Slug(validatedDataSourceSlug),
		TrackedBranch:  trackedBranch,
		PipelineDir:    pipelineDir,
		Mode:           api.SetProjectPipelineBackendRequestMode(mode),
	}
	if mode == string(api.SetProjectPipelineBackendRequestModeCreateFromTemplate) {
		owner := strings.TrimSpace(c.FormValue("provider_owner"))
		repositoryName := strings.TrimSpace(c.FormValue("repository_name"))
		if owner == "" || repositoryName == "" {
			return renderResponse(http.StatusBadRequest, rawDataSourceSlug, trackedBranch,
				"Pipeline repository could not be created.",
				"Provider owner and repository name are required.", "")
		}
		payload.ProviderOwner = &owner
		payload.RepositoryName = &repositoryName
	}
	_, status, updateErrorResponse, updateErr := r.setProjectPipelineBackendViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug.String(),
		payload,
	)
	if updateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := detailedErrorFromErrorResponse(updateErrorResponse)
		if message == "" {
			message = messageFromErrorResponse(updateErrorResponse)
		}
		if message == "" {
			message = "Pipeline backend settings could not be updated."
		}
		return renderResponse(
			status,
			rawDataSourceSlug,
			trackedBranch,
			message,
			backendErrorDetails("set project pipeline backend", status, updateErrorResponse, updateErr),
			"",
		)
	}

	return renderResponse(
		http.StatusOK,
		"",
		"",
		"",
		"",
		"Pipeline repository validated and registered at an exact commit. Bind imported data-source references before validation or deployment.",
	)
}

// handleProjectOverviewEnableEnvironmentSubmit enables a runtime target from the project overview panel.
func (r *Router) handleProjectOverviewEnableEnvironmentSubmit(c fiber.Ctx) error {
	orgSlug, projectSlug, ok := projectActionSlugs(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project context")
	}
	environmentSlug, err := slug.New(c.FormValue("environment_slug"))
	if err != nil {
		return r.renderProjectActionResponse(
			c,
			orgSlug,
			projectSlug,
			http.StatusBadRequest,
			"Project environment could not be enabled.",
			"Choose a valid catalog environment.",
			"",
		)
	}

	status, errorResponse, err := r.enableProjectEnvironmentViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug.String(),
		environmentSlug.String(),
		false,
		strings.TrimSpace(c.FormValue("protected_environment_confirmation")),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Project environment could not be enabled."
		}
		return r.renderProjectActionResponse(
			c,
			orgSlug,
			projectSlug,
			status,
			message,
			backendErrorDetails("enable project environment", status, errorResponse, err),
			"",
		)
	}

	return r.renderProjectActionResponse(c, orgSlug, projectSlug, http.StatusOK, "", "", "Project environment enabled.")
}

// handleProjectOverviewSetDefaultEnvironmentSubmit sets the default runtime target from the project overview panel.
func (r *Router) handleProjectOverviewSetDefaultEnvironmentSubmit(c fiber.Ctx) error {
	orgSlug, projectSlug, ok := projectActionSlugs(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project context")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}

	status, errorResponse, err := r.enableProjectEnvironmentViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug.String(),
		environmentSlug.String(),
		true,
		strings.TrimSpace(c.FormValue("protected_environment_confirmation")),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Default environment could not be updated."
		}
		return r.renderProjectActionResponse(
			c,
			orgSlug,
			projectSlug,
			status,
			message,
			backendErrorDetails("set default project environment", status, errorResponse, err),
			"",
		)
	}

	return r.renderProjectActionResponse(c, orgSlug, projectSlug, http.StatusOK, "", "", "Default environment updated.")
}

// handleProjectOverviewDisableEnvironmentSubmit disables a runtime target from the project overview panel.
func (r *Router) handleProjectOverviewDisableEnvironmentSubmit(c fiber.Ctx) error {
	orgSlug, projectSlug, ok := projectActionSlugs(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project context")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}

	status, errorResponse, err := r.disableProjectEnvironmentViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug.String(),
		environmentSlug.String(),
		strings.TrimSpace(c.FormValue("protected_environment_confirmation")),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Project environment could not be disabled."
		}
		return r.renderProjectActionResponse(
			c,
			orgSlug,
			projectSlug,
			status,
			message,
			backendErrorDetails("disable project environment", status, errorResponse, err),
			"",
		)
	}

	return r.renderProjectActionResponse(c, orgSlug, projectSlug, http.StatusOK, "", "", "Project environment disabled.")
}

// renderProjectActionResponse rerenders the current project surface after inline project-panel mutations.
func (r *Router) renderProjectActionResponse(
	c fiber.Ctx,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	statusCode int,
	errorSummary string,
	errorDetails string,
	successSummary string,
) error {
	if projectReturnToFullPage(c) {
		return r.renderOrganizationProjectFullPage(
			c,
			orgSlug,
			projectSlug,
			statusCode,
			"",
			"",
			errorSummary,
			errorDetails,
			successSummary,
		)
	}
	return r.renderOrganizationProjectsPage(
		c,
		orgSlug,
		statusCode,
		projectSlug.String(),
		"",
		"",
		errorSummary,
		errorDetails,
		successSummary,
		views.DefaultProjectCreateFormData(),
	)
}

// projectActionSlugs validates organization and project route parameters for project-panel mutations.
func projectActionSlugs(c fiber.Ctx) (slug.Slug, slug.Slug, bool) {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return "", "", false
	}
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return "", "", false
	}
	return orgSlug, projectSlug, true
}

// projectReturnToFullPage reports whether a project form should re-render the full project surface.
func projectReturnToFullPage(c fiber.Ctx) bool {
	return strings.EqualFold(strings.TrimSpace(c.Query("return_to")), "full") ||
		strings.EqualFold(strings.TrimSpace(c.FormValue("return_to")), "full")
}

func (r *Router) listOrganizationsViaAPI(
	ctx context.Context,
	cookieHeader string,
	params *api.ListOrganizationsParams,
) (*api.OrganizationListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListOrganizationsWithResponse(
		ctx,
		params,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list organizations api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) getOrganizationViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
) (*api.Organization, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetOrganizationWithResponse(
		ctx,
		organizationSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"get organization api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) getOrganizationRetentionPolicyViaAPI(
	ctx context.Context, cookieHeader, organizationSlug string,
) (*api.OrganizationRetentionPolicy, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetOrganizationRetentionPolicyWithResponse(
		ctx, organizationSlug, frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}
	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}
	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf("get organization retention api returned status %d", resp.StatusCode())
}

func (r *Router) listOrganizationRetentionRunsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
) (*api.OrganizationRetentionRunList, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListOrganizationRetentionRunsWithResponse(
		ctx,
		organizationSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}
	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}
	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list organization retention runs api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) updateOrganizationRetentionPolicyViaAPI(
	ctx context.Context, cookieHeader, organizationSlug string, policy api.OrganizationRetentionPolicy,
) (*api.OrganizationRetentionPolicy, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpdateOrganizationRetentionPolicyWithResponse(
		ctx, organizationSlug, policy, frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}
	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}
	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf("update organization retention api returned status %d", resp.StatusCode())
}

func (r *Router) setProjectPipelineBackendViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	payload api.SetProjectPipelineBackendJSONRequestBody,
) (*api.Project, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.SetProjectPipelineBackendWithResponse(
		ctx,
		organizationSlug,
		projectSlug,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"set project pipeline backend api returned status %d",
		resp.StatusCode(),
	)
}

// getProjectViaAPI loads one project by organization and project slug.
func (r *Router) getProjectViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
) (*api.Project, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetProjectWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"get project api returned status %d",
		resp.StatusCode(),
	)
}

// createProjectViaAPI delegates project creation to the API and maps non-created responses into frontend errors.
func (r *Router) createProjectViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	payload api.NewProject,
) (*api.Project, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreateProjectWithResponse(
		ctx,
		organizationSlug,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusCreated && resp.JSON201 != nil {
		return resp.JSON201, http.StatusCreated, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"create project api returned status %d",
		resp.StatusCode(),
	)
}

// deleteProjectViaAPI deletes a project through the API without requesting remote backend cleanup.
func (r *Router) deleteProjectViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
) (int, *api.ErrorResponse, error) {
	cleanupRemote := false
	params := &api.DeleteProjectParams{CleanupRemote: &cleanupRemote}
	resp, err := r.apiClient.DeleteProjectWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		params,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"delete project api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) handleCreateOrganizationPage(c fiber.Ctx) error {
	shellData := r.buildShellDataForDashboard(
		c,
		"Create organization",
		[]api.OrganizationWithRole{},
		"",
		"",
	)
	return renderTempl(
		c,
		views.OrganizationNewPage(shellData, views.DefaultOrganizationCreateFormData()),
	)
}

func (r *Router) handleCreateOrganizationSubmit(c fiber.Ctx) error {
	formData := organizationCreateFormDataFromRequest(c)
	name := formData.Name

	customSlug, validationMessage := optionalAPISlug(formData.Slug, "organization")
	if validationMessage == "" {
		validationMessage = validateOrganizationInput(name)
	}
	if validationMessage != "" {
		setHTMLResponseStatus(c, fiber.StatusBadRequest)
		shellData := r.buildShellDataForDashboard(
			c,
			"Create organization",
			[]api.OrganizationWithRole{},
			validationMessage,
			"",
		)
		return renderTempl(
			c,
			views.OrganizationNewPage(shellData, formData),
		)
	}

	createdOrganization, status, errorResponse, err := r.createOrganizationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		api.CreateOrganizationJSONRequestBody{Name: name, Slug: customSlug},
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if status < 400 || status >= 600 {
			status = fiber.StatusInternalServerError
		}

		setHTMLResponseStatus(c, status)
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = fmt.Sprintf("Create organization failed with status %d.", status)
		}

		shellData := r.buildShellDataForDashboard(
			c,
			"Create organization",
			[]api.OrganizationWithRole{},
			message,
			errorPopupDetails(errorResponse, ""),
		)

		return renderTempl(c, views.OrganizationNewPage(shellData, formData))
	}

	newOrgSlug, err := slug.New(createdOrganization.Slug)
	if err == nil {
		if setErr := reqctx.SetActiveOrganizationSlug(
			c,
			r.sessionStore,
			newOrgSlug,
		); setErr != nil {
			r.logger.Error(
				"failed to set active organization in session",
				slog.Any("error", setErr),
			)
		}
	}

	return redirectHTMXOrSeeOther(c, organizationsPath)
}

func organizationCreateFormDataFromRequest(c fiber.Ctx) views.OrganizationCreateFormData {
	formData := views.DefaultOrganizationCreateFormData()
	formData.Name = strings.TrimSpace(c.FormValue("name"))
	formData.Slug = strings.TrimSpace(c.FormValue("slug"))
	formData.Description = strings.TrimSpace(c.FormValue("description"))
	return formData
}

func (r *Router) createOrganizationViaAPI(
	ctx context.Context,
	cookieHeader string,
	payload api.CreateOrganizationJSONRequestBody,
) (*api.Organization, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreateOrganizationWithResponse(
		ctx,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusCreated && resp.JSON201 != nil {
		return resp.JSON201, http.StatusCreated, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"create organization api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) handleSwitchOrganization(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	returnTo := resolvePostLoginRedirect(c.Query("return_to"))
	canSelect, status, errorResponse, err := r.userCanSelectOrganization(c, orgSlug)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		r.logger.Error(
			"failed to verify organization switch membership",
			slog.Int("status", status),
			slog.String("details", errorPopupDetails(errorResponse, "")),
			slog.Any("error", err),
		)
		return c.Status(fiber.StatusInternalServerError).SendString("failed to switch organization")
	}
	if !canSelect {
		return c.Status(fiber.StatusForbidden).SendString("organization access requires membership")
	}

	if err := reqctx.SetActiveOrganizationSlug(c, r.sessionStore, orgSlug); err != nil {
		r.logger.Error("failed to set active organization", slog.Any("error", err))
		return c.Status(fiber.StatusInternalServerError).SendString("failed to switch organization")
	}

	return redirectHTMXOrSeeOther(c, returnTo)
}

func (r *Router) loadManagedRepositorySetup(c fiber.Ctx, org, project string, controls *views.ProjectPipelineBackendControls) {
	controls.RepositoryProvider = c.FormValue("repository_provider", "github")
	if c.Query("section") != "repository" || c.Query("repository_path") != "create" {
		return
	}
	if previous := c.Query("previous_setup"); previous != "" {
		attempt, err := r.apiClient.GetProjectRepositorySetupAttemptWithResponse(c.Context(), org, project, previous, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
		if err != nil || attempt.StatusCode() != http.StatusOK || attempt.JSON200 == nil || attempt.JSON200.AbandonedAt == nil {
			controls.PreviousSetupLoadError = "Previous setup details could not be loaded. Its resources have not been deleted."
		} else {
			controls.PreviousRepositorySetup = attempt.JSON200
			if c.Method() == fiber.MethodGet {
				request := attempt.JSON200.Request
				controls.RepositoryProvider = string(request.Provider)
				controls.ProviderOwner = request.Owner
				if request.TrackedBranch != nil {
					controls.CreationBranch = *request.TrackedBranch
				}
				if request.PipelineDir != nil {
					controls.PipelineDir = *request.PipelineDir
				}
			}
		}
	}
	response, err := r.apiClient.GetProjectRepositorySetupWithResponse(c.Context(), org, project, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil || (response.StatusCode() != http.StatusOK && response.StatusCode() != http.StatusNoContent) {
		controls.RepositorySetupLoadError = "Setup progress could not be loaded. Reload this page before creating a repository."
		return
	}
	if response.StatusCode() == http.StatusNoContent {
		return
	}
	if response.JSON200 == nil {
		controls.RepositorySetupLoadError = "Setup progress is unavailable. Reload this page before continuing."
		return
	}
	controls.RepositorySetup = response.JSON200
	request := response.JSON200.Request
	controls.RepositoryProvider = string(request.Provider)
	controls.ProviderOwner = request.Owner
	controls.RepositoryName = request.RepositoryName
	controls.CreationBranch = "main"
	if request.TrackedBranch != nil {
		controls.CreationBranch = *request.TrackedBranch
	}
	controls.PipelineDir = "pipelines"
	if request.PipelineDir != nil {
		controls.PipelineDir = *request.PipelineDir
	}
}
