package http

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

func TestPostSessions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        any
		loginUserFn func(ctx context.Context, email, password string) (*model.User, error)
		wantStatus  int
		wantBody    bool
		wantErr     *apierror.Error
	}{
		{
			name: "LogsInSuccessfullyAndReturnsUser",
			body: api.NewSession{Email: "john@example.com", Password: "secret123"},
			loginUserFn: func(_ context.Context, _, _ string) (*model.User, error) {
				return testUser, nil
			},
			wantStatus: http.StatusOK,
			wantBody:   true,
		},
		{
			name: "Returns401WhenCredentialsAreInvalid",
			body: api.NewSession{Email: "john@example.com", Password: "wrong"},
			loginUserFn: func(_ context.Context, _, _ string) (*model.User, error) {
				return nil, userssvc.ErrInvalidUsernameOrPassword
			},
			wantStatus: http.StatusUnauthorized,
			wantErr:    apierror.New(401, "INVALID_CREDENTIALS", "invalid credentials"),
		},
		{
			name: "Returns401WhenEmailIsNotVerified",
			body: api.NewSession{Email: "john@example.com", Password: "secret123"},
			loginUserFn: func(_ context.Context, _, _ string) (*model.User, error) {
				return nil, userssvc.ErrEmailNotVerified
			},
			wantStatus: http.StatusUnauthorized,
			wantErr:    apierror.New(401, "EMAIL_NOT_VERIFIED", "email not verified"),
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
					LoginUserFn: tt.loginUserFn,
				},
			})

			req := jsonRequest(t, http.MethodPost, "/sessions", tt.body)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody {
				expected := *testUserAPI
				expected.AuthenticationVersion = &testUser.AuthenticationVersion
				assertResponse(t, resp, &expected)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestPostSessions_ReturnsMFAChallengeWithoutCreatingSession(t *testing.T) {
	t.Parallel()

	expiresAt := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	app, _ := newTestApp(t, testServerConfig{
		usersService: &fakeUsersService{
			BeginLoginFn: func(_ context.Context, _, _ string) (*model.SessionLoginResult, error) {
				return &model.SessionLoginResult{
					MFARequired: true,
					MFAChallenge: &model.MFAChallenge{
						ChallengeToken: "challenge-token",
						ExpiresAt:      expiresAt,
					},
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/sessions", api.NewSession{
		Email:    "john@example.com",
		Password: "secret123",
	})
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Empty(t, resp.Cookies())
	assertResponse(t, resp, api.MFAChallenge{
		ChallengeToken: "challenge-token",
		ExpiresAt:      expiresAt,
	})
}

func TestPostSessions_RegeneratesPreAuthSession(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		usersService: &fakeUsersService{
			LoginUserFn: func(_ context.Context, _, _ string) (*model.User, error) {
				return testUser, nil
			},
		},
	})
	app.Post("/_test/preauth-session", func(c fiber.Ctx) error {
		sess, err := store.Get(c)
		if err != nil {
			return err
		}
		defer sess.Release()
		sess.Set("preauth_marker", "attacker-controlled")
		if err := sess.Save(); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusOK)
	})
	app.Get("/_test/session-user", func(c fiber.Ctx) error {
		return c.SendString(reqctx.GetUserSlug(c, store).String())
	})

	preAuthResp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/_test/preauth-session", nil))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, preAuthResp.Body.Close())
	}()
	oldCookie := requireSessionCookie(t, preAuthResp)

	loginReq := jsonRequest(t, http.MethodPost, "/sessions", api.NewSession{
		Email:    "john@example.com",
		Password: "secret123",
	})
	loginReq.AddCookie(oldCookie)
	loginResp, err := app.Test(loginReq)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, loginResp.Body.Close())
	}()
	require.Equal(t, http.StatusOK, loginResp.StatusCode)
	newCookie := requireSessionCookie(t, loginResp)
	require.NotEqual(t, oldCookie.Value, newCookie.Value)

	oldSessionReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/session-user", nil)
	oldSessionReq.AddCookie(oldCookie)
	oldSessionResp, err := app.Test(oldSessionReq)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, oldSessionResp.Body.Close())
	}()
	assertResponseBody(t, oldSessionResp, "")

	newSessionReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/session-user", nil)
	newSessionReq.AddCookie(newCookie)
	newSessionResp, err := app.Test(newSessionReq)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, newSessionResp.Body.Close())
	}()
	assertResponseBody(t, newSessionResp, testUser.Slug.String())
}

func TestVerifySessionMFA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		body               any
		verifySessionMFAFn func(ctx context.Context, challengeToken, code string) (*model.User, error)
		wantStatus         int
		wantBody           bool
		wantErr            *apierror.Error
	}{
		{
			name: "VerifiesChallengeAndReturnsUser",
			body: api.MFAVerification{ChallengeToken: "challenge-token", Code: "123456"},
			verifySessionMFAFn: func(_ context.Context, challengeToken, code string) (*model.User, error) {
				require.Equal(t, "challenge-token", challengeToken)
				require.Equal(t, "123456", code)
				user := *testUser
				user.HasLocalPassword = true
				return &user, nil
			},
			wantStatus: http.StatusOK,
			wantBody:   true,
		},
		{
			name: "Returns400WhenChallengeIsInvalid",
			body: api.MFAVerification{ChallengeToken: "challenge-token", Code: "000000"},
			verifySessionMFAFn: func(_ context.Context, _, _ string) (*model.User, error) {
				return nil, userssvc.ErrInvalidToken
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
				usersService: &fakeUsersService{
					VerifySessionMFAFn: tt.verifySessionMFAFn,
				},
			})

			req := jsonRequest(t, http.MethodPost, "/sessions/mfa/verify", tt.body)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			if tt.wantBody {
				want := *testUserAPI
				want.HasLocalPassword = true
				want.AuthenticationVersion = &testUser.AuthenticationVersion
				assertResponse(t, resp, &want)
				requireSessionCookie(t, resp)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func requireSessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()

	cookies := resp.Cookies()
	require.NotEmpty(t, cookies)
	for i := len(cookies) - 1; i >= 0; i-- {
		cookie := cookies[i]
		if cookie.Value == "" || cookie.MaxAge < 0 {
			continue
		}
		if strings.Contains(strings.ToLower(cookie.Name), "session") {
			return cookie
		}
	}

	require.Failf(t, "session cookie not found", "cookies: %#v", cookies)
	return nil
}

func assertResponseBody(t *testing.T, resp *http.Response, want string) {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, want, string(body))
}
