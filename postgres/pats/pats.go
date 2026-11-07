package pats

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
	ErrNotFound          = xerror.NotFound(xerror.PersonalAccessToken)
	ErrSlugAlreadyExists = xerror.AlreadyExists(xerror.PersonalAccessToken)
)

func NewRepository() Repository {
	return &repository{}
}

type repository struct{}

func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateParams,
) (*PersonalAccessToken, error) {
	row, err := dbgen.New(tx).CreatePersonalAccessToken(ctx, dbgen.CreatePersonalAccessTokenParams{
		PatSlug:                       params.Slug,
		Name:                          params.Name,
		Description:                   postgres.ToNullableText(params.Description),
		TokenHash:                     params.TokenHash,
		TokenPrefix:                   params.TokenPrefix,
		ExpiresAt:                     postgres.ToNullableTimestamptz(params.ExpiresAt),
		CanManagePersonalAccessTokens: params.CanManagePersonalAccessTokens,
		UserSlug:                      params.UserSlug,
	})
	if err != nil {
		if postgres.IsUniqueViolation(err, "uq_personal_access_tokens_user_slug") {
			return nil, ErrSlugAlreadyExists.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	token := personalAccessTokenFromRow(row)
	token.UserSlug = params.UserSlug
	return token, nil
}

func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	userSlug, patSlug slug.Slug,
) (*PersonalAccessToken, error) {
	row, err := dbgen.New(tx).FindPersonalAccessTokenBySlug(ctx, dbgen.FindPersonalAccessTokenBySlugParams{
		UserSlug: userSlug,
		PatSlug:  patSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return personalAccessTokenFromFindRow(row), nil
}

func (r *repository) ListByUser(
	ctx context.Context,
	tx postgres.DBTX,
	userSlug slug.Slug,
	req model.ListRequest,
) ([]*PersonalAccessToken, error) {
	cursorSlug, cursorTimestamp, cursorText, err := postgres.BuildCursorValues(
		req.Cursor,
		req.SortBy,
	)
	if err != nil {
		return nil, xerror.Wrapf("build personal access token cursor values: %w", err)
	}

	rows, err := dbgen.New(tx).ListPersonalAccessTokensByUser(
		ctx,
		dbgen.ListPersonalAccessTokensByUserParams{
			UserSlug:        userSlug,
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

	result := make([]*PersonalAccessToken, 0, len(rows))
	for _, row := range rows {
		result = append(result, personalAccessTokenFromListRow(row))
	}

	return result, nil
}

// Update persists editable PAT metadata for one user-owned token.
func (r *repository) Update(ctx context.Context, tx postgres.DBTX, params UpdateParams) error {
	rowsAffected, err := dbgen.New(tx).UpdatePersonalAccessToken(ctx, dbgen.UpdatePersonalAccessTokenParams{
		Name:                             params.Name,
		DescriptionSet:                   params.DescriptionSet,
		Description:                      postgres.ToNullableText(params.Description),
		ExpiresAtSet:                     params.ExpiresAtSet,
		ExpiresAt:                        postgres.ToNullableTimestamptz(params.ExpiresAt),
		CanManagePersonalAccessTokensSet: params.CanManagePersonalAccessTokensSet,
		CanManagePersonalAccessTokens:    params.CanManagePersonalAccessTokens,
		UserSlug:                         params.UserSlug,
		PatSlug:                          params.Slug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}

	return nil
}

func (r *repository) Revoke(
	ctx context.Context,
	tx postgres.DBTX,
	userSlug, patSlug slug.Slug,
) error {
	rowsAffected, err := dbgen.New(tx).RevokePersonalAccessToken(ctx, dbgen.RevokePersonalAccessTokenParams{
		UserSlug: userSlug,
		PatSlug:  patSlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}

	return nil
}

// DeleteRevoked removes revoked personal access token metadata scoped to a user.
func (r *repository) DeleteRevoked(
	ctx context.Context,
	tx postgres.DBTX,
	userSlug, patSlug slug.Slug,
) error {
	rowsAffected, err := dbgen.New(tx).DeleteRevokedPersonalAccessToken(ctx, dbgen.DeleteRevokedPersonalAccessTokenParams{
		UserSlug: userSlug,
		PatSlug:  patSlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}

	return nil
}

func (r *repository) Rotate(
	ctx context.Context,
	tx postgres.DBTX,
	params RotateParams,
) (*PersonalAccessToken, error) {
	row, err := dbgen.New(tx).RotatePersonalAccessToken(ctx, dbgen.RotatePersonalAccessTokenParams{
		TokenHash:   params.TokenHash,
		TokenPrefix: params.TokenPrefix,
		UserSlug:    params.UserSlug,
		PatSlug:     params.Slug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return personalAccessTokenFromRotateRow(row), nil
}

func (r *repository) FindActivePrincipalByTokenHash(
	ctx context.Context,
	tx postgres.DBTX,
	tokenHash string,
) (*Principal, error) {
	row, err := dbgen.New(tx).FindActivePersonalAccessTokenByTokenHash(ctx, tokenHash)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return &Principal{
		TokenSlug:                     row.Slug,
		UserSlug:                      row.UserSlug,
		CanManagePersonalAccessTokens: row.CanManagePersonalAccessTokens,
	}, nil
}

func (r *repository) MarkUsed(
	ctx context.Context,
	tx postgres.DBTX,
	userSlug, patSlug slug.Slug,
) error {
	rowsAffected, err := dbgen.New(tx).MarkPersonalAccessTokenUsed(ctx, dbgen.MarkPersonalAccessTokenUsedParams{
		UserSlug: userSlug,
		PatSlug:  patSlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}

	return nil
}

func personalAccessTokenFromRow(row dbgen.PersonalAccessToken) *PersonalAccessToken {
	return &PersonalAccessToken{
		ID:                            row.ID,
		Slug:                          row.Slug,
		Name:                          row.Name,
		Description:                   postgres.FromNullableText(row.Description),
		TokenPrefix:                   row.TokenPrefix,
		CanManagePersonalAccessTokens: row.CanManagePersonalAccessTokens,
		ExpiresAt:                     postgres.FromNullableTimestamptz(row.ExpiresAt),
		RevokedAt:                     postgres.FromNullableTimestamptz(row.RevokedAt),
		LastUsedAt:                    postgres.FromNullableTimestamptz(row.LastUsedAt),
		CreatedAt:                     row.CreatedAt.Time,
		UpdatedAt:                     row.UpdatedAt.Time,
	}
}

func personalAccessTokenFromFindRow(row dbgen.FindPersonalAccessTokenBySlugRow) *PersonalAccessToken {
	token := personalAccessTokenFromRow(dbgen.PersonalAccessToken{
		ID:                            row.ID,
		UserID:                        row.UserID,
		Slug:                          row.Slug,
		Name:                          row.Name,
		Description:                   row.Description,
		TokenPrefix:                   row.TokenPrefix,
		CanManagePersonalAccessTokens: row.CanManagePersonalAccessTokens,
		ExpiresAt:                     row.ExpiresAt,
		RevokedAt:                     row.RevokedAt,
		LastUsedAt:                    row.LastUsedAt,
		CreatedAt:                     row.CreatedAt,
		UpdatedAt:                     row.UpdatedAt,
	})
	token.UserSlug = row.UserSlug
	return token
}

func personalAccessTokenFromListRow(row dbgen.ListPersonalAccessTokensByUserRow) *PersonalAccessToken {
	token := personalAccessTokenFromRow(dbgen.PersonalAccessToken{
		ID:                            row.ID,
		UserID:                        row.UserID,
		Slug:                          row.Slug,
		Name:                          row.Name,
		Description:                   row.Description,
		TokenPrefix:                   row.TokenPrefix,
		CanManagePersonalAccessTokens: row.CanManagePersonalAccessTokens,
		ExpiresAt:                     row.ExpiresAt,
		RevokedAt:                     row.RevokedAt,
		LastUsedAt:                    row.LastUsedAt,
		CreatedAt:                     row.CreatedAt,
		UpdatedAt:                     row.UpdatedAt,
	})
	token.UserSlug = row.UserSlug
	return token
}

func personalAccessTokenFromRotateRow(row dbgen.RotatePersonalAccessTokenRow) *PersonalAccessToken {
	token := personalAccessTokenFromRow(dbgen.PersonalAccessToken{
		ID:                            row.ID,
		UserID:                        row.UserID,
		Slug:                          row.Slug,
		Name:                          row.Name,
		Description:                   row.Description,
		TokenPrefix:                   row.TokenPrefix,
		CanManagePersonalAccessTokens: row.CanManagePersonalAccessTokens,
		ExpiresAt:                     row.ExpiresAt,
		RevokedAt:                     row.RevokedAt,
		LastUsedAt:                    row.LastUsedAt,
		CreatedAt:                     row.CreatedAt,
		UpdatedAt:                     row.UpdatedAt,
	})
	token.UserSlug = row.UserSlug
	return token
}
