package frontend

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const (
	githubInstallOrganizationCookie = "github_install_organization"
	githubInstallProjectCookie      = "github_install_project"
	githubInstallCookiePath         = "/github/callback"
)

func (r *Router) githubInstallCookieName(name string) string {
	return httpsecurity.BrowserCookieName(name, githubInstallCookiePath, r.secureCookies)
}

func (r *Router) setGitHubInstallCookie(c fiber.Ctx, name, value string, maxAge int) {
	cookie := &fiber.Cookie{
		Name: r.githubInstallCookieName(name), Value: value, Path: githubInstallCookiePath, MaxAge: maxAge,
		HTTPOnly: true, Secure: r.secureCookies, SameSite: fiber.CookieSameSiteLaxMode,
	}
	if maxAge < 0 {
		cookie.Expires = time.Now().Add(-time.Hour)
	}
	c.Cookie(cookie)
}

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
	case fiber.Query[bool](c, "disconnect_failed"):
		feedback = "Could not disconnect GitHub. Check your access and retry."
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
	r.setGitHubInstallCookie(c, githubInstallProjectCookie, "", -1)
	returnURL := "/organizations/" + orgSlug.String() + "/github"
	moreTime := c.FormValue("more_time") == "true"
	var params *api.StartOrganizationGitHubAppInstallParams
	if moreTime {
		params = &api.StartOrganizationGitHubAppInstallParams{MoreTime: &moreTime}
	}
	resp, err := r.apiClient.StartOrganizationGitHubAppInstallWithResponse(c.Context(), orgSlug.String(), params, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err == nil && resp.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, returnURL)
	}
	if err != nil || resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return providerConnectionFailure(c, returnURL+"?connect_failed=true", "Could not start GitHub authorization. Try connecting again.")
	}
	r.setGitHubInstallCookie(c, githubInstallOrganizationCookie, orgSlug.String(), int(oauthflow.CallbackWindow(moreTime).Seconds()))
	return redirectHTMXOrSeeOther(c, resp.JSON200.AuthorizationUrl)
}

func (r *Router) handleOrganizationGitHubAuthorizeExisting(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	r.setGitHubInstallCookie(c, githubInstallProjectCookie, "", -1)
	returnURL := "/organizations/" + orgSlug.String() + "/github"
	moreTime := c.FormValue("more_time") == "true"
	request := api.AuthorizeOrganizationGitHubAppConnectionJSONRequestBody{AccountLogin: c.FormValue("account_login")}
	if moreTime {
		request.MoreTime = &moreTime
	}
	resp, err := r.apiClient.AuthorizeOrganizationGitHubAppConnectionWithResponse(c.Context(), orgSlug.String(), request, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err == nil && resp.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, returnURL)
	}
	if err != nil || resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return providerConnectionFailure(c, returnURL+"?connect_failed=true", "GitHub could not authorize the installation. Check the account or organization and try again.")
	}
	r.setGitHubInstallCookie(c, githubInstallOrganizationCookie, orgSlug.String(), int(oauthflow.CallbackWindow(moreTime).Seconds()))
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
	org, orgErr := slug.New(c.Cookies(r.githubInstallCookieName(githubInstallOrganizationCookie)))
	project, projectErr := slug.New(c.Cookies(r.githubInstallCookieName(githubInstallProjectCookie)))
	r.setGitHubInstallCookie(c, githubInstallProjectCookie, "", -1)
	r.setGitHubInstallCookie(c, githubInstallOrganizationCookie, "", -1)
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
	path, query, _ := strings.Cut(local, "?")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 4 || parts[0] != "organizations" || parts[2] != "projects" {
		return ""
	}
	org, orgErr := slug.New(parts[1])
	project, projectErr := slug.New(parts[3])
	if orgErr != nil || projectErr != nil {
		return ""
	}
	status := "failed"
	if values, err := url.ParseQuery(query); err == nil && values.Get("repository_setup") == "expired" {
		status = "expired"
	}
	return "/organizations/" + org.String() + "/projects/" + project.String() + "?section=repository&repository_path=create&repository_setup=" + status
}

func (r *Router) handleOrganizationGitHubDisconnect(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	returnURL := "/organizations/" + orgSlug.String() + "/github"
	resp, err := r.apiClient.DisconnectOrganizationGitHubAppConnectionWithResponse(c.Context(), orgSlug.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err == nil && resp.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, returnURL)
	}
	if err != nil || resp.StatusCode() != http.StatusNoContent {
		return redirectHTMXOrSeeOther(c, returnURL+"?disconnect_failed=true")
	}
	return redirectHTMXOrSeeOther(c, returnURL+"?disconnected=true")
}
