package userssvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/users"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

type UsersService interface {
	CreateUser(ctx context.Context, newUser model.NewUser) (*model.User, error)
	BeginLogin(ctx context.Context, email, password string) (*model.SessionLoginResult, error)
	LoginUser(ctx context.Context, email, password string) (*model.User, error)
	GetUserBySlug(ctx context.Context, userSlug slug.Slug) (*model.User, error)
	LoginOIDCUser(ctx context.Context, oidcUser *model.OIDCUserInfo) (*model.User, error)
	LoginOrCreateOIDCUser(ctx context.Context, oidcUser *model.OIDCUserInfo) (*model.User, error)
	VerifyEmail(ctx context.Context, token string) error
	RequestPasswordReset(ctx context.Context, email string) error
	ResetPassword(ctx context.Context, token, password string) error
	ChangePassword(ctx context.Context, userSlug slug.Slug, currentPassword, newPassword string) error
	ResendVerificationEmail(ctx context.Context, email string) error
	UpdateProfile(
		ctx context.Context,
		userSlug slug.Slug,
		update model.UpdateUserProfile,
	) (*model.User, error)
	VerifySessionMFA(ctx context.Context, challengeToken, code string) (*model.User, error)
}

type usersService struct {
	txm             postgres.TransactionManager
	usersRepository users.Repository
	hasher          crypto.Hasher
	emailProducer   workerproducer.EmailProducer
	eventProducer   workerproducer.EventProducer
	frontendURL     string
	created         func(context.Context, postgres.DBTX, *model.User) error
}

// New creates a new users service with the given querier and users repository.
func New(
	txm postgres.TransactionManager,
	hasher crypto.Hasher,
	usersRepository users.Repository,
	emailProducer workerproducer.EmailProducer,
	eventProducer workerproducer.EventProducer,
	frontendURL string,
	options ...Option,
) UsersService {
	s := &usersService{
		txm:             txm,
		hasher:          hasher,
		usersRepository: usersRepository,
		emailProducer:   emailProducer,
		eventProducer:   eventProducer,
		frontendURL:     frontendURL,
	}
	for _, option := range options {
		option(s)
	}
	return s
}

// Option configures optional behavior inside the account transaction.
type Option func(*usersService)

func WithCreated(created func(context.Context, postgres.DBTX, *model.User) error) Option {
	return func(s *usersService) { s.created = created }
}
