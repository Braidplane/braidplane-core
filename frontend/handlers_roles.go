package frontend

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// handleOrganizationRolesPage renders the organization role-management tab.
func (r *Router) handleOrganizationRolesPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	return r.renderOrganizationRolesPage(
		c,
		orgSlug,
		http.StatusOK,
		roleListViewFromRequest(c),
		views.DefaultRoleFormData(),
		"",
		"",
		"",
	)
}

// handleCreateRoleSubmit validates and creates a custom organization role.
func (r *Router) handleCreateRoleSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	formData := roleFormDataFromRequest(c)
	if strings.TrimSpace(formData.Name) == "" {
		return r.renderOrganizationRolesPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.RoleListViewActive,
			formData,
			"Role could not be created.",
			"Validation: role name is required.",
			"",
		)
	}

	payload, validationErr := roleCreatePayloadFromForm(formData)
	if validationErr != "" {
		return r.renderOrganizationRolesPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.RoleListViewActive,
			formData,
			"Role could not be created.",
			validationErr,
			"",
		)
	}

	_, status, errorResponse, createErr := r.createRoleViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		payload,
	)
	if createErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Role could not be created."
		}
		return r.renderOrganizationRolesPage(
			c,
			orgSlug,
			status,
			views.RoleListViewActive,
			formData,
			message,
			backendErrorDetails("create role", status, errorResponse, createErr),
			"",
		)
	}

	return redirectHTMXOrSeeOther(c, "/organizations/"+orgSlug.String()+"/roles")
}

// handleUpdateRoleSubmit validates and updates a custom organization role.
func (r *Router) handleUpdateRoleSubmit(c fiber.Ctx) error {
	orgSlug, roleSlug, err := parseOrganizationRoleSlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	formData := roleFormDataFromRequest(c)
	if strings.TrimSpace(formData.Name) == "" {
		return r.renderOrganizationRolesPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.RoleListViewActive,
			views.DefaultRoleFormData(),
			"Role could not be updated.",
			"Validation: role name is required.",
			"",
		)
	}

	payload, validationErr := roleUpdatePayloadFromForm(formData)
	if validationErr != "" {
		return r.renderOrganizationRolesPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.RoleListViewActive,
			views.DefaultRoleFormData(),
			"Role could not be updated.",
			validationErr,
			"",
		)
	}

	status, errorResponse, updateErr := r.updateRoleViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		roleSlug.String(),
		payload,
	)
	if updateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Role could not be updated."
		}
		return r.renderOrganizationRolesPage(
			c,
			orgSlug,
			status,
			views.RoleListViewActive,
			views.DefaultRoleFormData(),
			message,
			backendErrorDetails("update role", status, errorResponse, updateErr),
			"",
		)
	}

	return redirectHTMXOrSeeOther(c, "/organizations/"+orgSlug.String()+"/roles")
}

// handleArchiveRoleSubmit archives a custom organization role after typed confirmation.
func (r *Router) handleArchiveRoleSubmit(c fiber.Ctx) error {
	orgSlug, roleSlug, err := parseOrganizationRoleSlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	if !roleDeleteConfirmationMatches(c, roleSlug) {
		return r.renderOrganizationRolesPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.RoleListViewActive,
			views.DefaultRoleFormData(),
			"Role could not be archived.",
			"Validation: type the role slug to confirm archival.",
			"",
		)
	}

	status, errorResponse, archiveErr := r.archiveRoleViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		roleSlug.String(),
	)
	if archiveErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Role could not be archived."
		}
		return r.renderOrganizationRolesPage(
			c,
			orgSlug,
			status,
			views.RoleListViewActive,
			views.DefaultRoleFormData(),
			message,
			backendErrorDetails("archive role", status, errorResponse, archiveErr),
			"",
		)
	}

	return redirectHTMXOrSeeOther(c, "/organizations/"+orgSlug.String()+"/roles")
}

// handleRestoreRoleSubmit restores an archived custom organization role.
func (r *Router) handleRestoreRoleSubmit(c fiber.Ctx) error {
	orgSlug, roleSlug, err := parseOrganizationRoleSlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	status, errorResponse, restoreErr := r.restoreRoleViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		roleSlug.String(),
	)
	if restoreErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Role could not be restored."
		}
		return r.renderOrganizationRolesPage(
			c,
			orgSlug,
			status,
			views.RoleListViewArchived,
			views.DefaultRoleFormData(),
			message,
			backendErrorDetails("restore role", status, errorResponse, restoreErr),
			"",
		)
	}

	return redirectHTMXOrSeeOther(c, views.RoleListViewHref(orgSlug.String(), views.RoleListViewArchived))
}

// renderOrganizationRolesPage loads organization role data and renders the tab.
func (r *Router) renderOrganizationRolesPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	statusCode int,
	roleListView string,
	createFormData views.RoleFormData,
	contentErrorSummary string,
	contentErrorDetails string,
	contentSuccessSummary string,
) error {
	roleListView = views.NormalizeRoleListView(roleListView)
	organization, status, errorResponse, getErr := r.getOrganizationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = fmt.Sprintf("Failed to load organization (status %d).", status)
		}

		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Organization roles",
			orgSlug.String(),
			message,
			errorPopupDetails(errorResponse, ""),
			c.Get(fiber.HeaderCookie),
		)
		shellData.Breadcrumbs = []views.BreadcrumbItem{
			{Label: "Organizations", Href: "/organizations"},
			{Label: orgSlug.String(), Href: "/organizations/" + orgSlug.String()},
			{Label: "Roles"},
		}

		c.Status(status)
		return renderTempl(
			c,
			views.OrganizationDetailUnavailablePage(shellData, orgSlug.String(), message),
		)
	}

	var roles []api.OrgRole
	roles, rolesStatus, rolesErrorResponse, rolesErr := r.listRolesViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		views.RoleListIncludesArchived(roleListView),
		views.RoleListOnlyArchived(roleListView),
	)
	if rolesErr != nil {
		if rolesStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if contentErrorSummary == "" {
			if rolesStatus == fiber.StatusForbidden {
				contentErrorSummary = "You do not have access to this organization's roles."
			} else {
				contentErrorSummary = "Organization roles are temporarily unavailable."
			}
			contentErrorDetails = backendErrorDetails(
				"list roles",
				rolesStatus,
				rolesErrorResponse,
				rolesErr,
			)
		}
		if statusCode < http.StatusBadRequest && rolesStatus >= http.StatusBadRequest {
			statusCode = rolesStatus
		}
	} else if roles == nil {
		roles = []api.OrgRole{}
	}

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Organization roles",
		orgSlug.String(),
		"",
		"",
		c.Get(fiber.HeaderCookie),
	)
	shellData.Breadcrumbs = []views.BreadcrumbItem{
		{Label: "Organizations", Href: "/organizations"},
		{Label: organization.Name, Href: "/organizations/" + organization.Slug},
		{Label: "Roles"},
	}

	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	setHTMLResponseStatus(c, statusCode)
	return renderTempl(
		c,
		views.OrganizationRolesPage(
			shellData,
			*organization,
			roles,
			createFormData,
			roleListView,
			permissions,
			contentErrorSummary,
			contentErrorDetails,
			contentSuccessSummary,
		),
	)
}

// roleFormDataFromRequest reads role form fields and repeated permission rows.
func roleFormDataFromRequest(c fiber.Ctx) views.RoleFormData {
	return views.RoleFormData{
		Name:        strings.TrimSpace(c.FormValue("name")),
		Slug:        strings.TrimSpace(c.FormValue("slug")),
		Permissions: rolePermissionRowsFromRequest(c),
	}
}

// roleCreatePayloadFromForm converts form data into a create-role API payload.
func roleCreatePayloadFromForm(formData views.RoleFormData) (api.CreateRoleJSONRequestBody, string) {
	permissions, validationErr := rolePermissionsFromForm(formData.Permissions)
	if validationErr != "" {
		return api.CreateRoleJSONRequestBody{}, validationErr
	}

	payload := api.CreateRoleJSONRequestBody{
		Name:        strings.TrimSpace(formData.Name),
		Permissions: permissions,
	}
	if strings.TrimSpace(formData.Slug) != "" {
		roleSlug, err := slug.New(formData.Slug)
		if err != nil {
			return api.CreateRoleJSONRequestBody{}, "Validation: role slug must use lowercase letters, digits, and hyphens only."
		}
		value := roleSlug.String()
		payload.Slug = &value
	}

	return payload, ""
}

// roleUpdatePayloadFromForm converts form data into an update-role API payload.
func roleUpdatePayloadFromForm(formData views.RoleFormData) (api.UpdateRoleJSONRequestBody, string) {
	permissions, validationErr := rolePermissionsFromForm(formData.Permissions)
	if validationErr != "" {
		return api.UpdateRoleJSONRequestBody{}, validationErr
	}

	return api.UpdateRoleJSONRequestBody{
		Name:        strings.TrimSpace(formData.Name),
		Permissions: permissions,
	}, ""
}

// rolePermissionsFromForm normalizes non-empty resource/action pairs for role APIs.
func rolePermissionsFromForm(rows []views.RolePermissionFormData) ([]api.Permission, string) {
	permissions := make([]api.Permission, 0, len(rows))
	for _, row := range rows {
		resource := strings.TrimSpace(row.Resource)
		action := strings.TrimSpace(row.Action)
		if resource == "" && action == "" {
			continue
		}
		if resource == "" || action == "" {
			return nil, "Validation: every permission row needs both resource and action."
		}

		permissions = append(permissions, api.Permission{
			Resource: resource,
			Action:   api.PermissionAction(action),
		})
	}

	return permissions, ""
}

// rolePermissionRowsFromRequest preserves repeated permission rows from the form body.
func rolePermissionRowsFromRequest(c fiber.Ctx) []views.RolePermissionFormData {
	resources := repeatedFormValues(c, "permission_resource")
	actions := repeatedFormValues(c, "permission_action")
	count := len(resources)
	if len(actions) > count {
		count = len(actions)
	}

	rows := make([]views.RolePermissionFormData, 0, count)
	for idx := 0; idx < count; idx++ {
		row := views.RolePermissionFormData{}
		if idx < len(resources) {
			row.Resource = resources[idx]
		}
		if idx < len(actions) {
			row.Action = actions[idx]
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return views.RolePermissionRows(nil)
	}
	return rows
}

// repeatedFormValues returns all values for a form field in submitted order.
func repeatedFormValues(c fiber.Ctx, name string) []string {
	values := []string{}
	for _, value := range c.Request().PostArgs().PeekMulti(name) {
		values = append(values, strings.TrimSpace(string(value)))
	}
	return values
}

// roleDeleteConfirmationMatches checks the typed destructive role confirmation.
func roleDeleteConfirmationMatches(c fiber.Ctx, roleSlug slug.Slug) bool {
	return strings.TrimSpace(c.FormValue("role_confirmation")) == roleSlug.String()
}

// roleListViewFromRequest resolves active and archived role-list modes from query params.
func roleListViewFromRequest(c fiber.Ctx) string {
	if strings.EqualFold(strings.TrimSpace(c.Query("deleted")), "true") {
		return views.RoleListViewArchived
	}
	return views.NormalizeRoleListView(c.Query("view"))
}

// parseOrganizationRoleSlugs validates organization and role slugs from route params.
func parseOrganizationRoleSlugs(c fiber.Ctx) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return "", "", xerror.InvalidSlugFormat().Detail("invalid organization slug")
	}

	roleSlug, err := slug.New(c.Params("roleSlug"))
	if err != nil {
		return "", "", xerror.InvalidSlugFormat().Detail("invalid role slug")
	}

	return orgSlug, roleSlug, nil
}

// createRoleViaAPI creates a role through the backend API client.
func (r *Router) createRoleViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	body api.CreateRoleJSONRequestBody,
) (*api.OrgRole, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreateRoleWithResponse(
		ctx,
		organizationSlug,
		body,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusCreated && resp.JSON201 != nil {
		return resp.JSON201, http.StatusCreated, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"create role api returned status %d",
		resp.StatusCode(),
	)
}

// updateRoleViaAPI updates a role through the backend API client.
func (r *Router) updateRoleViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	roleSlug string,
	body api.UpdateRoleJSONRequestBody,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpdateRoleWithResponse(
		ctx,
		organizationSlug,
		roleSlug,
		body,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"update role api returned status %d",
		resp.StatusCode(),
	)
}

// archiveRoleViaAPI archives a role through the backend API client.
func (r *Router) archiveRoleViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	roleSlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeleteRoleWithResponse(
		ctx,
		organizationSlug,
		roleSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"archive role api returned status %d",
		resp.StatusCode(),
	)
}

// restoreRoleViaAPI restores an archived role through the backend API client.
func (r *Router) restoreRoleViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	roleSlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.RestoreRoleWithResponse(
		ctx,
		organizationSlug,
		roleSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"restore role api returned status %d",
		resp.StatusCode(),
	)
}
