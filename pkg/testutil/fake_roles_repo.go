package testutil

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/roles"
)

// FakeRolesRepo is a test double for roles.Repository.
type FakeRolesRepo struct {
	CreateFn                  func(ctx context.Context, tx postgres.DBTX, params *roles.CreateParams) (*roles.Role, error)
	EnsureDefaultFn           func(ctx context.Context, tx postgres.DBTX, params *roles.CreateParams) (*roles.Role, error)
	FindBySlugFn              func(ctx context.Context, tx postgres.DBTX, orgSlug, roleSlug slug.Slug) (*roles.Role, error)
	ListByOrganizationFn      func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, req model.ListRequest, includeDeleted, onlyDeleted bool) ([]*roles.Role, error)
	GetUserPermissionsInOrgFn func(ctx context.Context, tx postgres.DBTX, orgSlug, userSlug slug.Slug) ([]*roles.UserPermission, error)
	UpdateFn                  func(ctx context.Context, tx postgres.DBTX, params *roles.UpdateParams) error
	DeleteFn                  func(ctx context.Context, tx postgres.DBTX, params *roles.DeleteParams) error
	RestoreFn                 func(ctx context.Context, tx postgres.DBTX, params *roles.RestoreParams) error
}

func (f *FakeRolesRepo) Create(
	ctx context.Context,
	tx postgres.DBTX,
	p *roles.CreateParams,
) (*roles.Role, error) {
	return f.CreateFn(ctx, tx, p)
}

func (f *FakeRolesRepo) EnsureDefault(
	ctx context.Context,
	tx postgres.DBTX,
	p *roles.CreateParams,
) (*roles.Role, error) {
	if f.EnsureDefaultFn != nil {
		return f.EnsureDefaultFn(ctx, tx, p)
	}
	return f.Create(ctx, tx, p)
}

func (f *FakeRolesRepo) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, roleSlug slug.Slug,
) (*roles.Role, error) {
	if f.FindBySlugFn != nil {
		return f.FindBySlugFn(ctx, tx, orgSlug, roleSlug)
	}
	return nil, roles.ErrNotFound
}

func (f *FakeRolesRepo) ListByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
	includeDeleted bool,
	onlyDeleted bool,
) ([]*roles.Role, error) {
	return f.ListByOrganizationFn(ctx, tx, orgSlug, req, includeDeleted, onlyDeleted)
}

func (f *FakeRolesRepo) GetUserPermissionsInOrg(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, userSlug slug.Slug,
) ([]*roles.UserPermission, error) {
	if f.GetUserPermissionsInOrgFn != nil {
		return f.GetUserPermissionsInOrgFn(ctx, tx, orgSlug, userSlug)
	}
	return []*roles.UserPermission{}, nil
}

func (f *FakeRolesRepo) Update(ctx context.Context, tx postgres.DBTX, p *roles.UpdateParams) error {
	return f.UpdateFn(ctx, tx, p)
}

func (f *FakeRolesRepo) Delete(ctx context.Context, tx postgres.DBTX, p *roles.DeleteParams) error {
	return f.DeleteFn(ctx, tx, p)
}

func (f *FakeRolesRepo) Restore(ctx context.Context, tx postgres.DBTX, p *roles.RestoreParams) error {
	return f.RestoreFn(ctx, tx, p)
}
