package frontend

import (
	"net/http"
	"strconv"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) handlePipelineExecutionControl(c fiber.Ctx) error {
	org, project, ok := projectActionSlugs(c)
	pipeline, pipelineErr := slug.New(c.Params("pipelineSlug"))
	environment, environmentErr := slug.New(c.Params("environmentSlug"))
	if !ok || pipelineErr != nil || environmentErr != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	permissions := r.resolvePagePermissions(c, org.String())
	canUpdate := permissions.Can("pipelines", "read") && permissions.Can("pipelines", "update")
	editor := frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie))
	href := views.PipelineExecutionControlHref(org.String(), project.String(), environment.String(), pipeline.String())
	message := ""
	status := http.StatusOK
	if c.Method() == http.MethodPost {
		if !canUpdate {
			return c.SendStatus(http.StatusForbidden)
		}
		revision, revisionErr := strconv.ParseInt(c.FormValue("expected_revision"), 10, 64)
		action := c.FormValue("paused")
		if revisionErr != nil || revision < 0 || (action != "true" && action != "false") {
			return c.SendStatus(http.StatusBadRequest)
		}
		response, err := r.apiClient.SetPipelineExecutionControlWithResponse(c.Context(), org.String(), project.String(), environment.String(), pipeline.String(), api.PipelineExecutionControlUpdate{ExpectedRevision: revision, Paused: action == "true"}, editor)
		switch {
		case err != nil:
			status, message = http.StatusBadGateway, "Execution state could not be saved. Refresh status before trying again."
		case response.StatusCode() == http.StatusUnauthorized:
			return redirectToLogin(c, href)
		case response.StatusCode() == http.StatusConflict:
			status, message = http.StatusConflict, "Execution state changed. Review the current status before trying again."
		case response.JSON200 == nil:
			status, message = response.StatusCode(), "Execution state could not be saved. Check target access."
		default:
			return c.Redirect().Status(http.StatusSeeOther).To(href)
		}
	}
	response, err := r.apiClient.GetPipelineExecutionControlWithResponse(c.Context(), org.String(), project.String(), environment.String(), pipeline.String(), editor)
	var state *api.PipelineExecutionControl
	switch {
	case err != nil:
		status, message = http.StatusBadGateway, "Execution status could not be loaded."
	case response.StatusCode() == http.StatusUnauthorized:
		return redirectToLogin(c, href)
	case response.JSON200 == nil:
		status, message = response.StatusCode(), "Execution status is unavailable. Check target access."
	default:
		state = response.JSON200
	}
	shell := r.buildPipelineDetailShellData(c, org.String(), project.String(), pipeline.String(), "", "")
	shell.Title = "Pause and drain"
	shell.Subtitle = ""
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Status(status)
	return renderTempl(c, views.PipelineExecutionControlPage(shell, org.String(), project.String(), environment.String(), pipeline.String(), state, canUpdate, message))
}
