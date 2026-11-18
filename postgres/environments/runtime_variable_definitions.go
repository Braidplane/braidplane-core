package environments

import (
	"context"
	"database/sql"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func (r *repository) LockRuntimeVariableOrganization(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) error {
	_, err := dbgen.New(tx).LockRuntimeVariableOrganization(ctx, orgSlug)
	if xerror.Is(err, sql.ErrNoRows) {
		return xerror.NotFound(xerror.Organization)
	}
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *repository) FindRuntimeVariableDefinition(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, key string) (*model.RuntimeVariableDefinition, error) {
	row, err := dbgen.New(tx).FindRuntimeVariableDefinition(ctx, dbgen.FindRuntimeVariableDefinitionParams{
		OrgSlug: orgSlug, VariableKey: key,
	})
	if xerror.Is(err, sql.ErrNoRows) {
		return nil, ErrRuntimeVariableNotFound
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	definition, err := toRuntimeVariableDefinition(row)
	if err != nil {
		return nil, err
	}
	return &definition, nil
}

func (r *repository) ListRuntimeVariableDefinitions(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) ([]model.RuntimeVariableDefinition, error) {
	rows, err := dbgen.New(tx).ListRuntimeVariableDefinitions(ctx, orgSlug)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	definitions := make([]model.RuntimeVariableDefinition, 0, len(rows))
	for _, row := range rows {
		definition, err := toRuntimeVariableDefinition(row)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func (r *repository) UpsertRuntimeVariableDefinition(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, definition model.RuntimeVariableDefinition) (*model.RuntimeVariableDefinition, error) {
	row, err := dbgen.New(tx).UpsertRuntimeVariableDefinition(ctx, dbgen.UpsertRuntimeVariableDefinitionParams{
		OrgSlug:      orgSlug,
		VariableKey:  definition.Key,
		ValueType:    definition.ValueType.String(),
		Description:  nullableText(definition.Description),
		Required:     definition.Required,
		DefaultValue: nullableText(definition.DefaultValue),
	})
	if xerror.Is(err, sql.ErrNoRows) {
		return nil, xerror.NotFound(xerror.Organization)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result, err := toRuntimeVariableDefinition(row)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *repository) DeleteRuntimeVariableDefinition(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, key string) error {
	deleted, err := dbgen.New(tx).DeleteRuntimeVariableDefinition(ctx, dbgen.DeleteRuntimeVariableDefinitionParams{
		OrgSlug: orgSlug, VariableKey: key,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if deleted == 0 {
		return ErrRuntimeVariableNotFound
	}
	return nil
}

func toRuntimeVariableDefinition(row dbgen.RuntimeVariableDefinition) (model.RuntimeVariableDefinition, error) {
	valueType, err := model.RuntimeVariableValueTypeString(row.ValueType)
	if err != nil {
		return model.RuntimeVariableDefinition{}, xerror.Unexpected().With(err)
	}
	return model.RuntimeVariableDefinition{
		Key:          row.Key,
		ValueType:    valueType,
		Description:  textPtr(row.Description),
		Required:     row.Required,
		DefaultValue: textPtr(row.DefaultValue),
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}, nil
}

type RuntimeVariableComparisonEnvironment struct {
	Slug      slug.Slug
	Protected bool
}

func (r *repository) ListRuntimeVariableComparisonEnvironments(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) ([]RuntimeVariableComparisonEnvironment, error) {
	rows, err := dbgen.New(tx).ListRuntimeVariableComparisonEnvironments(ctx, orgSlug)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]RuntimeVariableComparisonEnvironment, len(rows))
	for i, row := range rows {
		result[i] = RuntimeVariableComparisonEnvironment{Slug: row.Slug, Protected: row.Protected}
	}
	return result, nil
}

func (r *repository) ListRuntimeVariableOverridesByOrganization(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) ([]model.RuntimeVariable, error) {
	rows, err := dbgen.New(tx).ListRuntimeVariableOverridesByOrganization(ctx, orgSlug)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]model.RuntimeVariable, len(rows))
	for i, row := range rows {
		variable, err := toRuntimeVariable(row.EnvironmentSlug, row.Key, row.ValueType, row.ValueText, row.Description, row.CreatedAt, row.UpdatedAt)
		if err != nil {
			return nil, err
		}
		result[i] = *variable
	}
	return result, nil
}
