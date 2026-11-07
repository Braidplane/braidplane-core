package testutil

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/members"
)

var errFakeMembersRepoFnNotSet = xerror.Unexpected().Detail("fake members repo function is not set")

// FakeMembersRepo is a test double for members.Repository.
type FakeMembersRepo struct {
	CreateFn              func(ctx context.Context, tx postgres.DBTX, p *members.CreateParams) (*members.Member, error)
	FindFn                func(ctx context.Context, tx postgres.DBTX, orgSlug, userSlug slug.Slug) (*members.Member, error)
	FindByEmailFn         func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, email string) (*members.Member, error)
	CountByOrganizationFn func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) (int64, error)
	CountByRoleFn         func(ctx context.Context, tx postgres.DBTX, orgSlug, roleSlug slug.Slug) (int64, error)
	ListByOrganizationFn  func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, req model.ListRequest) ([]*members.Member, error)
	UpdateFn              func(ctx context.Context, tx postgres.DBTX, orgSlug, userSlug, roleSlug slug.Slug) error
	RemoveFn              func(ctx context.Context, tx postgres.DBTX, orgSlug, userSlug slug.Slug) error
}

func (f *FakeMembersRepo) Create(
	ctx context.Context,
	tx postgres.DBTX,
	p *members.CreateParams,
) (*members.Member, error) {
	if f.CreateFn != nil {
		return f.CreateFn(ctx, tx, p)
	}
	return nil, errFakeMembersRepoFnNotSet
}

func (f *FakeMembersRepo) Find(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, userSlug slug.Slug,
) (*members.Member, error) {
	if f.FindFn != nil {
		return f.FindFn(ctx, tx, orgSlug, userSlug)
	}
	return nil, errFakeMembersRepoFnNotSet
}

func (f *FakeMembersRepo) FindByEmail(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	email string,
) (*members.Member, error) {
	if f.FindByEmailFn != nil {
		return f.FindByEmailFn(ctx, tx, orgSlug, email)
	}
	return nil, errFakeMembersRepoFnNotSet
}

func (f *FakeMembersRepo) CountByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
) (int64, error) {
	if f.CountByOrganizationFn != nil {
		return f.CountByOrganizationFn(ctx, tx, orgSlug)
	}
	return 0, errFakeMembersRepoFnNotSet
}

func (f *FakeMembersRepo) CountByRole(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, roleSlug slug.Slug,
) (int64, error) {
	if f.CountByRoleFn != nil {
		return f.CountByRoleFn(ctx, tx, orgSlug, roleSlug)
	}
	return 0, errFakeMembersRepoFnNotSet
}

func (f *FakeMembersRepo) ListByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*members.Member, error) {
	if f.ListByOrganizationFn != nil {
		return f.ListByOrganizationFn(ctx, tx, orgSlug, req)
	}
	return nil, errFakeMembersRepoFnNotSet
}

func (f *FakeMembersRepo) Update(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, userSlug, roleSlug slug.Slug,
) error {
	if f.UpdateFn != nil {
		return f.UpdateFn(ctx, tx, orgSlug, userSlug, roleSlug)
	}
	return nil
}

func (f *FakeMembersRepo) Remove(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, userSlug slug.Slug,
) error {
	if f.RemoveFn != nil {
		return f.RemoveFn(ctx, tx, orgSlug, userSlug)
	}
	return nil
}
