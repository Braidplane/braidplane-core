package rolessvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
)

func (s *service) addPermissionToRole(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, roleSlug slug.Slug,
	resource, action string,
) error {
	if resource == "" || action == "" {
		return ErrInvalidInput.Detail("resource and action are required")
	}

	perms, err := s.permissionsRepo.FindOrCreate(ctx, tx, []string{resource}, []string{action})
	if err != nil {
		return xerror.Wrapf("find or create permission: %w", err)
	}

	if len(perms) == 0 {
		return ErrPermissionNotFound.Detail("failed to create permission")
	}

	err = s.permissionsRepo.AddToRole(ctx, tx, &permissions.AddToRoleParams{
		OrgSlug:  orgSlug,
		RoleSlug: roleSlug,
		Resource: resource,
		Action:   action,
	})
	if err != nil {
		return xerror.Wrapf("add permission to role: %w", err)
	}

	return nil
}

// ListRolePermissions returns all permissions granted to a role.
func (s *service) ListRolePermissions(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
) ([]model.RolePermission, error) {
	var ret []model.RolePermission

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		perms, err := s.permissionsRepo.ListForRole(ctx, tx, orgSlug, roleSlug)
		if err != nil {
			return xerror.Wrapf("list role permissions: %w", err)
		}

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
