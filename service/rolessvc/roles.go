package rolessvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	"github.com/Braidplane/braidplane-core/postgres/roles"
)

func (s *service) createRole(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	newRole model.NewRole,
) (*model.OrgRole, error) {
	if err := validate.Default().Struct(newRole); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	roleSlug, err := newRole.Slug.ValidateOrGenerate(newRole.Name)
	if err != nil {
		return nil, ErrInvalidRoleSlug.With(err)
	}

	// Pre-check if slug exists within this organization
	if existingRole, err := s.rolesRepo.FindBySlug(
		ctx,
		tx,
		orgSlug,
		roleSlug,
	); err == nil &&
		existingRole != nil {
		// Slug exists, append suffix once
		nextSlug, genErr := slug.WithRandomSuffix(roleSlug, 6)
		if genErr != nil {
			return nil, xerror.Unexpected().With(genErr)
		}
		roleSlug = nextSlug
	} else if err != nil && !xerror.Is(err, roles.ErrNotFound) {
		return nil, xerror.Wrapf("check role slug collision: %w", err)
	}

	role, err := s.rolesRepo.Create(ctx, tx, &roles.CreateParams{
		OrgSlug:   orgSlug,
		Slug:      roleSlug,
		Name:      newRole.Name,
		IsDefault: newRole.IsDefault,
	})
	if err != nil {
		return nil, xerror.Wrapf("create role: %w", err)
	}

	return toRole(role), nil
}

// CreateRoleWithPermissions creates a new role and assigns permissions in a single transaction.
func (s *service) CreateRoleWithPermissions(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	newRole model.NewRole,
	permissions []model.RolePermission,
) (*model.OrgRole, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.OrgRole, error) {
		role, err := s.createRole(ctx, tx, orgSlug, newRole)
		if err != nil {
			return nil, xerror.Wrapf("create role in tx: %w", err)
		}
		if err := s.replaceRolePermissions(
			ctx,
			tx,
			orgSlug,
			role.Slug,
			permissions,
		); err != nil {
			return nil, xerror.Wrapf("replace role permissions: %w", err)
		}
		if err := s.enqueueRequiredRoleAuditEvent(ctx, tx, orgSlug, actor, roleCreatedEvent{
			role:            *role,
			permissionCount: len(permissions),
		}); err != nil {
			return nil, err
		}

		return role, nil
	})
}

// CreateDefaultRoles seeds the default roles (admin, viewer) with their permissions
// for a newly created organization.
func (s *service) CreateDefaultRoles(
	ctx context.Context,
	orgSlug slug.Slug,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		defaultRoles := []struct {
			name        string
			slug        slug.Slug
			isDefault   bool
			permissions []struct {
				action   model.Action
				resource model.Resource
			}
		}{
			{
				name:      "Admin",
				slug:      "admin",
				isDefault: true,
				permissions: []struct {
					action   model.Action
					resource model.Resource
				}{
					// Wildcard permissions handled as special case - admin gets everything.
				},
			},
			{
				name:      "Viewer",
				slug:      "viewer",
				isDefault: true,
				permissions: []struct {
					action   model.Action
					resource model.Resource
				}{
					{model.ActionRead, model.ResourceMembers},
					{model.ActionRead, model.ResourceOrganizations},
					{model.ActionRead, model.ResourceProfile},
					{model.ActionRead, model.ResourceRoles},
					{model.ActionRead, model.ResourceProjects},
					{model.ActionRead, model.ResourceEnvironments},
					{model.ActionRead, model.ResourceDataSources},
					{model.ActionRead, model.ResourcePipelines},
					{model.ActionRead, model.ResourceEvents},
					{model.ActionRead, model.ResourceNotifications},
					{model.ActionRead, model.ResourceAlertPolicies},
					{model.ActionRead, model.ResourceAlertDestinations},
				},
			},
		}

		for _, roleSpec := range defaultRoles {
			if err := s.ensureDefaultRole(ctx, tx, orgSlug, roleSpec.name, roleSpec.slug, roleSpec.isDefault); err != nil {
				return xerror.Wrapf("ensure default role %s: %w", roleSpec.slug, err)
			}

			if roleSpec.slug == "admin" {
				err := s.addPermissionToRole(
					ctx, tx,
					orgSlug, roleSpec.slug,
					"*", "*",
				)
				if err != nil {
					return xerror.Wrapf("add wildcard permission to role %s: %w", roleSpec.slug, err)
				}

				continue
			}

			for _, perm := range roleSpec.permissions {
				err := s.addPermissionToRole(
					ctx, tx,
					orgSlug, roleSpec.slug,
					perm.resource.String(), perm.action.String(),
				)
				if err != nil {
					return xerror.Wrapf(
						"add permission %s:%s to role %s: %w",
						perm.resource,
						perm.action,
						roleSpec.slug,
						err,
					)
				}
			}
		}

		return nil
	})
}

// ensureDefaultRole creates a default role when missing and leaves existing default slugs in place.
func (s *service) ensureDefaultRole(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	name string,
	roleSlug slug.Slug,
	isDefault bool,
) error {
	existing, err := s.rolesRepo.FindBySlug(ctx, tx, orgSlug, roleSlug)
	if err == nil && existing != nil {
		if !existing.IsDefault {
			return ErrInvalidInput.Detailf("default role slug %q is already used by a custom role", roleSlug.String())
		}
		return nil
	}
	if err != nil && !xerror.Is(err, roles.ErrNotFound) {
		return xerror.Wrapf("find default role %s: %w", roleSlug, err)
	}
	defaultRole, err := s.rolesRepo.EnsureDefault(ctx, tx, &roles.CreateParams{
		OrgSlug:   orgSlug,
		Name:      name,
		Slug:      roleSlug,
		IsDefault: isDefault,
	})
	if err != nil {
		return xerror.Wrapf("ensure default role %s: %w", roleSlug, err)
	}
	if defaultRole == nil || !defaultRole.IsDefault {
		return ErrInvalidInput.Detailf("default role slug %q is already used by a custom role", roleSlug.String())
	}
	return nil
}

// UpdateRoleWithPermissions updates a role's name and fully replaces its permissions.
func (s *service) UpdateRoleWithPermissions(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
	actor model.EventActor,
	update model.UpdateRole,
	permissions []model.RolePermission,
) error {
	if err := validate.Default().Struct(update); err != nil {
		return xerror.ValidationFailed().With(err)
	}

	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		role, err := s.rolesRepo.FindBySlug(ctx, tx, orgSlug, roleSlug)
		if err != nil {
			return xerror.Wrapf("get role for update check: %w", err)
		}
		if role.IsDefault {
			return ErrCannotUpdateDefaultRole
		}

		err = s.rolesRepo.Update(ctx, tx, &roles.UpdateParams{
			OrgSlug: orgSlug,
			Slug:    roleSlug,
			Name:    update.Name,
		})
		if err != nil {
			return xerror.Wrapf("update role: %w", err)
		}

		if err := s.replaceRolePermissions(
			ctx,
			tx,
			orgSlug,
			roleSlug,
			permissions,
		); err != nil {
			return xerror.Wrapf("replace role permissions: %w", err)
		}

		if err := s.enqueueRequiredRoleAuditEvent(ctx, tx, orgSlug, actor, roleUpdatedEvent{
			roleSlug:        roleSlug,
			permissionCount: len(permissions),
		}); err != nil {
			return err
		}
		return nil
	})
}

// DeleteRole archives a custom role from an organization.
func (s *service) DeleteRole(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		role, err := s.rolesRepo.FindBySlug(ctx, tx, orgSlug, roleSlug)
		if err != nil {
			return xerror.Wrapf("get role for delete check: %w", err)
		}
		if role.IsDefault {
			return ErrCannotDeleteDefaultRole
		}

		err = s.rolesRepo.Delete(ctx, tx, &roles.DeleteParams{
			OrgSlug: orgSlug,
			Slug:    roleSlug,
		})
		if err != nil {
			return xerror.Wrapf("delete role: %w", err)
		}
		if err := s.enqueueRequiredRoleAuditEvent(ctx, tx, orgSlug, actor, roleDeletedEvent{
			roleSlug: roleSlug,
		}); err != nil {
			return err
		}
		return nil
	})
}

// RestoreRole clears the archival tombstone for a custom role.
func (s *service) RestoreRole(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.rolesRepo.Restore(ctx, tx, &roles.RestoreParams{
			OrgSlug: orgSlug,
			Slug:    roleSlug,
		}); err != nil {
			return xerror.Wrapf("restore role: %w", err)
		}
		if err := s.enqueueRequiredRoleAuditEvent(ctx, tx, orgSlug, actor, roleRestoredEvent{
			roleSlug: roleSlug,
		}); err != nil {
			return err
		}
		return nil
	})
}

// GetRole retrieves a role by slug.
func (s *service) GetRole(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
) (*model.OrgRole, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.OrgRole, error) {
		role, err := s.getRole(ctx, tx, orgSlug, roleSlug)
		if err != nil {
			return nil, xerror.Wrapf("get role in tx: %w", err)
		}
		return role, nil
	})
}

func (s *service) getRole(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, roleSlug slug.Slug,
) (*model.OrgRole, error) {
	role, err := s.rolesRepo.FindBySlug(ctx, tx, orgSlug, roleSlug)
	if err != nil {
		return nil, xerror.Wrapf("get role: %w", err)
	}
	return toRole(role), nil
}

// ListRoles lists all roles in an organization.
func (s *service) ListRoles(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
	includeDeleted bool,
	onlyDeleted bool,
) (model.ListResponse[model.OrgRole], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var ret []model.OrgRole

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		roleList, err := s.rolesRepo.ListByOrganization(ctx, tx, orgSlug, queryReq, includeDeleted, onlyDeleted)
		if err != nil {
			return xerror.Wrapf("list roles: %w", err)
		}
		ret = toRoleList(roleList)
		return nil
	})
	if err != nil {
		return model.ListResponse[model.OrgRole]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item model.OrgRole, sortBy string) (string, string) {
			cursorValue := item.CreatedAt.Format(time.RFC3339Nano)
			switch sortBy {
			case model.ListSortUpdatedAt:
				cursorValue = item.UpdatedAt.Format(time.RFC3339Nano)
			case model.ListSortName:
				cursorValue = item.Name
			}
			return cursorValue, item.Slug.String()
		},
	)

	return model.ListResponse[model.OrgRole]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) replaceRolePermissions(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, roleSlug slug.Slug,
	permissionsList []model.RolePermission,
) error {
	err := s.permissionsRepo.ClearRolePermissions(ctx, tx, &permissions.ClearRolePermissionsParams{
		OrgSlug:  orgSlug,
		RoleSlug: roleSlug,
	})
	if err != nil {
		return xerror.Wrapf("clear role permissions: %w", err)
	}

	for _, perm := range permissionsList {
		if err := s.addPermissionToRole(
			ctx,
			tx,
			orgSlug,
			roleSlug,
			perm.Resource,
			perm.Action,
		); err != nil {
			return xerror.Wrapf("add role permission: %w", err)
		}
	}

	return nil
}

// GetUserPermissionsInOrg gets all permissions for a user in an organization (via their role).
func (s *service) GetUserPermissionsInOrg(
	ctx context.Context,
	orgSlug, userSlug slug.Slug,
) ([]model.RolePermission, error) {
	var ret []model.RolePermission

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		perms, err := s.rolesRepo.GetUserPermissionsInOrg(ctx, tx, orgSlug, userSlug)
		if err != nil {
			return xerror.Wrapf("get user permissions in org: %w", err)
		}

		// Convert to service model
		ret = make([]model.RolePermission, len(perms))
		for i, perm := range perms {
			ret[i] = model.RolePermission{
				Resource: perm.Resource,
				Action:   perm.Action,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}
