package frontend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestProtectedRouteRequiresBackendSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		slug   string
		want   int
	}{
		{"current session", http.StatusOK, "test-user", http.StatusBadRequest},
		{"revoked session", http.StatusUnauthorized, "", http.StatusSeeOther},
		{"closed account", http.StatusNotFound, "", http.StatusSeeOther},
		{"different subject", http.StatusOK, "other-user", http.StatusServiceUnavailable},
		{"backend unavailable", http.StatusBadGateway, "", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.NotEmpty(t, r.Header.Get("Cookie"))
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/organizations" {
					assert.Equal(t, "current session", tc.name)
					_, _ = io.WriteString(w, `{"organizations":[]}`)
					return
				}
				assert.Equal(t, "/api/users/profile", r.URL.Path)
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_, _ = io.WriteString(w, `{"slug":"`+tc.slug+`"}`)
				}
			}))
			defer backend.Close()
			store := session.NewStore()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/member-invitations/accept?source=email", nil)
			request.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			response, err := app.Test(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			require.Equal(t, tc.want, response.StatusCode)
			require.Equal(t, "no-store", response.Header.Get(fiber.HeaderCacheControl))
			if tc.want == http.StatusSeeOther {
				assert.Equal(t, "1", response.Header.Get(browserSessionEndedHeader))
				assert.Equal(t, "/auth/login?return_to=%2Fmember-invitations%2Faccept%3Fsource%3Demail", response.Header.Get("Location"))
			} else {
				assert.Empty(t, response.Header.Get(browserSessionEndedHeader))
			}
		})
	}
}

func TestProtectedDashboardRedirectPreservesReturnTo(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unauthenticated redirect contacted backend: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(backend.Close)
	router, err := newHandlerTestRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})
	response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dashboard?project_slug=ops", nil))
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	location, err := url.Parse(response.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "/auth/login", location.Path)
	require.Equal(t, "/dashboard?project_slug=ops", location.Query().Get("return_to"))
}

func TestBrowserSessionStatusReadsExpiryWithoutSaving(t *testing.T) {
	t.Parallel()
	store := session.NewStore()
	var profileStatus atomic.Int64
	profileStatus.Store(http.StatusOK)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(profileStatus.Load()))
		_, _ = io.WriteString(w, `{"slug":"test-user"}`)
	}))
	t.Cleanup(backend.Close)
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	cookie := getAuthenticatedSessionCookie(t, app)
	id := strings.SplitN(cookie, "=", 2)[1]
	router.SetBrowserSessionExpiry(func(_ context.Context, key string) (int64, error) {
		assert.Equal(t, id, key)
		return time.Now().Add(30 * time.Minute).Unix(), nil
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, browserSessionStatusPath, nil)
	req.Header.Set("Cookie", cookie)
	response, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.Equal(t, "no-store", response.Header.Get(fiber.HeaderCacheControl))
	require.Empty(t, response.Header.Values(fiber.HeaderSetCookie))
	expiry := response.Header.Get(browserSessionTTLHeader)
	require.Regexp(t, `^[0-9a-f]{32}:[0-9]+$`, expiry)
	remaining, err := strconv.Atoi(strings.SplitN(expiry, ":", 2)[1])
	require.NoError(t, err)
	require.InDelta(t, (30 * time.Minute).Milliseconds(), remaining, 5000)
	require.NotContains(t, expiry, id)

	for _, tc := range []struct {
		backendStatus, wantStatus int
		wantEnded                 string
	}{
		{http.StatusUnauthorized, http.StatusUnauthorized, "1"},
		{http.StatusServiceUnavailable, http.StatusServiceUnavailable, ""},
	} {
		profileStatus.Store(int64(tc.backendStatus))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, browserSessionStatusPath, nil)
		req.Header.Set("Cookie", cookie)
		response, err := app.Test(req)
		require.NoError(t, err)
		_ = response.Body.Close()
		require.Equal(t, tc.wantStatus, response.StatusCode)
		require.Equal(t, tc.wantEnded, response.Header.Get(browserSessionEndedHeader))
		require.Empty(t, response.Header.Get(browserSessionTTLHeader))
		require.Empty(t, response.Header.Values(fiber.HeaderSetCookie))
	}

	req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, browserSessionStatusPath, nil)
	anonymous, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = anonymous.Body.Close() }()
	require.Equal(t, http.StatusUnauthorized, anonymous.StatusCode)
	require.Equal(t, "1", anonymous.Header.Get(browserSessionEndedHeader))
	require.Empty(t, anonymous.Header.Get(browserSessionTTLHeader))
	require.Empty(t, anonymous.Header.Values(fiber.HeaderSetCookie))
}

func TestExpiryLookupFailurePreservesAuthorizedMutationResult(t *testing.T) {
	t.Parallel()
	store := session.NewStore()
	router, err := newHandlerTestRouter("http://127.0.0.1:1", slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	var lookupUnavailable atomic.Bool
	router.SetBrowserSessionExpiry(func(context.Context, string) (int64, error) {
		if lookupUnavailable.Load() {
			return 0, assert.AnError
		}
		return time.Now().Add(time.Minute).Unix(), nil
	})

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	app.All("/_test/mutation", router.requireAuth, func(c fiber.Ctx) error {
		lookupUnavailable.Store(true)
		return c.Type("html").SendString("<html><head></head><body>Synthetic result</body></html>")
	})
	cookie := getAuthenticatedSessionCookie(t, app)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		lookupUnavailable.Store(false)
		req := httptest.NewRequestWithContext(t.Context(), method, "/_test/mutation", nil)
		req.Header.Set("Cookie", cookie)
		if method == http.MethodPost {
			addCSRFToken(t, app, req)
		}
		response, err := app.Test(req)
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		if method == http.MethodGet {
			require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
			require.NotContains(t, string(body), "Synthetic result")
			continue
		}
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.Contains(t, string(body), "Synthetic result")
		require.Contains(t, string(body), `name="braidplane-session-expiry"`)
	}
}

func TestFrontendRouteNeedsSecuritySession(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{name: "landing", method: http.MethodGet, path: "/", want: false},
		{name: "privacy", method: http.MethodGet, path: "/privacy", want: false},
		{name: "pricing", method: http.MethodGet, path: "/pricing", want: false},
		{name: "unsafe pricing", method: http.MethodPost, path: "/pricing", want: true},
		{name: "roadmap", method: http.MethodGet, path: "/roadmap", want: false},
		{name: "solutions", method: http.MethodGet, path: "/solutions", want: false},
		{name: "settlement example", method: http.MethodGet, path: "/solutions/settlement-review", want: false},
		{name: "supplier example", method: http.MethodGet, path: "/solutions/supplier-price-desk", want: false},
		{name: "unknown solution", method: http.MethodGet, path: "/solutions/unknown", want: true},
		{name: "privacy trailing slash", method: http.MethodGet, path: "/privacy/", want: false},
		{name: "unsafe privacy", method: http.MethodPost, path: "/privacy/", want: true},
		{name: "robots", method: http.MethodGet, path: "/robots.txt", want: false},
		{name: "sitemap", method: http.MethodGet, path: "/sitemap.xml", want: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var got bool
			app := fiber.New()
			app.Use(func(c fiber.Ctx) error {
				got = frontendRouteNeedsSecuritySession(c)
				return c.SendStatus(http.StatusNoContent)
			})

			resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), testCase.method, testCase.path, nil))
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			assert.Equal(t, testCase.want, got)
		})
	}
}

func TestRouter_AuthForms_RenderCSRFToken(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := newHandlerTestRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Set(fiber.HeaderContentSecurityPolicy, "form-action 'self'")
		return c.Next()
	})
	router.RegisterRoutes(app, CommunityPresentation{})

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/login", nil))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "form-action 'self', frame-ancestors 'none'", resp.Header.Get(fiber.HeaderContentSecurityPolicy))
	assert.Contains(t, string(body), `name="_csrf"`)
	assert.NotEmpty(t, extractCSRFToken(t, string(body)))

	textResponse, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/robots.txt", nil))
	require.NoError(t, err)
	require.NoError(t, textResponse.Body.Close())
	require.Equal(t, http.StatusNotFound, textResponse.StatusCode)
	assert.Equal(t, "form-action 'self'", textResponse.Header.Get(fiber.HeaderContentSecurityPolicy))
}

func TestRouter_HandleLoginSubmit_RejectsMissingCSRFBeforeAPI(t *testing.T) {
	t.Parallel()

	var apiCalls atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apiCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer apiServer.Close()

	router, err := newHandlerTestRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("email=jane%40example.com&password=secret123")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "Security check failed")
	assert.Equal(t, "frame-ancestors 'none'", resp.Header.Get(fiber.HeaderContentSecurityPolicy))
	assert.Equal(t, int32(0), apiCalls.Load())
}

func TestRouter_HandleLoginSubmit_AllowsValidCSRF(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/methods" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"google_sign_in_enabled":false}`))
			return
		}
		assert.Equal(t, "/api/sessions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"authentication_version":1,
 "slug":"jane-example",
			"email":"jane@example.com",
			"full_name":"Jane Example",
			"role":"user",
			"created_at":"2026-01-01T00:00:00Z",
			"updated_at":"2026-01-01T00:00:00Z"
		}`))
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := newHandlerTestRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	token, cookies := frontendCSRFToken(t, app, "/auth/login")

	form := url.Values{}
	form.Set("email", "jane@example.com")
	form.Set("password", "secret123")
	form.Set("_csrf", token)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/dashboard", resp.Header.Get("Location"))
}

func TestRouter_HandleLoginSubmit_ValidationErrorIncludesFreshCSRF(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer apiServer.Close()

	router, err := newHandlerTestRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	token, cookies := frontendCSRFToken(t, app, "/auth/login")
	form := url.Values{}
	form.Set("email", "jane@example.com")
	form.Set("_csrf", token)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.NotEmpty(t, extractCSRFToken(t, string(body)))
}

func TestRouter_HandleLogout_RejectsMissingCSRF(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := newHandlerTestRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	app.Post("/_test/login", func(c fiber.Ctx) error {
		if err := reqctx.SetUserSlug(c, store, slug.Slug("jane-example")); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusOK)
	})
	router.RegisterRoutes(app, CommunityPresentation{})

	loginResp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/_test/login", nil))
	require.NoError(t, err)
	require.NoError(t, loginResp.Body.Close())

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/logout", nil)
	for _, cookie := range loginResp.Cookies() {
		req.AddCookie(cookie)
	}

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, string(body), "Security check failed")
}

func TestRouter_HandleLogout_AllowsValidCSRF(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEqual(t, "/api/users/profile", r.URL.Path)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	app.Post("/_test/login", func(c fiber.Ctx) error {
		if err := reqctx.SetUserSlug(c, store, slug.Slug("jane-example")); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusOK)
	})
	router.RegisterRoutes(app, CommunityPresentation{})

	token, cookies := frontendCSRFToken(t, app, "/auth/login")
	loginReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/_test/login", nil)
	for _, cookie := range cookies {
		loginReq.AddCookie(cookie)
	}
	loginResp, err := app.Test(loginReq)
	require.NoError(t, err)
	require.NoError(t, loginResp.Body.Close())

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/AUTH/logout/", strings.NewReader("_csrf="+url.QueryEscape(token)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range append(cookies, loginResp.Cookies()...) {
		req.AddCookie(cookie)
	}

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
}

func TestRouter_HandleLoginSubmit_ThrottlesOnlyTheClient(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"google_sign_in_enabled":false}`)
			return
		}
		var payload struct{ Password string }
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload.Password == "Synthetic valid password" {
			_, _ = io.WriteString(w, `{"authentication_version":1,"slug":"fixture-user","email":"jane@example.com","role":"user"}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"INVALID_CREDENTIALS","message":"invalid username or password"}}`)
	}))
	defer apiServer.Close()

	router, err := newHandlerTestRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New(fiber.Config{
		ProxyHeader: fiber.HeaderXForwardedFor, TrustProxy: true, EnableIPValidation: true,
		TrustProxyConfig: fiber.TrustProxyConfig{Proxies: []string{"0.0.0.0"}},
	})
	router.RegisterRoutes(app, CommunityPresentation{})

	submit := func(client, path, email, password string) *http.Response {
		t.Helper()
		token, cookies := frontendCSRFToken(t, app, "/auth/login")
		form := url.Values{"email": {email}, "password": {password}, "_csrf": {token}}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set(fiber.HeaderContentType, "application/x-www-form-urlencoded")
		req.Header.Set(fiber.HeaderXForwardedFor, client)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		resp, err := app.Test(req)
		require.NoError(t, err)
		return resp
	}

	const account = "jane@example.com"
	for range defaultAuthThrottleMax {
		resp := submit("192.0.2.1", "/auth/login", account, "Wrong fixture password")
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}
	blocked := submit("192.0.2.1", "/AUTH/LOGIN/", "other@example.test", "Wrong fixture password")
	defer func() { require.NoError(t, blocked.Body.Close()) }()
	body, err := io.ReadAll(blocked.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, blocked.StatusCode)
	require.Contains(t, string(body), "Too many attempts")

	allowed := submit("192.0.2.2", "/auth/login", account, "Synthetic valid password")
	defer func() { require.NoError(t, allowed.Body.Close()) }()
	require.Equal(t, http.StatusSeeOther, allowed.StatusCode)
	require.Equal(t, "/dashboard", allowed.Header.Get(fiber.HeaderLocation))
}

func TestRouter_HandleMFASubmit_ThrottlesAttempts(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_TOKEN","message":"invalid token"}}`))
	}))
	defer apiServer.Close()

	router, err := newHandlerTestRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	for i := 0; i < defaultAuthThrottleMax; i++ {
		token, cookies := frontendCSRFToken(t, app, "/auth/login")
		resp := postMFAWithCSRF(t, app, token, cookies)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	}

	token, cookies := frontendCSRFToken(t, app, "/auth/login")
	resp := postMFAWithCSRF(t, app, token, cookies)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Contains(t, string(body), "Too many attempts")
}

func TestRouter_HandleAccountPasswordSubmit_ThrottlesAttempts(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/users/profile/password":
			assert.Equal(t, http.MethodPut, r.Method)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"INVALID_CREDENTIALS","message":"invalid credentials"}}`))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, emptyAccountPATListJSON())
		case "/api/organizations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"organizations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := newHandlerTestRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	for i := 0; i < defaultAuthThrottleMax; i++ {
		resp := postAccountPasswordWithCSRF(t, app, authCookie)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}

	resp := postAccountPasswordWithCSRF(t, app, authCookie)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Contains(t, string(body), "Too many attempts")
}

func frontendCSRFToken(t *testing.T, app *fiber.App, path string) (string, []*http.Cookie) {
	t.Helper()

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return extractCSRFToken(t, string(body)), resp.Cookies()
}

func extractCSRFToken(t *testing.T, body string) string {
	t.Helper()

	matches := regexp.MustCompile(`name="_csrf"\s+value="([^"]+)"`).FindStringSubmatch(body)
	require.Len(t, matches, 2, "expected rendered page to contain a CSRF token")
	return matches[1]
}

// postAccountPasswordWithCSRF submits the account password form with a valid session and CSRF token.
func postAccountPasswordWithCSRF(t *testing.T, app *fiber.App, authCookie string) *http.Response {
	t.Helper()

	form := url.Values{}
	form.Set("current_password", "wrong-password")
	form.Set("new_password", "newsecurepass")
	form.Set("confirm_password", "newsecurepass")
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/account/security/password",
		strings.NewReader(form.Encode()),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	return resp
}

func postMFAWithCSRF(
	t *testing.T,
	app *fiber.App,
	token string,
	cookies []*http.Cookie,
) *http.Response {
	t.Helper()

	form := url.Values{}
	form.Set("challenge_token", "challenge-token")
	form.Set("code", "000000")
	form.Set("_csrf", token)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/mfa", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req)
	require.NoError(t, err)
	return resp
}
