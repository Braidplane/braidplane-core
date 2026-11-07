package frontend

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	pipelineDetailLoadErrorMessage     = "Something went wrong while loading pipeline details."
	maxLatestPipelineRunLookupParallel = 4
)

func (r *Router) handleProjectPipelinesPage(c fiber.Ctx) error {
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	return r.handlePipelinesPageWithProject(c, projectSlug.String())
}

func (r *Router) handlePipelinesPageWithProject(c fiber.Ctx, activeProjectSlug string) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	if isHTMXRequest(c) && c.Get("HX-History-Restore-Request") != "true" && c.Get("HX-Target") == "pipeline-detail-panel-slot" {
		shellData := r.withEnvironmentShellData(c, views.AuthenticatedShellData{ActiveOrganizationSlug: orgSlug.String()}, c.Get(fiber.HeaderCookie), activeProjectSlug)
		badges := map[string][]views.PipelineDeploymentBadge{}
		unavailable := false
		if c.Query("pipeline_slug") != "" {
			deployments, colors, status, _, err := r.listProjectPipelineDeploymentsViaAPI(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), activeProjectSlug)
			if status == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}
			unavailable = err != nil
			if err == nil {
				badges = views.PipelineDeploymentBadgesByPipeline(deployments, colors)
			}
		}
		selected, status, unauthorized := r.loadSelectedPipelineDetail(c, orgSlug.String(), activeProjectSlug, shellData.ActiveEnvironmentSlug, badges, unavailable)
		if unauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		setHTMLResponseStatus(c, status)
		return renderTempl(c, views.PipelineDetailPanelSlot(orgSlug.String(), activeProjectSlug, shellData.ActiveEnvironmentSlug, selected, views.DefaultOrganizationPermissions()))
	}

	pageError := strings.TrimSpace(c.Query("error"))
	pageErrorDetails := ""
	filters := pipelineCatalogFiltersFromRequest(c)
	params := &api.ListPipelinesParams{}
	limit := api.LimitParam(20)
	params.Limit = &limit

	cursor := strings.TrimSpace(c.Query("cursor"))
	if cursor != "" {
		params.Cursor = &cursor
	}

	direction := strings.TrimSpace(c.Query("direction"))
	switch direction {
	case pageDirectionPrev:
		d := api.ListPipelinesParamsDirection(api.PageDirectionParamPrev)
		params.Direction = &d
	case pageDirectionNext:
		d := api.ListPipelinesParamsDirection(api.PageDirectionParamNext)
		params.Direction = &d
	}

	list, status, errorResponse, listErr := r.listPipelinesViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlugForAPI(activeProjectSlug),
		params,
	)
	if listErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if status == fiber.StatusForbidden && pageError == "" {
			pageError = "You do not have access to this organization's pipelines."
		} else if pageError == "" {
			message := messageFromErrorResponse(errorResponse)
			if message == "" {
				message = "Something went wrong while loading pipelines."
			}
			pageError = message
		}

		pageErrorDetails = backendErrorDetails("list pipelines", status, errorResponse, listErr)

		shellData := r.buildPipelineShellData(c, orgSlug.String(), activeProjectSlug, pageError, pageErrorDetails)
		permissions := r.resolveOrganizationPermissions(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
		)
		c.Status(status)
		return renderTempl(
			c,
			views.PipelinesPage(
				shellData,
				orgSlug.String(),
				activeProjectSlug,
				shellData.ActiveEnvironmentSlug,
				[]api.PipelineRef{},
				nil,
				false,
				map[string]views.PipelineLatestRunSummary{},
				false,
				false,
				"",
				"",
				filters,
				views.PipelineSelectedDetail{},
				permissions,
			),
		)
	}

	nextCursor := ""
	if list.NextCursor != nil {
		nextCursor = *list.NextCursor
	}
	prevCursor := ""
	if list.PrevCursor != nil {
		prevCursor = *list.PrevCursor
	}

	shellData := r.buildPipelineShellData(c, orgSlug.String(), activeProjectSlug, pageError, pageErrorDetails)
	deploymentBadges := map[string][]views.PipelineDeploymentBadge{}
	deploymentsUnavailable := false
	deployments, environmentColors, deploymentStatus, deploymentErrorResponse, deploymentErr := r.listProjectPipelineDeploymentsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		activeProjectSlug,
	)
	if deploymentErr == nil {
		deploymentBadges = views.PipelineDeploymentBadgesByPipeline(deployments, environmentColors)
	} else {
		deploymentsUnavailable = true
		if pageError == "" {
			message := messageFromErrorResponse(deploymentErrorResponse)
			if message == "" {
				message = "Deployment status is unavailable."
			}
			pageError = message
			pageErrorDetails = backendErrorDetails("list pipeline deployments", deploymentStatus, deploymentErrorResponse, deploymentErr)
			shellData = r.buildPipelineShellData(c, orgSlug.String(), activeProjectSlug, pageError, pageErrorDetails)
		}
	}
	latestRuns := map[string]views.PipelineLatestRunSummary{}
	latestRunsUnavailable := false
	if strings.TrimSpace(shellData.ActiveEnvironmentSlug) != "" {
		var latestRunStatus int
		var latestRunErrorResponse *api.ErrorResponse
		var latestRunErr error
		latestRuns, latestRunStatus, latestRunErrorResponse, latestRunErr = r.latestPipelineRunsByPipelineViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			activeProjectSlug,
			shellData.ActiveEnvironmentSlug,
			list.Pipelines,
		)
		if latestRunErr != nil {
			latestRunsUnavailable = true
			if pageError == "" {
				message := messageFromErrorResponse(latestRunErrorResponse)
				if message == "" {
					message = "Latest run status is unavailable."
				}
				pageError = message
				pageErrorDetails = backendErrorDetails("list latest pipeline runs", latestRunStatus, latestRunErrorResponse, latestRunErr)
				shellData = r.buildPipelineShellData(c, orgSlug.String(), activeProjectSlug, pageError, pageErrorDetails)
			}
		}
	}
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	selectedDetail, selectedStatus, selectedUnauthorized := r.loadSelectedPipelineDetail(
		c,
		orgSlug.String(),
		activeProjectSlug,
		shellData.ActiveEnvironmentSlug,
		deploymentBadges,
		deploymentsUnavailable,
	)
	if selectedUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if selectedDetail.ErrorSummary != "" && pageError == "" {
		pageError = selectedDetail.ErrorSummary
		pageErrorDetails = mergeErrorDetails(pageErrorDetails, selectedDetail.ErrorDetails)
		shellData = r.buildPipelineShellData(c, orgSlug.String(), activeProjectSlug, pageError, pageErrorDetails)
	}
	if selectedStatus >= http.StatusBadRequest {
		setHTMLResponseStatus(c, selectedStatus)
	}

	return renderTempl(
		c,
		views.PipelinesPage(
			shellData,
			orgSlug.String(),
			activeProjectSlug,
			shellData.ActiveEnvironmentSlug,
			list.Pipelines,
			deploymentBadges,
			deploymentsUnavailable,
			latestRuns,
			latestRunsUnavailable,
			list.HasMore,
			nextCursor,
			prevCursor,
			filters,
			selectedDetail,
			permissions,
		),
	)
}

func (r *Router) buildPipelineShellData(
	c fiber.Ctx,
	orgSlug string,
	activeProjectSlug string,
	pageError string,
	pageErrorDetails string,
) views.AuthenticatedShellData {
	if strings.TrimSpace(activeProjectSlug) != "" {
		shellData := r.buildShellDataForProjectPage(
			c,
			"Pipelines",
			orgSlug,
			activeProjectSlug,
			pageError,
			pageErrorDetails,
			c.Get(fiber.HeaderCookie),
		)
		shellData.Subtitle = "Inspect Git-authored pipeline definitions and their materialized sync state."
		return shellData
	}

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Pipelines",
		orgSlug,
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Inspect Git-authored pipeline definitions and their materialized sync state."
	return shellData
}

func (r *Router) handleProjectPipelineDetailPage(c fiber.Ctx) error {
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	return r.handlePipelineDetailPageWithProject(c, projectSlug.String())
}

// handleProjectPipelineDeploymentDetailPage renders one environment-scoped deployment pointer.
func (r *Router) handleProjectPipelineDeploymentDetailPage(c fiber.Ctx) error {
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	return r.handlePipelineDeploymentDetailPageWithProject(c, projectSlug.String())
}

func (r *Router) handlePipelineDetailPageWithProject(c fiber.Ctx, activeProjectSlug string) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	pipelineSlug, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid pipeline slug")
	}

	pipeline, status, errorResponse, getErr := r.getPipelineViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlugForAPI(activeProjectSlug),
		pipelineSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = pipelineDetailLoadErrorMessage
		}

		return r.renderPipelineDetailPage(
			c,
			orgSlug,
			pipelineSlug,
			status,
			message,
			backendErrorDetails("get pipeline", status, errorResponse, getErr),
			nil,
			views.DefaultPipelineUpdateFormData(),
			activeProjectSlug,
		)
	}

	return r.renderPipelineDetailPage(
		c,
		orgSlug,
		pipelineSlug,
		http.StatusOK,
		"",
		"",
		pipeline,
		views.PipelineUpdateFormDataFromPipeline(*pipeline),
		activeProjectSlug,
	)
}

// handlePipelineDeploymentDetailPageWithProject loads the canonical pipeline and selected deployment pointer for SSR inspection.
func (r *Router) handlePipelineDeploymentDetailPageWithProject(c fiber.Ctx, activeProjectSlug string) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	pipelineSlug, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid pipeline slug")
	}

	environmentSlug, environmentStatus, err := r.resolveRuntimeEnvironmentSlug(c)
	if err != nil {
		if environmentStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		pageError := "Select a project runtime target before viewing deployment details."
		if strings.TrimSpace(requestRuntimeEnvironmentSlug(c)) != "" {
			pageError = "Invalid project runtime target."
		}
		shellData := r.buildPipelineDeploymentDetailShellData(
			c,
			orgSlug.String(),
			activeProjectSlug,
			"",
			pipelineSlug.String(),
			pageError,
			backendErrorDetails("resolve runtime environment", environmentStatus, nil, err),
		)
		c.Status(environmentStatus)
		return renderTempl(
			c,
			views.PipelineDeploymentDetailPage(
				shellData,
				orgSlug.String(),
				activeProjectSlug,
				"",
				pipelineSlug.String(),
				nil,
				nil,
			),
		)
	}

	pipeline, status, errorResponse, getPipelineErr := r.getPipelineViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlugForAPI(activeProjectSlug),
		pipelineSlug.String(),
	)
	if getPipelineErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = pipelineDetailLoadErrorMessage
		}
		shellData := r.buildPipelineDeploymentDetailShellData(
			c,
			orgSlug.String(),
			activeProjectSlug,
			environmentSlug.String(),
			pipelineSlug.String(),
			message,
			backendErrorDetails("get pipeline", status, errorResponse, getPipelineErr),
		)
		c.Status(status)
		return renderTempl(
			c,
			views.PipelineDeploymentDetailPage(
				shellData,
				orgSlug.String(),
				activeProjectSlug,
				environmentSlug.String(),
				pipelineSlug.String(),
				nil,
				nil,
			),
		)
	}

	deployment, status, errorResponse, getDeploymentErr := r.getPipelineDeploymentViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlugForAPI(activeProjectSlug),
		environmentSlug.String(),
		pipelineSlug.String(),
	)
	if getDeploymentErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Deployment pointer is unavailable for this runtime target."
		}
		shellData := r.buildPipelineDeploymentDetailShellData(
			c,
			orgSlug.String(),
			activeProjectSlug,
			environmentSlug.String(),
			pipelineSlug.String(),
			message,
			backendErrorDetails("get pipeline deployment", status, errorResponse, getDeploymentErr),
		)
		c.Status(status)
		return renderTempl(
			c,
			views.PipelineDeploymentDetailPage(
				shellData,
				orgSlug.String(),
				activeProjectSlug,
				environmentSlug.String(),
				pipelineSlug.String(),
				pipeline,
				nil,
			),
		)
	}

	shellData := r.buildPipelineDeploymentDetailShellData(
		c,
		orgSlug.String(),
		activeProjectSlug,
		environmentSlug.String(),
		pipelineSlug.String(),
		"",
		"",
	)
	return renderTempl(
		c,
		views.PipelineDeploymentDetailPage(
			shellData,
			orgSlug.String(),
			activeProjectSlug,
			environmentSlug.String(),
			pipelineSlug.String(),
			pipeline,
			deployment,
		),
	)
}

func (r *Router) handleDeletePipelineDraftSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	pipelineSlug, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid pipeline slug")
	}
	activeProjectSlug, err := optionalProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}
	activeEnvironmentSlug, err := optionalRuntimeEnvironmentSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}

	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if !permissions.Can("pipelines", "update") {
		return r.renderPipelineDetailPage(
			c,
			orgSlug,
			pipelineSlug,
			http.StatusForbidden,
			"You do not have permission to discard pipeline drafts.",
			"",
			nil,
			views.DefaultPipelineUpdateFormData(),
			activeProjectSlug,
		)
	}

	currentPipeline, status, errorResponse, getErr := r.getPipelineViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlugForAPI(activeProjectSlug),
		pipelineSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = pipelineDetailLoadErrorMessage
		}
		return r.renderPipelineDetailPage(
			c,
			orgSlug,
			pipelineSlug,
			status,
			message,
			backendErrorDetails("get pipeline", status, errorResponse, getErr),
			nil,
			views.DefaultPipelineUpdateFormData(),
			activeProjectSlug,
		)
	}

	deleteStatus, deleteErrResponse, deleteErr := r.deletePipelineDraftViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlugForAPI(activeProjectSlug),
		pipelineSlug.String(),
	)
	if deleteErr != nil {
		if deleteStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(deleteErrResponse)
		if message == "" {
			message = "Something went wrong while discarding the pipeline draft."
		}

		return r.renderPipelineDetailPage(
			c,
			orgSlug,
			pipelineSlug,
			deleteStatus,
			message,
			backendErrorDetails(
				"delete pipeline draft",
				deleteStatus,
				deleteErrResponse,
				deleteErr,
			),
			currentPipeline,
			views.PipelineUpdateFormDataFromPipeline(*currentPipeline),
			activeProjectSlug,
		)
	}

	return redirectHTMXOrSeeOther(c, pipelineValidationReturnHref(c, orgSlug.String(), activeProjectSlug, activeEnvironmentSlug, pipelineSlug.String()))
}

func (r *Router) handleRevalidatePipelineSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	pipelineSlug, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid pipeline slug")
	}
	activeProjectSlug, err := optionalProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}
	activeEnvironmentSlug, err := optionalRuntimeEnvironmentSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}

	currentPipeline, status, errorResponse, getErr := r.getPipelineViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlugForAPI(activeProjectSlug),
		pipelineSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = pipelineDetailLoadErrorMessage
		}
		return r.renderPipelineDetailPage(
			c,
			orgSlug,
			pipelineSlug,
			status,
			message,
			backendErrorDetails("get pipeline", status, errorResponse, getErr),
			nil,
			views.DefaultPipelineUpdateFormData(),
			activeProjectSlug,
		)
	}

	if strings.TrimSpace(activeEnvironmentSlug) == "" {
		return r.renderPipelineDetailPage(
			c,
			orgSlug,
			pipelineSlug,
			http.StatusBadRequest,
			"Select a runtime target before revalidating this pipeline.",
			"Validation results are scoped to one project runtime target.",
			currentPipeline,
			views.PipelineUpdateFormDataFromPipeline(*currentPipeline),
			activeProjectSlug,
		)
	}

	_, status, errorResponse, revalidateErr := r.revalidatePipelineViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlugForAPI(activeProjectSlug),
		activeEnvironmentSlug,
		pipelineSlug.String(),
	)
	if revalidateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while revalidating the pipeline."
		}
		return r.renderPipelineDetailPage(
			c,
			orgSlug,
			pipelineSlug,
			status,
			message,
			backendErrorDetails("revalidate pipeline", status, errorResponse, revalidateErr),
			currentPipeline,
			views.PipelineUpdateFormDataFromPipeline(*currentPipeline),
			activeProjectSlug,
		)
	}

	return redirectHTMXOrSeeOther(
		c,
		pipelineValidationReturnHref(c, orgSlug.String(), activeProjectSlug, activeEnvironmentSlug, pipelineSlug.String()),
	)
}

func (r *Router) loadSelectedPipelineDetail(
	c fiber.Ctx,
	orgSlug string,
	activeProjectSlug string,
	activeEnvironmentSlug string,
	deploymentBadges map[string][]views.PipelineDeploymentBadge,
	deploymentsUnavailable bool,
) (views.PipelineSelectedDetail, int, bool) {
	rawPipelineSlug := strings.TrimSpace(c.Query("pipeline_slug"))
	if rawPipelineSlug == "" {
		return views.PipelineSelectedDetail{}, http.StatusOK, false
	}

	pipelineSlug, err := slug.New(rawPipelineSlug)
	if err != nil {
		return views.PipelineSelectedDetail{
			Slug:         rawPipelineSlug,
			ErrorSummary: "Selected pipeline is invalid.",
			ErrorDetails: "Validation: pipeline slug must use lowercase letters, digits, and hyphens only.",
		}, http.StatusBadRequest, false
	}

	pipeline, status, errorResponse, getErr := r.getPipelineViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug,
		projectSlugForAPI(activeProjectSlug),
		pipelineSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return views.PipelineSelectedDetail{}, status, true
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = pipelineDetailLoadErrorMessage
		}
		return views.PipelineSelectedDetail{
			Slug:         pipelineSlug.String(),
			ErrorSummary: message,
			ErrorDetails: backendErrorDetails("get selected pipeline", status, errorResponse, getErr),
		}, status, false
	}

	selected := views.PipelineSelectedDetail{
		Slug:                   pipelineSlug.String(),
		Pipeline:               pipeline,
		Deployments:            deploymentBadges[pipelineSlug.String()],
		DeploymentsUnavailable: deploymentsUnavailable,
		UpdateFormData:         views.PipelineUpdateFormDataFromPipeline(*pipeline),
	}
	if strings.TrimSpace(activeEnvironmentSlug) == "" {
		return selected, http.StatusOK, false
	}

	latestRun, hasLatestRun, _, _, latestRunErr := r.latestPipelineRunViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug,
		activeProjectSlug,
		activeEnvironmentSlug,
		pipelineSlug.String(),
	)
	if latestRunErr != nil {
		selected.LatestRunUnavailable = true
	} else {
		selected.LatestRun = latestRun
		selected.HasLatestRun = hasLatestRun
	}

	validationResults, validationStatus, _, validationErr := r.listPipelineValidationResultsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug,
		activeProjectSlug,
		activeEnvironmentSlug,
		pipelineSlug.String(),
		5,
	)
	if validationErr != nil {
		if validationStatus == fiber.StatusUnauthorized {
			return selected, http.StatusUnauthorized, true
		}
		selected.ValidationUnavailable = true
		return selected, http.StatusOK, false
	}
	if validationResults != nil {
		selected.ValidationResults = validationResults.ValidationResults
	}
	return selected, http.StatusOK, false
}

func (r *Router) renderPipelineDetailPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	pipelineSlug slug.Slug,
	status int,
	pageError string,
	pageErrorDetails string,
	pipeline *api.Pipeline,
	updateFormData views.PipelineUpdateFormData,
	activeProjectSlug string,
) error {
	shellData := r.buildPipelineDetailShellData(
		c,
		orgSlug.String(),
		activeProjectSlug,
		pipelineSlug.String(),
		pageError,
		pageErrorDetails,
	)
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	deploymentBadges := []views.PipelineDeploymentBadge{}
	deploymentsUnavailable := false
	runPreview := views.PipelineRunPreview{}
	validationResults := []api.PipelineValidationResultRecord{}
	validationUnavailable := false
	bindingContext := views.PipelineBindingContext{}
	if pipeline != nil {
		deployments, environmentColors, _, _, deploymentErr := r.listProjectPipelineDeploymentsViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			activeProjectSlug,
		)
		if deploymentErr == nil {
			deploymentBadges = views.PipelineDeploymentBadgesByPipeline(deployments, environmentColors)[pipelineSlug.String()]
		} else {
			deploymentsUnavailable = true
		}
		if strings.TrimSpace(shellData.ActiveEnvironmentSlug) != "" {
			runPreview = r.loadPipelineRunPreview(c, orgSlug.String(), activeProjectSlug, shellData.ActiveEnvironmentSlug, pipelineSlug.String())
			validationHistory, validationStatus, _, validationErr := r.listPipelineValidationResultsViaAPI(
				c.Context(),
				c.Get(fiber.HeaderCookie),
				orgSlug.String(),
				activeProjectSlug,
				shellData.ActiveEnvironmentSlug,
				pipelineSlug.String(),
				5,
			)
			if validationErr != nil {
				if validationStatus == fiber.StatusUnauthorized {
					return redirectToLogin(c, c.OriginalURL())
				}
				validationUnavailable = true
			} else if validationHistory != nil {
				validationResults = validationHistory.ValidationResults
			}
		}
		var bindingUnauthorized bool
		bindingContext, bindingUnauthorized = r.loadPipelineBindingContext(c, orgSlug.String(), activeProjectSlug)
		if bindingUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
	}

	if pipeline != nil && shellData.ActiveEnvironmentSlug != "" && permissions.Can("pipelines", "read") {
		response, err := r.apiClient.GetSuccessfulRunRecencyWithResponse(c.Context(), orgSlug.String(), activeProjectSlug, shellData.ActiveEnvironmentSlug, pipelineSlug.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
		if err == nil && response.JSON200 != nil {
			c.SetContext(views.WithRecencyInspection(c.Context(), response.JSON200))
		}
	}
	if pipeline != nil && c.Query("section") == "connections" {
		connections, unauthorized := r.loadPipelineConnections(c, orgSlug.String(), activeProjectSlug, shellData.ActiveEnvironmentSlug, pipelineSlug.String())
		if unauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		c.SetContext(views.WithPipelineConnections(c.Context(), connections))
	}
	r.loadDefinitionInspection(c, orgSlug.String(), activeProjectSlug, pipeline)
	c.Status(status)
	return renderTempl(
		c,
		views.PipelineDetailPage(
			shellData,
			orgSlug.String(),
			activeProjectSlug,
			shellData.ActiveEnvironmentSlug,
			pipelineSlug.String(),
			pipeline,
			deploymentBadges,
			deploymentsUnavailable,
			runPreview,
			validationResults,
			validationUnavailable,
			bindingContext,
			updateFormData,
			permissions,
		),
	)
}

// loadPipelineBindingContext loads project binding state needed to resolve a pipeline's logical refs.
func (r *Router) loadPipelineBindingContext(
	c fiber.Ctx,
	orgSlug string,
	projectSlug string,
) (views.PipelineBindingContext, bool) {
	context := views.PipelineBindingContext{
		Bindings:                       map[string][]api.DataSourceBinding{},
		BindingUnavailableEnvironments: map[string]bool{},
	}
	projectSlug = strings.TrimSpace(projectSlug)
	if projectSlug == "" {
		context.EnvironmentsUnavailable = true
		context.BindingsUnavailable = true
		context.RefsUnavailable = true
		return context, false
	}

	refs, status, _, err := r.listDataSourceRefsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug,
		projectSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return context, true
		}
		context.RefsUnavailable = true
	} else if refs != nil {
		context.DataSourceRefs = refs.DataSourceRefs
	}

	projectEnvironments, status, _, err := r.listProjectEnvironmentsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug,
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

	dataSources, status, _, err := r.listAllDataSourcesViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return context, true
		}
		context.DataSourcesUnavailable = true
	} else {
		context.DataSources = dataSources
	}

	if context.EnvironmentsUnavailable {
		return context, false
	}
	for _, environment := range context.Environments {
		environmentSlug := strings.TrimSpace(environment.EnvironmentSlug)
		bindings, status, _, err := r.listDataSourceBindingsViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug,
			projectSlug,
			environmentSlug,
		)
		if err != nil {
			if status == fiber.StatusUnauthorized {
				return context, true
			}
			context.BindingUnavailableEnvironments[environmentSlug] = true
			continue
		}
		if bindings != nil {
			context.Bindings[environmentSlug] = bindings.DataSourceBindings
		}
	}
	return context, false
}

func (r *Router) buildPipelineDetailShellData(
	c fiber.Ctx,
	orgSlug string,
	activeProjectSlug string,
	pipelineSlug string,
	pageError string,
	pageErrorDetails string,
) views.AuthenticatedShellData {
	if strings.TrimSpace(activeProjectSlug) == "" {
		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Pipeline details",
			orgSlug,
			pageError,
			pageErrorDetails,
			c.Get(fiber.HeaderCookie),
		)
		shellData.Subtitle = "Inspect Git materialization and runtime state, then propose pipeline changes through Builder."
		shellData.Breadcrumbs = buildOrganizationDetailBreadcrumbs(
			shellData.ActiveOrganizationName,
			orgSlug,
			"Pipelines",
			"/organizations/"+orgSlug+"/projects",
			pipelineSlug,
		)
		return shellData
	}

	shellData := r.buildShellDataForProjectPage(
		c,
		"Pipeline details",
		orgSlug,
		activeProjectSlug,
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Inspect Git materialization and runtime state, then propose pipeline changes through Builder."
	pipelinesHref := "/orgs/" + orgSlug + "/projects/" + strings.TrimSpace(activeProjectSlug) + "/pipelines"
	shellData.Breadcrumbs = buildProjectBreadcrumbs(
		pipelinesHref,
		orgSlug,
		shellData.ActiveOrganizationName,
		activeProjectSlug,
		shellData.ActiveProjectName,
	)
	if len(shellData.Breadcrumbs) > 0 {
		shellData.Breadcrumbs[len(shellData.Breadcrumbs)-1].Href = pipelinesHref
	}
	shellData.Breadcrumbs = append(shellData.Breadcrumbs, views.BreadcrumbItem{Label: pipelineSlug})
	return shellData
}

// buildPipelineDeploymentDetailShellData prepares shell context for the deployment pointer drill-in page.
func (r *Router) buildPipelineDeploymentDetailShellData(
	c fiber.Ctx,
	orgSlug string,
	activeProjectSlug string,
	environmentSlug string,
	pipelineSlug string,
	pageError string,
	pageErrorDetails string,
) views.AuthenticatedShellData {
	shellData := r.buildShellDataForProjectPage(
		c,
		"Deployment details",
		orgSlug,
		activeProjectSlug,
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Inspect the deployed version pointer for the selected runtime target."

	pipelinesHref := views.ProjectPipelinesListHrefWithEnvironment(orgSlug, activeProjectSlug, environmentSlug, "", "")
	pipelineHref := views.PipelineDetailHrefWithEnvironment(orgSlug, activeProjectSlug, environmentSlug, pipelineSlug)
	shellData.Breadcrumbs = buildProjectBreadcrumbs(
		pipelinesHref,
		orgSlug,
		shellData.ActiveOrganizationName,
		activeProjectSlug,
		shellData.ActiveProjectName,
	)
	if len(shellData.Breadcrumbs) > 0 {
		shellData.Breadcrumbs[len(shellData.Breadcrumbs)-1].Href = pipelinesHref
	}
	shellData.Breadcrumbs = append(
		shellData.Breadcrumbs,
		views.BreadcrumbItem{Label: pipelineSlug, Href: pipelineHref},
		views.BreadcrumbItem{Label: "Deployment"},
	)
	return shellData
}

// pipelineCatalogFiltersFromRequest normalizes catalog filter query values from the SSR request.
func pipelineCatalogFiltersFromRequest(c fiber.Ctx) views.PipelineCatalogFilters {
	return views.PipelineCatalogFilters{
		Query:           strings.TrimSpace(c.Query("q")),
		SyncStatus:      strings.TrimSpace(c.Query("sync_status")),
		DraftState:      strings.TrimSpace(c.Query("draft")),
		DeploymentState: strings.TrimSpace(c.Query("deployment")),
		RunStatus:       strings.TrimSpace(c.Query("run_status")),
		Cursor:          strings.TrimSpace(c.Query("cursor")),
		Direction:       strings.TrimSpace(c.Query("direction")),
	}
}

func (r *Router) listPipelinesViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	params *api.ListPipelinesParams,
) (*api.PipelineListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListPipelinesWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
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
		"list pipelines api returned status %d",
		resp.StatusCode(),
	)
}

// listAllPipelinesViaAPI walks the paginated pipeline API for project binding requirement discovery.
func (r *Router) listAllPipelinesViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
) ([]api.PipelineRef, int, *api.ErrorResponse, error) {
	limit := api.LimitParam(100)
	direction := api.ListPipelinesParamsDirectionNext
	params := &api.ListPipelinesParams{
		Limit: &limit,
	}
	pipelines := []api.PipelineRef{}
	seenCursors := map[string]struct{}{}

	for {
		pipelineList, status, errorResponse, err := r.listPipelinesViaAPI(
			ctx,
			cookieHeader,
			organizationSlug,
			projectSlug,
			params,
		)
		if err != nil {
			return nil, status, errorResponse, err
		}

		pipelines = append(pipelines, pipelineList.Pipelines...)
		if !pipelineList.HasMore || pipelineList.NextCursor == nil {
			return pipelines, http.StatusOK, nil, nil
		}
		nextCursor := strings.TrimSpace(*pipelineList.NextCursor)
		if nextCursor == "" {
			return pipelines, http.StatusOK, nil, nil
		}
		if _, exists := seenCursors[nextCursor]; exists {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().
				Detailf("list pipelines pagination returned repeated cursor %q", nextCursor)
		}
		seenCursors[nextCursor] = struct{}{}

		params = &api.ListPipelinesParams{
			Limit:     &limit,
			Cursor:    &nextCursor,
			Direction: &direction,
		}
	}
}

// latestPipelineRunsByPipelineViaAPI loads one latest run per pipeline in the active runtime environment.
func (r *Router) latestPipelineRunsByPipelineViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	pipelines []api.PipelineRef,
) (map[string]views.PipelineLatestRunSummary, int, *api.ErrorResponse, error) {
	latestRuns := make(map[string]views.PipelineLatestRunSummary, len(pipelines))
	type latestRunLookupResult struct {
		pipelineSlug  string
		latestRun     views.PipelineLatestRunSummary
		ok            bool
		status        int
		errorResponse *api.ErrorResponse
		err           error
	}

	results := make(chan latestRunLookupResult)
	sem := make(chan struct{}, maxLatestPipelineRunLookupParallel)
	var wg sync.WaitGroup
	for _, pipeline := range pipelines {
		pipelineSlug := strings.TrimSpace(pipeline.Slug)
		if pipelineSlug == "" {
			continue
		}
		wg.Add(1)
		go func(pipelineSlug string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			latestRun, ok, status, errorResponse, err := r.latestPipelineRunViaAPI(
				ctx,
				cookieHeader,
				organizationSlug,
				projectSlug,
				environmentSlug,
				pipelineSlug,
			)
			results <- latestRunLookupResult{
				pipelineSlug:  pipelineSlug,
				latestRun:     latestRun,
				ok:            ok,
				status:        status,
				errorResponse: errorResponse,
				err:           err,
			}
		}(pipelineSlug)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	lookupStatus := http.StatusOK
	var lookupErrorResponse *api.ErrorResponse
	var lookupErr error
	for result := range results {
		if result.err != nil {
			r.logger.Warn(
				"pipeline latest run lookup failed",
				slog.String("organization_slug", organizationSlug),
				slog.String("project_slug", projectSlug),
				slog.String("environment_slug", environmentSlug),
				slog.String("pipeline_slug", result.pipelineSlug),
				slog.Int("status", result.status),
				slog.String("details", errorPopupDetails(result.errorResponse, "")),
				slog.Any("error", result.err),
			)
			if lookupErr == nil {
				lookupStatus = result.status
				if lookupStatus < 100 || lookupStatus >= 600 {
					lookupStatus = http.StatusInternalServerError
				}
				lookupErrorResponse = result.errorResponse
				lookupErr = result.err
			}
			continue
		}
		if result.ok {
			latestRuns[result.pipelineSlug] = result.latestRun
		}
	}

	return latestRuns, lookupStatus, lookupErrorResponse, lookupErr
}

// latestPipelineRunViaAPI queries the existing runs API for a single pipeline's newest run.
func (r *Router) latestPipelineRunViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	pipelineSlug string,
) (views.PipelineLatestRunSummary, bool, int, *api.ErrorResponse, error) {
	if strings.TrimSpace(projectSlug) == "" || strings.TrimSpace(environmentSlug) == "" || strings.TrimSpace(pipelineSlug) == "" {
		return views.PipelineLatestRunSummary{}, false, http.StatusOK, nil, nil
	}

	limit := api.LimitParam(1)
	sortBy := api.ListPipelineRunsParamsSortByRunCreatedAt
	order := api.ListPipelineRunsParamsOrderDesc
	pipelineSlugParam := strings.TrimSpace(pipelineSlug)
	params := &api.ListPipelineRunsParams{
		Limit:        &limit,
		SortBy:       &sortBy,
		Order:        &order,
		PipelineSlug: &pipelineSlugParam,
	}
	runList, status, errorResponse, err := r.listPipelineRunsViaAPI(
		ctx,
		cookieHeader,
		organizationSlug,
		projectSlug,
		environmentSlug,
		params,
	)
	if err != nil {
		return views.PipelineLatestRunSummary{}, false, status, errorResponse, err
	}
	if len(runList.Runs) == 0 {
		return views.PipelineLatestRunSummary{}, false, http.StatusOK, nil, nil
	}

	run := runList.Runs[0]
	return views.PipelineLatestRunSummary{
		EnvironmentSlug: run.EnvironmentSlug,
		RunSlug:         run.RunSlug,
		Status:          run.Status,
		CreatedAt:       run.CreatedAt,
		StartedAt:       run.StartedAt,
		DurationMs:      run.DurationMs,
		ErrorSummary:    run.ErrorSummary,
	}, true, http.StatusOK, nil, nil
}

func (r *Router) listPipelineValidationResultsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	pipelineSlug string,
	limitValue int32,
) (*api.PipelineValidationResultListResponse, int, *api.ErrorResponse, error) {
	if strings.TrimSpace(projectSlug) == "" || strings.TrimSpace(environmentSlug) == "" || strings.TrimSpace(pipelineSlug) == "" {
		return &api.PipelineValidationResultListResponse{ValidationResults: []api.PipelineValidationResultRecord{}}, http.StatusOK, nil, nil
	}

	limit := limitValue
	pipelineSlugParam := strings.TrimSpace(pipelineSlug)
	params := &api.ListPipelineValidationResultsParams{
		Limit:        &limit,
		PipelineSlug: &pipelineSlugParam,
	}
	resp, err := r.apiClient.ListPipelineValidationResultsWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
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
		"list pipeline validation results api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) revalidatePipelineViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	pipelineSlug string,
) (*api.PipelineValidationResultRecord, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.RevalidatePipelineWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
		pipelineSlug,
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
		"revalidate pipeline api returned status %d",
		resp.StatusCode(),
	)
}

// pipelineValidationReturnHref keeps revalidation on the caller's current safe product surface.
func pipelineValidationReturnHref(
	c fiber.Ctx,
	orgSlug string,
	projectSlug string,
	environmentSlug string,
	pipelineSlug string,
) string {
	if returnTo := safeReturnTo(c.FormValue("return_to")); returnTo != "" {
		return returnTo
	}
	if returnTo := safeReturnTo(c.Query("return_to")); returnTo != "" {
		return returnTo
	}
	if returnTo := safeCurrentRequestPath(c.Get("HX-Current-URL"), c.Host()); returnTo != "" {
		return returnTo
	}
	return views.PipelineDetailHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, pipelineSlug)
}

// safeCurrentRequestPath normalizes an HTMX current URL into a same-origin relative path.
func safeCurrentRequestPath(raw, currentHost string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	if parsed.IsAbs() || parsed.Host != "" {
		if !sameRequestHostname(parsed.Hostname(), currentHost) {
			return ""
		}
		trimmed = parsed.RequestURI()
	}
	return safeReturnTo(trimmed)
}

// sameRequestHostname compares hostnames after stripping any request host port decoration.
func sameRequestHostname(parsedHostname, currentHost string) bool {
	parsedHostname = strings.TrimSpace(parsedHostname)
	currentHostname := requestHostname(currentHost)
	if parsedHostname == "" || currentHostname == "" {
		return false
	}
	return strings.EqualFold(parsedHostname, currentHostname)
}

// requestHostname returns the hostname portion of a host value that may include a port.
func requestHostname(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(trimmed, "[]")
}

func (r *Router) listProjectPipelineDeploymentsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
) ([]api.PipelineDeployment, map[string]string, int, *api.ErrorResponse, error) {
	if strings.TrimSpace(projectSlug) == "" {
		return nil, nil, http.StatusOK, nil, nil
	}

	projectEnvironments, status, errorResponse, err := r.listProjectEnvironmentsViaAPI(
		ctx,
		cookieHeader,
		organizationSlug,
		projectSlug,
	)
	if err != nil {
		return nil, nil, status, errorResponse, err
	}
	environmentColors := views.ProjectEnvironmentColorMap(projectEnvironments.ProjectEnvironments)

	deployments := []api.PipelineDeployment{}
	for _, environment := range projectEnvironments.ProjectEnvironments {
		deploymentList, status, errorResponse, err := r.listPipelineDeploymentsViaAPI(
			ctx,
			cookieHeader,
			organizationSlug,
			projectSlug,
			environment.EnvironmentSlug,
		)
		if err != nil {
			return nil, nil, status, errorResponse, err
		}
		deployments = append(deployments, deploymentList.Deployments...)
	}

	return deployments, environmentColors, http.StatusOK, nil, nil
}

func (r *Router) listPipelineDeploymentsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
) (*api.PipelineDeploymentListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListPipelineDeploymentsWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
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
		"list pipeline deployments api returned status %d",
		resp.StatusCode(),
	)
}

// getPipelineDeploymentViaAPI loads one environment-scoped deployment pointer through the generated API client.
func (r *Router) getPipelineDeploymentViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	pipelineSlug string,
) (*api.PipelineDeployment, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetPipelineDeploymentWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
		pipelineSlug,
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
		"get pipeline deployment api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) publishPipelineDraftViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	pipelineSlug string,
	expectedVersionID *string,
	force bool,
) (*api.Pipeline, *api.PipelineDraftConflictResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.PublishPipelineDraftWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		pipelineSlug,
		api.PublishPipelineDraftJSONRequestBody{
			ExpectedSpecHash: expectedVersionID,
			Force:            &force,
		},
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, nil, http.StatusOK, nil, nil
	}

	if resp.StatusCode() == http.StatusConflict {
		conflictResp := parsePipelineDraftConflictResponse(resp.Body)
		if conflictResp != nil {
			return nil, conflictResp, http.StatusConflict, nil, xerror.Unexpected().Detail(
				"publish pipeline draft conflict",
			)
		}
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"publish pipeline draft api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) refreshPipelineDraftProposalViaAPI(
	ctx context.Context,
	cookieHeader, organizationSlug, projectSlug, pipelineSlug string,
) (*api.Pipeline, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.RefreshPipelineDraftProposalWithResponse(
		ctx, organizationSlug, projectSlugForAPI(projectSlug), pipelineSlug,
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
		"refresh pipeline draft proposal api returned status %d", resp.StatusCode(),
	)
}

func (r *Router) deletePipelineDraftViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	pipelineSlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeletePipelineDraftWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		pipelineSlug,
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
		"delete pipeline draft api returned status %d",
		resp.StatusCode(),
	)
}
