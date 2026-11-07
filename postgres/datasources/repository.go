package datasources

import (
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

// Repository provides access to data source data.
type Repository interface {
	// Create inserts a new data source and returns it.
	Create(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		params *CreateParams,
	) (*DataSource, error)

	// FindBySlug fetches a data source by its slug within an organization.
	FindBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
	) (*DataSource, error)

	// IsSlugReserved reports whether an active or soft-deleted data source reserves the slug.
	IsSlugReserved(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (bool, error)

	// ListByOrg lists all data sources in an organization.
	ListByOrg(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		req model.ListRequest,
	) ([]*DataSource, error)

	// Update updates an existing data source by slug.
	Update(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
		params *UpdateParams,
	) (*DataSource, error)

	// UpdateCredentials updates credentials for an existing data source by slug.
	UpdateCredentials(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
		params *UpdateCredentialsParams,
	) (*DataSource, error)

	// MarkTestSuccess marks a data source as active and stores the introspected schema.
	MarkTestSuccess(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
		params *MarkTestSuccessParams,
	) (*DataSource, error)

	// MarkTestFailure marks a data source as inactive and records the test error.
	MarkTestFailure(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
		expectedRevision int64,
		errMsg string,
		schema json.RawMessage,
	) (*DataSource, error)

	// Delete soft-deletes a data source by slug.
	Delete(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) error

	// DataSourceHasProjectBackendReferences reports whether any project uses this data source as its pipeline backend.
	DataSourceHasProjectBackendReferences(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (bool, error)

	// LockDataSourceForUpdate locks an active data source row for update within the current transaction.
	LockDataSourceForUpdate(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) error

	// Restore clears the deletion tombstone for a data source by slug.
	Restore(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) error

	// SetReadOnly sets the read_only flag for a data source.
	SetReadOnly(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
		readOnly bool,
	) error
}

// New creates a new data source repository.
func New() Repository {
	return &repository{}
}
