package frontend

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const (
	githubInstallOrganizationCookie = "github_install_organization"
	githubInstallProjectCookie      = "github_install_project"
)

func (r *Router) handleOrganizationGitHubPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	organization, status, _, err := r.getOrganizationViaAPI(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String())
	if err != nil {
		return c.SendStatus(status)
	}
	resp, err := r.apiClient.GetOrganizationGitHubAppConnectionWithResponse(c.Context(), orgSlug.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	var connection *api.GitHubAppConnection
	if resp.StatusCode() == http.StatusOK {
		connection = resp.JSON200
	} else if resp.StatusCode() != http.StatusNoContent {
		return c.SendStatus(resp.StatusCode())
	}
	shell := r.buildShellDataForOrganizationPage(c, "GitHub connection", orgSlug.String(), "", "", c.Get(fiber.HeaderCookie))
	shell.Breadcrumbs = []views.BreadcrumbItem{{Label: "Organizations", Href: organizationsPath}, {Label: organization.Name, Href: "/organizations/" + orgSlug.String()}, {Label: "GitHub"}}
	feedback := ""
	feedbackError := false
	switch {
	case fiber.Query[bool](c, "connected"):
		feedback = "GitHub connected."
	case fiber.Query[bool](c, "disconnected"):
		feedback = "GitHub disconnected."
	case fiber.Query[bool](c, "connect_failed"):
		feedback = "GitHub connection could not be completed. Try connecting again."
		feedbackError = true
	}
	return renderTempl(c, views.OrganizationGitHubPage(shell, *organization, connection, feedback, feedbackError))
}

func parsePositiveInt64(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fiber.ErrBadRequest
	}
	return parsed, nil
}

func (r *Router) handleOrganizationGitHubConnect(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	c.Cookie(&fiber.Cookie{Name: githubInstallProjectCookie, Value: "", Path: "/github/callback", MaxAge: -1, HTTPOnly: true, Secure: r.secureCookies, SameSite: fiber.CookieSameSiteLaxMode})
	resp, err := r.apiClient.StartOrganizationGitHubAppInstallWithResponse(c.Context(), orgSlug.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil || resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return fiber.ErrServiceUnavailable
	}
	c.Cookie(&fiber.Cookie{Name: githubInstallOrganizationCookie, Value: orgSlug.String(), Path: "/github/callback", MaxAge: 600, HTTPOnly: true, Secure: r.secureCookies, SameSite: fiber.CookieSameSiteLaxMode})
	return redirectHTMXOrSeeOther(c, resp.JSON200.AuthorizationUrl)
}

func (r *Router) handleOrganizationGitHubAuthorizeExisting(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	c.Cookie(&fiber.Cookie{Name: githubInstallProjectCookie, Value: "", Path: "/github/callback", MaxAge: -1, HTTPOnly: true, Secure: r.secureCookies, SameSite: fiber.CookieSameSiteLaxMode})
	resp, err := r.apiClient.AuthorizeOrganizationGitHubAppConnectionWithResponse(c.Context(), orgSlug.String(), api.AuthorizeOrganizationGitHubAppConnectionJSONRequestBody{AccountLogin: c.FormValue("account_login")}, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	switch resp.StatusCode() {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return c.SendStatus(resp.StatusCode())
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return fiber.ErrServiceUnavailable
	}
	c.Cookie(&fiber.Cookie{Name: githubInstallOrganizationCookie, Value: orgSlug.String(), Path: "/github/callback", MaxAge: 600, HTTPOnly: true, Secure: r.secureCookies, SameSite: fiber.CookieSameSiteLaxMode})
	return redirectHTMXOrSeeOther(c, resp.JSON200.AuthorizationUrl)
}

func (r *Router) handleOrganizationGitHubCallback(c fiber.Ctx) error {
	params := &api.CompleteOrganizationGitHubAppInstallParams{State: c.Query("state")}
	if providerError := c.Query("error"); providerError != "" {
		result := api.CompleteOrganizationGitHubAppInstallParamsError(providerError)
		params.Error = &result
	} else {
		code := c.Query("code")
		params.Code = &code
		if rawInstallationID := c.Query("installation_id"); rawInstallationID != "" {
			installationID, err := parsePositiveInt64(rawInstallationID)
			if err != nil {
				return fiber.ErrBadRequest
			}
			params.InstallationId = &installationID
		}
	}
	org, orgErr := slug.New(c.Cookies(githubInstallOrganizationCookie))
	project, projectErr := slug.New(c.Cookies(githubInstallProjectCookie))
	c.Cookie(&fiber.Cookie{Name: githubInstallProjectCookie, Value: "", Path: "/github/callback", Expires: time.Now().Add(-time.Hour), MaxAge: -1, HTTPOnly: true, Secure: r.secureCookies, SameSite: fiber.CookieSameSiteLaxMode})
	c.Cookie(&fiber.Cookie{Name: githubInstallOrganizationCookie, Value: "", Path: "/github/callback", Expires: time.Now().Add(-time.Hour), MaxAge: -1, HTTPOnly: true, Secure: r.secureCookies, SameSite: fiber.CookieSameSiteLaxMode})
	failurePath := organizationsPath
	if orgErr == nil {
		failurePath = "/organizations/" + org.String() + "/github?connect_failed=true"
		if projectErr == nil {
			failurePath = "/organizations/" + org.String() + "/projects/" + project.String() + "?section=repository&repository_path=create&repository_setup=failed"
		}
	}
	resp, err := r.apiClient.CompleteOrganizationGitHubAppInstallWithResponse(c.Context(), params, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil || resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		if resp != nil && resp.StatusCode() == http.StatusBadRequest && resp.HTTPResponse != nil {
			if recovery := repositorySetupFailureRedirect(resp.HTTPResponse.Header.Get(fiber.HeaderLocation), c.Host()); recovery != "" {
				failurePath = recovery
			}
		}
		return c.Redirect().Status(fiber.StatusSeeOther).To(failurePath)
	}
	if resp.JSON200.RepositorySetup != nil {
		project, err := slug.New(resp.JSON200.RepositorySetup.ProjectSlug)
		if err != nil {
			return fiber.ErrBadGateway
		}
		return c.Redirect().Status(fiber.StatusSeeOther).To("/organizations/" + resp.JSON200.OrganizationSlug + "/projects/" + project.String() + "?section=repository&repository_setup=complete")
	}
	return c.Redirect().Status(fiber.StatusSeeOther).To("/organizations/" + resp.JSON200.OrganizationSlug + "/github?connected=true")
}

func repositorySetupFailureRedirect(raw, currentHost string) string {
	local := safeCurrentRequestPath(raw, currentHost)
	path, _, _ := strings.Cut(local, "?")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 4 || parts[0] != "organizations" || parts[2] != "projects" {
		return ""
	}
	org, orgErr := slug.New(parts[1])
	project, projectErr := slug.New(parts[3])
	if orgErr != nil || projectErr != nil {
		return ""
	}
	return "/organizations/" + org.String() + "/projects/" + project.String() + "?section=repository&repository_path=create&repository_setup=failed"
}

func (r *Router) handleOrganizationGitHubDisconnect(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	resp, err := r.apiClient.DisconnectOrganizationGitHubAppConnectionWithResponse(c.Context(), orgSlug.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil || resp.StatusCode() != http.StatusNoContent {
		return fiber.ErrServiceUnavailable
	}
	return c.Redirect().Status(fiber.StatusSeeOther).To("/organizations/" + orgSlug.String() + "/github?disconnected=true")
}
