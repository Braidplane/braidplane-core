package frontend

import (
	"net/http"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) handleOrganizationDeletionSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	response, err := r.apiClient.DeleteOrganizationWithResponse(c.Context(), orgSlug.String(),
		api.DeleteOrganizationJSONRequestBody{Confirmation: c.FormValue("confirmation")},
		frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return c.SendStatus(fiber.StatusBadGateway)
	}
	if response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, "/organizations/"+orgSlug.String())
	}
	if response.StatusCode() != http.StatusNoContent {
		message := "Organization deletion was not confirmed."
		switch response.StatusCode() {
		case http.StatusConflict:
			message = "Remove projects and data sources and wait for outstanding processing to finish before deleting this organization."
		case http.StatusBadRequest:
			message = "Type the exact organization slug to confirm deletion."
		case http.StatusForbidden:
			message = "You do not have permission to delete this organization."
		}
		c.Status(response.StatusCode())
		return renderTempl(c, views.OrganizationDeletionError(orgSlug.String(), message))
	}
	if reqctx.GetActiveOrganizationSlug(c, r.sessionStore) == orgSlug {
		if err := reqctx.SetActiveOrganizationSlug(c, r.sessionStore, ""); err != nil {
			return err
		}
	}
	return c.Redirect().Status(fiber.StatusSeeOther).To("/account")
}
