package organizations

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
	ErrNotFound          = xerror.NotFound(xerror.Organization)
	ErrSlugAlreadyExists = xerror.AlreadyExists(xerror.Organization)
)

func NewRepository() Repository {
	return &repository{}
}

type repository struct{}

func (r *repository) DeletionBlocked(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) (bool, error) {
	blocked, err := dbgen.New(tx).OrganizationDeletionBlocked(ctx, orgSlug)
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return blocked.Bool, nil
}

func (r *repository) Delete(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) error {
	count, err := dbgen.New(tx).DeleteOrganization(ctx, pgtype.Text{String: orgSlug.String(), Valid: true})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func toOrganization(row dbgen.Organization) (*Organization, error) {
	return &Organization{
		Name:      row.Name,
		Slug:      row.Slug,
		Settings:  row.Settings,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

func toOrganizationWithRole(row dbgen.ListUserOrganizationsRow) (*OrganizationWithRole, error) {
	return &OrganizationWithRole{
		Organization: Organization{
			Name:      row.Name,
			Slug:      row.Slug,
			Settings:  row.Settings,
			CreatedAt: row.OrgCreatedAt.Time,
			UpdatedAt: row.OrgUpdatedAt.Time,
		},
		MemberRole: row.RoleSlug.String(),
	}, nil
}

func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params *CreateParams,
) (*Organization, error) {
	row, err := dbgen.New(tx).CreateOrganization(ctx, dbgen.CreateOrganizationParams{
		Name: params.Name,
		Slug: params.Slug,
	})
	if err != nil {
		if postgres.IsUniqueViolation(err, "organizations_slug_key") {
			return nil, ErrSlugAlreadyExists.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return &Organization{
		Name:      row.Name,
		Slug:      row.Slug,
		Settings:  []byte("{}"),
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	s slug.Slug,
) (*Organization, error) {
	row, err := dbgen.New(tx).FindOrganizationBySlug(ctx, s)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toOrganization(row)
}

func (r *repository) FindBySlugForUpdate(
	ctx context.Context,
	tx postgres.DBTX,
	s slug.Slug,
) (*Organization, error) {
	row, err := dbgen.New(tx).FindOrganizationBySlugForUpdate(ctx, s)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return &Organization{
		Name:      row.Name,
		Slug:      row.Slug,
		Settings:  row.Settings,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

func (r *repository) Update(ctx context.Context, tx postgres.DBTX, params *UpdateParams) error {
	err := dbgen.New(tx).UpdateOrganization(ctx, dbgen.UpdateOrganizationParams{
		Slug: params.Slug,
		Name: params.Name,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

func (r *repository) UpdateSettings(
	ctx context.Context,
	tx postgres.DBTX,
	params *UpdateSettingsParams,
) error {
	err := dbgen.New(
		tx,
	).UpdateOrganizationSettings(
		ctx,
		dbgen.UpdateOrganizationSettingsParams{
			Slug:     params.Slug,
			Settings: params.Settings,
		},
	)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

func (r *repository) ListByUser(
	ctx context.Context,
	tx postgres.DBTX,
	userSlug slug.Slug,
	req model.ListRequest,
) ([]*OrganizationWithRole, error) {
	cursorSlug, cursorTimestamp, cursorText, err := postgres.BuildCursorValues(
		req.Cursor,
		req.SortBy,
	)
	if err != nil {
		return nil, xerror.Wrapf("build organization cursor values: %w", err)
	}

	rows, err := dbgen.New(tx).ListUserOrganizations(ctx, dbgen.ListUserOrganizationsParams{
		Slug:            userSlug,
		CursorSlug:      cursorSlug,
		SortBy:          pgtype.Text{String: req.SortBy, Valid: req.SortBy != ""},
		OrderDir:        pgtype.Text{String: req.Order, Valid: req.Order != ""},
		CursorTimestamp: cursorTimestamp,
		CursorText:      cursorText,
		LimitCount:      int64(req.Limit),
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*OrganizationWithRole, 0, len(rows))
	for _, row := range rows {
		organization, err := toOrganizationWithRole(row)
		if err != nil {
			return nil, err
		}
		result = append(result, organization)
	}

	return result, nil
}
