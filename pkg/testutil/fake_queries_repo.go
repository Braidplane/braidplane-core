package testutil

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	qrepo "github.com/Braidplane/braidplane-core/postgres/queries"
)

// FakeQueryRepository is a test double for queries.Repository.
// By default all methods succeed without side effects.
type FakeQueryRepository struct {
	FindSnapshotFn                  func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*qrepo.OperationSnapshot, error)
	CreateFn                        func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *qrepo.CreateParams) (*qrepo.Query, []*qrepo.QueryParameter, error)
	FindBySlugFn                    func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error)
	ListByDataSourceFn              func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, req model.ListRequest, includeDeleted bool) ([]*qrepo.Query, error)
	UpdateFn                        func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug, params *qrepo.UpdateParams) (*qrepo.Query, []*qrepo.QueryParameter, error)
	DeleteFn                        func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) error
	RestoreFn                       func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) error
	PurgeFn                         func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug) error
	CreateExecutionFn               func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug, params *qrepo.CreateExecutionParams) (*qrepo.QueryExecution, error)
	ListExecutionsFn                func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug slug.Slug, req model.ListRequest) ([]*qrepo.QueryExecution, error)
	GetExecutionFn                  func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug, querySlug, executionSlug slug.Slug) (*qrepo.QueryExecution, error)
	FindLatestSuccessfulExecutionFn func(context.Context, postgres.DBTX, slug.Slug) (*qrepo.QueryExecutionIdentity, error)
}

func (f *FakeQueryRepository) FindSnapshot(ctx context.Context, db postgres.DBTX, org, source, operation slug.Slug) (*qrepo.OperationSnapshot, error) {
	if f.FindSnapshotFn != nil {
		return f.FindSnapshotFn(ctx, db, org, source, operation)
	}
	return &qrepo.OperationSnapshot{}, nil
}

func (f *FakeQueryRepository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *qrepo.CreateParams,
) (*qrepo.Query, []*qrepo.QueryParameter, error) {
	if f.CreateFn != nil {
		return f.CreateFn(ctx, tx, orgSlug, dsSlug, params)
	}
	return &qrepo.Query{Slug: params.Slug, Name: params.Name}, nil, nil
}

func (f *FakeQueryRepository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
) (*qrepo.Query, []*qrepo.QueryParameter, error) {
	if f.FindBySlugFn != nil {
		return f.FindBySlugFn(ctx, tx, orgSlug, dsSlug, querySlug)
	}
	return &qrepo.Query{Slug: querySlug}, nil, nil
}

func (f *FakeQueryRepository) ListByDataSource(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	req model.ListRequest,
	includeDeleted bool,
) ([]*qrepo.Query, error) {
	if f.ListByDataSourceFn != nil {
		return f.ListByDataSourceFn(ctx, tx, orgSlug, dsSlug, req, includeDeleted)
	}
	return []*qrepo.Query{}, nil
}

func (f *FakeQueryRepository) Update(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
	params *qrepo.UpdateParams,
) (*qrepo.Query, []*qrepo.QueryParameter, error) {
	if f.UpdateFn != nil {
		return f.UpdateFn(ctx, tx, orgSlug, dsSlug, querySlug, params)
	}
	return &qrepo.Query{Slug: querySlug}, nil, nil
}

func (f *FakeQueryRepository) Delete(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
) error {
	if f.DeleteFn != nil {
		return f.DeleteFn(ctx, tx, orgSlug, dsSlug, querySlug)
	}
	return nil
}

func (f *FakeQueryRepository) Restore(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
) error {
	if f.RestoreFn != nil {
		return f.RestoreFn(ctx, tx, orgSlug, dsSlug, querySlug)
	}
	return nil
}

func (f *FakeQueryRepository) Purge(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
) error {
	if f.PurgeFn != nil {
		return f.PurgeFn(ctx, tx, orgSlug, dsSlug, querySlug)
	}
	return nil
}

func (f *FakeQueryRepository) CreateExecution(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
	params *qrepo.CreateExecutionParams,
) (*qrepo.QueryExecution, error) {
	if f.CreateExecutionFn != nil {
		return f.CreateExecutionFn(ctx, tx, orgSlug, dsSlug, querySlug, params)
	}
	return &qrepo.QueryExecution{Slug: params.Slug}, nil
}

func (f *FakeQueryRepository) ListExecutions(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug slug.Slug,
	req model.ListRequest,
) ([]*qrepo.QueryExecution, error) {
	if f.ListExecutionsFn != nil {
		return f.ListExecutionsFn(ctx, tx, orgSlug, dsSlug, querySlug, req)
	}
	return []*qrepo.QueryExecution{}, nil
}

func (f *FakeQueryRepository) GetExecution(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug, querySlug, executionSlug slug.Slug,
) (*qrepo.QueryExecution, error) {
	if f.GetExecutionFn != nil {
		return f.GetExecutionFn(ctx, tx, orgSlug, dsSlug, querySlug, executionSlug)
	}
	return &qrepo.QueryExecution{Slug: executionSlug}, nil
}

func (f *FakeQueryRepository) FindLatestSuccessfulExecution(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
) (*qrepo.QueryExecutionIdentity, error) {
	if f.FindLatestSuccessfulExecutionFn != nil {
		return f.FindLatestSuccessfulExecutionFn(ctx, tx, orgSlug)
	}
	return nil, nil //nolint:nilnil // The default fake has no recorded successful execution.
}
