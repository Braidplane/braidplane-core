package members

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
	ErrNotFound      = xerror.NotFound(xerror.Member)
	ErrAlreadyMember = xerror.AlreadyExists(xerror.Member)
)

func NewRepository() Repository {
	return &repository{}
}

type repository struct{}

func toMember(row dbgen.FindOrganizationMemberRow) *Member {
	return memberFromFields(row.Slug, row.Email, row.RoleSlug, row.CreatedAt, row.UpdatedAt)
}

func toMemberFromEmail(row dbgen.FindOrganizationMemberByEmailRow) *Member {
	return memberFromFields(row.Slug, row.Email, row.RoleSlug, row.CreatedAt, row.UpdatedAt)
}

func memberFromFields(
	userSlug slug.Slug,
	email string,
	roleSlug slug.Slug,
	createdAt pgtype.Timestamp,
	updatedAt pgtype.Timestamp,
) *Member {
	return &Member{
		UserSlug:  userSlug,
		UserEmail: email,
		RoleSlug:  roleSlug,
		CreatedAt: createdAt.Time,
		UpdatedAt: updatedAt.Time,
	}
}

func toMemberFromList(row dbgen.ListOrganizationMembersRow) *Member {
	return &Member{
		UserSlug:  row.Slug,
		UserEmail: row.Email,
		RoleSlug:  row.RoleSlug,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}
}

func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params *CreateParams,
) (*Member, error) {
	row, err := dbgen.New(
		tx,
	).CreateOrganizationMember(
		ctx,
		dbgen.CreateOrganizationMemberParams{
			OrgSlug:  postgres.SlugToText(params.OrganizationSlug),
			UserSlug: postgres.SlugToText(params.UserSlug),
			RoleSlug: postgres.SlugToText(params.RoleSlug),
		},
	)
	if err != nil {
		if postgres.IsUniqueViolation(err, "uq_organization_member") {
			return nil, ErrAlreadyMember.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return &Member{
		UserSlug:  params.UserSlug,
		UserEmail: row.Email,
		RoleSlug:  params.RoleSlug,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

func (r *repository) Find(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, userSlug slug.Slug,
) (*Member, error) {
	row, err := dbgen.New(tx).FindOrganizationMember(ctx, dbgen.FindOrganizationMemberParams{
		OrgSlug:  orgSlug,
		UserSlug: userSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toMember(row), nil
}

// FindByEmail returns a member by organization slug and normalized user email.
func (r *repository) FindByEmail(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	email string,
) (*Member, error) {
	row, err := dbgen.New(tx).FindOrganizationMemberByEmail(ctx, dbgen.FindOrganizationMemberByEmailParams{
		OrgSlug: orgSlug,
		Email:   email,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toMemberFromEmail(row), nil
}

func (r *repository) CountByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
) (int64, error) {
	count, err := dbgen.New(tx).CountOrganizationMembers(ctx, orgSlug)
	if err != nil {
		return 0, xerror.Unexpected().With(err)
	}

	return count, nil
}

func (r *repository) CountByRole(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	roleSlug slug.Slug,
) (int64, error) {
	count, err := dbgen.New(tx).CountOrganizationMembersByRole(
		ctx,
		dbgen.CountOrganizationMembersByRoleParams{
			OrgSlug:  orgSlug,
			RoleSlug: roleSlug,
		},
	)
	if err != nil {
		return 0, xerror.Unexpected().With(err)
	}

	return count, nil
}

func (r *repository) ListByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*Member, error) {
	cursorSlug, cursorTimestamp, cursorText, err := postgres.BuildCursorValues(
		req.Cursor,
		req.SortBy,
	)
	if err != nil {
		return nil, xerror.Wrapf("build member cursor values: %w", err)
	}

	rows, err := dbgen.New(
		tx,
	).ListOrganizationMembers(
		ctx,
		dbgen.ListOrganizationMembersParams{
			OrgSlug:         orgSlug,
			CursorSlug:      cursorSlug,
			SortBy:          pgtype.Text{String: req.SortBy, Valid: req.SortBy != ""},
			OrderDir:        pgtype.Text{String: req.Order, Valid: req.Order != ""},
			CursorTimestamp: cursorTimestamp,
			CursorText:      cursorText,
			LimitCount:      int64(req.Limit),
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*Member, 0, len(rows))
	for _, row := range rows {
		result = append(result, toMemberFromList(row))
	}

	return result, nil
}

func (r *repository) Update(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, userSlug, roleSlug slug.Slug,
) error {
	err := dbgen.New(tx).UpdateOrganizationMember(ctx, dbgen.UpdateOrganizationMemberParams{
		OrgSlug:  pgtype.Text{String: string(orgSlug), Valid: true},
		UserSlug: pgtype.Text{String: string(userSlug), Valid: true},
		RoleSlug: pgtype.Text{String: string(roleSlug), Valid: true},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

func (r *repository) Remove(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, userSlug slug.Slug,
) error {
	err := dbgen.New(tx).RemoveOrganizationMember(ctx, dbgen.RemoveOrganizationMemberParams{
		OrgSlug:  pgtype.Text{String: string(orgSlug), Valid: true},
		UserSlug: pgtype.Text{String: string(userSlug), Valid: true},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}
