package datasources

import (
	"context"
	"database/sql"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// SchemaMetadataRepository provides access to schema metadata persistence.
type SchemaMetadataRepository interface {
	FindByDataSourceSlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
	) (*SchemaMetadata, error)
	DeleteByDataSourceSlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
	) error
	UpsertByDataSourceSlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
		params *UpsertSchemaMetadataParams,
	) (*SchemaMetadata, error)
	FindImportStateByDataSourceSlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
	) (*SchemaImportState, error)
	StartImportByDataSourceSlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
		attemptID string,
		startedAt time.Time,
	) (*SchemaImportState, error)
	FindCurrentImportAttemptByDataSourceSlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
		attemptID string,
	) (*SchemaImportState, error)
	MarkImportReadyByDataSourceSlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
		attemptID string,
		finishedAt time.Time,
	) (*SchemaImportState, error)
	MarkImportFailedByDataSourceSlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
		params *MarkSchemaImportFailedParams,
	) (*SchemaImportState, error)
	ResetImportByDataSourceSlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
	) (*SchemaImportState, error)
}

type schemaMetadataRepository struct{}

func NewSchemaMetadataRepository() SchemaMetadataRepository {
	return &schemaMetadataRepository{}
}

func (r *schemaMetadataRepository) FindByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (*SchemaMetadata, error) {
	row, err := dbgen.New(tx).FindSchemaMetadataByDataSourceSlug(
		ctx,
		dbgen.FindSchemaMetadataByDataSourceSlugParams{
			OrgSlug: orgSlug,
			DsSlug:  dsSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrSchemaMetadataNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toSchemaMetadata(row), nil
}

// DeleteByDataSourceSlug removes cached schema metadata for one organization-scoped data source.
func (r *schemaMetadataRepository) DeleteByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) error {
	if err := dbgen.New(tx).DeleteSchemaMetadataByDataSourceSlug(
		ctx,
		dbgen.DeleteSchemaMetadataByDataSourceSlugParams{
			OrgSlug: orgSlug,
			DsSlug:  dsSlug,
		},
	); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *schemaMetadataRepository) UpsertByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *UpsertSchemaMetadataParams,
) (*SchemaMetadata, error) {
	row, err := dbgen.New(tx).UpsertSchemaMetadataByDataSourceSlug(
		ctx,
		dbgen.UpsertSchemaMetadataByDataSourceSlugParams{
			OrgSlug:    orgSlug,
			DsSlug:     postgres.SlugToText(dsSlug),
			SchemaData: params.SchemaData,
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return toSchemaMetadata(row), nil
}

// FindImportStateByDataSourceSlug returns the durable schema import state for one active data source.
func (r *schemaMetadataRepository) FindImportStateByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (*SchemaImportState, error) {
	row, err := dbgen.New(tx).FindDataSourceSchemaImportState(
		ctx,
		dbgen.FindDataSourceSchemaImportStateParams{
			OrgSlug: orgSlug,
			DsSlug:  dsSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDataSourceNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toSchemaImportState(schemaImportRow{
		DataSourceSlug: row.Slug,
		Status:         row.SchemaImportStatus,
		ErrorCode:      row.SchemaImportErrorCode,
		ErrorMessage:   row.SchemaImportErrorMessage,
		StartedAt:      row.SchemaImportStartedAt,
		FinishedAt:     row.SchemaImportFinishedAt,
		Started:        row.Started,
	})
}

// StartImportByDataSourceSlug marks an import as running unless one is already in flight.
func (r *schemaMetadataRepository) StartImportByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	attemptID string,
	startedAt time.Time,
) (*SchemaImportState, error) {
	row, err := dbgen.New(tx).StartDataSourceSchemaImport(
		ctx,
		dbgen.StartDataSourceSchemaImportParams{
			OrgSlug:   orgSlug,
			DsSlug:    dsSlug,
			AttemptID: postgres.ToText(attemptID),
			StartedAt: postgres.ToTimestamptz(startedAt),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDataSourceNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toSchemaImportState(schemaImportRow{
		DataSourceSlug: row.Slug,
		Status:         row.SchemaImportStatus,
		ErrorCode:      row.SchemaImportErrorCode,
		ErrorMessage:   row.SchemaImportErrorMessage,
		StartedAt:      row.SchemaImportStartedAt,
		FinishedAt:     row.SchemaImportFinishedAt,
		Started:        row.Started,
	})
}

// FindCurrentImportAttemptByDataSourceSlug locks the data source when a queued attempt is still current.
func (r *schemaMetadataRepository) FindCurrentImportAttemptByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	attemptID string,
) (*SchemaImportState, error) {
	row, err := dbgen.New(tx).FindCurrentDataSourceSchemaImportAttempt(
		ctx,
		dbgen.FindCurrentDataSourceSchemaImportAttemptParams{
			OrgSlug:   orgSlug,
			DsSlug:    dsSlug,
			AttemptID: postgres.ToText(attemptID),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrSchemaImportAttemptNotCurrent.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toSchemaImportState(schemaImportRow{
		DataSourceSlug: row.Slug,
		Status:         row.SchemaImportStatus,
		ErrorCode:      row.SchemaImportErrorCode,
		ErrorMessage:   row.SchemaImportErrorMessage,
		StartedAt:      row.SchemaImportStartedAt,
		FinishedAt:     row.SchemaImportFinishedAt,
		Started:        row.Started,
	})
}

// MarkImportReadyByDataSourceSlug records a successful import after cache metadata was updated.
func (r *schemaMetadataRepository) MarkImportReadyByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	attemptID string,
	finishedAt time.Time,
) (*SchemaImportState, error) {
	row, err := dbgen.New(tx).MarkDataSourceSchemaImportReady(
		ctx,
		dbgen.MarkDataSourceSchemaImportReadyParams{
			OrgSlug:    orgSlug,
			DsSlug:     dsSlug,
			AttemptID:  postgres.ToText(attemptID),
			FinishedAt: postgres.ToTimestamptz(finishedAt),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrSchemaImportAttemptNotCurrent.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toSchemaImportState(schemaImportRow{
		DataSourceSlug: row.Slug,
		Status:         row.SchemaImportStatus,
		ErrorCode:      row.SchemaImportErrorCode,
		ErrorMessage:   row.SchemaImportErrorMessage,
		StartedAt:      row.SchemaImportStartedAt,
		FinishedAt:     row.SchemaImportFinishedAt,
		Started:        row.Started,
	})
}

// MarkImportFailedByDataSourceSlug records sanitized terminal diagnostics for a failed import.
func (r *schemaMetadataRepository) MarkImportFailedByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *MarkSchemaImportFailedParams,
) (*SchemaImportState, error) {
	if params == nil {
		return nil, xerror.Unexpected().Detail("schema import failure params are required")
	}

	row, err := dbgen.New(tx).MarkDataSourceSchemaImportFailed(
		ctx,
		dbgen.MarkDataSourceSchemaImportFailedParams{
			OrgSlug:      orgSlug,
			DsSlug:       dsSlug,
			AttemptID:    postgres.ToText(params.AttemptID),
			ErrorCode:    postgres.ToText(params.ErrorCode),
			ErrorMessage: postgres.ToText(params.ErrorMessage),
			FinishedAt:   postgres.ToTimestamptz(params.FinishedAt),
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrSchemaImportAttemptNotCurrent.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toSchemaImportState(schemaImportRow{
		DataSourceSlug: row.Slug,
		Status:         row.SchemaImportStatus,
		ErrorCode:      row.SchemaImportErrorCode,
		ErrorMessage:   row.SchemaImportErrorMessage,
		StartedAt:      row.SchemaImportStartedAt,
		FinishedAt:     row.SchemaImportFinishedAt,
		Started:        row.Started,
	})
}

// ResetImportByDataSourceSlug clears import state when cached metadata is invalidated.
func (r *schemaMetadataRepository) ResetImportByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (*SchemaImportState, error) {
	row, err := dbgen.New(tx).ResetDataSourceSchemaImport(
		ctx,
		dbgen.ResetDataSourceSchemaImportParams{
			OrgSlug: orgSlug,
			DsSlug:  dsSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrDataSourceNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toSchemaImportState(schemaImportRow{
		DataSourceSlug: row.Slug,
		Status:         row.SchemaImportStatus,
		ErrorCode:      row.SchemaImportErrorCode,
		ErrorMessage:   row.SchemaImportErrorMessage,
		StartedAt:      row.SchemaImportStartedAt,
		FinishedAt:     row.SchemaImportFinishedAt,
		Started:        row.Started,
	})
}
