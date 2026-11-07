package http

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

func TestBackendSecurity_RejectsMissingCSRFForCookieAuthenticatedStateChange(t *testing.T) {
	t.Parallel()

	app, store := newSecuredBackendTestApp(t, securedBackendTestConfig{
		usersService: &fakeUsersService{
			UpdateProfileFn: func(_ context.Context, _ slug.Slug, _ model.UpdateUserProfile) (*model.User, error) {
				t.Fatal("profile update must not run when CSRF token is missing")
				return &model.User{}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPatch, "/users/profile", map[string]string{
		"full_name": "Jane Example",
	})
	resp := authenticatedRequest(t, app, store, slug.Slug("csrf-user"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.New(
		http.StatusForbidden,
		apierror.CodeCSRFInvalid,
		"Invalid or missing CSRF token",
	))
}

func TestBackendSecurity_AllowsValidCSRFForCookieAuthenticatedStateChange(t *testing.T) {
	t.Parallel()

	app, _ := newSecuredBackendTestApp(t, securedBackendTestConfig{
		usersService: &fakeUsersService{
			UpdateProfileFn: func(_ context.Context, userSlug slug.Slug, update model.UpdateUserProfile) (*model.User, error) {
				require.Equal(t, slug.Slug("csrf-user"), userSlug)
				require.NotNil(t, update.FullName)
				require.Equal(t, "Jane Example", *update.FullName)
				return &model.User{
					Slug:     userSlug,
					Email:    "jane@example.com",
					FullName: *update.FullName,
					Role:     model.RoleUser,
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
				return true, nil
			},
		},
	})

	sessionCookies := backendSecurityLoginCookies(t, app)
	csrfToken, csrfCookies := backendSecurityCSRFToken(t, app, sessionCookies)

	req := jsonRequest(t, http.MethodPatch, "/users/profile", map[string]string{
		"full_name": "Jane Example",
	})
	for _, cookie := range append(sessionCookies, csrfCookies...) {
		req.AddCookie(cookie)
	}
	req.Header.Set("X-CSRF-Token", csrfToken)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestBackendSecurity_SafeAuthenticatedRequestInitializesCSRFToken(t *testing.T) {
	t.Parallel()

	app, _ := newSecuredBackendTestApp(t, securedBackendTestConfig{
		usersService: &fakeUsersService{
			UpdateProfileFn: func(_ context.Context, userSlug slug.Slug, update model.UpdateUserProfile) (*model.User, error) {
				require.Equal(t, slug.Slug("csrf-user"), userSlug)
				require.NotNil(t, update.FullName)
				require.Equal(t, "Jane Example", *update.FullName)
				return &model.User{
					Slug:     userSlug,
					Email:    "jane@example.com",
					FullName: *update.FullName,
					Role:     model.RoleUser,
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
				return true, nil
			},
		},
	})

	sessionCookies := backendSecurityLoginCookies(t, app)
	csrfReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/authenticated-csrf", nil)
	for _, cookie := range sessionCookies {
		csrfReq.AddCookie(cookie)
	}
	csrfResp, err := app.Test(csrfReq)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, csrfResp.Body.Close())
	}()
	require.Equal(t, http.StatusOK, csrfResp.StatusCode)
	csrfToken := strings.TrimSpace(csrfResp.Header.Get(httpsecurity.CSRFHeader))
	require.NotEmpty(t, csrfToken)

	req := jsonRequest(t, http.MethodPatch, "/users/profile", map[string]string{
		"full_name": "Jane Example",
	})
	for _, cookie := range append(sessionCookies, csrfResp.Cookies()...) {
		req.AddCookie(cookie)
	}
	req.Header.Set(httpsecurity.CSRFHeader, csrfToken)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestBackendSecurity_ThrottlesLoginAttempts(t *testing.T) {
	t.Parallel()

	app, _ := newSecuredBackendTestApp(t, securedBackendTestConfig{
		usersService: &fakeUsersService{
			LoginUserFn: func(_ context.Context, _, _ string) (*model.User, error) {
				return nil, userssvc.ErrInvalidUsernameOrPassword
			},
		},
		authThrottleMax: 2,
	})

	for i := 0; i < 2; i++ {
		req := jsonRequest(t, http.MethodPost, "/sessions", api.NewSession{
			Email:    "jane@example.com",
			Password: "wrong-password",
		})
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.Empty(t, resp.Cookies())
	}

	req := jsonRequest(t, http.MethodPost, "/sessions", api.NewSession{
		Email:    "jane@example.com",
		Password: "wrong-password",
	})
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Empty(t, resp.Cookies())
	assertErrorResponse(t, resp, apierror.New(
		http.StatusTooManyRequests,
		apierror.CodeRateLimited,
		"Too many attempts. Try again later.",
	))
}

func TestBackendSecurity_ThrottlesMFAVerificationAttempts(t *testing.T) {
	t.Parallel()

	app, _ := newSecuredBackendTestApp(t, securedBackendTestConfig{
		usersService: &fakeUsersService{
			VerifySessionMFAFn: func(_ context.Context, _, _ string) (*model.User, error) {
				return nil, userssvc.ErrInvalidToken
			},
		},
		authThrottleMax: 2,
	})

	body := api.MFAVerification{ChallengeToken: "challenge-token", Code: "000000"}
	for i := 0; i < 2; i++ {
		req := jsonRequest(t, http.MethodPost, "/sessions/mfa/verify", body)
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Empty(t, resp.Cookies())
	}

	req := jsonRequest(t, http.MethodPost, "/sessions/mfa/verify", body)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Empty(t, resp.Cookies())
	assertErrorResponse(t, resp, apierror.New(
		http.StatusTooManyRequests,
		apierror.CodeRateLimited,
		"Too many attempts. Try again later.",
	))
}

func TestBackendSecurity_MountedAPIBudgets(t *testing.T) {
	t.Parallel()
	for _, route := range []struct {
		path   string
		body   any
		status int
	}{
		{"/sessions", api.NewSession{Email: "jane@example.com", Password: "wrong-password"}, http.StatusUnauthorized},
		{"/sessions/mfa/verify", api.MFAVerification{ChallengeToken: "challenge-token", Code: "000000"}, http.StatusBadRequest},
		{"/organizations/acme/data-sources/source/schema", nil, http.StatusUnauthorized},
	} {
		t.Run(route.path, func(t *testing.T) {
			t.Parallel()
			apiApp, _ := newSecuredBackendTestApp(t, securedBackendTestConfig{
				authThrottleMax: 2,
				usersService: &fakeUsersService{
					LoginUserFn: func(context.Context, string, string) (*model.User, error) {
						return nil, userssvc.ErrInvalidUsernameOrPassword
					},
					VerifySessionMFAFn: func(context.Context, string, string) (*model.User, error) {
						return nil, userssvc.ErrInvalidToken
					},
				},
			})
			app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
			app.Use("/api", apiApp)
			// Fiber serves these aliases as the same route; they must share its budget.
			for i, path := range []string{"/api" + route.path, "/API" + route.path + "/", "/api" + route.path} {
				resp, err := app.Test(jsonRequest(t, http.MethodPost, path, route.body))
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				if i == 2 {
					assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
					assert.NotEmpty(t, resp.Header.Get("Retry-After"))
				} else {
					assert.Equal(t, route.status, resp.StatusCode)
				}
			}
		})
	}
}

func TestBackendSecurity_ThrottlesPasswordChangeAttempts(t *testing.T) {
	t.Parallel()

	app, _ := newSecuredBackendTestApp(t, securedBackendTestConfig{
		usersService: &fakeUsersService{
			ChangePasswordFn: func(_ context.Context, userSlug slug.Slug, currentPassword, newPassword string) error {
				require.Equal(t, slug.Slug("csrf-user"), userSlug)
				require.Equal(t, "wrong-password", currentPassword)
				require.Equal(t, "newsecurepass", newPassword)
				return userssvc.ErrInvalidUsernameOrPassword
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
				return true, nil
			},
		},
		authThrottleMax: 2,
	})

	sessionCookies := backendSecurityLoginCookies(t, app)
	csrfToken, csrfCookies := backendSecurityCSRFToken(t, app, sessionCookies)
	cookies := make([]*http.Cookie, 0, len(sessionCookies)+len(csrfCookies))
	cookies = append(cookies, sessionCookies...)
	cookies = append(cookies, csrfCookies...)
	body := api.UpdateUserPassword{CurrentPassword: "wrong-password", NewPassword: "newsecurepass"}

	for i := 0; i < 2; i++ {
		req := jsonRequest(t, http.MethodPut, "/users/profile/password", body)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		req.Header.Set(httpsecurity.CSRFHeader, csrfToken)
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}

	req := jsonRequest(t, http.MethodPut, "/users/profile/password", body)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	req.Header.Set(httpsecurity.CSRFHeader, csrfToken)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.New(
		http.StatusTooManyRequests,
		apierror.CodeRateLimited,
		"Too many attempts. Try again later.",
	))
}

func TestBackendSecurity_SharedLimiterSurvivesMiddlewareReplacement(t *testing.T) {
	t.Parallel()

	limiter := httpsecurity.NewMemoryAbuseLimiter()
	newApp := func() *fiber.App {
		app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
		app.Use(NewBackendSecurityMiddlewareForApp(
			session.NewStore(), slog.New(slog.DiscardHandler), limiter,
			1, time.Minute, 0, 0, 0, time.Minute,
		))
		app.Post("/sessions", func(c fiber.Ctx) error { return apierror.Unauthorized() })
		return app
	}
	request := func(app *fiber.App) *http.Response {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/sessions", strings.NewReader(`{"email":"restart@example.com"}`))
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		req.RemoteAddr = "192.0.2.10:1234"
		resp, err := app.Test(req)
		require.NoError(t, err)
		return resp
	}

	first := request(newApp())
	require.NoError(t, first.Body.Close())
	require.Equal(t, http.StatusUnauthorized, first.StatusCode)

	second := request(newApp())
	defer func() { require.NoError(t, second.Body.Close()) }()
	require.Equal(t, http.StatusTooManyRequests, second.StatusCode)
	assert.NotEmpty(t, second.Header.Get(fiber.HeaderRetryAfter))
	assertErrorResponse(t, second, apierror.RateLimited())
}

func TestBackendSecurity_DoesNotBlockExistingAccountTraffic(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(newBackendSecurityMiddleware(backendSecurityConfig{
		sessionStore: session.NewStore(),
		logger:       slog.New(slog.DiscardHandler),
	}))
	app.Post("/sessions", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusCreated) })
	app.Get("/organizations", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusOK) })

	login, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/sessions", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, login.StatusCode)
	require.NoError(t, login.Body.Close())

	organizations, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, organizations.StatusCode)
	require.NoError(t, organizations.Body.Close())
}

func TestBackendSecurity_DoesNotTrustForwardedAddressFromUntrustedPeer(t *testing.T) {
	t.Parallel()

	limiter := &recordingAbuseLimiter{}
	app := fiber.New(fiber.Config{
		ErrorHandler:       apierror.Middleware(),
		ProxyHeader:        fiber.HeaderXForwardedFor,
		TrustProxy:         true,
		TrustProxyConfig:   fiber.TrustProxyConfig{Proxies: []string{"10.0.0.1"}},
		EnableIPValidation: true,
	})
	app.Use(newBackendSecurityMiddleware(backendSecurityConfig{
		sessionStore:       session.NewStore(),
		logger:             slog.New(slog.DiscardHandler),
		authThrottleMax:    5,
		authThrottleWindow: time.Minute,
		abuseLimiter:       limiter,
	}))
	app.Post("/sessions", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusUnauthorized) })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/sessions", strings.NewReader(`{"email":"proxy@example.com"}`))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	req.Header.Set(fiber.HeaderXForwardedFor, "203.0.113.99")
	req.RemoteAddr = "192.0.2.44:4321"
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	budgets := limiter.Budgets()
	require.NotEmpty(t, budgets)
	assert.Contains(t, budgets[0].Key, "0.0.0.0")
	assert.NotContains(t, budgets[0].Key, "203.0.113.99")
}

func TestBackendSecurity_AuthorizedClientsShareOrganizationExecutionBudget(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(newBackendSecurityMiddleware(backendSecurityConfig{
		sessionStore:       session.NewStore(),
		logger:             slog.New(slog.DiscardHandler),
		expensiveClientMax: 100,
		expensiveTenantMax: 2,
		expensiveWindow:    2 * time.Minute,
		abuseLimiter:       &tenantOnlyAbuseLimiter{shared: httpsecurity.NewMemoryAbuseLimiter()},
	}))
	app.Post("/organizations/acme/data-sources/source/schema", func(c fiber.Ctx) error {
		if err := chargeAuthorizedOrganizationAbuseControl(c, "acme", "connector.schema_import", false); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusAccepted)
	})

	for i := 1; i <= 2; i++ {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/data-sources/source/schema", nil)
		req.RemoteAddr = "192.0.2." + strconv.Itoa(i) + ":1234"
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusAccepted, resp.StatusCode)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/data-sources/source/schema", nil)
	req.RemoteAddr = "198.51.100.1:1234"
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.RateLimited())
}

func TestBackendExpensiveOperationScopes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		method string
		path   string
	}{
		"api key":                {http.MethodPost, "/organizations/acme/api-keys"},
		"PAT rotation":           {http.MethodPost, "/users/profile/personal-access-tokens/token/rotate"},
		"provider callback":      {http.MethodGet, "/connectors/provider/hubspot/callback"},
		"data source create":     {http.MethodPost, "/organizations/acme/data-sources"},
		"data source update":     {http.MethodPatch, "/organizations/acme/data-sources/source"},
		"connection test":        {http.MethodPost, "/organizations/acme/data-sources/source/test-connection"},
		"schema import":          {http.MethodPost, "/organizations/acme/data-sources/source/schema"},
		"query":                  {http.MethodPost, "/organizations/acme/data-sources/source/queries/report/execute"},
		"run":                    {http.MethodPost, "/organizations/acme/projects/project/environments/prod/pipelines/sync:execute"},
		"pipeline update":        {http.MethodPut, "/organizations/acme/projects/project/pipelines/sync"},
		"branches":               {http.MethodGet, "/organizations/acme/data-sources/source/branches"},
		"credential update":      {http.MethodPatch, "/organizations/acme/data-sources/source/credentials"},
		"provider refresh":       {http.MethodPost, "/organizations/acme/data-sources/source/provider-grant/refresh"},
		"provider revoke":        {http.MethodPost, "/organizations/acme/data-sources/source/provider-grant/revoke"},
		"pipeline backend adopt": {http.MethodPut, "/organizations/acme/projects/project/pipeline-backend"},
		"pipeline reconcile":     {http.MethodPost, "/organizations/acme/projects/project/pipelines:reconcile"},
		"pipeline draft publish": {http.MethodPost, "/organizations/acme/projects/project/pipelines/sync/draft:publish"},
		"project delete":         {http.MethodDelete, "/organizations/acme/projects/project"},
		"alert delivery test":    {http.MethodPost, "/organizations/acme/alert-destinations/pager/test"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			scope, _ := backendExpensiveOperation(test.method, test.path)
			assert.NotEmpty(t, scope)
			mountedScope, _ := backendExpensiveOperation(test.method, "/api"+test.path)
			assert.Equal(t, scope, mountedScope)
		})
	}
	for name, route := range map[string]struct {
		method string
		path   string
	}{
		"query read":       {http.MethodGet, tests["query"].path},
		"cached schema":    {http.MethodGet, "/organizations/acme/data-sources/source/schema"},
		"provider install": {http.MethodPost, "/organizations/acme/connectors/provider/hubspot/installs"},
		"provider rotate":  {http.MethodPost, "/organizations/acme/data-sources/source/provider-grant/rotate"},
		"mount lookalike":  {http.MethodPost, "/apix/organizations/acme/data-sources/source/schema"},
		"nested mount":     {http.MethodPost, "/api/api/organizations/acme/data-sources/source/schema"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			scope, _ := backendExpensiveOperation(route.method, route.path)
			assert.Empty(t, scope)
		})
	}
}

func TestBackendAbuseBudgetsUseConfiguredClientLimitAndWindow(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Post("/organizations/acme/data-sources/source/schema", func(c fiber.Ctx) error {
		budgets := backendAbuseBudgets(c, backendSecurityConfig{
			sessionStore:       session.NewStore(),
			expensiveClientMax: 7,
			expensiveTenantMax: 9,
			expensiveWindow:    3 * time.Minute,
		})
		require.Len(t, budgets, 1)
		assert.Equal(t, 7, budgets[0].Limit)
		assert.Equal(t, 3*time.Minute, budgets[0].Window)
		return c.SendStatus(fiber.StatusNoContent)
	})
	resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/data-sources/source/schema", nil))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestBackendSecurity_DeniedOrganizationRequestDoesNotSpendTenantBudget(t *testing.T) {
	t.Parallel()

	limiter := &recordingAbuseLimiter{}
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(newBackendSecurityMiddleware(backendSecurityConfig{
		sessionStore:       session.NewStore(),
		logger:             slog.New(slog.DiscardHandler),
		expensiveClientMax: 10,
		expensiveTenantMax: 1,
		expensiveWindow:    time.Minute,
		abuseLimiter:       limiter,
	}))
	app.Post("/organizations/victim/data-sources/source/schema", func(c fiber.Ctx) error {
		return apierror.Forbidden(model.ResourceDataSources.String(), model.Update.String())
	})

	resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/victim/data-sources/source/schema", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	for _, budget := range limiter.Budgets() {
		assert.NotContains(t, budget.Scope, ".tenant")
		assert.NotContains(t, budget.Key, "victim")
	}
}

type recordingAbuseLimiter struct {
	mu      sync.Mutex
	budgets []httpsecurity.AbuseLimit
}

// tenantOnlyAbuseLimiter models requests arriving from distinct client
// identities while preserving their shared organization budget.
type tenantOnlyAbuseLimiter struct {
	shared httpsecurity.AbuseLimiter
}

func (l *tenantOnlyAbuseLimiter) Attempt(
	ctx context.Context,
	budget httpsecurity.AbuseLimit,
) (httpsecurity.AbuseDecision, error) {
	if strings.HasSuffix(budget.Scope, ".client") {
		return httpsecurity.AbuseDecision{Allowed: true, ResetAt: time.Now().Add(time.Minute)}, nil
	}
	return l.shared.Attempt(ctx, budget)
}

func (l *recordingAbuseLimiter) Attempt(
	_ context.Context,
	budget httpsecurity.AbuseLimit,
) (httpsecurity.AbuseDecision, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.budgets = append(l.budgets, budget)
	return httpsecurity.AbuseDecision{Allowed: true, ResetAt: time.Now().Add(time.Minute)}, nil
}

func (l *recordingAbuseLimiter) Budgets() []httpsecurity.AbuseLimit {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]httpsecurity.AbuseLimit(nil), l.budgets...)
}

type securedBackendTestConfig struct {
	usersService    userssvc.UsersService
	authService     fakeAuthService
	authThrottleMax int
	abuseLimiter    httpsecurity.AbuseLimiter
}

func newSecuredBackendTestApp(
	t *testing.T,
	cfg securedBackendTestConfig,
) (*fiber.App, *session.Store) {
	t.Helper()

	store := session.NewStore()
	if cfg.authThrottleMax == 0 {
		cfg.authThrottleMax = 10
	}
	if cfg.usersService == nil {
		cfg.usersService = &fakeUsersService{}
	}

	srv := &server{
		usersService:         cfg.usersService,
		oidcService:          &fakeOIDCService{},
		sessionStore:         store,
		authorizationService: &cfg.authService,
		orgService:           &fakeOrgService{},
		projectsService:      &fakeProjectsService{},
		membersService:       &fakeMembersService{},
		rolesService:         &fakeRolesService{},
		datasourcesService:   &fakeDataSourcesService{},
		queriesService:       &fakeQueriesService{},
		pipelinesService:     &fakePipelinesService{},
		frontendURL:          "http://localhost:3000",
		googleRedirectURI:    "http://localhost:8080/auth/google/callback",
	}

	app := fiber.New(fiber.Config{
		ErrorHandler: apierror.Middleware(),
	})
	app.Post("/_test/login", func(c fiber.Ctx) error {
		if err := reqctx.SetUserSlug(c, store, slug.Slug("csrf-user")); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusOK)
	})
	app.Get("/_test/csrf", func(c fiber.Ctx) error {
		token, err := httpsecurity.EnsureCSRFToken(c, store)
		if err != nil {
			return err
		}
		c.Set(httpsecurity.CSRFHeader, token)
		return c.SendStatus(fiber.StatusOK)
	})
	app.Use(newBackendSecurityMiddleware(backendSecurityConfig{
		sessionStore:       store,
		logger:             slog.New(slog.DiscardHandler),
		csrfEnabled:        true,
		authThrottleMax:    cfg.authThrottleMax,
		authThrottleWindow: time.Minute,
		abuseLimiter:       cfg.abuseLimiter,
		expensiveClientMax: 2,
		expensiveWindow:    time.Minute,
	}))
	app.Get("/_test/authenticated-csrf", func(c fiber.Ctx) error {
		if reqctx.GetUserSlug(c, store) == "" {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		return c.SendStatus(fiber.StatusOK)
	})
	api.RegisterHandlers(app, srv)

	return app, store
}

func backendSecurityLoginCookies(t *testing.T, app *fiber.App) []*http.Cookie {
	t.Helper()

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/_test/login", nil))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp.Cookies()
}

func backendSecurityCSRFToken(
	t *testing.T,
	app *fiber.App,
	sessionCookies []*http.Cookie,
) (string, []*http.Cookie) {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/csrf", nil)
	for _, cookie := range sessionCookies {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	token := strings.TrimSpace(resp.Header.Get("X-CSRF-Token"))
	require.NotEmpty(t, token)
	return token, resp.Cookies()
}

func TestBackendSecurity_RequiresInjectedLimiter(t *testing.T) {
	t.Parallel()
	require.Panics(t, func() {
		NewBackendSecurityMiddlewareForApp(nil, nil, nil, 1, time.Minute, 1, 1, 1, time.Minute)
	})
}
