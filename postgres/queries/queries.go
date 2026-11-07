package queries

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

// repository implements Repository.
type repository struct{}

// Create inserts a new query with its parameters.
func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *CreateParams,
) (*Query, []*QueryParameter, error) {
	queries := dbgen.New(tx)

	// Create the query with org and data source verification built into the query
	sqlcParams := dbgen.CreateQueryParams{
		OrgSlug:                  orgSlug,
		DataSourceSlug:           dsSlug,
		Slug:                     params.Slug,
		Name:                     params.Name,
		Description:              postgres.ToNullableText(params.Description),
		Data:                     params.Data,
		ExpectedInputContract:    params.ExpectedInputContract,
		ExpectedInputContractRef: params.ExpectedInputContractRef,
		OutputContract:           params.OutputContract,
		OutputContractRef:        params.OutputContractRef,
	}
	row, err := queries.CreateQuery(ctx, sqlcParams)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrDataSourceNotFound.With(err)
		}
		if postgres.IsUniqueViolation(err, "queries_data_source_id_slug_key") {
			return nil, nil, ErrSlugTaken.With(err)
		}
		return nil, nil, err
	}

	// Create query parameters
	queryParams := make([]*QueryParameter, 0, len(params.Parameters))
	for _, param := range params.Parameters {
		sqlcParam := dbgen.CreateQueryParameterParams{
			Slug:           param.Slug,
			OrgSlug:        orgSlug,
			DataSourceSlug: dsSlug,
			QuerySlug:      params.Slug,
			Name:           param.Name,
			DataType:       param.DataType,
			IsRequired:     param.IsRequired,
			DefaultValue:   postgres.ToNullableText(param.DefaultValue),
			Description:    postgres.ToNullableText(param.Description),
		}
		paramRow, err := queries.CreateQueryParameter(ctx, sqlcParam)
		if err != nil {
			return nil, nil, err
		}
		queryParams = append(queryParams, toQueryParameter(paramRow))
	}

	query := toQuery(row, dsSlug)
	return query, queryParams, nil
}

// FindBySlug fetches a query by slug with its parameters.
func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
) (*Query, []*QueryParameter, error) {
	queries := dbgen.New(tx)

	// Fetch the query with org and data source verification
	row, err := queries.FindQueryBySlug(ctx, dbgen.FindQueryBySlugParams{
		OrgSlug:        orgSlug,
		DataSourceSlug: dsSlug,
		QuerySlug:      querySlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrQueryNotFound.With(err)
		}
		return nil, nil, err
	}

	query := toQuery(row, dsSlug)

	// Fetch parameters using slug-based lookup
	paramRows, err := queries.ListQueryParametersByQuery(
		ctx,
		dbgen.ListQueryParametersByQueryParams{
			OrgSlug:        orgSlug,
			DataSourceSlug: dsSlug,
			QuerySlug:      querySlug,
		},
	)
	if err != nil {
		return nil, nil, err
	}

	params := make([]*QueryParameter, len(paramRows))
	for i, row := range paramRows {
		params[i] = toQueryParameter(row)
	}

	return query, params, nil
}

// ListByDataSource lists all queries in a data source with pagination.
func (r *repository) ListByDataSource(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	req model.ListRequest,
	includeDeleted bool,
) ([]*Query, error) {
	queries := dbgen.New(tx)

	cursorSlug, cursorTimestamp, cursorText, err := postgres.BuildCursorValues(
		req.Cursor,
		req.SortBy,
	)
	if err != nil {
		return nil, xerror.Wrapf("build query cursor values: %w", err)
	}

	rows, err := queries.ListQueriesByDataSource(ctx, dbgen.ListQueriesByDataSourceParams{
		OrgSlug:         orgSlug,
		DataSourceSlug:  dsSlug,
		CursorSlug:      cursorSlug,
		SortBy:          pgtype.Text{String: req.SortBy, Valid: req.SortBy != ""},
		OrderDir:        pgtype.Text{String: req.Order, Valid: req.Order != ""},
		CursorTimestamp: cursorTimestamp,
		CursorText:      cursorText,
		LimitCount:      int64(req.Limit),
		IncludeDeleted:  includeDeleted,
	})
	if err != nil {
		return nil, err
	}

	result := make([]*Query, len(rows))
	for i, row := range rows {
		result[i] = toQuery(row, dsSlug)
	}

	return result, nil
}

// Update updates an existing query and replaces its parameters.
func (r *repository) Update(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
	params *UpdateParams,
) (*Query, []*QueryParameter, error) {
	queries := dbgen.New(tx)

	// Update the query
	sqlcParams := dbgen.UpdateQueryParams{
		OrgSlug:                  orgSlug,
		DataSourceSlug:           dsSlug,
		QuerySlug:                querySlug,
		Name:                     params.Name,
		Description:              postgres.ToNullableText(params.Description),
		Data:                     params.Data,
		ExpectedInputContract:    params.ExpectedInputContract,
		ExpectedInputContractRef: params.ExpectedInputContractRef,
		OutputContract:           params.OutputContract,
		OutputContractRef:        params.OutputContractRef,
	}
	_, err := queries.UpdateQuery(ctx, sqlcParams)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrQueryNotFound.With(err)
		}
		return nil, nil, err
	}

	// Delete existing parameters
	err = queries.DeleteQueryParametersByQuery(ctx, dbgen.DeleteQueryParametersByQueryParams{
		OrgSlug:        orgSlug,
		DataSourceSlug: dsSlug,
		QuerySlug:      querySlug,
	})
	if err != nil {
		return nil, nil, err
	}

	// Create new parameters
	queryParams := make([]*QueryParameter, 0, len(params.Parameters))
	for _, param := range params.Parameters {
		sqlcParam := dbgen.CreateQueryParameterParams{
			Slug:           param.Slug,
			OrgSlug:        orgSlug,
			DataSourceSlug: dsSlug,
			QuerySlug:      querySlug,
			Name:           param.Name,
			DataType:       param.DataType,
			IsRequired:     param.IsRequired,
			DefaultValue:   postgres.ToNullableText(param.DefaultValue),
			Description:    postgres.ToNullableText(param.Description),
		}
		paramRow, err := queries.CreateQueryParameter(ctx, sqlcParam)
		if err != nil {
			return nil, nil, err
		}
		queryParams = append(queryParams, toQueryParameter(paramRow))
	}

	// Fetch the updated query
	query, _, err := r.FindBySlug(ctx, tx, orgSlug, dsSlug, querySlug)
	if err != nil {
		return nil, nil, err
	}

	return query, queryParams, nil
}

// Delete soft-deletes a query by slug.
func (r *repository) Delete(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
) error {
	queries := dbgen.New(tx)

	result, err := queries.DeleteQuery(ctx, dbgen.DeleteQueryParams{
		OrgSlug:        orgSlug,
		DataSourceSlug: dsSlug,
		QuerySlug:      querySlug,
		DeleteReason:   pgtype.Text{Valid: false},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if result.RowsAffected() == 0 {
		return ErrQueryNotFound
	}

	return nil
}

// Restore clears the deletion tombstone for a query by slug.
func (r *repository) Restore(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
) error {
	queries := dbgen.New(tx)

	result, err := queries.RestoreQuery(ctx, dbgen.RestoreQueryParams{
		OrgSlug:        orgSlug,
		DataSourceSlug: dsSlug,
		QuerySlug:      querySlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if result.RowsAffected() == 0 {
		return ErrQueryNotFound
	}

	return nil
}

// Purge permanently deletes a soft-deleted query by slug.
func (r *repository) Purge(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
) error {
	queries := dbgen.New(tx)

	result, err := queries.PurgeQuery(ctx, dbgen.PurgeQueryParams{
		OrgSlug:        postgres.SlugToText(orgSlug),
		DataSourceSlug: postgres.SlugToText(dsSlug),
		QuerySlug:      postgres.SlugToText(querySlug),
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if result.RowsAffected() > 0 {
		return nil
	}

	deletedAt, err := queries.FindQueryDeletionState(ctx, dbgen.FindQueryDeletionStateParams{
		OrgSlug:        orgSlug,
		DataSourceSlug: dsSlug,
		QuerySlug:      querySlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return ErrQueryNotFound.With(err)
		}
		return xerror.Unexpected().With(err)
	}
	if !deletedAt.Valid {
		return ErrQueryNotDeleted
	}

	return ErrQueryNotFound
}

// CreateExecution records a query execution.
func (r *repository) CreateExecution(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
	params *CreateExecutionParams,
) (*QueryExecution, error) {
	queries := dbgen.New(tx)

	sqlcParams := dbgen.CreateQueryExecutionParams{
		Slug:           params.Slug,
		OrgSlug:        orgSlug,
		DataSourceSlug: dsSlug,
		QuerySlug:      querySlug,
		Status:         params.Status,
		Result:         params.Result,
	}
	row, err := queries.CreateQueryExecution(ctx, sqlcParams)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrQueryNotFound.With(err)
		}
		return nil, err
	}

	execution := toQueryExecution(row)
	return execution, nil
}

// ListExecutions lists query executions for a query with pagination.
func (r *repository) ListExecutions(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
	req model.ListRequest,
) ([]*QueryExecution, error) {
	queries := dbgen.New(tx)

	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(req.Cursor, req.SortBy)
	if err != nil {
		return nil, xerror.Wrapf("build query execution cursor values: %w", err)
	}

	rows, err := queries.ListQueryExecutionsByQuery(ctx, dbgen.ListQueryExecutionsByQueryParams{
		OrgSlug:         orgSlug,
		DataSourceSlug:  dsSlug,
		QuerySlug:       querySlug,
		CursorSlug:      cursorSlug,
		OrderDir:        pgtype.Text{String: req.Order, Valid: req.Order != ""},
		CursorTimestamp: cursorTimestamp,
		LimitCount:      int64(req.Limit),
	})
	if err != nil {
		return nil, err
	}

	executions := make([]*QueryExecution, len(rows))
	for i, row := range rows {
		executions[i] = toQueryExecution(row)
	}

	return executions, nil
}

// GetExecution fetches a query execution by slug.
func (r *repository) GetExecution(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug, executionSlug slug.Slug,
) (*QueryExecution, error) {
	queries := dbgen.New(tx)

	row, err := queries.FindQueryExecutionBySlug(ctx, dbgen.FindQueryExecutionBySlugParams{
		OrgSlug:        orgSlug,
		DataSourceSlug: dsSlug,
		QuerySlug:      querySlug,
		ExecutionSlug:  executionSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrQueryNotFound.With(err)
		}
		return nil, err
	}

	return toQueryExecution(row), nil
}

// FindLatestSuccessfulExecution returns the latest recorded successful execution in an organization.
func (r *repository) FindLatestSuccessfulExecution(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
) (*QueryExecutionIdentity, error) {
	row, err := dbgen.New(tx).FindLatestSuccessfulQueryExecutionByOrganization(ctx, orgSlug)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // No retained successful execution is an expected summary result.
		}
		return nil, err
	}

	return &QueryExecutionIdentity{
		ExecutionSlug:  row.ExecutionSlug,
		DataSourceSlug: row.DataSourceSlug,
		QuerySlug:      row.QuerySlug,
		ExecutedAt:     row.ExecutedAt.Time,
	}, nil
}
