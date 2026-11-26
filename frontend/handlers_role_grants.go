package frontend

import (
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func (r *Router) handleRoleGrantsPage(c fiber.Ctx) error {
	return r.renderRoleGrantsPage(c, http.StatusOK, api.NewRoleGrant{
		SubjectKind: api.NewRoleGrantSubjectKind(c.Query("subject_kind")),
		SubjectSlug: c.Query("subject_slug"),
	}, "", "")
}

func (r *Router) handleCreateRoleGrantSubmit(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid organization slug")
	}
	form := roleGrantFormFromRequest(c)
	ctx := c.Context()
	response, err := r.apiClient.CreateRoleGrantWithResponse(ctx, org.String(), form, frontendRequestEditor(ctx, c.Get(fiber.HeaderCookie)))
	if err != nil {
		return r.renderRoleGrantsPage(c, http.StatusBadGateway, form, "Could not add the grant. Try again.", "")
	}
	if response.StatusCode() == http.StatusCreated {
		return redirectHTMXOrSeeOther(c, views.RoleGrantsHref(org.String(), c.Query("subject_kind"), c.Query("subject_slug")))
	}
	return r.renderRoleGrantsPage(c, response.StatusCode(), form, roleGrantError(response.StatusCode(), response.Body, "Could not add the grant."), "")
}

func (r *Router) handleRevokeRoleGrantSubmit(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid organization slug")
	}
	grant, err := slug.New(c.Params("grantSlug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid grant slug")
	}
	ctx := c.Context()
	response, err := r.apiClient.RevokeRoleGrantWithResponse(ctx, org.String(), grant.String(), frontendRequestEditor(ctx, c.Get(fiber.HeaderCookie)))
	if err != nil {
		return r.renderRoleGrantsPage(c, http.StatusBadGateway, api.NewRoleGrant{}, "", "Could not revoke the grant. Try again.")
	}
	if response.StatusCode() == http.StatusNoContent {
		return redirectHTMXOrSeeOther(c, views.RoleGrantsHref(org.String(), c.Query("subject_kind"), c.Query("subject_slug")))
	}
	return r.renderRoleGrantsPage(c, response.StatusCode(), api.NewRoleGrant{}, "", roleGrantError(response.StatusCode(), response.Body, "Could not revoke the grant."))
}

func (r *Router) renderRoleGrantsPage(c fiber.Ctx, status int, form api.NewRoleGrant, formError, listError string) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid organization slug")
	}
	if status == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	ctx, cookie := c.Context(), c.Get(fiber.HeaderCookie)
	organization, orgStatus, _, err := r.getOrganizationViaAPI(ctx, cookie, org.String())
	if err != nil {
		if orgStatus == http.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		setHTMLResponseStatus(c, orgStatus)
		shell := r.buildShellDataForOrganizationPage(c, "Role grants", org.String(), "", "", cookie)
		return renderTempl(c, views.OrganizationDetailUnavailablePage(shell, org.String(), "Could not load the organization."))
	}

	subjectKind, subjectSlug := c.Query("subject_kind"), c.Query("subject_slug")
	page := views.RoleGrantsPageData{
		Organization: *organization,
		Form:         form,
		FormError:    formError,
		ListError:    listError,
		SubjectKind:  subjectKind,
		SubjectSlug:  subjectSlug,
	}
	response, err := r.apiClient.ListRoleGrantsWithResponse(ctx, org.String(), frontendRequestEditor(ctx, cookie))
	switch {
	case err != nil:
		page.ListError = "Could not load role grants. Try again."
		if status < http.StatusBadRequest {
			status = http.StatusBadGateway
		}
	case response.StatusCode() == http.StatusUnauthorized:
		return redirectToLogin(c, c.OriginalURL())
	case response.JSON200 != nil:
		page.ListAvailable = true
		for _, grant := range response.JSON200.Grants {
			if subjectSlug == "" || (string(grant.SubjectKind) == subjectKind && grant.SubjectSlug == subjectSlug) {
				page.Grants = append(page.Grants, grant)
			}
		}
	default:
		page.ListError = roleGrantError(response.StatusCode(), response.Body, "Could not load role grants.")
		if status < http.StatusBadRequest {
			status = response.StatusCode()
		}
	}
	page.CanManage = page.ListAvailable && r.resolveOrganizationPermissions(ctx, cookie, org.String()).Can("roles", "update")
	if page.CanManage {
		page.Roles, _, _, _ = r.listRolesViaAPI(ctx, cookie, org.String(), false, false)
	}

	shell := r.buildShellDataForOrganizationPage(c, "Role grants", org.String(), "", "", cookie)
	shell.Breadcrumbs = []views.BreadcrumbItem{
		{Label: "Organizations", Href: "/organizations"},
		{Label: organization.Name, Href: "/organizations/" + organization.Slug},
		{Label: "Grants"},
	}
	setHTMLResponseStatus(c, status)
	return renderTempl(c, views.OrganizationRoleGrantsPage(shell, page))
}

func roleGrantFormFromRequest(c fiber.Ctx) api.NewRoleGrant {
	form := c.Request().PostArgs()
	field := func(name string) string {
		return strings.TrimSpace(string(form.Peek(name)))
	}
	optional := func(name string) *string {
		value := field(name)
		if value == "" {
			return nil
		}
		return &value
	}
	return api.NewRoleGrant{
		SubjectKind:     api.NewRoleGrantSubjectKind(field("subject_kind")),
		SubjectSlug:     field("subject_slug"),
		RoleSlug:        field("role_slug"),
		Scope:           api.NewRoleGrantScope(field("scope")),
		TargetKind:      optional("target_kind"),
		TargetSlug:      optional("target_slug"),
		ProjectSlug:     optional("project_slug"),
		EnvironmentSlug: optional("environment_slug"),
		DatasourceSlug:  optional("datasource_slug"),
		ParentSlug:      optional("parent_slug"),
	}
}

func roleGrantError(status int, body []byte, fallback string) string {
	if status == http.StatusForbidden {
		return "You do not have permission to perform this grant operation."
	}
	if status == http.StatusNotFound {
		return "The grant, subject, role, or target could not be found in this organization. Check the slugs and scope."
	}
	if status == http.StatusConflict {
		return "That role grant already exists."
	}
	if status == http.StatusBadRequest {
		if message := messageFromErrorResponse(parseAPIErrorResponse(body)); message != "" {
			return message
		}
	}
	return fallback
}
