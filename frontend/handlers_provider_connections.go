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
	case "connect_failed":
		feedback, failed = gitLabConnectStartError, true
	case "disconnect_failed":
		feedback, failed = "Could not disconnect GitLab. Check your access and retry.", true
	}
	if fiber.Query[bool](c, "saved") {
		feedback, failed = "Proof configuration saved.", false
	}
	permissions := r.resolvePagePermissions(c, org.String())
	return renderTempl(c, views.ProviderConnectionsPage(shell, org.String(), gitlab, configurations, feedback, failed, connectionError, proofError, permissions))
}

const gitLabConnectStartError = "Could not start GitLab authorization. Try connecting again."

func (r *Router) handleOrganizationGitLabConnect(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	returnURL := "/organizations/" + org.String() + "/connections"
	includeWrite := c.FormValue("include_write_scopes") == "on"
	request := api.StartOrganizationGitLabAuthorizationJSONRequestBody{IncludeWriteScopes: includeWrite}
	if moreTime := c.FormValue("more_time") == "true"; moreTime {
		request.MoreTime = &moreTime
	}
	response, err := r.apiClient.StartOrganizationGitLabAuthorizationWithResponse(c.Context(), org.String(), request, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err == nil && response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, returnURL)
	}
	if err != nil || response.StatusCode() != http.StatusCreated || response.JSON201 == nil {
		return providerConnectionFailure(c, returnURL+"?provider_oauth=connect_failed", gitLabConnectStartError)
	}
	return redirectHTMXOrSeeOther(c, response.JSON201.AuthorizationUrl)
}

func (r *Router) handleOrganizationGitLabDisconnect(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	returnURL := "/organizations/" + org.String() + "/connections"
	response, err := r.apiClient.DisconnectOrganizationGitLabConnectionWithResponse(c.Context(), org.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err == nil && response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, returnURL)
	}
	if err != nil || response.StatusCode() != http.StatusOK {
		return redirectHTMXOrSeeOther(c, returnURL+"?provider_oauth=disconnect_failed")
	}
	return redirectHTMXOrSeeOther(c, returnURL+"?provider_oauth=disconnected")
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
	returnURL := dataSourceReturnURL(c, org.String(), ds.String())
	const fallback = "Could not attach the organization GitLab connection. Check repository access and retry."
	response, err := r.apiClient.AttachDataSourceOrganizationGitLabConnectionWithResponse(c.Context(), org.String(), ds.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(returnURL, "error", fallback))
	}
	if response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, returnURL)
	}
	if response.StatusCode() != http.StatusNoContent {
		message := cmp.Or(messageFromErrorResponse(cmp.Or(response.JSON400, response.JSON403, response.JSON404, response.JSON409, response.JSONDefault)), fallback)
		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(returnURL, "error", message))
	}
	return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(returnURL, "notice", "Organization GitLab connection attached."))
}

func providerConnectionFailure(c fiber.Ctx, returnURL, message string) error {
	if isHTMXRequest(c) {
		c.Set("HX-Retarget", "#provider-connections-feedback")
		c.Set("HX-Reswap", "outerHTML")
		return renderTempl(c, views.ProviderConnectionsFeedback(message, true))
	}
	return redirectHTMXOrSeeOther(c, returnURL)
}
