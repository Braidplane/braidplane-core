package testutil

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
)

var errFakeOrgsRepoFnNotSet = xerror.Unexpected().Detail(
	"fake organizations repo function is not set",
)

// FakeOrgsRepo is a test double for organizations.Repository.
type FakeOrgsRepo struct {
	DeletionBlockedFn     func(context.Context, postgres.DBTX, slug.Slug) (bool, error)
	DeleteFn              func(context.Context, postgres.DBTX, slug.Slug) error
	CreateFn              func(ctx context.Context, tx postgres.DBTX, p *organizations.CreateParams) (*organizations.Organization, error)
	FindBySlugFn          func(ctx context.Context, tx postgres.DBTX, slug slug.Slug) (*organizations.Organization, error)
	FindBySlugForUpdateFn func(ctx context.Context, tx postgres.DBTX, slug slug.Slug) (*organizations.Organization, error)
	UpdateFn              func(ctx context.Context, tx postgres.DBTX, p *organizations.UpdateParams) error
	UpdateSettingsFn      func(ctx context.Context, tx postgres.DBTX, p *organizations.UpdateSettingsParams) error
	ListByUserFn          func(ctx context.Context, tx postgres.DBTX, userSlug slug.Slug, req model.ListRequest) ([]*organizations.OrganizationWithRole, error)
}

func (f *FakeOrgsRepo) DeletionBlocked(ctx context.Context, tx postgres.DBTX, s slug.Slug) (bool, error) {
	return f.DeletionBlockedFn(ctx, tx, s)
}

func (f *FakeOrgsRepo) Delete(ctx context.Context, tx postgres.DBTX, s slug.Slug) error {
	return f.DeleteFn(ctx, tx, s)
}

func (f *FakeOrgsRepo) Create(
	ctx context.Context,
	tx postgres.DBTX,
	p *organizations.CreateParams,
) (*organizations.Organization, error) {
	if f.CreateFn != nil {
		return f.CreateFn(ctx, tx, p)
	}
	return nil, errFakeOrgsRepoFnNotSet
}

func (f *FakeOrgsRepo) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	s slug.Slug,
) (*organizations.Organization, error) {
	if f.FindBySlugFn != nil {
		return f.FindBySlugFn(ctx, tx, s)
	}
	return nil, errFakeOrgsRepoFnNotSet
}

func (f *FakeOrgsRepo) FindBySlugForUpdate(
	ctx context.Context,
	tx postgres.DBTX,
	s slug.Slug,
) (*organizations.Organization, error) {
	if f.FindBySlugForUpdateFn != nil {
		return f.FindBySlugForUpdateFn(ctx, tx, s)
	}
	return nil, errFakeOrgsRepoFnNotSet
}

func (f *FakeOrgsRepo) Update(
	ctx context.Context,
	tx postgres.DBTX,
	p *organizations.UpdateParams,
) error {
	if f.UpdateFn != nil {
		return f.UpdateFn(ctx, tx, p)
	}
	return nil
}

func (f *FakeOrgsRepo) ListByUser(
	ctx context.Context,
	tx postgres.DBTX,
	userSlug slug.Slug,
	req model.ListRequest,
) ([]*organizations.OrganizationWithRole, error) {
	if f.ListByUserFn != nil {
		return f.ListByUserFn(ctx, tx, userSlug, req)
	}
	return nil, errFakeOrgsRepoFnNotSet
}

func (f *FakeOrgsRepo) UpdateSettings(
	ctx context.Context,
	tx postgres.DBTX,
	p *organizations.UpdateSettingsParams,
) error {
	if f.UpdateSettingsFn != nil {
		return f.UpdateSettingsFn(ctx, tx, p)
	}
	return nil
}
