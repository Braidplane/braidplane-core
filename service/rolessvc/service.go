package rolessvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	"github.com/Braidplane/braidplane-core/postgres/roles"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

type Service interface {
	CreateRoleWithPermissions(
		ctx context.Context,
		orgSlug slug.Slug,
		actor model.EventActor,
		newRole model.NewRole,
		permissions []model.RolePermission,
	) (*model.OrgRole, error)
	CreateDefaultRoles(ctx context.Context, orgSlug slug.Slug) error
	UpdateRoleWithPermissions(
		ctx context.Context,
		orgSlug, roleSlug slug.Slug,
		actor model.EventActor,
		update model.UpdateRole,
		permissions []model.RolePermission,
	) error
	DeleteRole(ctx context.Context, orgSlug, roleSlug slug.Slug, actor model.EventActor) error
	RestoreRole(ctx context.Context, orgSlug, roleSlug slug.Slug, actor model.EventActor) error
	GetRole(ctx context.Context, orgSlug, roleSlug slug.Slug) (*model.OrgRole, error)
	ListRoles(
		ctx context.Context,
		orgSlug slug.Slug,
		req model.ListRequest,
		includeDeleted bool,
		onlyDeleted bool,
	) (model.ListResponse[model.OrgRole], error)
	GetUserPermissionsInOrg(
		ctx context.Context,
		orgSlug, userSlug slug.Slug,
	) ([]model.RolePermission, error)
	ListRolePermissions(
		ctx context.Context,
		orgSlug, roleSlug slug.Slug,
	) ([]model.RolePermission, error)
}

type service struct {
	txm             postgres.TransactionManager
	rolesRepo       roles.Repository
	permissionsRepo permissions.Repository
	eventProducer   workerproducer.EventProducer
}

// New creates a new roles service.
func New(
	txm postgres.TransactionManager,
	rolesRepo roles.Repository,
	permissionsRepo permissions.Repository,
	eventProducer workerproducer.EventProducer,
) Service {
	return &service{
		txm:             txm,
		rolesRepo:       rolesRepo,
		permissionsRepo: permissionsRepo,
		eventProducer:   eventProducer,
	}
}
