package datasourcerefs

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

type repository struct{}

func toCapabilityStrings(values []model.DataSourceRefCapability) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.String()
	}
	return result
}

func capabilitiesFromStrings(values []string) ([]model.DataSourceRefCapability, error) {
	result := make([]model.DataSourceRefCapability, len(values))
	for i, value := range values {
		parsed, err := model.DataSourceRefCapabilityString(value)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		result[i] = parsed
	}
	return result, nil
}

func dataSourceTypeFromString(value string) (model.DataSourceType, error) {
	parsed, err := model.DataSourceTypeString(value)
	if err != nil {
		return model.DataSourceTypeUnknown, xerror.Unexpected().With(err)
	}
	return parsed, nil
}

func toDataSourceRef(
	pipelineSlug, dataSourceRefSlug slug.Slug,
	name string,
	dataSourceType string,
	requiredCapabilities []string,
	createdAt, updatedAt pgtype.Timestamptz,
) (*DataSourceRef, error) {
	dsType, err := dataSourceTypeFromString(dataSourceType)
	if err != nil {
		return nil, err
	}
	capabilities, err := capabilitiesFromStrings(requiredCapabilities)
	if err != nil {
		return nil, err
	}
	return &DataSourceRef{
		PipelineSlug:         pipelineSlug,
		Slug:                 dataSourceRefSlug,
		Name:                 name,
		DataSourceType:       dsType,
		RequiredCapabilities: capabilities,
		CreatedAt:            createdAt.Time,
		UpdatedAt:            updatedAt.Time,
	}, nil
}

func toDataSourceBinding(
	pipelineSlug slug.Slug,
	dataSourceRefSlug slug.Slug,
	dataSourceRefName string,
	refRequiredCapabilities []string,
	environmentSlug slug.Slug,
	dataSourceSlug slug.Slug,
	dataSourceName string,
	dataSourceType string,
	dataSourceActive bool,
	dataSourceReadOnly bool,
	createdAt, updatedAt pgtype.Timestamptz,
) (*DataSourceBinding, error) {
	dsType, err := dataSourceTypeFromString(dataSourceType)
	if err != nil {
		return nil, err
	}
	capabilities, err := capabilitiesFromStrings(refRequiredCapabilities)
	if err != nil {
		return nil, err
	}
	return &DataSourceBinding{
		PipelineSlug:         pipelineSlug,
		DataSourceRefSlug:    dataSourceRefSlug,
		DataSourceRefName:    dataSourceRefName,
		EnvironmentSlug:      environmentSlug,
		DataSourceSlug:       dataSourceSlug,
		DataSourceName:       dataSourceName,
		DataSourceType:       dsType,
		DataSourceActive:     dataSourceActive,
		DataSourceReadOnly:   dataSourceReadOnly,
		RequiredCapabilities: capabilities,
		CreatedAt:            createdAt.Time,
		UpdatedAt:            updatedAt.Time,
	}, nil
}

func toRuntimeVariable(
	environmentSlug slug.Slug,
	key string,
	valueType string,
	valueText string,
	description pgtype.Text,
	createdAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
) (model.RuntimeVariable, error) {
	parsedValueType, err := model.RuntimeVariableValueTypeString(valueType)
	if err != nil {
		return model.RuntimeVariable{}, xerror.Unexpected().With(err)
	}
	return model.RuntimeVariable{
		EnvironmentSlug: environmentSlug,
		Key:             key,
		ValueType:       parsedValueType,
		Value:           valueText,
		Description:     textPtr(description),
		CreatedAt:       createdAt.Time,
		UpdatedAt:       updatedAt.Time,
	}, nil
}

func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func (r *repository) CreateDataSourceRef(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	params CreateDataSourceRefParams,
) (*DataSourceRef, error) {
	row, err := dbgen.New(tx).CreateDataSourceRef(ctx, dbgen.CreateDataSourceRefParams{
		PipelineSlug:         pipelineSlug,
		DataSourceRefSlug:    params.Slug,
		Name:                 params.Name,
		DataSourceType:       params.DataSourceType.String(),
		RequiredCapabilities: toCapabilityStrings(params.RequiredCapabilities),
		OrgSlug:              orgSlug,
		ProjectSlug:          projectSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDataSourceRefNotFound.With(err)
		}
		if postgres.IsUniqueViolation(err, "uq_data_source_refs_pipeline_slug") {
			return nil, ErrDataSourceRefSlugTaken.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return toDataSourceRef(
		row.PipelineSlug,
		row.Slug,
		row.Name,
		row.DataSourceType,
		row.RequiredCapabilities,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func (r *repository) FindDataSourceRefBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, dataSourceRefSlug slug.Slug,
) (*DataSourceRef, error) {
	row, err := dbgen.New(tx).FindDataSourceRefBySlug(ctx, dbgen.FindDataSourceRefBySlugParams{
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		PipelineSlug:      pipelineSlug,
		DataSourceRefSlug: dataSourceRefSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDataSourceRefNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return toDataSourceRef(
		row.PipelineSlug,
		row.Slug,
		row.Name,
		row.DataSourceType,
		row.RequiredCapabilities,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func (r *repository) ListDataSourceRefs(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	req model.ListRequest,
) ([]*DataSourceRef, error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)
	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(queryReq.Cursor, normalized.SortBy)
	if err != nil {
		return nil, xerror.Wrapf("build data source ref cursor values: %w", err)
	}
	rows, err := dbgen.New(tx).ListDataSourceRefs(ctx, dbgen.ListDataSourceRefsParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		CursorSlug:      cursorSlug,
		OrderDir:        pgtype.Text{String: queryReq.Order, Valid: queryReq.Order != ""},
		CursorTimestamp: cursorTimestamp,
		LimitCount:      int64(queryReq.Limit),
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]*DataSourceRef, 0, len(rows))
	for _, row := range rows {
		converted, err := toDataSourceRef(
			row.PipelineSlug,
			row.Slug,
			row.Name,
			row.DataSourceType,
			row.RequiredCapabilities,
			row.CreatedAt,
			row.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func (r *repository) ListDataSourceRefsByPipeline(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) ([]*DataSourceRef, error) {
	rows, err := dbgen.New(tx).ListDataSourceRefsByPipeline(ctx, dbgen.ListDataSourceRefsByPipelineParams{
		OrgSlug:      orgSlug,
		ProjectSlug:  projectSlug,
		PipelineSlug: pipelineSlug,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]*DataSourceRef, 0, len(rows))
	for _, row := range rows {
		converted, err := toDataSourceRef(
			row.PipelineSlug,
			row.Slug,
			row.Name,
			row.DataSourceType,
			row.RequiredCapabilities,
			row.CreatedAt,
			row.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func (r *repository) UpdateDataSourceRef(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, dataSourceRefSlug slug.Slug,
	params UpdateDataSourceRefParams,
) (*DataSourceRef, error) {
	row, err := dbgen.New(tx).UpdateDataSourceRef(ctx, dbgen.UpdateDataSourceRefParams{
		Name:                 params.Name,
		DataSourceType:       params.DataSourceType.String(),
		RequiredCapabilities: toCapabilityStrings(params.RequiredCapabilities),
		OrgSlug:              orgSlug,
		ProjectSlug:          projectSlug,
		PipelineSlug:         pipelineSlug,
		DataSourceRefSlug:    dataSourceRefSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDataSourceRefNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return toDataSourceRef(
		row.PipelineSlug,
		row.Slug,
		row.Name,
		row.DataSourceType,
		row.RequiredCapabilities,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func (r *repository) DeleteDataSourceRef(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, dataSourceRefSlug slug.Slug,
) error {
	rowsAffected, err := dbgen.New(tx).DeleteDataSourceRef(ctx, dbgen.DeleteDataSourceRefParams{
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		PipelineSlug:      pipelineSlug,
		DataSourceRefSlug: dataSourceRefSlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrDataSourceRefNotFound
	}
	return nil
}

func (r *repository) DeleteDataSourceRefsForPipeline(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) error {
	_, err := dbgen.New(tx).DeleteDataSourceRefsForPipeline(ctx, dbgen.DeleteDataSourceRefsForPipelineParams{
		OrgSlug:      orgSlug,
		ProjectSlug:  projectSlug,
		PipelineSlug: pipelineSlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *repository) ListProtectedBindingEnvironmentsForDataSourceRef(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, dataSourceRefSlug slug.Slug,
) ([]model.ProjectEnvironmentProtection, error) {
	rows, err := dbgen.New(tx).ListProtectedBindingEnvironmentsForDataSourceRef(
		ctx,
		dbgen.ListProtectedBindingEnvironmentsForDataSourceRefParams{
			OrgSlug:           orgSlug,
			ProjectSlug:       projectSlug,
			PipelineSlug:      pipelineSlug,
			DataSourceRefSlug: dataSourceRefSlug,
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	protections := make([]model.ProjectEnvironmentProtection, 0, len(rows))
	for _, row := range rows {
		protections = append(protections, model.ProjectEnvironmentProtection{
			EnvironmentSlug: row.EnvironmentSlug,
			Protected:       row.Protected,
		})
	}
	return protections, nil
}

func (r *repository) UpsertEnvironmentBinding(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, environmentSlug, dataSourceRefSlug slug.Slug,
	params UpsertEnvironmentBindingParams,
) (*DataSourceBinding, error) {
	row, err := dbgen.New(tx).UpsertDataSourceBinding(ctx, dbgen.UpsertDataSourceBindingParams{
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		EnvironmentSlug:   environmentSlug,
		PipelineSlug:      pipelineSlug,
		DataSourceRefSlug: dataSourceRefSlug,
		DataSourceSlug:    params.DataSourceSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDataSourceBindingNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return toDataSourceBinding(
		row.PipelineSlug,
		row.DataSourceRefSlug,
		row.DataSourceRefName,
		row.RefRequiredCapabilities,
		row.EnvironmentSlug,
		row.DataSourceSlug,
		row.DataSourceName,
		row.DataSourceType,
		row.DataSourceActive,
		row.DataSourceReadOnly,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func (r *repository) ProjectEnvironmentEnabled(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (bool, error) {
	enabled, err := dbgen.New(tx).ProjectEnvironmentEnabled(ctx, dbgen.ProjectEnvironmentEnabledParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
	})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return enabled, nil
}

func (r *repository) FindProjectEnvironmentProtection(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	row, err := dbgen.New(tx).FindProjectEnvironmentProtection(ctx, dbgen.FindProjectEnvironmentProtectionParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrProjectEnvironmentNotEnabled.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return &model.ProjectEnvironmentProtection{
		EnvironmentSlug: row.EnvironmentSlug,
		Protected:       row.Protected,
	}, nil
}

func (r *repository) FindEnvironmentBinding(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, environmentSlug, dataSourceRefSlug slug.Slug,
) (*DataSourceBinding, error) {
	row, err := dbgen.New(tx).FindDataSourceBinding(ctx, dbgen.FindDataSourceBindingParams{
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		EnvironmentSlug:   environmentSlug,
		PipelineSlug:      pipelineSlug,
		DataSourceRefSlug: dataSourceRefSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDataSourceBindingNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return toDataSourceBinding(
		row.PipelineSlug,
		row.DataSourceRefSlug,
		row.DataSourceRefName,
		row.RefRequiredCapabilities,
		row.EnvironmentSlug,
		row.DataSourceSlug,
		row.DataSourceName,
		row.DataSourceType,
		row.DataSourceActive,
		row.DataSourceReadOnly,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func (r *repository) ListEnvironmentBindings(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) ([]*DataSourceBinding, error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortUpdatedAt)
	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(queryReq.Cursor, normalized.SortBy)
	if err != nil {
		return nil, xerror.Wrapf("build data source binding cursor values: %w", err)
	}
	rows, err := dbgen.New(tx).ListDataSourceBindings(ctx, dbgen.ListDataSourceBindingsParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		CursorSlug:      cursorSlug,
		OrderDir:        pgtype.Text{String: queryReq.Order, Valid: queryReq.Order != ""},
		CursorTimestamp: cursorTimestamp,
		LimitCount:      int64(queryReq.Limit),
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]*DataSourceBinding, 0, len(rows))
	for _, row := range rows {
		converted, err := toDataSourceBinding(
			row.PipelineSlug,
			row.DataSourceRefSlug,
			row.DataSourceRefName,
			row.RefRequiredCapabilities,
			row.EnvironmentSlug,
			row.DataSourceSlug,
			row.DataSourceName,
			row.DataSourceType,
			row.DataSourceActive,
			row.DataSourceReadOnly,
			row.CreatedAt,
			row.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func (r *repository) ListEnvironmentBindingsForDataSourceRef(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, dataSourceRefSlug slug.Slug,
) ([]*DataSourceBinding, error) {
	rows, err := dbgen.New(tx).ListDataSourceBindingsForDataSourceRef(ctx, dbgen.ListDataSourceBindingsForDataSourceRefParams{
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		PipelineSlug:      pipelineSlug,
		DataSourceRefSlug: dataSourceRefSlug,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]*DataSourceBinding, 0, len(rows))
	for _, row := range rows {
		converted, err := toDataSourceBinding(
			row.PipelineSlug,
			row.DataSourceRefSlug,
			row.DataSourceRefName,
			row.RefRequiredCapabilities,
			row.EnvironmentSlug,
			row.DataSourceSlug,
			row.DataSourceName,
			row.DataSourceType,
			row.DataSourceActive,
			row.DataSourceReadOnly,
			row.CreatedAt,
			row.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func (r *repository) ResolveEnvironmentBinding(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, environmentSlug, dataSourceRefSlug slug.Slug,
) (*DataSourceBinding, error) {
	enabled, err := r.ProjectEnvironmentEnabled(ctx, tx, orgSlug, projectSlug, environmentSlug)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, ErrProjectEnvironmentNotEnabled
	}
	return r.FindEnvironmentBinding(ctx, tx, orgSlug, projectSlug, pipelineSlug, environmentSlug, dataSourceRefSlug)
}

func (r *repository) ListRuntimeVariables(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) ([]model.RuntimeVariable, error) {
	rows, err := dbgen.New(tx).ListAllRuntimeVariables(ctx, dbgen.ListAllRuntimeVariablesParams{
		OrgSlug:         orgSlug,
		EnvironmentSlug: environmentSlug,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]model.RuntimeVariable, len(rows))
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

func (r *repository) DeleteEnvironmentBinding(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, environmentSlug, dataSourceRefSlug slug.Slug,
) error {
	rowsAffected, err := dbgen.New(tx).DeleteDataSourceBinding(ctx, dbgen.DeleteDataSourceBindingParams{
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		EnvironmentSlug:   environmentSlug,
		PipelineSlug:      pipelineSlug,
		DataSourceRefSlug: dataSourceRefSlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrDataSourceBindingNotFound
	}
	return nil
}
