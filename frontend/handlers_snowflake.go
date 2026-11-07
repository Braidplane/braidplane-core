package frontend

import (
	"net/http"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) handleSnowflakeOnboarding(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	ds, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), org.String())
	resp, err := r.apiClient.GetSnowflakeOnboardingWithResponse(c.Context(), org.String(), ds.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	var onboarding *api.SnowflakeOnboarding
	message := "Onboarding is unavailable. Check the connection's workload identity settings and selected tables, then retry."
	if err == nil && resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		onboarding = resp.JSON200
		message = ""
	}
	if err == nil && resp.StatusCode() == http.StatusNotFound {
		return c.SendString("")
	}
	if err == nil && resp.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	return renderTempl(c, views.SnowflakeOnboardingPanel(org.String(), dataSourceActiveEnvironmentSlug(c), ds.String(), onboarding, message, permissions.Can("datasources", "update")))
}

func (r *Router) handleSnowflakeIdentity(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	ds, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	enabled := c.FormValue("enabled") == "true"
	if value := c.FormValue("enabled"); value != "true" && value != "false" {
		return c.SendStatus(http.StatusBadRequest)
	}
	_, status, errorResponse, err := r.updateDataSourceViaAPI(c.Context(), c.Get(fiber.HeaderCookie), org.String(), ds.String(), api.UpdateDataSourceJSONRequestBody{IsActive: &enabled})
	if status == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	key, message := "notice", "Identity revocation requested. Existing Snowflake sessions may require termination."
	if enabled {
		message = "Identity provisioning requested. Run the SQL script, then Verify after readiness."
	}
	if err != nil {
		key = "error"
		message = "Could not disable identity."
		if enabled {
			message = "Could not enable identity."
		}
		message = formFeedbackFromErrorResponse(errorResponse, message)
	}
	return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(dataSourceReturnURL(c, org.String(), ds.String()), key, message))
}
