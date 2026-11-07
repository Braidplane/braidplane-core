package apikeys

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
	ErrNotFound          = xerror.NotFound(xerror.APIKey)
	ErrSlugAlreadyExists = xerror.AlreadyExists(xerror.APIKey)
)

func NewRepository() Repository {
	return &repository{}
}

type repository struct{}

func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateParams,
) (*APIKey, error) {
	var createdByUserSlug slug.Slug
	if params.CreatedByUserSlug != nil {
		createdByUserSlug = *params.CreatedByUserSlug
	}
	row, err := dbgen.New(tx).CreateAPIKey(ctx, dbgen.CreateAPIKeyParams{
		ApiKeySlug:        params.Slug,
		Name:              params.Name,
		Description:       postgres.ToNullableText(params.Description),
		TokenHash:         params.TokenHash,
		TokenPrefix:       params.TokenPrefix,
		OrgSlug:           params.OrgSlug,
		CreatedByUserSlug: createdByUserSlug,
		RoleSlug:          params.RoleSlug,
		ExpiresAt:         postgres.ToNullableTimestamptz(params.ExpiresAt),
		CanManageApiKeys:  params.CanManageAPIKeys,
	})
	if err != nil {
		if postgres.IsUniqueViolation(err, "uq_api_keys_org_slug") {
			return nil, ErrSlugAlreadyExists.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return apiKeyFromRow(row), nil
}

func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, apiKeySlug slug.Slug,
) (*APIKey, error) {
	row, err := dbgen.New(tx).FindAPIKeyBySlug(ctx, dbgen.FindAPIKeyBySlugParams{
		OrgSlug:    orgSlug,
		ApiKeySlug: apiKeySlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return apiKeyFromFindRow(row), nil
}

func (r *repository) ListByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	roleFilter *slug.Slug,
	req model.ListRequest,
) ([]*APIKey, error) {
	cursorSlug, cursorTimestamp, cursorText, err := postgres.BuildCursorValues(
		req.Cursor,
		req.SortBy,
	)
	if err != nil {
		return nil, xerror.Wrapf("build api key cursor values: %w", err)
	}

	var roleSlugFilter slug.Slug
	if roleFilter != nil {
		roleSlugFilter = *roleFilter
	}

	rows, err := dbgen.New(tx).ListAPIKeysByOrganization(
		ctx,
		dbgen.ListAPIKeysByOrganizationParams{
			OrgSlug:         orgSlug,
			RoleSlugFilter:  roleSlugFilter.String(),
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

	result := make([]*APIKey, 0, len(rows))
	for _, row := range rows {
		result = append(result, apiKeyFromListRow(row))
	}

	return result, nil
}

func (r *repository) Update(ctx context.Context, tx postgres.DBTX, params UpdateParams) error {
	err := dbgen.New(tx).UpdateAPIKey(ctx, dbgen.UpdateAPIKeyParams{
		Name:                params.Name,
		Description:         postgres.ToNullableText(params.Description),
		ExpiresAt:           postgres.ToNullableTimestamptz(params.ExpiresAt),
		CanManageApiKeysSet: params.CanManageAPIKeysSet,
		CanManageApiKeys:    params.CanManageAPIKeys,
		OrgSlug:             params.OrgSlug,
		ApiKeySlug:          params.Slug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

func (r *repository) Revoke(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, apiKeySlug slug.Slug,
) error {
	err := dbgen.New(tx).RevokeAPIKey(ctx, dbgen.RevokeAPIKeyParams{
		OrgSlug:    orgSlug,
		ApiKeySlug: apiKeySlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

// DeleteRevoked removes revoked API key metadata scoped to an organization.
func (r *repository) DeleteRevoked(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, apiKeySlug slug.Slug,
) error {
	rowsAffected, err := dbgen.New(tx).DeleteRevokedAPIKey(ctx, dbgen.DeleteRevokedAPIKeyParams{
		OrgSlug:    orgSlug,
		ApiKeySlug: apiKeySlug,
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
) (*APIKey, error) {
	row, err := dbgen.New(tx).RotateAPIKey(ctx, dbgen.RotateAPIKeyParams{
		TokenHash:   params.TokenHash,
		TokenPrefix: params.TokenPrefix,
		OrgSlug:     params.OrgSlug,
		ApiKeySlug:  params.Slug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return apiKeyFromRotateRow(row), nil
}

func (r *repository) FindActivePrincipalByTokenHash(
	ctx context.Context,
	tx postgres.DBTX,
	tokenHash string,
) (*Principal, error) {
	row, err := dbgen.New(tx).FindActiveAPIKeyByTokenHash(ctx, tokenHash)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return &Principal{
		Slug:             row.Slug,
		OrgSlug:          row.OrgSlug,
		Role:             row.RoleSlug,
		CanManageAPIKeys: row.CanManageApiKeys,
	}, nil
}

func (r *repository) MarkUsed(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, apiKeySlug slug.Slug,
) error {
	rowsAffected, err := dbgen.New(tx).MarkAPIKeyUsed(ctx, dbgen.MarkAPIKeyUsedParams{
		OrgSlug:    orgSlug,
		ApiKeySlug: apiKeySlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}

	return nil
}

func apiKeyFromRow(row dbgen.ApiKey) *APIKey {
	return &APIKey{
		ID:               row.ID,
		Slug:             row.Slug,
		Name:             row.Name,
		Description:      postgres.FromNullableText(row.Description),
		TokenPrefix:      row.TokenPrefix,
		CanManageAPIKeys: row.CanManageApiKeys,
		ExpiresAt:        postgres.FromNullableTimestamptz(row.ExpiresAt),
		RevokedAt:        postgres.FromNullableTimestamptz(row.RevokedAt),
		LastUsedAt:       postgres.FromNullableTimestamptz(row.LastUsedAt),
		CreatedAt:        row.CreatedAt.Time,
		UpdatedAt:        row.UpdatedAt.Time,
	}
}

func apiKeyFromFindRow(row dbgen.FindAPIKeyBySlugRow) *APIKey {
	apiKey := apiKeyFromRow(dbgen.ApiKey{
		ID:                row.ID,
		OrganizationID:    row.OrganizationID,
		Slug:              row.Slug,
		Name:              row.Name,
		Description:       row.Description,
		TokenPrefix:       row.TokenPrefix,
		CanManageApiKeys:  row.CanManageApiKeys,
		CreatedByMemberID: row.CreatedByMemberID,
		RoleID:            row.RoleID,
		ExpiresAt:         row.ExpiresAt,
		RevokedAt:         row.RevokedAt,
		LastUsedAt:        row.LastUsedAt,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	})
	apiKey.RoleSlug = row.RoleSlug
	apiKey.CreatedByUserSlug = optionalSlug(row.CreatedByUserSlug)
	return apiKey
}

func apiKeyFromListRow(row dbgen.ListAPIKeysByOrganizationRow) *APIKey {
	apiKey := apiKeyFromRow(dbgen.ApiKey{
		ID:                row.ID,
		OrganizationID:    row.OrganizationID,
		Slug:              row.Slug,
		Name:              row.Name,
		Description:       row.Description,
		TokenPrefix:       row.TokenPrefix,
		CanManageApiKeys:  row.CanManageApiKeys,
		CreatedByMemberID: row.CreatedByMemberID,
		RoleID:            row.RoleID,
		ExpiresAt:         row.ExpiresAt,
		RevokedAt:         row.RevokedAt,
		LastUsedAt:        row.LastUsedAt,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	})
	apiKey.RoleSlug = row.RoleSlug
	apiKey.CreatedByUserSlug = optionalSlug(row.CreatedByUserSlug)
	return apiKey
}

func apiKeyFromRotateRow(row dbgen.RotateAPIKeyRow) *APIKey {
	apiKey := apiKeyFromRow(dbgen.ApiKey{
		ID:                row.ID,
		OrganizationID:    row.OrganizationID,
		Slug:              row.Slug,
		Name:              row.Name,
		Description:       row.Description,
		TokenPrefix:       row.TokenPrefix,
		CanManageApiKeys:  row.CanManageApiKeys,
		CreatedByMemberID: row.CreatedByMemberID,
		RoleID:            row.RoleID,
		ExpiresAt:         row.ExpiresAt,
		RevokedAt:         row.RevokedAt,
		LastUsedAt:        row.LastUsedAt,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	})
	apiKey.RoleSlug = row.RoleSlug
	apiKey.CreatedByUserSlug = optionalSlug(row.CreatedByUserSlug)
	return apiKey
}

func optionalSlug(value slug.Slug) *slug.Slug {
	if value == "" {
		return nil
	}
	return &value
}
