package frontend

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// handleProjectOverviewUpsertDataSourceBindingSubmit saves a project binding from the project surface.
func (r *Router) handleProjectOverviewUpsertDataSourceBindingSubmit(c fiber.Ctx) error {
	orgSlug, projectSlug, environmentSlug, refSlug, ok := dataSourceBindingActionSlugs(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("invalid binding context")
	}
	pipelineSlug, ok := dataSourceBindingPipelineSlug(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("invalid binding context")
	}

	payload, validationErr := dataSourceBindingUpsertPayloadFromRequest(c)
	if validationErr != "" {
		return r.renderProjectActionResponse(
			c,
			orgSlug,
			projectSlug,
			http.StatusBadRequest,
			"Data-source binding could not be saved.",
			validationErr,
			"",
		)
	}

	_, status, errorResponse, err := r.upsertDataSourceBindingViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug.String(),
		pipelineSlug.String(),
		environmentSlug.String(),
		refSlug.String(),
		payload,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Data-source binding could not be saved."
		}
		return r.renderProjectActionResponse(
			c,
			orgSlug,
			projectSlug,
			status,
			message,
			backendErrorDetails("upsert data-source binding", status, errorResponse, err),
			"",
		)
	}

	return r.renderProjectActionResponse(c, orgSlug, projectSlug, http.StatusOK, "", "", "Data-source binding saved.")
}

// handleProjectOverviewDeleteDataSourceBindingSubmit removes a project binding from the project surface.
func (r *Router) handleProjectOverviewDeleteDataSourceBindingSubmit(c fiber.Ctx) error {
	orgSlug, projectSlug, environmentSlug, refSlug, ok := dataSourceBindingActionSlugs(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("invalid binding context")
	}
	pipelineSlug, ok := dataSourceBindingPipelineSlug(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("invalid binding context")
	}

	status, errorResponse, err := r.deleteDataSourceBindingViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug.String(),
		pipelineSlug.String(),
		environmentSlug.String(),
		refSlug.String(),
		dataSourceBindingDeleteParamsFromRequest(c),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Data-source binding could not be removed."
		}
		return r.renderProjectActionResponse(
			c,
			orgSlug,
			projectSlug,
			status,
			message,
			backendErrorDetails("delete data-source binding", status, errorResponse, err),
			"",
		)
	}

	return r.renderProjectActionResponse(c, orgSlug, projectSlug, http.StatusOK, "", "", "Data-source binding removed.")
}

// handlePipelineUpsertDataSourceBindingSubmit saves a project binding from a pipeline workflow.
func (r *Router) handlePipelineUpsertDataSourceBindingSubmit(c fiber.Ctx) error {
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, ok := pipelineDataSourceBindingActionSlugs(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("invalid binding context")
	}

	payload, validationErr := dataSourceBindingUpsertPayloadFromRequest(c)
	if validationErr != "" {
		return r.renderPipelineBindingActionError(
			c,
			orgSlug,
			projectSlug,
			pipelineSlug,
			http.StatusBadRequest,
			"Data-source binding could not be saved.",
			validationErr,
		)
	}

	_, status, errorResponse, err := r.upsertDataSourceBindingViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug.String(),
		pipelineSlug.String(),
		environmentSlug.String(),
		refSlug.String(),
		payload,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, pipelineBindingReturnHref(c, orgSlug.String(), projectSlug.String(), pipelineSlug.String()))
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Data-source binding could not be saved."
		}
		return r.renderPipelineBindingActionError(
			c,
			orgSlug,
			projectSlug,
			pipelineSlug,
			status,
			message,
			backendErrorDetails("upsert pipeline data-source binding", status, errorResponse, err),
		)
	}

	if c.Query("binding_setup") == "1" {
		// A fragment-only redirect would leave the resolver showing pre-save state.
		c.Set("HX-Refresh", "true")
	}
	return redirectHTMXOrSeeOther(c, pipelineBindingReturnHref(c, orgSlug.String(), projectSlug.String(), pipelineSlug.String()))
}

// handlePipelineDeleteDataSourceBindingSubmit removes a project binding from a pipeline workflow.
func (r *Router) handlePipelineDeleteDataSourceBindingSubmit(c fiber.Ctx) error {
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, ok := pipelineDataSourceBindingActionSlugs(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("invalid binding context")
	}

	status, errorResponse, err := r.deleteDataSourceBindingViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug.String(),
		pipelineSlug.String(),
		environmentSlug.String(),
		refSlug.String(),
		dataSourceBindingDeleteParamsFromRequest(c),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, pipelineBindingReturnHref(c, orgSlug.String(), projectSlug.String(), pipelineSlug.String()))
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Data-source binding could not be removed."
		}
		return r.renderPipelineBindingActionError(
			c,
			orgSlug,
			projectSlug,
			pipelineSlug,
			status,
			message,
			backendErrorDetails("delete pipeline data-source binding", status, errorResponse, err),
		)
	}

	if c.Query("binding_setup") == "1" {
		// A fragment-only redirect would leave the resolver showing pre-save state.
		c.Set("HX-Refresh", "true")
	}
	return redirectHTMXOrSeeOther(c, pipelineBindingReturnHref(c, orgSlug.String(), projectSlug.String(), pipelineSlug.String()))
}

// renderPipelineBindingActionError reloads pipeline detail with binding-action feedback.
func (r *Router) renderPipelineBindingActionError(
	c fiber.Ctx,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	pipelineSlug slug.Slug,
	statusCode int,
	errorSummary string,
	errorDetails string,
) error {
	if c.Query("binding_setup") == "1" {
		return r.renderPipelineBindingSetup(c, statusCode, errorSummary)
	}
	pipeline, status, errorResponse, err := r.getPipelineViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug.String(),
		pipelineSlug.String(),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		return r.renderPipelineDetailPage(
			c,
			orgSlug,
			pipelineSlug,
			statusCode,
			errorSummary,
			mergeErrorDetails(errorDetails, backendErrorDetails("get pipeline after binding action", status, errorResponse, err)),
			nil,
			views.DefaultPipelineUpdateFormData(),
			projectSlug.String(),
		)
	}

	return r.renderPipelineDetailPage(
		c,
		orgSlug,
		pipelineSlug,
		statusCode,
		errorSummary,
		errorDetails,
		pipeline,
		views.PipelineUpdateFormDataFromPipeline(*pipeline),
		projectSlug.String(),
	)
}

// dataSourceBindingActionSlugs validates route slugs shared by project and pipeline binding actions.
func dataSourceBindingActionSlugs(c fiber.Ctx) (slug.Slug, slug.Slug, slug.Slug, slug.Slug, bool) {
	orgSlug, projectSlug, ok := projectActionSlugs(c)
	if !ok {
		return "", "", "", "", false
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return "", "", "", "", false
	}
	refSlug, err := slug.New(c.Params("dataSourceRefSlug"))
	if err != nil {
		return "", "", "", "", false
	}
	return orgSlug, projectSlug, environmentSlug, refSlug, true
}

// pipelineDataSourceBindingActionSlugs validates the pipeline-specific binding route context.
func pipelineDataSourceBindingActionSlugs(c fiber.Ctx) (slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug, bool) {
	orgSlug, projectSlug, environmentSlug, refSlug, ok := dataSourceBindingActionSlugs(c)
	if !ok {
		return "", "", "", "", "", false
	}
	pipelineSlug, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return "", "", "", "", "", false
	}
	return orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, true
}

// dataSourceBindingPipelineSlug validates the pipeline owner carried by project-surface binding forms.
func dataSourceBindingPipelineSlug(c fiber.Ctx) (slug.Slug, bool) {
	value := strings.TrimSpace(c.FormValue("pipeline_slug"))
	if value == "" {
		value = strings.TrimSpace(c.Query("pipeline_slug"))
	}
	pipelineSlug, err := slug.New(value)
	if err != nil {
		return "", false
	}
	return pipelineSlug, true
}

// dataSourceBindingUpsertPayloadFromRequest maps SSR form fields to the binding API request body.
func dataSourceBindingUpsertPayloadFromRequest(c fiber.Ctx) (api.UpsertDataSourceBindingJSONRequestBody, string) {
	dataSourceSlug, err := slug.New(c.FormValue("data_source_slug"))
	if err != nil {
		return api.UpsertDataSourceBindingJSONRequestBody{}, "Choose a valid data source."
	}
	payload := api.UpsertDataSourceBindingJSONRequestBody{
		DataSourceSlug: dataSourceSlug.String(),
	}
	if confirmation := strings.TrimSpace(c.FormValue("protected_environment_confirmation")); confirmation != "" {
		value := confirmation
		payload.ProtectedEnvironmentConfirmation = &value
	}
	return payload, ""
}

// dataSourceBindingDeleteParamsFromRequest maps SSR form fields to binding delete query params.
func dataSourceBindingDeleteParamsFromRequest(c fiber.Ctx) *api.DeleteDataSourceBindingParams {
	params := &api.DeleteDataSourceBindingParams{}
	if confirmation := strings.TrimSpace(c.FormValue("protected_environment_confirmation")); confirmation != "" {
		value := confirmation
		params.ProtectedEnvironmentConfirmation = &value
	}
	return params
}

// pipelineBindingReturnHref preserves the caller's pipeline detail or catalog context after actions.
func pipelineBindingReturnHref(c fiber.Ctx, orgSlug, projectSlug, pipelineSlug string) string {
	if c.Query("binding_setup") == "1" {
		return pipelineBindingSetupReloadHref(c, orgSlug, projectSlug, pipelineSlug)
	}
	activeEnvironmentSlug := strings.TrimSpace(c.Query("environment_slug"))
	if activeEnvironmentSlug == "" {
		activeEnvironmentSlug = strings.TrimSpace(c.FormValue("active_environment_slug"))
	}
	if strings.EqualFold(strings.TrimSpace(c.Query("return_to")), "list") ||
		strings.EqualFold(strings.TrimSpace(c.FormValue("return_to")), "list") {
		values := url.Values{}
		values.Set("pipeline_slug", strings.TrimSpace(pipelineSlug))
		if activeEnvironmentSlug != "" {
			values.Set("environment_slug", activeEnvironmentSlug)
		}
		return "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + strings.TrimSpace(projectSlug) + "/pipelines?" + values.Encode()
	}
	detailHref := views.PipelineDetailHrefWithEnvironment(orgSlug, projectSlug, activeEnvironmentSlug, pipelineSlug)
	expected, _ := url.Parse(detailHref)
	current, _ := url.Parse(safeCurrentRequestPath(c.Get("HX-Current-URL"), c.Host()))
	if current != nil && current.Path == expected.Path {
		return current.String()
	}
	values := expected.Query()
	if listReturn := c.Query("list_return"); listReturn != "" {
		values.Set("list_return", listReturn)
	}
	values.Set("section", "deployments")
	expected.RawQuery = values.Encode()
	expected.Fragment = "pipeline-bindings"
	return expected.String()
}
