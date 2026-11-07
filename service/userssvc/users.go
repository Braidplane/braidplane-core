package userssvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"

	mailchecker "github.com/FGRibreau/mailchecker/v6/platform/go"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	slugpkg "github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/users"
)

type passwordResetRequest struct {
	Email string `validate:"required,email"`
}

type passwordResetComplete struct {
	Token    string `validate:"required"`
	Password string `validate:"required,min=8,max=1024"`
}

type passwordChangeRequest struct {
	CurrentPassword string `validate:"required,min=8,max=1024"`
	NewPassword     string `validate:"required,min=8,max=1024"`
}

type resendVerificationRequest struct {
	Email string `validate:"required,email"`
}

const (
	tokenLength      = 32
	tokenExpiryHours = 24
	mfaCodeDigits    = 6
	mfaExpiryMinutes = 10
	mfaMaxAttempts   = 5
)

var phoneNumberPattern = regexp.MustCompile(`^\+?[0-9 .()\-]{3,32}$`)

// CreateUser creates a new user with the given email, password, and role.
func (s *usersService) CreateUser(ctx context.Context, newUser model.NewUser) (*model.User, error) {
	if err := validate.Default().Struct(newUser); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}
	if mailchecker.IsBlacklisted(strings.ToLower(newUser.Email)) {
		return nil, ErrInvalidInput.Detail("signup email domain is disposable")
	}

	hashedPassword, err := s.hasher.Hash(newUser.Password)
	if err != nil {
		return nil, xerror.Wrapf("hash password: %w", err)
	}

	userSlug, err := newAccountSlug()
	if err != nil {
		return nil, err
	}

	var ret *model.User
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		// Pre-check if slug exists and append suffix once if it does
		if reserved, err := s.usersRepository.IsSlugReserved(
			ctx,
			tx,
			userSlug,
		); err == nil && reserved {
			// Slug exists, append suffix once
			nextSlug, genErr := slugpkg.WithRandomSuffix(userSlug, 6)
			if genErr != nil {
				return xerror.Unexpected().With(genErr)
			}
			userSlug = nextSlug
		} else if err != nil {
			return xerror.Wrapf("check user slug collision: %w", err)
		}

		createParams := &users.CreateParams{
			Slug:     userSlug,
			FullName: newUser.FullName,
			Email:    newUser.Email,
			Password: hashedPassword,
			Role:     newUser.Role,
			Locale:   newUser.Locale,
		}

		u, err := s.usersRepository.CreateUser(ctx, tx, createParams)
		if err != nil {
			return xerror.Wrapf("create user: %w", err)
		}

		ret = toDomain(u)
		if s.created != nil {
			if err := s.created(ctx, tx, ret); err != nil {
				return err
			}
		}

		token, tokenHash, err := generateToken()
		if err != nil {
			return xerror.Wrapf("failed to generate token: %w", err)
		}

		expiresAt := time.Now().Add(tokenExpiryHours * time.Hour)

		err = s.usersRepository.CreateEmailVerificationToken(
			ctx,
			tx,
			users.CreateEmailVerificationTokenParams{
				Slug:                            ret.Slug,
				EmailVerificationTokenHash:      tokenHash,
				EmailVerificationTokenExpiresAt: expiresAt,
			},
		)
		if err != nil {
			return xerror.Wrapf("failed to store verification token: %w", err)
		}

		verificationURL := fmt.Sprintf("%s/verify-email", s.frontendURL)

		_, err = s.emailProducer.EnqueueEmailVerification(ctx, tx, ret, verificationURL, token)
		if err != nil {
			return xerror.Wrapf("enqueue verification email: %w", err)
		}

		// Verification proves email ownership only. Membership is granted later
		// by explicit, versioned invitation acceptance in memberssvc.
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

// generateToken generates a random token and its hash
func generateToken() (string, string, error) {
	tokenBytes := make([]byte, tokenLength)
	_, err := rand.Read(tokenBytes)
	if err != nil {
		return "", "", xerror.Unexpected().With(err)
	}

	token := hex.EncodeToString(tokenBytes)

	tokenHash := hashToken(token)

	return token, tokenHash, nil
}

// hashToken creates a SHA-256 hash of the token for storage
func hashToken(token string) string {
	hashBytes := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hashBytes[:])
}

// LoginUser logs in a user with the given email and password.
func (s *usersService) LoginUser(ctx context.Context, email, password string) (*model.User, error) {
	result, err := s.BeginLogin(ctx, email, password)
	if err != nil {
		return nil, err
	}
	if result.MFARequired {
		return nil, ErrMFARequired
	}
	return result.User, nil
}

// BeginLogin validates credentials and either returns a user or starts an email MFA challenge.
func (s *usersService) BeginLogin(
	ctx context.Context,
	email, password string,
) (*model.SessionLoginResult, error) {
	loginData := model.UserLogin{
		Email:    email,
		Password: password,
	}

	if err := validate.Default().Struct(loginData); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	var ret *model.SessionLoginResult

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		u, err := s.usersRepository.FindByEmailWithPassword(ctx, tx, email)
		if err != nil {
			if xerror.Is(err, users.ErrUserNotFound) {
				return ErrInvalidUsernameOrPassword
			}
			return xerror.Wrapf("find user with password: %w", err)
		}

		match, err := s.hasher.CompareHash(password, u.Password)
		if err != nil {
			return xerror.Wrapf("compare hash: %w", err)
		}

		if !match {
			return ErrInvalidUsernameOrPassword
		}

		userPublic, err := s.usersRepository.FindBySlug(ctx, tx, u.Slug)
		if err != nil {
			return xerror.Wrapf("find user by slug: %w", err)
		}

		if userPublic.VerifiedAt.IsZero() {
			return ErrEmailNotVerified
		}

		user := toDomain(userPublic)
		if !u.MFAEmailEnabled {
			ret = &model.SessionLoginResult{User: user}
			return nil
		}

		challengeToken, challengeTokenHash, err := generateToken()
		if err != nil {
			return xerror.Wrapf("generate mfa challenge token: %w", err)
		}

		code, err := generateMFACode()
		if err != nil {
			return xerror.Wrapf("generate mfa code: %w", err)
		}
		codeHash, err := s.hasher.Hash(code)
		if err != nil {
			return xerror.Wrapf("hash mfa code: %w", err)
		}

		expiresAt := time.Now().Add(mfaExpiryMinutes * time.Minute)
		err = s.usersRepository.CreateMFAChallenge(
			ctx,
			tx,
			user.Slug,
			challengeTokenHash,
			codeHash,
			expiresAt,
		)
		if err != nil {
			return xerror.Wrapf("create mfa challenge: %w", err)
		}

		_, err = s.emailProducer.EnqueueSessionMFA(ctx, tx, user, code)
		if err != nil {
			return xerror.Wrapf("enqueue mfa email: %w", err)
		}

		ret = &model.SessionLoginResult{
			MFARequired: true,
			MFAChallenge: &model.MFAChallenge{
				ChallengeToken: challengeToken,
				ExpiresAt:      expiresAt,
			},
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

// generateMFACode creates a six-digit email MFA code for out-of-band delivery.
func generateMFACode() (string, error) {
	codeSpace := big.NewInt(1_000_000)
	n, err := rand.Int(rand.Reader, codeSpace)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}

	code := fmt.Sprintf("%0*d", mfaCodeDigits, n.Int64())
	return code, nil
}

func (s *usersService) GetUserBySlug(
	ctx context.Context,
	userSlug slugpkg.Slug,
) (*model.User, error) {
	var ret *model.User

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		u, err := s.usersRepository.FindBySlug(ctx, tx, userSlug)
		if err != nil {
			return xerror.Wrapf("find user by slug: %w", err)
		}

		ret = toDomain(u)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

// LoginOIDCUser signs in an existing user already linked to the OIDC provider.
func (s *usersService) LoginOIDCUser(
	ctx context.Context,
	oidcUser *model.OIDCUserInfo,
) (*model.User, error) {
	if err := validateOIDCUserInfo(oidcUser); err != nil {
		return nil, err
	}

	var ret *model.User
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		existingUser, err := s.findOIDCProviderUser(ctx, tx, oidcUser)
		if err != nil {
			return err
		}

		ret = toDomain(existingUser)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *usersService) LoginOrCreateOIDCUser(
	ctx context.Context,
	oidcUser *model.OIDCUserInfo,
) (*model.User, error) {
	if err := validateOIDCUserInfo(oidcUser); err != nil {
		return nil, err
	}

	var ret *model.User

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		// Existing linked OIDC users always sign in without touching account creation.
		existingUser, err := s.findOIDCProviderUser(ctx, tx, oidcUser)
		if err == nil {
			ret = toDomain(existingUser)
			return nil
		}
		if !xerror.Is(err, ErrUserNotFound) {
			return err
		}

		// User doesn't exist, check if email is already used
		if oidcUser.Email != "" {
			_, err := s.usersRepository.FindByEmail(ctx, tx, oidcUser.Email)
			if err == nil {
				return ErrEmailAlreadyExists
			}
			if !xerror.Is(err, users.ErrUserNotFound) {
				return xerror.Wrapf("find user by email: %w", err)
			}
		}

		userSlug, err := newAccountSlug()
		if err != nil {
			return err
		}

		// Pre-check if slug exists and append suffix once if it does
		slugToUse := userSlug
		if reserved, err := s.usersRepository.IsSlugReserved(
			ctx,
			tx,
			userSlug,
		); err == nil && reserved {
			// Slug exists, append suffix once
			nextSlug, genErr := slugpkg.WithRandomSuffix(userSlug, 6)
			if genErr != nil {
				return xerror.Unexpected().With(genErr)
			}
			slugToUse = nextSlug
		} else if err != nil {
			return xerror.Wrapf("find user by slug: %w", err)
		}

		createParams := &users.CreateOIDCUserParams{
			Slug:           slugToUse,
			Email:          oidcUser.Email,
			FullName:       oidcUser.Name,
			OIDCProvider:   oidcUser.Provider,
			OIDCProviderID: oidcUser.ProviderID,
			AvatarURL:      oidcUser.Picture,
			Role:           model.RoleUser,
			Locale:         oidcUser.Locale,
		}

		u, err := s.usersRepository.CreateOIDCUser(ctx, tx, createParams)
		if err != nil {
			return xerror.Wrapf("create oidc user: %w", err)
		}

		ret = toDomain(u)
		if s.created != nil {
			if err := s.created(ctx, tx, ret); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

// validateOIDCUserInfo checks the minimum OIDC profile required by this service.
func validateOIDCUserInfo(oidcUser *model.OIDCUserInfo) error {
	if oidcUser == nil || oidcUser.Name == "" || oidcUser.Provider == "" || oidcUser.ProviderID == "" {
		return ErrInvalidInput
	}
	return nil
}

// findOIDCProviderUser returns the existing local user linked to the provider account.
func (s *usersService) findOIDCProviderUser(
	ctx context.Context,
	tx postgres.DBTX,
	oidcUser *model.OIDCUserInfo,
) (*users.UserPublic, error) {
	if err := validateOIDCUserInfo(oidcUser); err != nil {
		return nil, err
	}

	existingUser, err := s.usersRepository.FindByOIDCProvider(
		ctx,
		tx,
		oidcUser.Provider,
		oidcUser.ProviderID,
	)
	if err == nil {
		return existingUser, nil
	}
	if xerror.Is(err, users.ErrUserNotFound) {
		return nil, ErrUserNotFound
	}
	return nil, xerror.Wrapf("find user by oidc provider: %w", err)
}

// VerifyEmail verifies the email using the provided token
func (s *usersService) VerifyEmail(ctx context.Context, token string) error {
	tokenHash := hashToken(token)

	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		user, err := s.usersRepository.FindByEmailVerificationToken(ctx, tx, tokenHash)
		if err != nil {
			if xerror.Is(err, users.ErrUserNotFound) {
				return ErrInvalidToken
			}
			return xerror.Wrapf("find user by token: %w", err)
		}

		verifyTime := time.Now()
		if !user.VerifiedAt.IsZero() {
			return nil
		}

		err = s.usersRepository.MarkEmailAsVerified(ctx, tx, user.Slug, verifyTime)
		if err != nil {
			return xerror.Wrapf("failed to mark email as verified: %w", err)
		}
		return nil
	})
}

func (s *usersService) RequestPasswordReset(ctx context.Context, email string) error {
	if err := validate.Default().Struct(passwordResetRequest{Email: email}); err != nil {
		return xerror.ValidationFailed().With(err)
	}

	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		user, err := s.usersRepository.FindByEmail(ctx, tx, email)
		if err != nil {
			if xerror.Is(err, users.ErrUserNotFound) {
				return nil
			}
			return xerror.Wrapf("find user by email: %w", err)
		}

		token, tokenHash, err := generateToken()
		if err != nil {
			return xerror.Wrapf("generate token: %w", err)
		}

		expiresAt := time.Now().Add(tokenExpiryHours * time.Hour)
		err = s.usersRepository.CreatePasswordResetToken(
			ctx,
			tx,
			users.CreatePasswordResetTokenParams{
				Slug:                        user.Slug,
				PasswordResetTokenHash:      tokenHash,
				PasswordResetTokenExpiresAt: expiresAt,
			},
		)
		if err != nil {
			return xerror.Wrapf("store password reset token: %w", err)
		}

		resetURL := fmt.Sprintf("%s/auth/reset-password", s.frontendURL)
		_, err = s.emailProducer.EnqueuePasswordReset(ctx, tx, toDomain(user), resetURL, token)
		if err != nil {
			return xerror.Wrapf("enqueue password reset email: %w", err)
		}

		return nil
	})
}

func (s *usersService) ResetPassword(ctx context.Context, token, password string) error {
	if err := validate.Default().Struct(
		passwordResetComplete{Token: token, Password: password},
	); err != nil {
		return xerror.ValidationFailed().With(err)
	}

	tokenHash := hashToken(token)

	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		user, err := s.usersRepository.FindByPasswordResetToken(ctx, tx, tokenHash)
		if err != nil {
			if xerror.Is(err, users.ErrUserNotFound) {
				return ErrInvalidToken
			}
			return xerror.Wrapf("find user by password reset token: %w", err)
		}

		hashedPassword, err := s.hasher.Hash(password)
		if err != nil {
			return xerror.Wrapf("hash password: %w", err)
		}

		err = s.usersRepository.UpdatePassword(ctx, tx, user.Slug, hashedPassword)
		if err != nil {
			return xerror.Wrapf("update password: %w", err)
		}

		return nil
	})
}

// ChangePassword updates a local-password account after verifying the current password hash.
func (s *usersService) ChangePassword(
	ctx context.Context,
	userSlug slugpkg.Slug,
	currentPassword,
	newPassword string,
) error {
	if err := validate.Default().Struct(passwordChangeRequest{
		CurrentPassword: currentPassword,
		NewPassword:     newPassword,
	}); err != nil {
		return xerror.ValidationFailed().With(err)
	}

	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		user, err := s.usersRepository.FindBySlugWithPassword(ctx, tx, userSlug)
		if err != nil {
			if xerror.Is(err, users.ErrUserNotFound) {
				return ErrLocalPasswordUnavailable
			}
			return xerror.Wrapf("find user password by slug: %w", err)
		}

		match, err := s.hasher.CompareHash(currentPassword, user.Password)
		if err != nil {
			return xerror.Wrapf("compare current password hash: %w", err)
		}
		if !match {
			return ErrInvalidUsernameOrPassword
		}

		hashedPassword, err := s.hasher.Hash(newPassword)
		if err != nil {
			return xerror.Wrapf("hash new password: %w", err)
		}

		if err := s.usersRepository.UpdatePassword(ctx, tx, userSlug, hashedPassword); err != nil {
			return xerror.Wrapf("update password: %w", err)
		}
		return nil
	})
}

func (s *usersService) ResendVerificationEmail(ctx context.Context, email string) error {
	if err := validate.Default().Struct(resendVerificationRequest{Email: email}); err != nil {
		return xerror.ValidationFailed().With(err)
	}

	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		user, err := s.usersRepository.FindByEmail(ctx, tx, email)
		if err != nil {
			if xerror.Is(err, users.ErrUserNotFound) {
				return nil
			}
			return xerror.Wrapf("find user by email: %w", err)
		}

		if !user.VerifiedAt.IsZero() {
			return nil
		}

		token, tokenHash, err := generateToken()
		if err != nil {
			return xerror.Wrapf("generate token: %w", err)
		}

		expiresAt := time.Now().Add(tokenExpiryHours * time.Hour)
		err = s.usersRepository.CreateEmailVerificationToken(
			ctx,
			tx,
			users.CreateEmailVerificationTokenParams{
				Slug:                            user.Slug,
				EmailVerificationTokenHash:      tokenHash,
				EmailVerificationTokenExpiresAt: expiresAt,
			},
		)
		if err != nil {
			return xerror.Wrapf("store email verification token: %w", err)
		}

		verificationURL := fmt.Sprintf("%s/verify-email", s.frontendURL)
		_, err = s.emailProducer.EnqueueEmailVerification(ctx, tx, toDomain(user), verificationURL, token)
		if err != nil {
			return xerror.Wrapf("enqueue verification email: %w", err)
		}

		return nil
	})
}

// VerifySessionMFA verifies a pending email MFA challenge and returns the authenticated user.
func (s *usersService) VerifySessionMFA(
	ctx context.Context,
	challengeToken string,
	code string,
) (*model.User, error) {
	req := struct {
		ChallengeToken string `validate:"required"`
		Code           string `validate:"required,len=6,numeric"`
	}{ChallengeToken: challengeToken, Code: code}
	if err := validate.Default().Struct(req); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	challengeTokenHash := hashToken(challengeToken)
	var ret *model.User

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		challenge, err := s.usersRepository.FindMFAChallengeByToken(ctx, tx, challengeTokenHash)
		if err != nil {
			if xerror.Is(err, users.ErrUserNotFound) {
				return ErrInvalidToken
			}
			return xerror.Wrapf("find mfa challenge: %w", err)
		}

		if !challenge.ConsumedAt.IsZero() {
			return ErrMFAChallengeConsumed
		}
		if time.Now().After(challenge.ExpiresAt) {
			return ErrMFAChallengeExpired
		}

		// The repository locks the challenge row for this transaction before
		// this check, so parallel wrong-code attempts cannot all pass the cap.
		if challenge.AttemptCount >= mfaMaxAttempts {
			return ErrMFAChallengeLocked
		}
		codeMatches, err := s.hasher.CompareHash(code, challenge.CodeHash)
		if err != nil {
			return xerror.Wrapf("compare mfa code hash: %w", err)
		}
		if !codeMatches {
			if err := s.usersRepository.IncrementMFAChallengeAttempts(ctx, tx, challenge.ID); err != nil {
				return xerror.Wrapf("increment mfa challenge attempts: %w", err)
			}
			// Commit the failed attempt before returning the authentication rejection.
			return nil
		}

		if err := s.usersRepository.ConsumeMFAChallenge(ctx, tx, challenge.ID); err != nil {
			return xerror.Wrapf("consume mfa challenge: %w", err)
		}

		ret = toDomain(challenge.User)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if ret == nil {
		return nil, ErrInvalidToken
	}
	return ret, nil
}

// UpdateProfile updates the user's profile information
func (s *usersService) UpdateProfile(
	ctx context.Context,
	userSlug slugpkg.Slug,
	update model.UpdateUserProfile,
) (*model.User, error) {
	if err := validate.Default().Struct(update); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}
	if err := validateProfileUpdate(update); err != nil {
		return nil, err
	}

	var ret *model.User

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		err := s.usersRepository.UpdateProfile(ctx, tx, &users.UpdateProfileParams{
			Slug:            userSlug,
			FullName:        update.FullName,
			Locale:          update.Locale,
			AvatarURL:       update.AvatarURL,
			PhoneNumber:     update.PhoneNumber,
			JobTitle:        update.JobTitle,
			Timezone:        update.Timezone,
			MFAEmailEnabled: update.MFAEmailEnabled,
		})
		if err != nil {
			return xerror.Wrapf("update profile: %w", err)
		}

		user, err := s.usersRepository.FindBySlug(ctx, tx, userSlug)
		if err != nil {
			return xerror.Wrapf("find user by slug: %w", err)
		}

		ret = toDomain(user)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func validateProfileUpdate(update model.UpdateUserProfile) error {
	if update.AvatarURL != nil && strings.TrimSpace(*update.AvatarURL) != "" {
		parsed, err := url.ParseRequestURI(strings.TrimSpace(*update.AvatarURL))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return xerror.ValidationFailed().Detail("avatar_url must be an absolute URL")
		}
	}

	if update.PhoneNumber != nil && strings.TrimSpace(*update.PhoneNumber) != "" &&
		!phoneNumberPattern.MatchString(strings.TrimSpace(*update.PhoneNumber)) {
		return xerror.ValidationFailed().Detail("phone_number is invalid")
	}

	if update.Timezone != nil && strings.TrimSpace(*update.Timezone) != "" {
		if _, err := time.LoadLocation(strings.TrimSpace(*update.Timezone)); err != nil {
			return xerror.ValidationFailed().Detail("timezone must be an IANA timezone")
		}
	}

	return nil
}

// Account identities must not be reclaimable after hard deletion.
func newAccountSlug() (slugpkg.Slug, error) {
	s, err := slugpkg.WithRandomSuffix("user", 32)
	if err != nil {
		return "", xerror.Wrapf("generate account identity: %w", err)
	}
	return s, nil
}
