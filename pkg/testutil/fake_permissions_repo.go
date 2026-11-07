package testutil

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
)

// FakePermissionsRepo is a test double for permissions.Repository.
type FakePermissionsRepo struct {
	CreateFn               func(ctx context.Context, tx postgres.DBTX, params *permissions.CreateParams) (*permissions.Permission, error)
	FindOrCreateFn         func(ctx context.Context, tx postgres.DBTX, resources, actions []string) ([]*permissions.Permission, error)
	ListAllFn              func(ctx context.Context, tx postgres.DBTX) ([]*permissions.Permission, error)
	ListForRoleFn          func(ctx context.Context, tx postgres.DBTX, orgSlug, roleSlug slug.Slug) ([]*permissions.Permission, error)
	AddToRoleFn            func(ctx context.Context, tx postgres.DBTX, p *permissions.AddToRoleParams) error
	RemoveFromRoleFn       func(ctx context.Context, tx postgres.DBTX, p *permissions.RemoveFromRoleParams) error
	ClearRolePermissionsFn func(ctx context.Context, tx postgres.DBTX, p *permissions.ClearRolePermissionsParams) error
}

func (f *FakePermissionsRepo) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params *permissions.CreateParams,
) (*permissions.Permission, error) {
	return f.CreateFn(ctx, tx, params)
}

func (f *FakePermissionsRepo) FindOrCreate(
	ctx context.Context,
	tx postgres.DBTX,
	resources, actions []string,
) ([]*permissions.Permission, error) {
	return f.FindOrCreateFn(ctx, tx, resources, actions)
}

func (f *FakePermissionsRepo) ListAll(
	ctx context.Context,
	tx postgres.DBTX,
) ([]*permissions.Permission, error) {
	return f.ListAllFn(ctx, tx)
}

func (f *FakePermissionsRepo) ListForRole(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, roleSlug slug.Slug,
) ([]*permissions.Permission, error) {
	return f.ListForRoleFn(ctx, tx, orgSlug, roleSlug)
}

func (f *FakePermissionsRepo) AddToRole(
	ctx context.Context,
	tx postgres.DBTX,
	p *permissions.AddToRoleParams,
) error {
	return f.AddToRoleFn(ctx, tx, p)
}

func (f *FakePermissionsRepo) RemoveFromRole(
	ctx context.Context,
	tx postgres.DBTX,
	p *permissions.RemoveFromRoleParams,
) error {
	return f.RemoveFromRoleFn(ctx, tx, p)
}

func (f *FakePermissionsRepo) ClearRolePermissions(
	ctx context.Context,
	tx postgres.DBTX,
	p *permissions.ClearRolePermissionsParams,
) error {
	return f.ClearRolePermissionsFn(ctx, tx, p)
}
