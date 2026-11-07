package frontend

import (
	"context"
	"net/http"
	"strconv"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

type recencyCapabilitiesLoadedKey struct{}

func (r *Router) loadOperationalCapabilities(c fiber.Ctx, org string) {
	if _, exists := c.Context().Value(recencyCapabilitiesLoadedKey{}).(bool); exists {
		return
	}
	response, err := r.apiClient.GetPipelineOperationalCapabilitiesWithResponse(c.Context(), org, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	var capabilities *api.PipelineOperationalCapabilities
	if err == nil {
		capabilities = response.JSON200
	}
	c.SetContext(views.WithOperationalCapabilities(c.Context(), capabilities))
	c.SetContext(context.WithValue(c.Context(), recencyCapabilitiesLoadedKey{}, true))
}

func (r *Router) handlePipelineRecency(c fiber.Ctx) error {
	org, project, ok := projectActionSlugs(c)
	if !ok {
		return c.SendStatus(http.StatusBadRequest)
	}
	pipeline, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	environment, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), org.String())
	if !permissions.Can("pipelines", "read") {
		return c.SendStatus(http.StatusForbidden)
	}
	editor := frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie))
	message := ""
	status := http.StatusOK
	if c.Method() == http.MethodPost {
		if !permissions.Can("pipelines", "update") {
			return c.SendStatus(http.StatusForbidden)
		}
		revision, revisionErr := strconv.ParseInt(c.FormValue("expected_revision"), 10, 64)
		age, ageErr := strconv.ParseInt(c.FormValue("maximum_age_seconds"), 10, 64)
		enabled := c.FormValue("enabled")
		if revisionErr != nil || ageErr != nil || revision < 0 || age < 1 || age > 9223372036 || (enabled != "true" && enabled != "false") {
			message, status = "Enter a positive whole-second maximum age and select enabled or disabled.", http.StatusBadRequest
		} else {
			response, updateErr := r.apiClient.UpdateSuccessfulRunRecencyWithResponse(c.Context(), org.String(), project.String(), environment.String(), pipeline.String(), api.SuccessfulRunRecencyUpdate{ExpectedRevision: revision, MaximumAgeSeconds: age, Enabled: enabled == "true"}, editor)
			switch {
			case updateErr != nil:
				message, status = "Expectation could not be saved. Reload and try again.", http.StatusBadGateway
			case response.StatusCode() == http.StatusUnauthorized:
				return redirectToLogin(c, c.OriginalURL())
			case response.StatusCode() == http.StatusConflict:
				message, status = "The expectation changed. Review the current revision below before saving again.", http.StatusConflict
			case response.JSON200 == nil:
				message, status = "Expectation could not be saved. Check target access and capability availability.", response.StatusCode()
			default:
				return c.Redirect().Status(http.StatusSeeOther).To(views.RecencyHref(org.String(), project.String(), environment.String(), pipeline.String()))
			}
		}
	}
	response, err := r.apiClient.GetSuccessfulRunRecencyWithResponse(c.Context(), org.String(), project.String(), environment.String(), pipeline.String(), editor)
	var inspection *api.SuccessfulRunRecency
	switch {
	case err != nil:
		status, message = http.StatusBadGateway, "Recency evidence could not be loaded. Try again."
	case response.StatusCode() == http.StatusUnauthorized:
		return redirectToLogin(c, c.OriginalURL())
	case response.JSON200 == nil:
		status, message = response.StatusCode(), "Recency evidence is unavailable. Check the selected target and access."
	default:
		inspection = response.JSON200
	}
	shell := r.buildPipelineDetailShellData(c, org.String(), project.String(), pipeline.String(), "", "")
	shell.Title, shell.Subtitle = "Successful-run recency", "Inspect the selected deployment and its recency expectation."
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Status(status)
	return renderTempl(c, views.RunRecencyPage(shell, org.String(), project.String(), environment.String(), pipeline.String(), inspection, permissions.Can("pipelines", "update"), permissions.Can("alert_policies", "create"), message))
}
