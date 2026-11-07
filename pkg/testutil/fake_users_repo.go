package testutil

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/users"
)

// FakeUsersRepo is a test double for users.Repository.
// Set each Fn field to control the behavior for that method.
// Unconfigured methods return an explicit sentinel error to surface unexpected calls.
type FakeUsersRepo struct {
	IsSlugReservedFn                func(ctx context.Context, tx postgres.DBTX, slug slug.Slug) (bool, error)
	CreateUserFn                    func(ctx context.Context, tx postgres.DBTX, p *users.CreateParams) (*users.UserPublic, error)
	CreateOIDCUserFn                func(ctx context.Context, tx postgres.DBTX, p *users.CreateOIDCUserParams) (*users.UserPublic, error)
	FindBySlugFn                    func(ctx context.Context, tx postgres.DBTX, slug slug.Slug) (*users.UserPublic, error)
	FindByEmailWithPasswordFn       func(ctx context.Context, tx postgres.DBTX, email string) (*users.UserWithPassword, error)
	FindBySlugWithPasswordFn        func(ctx context.Context, tx postgres.DBTX, slug slug.Slug) (*users.UserWithPassword, error)
	FindByOIDCProviderFn            func(ctx context.Context, tx postgres.DBTX, provider, providerID string) (*users.UserPublic, error)
	FindByEmailFn                   func(ctx context.Context, tx postgres.DBTX, email string) (*users.UserPublic, error)
	CreateEmailVerificationTokenFn  func(ctx context.Context, tx postgres.DBTX, params users.CreateEmailVerificationTokenParams) error
	CreatePasswordResetTokenFn      func(ctx context.Context, tx postgres.DBTX, params users.CreatePasswordResetTokenParams) error
	CreateMFAChallengeFn            func(ctx context.Context, tx postgres.DBTX, userSlug slug.Slug, challengeTokenHash, codeHash string, expiresAt time.Time) error
	FindMFAChallengeByTokenFn       func(ctx context.Context, tx postgres.DBTX, challengeTokenHash string) (*users.MFAChallenge, error)
	IncrementMFAChallengeAttemptsFn func(ctx context.Context, tx postgres.DBTX, id int) error
	ConsumeMFAChallengeFn           func(ctx context.Context, tx postgres.DBTX, id int) error
	FindByEmailVerificationTokenFn  func(ctx context.Context, tx postgres.DBTX, tokenHash string) (*users.UserPublic, error)
	FindByPasswordResetTokenFn      func(ctx context.Context, tx postgres.DBTX, tokenHash string) (*users.UserPublic, error)
	MarkEmailAsVerifiedFn           func(ctx context.Context, tx postgres.DBTX, slug slug.Slug, verifiedAt time.Time) error
	UpdatePasswordFn                func(ctx context.Context, tx postgres.DBTX, slug slug.Slug, hashedPassword string) error
	UpdateProfileFn                 func(ctx context.Context, tx postgres.DBTX, params *users.UpdateProfileParams) error
}

var errFakeUsersRepoFnNotSet = xerror.Unexpected().Detail("fake users repo function is not set")

func (f *FakeUsersRepo) CreateUser(
	ctx context.Context,
	tx postgres.DBTX,
	p *users.CreateParams,
) (*users.UserPublic, error) {
	if f.CreateUserFn != nil {
		return f.CreateUserFn(ctx, tx, p)
	}
	return nil, errFakeUsersRepoFnNotSet
}

func (f *FakeUsersRepo) CreateOIDCUser(
	ctx context.Context,
	tx postgres.DBTX,
	p *users.CreateOIDCUserParams,
) (*users.UserPublic, error) {
	if f.CreateOIDCUserFn != nil {
		return f.CreateOIDCUserFn(ctx, tx, p)
	}
	return nil, errFakeUsersRepoFnNotSet
}

func (f *FakeUsersRepo) IsSlugReserved(ctx context.Context, tx postgres.DBTX, s slug.Slug) (bool, error) {
	if f.IsSlugReservedFn != nil {
		return f.IsSlugReservedFn(ctx, tx, s)
	}
	return false, errFakeUsersRepoFnNotSet
}

func (f *FakeUsersRepo) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	slug slug.Slug,
) (*users.UserPublic, error) {
	if f.FindBySlugFn != nil {
		return f.FindBySlugFn(ctx, tx, slug)
	}
	return nil, errFakeUsersRepoFnNotSet
}

func (f *FakeUsersRepo) FindByEmailWithPassword(
	ctx context.Context,
	tx postgres.DBTX,
	email string,
) (*users.UserWithPassword, error) {
	if f.FindByEmailWithPasswordFn != nil {
		return f.FindByEmailWithPasswordFn(ctx, tx, email)
	}
	return nil, errFakeUsersRepoFnNotSet
}

func (f *FakeUsersRepo) FindBySlugWithPassword(
	ctx context.Context,
	tx postgres.DBTX,
	slug slug.Slug,
) (*users.UserWithPassword, error) {
	if f.FindBySlugWithPasswordFn != nil {
		return f.FindBySlugWithPasswordFn(ctx, tx, slug)
	}
	return nil, errFakeUsersRepoFnNotSet
}

func (f *FakeUsersRepo) FindByOIDCProvider(
	ctx context.Context,
	tx postgres.DBTX,
	provider, providerID string,
) (*users.UserPublic, error) {
	if f.FindByOIDCProviderFn != nil {
		return f.FindByOIDCProviderFn(ctx, tx, provider, providerID)
	}
	return nil, errFakeUsersRepoFnNotSet
}

func (f *FakeUsersRepo) FindByEmail(
	ctx context.Context,
	tx postgres.DBTX,
	email string,
) (*users.UserPublic, error) {
	if f.FindByEmailFn != nil {
		return f.FindByEmailFn(ctx, tx, email)
	}
	return nil, errFakeUsersRepoFnNotSet
}

func (f *FakeUsersRepo) CreateEmailVerificationToken(
	ctx context.Context,
	tx postgres.DBTX,
	params users.CreateEmailVerificationTokenParams,
) error {
	if f.CreateEmailVerificationTokenFn != nil {
		return f.CreateEmailVerificationTokenFn(ctx, tx, params)
	}
	return nil
}

func (f *FakeUsersRepo) CreatePasswordResetToken(
	ctx context.Context,
	tx postgres.DBTX,
	params users.CreatePasswordResetTokenParams,
) error {
	if f.CreatePasswordResetTokenFn != nil {
		return f.CreatePasswordResetTokenFn(ctx, tx, params)
	}
	return nil
}

func (f *FakeUsersRepo) CreateMFAChallenge(
	ctx context.Context,
	tx postgres.DBTX,
	userSlug slug.Slug,
	challengeTokenHash string,
	codeHash string,
	expiresAt time.Time,
) error {
	if f.CreateMFAChallengeFn != nil {
		return f.CreateMFAChallengeFn(ctx, tx, userSlug, challengeTokenHash, codeHash, expiresAt)
	}
	return nil
}

func (f *FakeUsersRepo) FindMFAChallengeByToken(
	ctx context.Context,
	tx postgres.DBTX,
	challengeTokenHash string,
) (*users.MFAChallenge, error) {
	if f.FindMFAChallengeByTokenFn != nil {
		return f.FindMFAChallengeByTokenFn(ctx, tx, challengeTokenHash)
	}
	return nil, errFakeUsersRepoFnNotSet
}

func (f *FakeUsersRepo) IncrementMFAChallengeAttempts(
	ctx context.Context,
	tx postgres.DBTX,
	id int,
) error {
	if f.IncrementMFAChallengeAttemptsFn != nil {
		return f.IncrementMFAChallengeAttemptsFn(ctx, tx, id)
	}
	return nil
}

func (f *FakeUsersRepo) ConsumeMFAChallenge(ctx context.Context, tx postgres.DBTX, id int) error {
	if f.ConsumeMFAChallengeFn != nil {
		return f.ConsumeMFAChallengeFn(ctx, tx, id)
	}
	return nil
}

func (f *FakeUsersRepo) FindByEmailVerificationToken(
	ctx context.Context,
	tx postgres.DBTX,
	tokenHash string,
) (*users.UserPublic, error) {
	if f.FindByEmailVerificationTokenFn != nil {
		return f.FindByEmailVerificationTokenFn(ctx, tx, tokenHash)
	}
	return nil, errFakeUsersRepoFnNotSet
}

func (f *FakeUsersRepo) FindByPasswordResetToken(
	ctx context.Context,
	tx postgres.DBTX,
	tokenHash string,
) (*users.UserPublic, error) {
	if f.FindByPasswordResetTokenFn != nil {
		return f.FindByPasswordResetTokenFn(ctx, tx, tokenHash)
	}
	return nil, errFakeUsersRepoFnNotSet
}

func (f *FakeUsersRepo) MarkEmailAsVerified(
	ctx context.Context,
	tx postgres.DBTX,
	slug slug.Slug,
	verifiedAt time.Time,
) error {
	if f.MarkEmailAsVerifiedFn != nil {
		return f.MarkEmailAsVerifiedFn(ctx, tx, slug, verifiedAt)
	}
	return nil
}

func (f *FakeUsersRepo) UpdatePassword(
	ctx context.Context,
	tx postgres.DBTX,
	slug slug.Slug,
	hashedPassword string,
) error {
	if f.UpdatePasswordFn != nil {
		return f.UpdatePasswordFn(ctx, tx, slug, hashedPassword)
	}
	return nil
}

func (f *FakeUsersRepo) UpdateProfile(
	ctx context.Context,
	tx postgres.DBTX,
	params *users.UpdateProfileParams,
) error {
	if f.UpdateProfileFn != nil {
		return f.UpdateProfileFn(ctx, tx, params)
	}
	return nil
}

// StubUserPublic returns a minimal UserPublic for use in tests.
func StubUserPublic(userSlug slug.Slug, fullName, email string, role model.Role) *users.UserPublic {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &users.UserPublic{
		Slug:      userSlug,
		Email:     email,
		FullName:  fullName,
		Role:      role,
		Locale:    "en",
		CreatedAt: now,
		UpdatedAt: now,
	}
}
