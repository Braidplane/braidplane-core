package fakes

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// RolesService is a test double for rolessvc.Service.
type RolesService struct {
	CreateRoleWithPermissionsFn func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, newRole model.NewRole, perms []model.RolePermission) (*model.OrgRole, error)
	CreateDefaultRolesFn        func(ctx context.Context, orgSlug slug.Slug) error
	UpdateRoleWithPermissionsFn func(ctx context.Context, orgSlug, roleSlug slug.Slug, actor model.EventActor, update model.UpdateRole, perms []model.RolePermission) error
	DeleteRoleFn                func(ctx context.Context, orgSlug, roleSlug slug.Slug, actor model.EventActor) error
	RestoreRoleFn               func(ctx context.Context, orgSlug, roleSlug slug.Slug, actor model.EventActor) error
	GetRoleFn                   func(ctx context.Context, orgSlug, roleSlug slug.Slug) (*model.OrgRole, error)
	ListRolesFn                 func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest, includeDeleted, onlyDeleted bool) (model.ListResponse[model.OrgRole], error)
	GetUserPermissionsInOrgFn   func(ctx context.Context, orgSlug, userSlug slug.Slug) ([]model.RolePermission, error)
	ListRolePermissionsFn       func(ctx context.Context, orgSlug, roleSlug slug.Slug) ([]model.RolePermission, error)
}

func (f *RolesService) CreateRoleWithPermissions(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	newRole model.NewRole,
	perms []model.RolePermission,
) (*model.OrgRole, error) {
	if f.CreateRoleWithPermissionsFn != nil {
		return f.CreateRoleWithPermissionsFn(ctx, orgSlug, actor, newRole, perms)
	}
	return &model.OrgRole{Slug: newRole.Slug, Name: newRole.Name}, nil
}

func (f *RolesService) CreateDefaultRoles(
	ctx context.Context,
	orgSlug slug.Slug,
) error {
	if f.CreateDefaultRolesFn != nil {
		return f.CreateDefaultRolesFn(ctx, orgSlug)
	}
	return nil
}

func (f *RolesService) UpdateRoleWithPermissions(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
	actor model.EventActor,
	update model.UpdateRole,
	perms []model.RolePermission,
) error {
	if f.UpdateRoleWithPermissionsFn != nil {
		return f.UpdateRoleWithPermissionsFn(ctx, orgSlug, roleSlug, actor, update, perms)
	}
	return nil
}

func (f *RolesService) DeleteRole(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteRoleFn != nil {
		return f.DeleteRoleFn(ctx, orgSlug, roleSlug, actor)
	}
	return nil
}

func (f *RolesService) RestoreRole(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.RestoreRoleFn != nil {
		return f.RestoreRoleFn(ctx, orgSlug, roleSlug, actor)
	}
	return nil
}

func (f *RolesService) GetRole(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
) (*model.OrgRole, error) {
	if f.GetRoleFn != nil {
		return f.GetRoleFn(ctx, orgSlug, roleSlug)
	}
	return &model.OrgRole{Slug: roleSlug, Name: string(roleSlug)}, nil
}

func (f *RolesService) ListRoles(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
	includeDeleted bool,
	onlyDeleted bool,
) (model.ListResponse[model.OrgRole], error) {
	if f.ListRolesFn != nil {
		return f.ListRolesFn(ctx, orgSlug, req, includeDeleted, onlyDeleted)
	}
	return model.ListResponse[model.OrgRole]{
		Items:      []model.OrgRole{},
		HasMore:    false,
		NextCursor: nil,
	}, nil
}

func (f *RolesService) GetUserPermissionsInOrg(
	ctx context.Context,
	orgSlug, userSlug slug.Slug,
) ([]model.RolePermission, error) {
	if f.GetUserPermissionsInOrgFn != nil {
		return f.GetUserPermissionsInOrgFn(ctx, orgSlug, userSlug)
	}
	return []model.RolePermission{}, nil
}

func (f *RolesService) ListRolePermissions(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
) ([]model.RolePermission, error) {
	if f.ListRolePermissionsFn != nil {
		return f.ListRolePermissionsFn(ctx, orgSlug, roleSlug)
	}
	return []model.RolePermission{}, nil
}
