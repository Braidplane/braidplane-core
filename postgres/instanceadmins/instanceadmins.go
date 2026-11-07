package instanceadmins

import (
	"context"
	"database/sql"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type repository struct{}

func NewRepository() Repository { return &repository{} }

func (*repository) LockRoleChanges(ctx context.Context, tx postgres.DBTX) error {
	if err := dbgen.New(tx).LockInstanceAdminRoleChanges(ctx); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (*repository) FindUserForUpdate(ctx context.Context, tx postgres.DBTX, userSlug slug.Slug) (*User, error) {
	user, err := dbgen.New(tx).FindInstanceAdminUserForUpdate(ctx, userSlug)
	if xerror.Is(err, sql.ErrNoRows) {
		return nil, xerror.NotFound(xerror.User).With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	role, err := model.RoleString(user.Role)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return &User{Slug: user.Slug, Role: role, VerifiedAt: user.VerifiedAt.Time}, nil
}

func (*repository) CountSuperAdmins(ctx context.Context, tx postgres.DBTX) (int64, error) {
	count, err := dbgen.New(tx).CountSuperAdmins(ctx)
	if err != nil {
		return 0, xerror.Unexpected().With(err)
	}
	return count, nil
}

func (*repository) SetUserRole(ctx context.Context, tx postgres.DBTX, userSlug slug.Slug, role model.Role) error {
	rows, err := dbgen.New(tx).SetInstanceAdminUserRole(ctx, dbgen.SetInstanceAdminUserRoleParams{Slug: userSlug, Role: role.String()})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows != 1 {
		return xerror.NotFound(xerror.User)
	}
	return nil
}

func (*repository) CreateAudit(ctx context.Context, tx postgres.DBTX, userSlug slug.Slug, previousRole, newRole model.Role) error {
	if err := dbgen.New(tx).CreateInstanceAdminAudit(ctx, dbgen.CreateInstanceAdminAuditParams{TargetUserSlug: userSlug.String(), PreviousRole: previousRole.String(), NewRole: newRole.String()}); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}
