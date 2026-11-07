package frontend

import (
	"cmp"
	"net/http"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) handleProviderConnectionsPage(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	var gitlab *api.CredentialMetadata
	connectionError := ""
	response, err := r.apiClient.GetOrganizationGitLabConnectionWithResponse(c.Context(), org.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	switch {
	case err != nil || response.StatusCode() >= http.StatusInternalServerError:
		connectionError = "GitLab connection could not be loaded. Reload to try again."
	case response.StatusCode() == http.StatusForbidden:
		connectionError = "Organization read permission is required to view shared connections."
	case response.StatusCode() == http.StatusOK && response.JSON200 != nil:
		gitlab = response.JSON200
	case response.StatusCode() == http.StatusOK:
		connectionError = "GitLab connection could not be loaded. Reload to try again."
	case response.StatusCode() != http.StatusNoContent:
		return c.SendStatus(response.StatusCode())
	}
	configurations := []api.DataSourceProofConfiguration{}
	proofError := ""
	proof, err := r.apiClient.ListProofConfigurationsWithResponse(c.Context(), org.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	switch {
	case err != nil || proof.StatusCode() >= http.StatusInternalServerError:
		proofError = "Proof settings could not be loaded. Reload to try again."
	case proof.StatusCode() == http.StatusForbidden:
		proofError = "Data source and pipeline read permissions are required to view Proof settings."
	case proof.StatusCode() == http.StatusOK && proof.JSON200 != nil:
		configurations = *proof.JSON200
	case proof.StatusCode() == http.StatusOK:
		proofError = "Proof settings could not be loaded. Reload to try again."
	default:
		return c.SendStatus(proof.StatusCode())
	}
	if response != nil && proof != nil && response.StatusCode() == http.StatusForbidden && proof.StatusCode() == http.StatusForbidden {
		return fiber.ErrForbidden
	}
	shell := r.buildShellDataForOrganizationPage(c, "Git integrations", org.String(), "", "", c.Get(fiber.HeaderCookie))
	shell.Breadcrumbs = []views.BreadcrumbItem{{Label: shell.ActiveOrganizationName, Href: "/organizations/" + org.String()}, {Label: "Git integrations"}}
	feedback, failed := "", false
	switch c.Query("provider_oauth") {
	case "success":
		feedback = "GitLab connected. Select repositories when creating managed data sources."
	case "disconnected":
		feedback = "GitLab disconnected. Attached data sources need a new connection before repository operations can resume."
	case "denied":
		feedback, failed = "GitLab authorization was declined. Existing authorization has not been replaced.", true
	case "expired":
		feedback, failed = "GitLab authorization expired. Start a new connection attempt.", true
	case "error":
		feedback, failed = "GitLab authorization could not be completed. Try connecting again.", true
	}
	if fiber.Query[bool](c, "saved") {
		feedback = "Proof configuration saved."
	}
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), org.String())
	return renderTempl(c, views.ProviderConnectionsPage(shell, org.String(), gitlab, configurations, feedback, failed, connectionError, proofError, permissions))
}

func (r *Router) handleOrganizationGitLabConnect(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	response, err := r.apiClient.StartOrganizationGitLabAuthorizationWithResponse(c.Context(), org.String(), api.StartOrganizationGitLabAuthorizationJSONRequestBody{IncludeWriteScopes: c.FormValue("include_write_scopes") == "on"}, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil || response.StatusCode() != http.StatusCreated || response.JSON201 == nil {
		return fiber.ErrServiceUnavailable
	}
	return redirectHTMXOrSeeOther(c, response.JSON201.AuthorizationUrl)
}

func (r *Router) handleOrganizationGitLabDisconnect(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	response, err := r.apiClient.DisconnectOrganizationGitLabConnectionWithResponse(c.Context(), org.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	if response.StatusCode() != http.StatusOK {
		return c.SendStatus(response.StatusCode())
	}
	return redirectHTMXOrSeeOther(c, "/organizations/"+org.String()+"/connections?provider_oauth=disconnected")
}

func (r *Router) handleAttachOrganizationGitLabConnection(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	ds, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	response, err := r.apiClient.AttachDataSourceOrganizationGitLabConnectionWithResponse(c.Context(), org.String(), ds.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	if response.StatusCode() != http.StatusNoContent {
		return c.Status(response.StatusCode()).SendString(formFeedbackFromErrorResponse(cmp.Or(response.JSON400, response.JSON401, response.JSON403, response.JSON404, response.JSON409, response.JSONDefault), "Could not attach the organization GitLab connection. Check repository access and retry."))
	}
	return redirectHTMXOrSeeOther(c, views.DataSourceDetailHref(org.String(), dataSourceActiveEnvironmentSlug(c), ds.String()))
}
