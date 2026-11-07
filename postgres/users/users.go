package users

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func NewRepository() Repository {
	return &usersRepository{}
}

type usersRepository struct{}

var (
	ErrUserNotFound         = xerror.NotFound(xerror.User)
	ErrEmailAlreadyExists   = xerror.AlreadyExists(xerror.UserEmail)
	ErrSlugAlreadyExists    = xerror.AlreadyExists(xerror.User)
	ErrMFAChallengeConsumed = xerror.InvalidToken().Detail(
		"mfa challenge already consumed",
	)
)

// toUserPublic converts an internal database user to a public user model
func toUserPublic(dbUser dbgen.User) (*UserPublic, error) {
	role, err := model.RoleString(dbUser.Role)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &UserPublic{
		AuthenticationVersion: dbUser.AuthenticationVersion,
		Slug:                  dbUser.Slug,
		Email:                 dbUser.Email,
		FullName:              dbUser.FullName,
		Role:                  role,
		Locale:                dbUser.Locale,
		AvatarURL:             dbUser.AvatarUrl.String,
		PhoneNumber:           dbUser.PhoneNumber.String,
		JobTitle:              dbUser.JobTitle.String,
		Timezone:              dbUser.Timezone.String,
		MFAEmailEnabled:       dbUser.MfaEmailEnabled,
		HasLocalPassword:      dbUser.Password.Valid && dbUser.Password.String != "",
		VerifiedAt:            dbUser.VerifiedAt.Time,
		CreatedAt:             dbUser.CreatedAt.Time,
		UpdatedAt:             dbUser.UpdatedAt.Time,
	}, nil
}

// IsSlugReserved includes tombstones so new accounts cannot inherit retained identities.
func (r *usersRepository) IsSlugReserved(ctx context.Context, tx postgres.DBTX, s slug.Slug) (bool, error) {
	reserved, err := dbgen.New(tx).IsUserSlugReserved(ctx, s)
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return reserved, nil
}

func (r *usersRepository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	s slug.Slug,
) (*UserPublic, error) {
	cur, err := dbgen.New(tx).FindUserBySlug(ctx, s)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toUserPublic(cur)
}

func (r *usersRepository) CreateUser(
	ctx context.Context,
	tx postgres.DBTX,
	user *CreateParams,
) (*UserPublic, error) {
	var userPassword pgtype.Text
	if err := userPassword.Scan(user.Password); err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	userDB := dbgen.CreateUserParams{
		Slug:     user.Slug,
		Email:    user.Email,
		FullName: user.FullName,
		Password: userPassword,
		Role:     user.Role.String(),
		Locale:   user.Locale,
	}

	cur, err := dbgen.New(tx).CreateUser(ctx, userDB)
	if err != nil {
		if postgres.IsUniqueViolation(err, "users_email_key") {
			return nil, ErrEmailAlreadyExists.With(err)
		}
		if postgres.IsUniqueViolation(err, "users_slug_key") {
			return nil, ErrSlugAlreadyExists.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toUserPublic(cur)
}

func (r *usersRepository) CreateOIDCUser(
	ctx context.Context,
	tx postgres.DBTX,
	params *CreateOIDCUserParams,
) (*UserPublic, error) {
	verifiedAt := time.Now()
	userDB := dbgen.CreateOIDCUserParams{
		Slug:           params.Slug,
		Email:          params.Email,
		FullName:       params.FullName,
		OidcProvider:   params.OIDCProvider,
		OidcProviderID: pgtype.Text{String: params.OIDCProviderID, Valid: true},
		AvatarUrl:      pgtype.Text{String: params.AvatarURL, Valid: params.AvatarURL != ""},
		Role:           params.Role.String(),
		VerifiedAt:     pgtype.Timestamp{Time: verifiedAt, Valid: true},
		Locale:         params.Locale,
	}

	cur, err := dbgen.New(tx).CreateOIDCUser(ctx, userDB)
	if err != nil {
		if postgres.IsUniqueViolation(err, "users_email_key") {
			return nil, ErrEmailAlreadyExists.With(err)
		}
		if postgres.IsUniqueViolation(err, "users_slug_key") {
			return nil, ErrSlugAlreadyExists.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toUserPublic(cur)
}

func (r *usersRepository) FindByOIDCProvider(
	ctx context.Context,
	tx postgres.DBTX,
	provider, providerID string,
) (*UserPublic, error) {
	cur, err := dbgen.New(tx).FindUserByOIDCProvider(ctx, dbgen.FindUserByOIDCProviderParams{
		OidcProvider:   provider,
		OidcProviderID: pgtype.Text{String: providerID, Valid: true},
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toUserPublic(cur)
}

func (r *usersRepository) FindByEmail(
	ctx context.Context,
	tx postgres.DBTX,
	email string,
) (*UserPublic, error) {
	cur, err := dbgen.New(tx).FindUserByEmail(ctx, email)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toUserPublic(cur)
}

func (r *usersRepository) FindByEmailWithPassword(
	ctx context.Context,
	tx postgres.DBTX,
	email string,
) (*UserWithPassword, error) {
	cur, err := dbgen.New(tx).FindUserByEmail(ctx, email)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	if !cur.Password.Valid || cur.Password.String == "" {
		return nil, ErrUserNotFound
	}

	return &UserWithPassword{
		Slug:            cur.Slug,
		Email:           cur.Email,
		FullName:        cur.FullName,
		Password:        cur.Password.String,
		MFAEmailEnabled: cur.MfaEmailEnabled,
	}, nil
}

// FindBySlugWithPassword locks and returns the local password hash for current-password verification.
func (r *usersRepository) FindBySlugWithPassword(
	ctx context.Context,
	tx postgres.DBTX,
	s slug.Slug,
) (*UserWithPassword, error) {
	cur, err := dbgen.New(tx).FindUserBySlugForUpdate(ctx, s)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	if !cur.Password.Valid || cur.Password.String == "" {
		return nil, ErrUserNotFound
	}

	return &UserWithPassword{
		Slug:            cur.Slug,
		Email:           cur.Email,
		FullName:        cur.FullName,
		Password:        cur.Password.String,
		MFAEmailEnabled: cur.MfaEmailEnabled,
	}, nil
}

func (r *usersRepository) CreateEmailVerificationToken(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateEmailVerificationTokenParams,
) error {
	err := dbgen.New(
		tx,
	).CreateEmailVerificationToken(
		ctx,
		dbgen.CreateEmailVerificationTokenParams{
			Slug: params.Slug,
			EmailVerificationTokenHash: pgtype.Text{
				String: params.EmailVerificationTokenHash,
				Valid:  true,
			},
			EmailVerificationTokenExpiresAt: pgtype.Timestamp{
				Time:  params.EmailVerificationTokenExpiresAt,
				Valid: true,
			},
		},
	)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *usersRepository) CreatePasswordResetToken(
	ctx context.Context,
	tx postgres.DBTX,
	params CreatePasswordResetTokenParams,
) error {
	err := dbgen.New(tx).CreatePasswordResetToken(ctx, dbgen.CreatePasswordResetTokenParams{
		Slug: params.Slug,
		PasswordResetTokenHash: pgtype.Text{
			String: params.PasswordResetTokenHash,
			Valid:  true,
		},
		PasswordResetTokenExpiresAt: pgtype.Timestamp{
			Time:  params.PasswordResetTokenExpiresAt,
			Valid: true,
		},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *usersRepository) CreateMFAChallenge(
	ctx context.Context,
	tx postgres.DBTX,
	userSlug slug.Slug,
	challengeTokenHash string,
	codeHash string,
	expiresAt time.Time,
) error {
	_, err := dbgen.New(tx).CreateUserMFAChallenge(
		ctx,
		dbgen.CreateUserMFAChallengeParams{
			UserSlug:           userSlug,
			ChallengeTokenHash: challengeTokenHash,
			CodeHash:           codeHash,
			ExpiresAt:          pgtype.Timestamptz{Time: expiresAt, Valid: true},
		},
	)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *usersRepository) FindMFAChallengeByToken(
	ctx context.Context,
	tx postgres.DBTX,
	challengeTokenHash string,
) (*MFAChallenge, error) {
	row, err := dbgen.New(tx).FindUserMFAChallengeByToken(ctx, challengeTokenHash)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	role, err := model.RoleString(row.Role)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &MFAChallenge{
		ID: row.ID,
		User: &UserPublic{
			AuthenticationVersion: row.UserAuthenticationVersion,
			Slug:                  row.UserSlug,
			Email:                 row.Email,
			FullName:              row.FullName,
			Role:                  role,
			Locale:                row.Locale,
			AvatarURL:             row.AvatarUrl.String,
			PhoneNumber:           row.PhoneNumber.String,
			JobTitle:              row.JobTitle.String,
			Timezone:              row.Timezone.String,
			MFAEmailEnabled:       row.MfaEmailEnabled,
			HasLocalPassword:      row.HasLocalPassword.Bool,
			VerifiedAt:            row.VerifiedAt.Time,
			CreatedAt:             row.UserCreatedAt.Time,
			UpdatedAt:             row.UserUpdatedAt.Time,
		},
		CodeHash:     row.CodeHash,
		ExpiresAt:    row.ExpiresAt.Time,
		ConsumedAt:   row.ConsumedAt.Time,
		AttemptCount: int(row.AttemptCount),
	}, nil
}

func (r *usersRepository) IncrementMFAChallengeAttempts(
	ctx context.Context,
	tx postgres.DBTX,
	id int,
) error {
	err := dbgen.New(tx).IncrementUserMFAChallengeAttempts(ctx, id)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *usersRepository) ConsumeMFAChallenge(
	ctx context.Context,
	tx postgres.DBTX,
	id int,
) error {
	_, err := dbgen.New(tx).ConsumeUserMFAChallenge(ctx, id)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return ErrMFAChallengeConsumed.With(err)
		}
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *usersRepository) FindByEmailVerificationToken(
	ctx context.Context,
	tx postgres.DBTX,
	tokenHash string,
) (*UserPublic, error) {
	cur, err := dbgen.New(
		tx,
	).FindUserByEmailVerificationToken(
		ctx,
		pgtype.Text{String: tokenHash, Valid: true},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toUserPublic(cur)
}

func (r *usersRepository) FindByPasswordResetToken(
	ctx context.Context,
	tx postgres.DBTX,
	tokenHash string,
) (*UserPublic, error) {
	cur, err := dbgen.New(tx).FindUserByPasswordResetToken(
		ctx,
		pgtype.Text{String: tokenHash, Valid: true},
	)
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	return toUserPublic(cur)
}

func (r *usersRepository) MarkEmailAsVerified(
	ctx context.Context,
	tx postgres.DBTX,
	s slug.Slug,
	verifiedAt time.Time,
) error {
	err := dbgen.New(tx).MarkAsVerified(ctx, dbgen.MarkAsVerifiedParams{
		Slug:       s,
		VerifiedAt: pgtype.Timestamp{Time: verifiedAt, Valid: true},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *usersRepository) UpdatePassword(
	ctx context.Context,
	tx postgres.DBTX,
	s slug.Slug,
	hashedPassword string,
) error {
	err := dbgen.New(tx).UpdateUserPassword(ctx, dbgen.UpdateUserPasswordParams{
		Slug:     s,
		Password: pgtype.Text{String: hashedPassword, Valid: true},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *usersRepository) UpdateProfile(
	ctx context.Context,
	tx postgres.DBTX,
	params *UpdateProfileParams,
) error {
	err := dbgen.New(tx).UpdateUserProfile(ctx, dbgen.UpdateUserProfileParams{
		Slug:               params.Slug,
		FullNameSet:        params.FullName != nil,
		FullName:           stringValue(params.FullName),
		LocaleSet:          params.Locale != nil,
		Locale:             stringValue(params.Locale),
		AvatarUrlSet:       params.AvatarURL != nil,
		AvatarUrl:          pgtype.Text{String: stringValue(params.AvatarURL), Valid: params.AvatarURL != nil},
		PhoneNumberSet:     params.PhoneNumber != nil,
		PhoneNumber:        pgtype.Text{String: stringValue(params.PhoneNumber), Valid: params.PhoneNumber != nil},
		JobTitleSet:        params.JobTitle != nil,
		JobTitle:           pgtype.Text{String: stringValue(params.JobTitle), Valid: params.JobTitle != nil},
		TimezoneSet:        params.Timezone != nil,
		Timezone:           pgtype.Text{String: stringValue(params.Timezone), Valid: params.Timezone != nil},
		MfaEmailEnabledSet: params.MFAEmailEnabled != nil,
		MfaEmailEnabled:    boolValue(params.MFAEmailEnabled),
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func boolValue(value *bool) bool {
	if value == nil {
		return false
	}
	return *value
}
