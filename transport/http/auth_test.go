package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/oidc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

func TestInitiateGoogleAuth(t *testing.T) {
	t.Parallel()

	// InitiateGoogleAuth should redirect to the Google OAuth URL.
	app, store := newTestApp(t, testServerConfig{
		oidcService: &fakeOIDCService{
			GetAuthURLFn: func(state, redirectURI string) string {
				return "https://accounts.google.com/authorize?state=" + state
			},
		},
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/google", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	assert.Equal(t, http.StatusFound, resp.StatusCode)

	location := resp.Header.Get("Location")
	assert.Contains(t, location, "https://accounts.google.com/authorize")
	authURL, err := url.Parse(location)
	require.NoError(t, err)
	state := authURL.Query().Get("state")
	decodedState, err := base64.RawURLEncoding.DecodeString(state)
	require.NoError(t, err)
	assert.Len(t, decodedState, 32)

	app.Get("/_test/oauth-state", func(c fiber.Ctx) error {
		sess, err := store.Get(c)
		if err != nil {
			return err
		}
		defer sess.Release()
		storedState, _ := sess.Get("oauth_state").(string)
		return c.SendString(storedState)
	})
	stateReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/oauth-state", nil)
	for _, cookie := range resp.Cookies() {
		stateReq.AddCookie(cookie)
	}
	stateResp, err := app.Test(stateReq)
	require.NoError(t, err)
	defer func() { require.NoError(t, stateResp.Body.Close()) }()
	assertResponseBody(t, stateResp, state)
}

func TestInitiateGoogleAuthUnavailable(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	app, _ := newTestApp(t, testServerConfig{
		oidcService: &fakeOIDCService{GetAuthURLError: &net.DNSError{Name: "private-issuer.invalid", Err: "provider-access-secret"}},
	})
	resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/google", nil))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"))
	assert.Empty(t, resp.Cookies())
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "GOOGLE_AUTH_UNAVAILABLE")
	assert.NotContains(t, string(body), "provider-access-secret")
	assert.NotContains(t, string(body), "private-issuer.invalid")
	assert.Contains(t, logs.String(), `"error.category":"dns"`)
	assert.NotContains(t, logs.String(), "private-issuer.invalid")
	assert.NotContains(t, logs.String(), "provider-access-secret")
}

func TestGoogleAuthFailureCategoryIsBounded(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"cancelled", context.Canceled, "request_canceled"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"dns", &net.DNSError{Err: "opaque-provider-secret"}, "dns"},
		{"tls", &tls.CertificateVerificationError{Err: fmt.Errorf("opaque-provider-secret")}, "tls"},
		{"network", &net.OpError{Err: fmt.Errorf("opaque-provider-secret")}, "network"},
		{"configuration", xerror.ValidationFailed(), "configuration"},
		{"unknown", fmt.Errorf("opaque-provider-secret"), "provider_discovery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, googleAuthFailureCategory(xerror.Unexpected().With(tc.err)))
		})
	}
}

func TestAuthenticationMethodsArePublicAndNotCached(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			app, _ := newTestApp(t, testServerConfig{oidcService: &fakeOIDCService{GoogleDisabled: !enabled}})
			resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/methods", nil))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.JSONEq(t, fmt.Sprintf(`{"google_sign_in_enabled":%t}`, enabled), string(body))
		})
	}
}

func TestHandleGoogleCallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		queryParams             string
		sessionState            string
		sessionExpired          bool
		exchangeCodeForTokenFn  func(ctx context.Context, code, redirectURI string) (*oidc.User, error)
		loginOIDCUserFn         func(ctx context.Context, oidcUser *model.OIDCUserInfo) (*model.User, error)
		loginOrCreateOIDCUserFn func(ctx context.Context, oidcUser *model.OIDCUserInfo) (*model.User, error)
		registrationEnabled     *bool
		externalOnboarding      *bool
		wantStatus              int
		wantLocationContains    string
	}{
		{
			name:         "SuccessfulCallbackRedirectsToFrontend",
			queryParams:  "code=valid-code&state=test-state",
			sessionState: "test-state",
			exchangeCodeForTokenFn: func(_ context.Context, _, _ string) (*oidc.User, error) {
				return &oidc.User{
					ProviderID:    "google-123",
					Email:         "john@example.com",
					EmailVerified: true,
					Name:          "John Doe",
				}, nil
			},
			loginOrCreateOIDCUserFn: func(_ context.Context, _ *model.OIDCUserInfo) (*model.User, error) {
				return testUser, nil
			},
			wantStatus:           http.StatusFound,
			wantLocationContains: "http://localhost:3000/workspace?return_to=%2Fdashboard",
		},
		{
			name:                 "GoogleCancellationWithoutCodeGetsRetryPage",
			queryParams:          "error=access_denied&state=test-state",
			sessionState:         "test-state",
			wantStatus:           http.StatusFound,
			wantLocationContains: "/auth/error?error=access_denied",
		},
		{
			name:                 "GoogleErrorIsEscapedInRedirect",
			queryParams:          "error=bad%20value%2F%3F&state=test-state",
			sessionState:         "test-state",
			wantStatus:           http.StatusFound,
			wantLocationContains: "/auth/error?error=bad+value%2F%3F",
		},
		{
			name:                 "MissingCodeGetsRetryPage",
			queryParams:          "state=test-state",
			sessionState:         "test-state",
			wantStatus:           http.StatusFound,
			wantLocationContains: "/auth/error?error=missing_code",
		},
		{
			name:                 "ExpiredStateGetsRetryPage",
			queryParams:          "code=valid-code&state=test-state",
			sessionState:         "test-state",
			sessionExpired:       true,
			wantStatus:           http.StatusFound,
			wantLocationContains: "/auth/error?error=invalid_state",
		},
		{
			name:                 "StateMismatchRedirectsToErrorPage",
			queryParams:          "code=valid-code&state=wrong-state",
			sessionState:         "correct-state",
			wantStatus:           http.StatusFound,
			wantLocationContains: "/auth/error?error=invalid_state",
		},
		{
			name:         "UnverifiedEmailRedirectsToErrorPage",
			queryParams:  "code=valid-code&state=test-state",
			sessionState: "test-state",
			exchangeCodeForTokenFn: func(_ context.Context, _, _ string) (*oidc.User, error) {
				return &oidc.User{
					Email:         "unverified@example.com",
					EmailVerified: false,
				}, nil
			},
			wantStatus:           http.StatusFound,
			wantLocationContains: "/auth/error?error=email_not_verified",
		},
		{
			name:         "EmailAlreadyExistsRedirectsToErrorPage",
			queryParams:  "code=valid-code&state=test-state",
			sessionState: "test-state",
			exchangeCodeForTokenFn: func(_ context.Context, _, _ string) (*oidc.User, error) {
				return &oidc.User{
					Email:         "existing@example.com",
					EmailVerified: true,
				}, nil
			},
			loginOrCreateOIDCUserFn: func(_ context.Context, _ *model.OIDCUserInfo) (*model.User, error) {
				return nil, userssvc.ErrEmailAlreadyExists
			},
			wantStatus:           http.StatusFound,
			wantLocationContains: "/auth/error?error=email_already_exists",
		},
		{
			name:         "RegistrationDisabledRedirectsToErrorPage",
			queryParams:  "code=valid-code&state=test-state",
			sessionState: "test-state",
			exchangeCodeForTokenFn: func(_ context.Context, _, _ string) (*oidc.User, error) {
				return &oidc.User{
					ProviderID:    "google-456",
					Email:         "new@example.com",
					EmailVerified: true,
					Name:          "New User",
				}, nil
			},
			loginOIDCUserFn: func(_ context.Context, _ *model.OIDCUserInfo) (*model.User, error) {
				return nil, userssvc.ErrUserNotFound
			},
			registrationEnabled:  ptr.Ptr(false),
			wantStatus:           http.StatusFound,
			wantLocationContains: "/auth/error?error=registration_disabled",
		},
		{
			name:         "RegistrationDisabledExistingProviderRedirectsToFrontend",
			queryParams:  "code=valid-code&state=test-state",
			sessionState: "test-state",
			exchangeCodeForTokenFn: func(_ context.Context, _, _ string) (*oidc.User, error) {
				return &oidc.User{
					ProviderID:    "google-123",
					Email:         "john@example.com",
					EmailVerified: true,
					Name:          "John Doe",
				}, nil
			},
			loginOIDCUserFn: func(_ context.Context, _ *model.OIDCUserInfo) (*model.User, error) {
				return testUser, nil
			},
			registrationEnabled:  ptr.Ptr(false),
			wantStatus:           http.StatusFound,
			wantLocationContains: "http://localhost:3000/workspace?return_to=%2Fdashboard",
		},
		{
			name:         "ExternalOnboardingDisabledRedirectsUnregisteredUserToSpecificError",
			queryParams:  "code=valid-code&state=test-state",
			sessionState: "test-state",
			exchangeCodeForTokenFn: func(_ context.Context, _, _ string) (*oidc.User, error) {
				return &oidc.User{
					ProviderID:    "google-789",
					Email:         "external@example.com",
					EmailVerified: true,
					Name:          "External User",
				}, nil
			},
			loginOIDCUserFn: func(_ context.Context, _ *model.OIDCUserInfo) (*model.User, error) {
				return nil, userssvc.ErrUserNotFound
			},
			externalOnboarding:   ptr.Ptr(false),
			wantStatus:           http.StatusFound,
			wantLocationContains: "/auth/error?error=external_access_disabled",
		},
		{
			name:         "ExternalOnboardingDisabledExistingProviderRedirectsToFrontend",
			queryParams:  "code=valid-code&state=test-state",
			sessionState: "test-state",
			exchangeCodeForTokenFn: func(_ context.Context, _, _ string) (*oidc.User, error) {
				return &oidc.User{
					ProviderID:    "google-123",
					Email:         "john@example.com",
					EmailVerified: true,
					Name:          "John Doe",
				}, nil
			},
			loginOIDCUserFn: func(_ context.Context, _ *model.OIDCUserInfo) (*model.User, error) {
				return testUser, nil
			},
			externalOnboarding:   ptr.Ptr(false),
			wantStatus:           http.StatusFound,
			wantLocationContains: "http://localhost:3000/workspace?return_to=%2Fdashboard",
		},
		{
			name:         "ExchangeCodeFailureRedirectsToErrorPage",
			queryParams:  "code=bad-code&state=test-state",
			sessionState: "test-state",
			exchangeCodeForTokenFn: func(_ context.Context, _, _ string) (*oidc.User, error) {
				return nil, fmt.Errorf("token exchange failed")
			},
			wantStatus:           http.StatusFound,
			wantLocationContains: "/auth/error?error=authentication_failed",
		},
		{
			name:         "GenericLoginErrorRedirectsToErrorPage",
			queryParams:  "code=valid-code&state=test-state",
			sessionState: "test-state",
			exchangeCodeForTokenFn: func(_ context.Context, _, _ string) (*oidc.User, error) {
				return &oidc.User{
					Email:         "new@example.com",
					EmailVerified: true,
				}, nil
			},
			loginOrCreateOIDCUserFn: func(_ context.Context, _ *model.OIDCUserInfo) (*model.User, error) {
				return nil, fmt.Errorf("database connection lost")
			},
			wantStatus:           http.StatusFound,
			wantLocationContains: "/auth/error?error=authentication_failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				selfServiceRegistrationEnabled: tt.registrationEnabled,
				externalOnboardingEnabled:      tt.externalOnboarding,
				usersService: &fakeUsersService{
					LoginOIDCUserFn:         tt.loginOIDCUserFn,
					LoginOrCreateOIDCUserFn: tt.loginOrCreateOIDCUserFn,
				},
				oidcService: &fakeOIDCService{
					ExchangeCodeForTokenFn: tt.exchangeCodeForTokenFn,
				},
			})

			// If we need oauth_state in the session, set it via a temp route
			var sessionCookies []*http.Cookie
			if tt.sessionState != "" {
				app.Post("/_test/set-state", func(c fiber.Ctx) error {
					sess, err := store.Get(c)
					if err != nil {
						return err
					}
					defer sess.Release()
					sess.Set("oauth_state", tt.sessionState)
					sess.Set("oauth_expires_at", time.Now().Add(10*time.Minute).Unix())
					if tt.sessionExpired {
						sess.Set("oauth_expires_at", time.Now().Add(-time.Minute).Unix())
					}
					if err := sess.Save(); err != nil {
						return err
					}
					return c.SendStatus(fiber.StatusOK)
				})

				stateReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/_test/set-state", nil)
				stateResp, err := app.Test(stateReq)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, stateResp.StatusCode)
				defer func() {
					require.NoError(t, stateResp.Body.Close())
				}()
				sessionCookies = stateResp.Cookies()
			}

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/google/callback?"+tt.queryParams, nil)
			for _, cookie := range sessionCookies {
				req.AddCookie(cookie)
			}

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantLocationContains != "" {
				location := resp.Header.Get("Location")
				assert.Contains(t, location, tt.wantLocationContains)
			}
		})
	}
}

func TestHandleGoogleCallback_RegeneratesPreAuthSession(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		usersService: &fakeUsersService{
			LoginOrCreateOIDCUserFn: func(_ context.Context, _ *model.OIDCUserInfo) (*model.User, error) {
				return testUser, nil
			},
		},
		oidcService: &fakeOIDCService{
			ExchangeCodeForTokenFn: func(_ context.Context, _, _ string) (*oidc.User, error) {
				return &oidc.User{
					ProviderID:    "google-123",
					Email:         "john@example.com",
					EmailVerified: true,
					Name:          "John Doe",
				}, nil
			},
		},
	})
	app.Post("/_test/preauth-oidc-session", func(c fiber.Ctx) error {
		sess, err := store.Get(c)
		if err != nil {
			return err
		}
		defer sess.Release()
		sess.Set("oauth_state", "test-state")
		sess.Set("oauth_expires_at", time.Now().Add(10*time.Minute).Unix())
		sess.Set("preauth_marker", "attacker-controlled")
		if err := sess.Save(); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusOK)
	})
	app.Get("/_test/oidc-session-user", func(c fiber.Ctx) error {
		return c.SendString(reqctx.GetUserSlug(c, store).String())
	})

	preAuthResp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/_test/preauth-oidc-session", nil))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, preAuthResp.Body.Close())
	}()
	oldCookie := requireSessionCookie(t, preAuthResp)

	callbackReq := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/auth/google/callback?code=valid-code&state=test-state",
		nil)

	callbackReq.AddCookie(oldCookie)
	callbackResp, err := app.Test(callbackReq)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, callbackResp.Body.Close())
	}()
	require.Equal(t, http.StatusFound, callbackResp.StatusCode)
	newCookie := requireSessionCookie(t, callbackResp)
	require.NotEqual(t, oldCookie.Value, newCookie.Value)

	oldSessionReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/oidc-session-user", nil)
	oldSessionReq.AddCookie(oldCookie)
	oldSessionResp, err := app.Test(oldSessionReq)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, oldSessionResp.Body.Close())
	}()
	assertResponseBody(t, oldSessionResp, "")

	newSessionReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/oidc-session-user", nil)
	newSessionReq.AddCookie(newCookie)
	newSessionResp, err := app.Test(newSessionReq)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, newSessionResp.Body.Close())
	}()
	assertResponseBody(t, newSessionResp, testUser.Slug.String())
}
