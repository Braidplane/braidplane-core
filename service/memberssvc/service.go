package memberssvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

// Service defines the member business logic.
type Service interface {
	ListMembers(
		ctx context.Context,
		orgSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.Member], error)
	ListMemberInvitations(
		ctx context.Context,
		orgSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.MemberInvitation], error)
	ListMyMemberInvitations(
		ctx context.Context,
		userSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[model.MemberInvitation], error)
	GetMemberInvitation(
		ctx context.Context,
		orgSlug, invitationSlug slug.Slug,
	) (*model.MemberInvitation, error)
	CreateMemberInvitation(
		ctx context.Context,
		orgSlug slug.Slug,
		inviterUserSlug slug.Slug,
		actor model.EventActor,
		newInvitation model.NewMemberInvitation,
	) (*model.MemberInvitation, error)
	ResendMemberInvitation(
		ctx context.Context,
		orgSlug, invitationSlug slug.Slug,
		actor model.EventActor,
	) (*model.MemberInvitation, error)
	RevokeMemberInvitation(
		ctx context.Context,
		orgSlug, invitationSlug slug.Slug,
		actor model.EventActor,
	) (*model.MemberInvitation, error)
	AcceptMemberInvitation(
		ctx context.Context,
		userSlug slug.Slug,
		token string,
		actor model.EventActor,
	) (*model.MemberInvitationAcceptance, error)
	AcceptMyMemberInvitation(
		ctx context.Context,
		userSlug, orgSlug, invitationSlug slug.Slug,
		actor model.EventActor,
	) (*model.MemberInvitationAcceptance, error)
	DeclineMemberInvitation(
		ctx context.Context,
		userSlug slug.Slug,
		token string,
		actor model.EventActor,
	) (*model.MemberInvitation, error)
	DeclineMyMemberInvitation(
		ctx context.Context,
		userSlug, orgSlug, invitationSlug slug.Slug,
		actor model.EventActor,
	) (*model.MemberInvitation, error)
	CreateMember(
		ctx context.Context,
		orgSlug slug.Slug,
		actor model.EventActor,
		newMember model.NewMember,
	) (*model.Member, error)
	CreateBootstrapMember(
		ctx context.Context,
		orgSlug slug.Slug,
		newMember model.NewMember,
	) (*model.Member, error)
	UpdateMember(
		ctx context.Context,
		orgSlug, userSlug slug.Slug,
		actor model.EventActor,
		update model.UpdateMember,
	) (*model.Member, error)
	RemoveMember(ctx context.Context, orgSlug, userSlug slug.Slug, actor model.EventActor) error
}

type service struct {
	txm                   postgres.TransactionManager
	membersRepo           members.Repository
	memberInvitationsRepo memberinvitations.Repository
	orgsRepo              organizations.Repository
	rolesService          rolessvc.Service
	usersService          userssvc.UsersService
	emailProducer         workerproducer.EmailProducer
	eventProducer         workerproducer.EventProducer
	frontendURL           string
	clock                 clock.Clock
	accepted              func(context.Context, postgres.DBTX, *memberinvitations.Invitation, model.User, bool) error
}

// New creates a new members service.
func New(
	txm postgres.TransactionManager,
	membersRepo members.Repository,
	memberInvitationsRepo memberinvitations.Repository,
	orgsRepo organizations.Repository,
	rolesService rolessvc.Service,
	usersService userssvc.UsersService,
	emailProducer workerproducer.EmailProducer,
	eventProducer workerproducer.EventProducer,
	frontendURL string,
	clk clock.Clock,
	options ...Option,
) Service {
	if clk == nil {
		clk = clock.New()
	}

	s := &service{
		txm:                   txm,
		membersRepo:           membersRepo,
		memberInvitationsRepo: memberInvitationsRepo,
		orgsRepo:              orgsRepo,
		rolesService:          rolesService,
		usersService:          usersService,
		emailProducer:         emailProducer,
		eventProducer:         eventProducer,
		frontendURL:           frontendURL,
		clock:                 clk,
	}
	for _, option := range options {
		option(s)
	}
	return s
}

type Option func(*service)

// WithInvitationAccepted runs inside the membership transaction; the final argument identifies retries.
func WithInvitationAccepted(accepted func(context.Context, postgres.DBTX, *memberinvitations.Invitation, model.User, bool) error) Option {
	return func(s *service) { s.accepted = accepted }
}
