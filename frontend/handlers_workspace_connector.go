package frontend

import (
	"net/http"

	"github.com/Braidplane/braidplane-core/pkg/slug"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) handleStartWorkspaceDataSourceOAuthSubmit(c fiber.Ctx) error {
	return r.handleStartProviderDataSourceOAuthSubmit(c, "google_workspace", "Google")
}

func (r *Router) handleStartExistingWorkspaceOAuthSubmit(c fiber.Ctx) error {
	return r.handleStartExistingProviderOAuthSubmit(c, "google_workspace")
}

func (r *Router) handleWorkspaceDiscovery(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	source, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	params := &api.ListGoogleWorkspaceFilesParams{}
	if v := c.Query("folder_id"); v != "" {
		params.FolderId = &v
	}
	if v := c.Query("drive_id"); v != "" {
		params.DriveId = &v
	}
	if v := c.Query("mime_type"); v != "" {
		params.MimeType = &v
	}
	if token := c.Query("page_token"); token != "" {
		params.PageToken = &token
	}
	response, err := r.apiClient.ListGoogleWorkspaceFilesWithResponse(c.Context(), org.String(), source.String(), params, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return c.Status(503).SendString("File discovery unavailable")
	}
	if response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if response.StatusCode() != http.StatusOK {
		return c.Status(response.StatusCode()).JSON(fiber.Map{"message": "File discovery unavailable"})
	}
	c.Type("json")
	return c.Status(response.StatusCode()).Send(response.Body)
}

func (r *Router) handleWorkspaceWorksheets(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	source, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	response, err := r.apiClient.GetGoogleWorkspaceWorksheetsWithResponse(c.Context(), org.String(), source.String(), c.Query("spreadsheet_id"), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return c.Status(503).SendString("Worksheet metadata unavailable")
	}
	if response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if response.StatusCode() != http.StatusOK {
		return c.Status(response.StatusCode()).JSON(fiber.Map{"message": "Worksheet metadata unavailable"})
	}
	c.Type("json")
	return c.Status(response.StatusCode()).Send(response.Body)
}

func (r *Router) handleWorkspacePicker(c fiber.Ctx) error {
	org, source, err := workspaceRouteSlugs(c)
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	response, err := r.apiClient.GetGoogleWorkspacePickerWithResponse(c.Context(), org.String(), source.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{"message": "Google Picker unavailable"})
	}
	c.Set("Cache-Control", "no-store")
	if response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if response.StatusCode() != http.StatusOK {
		return c.Status(response.StatusCode()).JSON(fiber.Map{"message": "Google Picker unavailable"})
	}
	c.Type("json")
	return c.Status(response.StatusCode()).Send(response.Body)
}

func (r *Router) handleWorkspaceVerify(c fiber.Ctx) error {
	org, source, err := workspaceRouteSlugs(c)
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	var body api.VerifyGoogleWorkspaceFilesJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	response, err := r.apiClient.VerifyGoogleWorkspaceFilesWithResponse(c.Context(), org.String(), source.String(), body, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{"message": "File verification unavailable"})
	}
	c.Set("Cache-Control", "no-store")
	if response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if response.StatusCode() != http.StatusOK {
		return c.Status(response.StatusCode()).JSON(fiber.Map{"message": "File verification unavailable"})
	}
	c.Type("json")
	return c.Status(response.StatusCode()).Send(response.Body)
}

func workspaceRouteSlugs(c fiber.Ctx) (slug.Slug, slug.Slug, error) {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return "", "", err
	}
	source, err := slug.New(c.Params("dsSlug"))
	return org, source, err
}
