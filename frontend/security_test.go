package frontend

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestProtectedDashboardRedirectPreservesReturnTo(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unauthenticated redirect contacted backend: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(backend.Close)
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
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

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/login", nil))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), `name="_csrf"`)
	assert.NotEmpty(t, extractCSRFToken(t, string(body)))
}

func TestRouter_HandleLoginSubmit_RejectsMissingCSRFBeforeAPI(t *testing.T) {
	t.Parallel()

	var apiCalls atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apiCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
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
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
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

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
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

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/logout", strings.NewReader("_csrf="+url.QueryEscape(token)))
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

func TestRouter_HandleLoginSubmit_ThrottlesAttempts(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_CREDENTIALS","message":"invalid username or password"}}`))
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	for i := 0; i < defaultAuthThrottleMax; i++ {
		token, cookies := frontendCSRFToken(t, app, "/auth/login")
		resp := postLoginWithCSRF(t, app, token, cookies)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}

	token, cookies := frontendCSRFToken(t, app, "/auth/login")
	resp := postLoginWithCSRF(t, app, token, cookies)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Contains(t, string(body), "Too many attempts")
}

func TestRouter_HandleMFASubmit_ThrottlesAttempts(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_TOKEN","message":"invalid token"}}`))
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
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
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
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

func postLoginWithCSRF(
	t *testing.T,
	app *fiber.App,
	token string,
	cookies []*http.Cookie,
) *http.Response {
	t.Helper()

	form := url.Values{}
	form.Set("email", "jane@example.com")
	form.Set("password", "wrong-password")
	form.Set("_csrf", token)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req)
	require.NoError(t, err)
	return resp
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
