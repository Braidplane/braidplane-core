package queries

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

// Repository provides access to query data.
type Repository interface {
	FindSnapshot(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*OperationSnapshot, error)
	// Create inserts a new query and returns it with its parameters.
	Create(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		dsSlug slug.Slug,
		params *CreateParams,
	) (*Query, []*QueryParameter, error)

	// FindBySlug fetches a query by its slug within a data source with its parameters.
	FindBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug, querySlug slug.Slug,
	) (*Query, []*QueryParameter, error)

	// ListByDataSource lists all queries in a data source with pagination.
	ListByDataSource(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug slug.Slug,
		req model.ListRequest,
		includeDeleted bool,
	) ([]*Query, error)

	// Update updates an existing query by slug and replaces its parameters.
	Update(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug, querySlug slug.Slug,
		params *UpdateParams,
	) (*Query, []*QueryParameter, error)

	// Delete soft-deletes a query by slug.
	Delete(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) error

	// Restore clears the deletion tombstone for a query by slug.
	Restore(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) error

	// Purge permanently deletes a soft-deleted query by slug.
	Purge(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) error

	// CreateExecution records a query execution.
	CreateExecution(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug, querySlug slug.Slug,
		params *CreateExecutionParams,
	) (*QueryExecution, error)

	// ListExecutions lists query executions for a query with pagination.
	ListExecutions(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug, querySlug slug.Slug,
		req model.ListRequest,
	) ([]*QueryExecution, error)

	// GetExecution fetches a query execution by slug.
	GetExecution(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, dsSlug, querySlug, executionSlug slug.Slug,
	) (*QueryExecution, error)

	// FindLatestSuccessfulExecution returns the latest recorded successful execution in an organization.
	FindLatestSuccessfulExecution(context.Context, postgres.DBTX, slug.Slug) (*QueryExecutionIdentity, error)
}

// New creates a new query repository.
func New() Repository {
	return &repository{}
}
