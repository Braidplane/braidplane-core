package users

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	CreateUser(ctx context.Context, tx postgres.DBTX, user *CreateParams) (*UserPublic, error)
	CreateOIDCUser(
		ctx context.Context,
		tx postgres.DBTX,
		params *CreateOIDCUserParams,
	) (*UserPublic, error)
	FindBySlug(ctx context.Context, tx postgres.DBTX, slug slug.Slug) (*UserPublic, error)
	IsSlugReserved(ctx context.Context, tx postgres.DBTX, slug slug.Slug) (bool, error)
	FindByEmailWithPassword(
		ctx context.Context,
		tx postgres.DBTX,
		email string,
	) (*UserWithPassword, error)
	FindBySlugWithPassword(
		ctx context.Context,
		tx postgres.DBTX,
		slug slug.Slug,
	) (*UserWithPassword, error)
	FindByOIDCProvider(
		ctx context.Context,
		tx postgres.DBTX,
		provider, providerID string,
	) (*UserPublic, error)
	FindByEmail(ctx context.Context, tx postgres.DBTX, email string) (*UserPublic, error)
	CreateEmailVerificationToken(
		ctx context.Context,
		tx postgres.DBTX,
		params CreateEmailVerificationTokenParams,
	) error
	CreatePasswordResetToken(
		ctx context.Context,
		tx postgres.DBTX,
		params CreatePasswordResetTokenParams,
	) error
	CreateMFAChallenge(
		ctx context.Context,
		tx postgres.DBTX,
		userSlug slug.Slug,
		challengeTokenHash string,
		codeHash string,
		expiresAt time.Time,
	) error
	FindMFAChallengeByToken(
		ctx context.Context,
		tx postgres.DBTX,
		challengeTokenHash string,
	) (*MFAChallenge, error)
	IncrementMFAChallengeAttempts(ctx context.Context, tx postgres.DBTX, id int) error
	ConsumeMFAChallenge(ctx context.Context, tx postgres.DBTX, id int) error
	FindByEmailVerificationToken(
		ctx context.Context,
		tx postgres.DBTX,
		tokenHash string,
	) (*UserPublic, error)
	FindByPasswordResetToken(
		ctx context.Context,
		tx postgres.DBTX,
		tokenHash string,
	) (*UserPublic, error)
	MarkEmailAsVerified(
		ctx context.Context,
		tx postgres.DBTX,
		slug slug.Slug,
		verifiedAt time.Time,
	) error
	UpdatePassword(
		ctx context.Context,
		tx postgres.DBTX,
		slug slug.Slug,
		hashedPassword string,
	) error
	UpdateProfile(ctx context.Context, tx postgres.DBTX, params *UpdateProfileParams) error
}
