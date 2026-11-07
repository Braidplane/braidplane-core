package orgsvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

// Service defines the organization business logic.
type Service interface {
	DeleteOrganization(ctx context.Context, orgSlug slug.Slug) error
	CreateOrganization(
		ctx context.Context,
		userSlug slug.Slug,
		actor model.EventActor,
		newOrg model.NewOrganization,
	) (*model.Organization, error)
	ListOrganizations(
		ctx context.Context,
		userSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.OrganizationWithRole], error)
	GetOrganization(ctx context.Context, orgSlug slug.Slug) (*model.Organization, error)
	GetRetentionPolicy(ctx context.Context, orgSlug slug.Slug) (model.OrganizationRetentionPolicy, error)
	UpdateRetentionPolicy(
		ctx context.Context,
		orgSlug slug.Slug,
		actor model.EventActor,
		policy model.OrganizationRetentionPolicy,
	) (model.OrganizationRetentionPolicy, error)
	UpdateOrganization(
		ctx context.Context,
		orgSlug slug.Slug,
		actor model.EventActor,
		update model.UpdateOrganization,
	) (*model.Organization, error)
}

type service struct {
	txm                 postgres.TransactionManager
	orgsRepo            organizations.Repository
	environmentsService DefaultEnvironmentsService
	membersService      memberssvc.Service
	rolesService        rolessvc.Service
	eventProducer       workerproducer.EventProducer
	admission           admissionsvc.Service
	beforeDelete        func(context.Context, postgres.DBTX, slug.Slug) error
}

type DefaultEnvironmentsService interface {
	CreateDefaultEnvironments(ctx context.Context, orgSlug slug.Slug) error
}

// New creates a new organization service.
func New(
	txm postgres.TransactionManager,
	orgsRepo organizations.Repository,
	environmentsService DefaultEnvironmentsService,
	membersService memberssvc.Service,
	rolesService rolessvc.Service,
	eventProducer workerproducer.EventProducer,
	admission admissionsvc.Service,
	options ...Option,
) Service {
	s := &service{
		txm:                 txm,
		orgsRepo:            orgsRepo,
		environmentsService: environmentsService,
		membersService:      membersService,
		rolesService:        rolesService,
		eventProducer:       eventProducer,
		admission:           admission,
	}
	for _, option := range options {
		option(s)
	}
	return s
}

type Option func(*service)

func WithBeforeDelete(beforeDelete func(context.Context, postgres.DBTX, slug.Slug) error) Option {
	return func(s *service) { s.beforeDelete = beforeDelete }
}
