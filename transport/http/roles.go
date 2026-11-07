package http

import (
	"fmt"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (s *server) ListRoles(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListRolesParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceRoles); err != nil {
		return err
	}

	listReq := toRolesListRequest(params)
	includeDeleted := params.IncludeDeleted != nil && *params.IncludeDeleted
	onlyDeleted := params.OnlyDeleted != nil && *params.OnlyDeleted
	if onlyDeleted {
		includeDeleted = true
	}
	if includeDeleted {
		if err := s.canOrg(c, orgSlug, model.Restore, model.ResourceRoles); err != nil {
			return err
		}
	}

	roles, err := s.rolesService.ListRoles(c.Context(), orgSlug, listReq, includeDeleted, onlyDeleted)
	if err != nil {
		return mapServiceError(err)
	}

	apiRoles := make([]api.OrgRole, 0, len(roles.Items))
	for _, role := range roles.Items {
		perms, err := s.rolesService.ListRolePermissions(c.Context(), orgSlug, role.Slug)
		if err != nil {
			return mapServiceError(err)
		}
		apiRoles = append(apiRoles, roleToHTTP(role, perms))
	}

	return c.JSON(api.RoleListResponse{
		Roles:      apiRoles,
		HasMore:    roles.HasMore,
		NextCursor: roles.NextCursor,
		PrevCursor: roles.PrevCursor,
	})
}

func (s *server) CreateRole(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Create, model.ResourceRoles); err != nil {
		return err
	}

	var req api.NewOrgRole
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	var roleSlug slug.Slug
	if req.Slug != nil {
		parsed, err := slug.New(*req.Slug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid role slug: %v", err))
		}
		roleSlug = parsed
	}

	perms, err := permissionsFromAPI(req.Permissions)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	role, err := s.rolesService.CreateRoleWithPermissions(
		c.Context(),
		orgSlug,
		s.eventActor(c),
		model.NewRole{
			Name: req.Name,
			Slug: roleSlug,
		},
		perms,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusCreated).JSON(roleToHTTP(*role, perms))
}

func (s *server) GetRole(
	c fiber.Ctx,
	slugParam api.SlugParam,
	roleSlugParam api.RoleSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	roleSlug, err := slug.New(roleSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid role slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceRoles); err != nil {
		return err
	}

	role, err := s.rolesService.GetRole(c.Context(), orgSlug, roleSlug)
	if err != nil {
		return mapServiceError(err)
	}

	perms, err := s.rolesService.ListRolePermissions(c.Context(), orgSlug, roleSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(roleToHTTP(*role, perms))
}

func (s *server) UpdateRole(
	c fiber.Ctx,
	slugParam api.SlugParam,
	roleSlugParam api.RoleSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	roleSlug, err := slug.New(roleSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid role slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceRoles); err != nil {
		return err
	}

	var req api.UpdateOrgRole
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	perms, err := permissionsFromAPI(req.Permissions)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	err = s.rolesService.UpdateRoleWithPermissions(
		c.Context(),
		orgSlug,
		roleSlug,
		s.eventActor(c),
		model.UpdateRole{Name: req.Name},
		perms,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) DeleteRole(
	c fiber.Ctx,
	slugParam api.SlugParam,
	roleSlugParam api.RoleSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	roleSlug, err := slug.New(roleSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid role slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceRoles); err != nil {
		return err
	}

	err = s.rolesService.DeleteRole(c.Context(), orgSlug, roleSlug, s.eventActor(c))
	if err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) RestoreRole(
	c fiber.Ctx,
	slugParam api.SlugParam,
	roleSlugParam api.RoleSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	roleSlug, err := slug.New(roleSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid role slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Restore, model.ResourceRoles); err != nil {
		return err
	}

	err = s.rolesService.RestoreRole(c.Context(), orgSlug, roleSlug, s.eventActor(c))
	if err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func permissionsFromAPI(perms []api.Permission) ([]model.RolePermission, error) {
	converted := make([]model.RolePermission, 0, len(perms))
	for _, perm := range perms {
		convertedPerm := model.RolePermission{
			Resource: perm.Resource,
			Action:   string(perm.Action),
		}

		converted = append(converted, convertedPerm)
	}

	return converted, nil
}

func permissionToHTTP(permission model.RolePermission) api.Permission {
	return api.Permission{
		Resource: permission.Resource,
		Action:   api.PermissionAction(permission.Action),
	}
}

func roleToHTTP(role model.OrgRole, perms []model.RolePermission) api.OrgRole {
	apiPermissions := make([]api.Permission, 0, len(perms))
	for _, perm := range perms {
		apiPermissions = append(apiPermissions, permissionToHTTP(perm))
	}

	return api.OrgRole{
		Slug:        role.Slug.String(),
		Name:        role.Name,
		IsDefault:   role.IsDefault,
		Permissions: apiPermissions,
		DeletedAt:   role.DeletedAt,
		CreatedAt:   role.CreatedAt,
		UpdatedAt:   role.UpdatedAt,
	}
}
