package environments

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type repository struct{}

func nullableText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

// environmentColorFromStorage preserves stored palette values and neutralizes unexpected legacy data.
func environmentColorFromStorage(value string) model.EnvironmentColor {
	color, err := model.EnvironmentColorString(value)
	if err != nil {
		slog.Warn("unexpected environment color value in storage; defaulting to gray", "value", value)
		return model.EnvironmentColorGray
	}
	return color
}

func toEnvironment(row dbgen.Environment) *Environment {
	return &Environment{
		Slug:         row.Slug,
		Name:         row.Name,
		Description:  textPtr(row.Description),
		Color:        environmentColorFromStorage(row.Color),
		Protected:    row.Protected,
		DeletedAt:    timePtr(row.DeletedAt),
		DeleteReason: textPtr(row.DeleteReason),
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}
}

func toProjectEnvironment(
	environmentSlug slug.Slug,
	environmentName string,
	environmentDescription pgtype.Text,
	environmentColor string,
	environmentDeletedAt pgtype.Timestamptz,
	environmentProtected bool,
	environmentCreatedAt pgtype.Timestamptz,
	environmentUpdatedAt pgtype.Timestamptz,
	isDefault bool,
	enabledAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
) *ProjectEnvironment {
	return &ProjectEnvironment{
		Environment: Environment{
			Slug:        environmentSlug,
			Name:        environmentName,
			Description: textPtr(environmentDescription),
			Color:       environmentColorFromStorage(environmentColor),
			Protected:   environmentProtected,
			DeletedAt:   timePtr(environmentDeletedAt),
			CreatedAt:   environmentCreatedAt.Time,
			UpdatedAt:   environmentUpdatedAt.Time,
		},
		IsDefault: isDefault,
		EnabledAt: enabledAt.Time,
		UpdatedAt: updatedAt.Time,
	}
}

func toRuntimeVariable(
	environmentSlug slug.Slug,
	key string,
	valueType string,
	valueText string,
	description pgtype.Text,
	createdAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
) (*model.RuntimeVariable, error) {
	parsedValueType, err := model.RuntimeVariableValueTypeString(valueType)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return &model.RuntimeVariable{
		EnvironmentSlug: environmentSlug,
		Key:             key,
		ValueType:       parsedValueType,
		Value:           valueText,
		Description:     textPtr(description),
		CreatedAt:       createdAt.Time,
		UpdatedAt:       updatedAt.Time,
	}, nil
}

func toEnvironmentSecret(
	environmentSlug slug.Slug,
	key string,
	description pgtype.Text,
	createdAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
) *model.EnvironmentSecret {
	return &model.EnvironmentSecret{
		EnvironmentSlug: environmentSlug,
		Key:             key,
		Description:     textPtr(description),
		CreatedAt:       createdAt.Time,
		UpdatedAt:       updatedAt.Time,
	}
}

func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	params *CreateParams,
) (*Environment, error) {
	row, err := dbgen.New(tx).CreateEnvironment(ctx, dbgen.CreateEnvironmentParams{
		EnvironmentSlug: params.Slug,
		Name:            params.Name,
		Description:     nullableText(params.Description),
		Color:           params.Color.String(),
		Protected:       params.Protected,
		OrgSlug:         orgSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		if postgres.IsUniqueViolation(err, "uq_environments_org_slug") {
			return nil, ErrSlugTaken.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toEnvironment(row), nil
}

func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
) (*Environment, error) {
	row, err := dbgen.New(tx).FindEnvironmentBySlug(ctx, dbgen.FindEnvironmentBySlugParams{
		OrgSlug:         orgSlug,
		EnvironmentSlug: environmentSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toEnvironment(row), nil
}

func (r *repository) FindAnyBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
) (*Environment, error) {
	row, err := dbgen.New(tx).FindAnyEnvironmentBySlug(
		ctx,
		dbgen.FindAnyEnvironmentBySlugParams{
			OrgSlug:         orgSlug,
			EnvironmentSlug: environmentSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toEnvironment(row), nil
}

func (r *repository) ListByOrg(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListEnvironmentsRequest,
) ([]*Environment, error) {
	cursorSlug, cursorTimestamp, cursorText, err := postgres.BuildCursorValues(
		req.Cursor,
		req.SortBy,
	)
	if err != nil {
		return nil, xerror.Wrapf("build environment cursor values: %w", err)
	}

	rows, err := dbgen.New(tx).ListEnvironmentsByOrg(ctx, dbgen.ListEnvironmentsByOrgParams{
		OrgSlug:         orgSlug,
		IncludeDeleted:  req.IncludeDeleted,
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

	result := make([]*Environment, len(rows))
	for i, row := range rows {
		result[i] = toEnvironment(row)
	}
	return result, nil
}

func (r *repository) Update(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	params *UpdateParams,
) (*Environment, error) {
	row, err := dbgen.New(tx).UpdateEnvironment(ctx, dbgen.UpdateEnvironmentParams{
		Name:            params.Name,
		Description:     nullableText(params.Description),
		Color:           params.Color.String(),
		Protected:       params.Protected,
		OrgSlug:         orgSlug,
		EnvironmentSlug: environmentSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toEnvironment(row), nil
}

func (r *repository) Delete(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	params *DeleteParams,
) error {
	rowsAffected, err := dbgen.New(tx).DeleteEnvironment(ctx, dbgen.DeleteEnvironmentParams{
		DeleteReason:    nullableText(params.Reason),
		OrgSlug:         orgSlug,
		EnvironmentSlug: environmentSlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *repository) EnableProjectEnvironment(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params *EnableProjectParams,
) (*ProjectEnvironment, error) {
	if params.IsDefault {
		if _, err := dbgen.New(tx).ClearProjectEnvironmentDefault(
			ctx,
			dbgen.ClearProjectEnvironmentDefaultParams{
				OrgSlug:     orgSlug,
				ProjectSlug: projectSlug,
			},
		); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
	}

	row, err := dbgen.New(tx).EnableProjectEnvironment(
		ctx,
		dbgen.EnableProjectEnvironmentParams{
			IsDefault:       params.IsDefault,
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrProjectEnvironmentNotFound.With(err)
		}
		if postgres.IsUniqueViolation(err, "uq_project_environments_project_default") {
			return nil, ErrProjectEnvironmentTaken.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toProjectEnvironment(
		row.EnvironmentSlug,
		row.EnvironmentName,
		row.EnvironmentDescription,
		row.EnvironmentColor,
		row.EnvironmentDeletedAt,
		row.EnvironmentProtected,
		row.EnvironmentCreatedAt,
		row.EnvironmentUpdatedAt,
		row.IsDefault,
		row.EnabledAt,
		row.UpdatedAt,
	), nil
}

func (r *repository) ListProjectEnvironments(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	req model.ListRequest,
) ([]*ProjectEnvironment, error) {
	cursorSlug, cursorTimestamp, cursorText, err := postgres.BuildCursorValues(
		req.Cursor,
		req.SortBy,
	)
	if err != nil {
		return nil, xerror.Wrapf("build project environment cursor values: %w", err)
	}

	rows, err := dbgen.New(tx).ListProjectEnvironments(
		ctx,
		dbgen.ListProjectEnvironmentsParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
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

	result := make([]*ProjectEnvironment, len(rows))
	for i, row := range rows {
		result[i] = toProjectEnvironment(
			row.EnvironmentSlug,
			row.EnvironmentName,
			row.EnvironmentDescription,
			row.EnvironmentColor,
			row.EnvironmentDeletedAt,
			row.EnvironmentProtected,
			row.EnvironmentCreatedAt,
			row.EnvironmentUpdatedAt,
			row.IsDefault,
			row.EnabledAt,
			row.UpdatedAt,
		)
	}
	return result, nil
}

func (r *repository) FindProjectEnvironmentProtection(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	row, err := dbgen.New(tx).FindProjectEnvironmentProtection(
		ctx,
		dbgen.FindProjectEnvironmentProtectionParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrProjectEnvironmentNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return &model.ProjectEnvironmentProtection{
		EnvironmentSlug: row.EnvironmentSlug,
		Protected:       row.Protected,
	}, nil
}

func (r *repository) FindProjectEnvironmentCatalogProtection(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	row, err := dbgen.New(tx).FindProjectEnvironmentCatalogProtection(
		ctx,
		dbgen.FindProjectEnvironmentCatalogProtectionParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrProjectEnvironmentNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return &model.ProjectEnvironmentProtection{
		EnvironmentSlug: row.EnvironmentSlug,
		Protected:       row.Protected,
	}, nil
}

func (r *repository) DisableProjectEnvironment(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) error {
	rowsAffected, err := dbgen.New(tx).DisableProjectEnvironment(
		ctx,
		dbgen.DisableProjectEnvironmentParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
		},
	)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrProjectEnvironmentNotFound
	}
	return nil
}

func (r *repository) UpsertRuntimeVariable(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	params model.UpsertRuntimeVariable,
) (*model.RuntimeVariable, error) {
	row, err := dbgen.New(tx).UpsertRuntimeVariable(
		ctx,
		dbgen.UpsertRuntimeVariableParams{
			OrgSlug:         orgSlug,
			EnvironmentSlug: environmentSlug,
			VariableKey:     params.Key,
			ValueType:       params.ValueType.String(),
			ValueText:       params.Value,
			Description:     nullableText(params.Description),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toRuntimeVariable(
		slug.FromString(row.EnvironmentSlug),
		row.Key,
		row.ValueType,
		row.ValueText,
		row.Description,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func (r *repository) FindRuntimeVariable(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	key string,
) (*model.RuntimeVariable, error) {
	row, err := dbgen.New(tx).FindRuntimeVariable(
		ctx,
		dbgen.FindRuntimeVariableParams{
			OrgSlug:         orgSlug,
			EnvironmentSlug: environmentSlug,
			VariableKey:     key,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrRuntimeVariableNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toRuntimeVariable(
		row.EnvironmentSlug,
		row.Key,
		row.ValueType,
		row.ValueText,
		row.Description,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func (r *repository) ListRuntimeVariables(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) ([]*model.RuntimeVariable, error) {
	if _, err := r.FindBySlug(ctx, tx, orgSlug, environmentSlug); err != nil {
		return nil, err
	}

	cursorKey, cursorTimestamp, _, err := postgres.BuildCursorValues(req.Cursor, model.ListSortUpdatedAt)
	if err != nil {
		return nil, xerror.Wrapf("build runtime variable cursor values: %w", err)
	}

	rows, err := dbgen.New(tx).ListRuntimeVariables(
		ctx,
		dbgen.ListRuntimeVariablesParams{
			OrgSlug:         orgSlug,
			EnvironmentSlug: environmentSlug,
			CursorKey:       cursorKey,
			OrderDir:        pgtype.Text{String: req.Order, Valid: req.Order != ""},
			CursorTimestamp: cursorTimestamp,
			LimitCount:      int64(req.Limit),
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*model.RuntimeVariable, len(rows))
	for i, row := range rows {
		variable, err := toRuntimeVariable(
			row.EnvironmentSlug,
			row.Key,
			row.ValueType,
			row.ValueText,
			row.Description,
			row.CreatedAt,
			row.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		result[i] = variable
	}
	return result, nil
}

func (r *repository) DeleteRuntimeVariable(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	key string,
) error {
	rowsAffected, err := dbgen.New(tx).DeleteRuntimeVariable(
		ctx,
		dbgen.DeleteRuntimeVariableParams{
			OrgSlug:         orgSlug,
			EnvironmentSlug: environmentSlug,
			VariableKey:     key,
		},
	)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrRuntimeVariableNotFound
	}
	return nil
}

func (r *repository) UpsertEnvironmentSecret(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	params model.UpsertEnvironmentSecret,
	credentialSlug slug.Slug,
) (*model.EnvironmentSecret, error) {
	row, err := dbgen.New(tx).UpsertEnvironmentSecret(
		ctx,
		dbgen.UpsertEnvironmentSecretParams{
			OrgSlug:              orgSlug,
			EnvironmentSlug:      environmentSlug,
			CredentialSlug:       credentialSlug,
			EnvironmentSecretKey: params.Key,
			Description:          nullableText(params.Description),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toEnvironmentSecret(
		slug.FromString(row.EnvironmentSlug),
		row.Key,
		row.Description,
		row.CreatedAt,
		row.UpdatedAt,
	), nil
}

func (r *repository) FindEnvironmentSecret(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	key string,
) (*model.EnvironmentSecret, error) {
	row, err := dbgen.New(tx).FindEnvironmentSecret(
		ctx,
		dbgen.FindEnvironmentSecretParams{
			OrgSlug:              orgSlug,
			EnvironmentSlug:      environmentSlug,
			EnvironmentSecretKey: key,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrEnvironmentSecretNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toEnvironmentSecret(
		row.EnvironmentSlug,
		row.Key,
		row.Description,
		row.CreatedAt,
		row.UpdatedAt,
	), nil
}

func (r *repository) ListEnvironmentSecrets(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) ([]*model.EnvironmentSecret, error) {
	cursorKey, cursorTimestamp, _, err := postgres.BuildCursorValues(req.Cursor, model.ListSortUpdatedAt)
	if err != nil {
		return nil, xerror.Wrapf("build environment secret cursor values: %w", err)
	}

	rows, err := dbgen.New(tx).ListEnvironmentSecrets(
		ctx,
		dbgen.ListEnvironmentSecretsParams{
			OrgSlug:         orgSlug,
			EnvironmentSlug: environmentSlug,
			CursorKey:       cursorKey,
			OrderDir:        pgtype.Text{String: req.Order, Valid: req.Order != ""},
			CursorTimestamp: cursorTimestamp,
			LimitCount:      int64(req.Limit),
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*model.EnvironmentSecret, len(rows))
	for i, row := range rows {
		result[i] = toEnvironmentSecret(
			row.EnvironmentSlug,
			row.Key,
			row.Description,
			row.CreatedAt,
			row.UpdatedAt,
		)
	}
	return result, nil
}

func (r *repository) ListAllEnvironmentSecrets(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
) ([]*model.EnvironmentSecret, error) {
	rows, err := dbgen.New(tx).ListAllEnvironmentSecrets(
		ctx,
		dbgen.ListAllEnvironmentSecretsParams{
			OrgSlug:         orgSlug,
			EnvironmentSlug: environmentSlug,
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*model.EnvironmentSecret, len(rows))
	for i, row := range rows {
		result[i] = toEnvironmentSecret(
			row.EnvironmentSlug,
			row.Key,
			row.Description,
			row.CreatedAt,
			row.UpdatedAt,
		)
	}
	return result, nil
}

func (r *repository) DeleteEnvironmentSecret(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
	key string,
) error {
	deletedCount, err := dbgen.New(tx).DeleteEnvironmentSecret(
		ctx,
		dbgen.DeleteEnvironmentSecretParams{
			OrgSlug:              orgSlug,
			EnvironmentSlug:      environmentSlug,
			EnvironmentSecretKey: key,
		},
	)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if deletedCount == 0 {
		return ErrEnvironmentSecretNotFound
	}
	return nil
}

func (r *repository) DeleteEnvironmentSecretsForEnvironment(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, environmentSlug slug.Slug,
) error {
	if _, err := dbgen.New(tx).DeleteEnvironmentSecretsForEnvironment(
		ctx,
		dbgen.DeleteEnvironmentSecretsForEnvironmentParams{
			OrgSlug:         orgSlug,
			EnvironmentSlug: environmentSlug,
		},
	); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}
