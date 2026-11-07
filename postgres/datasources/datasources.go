package datasources

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// repository implements Repository.
type repository struct{}

// Create inserts a new data source.
func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	params *CreateParams,
) (*DataSource, error) {
	queries := dbgen.New(tx)
	sqlcParams := toCreateDataSourceParams(orgSlug, params)

	row, err := queries.CreateDataSource(ctx, sqlcParams)
	if err != nil {
		if postgres.IsUniqueViolation(err, "data_sources_organization_id_slug_key") {
			return nil, ErrSlugTaken.With(err)
		}
		return nil, err
	}

	return toDataSource(row), nil
}

// FindBySlug fetches a data source by slug.
func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (*DataSource, error) {
	queries := dbgen.New(tx)

	row, err := queries.FindDataSourceBySlug(ctx, dbgen.FindDataSourceBySlugParams{
		OrgSlug: orgSlug,
		DsSlug:  dsSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDataSourceNotFound.With(err)
		}
		return nil, err
	}

	return toDataSourceWithCredential(row.DataSource, row.CredentialMetadata)
}

// IsSlugReserved reports whether an active or soft-deleted data source owns the organization-scoped slug.
func (r *repository) IsSlugReserved(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (bool, error) {
	return dbgen.New(tx).IsDataSourceSlugReserved(ctx, dbgen.IsDataSourceSlugReservedParams{
		OrgSlug: orgSlug,
		DsSlug:  dsSlug,
	})
}

// ListByOrg lists all data sources in an organization.
func (r *repository) ListByOrg(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*DataSource, error) {
	queries := dbgen.New(tx)

	cursorSlug, cursorTimestamp, cursorText, err := postgres.BuildCursorValues(
		req.Cursor,
		req.SortBy,
	)
	if err != nil {
		return nil, err
	}

	rows, err := queries.ListDataSourcesByOrg(ctx, dbgen.ListDataSourcesByOrgParams{
		OrgSlug:         orgSlug,
		CursorSlug:      cursorSlug,
		SortBy:          pgtype.Text{String: req.SortBy, Valid: req.SortBy != ""},
		OrderDir:        pgtype.Text{String: req.Order, Valid: req.Order != ""},
		CursorTimestamp: cursorTimestamp,
		CursorText:      cursorText,
		LimitCount:      int64(req.Limit),
	})
	if err != nil {
		return nil, err
	}

	result := make([]*DataSource, len(rows))
	for i, row := range rows {
		result[i], err = toDataSourceWithCredential(row.DataSource, row.CredentialMetadata)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Update updates a data source by slug.
func (r *repository) Update(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *UpdateParams,
) (*DataSource, error) {
	queries := dbgen.New(tx)
	sqlcParams := toUpdateDataSourceParams(orgSlug, dsSlug, params)

	err := queries.UpdateDataSource(ctx, sqlcParams)
	if err != nil {
		return nil, err
	}

	// Fetch the updated record to return it
	return r.FindBySlug(ctx, tx, orgSlug, dsSlug)
}

// UpdateCredentials updates credentials for a data source by slug.
func (r *repository) UpdateCredentials(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *UpdateCredentialsParams,
) (*DataSource, error) {
	queries := dbgen.New(tx)
	sqlcParams := toUpdateDataSourceCredentialsParams(orgSlug, dsSlug, params)

	if err := queries.UpdateDataSourceCredentials(ctx, sqlcParams); err != nil {
		return nil, err
	}

	return r.FindBySlug(ctx, tx, orgSlug, dsSlug)
}

// MarkTestSuccess marks a data source as active and stores the introspected schema.
func (r *repository) MarkTestSuccess(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *MarkTestSuccessParams,
) (*DataSource, error) {
	queries := dbgen.New(tx)

	row, err := queries.MarkDataSourceTestSuccess(ctx, dbgen.MarkDataSourceTestSuccessParams{
		OrgSlug:          orgSlug,
		DsSlug:           dsSlug,
		ExpectedRevision: params.ExpectedRevision,
		TestedAtTs:       postgres.ToTimestamptz(params.TestedAt),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionTestConflict.With(err)
		}
		return nil, err
	}

	return toDataSource(row), nil
}

// MarkTestFailure marks a data source as inactive with an error message.
func (r *repository) MarkTestFailure(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	expectedRevision int64,
	errMsg string,
	schema json.RawMessage,
) (*DataSource, error) {
	queries := dbgen.New(tx)

	row, err := queries.MarkDataSourceTestFailure(ctx, dbgen.MarkDataSourceTestFailureParams{
		OrgSlug:          orgSlug,
		DsSlug:           dsSlug,
		ExpectedRevision: expectedRevision,
		ErrorMessage:     pgtype.Text{String: errMsg, Valid: true},
		TestedAtTs:       pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionTestConflict.With(err)
		}
		return nil, err
	}

	return toDataSource(row), nil
}

// Delete soft-deletes a data source by slug.
func (r *repository) Delete(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) error {
	queries := dbgen.New(tx)
	result, err := queries.DeleteDataSource(ctx, dbgen.DeleteDataSourceParams{
		OrgSlug:      orgSlug,
		DsSlug:       dsSlug,
		DeleteReason: pgtype.Text{Valid: false},
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		hasProjectBackendReferences, refsErr := r.DataSourceHasProjectBackendReferences(
			ctx,
			tx,
			orgSlug,
			dsSlug,
		)
		if refsErr != nil {
			return refsErr
		}
		if hasProjectBackendReferences {
			return ErrDataSourceUsedAsProjectBackend
		}
		return ErrDataSourceNotFound
	}
	return nil
}

// DataSourceHasProjectBackendReferences reports whether any project uses the data source as its pipeline backend.
func (r *repository) DataSourceHasProjectBackendReferences(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (bool, error) {
	queries := dbgen.New(tx)
	return queries.DataSourceHasProjectBackendReferences(
		ctx,
		dbgen.DataSourceHasProjectBackendReferencesParams{
			OrgSlug: orgSlug,
			DsSlug:  dsSlug,
		},
	)
}

// LockDataSourceForUpdate locks an active data source row for update within the current transaction.
func (r *repository) LockDataSourceForUpdate(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) error {
	queries := dbgen.New(tx)
	_, err := queries.LockDataSourceForUpdate(ctx, dbgen.LockDataSourceForUpdateParams{
		OrgSlug: orgSlug,
		DsSlug:  dsSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return ErrDataSourceNotFound.With(err)
		}
		return err
	}
	return nil
}

// Restore clears the deletion tombstone for a data source by slug.
func (r *repository) Restore(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) error {
	queries := dbgen.New(tx)
	result, err := queries.RestoreDataSource(ctx, dbgen.RestoreDataSourceParams{
		OrgSlug: orgSlug,
		DsSlug:  dsSlug,
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrDataSourceNotFound
	}
	return nil
}

// SetReadOnly sets the read_only flag for a data source.
func (r *repository) SetReadOnly(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	readOnly bool,
) error {
	queries := dbgen.New(tx)
	err := queries.SetDataSourceReadOnly(ctx, dbgen.SetDataSourceReadOnlyParams{
		OrgSlug:  orgSlug,
		DsSlug:   dsSlug,
		ReadOnly: readOnly,
	})
	return err
}
