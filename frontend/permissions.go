package frontend

import (
	"context"
	"net/http"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (r *Router) resolveOrganizationPermissions(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
) views.OrganizationPermissions {
	organizationRole, err := r.findOrganizationRoleForCurrentUser(
		ctx,
		cookieHeader,
		organizationSlug,
	)
	if err != nil || organizationRole == "" {
		return views.DefaultOrganizationPermissions()
	}

	role, _, _, getRoleErr := r.getRoleViaAPI(
		ctx,
		cookieHeader,
		organizationSlug,
		organizationRole,
	)
	if getRoleErr != nil || role == nil {
		return views.DefaultOrganizationPermissions()
	}

	return organizationPermissionsFromRole(role.Permissions)
}

func (r *Router) findOrganizationRoleForCurrentUser(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
) (string, error) {
	params := &api.ListOrganizationsParams{}
	limit := api.LimitParam(100)
	params.Limit = &limit

	organizationList, _, _, err := r.listOrganizationsViaAPI(ctx, cookieHeader, params)
	if err != nil || organizationList == nil {
		return "", err
	}

	for _, organization := range organizationList.Organizations {
		if organization.Slug == organizationSlug {
			return organization.MemberRole, nil
		}
	}

	return "", nil
}

func (r *Router) getRoleViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	roleSlug string,
) (*api.OrgRole, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetRoleWithResponse(
		ctx,
		organizationSlug,
		roleSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"get role api returned status %d",
		resp.StatusCode(),
	)
}

func organizationPermissionsFromRole(permissions []api.Permission) views.OrganizationPermissions {
	values := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		resource := strings.TrimSpace(permission.Resource)
		action := strings.TrimSpace(string(permission.Action))
		if resource == "" || action == "" {
			continue
		}
		values = append(values, resource+":"+action)
	}

	return views.NewOrganizationPermissions(values...)
}
