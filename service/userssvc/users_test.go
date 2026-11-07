package userssvc

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/users"
)

var stubTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newTestService(
	repo *testutil.FakeUsersRepo,
	hasher *testutil.FakeHasher,
	emailSvc *testutil.FakeEmailService,
) UsersService {
	if hasher == nil {
		hasher = &testutil.FakeHasher{}
	}
	if emailSvc == nil {
		emailSvc = &testutil.FakeEmailService{}
	}
	return New(
		testutil.FakeTransactionManager{},
		hasher,
		repo,
		newEmailProducerFromService(emailSvc),
		nil,
		"https://app.example.com",
	)
}

func TestCreateUser(t *testing.T) {
	t.Parallel()

	validUser := model.NewUser{
		Email:    "alice@example.com",
		FullName: "Alice Smith",
		Password: "securepass123",
		Role:     model.RoleUser,
		Locale:   "en",
	}

	tests := []struct {
		name            string
		newUser         model.NewUser
		setup           func(repo *testutil.FakeUsersRepo, hasher *testutil.FakeHasher, emailSvc *testutil.FakeEmailService)
		wantSlug        string
		wantErr         error
		wantErrContains string
	}{
		{
			name:    "CreatesUserHashesPasswordGeneratesTokenSendsEmail",
			newUser: validUser,
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher, emailSvc *testutil.FakeEmailService) {
				repo.IsSlugReservedFn = func(_ context.Context, _ postgres.DBTX, userSlug slug.Slug) (bool, error) {
					return false, nil
				}
				repo.CreateUserFn = func(_ context.Context, _ postgres.DBTX, p *users.CreateParams) (*users.UserPublic, error) {
					assert.Regexp(t, `^user-[a-z0-9]{32}$`, p.Slug.String())
					assert.Equal(t, "alice@example.com", p.Email)
					assert.Equal(t, "Alice Smith", p.FullName)
					// Password should be hashed (identity hasher returns input unchanged)
					assert.Equal(t, "securepass123", p.Password)
					return testutil.StubUserPublic(
						must.Slug("alice-smith"),
						"Alice Smith",
						"alice@example.com",
						model.RoleUser,
					), nil
				}
				repo.CreateEmailVerificationTokenFn = func(_ context.Context, _ postgres.DBTX, params users.CreateEmailVerificationTokenParams) error {
					assert.Equal(t, must.Slug("alice-smith"), params.Slug)
					assert.NotEmpty(t, params.EmailVerificationTokenHash)
					assert.False(t, params.EmailVerificationTokenExpiresAt.IsZero())
					return nil
				}
				emailSvc.EmailVerificationFn = func(_ context.Context, user *model.User, link string) error {
					assert.Equal(t, must.Slug("alice-smith"), user.Slug)
					assert.Contains(t, link, "https://app.example.com/verify-email?token=")
					return nil
				}
			},
			wantSlug: "alice-smith",
		},
		{
			name:    "ReturnsErrorWhenEmailAlreadyExists",
			newUser: validUser,
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher, _ *testutil.FakeEmailService) {
				repo.IsSlugReservedFn = func(_ context.Context, _ postgres.DBTX, userSlug slug.Slug) (bool, error) {
					return false, nil
				}
				repo.CreateUserFn = func(_ context.Context, _ postgres.DBTX, _ *users.CreateParams) (*users.UserPublic, error) {
					return nil, users.ErrEmailAlreadyExists
				}
			},
			wantErr: ErrEmailAlreadyExists,
		},
		{
			name:    "FailsClosedWhenSlugLookupReturnsUnexpectedError",
			newUser: validUser,
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher, _ *testutil.FakeEmailService) {
				repo.IsSlugReservedFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (bool, error) {
					return false, xerror.Unexpected().Detail("user slug lookup failed")
				}
				repo.CreateUserFn = func(_ context.Context, _ postgres.DBTX, _ *users.CreateParams) (*users.UserPublic, error) {
					return nil, xerror.Unexpected().Detail("unexpected create user call")
				}
			},
			wantErrContains: "user slug lookup failed",
		},
		{
			name: "ReturnsValidationErrorForMissingEmail",
			newUser: model.NewUser{
				Password: "securepass123",
				FullName: "Alice Smith",
				Role:     model.RoleUser,
			},
			setup:           func(_ *testutil.FakeUsersRepo, _ *testutil.FakeHasher, _ *testutil.FakeEmailService) {},
			wantErrContains: "validation failed",
		},
		{
			name: "ReturnsValidationErrorForInvalidEmailSyntax",
			newUser: model.NewUser{
				Email:    "alice.example.com",
				Password: "securepass123",
				FullName: "Alice Smith",
				Role:     model.RoleUser,
			},
			setup:           func(_ *testutil.FakeUsersRepo, _ *testutil.FakeHasher, _ *testutil.FakeEmailService) {},
			wantErrContains: "validation failed",
		},
		{
			name: "ReturnsValidationErrorForShortPassword",
			newUser: model.NewUser{
				Email:    "alice@example.com",
				FullName: "Alice Smith",
				Password: "short",
				Role:     model.RoleUser,
			},
			setup:           func(_ *testutil.FakeUsersRepo, _ *testutil.FakeHasher, _ *testutil.FakeEmailService) {},
			wantErrContains: "validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeUsersRepo{}
			hasher := &testutil.FakeHasher{}
			emailSvc := &testutil.FakeEmailService{}
			tt.setup(repo, hasher, emailSvc)
			svc := newTestService(repo, hasher, emailSvc)

			got, err := svc.CreateUser(context.Background(), tt.newUser)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, must.Slug(tt.wantSlug), got.Slug)
		})
	}
}

func TestCreateUserRejectsDisposableEmailBeforeSignupSideEffects(t *testing.T) {
	t.Parallel()

	repo := &testutil.FakeUsersRepo{}
	var slugLookups int
	repo.IsSlugReservedFn = func(context.Context, postgres.DBTX, slug.Slug) (bool, error) {
		slugLookups++
		return false, nil
	}
	var createdUsers int
	repo.CreateUserFn = func(context.Context, postgres.DBTX, *users.CreateParams) (*users.UserPublic, error) {
		createdUsers++
		return testutil.StubUserPublic(
			must.Slug("temporary-user"),
			"Temporary User",
			"temporary@10minutemail.com",
			model.RoleUser,
		), nil
	}
	var verificationTokens int
	repo.CreateEmailVerificationTokenFn = func(context.Context, postgres.DBTX, users.CreateEmailVerificationTokenParams) error {
		verificationTokens++
		return nil
	}
	emailProducer := &recordingEmailProducer{}
	svc := New(
		testutil.FakeTransactionManager{},
		&testutil.FakeHasher{},
		repo,
		emailProducer,
		nil,
		"https://app.example.com",
	)

	got, err := svc.CreateUser(context.Background(), model.NewUser{
		Email:    "temporary@10minutemail.com",
		FullName: "Temporary User",
		Password: "securepass123",
		Role:     model.RoleUser,
		Locale:   "en",
	})

	require.Nil(t, got)
	require.ErrorIs(t, err, ErrInvalidInput)
	require.ErrorContains(t, err, "validation failed")
	require.Equal(t, 0, slugLookups)
	require.Equal(t, 0, createdUsers)
	require.Equal(t, 0, verificationTokens)
	require.Equal(t, 0, emailProducer.verificationCalls)
}

func TestLoginUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		email           string
		password        string
		setup           func(repo *testutil.FakeUsersRepo, hasher *testutil.FakeHasher)
		wantSlug        string
		wantErr         error
		wantErrContains string
	}{
		{
			name:     "AuthenticatesWithCorrectCredentials",
			email:    "alice@example.com",
			password: "securepass123",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher) {
				repo.FindByEmailWithPasswordFn = func(_ context.Context, _ postgres.DBTX, email string) (*users.UserWithPassword, error) {
					assert.Equal(t, "alice@example.com", email)
					return &users.UserWithPassword{
						Slug:     "alice",
						Email:    "alice@example.com",
						Password: "securepass123",
					}, nil
				}
				repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, userSlug slug.Slug) (*users.UserPublic, error) {
					assert.Equal(t, "alice", userSlug.String())
					u := testutil.StubUserPublic(
						must.Slug("alice"),
						"Alice Smith",
						"alice@example.com",
						model.RoleUser,
					)
					u.VerifiedAt = stubTime
					return u, nil
				}
			},
			wantSlug: "alice",
		},
		{
			name:     "ReturnsErrorWhenEmailIsNotVerified",
			email:    "alice@example.com",
			password: "securepass123",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher) {
				repo.FindByEmailWithPasswordFn = func(_ context.Context, _ postgres.DBTX, _ string) (*users.UserWithPassword, error) {
					return &users.UserWithPassword{
						Slug:     "alice",
						Email:    "alice@example.com",
						Password: "securepass123",
					}, nil
				}
				repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (*users.UserPublic, error) {
					return testutil.StubUserPublic(
						must.Slug("alice"),
						"Alice Smith",
						"alice@example.com",
						model.RoleUser,
					), nil
				}
			},
			wantErr: ErrEmailNotVerified,
		},
		{
			name:     "ReturnsErrorForWrongPassword",
			email:    "alice@example.com",
			password: "wrongpassword",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher) {
				repo.FindByEmailWithPasswordFn = func(_ context.Context, _ postgres.DBTX, _ string) (*users.UserWithPassword, error) {
					return &users.UserWithPassword{
						Slug:     "alice",
						Email:    "alice@example.com",
						Password: "correct-hash",
					}, nil
				}
			},
			wantErr: ErrInvalidUsernameOrPassword,
		},
		{
			name:     "ReturnsErrorForNonexistentUser",
			email:    "nobody@example.com",
			password: "securepass123",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher) {
				repo.FindByEmailWithPasswordFn = func(_ context.Context, _ postgres.DBTX, _ string) (*users.UserWithPassword, error) {
					return nil, users.ErrUserNotFound
				}
			},
			wantErr: ErrInvalidUsernameOrPassword,
		},
		{
			name:            "ReturnsValidationErrorForInvalidEmail",
			email:           "not-an-email",
			password:        "securepass123",
			setup:           func(_ *testutil.FakeUsersRepo, _ *testutil.FakeHasher) {},
			wantErrContains: "validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeUsersRepo{}
			hasher := &testutil.FakeHasher{}
			tt.setup(repo, hasher)
			svc := newTestService(repo, hasher, nil)

			got, err := svc.LoginUser(context.Background(), tt.email, tt.password)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, must.Slug(tt.wantSlug), got.Slug)
		})
	}
}

func TestBeginLoginRequiresEmailMFAWhenEnabled(t *testing.T) {
	t.Parallel()

	repo := &testutil.FakeUsersRepo{}
	repo.FindByEmailWithPasswordFn = func(_ context.Context, _ postgres.DBTX, email string) (*users.UserWithPassword, error) {
		require.Equal(t, "alice@example.com", email)
		return &users.UserWithPassword{
			Slug:            must.Slug("alice"),
			Email:           "alice@example.com",
			Password:        "securepass123",
			MFAEmailEnabled: true,
		}, nil
	}
	repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, userSlug slug.Slug) (*users.UserPublic, error) {
		require.Equal(t, must.Slug("alice"), userSlug)
		u := testutil.StubUserPublic(
			must.Slug("alice"),
			"Alice Smith",
			"alice@example.com",
			model.RoleUser,
		)
		u.VerifiedAt = stubTime
		u.MFAEmailEnabled = true
		return u, nil
	}
	var challengeTokenHash string
	var codeHash string
	repo.CreateMFAChallengeFn = func(
		_ context.Context,
		_ postgres.DBTX,
		userSlug slug.Slug,
		gotChallengeTokenHash string,
		gotCodeHash string,
		expiresAt time.Time,
	) error {
		require.Equal(t, must.Slug("alice"), userSlug)
		require.NotEmpty(t, gotChallengeTokenHash)
		require.NotEmpty(t, gotCodeHash)
		require.True(t, expiresAt.After(time.Now()))
		challengeTokenHash = gotChallengeTokenHash
		codeHash = gotCodeHash
		return nil
	}
	emailSvc := &testutil.FakeEmailService{}
	var sentCode string
	emailSvc.SessionMFAFn = func(_ context.Context, user *model.User, code string) error {
		require.Equal(t, must.Slug("alice"), user.Slug)
		require.Len(t, code, mfaCodeDigits)
		sentCode = code
		return nil
	}

	hasher := &testutil.FakeHasher{
		HashFn: func(input string) (string, error) {
			return "hashed:" + input, nil
		},
	}
	svc := newTestService(repo, hasher, emailSvc)
	got, err := svc.BeginLogin(context.Background(), "alice@example.com", "securepass123")

	require.NoError(t, err)
	require.True(t, got.MFARequired)
	require.NotNil(t, got.MFAChallenge)
	require.NotEmpty(t, got.MFAChallenge.ChallengeToken)
	require.Empty(t, got.User)
	require.NotEmpty(t, sentCode)
	require.Equal(t, hashToken(got.MFAChallenge.ChallengeToken), challengeTokenHash)
	require.Equal(t, "hashed:"+sentCode, codeHash)
}

func TestVerifySessionMFA(t *testing.T) {
	t.Parallel()

	incrementErr := fmt.Errorf("attempt persistence failed")
	tests := []struct {
		name          string
		challenge     *users.MFAChallenge
		code          string
		wantErr       error
		wantIncrement bool
		wantConsume   bool
		consumeErr    error
		incrementErr  error
	}{
		{
			name: "ConsumesValidChallenge",
			challenge: &users.MFAChallenge{
				ID:        42,
				User:      verifiedLocalPasswordUserPublic(),
				CodeHash:  "123456",
				ExpiresAt: time.Now().Add(time.Minute),
			},
			code:        "123456",
			wantConsume: true,
		},
		{
			name: "RejectsInvalidCodeAndIncrementsAttempts",
			challenge: &users.MFAChallenge{
				ID:        42,
				User:      verifiedUserPublic(),
				CodeHash:  "123456",
				ExpiresAt: time.Now().Add(time.Minute),
			},
			code:          "000000",
			wantErr:       ErrInvalidToken,
			wantIncrement: true,
		},
		{
			name: "RejectsAttemptPersistenceFailure",
			challenge: &users.MFAChallenge{
				ID:        42,
				User:      verifiedUserPublic(),
				CodeHash:  "123456",
				ExpiresAt: time.Now().Add(time.Minute),
			},
			code:          "000000",
			wantErr:       incrementErr,
			wantIncrement: true,
			incrementErr:  incrementErr,
		},
		{
			name: "RejectsExpiredChallenge",
			challenge: &users.MFAChallenge{
				ID:        42,
				User:      verifiedUserPublic(),
				CodeHash:  "123456",
				ExpiresAt: time.Now().Add(-time.Minute),
			},
			code:    "123456",
			wantErr: ErrMFAChallengeExpired,
		},
		{
			name: "RejectsConsumedChallenge",
			challenge: &users.MFAChallenge{
				ID:         42,
				User:       verifiedUserPublic(),
				CodeHash:   "123456",
				ExpiresAt:  time.Now().Add(time.Minute),
				ConsumedAt: time.Now(),
			},
			code:    "123456",
			wantErr: ErrMFAChallengeConsumed,
		},
		{
			name: "RejectsLockedChallenge",
			challenge: &users.MFAChallenge{
				ID:           42,
				User:         verifiedUserPublic(),
				CodeHash:     "123456",
				ExpiresAt:    time.Now().Add(time.Minute),
				AttemptCount: mfaMaxAttempts,
			},
			code:    "123456",
			wantErr: ErrMFAChallengeLocked,
		},
		{
			name: "RejectsConcurrentConsumedChallenge",
			challenge: &users.MFAChallenge{
				ID:        42,
				User:      verifiedUserPublic(),
				CodeHash:  "123456",
				ExpiresAt: time.Now().Add(time.Minute),
			},
			code:        "123456",
			wantErr:     ErrMFAChallengeConsumed,
			wantConsume: true,
			consumeErr:  users.ErrMFAChallengeConsumed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeUsersRepo{}
			repo.FindMFAChallengeByTokenFn = func(_ context.Context, _ postgres.DBTX, challengeTokenHash string) (*users.MFAChallenge, error) {
				require.Equal(t, hashToken("challenge-token"), challengeTokenHash)
				return tt.challenge, nil
			}
			incremented := false
			repo.IncrementMFAChallengeAttemptsFn = func(_ context.Context, _ postgres.DBTX, id int) error {
				require.Equal(t, tt.challenge.ID, id)
				incremented = true
				return tt.incrementErr
			}
			consumed := false
			repo.ConsumeMFAChallengeFn = func(_ context.Context, _ postgres.DBTX, id int) error {
				require.Equal(t, tt.challenge.ID, id)
				consumed = true
				return tt.consumeErr
			}

			svc := newTestService(repo, nil, nil)
			got, err := svc.VerifySessionMFA(context.Background(), "challenge-token", tt.code)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, must.Slug("alice"), got.Slug)
				require.Equal(t, tt.challenge.User.HasLocalPassword, got.HasLocalPassword)
			}
			require.Equal(t, tt.wantIncrement, incremented)
			require.Equal(t, tt.wantConsume, consumed)
		})
	}
}

// verifiedLocalPasswordUserPublic builds a verified test user that can manage a local password.
func verifiedLocalPasswordUserPublic() *users.UserPublic {
	u := verifiedUserPublic()
	u.HasLocalPassword = true
	return u
}

func verifiedUserPublic() *users.UserPublic {
	u := testutil.StubUserPublic(
		must.Slug("alice"),
		"Alice Smith",
		"alice@example.com",
		model.RoleUser,
	)
	u.VerifiedAt = stubTime
	return u
}

func TestGetUserBySlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		slug     slug.Slug
		setup    func(repo *testutil.FakeUsersRepo)
		wantSlug string
		wantErr  error
	}{
		{
			name: "ReturnsUserBySlug",
			slug: must.Slug("alice"),
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, userSlugParam slug.Slug) (*users.UserPublic, error) {
					assert.Equal(t, must.Slug("alice"), userSlugParam)
					return testutil.StubUserPublic(
						must.Slug("alice"),
						"Alice Smith",
						"alice@example.com",
						model.RoleUser,
					), nil
				}
			},
			wantSlug: "alice",
		},
		{
			name: "ReturnsNotFoundError",
			slug: must.Slug("nonexistent"),
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (*users.UserPublic, error) {
					return nil, users.ErrUserNotFound
				}
			},
			wantErr: ErrUserNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeUsersRepo{}
			tt.setup(repo)
			svc := newTestService(repo, nil, nil)

			got, err := svc.GetUserBySlug(context.Background(), tt.slug)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, must.Slug(tt.wantSlug), got.Slug)
		})
	}
}

func TestLoginOrCreateOIDCUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		oidcUser        *model.OIDCUserInfo
		setup           func(repo *testutil.FakeUsersRepo)
		wantSlug        string
		wantErr         error
		wantErrContains string
	}{
		{
			name: "ReturnsExistingUserWhenFoundByProvider",
			oidcUser: &model.OIDCUserInfo{
				Provider:   "google",
				ProviderID: "google-123",
				Email:      "alice@example.com",
				Name:       "Alice Smith",
			},
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.FindByOIDCProviderFn = func(_ context.Context, _ postgres.DBTX, provider, providerID string) (*users.UserPublic, error) {
					assert.Equal(t, "google", provider)
					assert.Equal(t, "google-123", providerID)
					return testutil.StubUserPublic(
						must.Slug("alice-smith"),
						"Alice Smith",
						"alice@example.com",
						model.RoleUser,
					), nil
				}
			},
			wantSlug: "alice-smith",
		},
		{
			name: "LooksUpExistingUserWithOIDCProvider",
			oidcUser: &model.OIDCUserInfo{
				Provider:   "github",
				ProviderID: "github-123",
				Email:      "alice@example.com",
				Name:       "Alice Smith",
			},
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.FindByOIDCProviderFn = func(_ context.Context, _ postgres.DBTX, provider, providerID string) (*users.UserPublic, error) {
					assert.Equal(t, "github", provider)
					assert.Equal(t, "github-123", providerID)
					return testutil.StubUserPublic(
						must.Slug("alice-smith"),
						"Alice Smith",
						"alice@example.com",
						model.RoleUser,
					), nil
				}
			},
			wantSlug: "alice-smith",
		},
		{
			name: "CreatesNewUserWhenNotFoundByProvider",
			oidcUser: &model.OIDCUserInfo{
				Provider:   "google",
				ProviderID: "google-456",
				Email:      "bob@example.com",
				Name:       "Bob Jones",
				Locale:     "en",
			},
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.FindByOIDCProviderFn = func(_ context.Context, _ postgres.DBTX, _, _ string) (*users.UserPublic, error) {
					return nil, users.ErrUserNotFound
				}
				repo.FindByEmailFn = func(_ context.Context, _ postgres.DBTX, _ string) (*users.UserPublic, error) {
					return nil, users.ErrUserNotFound
				}
				repo.IsSlugReservedFn = func(_ context.Context, _ postgres.DBTX, userSlug slug.Slug) (bool, error) {
					return false, nil
				}
				repo.CreateOIDCUserFn = func(_ context.Context, _ postgres.DBTX, p *users.CreateOIDCUserParams) (*users.UserPublic, error) {
					assert.Regexp(t, `^user-[a-z0-9]{32}$`, p.Slug.String())
					assert.Equal(t, "bob@example.com", p.Email)
					assert.Equal(t, "Bob Jones", p.FullName)
					assert.Equal(t, "google", p.OIDCProvider)
					assert.Equal(t, "google-456", p.OIDCProviderID)
					return testutil.StubUserPublic(
						must.Slug("bob-jones"),
						"Bob Jones",
						"bob@example.com",
						model.RoleUser,
					), nil
				}
			},
			wantSlug: "bob-jones",
		},
		{
			name: "ReturnsEmailConflictWhenEmailAlreadyUsed",
			oidcUser: &model.OIDCUserInfo{
				Provider:   "google",
				ProviderID: "google-789",
				Email:      "taken@example.com",
				Name:       "Existing User",
			},
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.FindByOIDCProviderFn = func(_ context.Context, _ postgres.DBTX, _, _ string) (*users.UserPublic, error) {
					return nil, users.ErrUserNotFound
				}
				repo.FindByEmailFn = func(_ context.Context, _ postgres.DBTX, _ string) (*users.UserPublic, error) {
					return testutil.StubUserPublic(
						must.Slug("existing"),
						"Existing User",
						"taken@example.com",
						model.RoleUser,
					), nil
				}
			},
			wantErr: ErrEmailAlreadyExists,
		},
		{
			name: "FailsClosedWhenEmailLookupReturnsUnexpectedError",
			oidcUser: &model.OIDCUserInfo{
				Provider:   "google",
				ProviderID: "google-999",
				Email:      "alice@example.com",
				Name:       "Alice Smith",
			},
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.FindByOIDCProviderFn = func(_ context.Context, _ postgres.DBTX, _, _ string) (*users.UserPublic, error) {
					return nil, users.ErrUserNotFound
				}
				repo.FindByEmailFn = func(_ context.Context, _ postgres.DBTX, _ string) (*users.UserPublic, error) {
					return nil, xerror.Unexpected().Detail("user email lookup failed")
				}
				repo.CreateOIDCUserFn = func(_ context.Context, _ postgres.DBTX, _ *users.CreateOIDCUserParams) (*users.UserPublic, error) {
					return nil, xerror.Unexpected().Detail("unexpected create oidc user call")
				}
			},
			wantErrContains: "user email lookup failed",
		},
		{
			name: "ReturnsInvalidInputWhenProviderIDMissing",
			oidcUser: &model.OIDCUserInfo{
				Provider: "google",
				Email:    "alice@example.com",
				Name:     "Alice Smith",
			},
			setup:   func(repo *testutil.FakeUsersRepo) {},
			wantErr: ErrInvalidInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeUsersRepo{}
			tt.setup(repo)
			svc := newTestService(repo, nil, nil)

			got, err := svc.LoginOrCreateOIDCUser(context.Background(), tt.oidcUser)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, must.Slug(tt.wantSlug), got.Slug)
		})
	}
}

func TestLoginOIDCUserUsesExistingProviderOnly(t *testing.T) {
	t.Parallel()

	t.Run("ExistingProviderUserCanSignIn", func(t *testing.T) {
		t.Parallel()

		repo := &testutil.FakeUsersRepo{
			FindByOIDCProviderFn: func(_ context.Context, _ postgres.DBTX, provider, providerID string) (*users.UserPublic, error) {
				assert.Equal(t, "google", provider)
				assert.Equal(t, "google-123", providerID)
				return testutil.StubUserPublic(
					must.Slug("alice-smith"),
					"Alice Smith",
					"alice@example.com",
					model.RoleUser,
				), nil
			},
			CreateOIDCUserFn: func(_ context.Context, _ postgres.DBTX, _ *users.CreateOIDCUserParams) (*users.UserPublic, error) {
				require.FailNow(t, "disabled self-service registration must not create a new OIDC user")
				return nil, xerror.Unexpected().Detail("unexpected oidc user creation")
			},
		}
		svc := newTestService(repo, nil, nil)

		got, err := svc.LoginOIDCUser(context.Background(), &model.OIDCUserInfo{
			Provider:   "google",
			ProviderID: "google-123",
			Email:      "alice@example.com",
			Name:       "Alice Smith",
		})

		require.NoError(t, err)
		assert.Equal(t, must.Slug("alice-smith"), got.Slug)
	})

	t.Run("MissingProviderUserIsNotAutoCreated", func(t *testing.T) {
		t.Parallel()

		createCalled := false
		repo := &testutil.FakeUsersRepo{
			FindByOIDCProviderFn: func(_ context.Context, _ postgres.DBTX, _, _ string) (*users.UserPublic, error) {
				return nil, users.ErrUserNotFound
			},
			FindByEmailFn: func(_ context.Context, _ postgres.DBTX, _ string) (*users.UserPublic, error) {
				return nil, users.ErrUserNotFound
			},
			FindBySlugFn: func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (*users.UserPublic, error) {
				return nil, users.ErrUserNotFound
			},
			CreateOIDCUserFn: func(_ context.Context, _ postgres.DBTX, _ *users.CreateOIDCUserParams) (*users.UserPublic, error) {
				createCalled = true
				return nil, xerror.Unexpected().Detail("unexpected create oidc user call")
			},
		}
		svc := newTestService(repo, nil, nil)

		_, err := svc.LoginOIDCUser(context.Background(), &model.OIDCUserInfo{
			Provider:   "google",
			ProviderID: "google-456",
			Email:      "new@example.com",
			Name:       "New User",
		})

		require.ErrorIs(t, err, ErrUserNotFound)
		assert.False(t, createCalled)
	})

	t.Run("MissingProviderIDDoesNotQueryRepository", func(t *testing.T) {
		t.Parallel()

		repo := &testutil.FakeUsersRepo{
			FindByOIDCProviderFn: func(context.Context, postgres.DBTX, string, string) (*users.UserPublic, error) {
				require.FailNow(t, "missing provider id must be rejected before repository lookup")
				return nil, xerror.Unexpected().Detail("unexpected provider lookup")
			},
		}
		svc := newTestService(repo, nil, nil)

		_, err := svc.LoginOIDCUser(context.Background(), &model.OIDCUserInfo{
			Provider: "google",
			Email:    "new@example.com",
			Name:     "New User",
		})

		require.ErrorIs(t, err, ErrInvalidInput)
	})
}

func TestVerifyEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		token   string
		setup   func(repo *testutil.FakeUsersRepo)
		wantErr error
	}{
		{
			name:  "VerifiesEmailSuccessfully",
			token: "some-valid-token",
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.FindByEmailVerificationTokenFn = func(_ context.Context, _ postgres.DBTX, _ string) (*users.UserPublic, error) {
					return &users.UserPublic{
						Slug:      "alice",
						Email:     "alice@example.com",
						Role:      model.RoleUser,
						CreatedAt: stubTime,
						UpdatedAt: stubTime,
						// VerifiedAt is zero — not yet verified
					}, nil
				}
				repo.MarkEmailAsVerifiedFn = func(_ context.Context, _ postgres.DBTX, userSlugParam slug.Slug, verifiedAt time.Time) error {
					assert.Equal(t, must.Slug("alice"), userSlugParam)
					return nil
				}
			},
		},
		{
			name:  "ReturnsErrorForInvalidToken",
			token: "bad-token",
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.FindByEmailVerificationTokenFn = func(_ context.Context, _ postgres.DBTX, _ string) (*users.UserPublic, error) {
					return nil, users.ErrUserNotFound
				}
			},
			wantErr: ErrInvalidToken,
		},
		{
			name:  "ReturnsNilWhenAlreadyVerified",
			token: "some-valid-token",
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.FindByEmailVerificationTokenFn = func(_ context.Context, _ postgres.DBTX, _ string) (*users.UserPublic, error) {
					return &users.UserPublic{
						Slug:       "alice",
						Email:      "alice@example.com",
						Role:       model.RoleUser,
						VerifiedAt: stubTime,
						CreatedAt:  stubTime,
						UpdatedAt:  stubTime,
					}, nil
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeUsersRepo{}
			tt.setup(repo)
			svc := newTestService(repo, nil, nil)

			err := svc.VerifyEmail(context.Background(), tt.token)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRequestPasswordReset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		email           string
		setup           func(repo *testutil.FakeUsersRepo, hasher *testutil.FakeHasher, emailSvc *testutil.FakeEmailService)
		wantErr         error
		wantErrContains string
	}{
		{
			name:  "ReturnsNilWhenUserDoesNotExist",
			email: "missing@example.com",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher, emailSvc *testutil.FakeEmailService) {
				repo.FindByEmailFn = func(_ context.Context, _ postgres.DBTX, email string) (*users.UserPublic, error) {
					assert.Equal(t, "missing@example.com", email)
					return nil, users.ErrUserNotFound
				}
				emailSvc.PasswordResetFn = func(_ context.Context, _ *model.User, _ string) error {
					t.Fatal("password reset email should not be sent")
					return nil
				}
			},
		},
		{
			name:  "CreatesTokenAndSendsResetEmail",
			email: "alice@example.com",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher, emailSvc *testutil.FakeEmailService) {
				repo.FindByEmailFn = func(_ context.Context, _ postgres.DBTX, email string) (*users.UserPublic, error) {
					assert.Equal(t, "alice@example.com", email)
					return testutil.StubUserPublic(
						must.Slug("alice-smith"),
						"Alice Smith",
						email,
						model.RoleUser,
					), nil
				}
				repo.CreatePasswordResetTokenFn = func(_ context.Context, _ postgres.DBTX, params users.CreatePasswordResetTokenParams) error {
					assert.Equal(t, must.Slug("alice-smith"), params.Slug)
					assert.NotEmpty(t, params.PasswordResetTokenHash)
					assert.False(t, params.PasswordResetTokenExpiresAt.IsZero())
					return nil
				}
				emailSvc.PasswordResetFn = func(_ context.Context, user *model.User, link string) error {
					assert.Equal(t, must.Slug("alice-smith"), user.Slug)
					resetURL, err := url.Parse(link)
					require.NoError(t, err)
					assert.Equal(t, "https", resetURL.Scheme)
					assert.Equal(t, "app.example.com", resetURL.Host)
					assert.Equal(t, "/auth/reset-password", resetURL.Path)
					assert.NotEmpty(t, resetURL.Query().Get("token"))
					return nil
				}
			},
		},
		{
			name:            "ReturnsValidationErrorForInvalidEmail",
			email:           "bad-email",
			setup:           func(_ *testutil.FakeUsersRepo, _ *testutil.FakeHasher, _ *testutil.FakeEmailService) {},
			wantErrContains: "validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeUsersRepo{}
			hasher := &testutil.FakeHasher{}
			emailSvc := &testutil.FakeEmailService{}
			tt.setup(repo, hasher, emailSvc)
			svc := newTestService(repo, hasher, emailSvc)

			err := svc.RequestPasswordReset(context.Background(), tt.email)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestResetPassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		token           string
		password        string
		setup           func(repo *testutil.FakeUsersRepo, hasher *testutil.FakeHasher, emailSvc *testutil.FakeEmailService)
		wantErr         error
		wantErrContains string
	}{
		{
			name:     "ReturnsInvalidTokenWhenUserDoesNotExist",
			token:    "missing-token",
			password: "newsecurepass",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher, _ *testutil.FakeEmailService) {
				repo.FindByPasswordResetTokenFn = func(_ context.Context, _ postgres.DBTX, tokenHash string) (*users.UserPublic, error) {
					assert.NotEmpty(t, tokenHash)
					return nil, users.ErrUserNotFound
				}
			},
			wantErr: ErrInvalidToken,
		},
		{
			name:     "UpdatesPasswordWhenTokenIsValid",
			token:    "valid-token",
			password: "newsecurepass",
			setup: func(repo *testutil.FakeUsersRepo, hasher *testutil.FakeHasher, _ *testutil.FakeEmailService) {
				repo.FindByPasswordResetTokenFn = func(_ context.Context, _ postgres.DBTX, tokenHash string) (*users.UserPublic, error) {
					assert.NotEmpty(t, tokenHash)
					return testutil.StubUserPublic(
						must.Slug("alice-smith"),
						"Alice Smith",
						"alice@example.com",
						model.RoleUser,
					), nil
				}
				hasher.HashFn = func(input string) (string, error) {
					assert.Equal(t, "newsecurepass", input)
					return "hashed-password", nil
				}
				repo.UpdatePasswordFn = func(_ context.Context, _ postgres.DBTX, userSlug slug.Slug, hashedPassword string) error {
					assert.Equal(t, must.Slug("alice-smith"), userSlug)
					assert.Equal(t, "hashed-password", hashedPassword)
					return nil
				}
			},
		},
		{
			name:            "ReturnsValidationErrorForShortPassword",
			token:           "valid-token",
			password:        "short",
			setup:           func(_ *testutil.FakeUsersRepo, _ *testutil.FakeHasher, _ *testutil.FakeEmailService) {},
			wantErrContains: "validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeUsersRepo{}
			hasher := &testutil.FakeHasher{}
			emailSvc := &testutil.FakeEmailService{}
			tt.setup(repo, hasher, emailSvc)
			svc := newTestService(repo, hasher, emailSvc)

			err := svc.ResetPassword(context.Background(), tt.token, tt.password)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestChangePassword(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("alice-smith")

	tests := []struct {
		name            string
		currentPassword string
		newPassword     string
		setup           func(repo *testutil.FakeUsersRepo, hasher *testutil.FakeHasher)
		wantErr         error
		wantErrContains string
	}{
		{
			name:            "UpdatesPasswordAfterVerifyingCurrentPassword",
			currentPassword: "currentpass",
			newPassword:     "newsecurepass",
			setup: func(repo *testutil.FakeUsersRepo, hasher *testutil.FakeHasher) {
				repo.FindBySlugWithPasswordFn = func(_ context.Context, _ postgres.DBTX, gotSlug slug.Slug) (*users.UserWithPassword, error) {
					assert.Equal(t, userSlug, gotSlug)
					return &users.UserWithPassword{
						Slug:     userSlug,
						Email:    "alice@example.com",
						FullName: "Alice Smith",
						Password: "stored-current-hash",
					}, nil
				}
				hasher.CompareHashFn = func(input, hash string) (bool, error) {
					assert.Equal(t, "currentpass", input)
					assert.Equal(t, "stored-current-hash", hash)
					return true, nil
				}
				hasher.HashFn = func(input string) (string, error) {
					assert.Equal(t, "newsecurepass", input)
					return "stored-new-hash", nil
				}
				repo.UpdatePasswordFn = func(_ context.Context, _ postgres.DBTX, gotSlug slug.Slug, hashedPassword string) error {
					assert.Equal(t, userSlug, gotSlug)
					assert.Equal(t, "stored-new-hash", hashedPassword)
					return nil
				}
			},
		},
		{
			name:            "RejectsWrongCurrentPassword",
			currentPassword: "wrongpass",
			newPassword:     "newsecurepass",
			setup: func(repo *testutil.FakeUsersRepo, hasher *testutil.FakeHasher) {
				repo.FindBySlugWithPasswordFn = func(_ context.Context, _ postgres.DBTX, gotSlug slug.Slug) (*users.UserWithPassword, error) {
					assert.Equal(t, userSlug, gotSlug)
					return &users.UserWithPassword{Slug: userSlug, Password: "stored-current-hash"}, nil
				}
				hasher.CompareHashFn = func(input, hash string) (bool, error) {
					assert.Equal(t, "wrongpass", input)
					assert.Equal(t, "stored-current-hash", hash)
					return false, nil
				}
				repo.UpdatePasswordFn = func(context.Context, postgres.DBTX, slug.Slug, string) error {
					t.Fatal("password should not be updated when current password is wrong")
					return nil
				}
			},
			wantErr: ErrInvalidUsernameOrPassword,
		},
		{
			name:            "RejectsUserWithoutLocalPassword",
			currentPassword: "currentpass",
			newPassword:     "newsecurepass",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher) {
				repo.FindBySlugWithPasswordFn = func(_ context.Context, _ postgres.DBTX, gotSlug slug.Slug) (*users.UserWithPassword, error) {
					assert.Equal(t, userSlug, gotSlug)
					return nil, users.ErrUserNotFound
				}
				repo.UpdatePasswordFn = func(context.Context, postgres.DBTX, slug.Slug, string) error {
					t.Fatal("password should not be updated when local password is unavailable")
					return nil
				}
			},
			wantErr: ErrLocalPasswordUnavailable,
		},
		{
			name:            "ReturnsValidationErrorForShortNewPassword",
			currentPassword: "currentpass",
			newPassword:     "short",
			setup:           func(_ *testutil.FakeUsersRepo, _ *testutil.FakeHasher) {},
			wantErrContains: "validation failed",
		},
		{
			name:            "WrapsUpdateFailure",
			currentPassword: "currentpass",
			newPassword:     "newsecurepass",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher) {
				repo.FindBySlugWithPasswordFn = func(_ context.Context, _ postgres.DBTX, gotSlug slug.Slug) (*users.UserWithPassword, error) {
					assert.Equal(t, userSlug, gotSlug)
					return &users.UserWithPassword{Slug: userSlug, Password: "currentpass"}, nil
				}
				repo.UpdatePasswordFn = func(_ context.Context, _ postgres.DBTX, gotSlug slug.Slug, hashedPassword string) error {
					assert.Equal(t, userSlug, gotSlug)
					assert.Equal(t, "newsecurepass", hashedPassword)
					return xerror.Unexpected().Detail("update failed")
				}
			},
			wantErrContains: "update failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeUsersRepo{}
			hasher := &testutil.FakeHasher{}
			tt.setup(repo, hasher)
			svc := newTestService(repo, hasher, nil)

			err := svc.ChangePassword(context.Background(), userSlug, tt.currentPassword, tt.newPassword)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestResendVerificationEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		email           string
		setup           func(repo *testutil.FakeUsersRepo, hasher *testutil.FakeHasher, emailSvc *testutil.FakeEmailService)
		wantErr         error
		wantErrContains string
	}{
		{
			name:  "ReturnsNilWhenUserDoesNotExist",
			email: "missing@example.com",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher, emailSvc *testutil.FakeEmailService) {
				repo.FindByEmailFn = func(_ context.Context, _ postgres.DBTX, email string) (*users.UserPublic, error) {
					assert.Equal(t, "missing@example.com", email)
					return nil, users.ErrUserNotFound
				}
				emailSvc.EmailVerificationFn = func(_ context.Context, _ *model.User, _ string) error {
					t.Fatal("verification email should not be sent")
					return nil
				}
			},
		},
		{
			name:  "ReturnsNilWhenUserAlreadyVerified",
			email: "verified@example.com",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher, emailSvc *testutil.FakeEmailService) {
				repo.FindByEmailFn = func(_ context.Context, _ postgres.DBTX, email string) (*users.UserPublic, error) {
					user := testutil.StubUserPublic(
						must.Slug("verified-user"),
						"Verified User",
						email,
						model.RoleUser,
					)
					user.VerifiedAt = time.Now()
					return user, nil
				}
				emailSvc.EmailVerificationFn = func(_ context.Context, _ *model.User, _ string) error {
					t.Fatal("verification email should not be sent")
					return nil
				}
			},
		},
		{
			name:  "CreatesTokenAndSendsVerificationEmail",
			email: "alice@example.com",
			setup: func(repo *testutil.FakeUsersRepo, _ *testutil.FakeHasher, emailSvc *testutil.FakeEmailService) {
				repo.FindByEmailFn = func(_ context.Context, _ postgres.DBTX, email string) (*users.UserPublic, error) {
					return testutil.StubUserPublic(
						must.Slug("alice-smith"),
						"Alice Smith",
						email,
						model.RoleUser,
					), nil
				}
				repo.CreateEmailVerificationTokenFn = func(_ context.Context, _ postgres.DBTX, params users.CreateEmailVerificationTokenParams) error {
					assert.Equal(t, must.Slug("alice-smith"), params.Slug)
					assert.NotEmpty(t, params.EmailVerificationTokenHash)
					assert.False(t, params.EmailVerificationTokenExpiresAt.IsZero())
					return nil
				}
				emailSvc.EmailVerificationFn = func(_ context.Context, user *model.User, link string) error {
					assert.Equal(t, must.Slug("alice-smith"), user.Slug)
					assert.Contains(t, link, "https://app.example.com/verify-email?token=")
					return nil
				}
			},
		},
		{
			name:            "ReturnsValidationErrorForInvalidEmail",
			email:           "bad-email",
			setup:           func(_ *testutil.FakeUsersRepo, _ *testutil.FakeHasher, _ *testutil.FakeEmailService) {},
			wantErrContains: "validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeUsersRepo{}
			hasher := &testutil.FakeHasher{}
			emailSvc := &testutil.FakeEmailService{}
			tt.setup(repo, hasher, emailSvc)
			svc := newTestService(repo, hasher, emailSvc)

			err := svc.ResendVerificationEmail(context.Background(), tt.email)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestUpdateProfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		userSlug        slug.Slug
		update          model.UpdateUserProfile
		setup           func(repo *testutil.FakeUsersRepo)
		wantErrContains string
	}{
		{
			name:     "UpdatesProfileAndReturnsUpdatedUser",
			userSlug: must.Slug("alice"),
			update:   model.UpdateUserProfile{Locale: testStringPtr("pl")},
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.UpdateProfileFn = func(_ context.Context, _ postgres.DBTX, p *users.UpdateProfileParams) error {
					assert.Equal(t, must.Slug("alice"), p.Slug)
					require.NotNil(t, p.Locale)
					assert.Equal(t, "pl", *p.Locale)
					return nil
				}
				repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, userSlugParam slug.Slug) (*users.UserPublic, error) {
					return &users.UserPublic{
						Slug:      "alice",
						Email:     "alice@example.com",
						FullName:  "Alice Smith",
						Role:      model.RoleUser,
						Locale:    "pl",
						CreatedAt: stubTime,
						UpdatedAt: stubTime,
					}, nil
				}
			},
		},
		{
			name:     "UpdatesProfileWithOptionalFullName",
			userSlug: must.Slug("alice"),
			update:   model.UpdateUserProfile{Locale: testStringPtr("pl"), FullName: testStringPtr("Alice S.")},
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.UpdateProfileFn = func(_ context.Context, _ postgres.DBTX, p *users.UpdateProfileParams) error {
					assert.Equal(t, must.Slug("alice"), p.Slug)
					require.NotNil(t, p.Locale)
					require.NotNil(t, p.FullName)
					assert.Equal(t, "pl", *p.Locale)
					assert.Equal(t, "Alice S.", *p.FullName)
					return nil
				}
				repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, userSlugParam slug.Slug) (*users.UserPublic, error) {
					return &users.UserPublic{
						Slug:      "alice",
						Email:     "alice@example.com",
						FullName:  "Alice S.",
						Role:      model.RoleUser,
						Locale:    "pl",
						CreatedAt: stubTime,
						UpdatedAt: stubTime,
					}, nil
				}
			},
		},
		{
			name:     "PropagatesRepositoryError",
			userSlug: must.Slug("alice"),
			update:   model.UpdateUserProfile{Locale: testStringPtr("en")},
			setup: func(repo *testutil.FakeUsersRepo) {
				repo.UpdateProfileFn = func(_ context.Context, _ postgres.DBTX, _ *users.UpdateProfileParams) error {
					return fmt.Errorf("db down")
				}
			},
			wantErrContains: "db down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeUsersRepo{}
			tt.setup(repo)
			svc := newTestService(repo, nil, nil)

			got, err := svc.UpdateProfile(context.Background(), tt.userSlug, tt.update)
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.userSlug, got.Slug)
		})
	}
}

func TestUpdateProfileValidatesExtendedFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		update model.UpdateUserProfile
		want   string
	}{
		{
			name:   "RejectsInvalidAvatarURL",
			update: model.UpdateUserProfile{AvatarURL: testStringPtr("not-a-url")},
			want:   "avatar_url must be an absolute URL",
		},
		{
			name:   "RejectsInvalidPhoneNumber",
			update: model.UpdateUserProfile{PhoneNumber: testStringPtr("abc")},
			want:   "phone_number is invalid",
		},
		{
			name:   "RejectsInvalidTimezone",
			update: model.UpdateUserProfile{Timezone: testStringPtr("Mars/Base")},
			want:   "timezone must be an IANA timezone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := newTestService(&testutil.FakeUsersRepo{}, nil, nil)

			got, err := svc.UpdateProfile(context.Background(), must.Slug("alice"), tt.update)

			require.ErrorContains(t, err, tt.want)
			require.Nil(t, got)
		})
	}
}

func testStringPtr(value string) *string {
	return &value
}

func TestNewAccountSlug(t *testing.T) {
	t.Parallel()
	first, err := newAccountSlug()
	require.NoError(t, err)
	second, err := newAccountSlug()
	require.NoError(t, err)
	require.Regexp(t, `^user-[a-z0-9]{32}$`, first.String())
	require.Regexp(t, `^user-[a-z0-9]{32}$`, second.String())
	require.NotEqual(t, first, second)
}

func TestPasswordMaximumBeforeDependencies(t *testing.T) {
	svc := newTestService(nil, nil, nil)
	oversized := strings.Repeat("p", 1025)
	_, err := svc.CreateUser(context.Background(), model.NewUser{Email: "alice@example.com", FullName: "Alice", Password: oversized, Role: model.RoleUser})
	require.Error(t, err)
	_, err = svc.BeginLogin(context.Background(), "alice@example.com", oversized)
	require.Error(t, err)
	require.Error(t, svc.ResetPassword(context.Background(), "reset-token", oversized))
	require.Error(t, svc.ChangePassword(context.Background(), slug.Slug("alice"), oversized, "new-password"))
	require.Error(t, svc.ChangePassword(context.Background(), slug.Slug("alice"), "old-password", oversized))
}
