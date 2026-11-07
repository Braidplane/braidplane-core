package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

var testUser = &model.User{
	Slug:   "johndoe",
	Email:  "john@example.com",
	Role:   model.RoleUser,
	Locale: "en",
	Timestamps: model.Timestamps{
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	},
}

var testUserAPI = userToHTTP(testUser)

func TestPostUsers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         any
		createUserFn func(ctx context.Context, newUser model.NewUser) (*model.User, error)
		wantStatus   int
		wantBody     *api.User
		wantErr      *apierror.Error
	}{
		{
			name: "CannotSelectDeletedAccountSlug",
			body: map[string]any{
				"slug": "deleted-account", "full_name": "John Doe", "email": "john@example.com", "password": "secret123",
			},
			createUserFn: func(_ context.Context, newUser model.NewUser) (*model.User, error) {
				require.Equal(t, "John Doe", newUser.FullName)
				return testUser, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   testUserAPI,
		},
		{
			name: "CreatesUserAndReturns201",
			body: api.NewUser{
				FullName: "John Doe",
				Email:    "john@example.com",
				Password: "secret123",
			},
			createUserFn: func(_ context.Context, newUser model.NewUser) (*model.User, error) {
				require.Equal(t, "John Doe", newUser.FullName)
				require.Equal(t, "john@example.com", newUser.Email)
				require.Equal(t, "secret123", newUser.Password)
				require.Equal(t, model.RoleUser, newUser.Role)
				require.Equal(t, "en", newUser.Locale)
				return testUser, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   testUserAPI,
		},
		{
			name: "Returns409WhenEmailAlreadyExists",
			body: api.NewUser{
				Email:    "john@example.com",
				Password: "secret123",
			},
			createUserFn: func(_ context.Context, _ model.NewUser) (*model.User, error) {
				return nil, userssvc.ErrEmailAlreadyExists
			},
			wantStatus: http.StatusConflict,
			wantErr:    apierror.New(409, "USER_EMAIL_ALREADY_EXISTS", "user email already exists"),
		},
		{
			name: "Returns409WhenSlugAlreadyExists",
			body: api.NewUser{
				Email:    "john@example.com",
				Password: "secret123",
			},
			createUserFn: func(_ context.Context, _ model.NewUser) (*model.User, error) {
				return nil, userssvc.ErrSlugAlreadyExists
			},
			wantStatus: http.StatusConflict,
			wantErr:    apierror.New(409, "USER_ALREADY_EXISTS", "user already exists"),
		},
		{
			name:       "Returns400WhenInvalidRequestBody",
			body:       "not-json",
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name: "Returns400WhenServiceReturnsInvalidInput",
			body: api.NewUser{
				Email:    "john@example.com",
				Password: "s",
			},
			createUserFn: func(_ context.Context, _ model.NewUser) (*model.User, error) {
				return nil, userssvc.ErrInvalidInput
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "VALIDATION_FAILED", "validation failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, _ := newTestApp(t, testServerConfig{
				usersService: &fakeUsersService{
					CreateUserFn: tt.createUserFn,
				},
			})

			req := jsonRequest(t, http.MethodPost, "/users", tt.body)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody != nil {
				assertResponse(t, resp, tt.wantBody)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestPostUsersReturns403WhenSelfServiceRegistrationDisabled(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t, testServerConfig{
		selfServiceRegistrationEnabled: ptr.Ptr(false),
		usersService: &fakeUsersService{
			CreateUserFn: func(context.Context, model.NewUser) (*model.User, error) {
				require.FailNow(t, "disabled self-service registration must reject before creating a user")
				return nil, xerror.Unexpected().Detail("unexpected user creation")
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/users", api.NewUser{
		FullName: "Jane Doe",
		Email:    "jane@example.com",
		Password: "secret123",
	})
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assertErrorResponse(
		t,
		resp,
		apierror.New(
			http.StatusForbidden,
			"SELF_SERVICE_REGISTRATION_DISABLED",
			"self-service registration is disabled",
		),
	)
}

func TestPostUsersReturnsExternalAccessDisabledWhenExternalOnboardingDisabled(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t, testServerConfig{
		externalOnboardingEnabled: ptr.Ptr(false),
		usersService: &fakeUsersService{
			CreateUserFn: func(context.Context, model.NewUser) (*model.User, error) {
				require.FailNow(t, "disabled external onboarding must reject before creating a user")
				return nil, xerror.Unexpected().Detail("unexpected user creation")
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/users", api.NewUser{
		FullName: "Jane Doe",
		Email:    "jane@example.com",
		Password: "secret123",
	})
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assertErrorResponse(
		t,
		resp,
		apierror.New(
			http.StatusForbidden,
			"EXTERNAL_ACCESS_DISABLED",
			"external access is disabled",
		),
	)
}

func TestRegistrationControlsDoNotBlockExistingAuthFlows(t *testing.T) {
	t.Parallel()

	flows := []struct {
		name       string
		method     string
		path       string
		body       any
		wantStatus int
	}{
		{
			name:       "Login",
			method:     http.MethodPost,
			path:       "/sessions",
			body:       api.NewSession{Email: "john@example.com", Password: "secret123"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "MFA",
			method:     http.MethodPost,
			path:       "/sessions/mfa/verify",
			body:       api.MFAVerification{ChallengeToken: "challenge-token", Code: "123456"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "EmailVerification",
			method:     http.MethodPost,
			path:       "/auth/email/verify",
			body:       api.EmailVerificationRequest{Token: "verify-token"},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "PasswordResetRequest",
			method:     http.MethodPost,
			path:       "/auth/password/forgot",
			body:       api.PasswordForgotRequest{Email: "john@example.com"},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "PasswordResetComplete",
			method:     http.MethodPost,
			path:       "/auth/password/reset",
			body:       api.PasswordResetRequest{Token: "reset-token", Password: "NewSecurePass123!"},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "ResendVerification",
			method:     http.MethodPost,
			path:       "/auth/email/resend-verification",
			body:       api.ResendVerificationEmailRequest{Email: "john@example.com"},
			wantStatus: http.StatusNoContent,
		},
	}

	controls := []struct {
		name                           string
		selfServiceRegistrationEnabled *bool
		externalOnboardingEnabled      *bool
	}{
		{
			name:                           "SelfServiceRegistrationDisabled",
			selfServiceRegistrationEnabled: ptr.Ptr(false),
		},
		{
			name:                      "ExternalOnboardingDisabled",
			externalOnboardingEnabled: ptr.Ptr(false),
		},
	}

	for _, control := range controls {
		t.Run(control.name, func(t *testing.T) {
			t.Parallel()

			for _, flow := range flows {
				t.Run(flow.name, func(t *testing.T) {
					t.Parallel()

					app, _ := newTestApp(t, testServerConfig{
						selfServiceRegistrationEnabled: control.selfServiceRegistrationEnabled,
						externalOnboardingEnabled:      control.externalOnboardingEnabled,
						usersService: &fakeUsersService{
							BeginLoginFn: func(_ context.Context, email, password string) (*model.SessionLoginResult, error) {
								require.Equal(t, "john@example.com", email)
								require.Equal(t, "secret123", password)
								return &model.SessionLoginResult{User: testUser}, nil
							},
							VerifySessionMFAFn: func(_ context.Context, challengeToken, code string) (*model.User, error) {
								require.Equal(t, "challenge-token", challengeToken)
								require.Equal(t, "123456", code)
								return testUser, nil
							},
							VerifyEmailFn: func(_ context.Context, token string) error {
								require.Equal(t, "verify-token", token)
								return nil
							},
							RequestPasswordResetFn: func(_ context.Context, email string) error {
								require.Equal(t, "john@example.com", email)
								return nil
							},
							ResetPasswordFn: func(_ context.Context, token, password string) error {
								require.Equal(t, "reset-token", token)
								require.Equal(t, "NewSecurePass123!", password)
								return nil
							},
							ResendVerificationEmailFn: func(_ context.Context, email string) error {
								require.Equal(t, "john@example.com", email)
								return nil
							},
						},
					})

					req := jsonRequest(t, flow.method, flow.path, flow.body)
					resp, err := app.Test(req)
					require.NoError(t, err)
					defer func() {
						require.NoError(t, resp.Body.Close())
					}()

					assert.Equal(t, flow.wantStatus, resp.StatusCode)
				})
			}
		})
	}
}

func TestVerifyEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		body          any
		verifyEmailFn func(ctx context.Context, token string) error
		wantStatus    int
		wantErr       *apierror.Error
	}{
		{
			name: "Returns204OnSuccess",
			body: api.EmailVerificationRequest{Token: "valid-token"},
			verifyEmailFn: func(_ context.Context, _ string) error {
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "Returns400ForInvalidToken",
			body: api.EmailVerificationRequest{Token: "bad-token"},
			verifyEmailFn: func(_ context.Context, _ string) error {
				return userssvc.ErrInvalidToken
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "INVALID_TOKEN", "invalid token"),
		},
		{
			name: "Returns400WhenAlreadyVerified",
			body: api.EmailVerificationRequest{Token: "used-token"},
			verifyEmailFn: func(_ context.Context, _ string) error {
				return userssvc.ErrAlreadyVerified
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "ALREADY_VERIFIED", "already verified"),
		},
		{
			name:       "Returns400WhenInvalidRequestBody",
			body:       "not-json",
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, _ := newTestApp(t, testServerConfig{
				usersService: &fakeUsersService{
					VerifyEmailFn: tt.verifyEmailFn,
				},
			})

			req := jsonRequest(t, http.MethodPost, "/auth/email/verify", tt.body)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestRequestPasswordReset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		body                   any
		requestPasswordResetFn func(ctx context.Context, email string) error
		wantStatus             int
		wantErr                *apierror.Error
	}{
		{
			name: "Returns204OnSuccess",
			body: api.PasswordForgotRequest{Email: "john@example.com"},
			requestPasswordResetFn: func(_ context.Context, email string) error {
				require.Equal(t, "john@example.com", email)
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "Returns400WhenEmailFormatIsInvalid",
			body:       map[string]string{"email": "bad-email"},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name:       "Returns400WhenInvalidRequestBody",
			body:       "not-json",
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, _ := newTestApp(t, testServerConfig{
				usersService: &fakeUsersService{RequestPasswordResetFn: tt.requestPasswordResetFn},
			})

			req := jsonRequest(t, http.MethodPost, "/auth/password/forgot", tt.body)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestResetPassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		body            any
		resetPasswordFn func(ctx context.Context, token, password string) error
		wantStatus      int
		wantErr         *apierror.Error
	}{
		{
			name: "Returns204OnSuccess",
			body: api.PasswordResetRequest{Token: "reset-token", Password: "NewSecurePass123!"},
			resetPasswordFn: func(_ context.Context, token, password string) error {
				require.Equal(t, "reset-token", token)
				require.Equal(t, "NewSecurePass123!", password)
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "Returns400ForInvalidToken",
			body: api.PasswordResetRequest{Token: "bad-token", Password: "NewSecurePass123!"},
			resetPasswordFn: func(_ context.Context, _, _ string) error {
				return userssvc.ErrInvalidToken
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "INVALID_TOKEN", "invalid token"),
		},
		{
			name:       "Returns400WhenInvalidRequestBody",
			body:       "not-json",
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, _ := newTestApp(t, testServerConfig{
				usersService: &fakeUsersService{ResetPasswordFn: tt.resetPasswordFn},
			})

			req := jsonRequest(t, http.MethodPost, "/auth/password/reset", tt.body)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestResendVerificationEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                      string
		body                      any
		resendVerificationEmailFn func(ctx context.Context, email string) error
		wantStatus                int
		wantErr                   *apierror.Error
	}{
		{
			name: "Returns204OnSuccess",
			body: api.ResendVerificationEmailRequest{Email: "john@example.com"},
			resendVerificationEmailFn: func(_ context.Context, email string) error {
				require.Equal(t, "john@example.com", email)
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "Returns204WhenDeliveryFails",
			body: api.ResendVerificationEmailRequest{Email: "john@example.com"},
			resendVerificationEmailFn: func(_ context.Context, email string) error {
				require.Equal(t, "john@example.com", email)
				return xerror.Unexpected().Detail("email delivery failed")
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "Returns400WhenEmailFormatIsInvalid",
			body:       map[string]string{"email": "bad-email"},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
		{
			name:       "Returns400WhenInvalidRequestBody",
			body:       "not-json",
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.InvalidRequestBody(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, _ := newTestApp(t, testServerConfig{
				usersService: &fakeUsersService{
					ResendVerificationEmailFn: tt.resendVerificationEmailFn,
				},
			})

			req := jsonRequest(t, http.MethodPost, "/auth/email/resend-verification", tt.body)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestPatchUsersProfile(t *testing.T) {
	t.Parallel()

	avatarURL := "https://example.com/avatar.png"
	phoneNumber := "+48123123123"
	jobTitle := "Operations Lead"
	timezone := "Europe/Warsaw"
	mfaEnabled := true
	updatedUser := &model.User{
		Slug:            "johndoe",
		Email:           "john@example.com",
		Role:            model.RoleUser,
		Locale:          "pl",
		AvatarURL:       avatarURL,
		PhoneNumber:     phoneNumber,
		JobTitle:        jobTitle,
		Timezone:        timezone,
		MFAEmailEnabled: true,
		Timestamps:      testUser.Timestamps,
	}
	updatedUserAPI := userToHTTP(updatedUser)

	tests := []struct {
		name            string
		body            any
		userSlug        slug.Slug
		authAllowed     bool
		updateProfileFn func(ctx context.Context, userSlug slug.Slug, update model.UpdateUserProfile) (*model.User, error)
		wantStatus      int
		wantBody        *api.User
		wantErr         *apierror.Error
	}{
		{
			name: "UpdatesProfileAndReturns200",
			body: api.UpdateUserProfile{
				Locale:          ptr.Ptr("pl-PL"),
				AvatarUrl:       &avatarURL,
				PhoneNumber:     &phoneNumber,
				JobTitle:        &jobTitle,
				Timezone:        &timezone,
				MfaEmailEnabled: &mfaEnabled,
			},
			userSlug:    must.Slug("johndoe"),
			authAllowed: true,
			updateProfileFn: func(_ context.Context, gotUserSlug slug.Slug, update model.UpdateUserProfile) (*model.User, error) {
				require.Equal(t, must.Slug("johndoe"), gotUserSlug)
				require.NotNil(t, update.Locale)
				require.Equal(t, "pl", *update.Locale)
				require.Equal(t, &avatarURL, update.AvatarURL)
				require.Equal(t, &phoneNumber, update.PhoneNumber)
				require.Equal(t, &jobTitle, update.JobTitle)
				require.Equal(t, &timezone, update.Timezone)
				require.Equal(t, &mfaEnabled, update.MFAEmailEnabled)
				return updatedUser, nil
			},
			wantStatus: http.StatusOK,
			wantBody:   updatedUserAPI,
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			body:        api.UpdateUserProfile{Locale: new("pl")},
			userSlug:    must.Slug("johndoe"),
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("profile", "update"),
		},
		{
			name:        "Returns404WhenUserNotFound",
			body:        api.UpdateUserProfile{Locale: new("pl")},
			userSlug:    must.Slug("ghost"),
			authAllowed: true,
			updateProfileFn: func(_ context.Context, _ slug.Slug, _ model.UpdateUserProfile) (*model.User, error) {
				return nil, userssvc.ErrUserNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "USER_NOT_FOUND", "user not found"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				usersService: &fakeUsersService{
					UpdateProfileFn: tt.updateProfileFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodPatch, "/users/profile", tt.body)
			resp := authenticatedRequest(t, app, store, tt.userSlug, req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody != nil {
				assertResponse(t, resp, tt.wantBody)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestPatchUsersProfile_RejectsPATForMFASetting(t *testing.T) {
	t.Parallel()

	mfaEnabled := false
	updateCalled := false
	app, _ := newTestApp(t, testServerConfig{
		usersService: &fakeUsersService{
			UpdateProfileFn: func(_ context.Context, _ slug.Slug, _ model.UpdateUserProfile) (*model.User, error) {
				updateCalled = true
				return &model.User{}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
				return true, nil
			},
		},
		middleware: []fiber.Handler{
			func(c fiber.Ctx) error {
				reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{
					Slug:   must.Slug("johndoe"),
					Source: reqctx.UserPrincipalSourcePersonalAccessToken,
				})
				return c.Next()
			},
		},
	})

	req := jsonRequest(t, http.MethodPatch, "/users/profile", api.UpdateUserProfile{
		MfaEmailEnabled: &mfaEnabled,
	})
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.False(t, updateCalled)
	assertErrorResponse(t, resp, apierror.UserPrincipalRequired())
}

func TestUpdateUserPassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		body             any
		authAllowed      bool
		changePasswordFn func(ctx context.Context, userSlug slug.Slug, currentPassword, newPassword string) error
		wantStatus       int
		wantErr          *apierror.Error
		wantDetailed     string
	}{
		{
			name:        "ChangesPasswordAndReturns204",
			body:        api.UpdateUserPassword{CurrentPassword: "currentpass", NewPassword: "newsecurepass"},
			authAllowed: true,
			changePasswordFn: func(_ context.Context, userSlug slug.Slug, currentPassword, newPassword string) error {
				assert.Equal(t, must.Slug("johndoe"), userSlug)
				assert.Equal(t, "currentpass", currentPassword)
				assert.Equal(t, "newsecurepass", newPassword)
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:        "Returns401WhenCurrentPasswordIsWrong",
			body:        api.UpdateUserPassword{CurrentPassword: "wrongpass", NewPassword: "newsecurepass"},
			authAllowed: true,
			changePasswordFn: func(context.Context, slug.Slug, string, string) error {
				return userssvc.ErrInvalidUsernameOrPassword
			},
			wantStatus: http.StatusUnauthorized,
			wantErr:    apierror.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid credentials"),
		},
		{
			name:        "Returns400WhenLocalPasswordIsUnavailable",
			body:        api.UpdateUserPassword{CurrentPassword: "currentpass", NewPassword: "newsecurepass"},
			authAllowed: true,
			changePasswordFn: func(context.Context, slug.Slug, string, string) error {
				return userssvc.ErrLocalPasswordUnavailable
			},
			wantStatus:   http.StatusBadRequest,
			wantErr:      apierror.ValidationFailed("validation failed"),
			wantDetailed: "local password sign-in is not enabled for this account",
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			body:        api.UpdateUserPassword{CurrentPassword: "currentpass", NewPassword: "newsecurepass"},
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("profile", "update"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				usersService: &fakeUsersService{ChangePasswordFn: tt.changePasswordFn},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.Update, input.Action)
						assert.Equal(t, model.ResourceProfile, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodPut, "/users/profile/password", tt.body)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			if tt.wantErr != nil {
				errResp := parseResponse[apierror.ErrorResponse](t, resp)
				require.NotNil(t, errResp.Error, "expected error in response body")
				assert.Equal(t, tt.wantErr.Code, errResp.Error.Code)
				assert.Equal(t, tt.wantErr.Message, errResp.Error.Message)
				if tt.wantDetailed != "" {
					assert.Equal(t, tt.wantDetailed, errResp.Error.DetailedError)
				}
			}
		})
	}
}

func TestUpdateUserPassword_RejectsPATPrincipal(t *testing.T) {
	t.Parallel()

	changeCalled := false
	app, _ := newTestApp(t, testServerConfig{
		usersService: &fakeUsersService{
			ChangePasswordFn: func(context.Context, slug.Slug, string, string) error {
				changeCalled = true
				return nil
			},
		},
		middleware: []fiber.Handler{
			func(c fiber.Ctx) error {
				reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{
					Slug:   must.Slug("johndoe"),
					Source: reqctx.UserPrincipalSourcePersonalAccessToken,
				})
				return c.Next()
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/users/profile/password",
		api.UpdateUserPassword{CurrentPassword: "currentpass", NewPassword: "newsecurepass"},
	)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.False(t, changeCalled)
	assertErrorResponse(t, resp, apierror.UserPrincipalRequired())
}

func TestGetUsersProfile(t *testing.T) {
	t.Parallel()

	profile := *testUser
	profile.AvatarURL = "https://example.com/avatar.png"
	profile.PhoneNumber = "+48123123123"
	profile.JobTitle = "Operations Lead"
	profile.Timezone = "Europe/Warsaw"
	profile.MFAEmailEnabled = true

	app, store := newTestApp(t, testServerConfig{
		usersService: &fakeUsersService{
			GetUserBySlugFn: func(_ context.Context, gotSlug slug.Slug) (*model.User, error) {
				require.Equal(t, must.Slug("johndoe"), gotSlug)
				return &profile, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, model.Read, input.Action)
				require.Equal(t, model.ResourceProfile, input.Resource)
				return true, nil
			},
		},
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/users/profile", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, userToHTTP(&profile))
}
