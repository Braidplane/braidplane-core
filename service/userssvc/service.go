package userssvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/abusecontrols"
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
	passwordCreated func(context.Context, postgres.DBTX, *model.User) (string, error)
	oidcCreated     func(context.Context, postgres.DBTX, *model.User) error
	mfaEmailLimiter abusecontrols.Repository
	mfaEmailBudget  httpsecurity.AbuseLimit
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

// WithCreated preserves the legacy account-created callback for password and
// OIDC accounts. New integrations should use the creation-specific options.
func WithCreated(created func(context.Context, postgres.DBTX, *model.User) error) Option {
	return func(s *usersService) {
		if created == nil {
			s.passwordCreated, s.oidcCreated = nil, nil
			return
		}
		s.passwordCreated = func(ctx context.Context, tx postgres.DBTX, user *model.User) (string, error) {
			return "", created(ctx, tx, user)
		}
		s.oidcCreated = created
	}
}

// WithPasswordCreated runs after a password account is created and snapshots an
// optional document for its initial verification email.
func WithPasswordCreated(created func(context.Context, postgres.DBTX, *model.User) (string, error)) Option {
	return func(s *usersService) { s.passwordCreated = created }
}

// WithOIDCCreated runs after an OIDC account is created.
func WithOIDCCreated(created func(context.Context, postgres.DBTX, *model.User) error) Option {
	return func(s *usersService) { s.oidcCreated = created }
}

// WithMFAEmailBudget limits challenge delivery after password verification.
func WithMFAEmailBudget(repo abusecontrols.Repository, limit int, window time.Duration) Option {
	return func(s *usersService) {
		s.mfaEmailLimiter = repo
		s.mfaEmailBudget = httpsecurity.AbuseLimit{Scope: "auth.mfa_email.recipient", Limit: limit, Window: window}
	}
}
