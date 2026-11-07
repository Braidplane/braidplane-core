package permissions

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var ErrInvalidInput = xerror.ValidationFailed()

func NewRepository() Repository {
	return &repository{}
}

type repository struct{}

func toPermission(row dbgen.Permission) *Permission {
	return &Permission{
		Resource: row.Resource,
		Action:   row.Action,
	}
}

func toRolePermission(row dbgen.Permission) *Permission {
	return &Permission{
		Resource: row.Resource,
		Action:   row.Action,
	}
}

func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params *CreateParams,
) (*Permission, error) {
	_, err := dbgen.New(tx).CreatePermission(ctx, dbgen.CreatePermissionParams{
		Resource: params.Resource,
		Action:   params.Action,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &Permission{
		Resource: params.Resource,
		Action:   params.Action,
	}, nil
}

func (r *repository) FindOrCreate(
	ctx context.Context,
	tx postgres.DBTX,
	resources, actions []string,
) ([]*Permission, error) {
	if len(resources) != len(actions) {
		return nil, ErrInvalidInput.Detail("resources and actions must have the same length")
	}

	if len(resources) == 0 {
		return []*Permission{}, nil
	}

	rows, err := dbgen.New(
		tx,
	).FindOrCreatePermissions(
		ctx,
		dbgen.FindOrCreatePermissionsParams{
			Resources: resources,
			Actions:   actions,
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*Permission, len(rows))
	for i, row := range rows {
		result[i] = toPermission(row)
	}

	return result, nil
}

func (r *repository) ListAll(ctx context.Context, tx postgres.DBTX) ([]*Permission, error) {
	rows, err := dbgen.New(tx).ListAllPermissions(ctx)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*Permission, len(rows))
	for i, row := range rows {
		result[i] = toPermission(row)
	}

	return result, nil
}

func (r *repository) ListForRole(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, roleSlug slug.Slug,
) ([]*Permission, error) {
	rows, err := dbgen.New(tx).ListPermissionsForRole(ctx, dbgen.ListPermissionsForRoleParams{
		OrgSlug:  postgres.SlugToText(orgSlug),
		RoleSlug: postgres.SlugToText(roleSlug),
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*Permission, len(rows))
	for i, row := range rows {
		result[i] = toRolePermission(row)
	}

	return result, nil
}

func (r *repository) AddToRole(
	ctx context.Context,
	tx postgres.DBTX,
	params *AddToRoleParams,
) error {
	err := dbgen.New(tx).AddPermissionToRole(ctx, dbgen.AddPermissionToRoleParams{
		OrgSlug:  postgres.SlugToText(params.OrgSlug),
		RoleSlug: postgres.SlugToText(params.RoleSlug),
		Resource: pgtype.Text{String: params.Resource, Valid: params.Resource != ""},
		Action:   pgtype.Text{String: params.Action, Valid: params.Action != ""},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

func (r *repository) RemoveFromRole(
	ctx context.Context,
	tx postgres.DBTX,
	params *RemoveFromRoleParams,
) error {
	err := dbgen.New(tx).RemovePermissionFromRole(ctx, dbgen.RemovePermissionFromRoleParams{
		OrgSlug:  postgres.SlugToText(params.OrgSlug),
		RoleSlug: postgres.SlugToText(params.RoleSlug),
		Resource: pgtype.Text{String: params.Resource, Valid: params.Resource != ""},
		Action:   pgtype.Text{String: params.Action, Valid: params.Action != ""},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

func (r *repository) ClearRolePermissions(
	ctx context.Context,
	tx postgres.DBTX,
	params *ClearRolePermissionsParams,
) error {
	err := dbgen.New(tx).ClearRolePermissions(ctx, dbgen.ClearRolePermissionsParams{
		OrgSlug:  postgres.SlugToText(params.OrgSlug),
		RoleSlug: postgres.SlugToText(params.RoleSlug),
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}
