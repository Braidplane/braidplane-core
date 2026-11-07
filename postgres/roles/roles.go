package roles

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var (
	ErrNotFound               = xerror.NotFound(xerror.Role)
	ErrSlugAlreadyExists      = xerror.AlreadyExists(xerror.Role)
	ErrCannotDeleteSystemRole = xerror.CannotDeleteDefault(xerror.Role)
	ErrUserNotMember          = xerror.NotFound(xerror.UserMember)
)

func NewRepository() Repository {
	return &repository{}
}

type repository struct{}

func toRole(row dbgen.Role) *Role {
	return &Role{
		Slug:      row.Slug,
		Name:      row.Name,
		IsDefault: row.IsDefault,
		DeletedAt: postgres.FromNullableTimestamptz(row.DeletedAt),
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}
}

func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params *CreateParams,
) (*Role, error) {
	row, err := dbgen.New(tx).CreateRole(ctx, dbgen.CreateRoleParams{
		OrgSlug:   postgres.SlugToText(params.OrgSlug),
		RoleSlug:  postgres.SlugToText(params.Slug),
		Name:      pgtype.Text{String: params.Name, Valid: params.Name != ""},
		IsDefault: pgtype.Bool{Bool: params.IsDefault, Valid: true},
	})
	if err != nil {
		if postgres.IsUniqueViolation(err, "uq_organization_role_slug") {
			return nil, ErrSlugAlreadyExists.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return &Role{
		Slug:      params.Slug,
		Name:      params.Name,
		IsDefault: params.IsDefault,
		DeletedAt: postgres.FromNullableTimestamptz(row.DeletedAt),
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

func (r *repository) EnsureDefault(
	ctx context.Context,
	tx postgres.DBTX,
	params *CreateParams,
) (*Role, error) {
	row, err := dbgen.New(tx).EnsureDefaultRole(ctx, dbgen.EnsureDefaultRoleParams{
		OrgSlug:  postgres.SlugToText(params.OrgSlug),
		RoleSlug: postgres.SlugToText(params.Slug),
		Name:     pgtype.Text{String: params.Name, Valid: params.Name != ""},
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return &Role{
		Slug:      row.Slug,
		Name:      row.Name,
		IsDefault: row.IsDefault,
		DeletedAt: postgres.FromNullableTimestamptz(row.DeletedAt),
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, roleSlug slug.Slug,
) (*Role, error) {
	row, err := dbgen.New(tx).FindRoleBySlug(ctx, dbgen.FindRoleBySlugParams{
		OrgSlug:  orgSlug,
		RoleSlug: roleSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toRole(row), nil
}

func (r *repository) ListByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
	includeDeleted bool,
	onlyDeleted bool,
) ([]*Role, error) {
	cursorSlug, cursorTimestamp, cursorText, err := postgres.BuildCursorValues(
		req.Cursor,
		req.SortBy,
	)
	if err != nil {
		return nil, xerror.Wrapf("build role cursor values: %w", err)
	}

	rows, err := dbgen.New(
		tx,
	).ListRolesByOrganization(
		ctx,
		dbgen.ListRolesByOrganizationParams{
			OrgSlug:         postgres.SlugToText(orgSlug),
			CursorSlug:      cursorSlug,
			SortBy:          pgtype.Text{String: req.SortBy, Valid: req.SortBy != ""},
			OrderDir:        pgtype.Text{String: req.Order, Valid: req.Order != ""},
			CursorTimestamp: cursorTimestamp,
			CursorText:      cursorText,
			LimitCount:      pgtype.Int8{Int64: int64(req.Limit), Valid: req.Limit > 0},
			IncludeDeleted:  pgtype.Bool{Bool: includeDeleted, Valid: true},
			OnlyDeleted:     pgtype.Bool{Bool: onlyDeleted, Valid: true},
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*Role, len(rows))
	for i, row := range rows {
		result[i] = toRole(row)
	}

	return result, nil
}

func (r *repository) GetUserPermissionsInOrg(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, userSlug slug.Slug,
) ([]*UserPermission, error) {
	rows, err := dbgen.New(
		tx,
	).GetUserPermissionsInOrganization(
		ctx,
		dbgen.GetUserPermissionsInOrganizationParams{
			OrgSlug:  orgSlug,
			UserSlug: userSlug,
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if len(rows) == 0 {
		return nil, ErrUserNotMember
	}

	result := make([]*UserPermission, 0, len(rows))
	for _, row := range rows {
		if row.Resource == "" && row.Action == "" {
			continue
		}
		result = append(result, &UserPermission{
			Resource: row.Resource,
			Action:   row.Action,
		})
	}

	return result, nil
}

func (r *repository) Update(ctx context.Context, tx postgres.DBTX, params *UpdateParams) error {
	result, err := dbgen.New(tx).UpdateRole(ctx, dbgen.UpdateRoleParams{
		OrgSlug:  postgres.SlugToText(params.OrgSlug),
		RoleSlug: postgres.SlugToText(params.Slug),
		Name:     pgtype.Text{String: params.Name, Valid: params.Name != ""},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

func (r *repository) Delete(ctx context.Context, tx postgres.DBTX, params *DeleteParams) error {
	result, err := dbgen.New(tx).DeleteRole(ctx, dbgen.DeleteRoleParams{
		OrgSlug:      postgres.SlugToText(params.OrgSlug),
		RoleSlug:     postgres.SlugToText(params.Slug),
		DeleteReason: pgtype.Text{Valid: false},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

func (r *repository) Restore(ctx context.Context, tx postgres.DBTX, params *RestoreParams) error {
	result, err := dbgen.New(tx).RestoreRole(ctx, dbgen.RestoreRoleParams{
		OrgSlug:  postgres.SlugToText(params.OrgSlug),
		RoleSlug: postgres.SlugToText(params.Slug),
	})
	if err != nil {
		if postgres.IsUniqueViolation(err, "uq_organization_role_slug") {
			return ErrSlugAlreadyExists.With(err)
		}
		return xerror.Unexpected().With(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}
