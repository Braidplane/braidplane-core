package frontend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
)

func TestRouter_HandleSignupPage_ReturnsHTML(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/methods" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"google_sign_in_enabled":true}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/signup", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Create account")
	require.Contains(t, string(body), "method=\"post\" action=\"/auth/signup\"")
	require.Contains(t, string(body), "Create an account on this instance.")
	require.NotContains(t, string(body), `name="terms_accepted"`)
	require.NotContains(t, string(body), "30-day trial")
	require.NotContains(t, string(body), `name="usage_type"`)
	require.NotContains(t, string(body), `name="organization_workspace_name"`)
	require.Contains(t, string(body), "method=\"post\" action=\"/api/auth/google\"")
}

func TestRouter_HandleSignupPage_ResumesVerifiedGoogleSignup(t *testing.T) {
	t.Parallel()
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%t", expired), func(t *testing.T) {
			store := session.NewStore()
			router, err := NewRouter("http://localhost:1", slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			app.Get("/_test/pending", func(c fiber.Ctx) error {
				sess, err := store.Get(c)
				require.NoError(t, err)
				defer sess.Release()
				sess.Set(reqctx.SessionGoogleSignupKey, "server-only-identity")
				expires := time.Now().Add(10 * time.Minute)
				if expired {
					expires = time.Now().Add(-time.Minute)
				}
				sess.Set(reqctx.SessionGoogleSignupExpiresKey, expires.Unix())
				require.NoError(t, sess.Save())
				return c.SendStatus(fiber.StatusOK)
			})
			router.RegisterRoutes(app, CommunityPresentation{})
			start, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_test/pending", nil))
			require.NoError(t, err)
			defer func() { _ = start.Body.Close() }()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/signup?return_to=%2Faccount", nil)
			for _, cookie := range start.Cookies() {
				req.AddCookie(cookie)
			}
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			html := string(body)
			if expired {
				require.Contains(t, html, "Your Google sign-in expired.")
				require.NotContains(t, html, ">Continue with Google<")
				require.NotContains(t, html, "Google verified your account.")
				return
			}
			require.Contains(t, html, "Google verified your account.")
			require.Contains(t, html, "Create account with Google")
			require.Contains(t, html, `action="/api/auth/google?return_to=%2Faccount"`)
			require.Contains(t, html, `name="_csrf"`)
			require.NotContains(t, html, "server-only-identity")
			require.NotContains(t, html, ">Continue with Google<")
		})
	}
}

func TestRouter_HandleSignupPage_WhenSelfServiceRegistrationDisabledShowsClosedState(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)
	router.selfServiceRegistrationEnabled = false

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/signup", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	html := string(body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Account creation is currently unavailable.")
	require.Contains(t, html, `href="/auth/login"`)
	require.Contains(t, html, "Contact your instance administrator")
	require.NotContains(t, html, `method="post" action="/auth/signup"`)
	require.NotContains(t, html, `action="/api/auth/google"`)
}

func TestRouter_HandleSignupPage_IgnoresWorkspaceUsageQuery(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/signup?usage_type=organization", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	html := string(body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, html, `name="usage_type"`)
	require.NotContains(t, html, `name="organization_workspace_name"`)
	require.Contains(t, html, "Create an account on this instance.")
}

func TestRouter_HandleLoginPage_ReturnsHTML(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/methods" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"google_sign_in_enabled":true}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/login", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Sign in")
	require.Contains(t, string(body), "method=\"post\" action=\"/auth/login\"")
	require.Contains(t, string(body), "href=\"/api/auth/google\"")
}

func TestAuthenticationPagesHideUnavailableGoogleSignIn(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprintf("status=%d", status), func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"google_sign_in_enabled":false}`)
			}))
			defer backend.Close()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			router.RegisterRoutes(app, CommunityPresentation{})
			for _, path := range []string{"/auth/login", "/auth/signup"} {
				resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
				require.NoError(t, err)
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.NotContains(t, string(body), "/api/auth/google")
			}
		})
	}
}

func TestRouter_HandleLoginPage_WhenSelfServiceRegistrationDisabledHidesSignupLink(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/methods" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"google_sign_in_enabled":true}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)
	router.selfServiceRegistrationEnabled = false

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/login", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	html := string(body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Sign in")
	require.Contains(t, html, "href=\"/api/auth/google\"")
	require.NotContains(t, html, "Create one")
	require.NotContains(t, html, "/auth/signup")
}

func TestRouter_HandleLoginPage_PreservesSafeReturnTo(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/auth/login?return_to=%2Fdashboard%3Fcursor%3Dnext-123",
		nil)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "name=\"return_to\"")
	require.Contains(t, string(body), "value=\"/dashboard?cursor=next-123\"")
	require.Contains(t, string(body), `href="/auth/signup?return_to=%2Fdashboard%3Fcursor%3Dnext-123"`)
}

func TestRouter_HandleSignupPage_PreservesSafeReturnTo(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/methods" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"google_sign_in_enabled":true}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/auth/signup?return_to=%2Fmember-invitations%2Faccept%3Ftoken%3Draw-token",
		nil)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `name="return_to" value="/member-invitations/accept?token=raw-token"`)
	require.Contains(t, string(body), `action="/api/auth/google?return_to=%2Fmember-invitations%2Faccept%3Ftoken%3Draw-token"`)
	require.Contains(t, string(body), `href="/auth/login?return_to=%2Fmember-invitations%2Faccept%3Ftoken%3Draw-token"`)
	require.NotContains(t, string(body), `usage_type=`)
}

func TestRouter_HandleSignupSubmit_ValidationError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(
			[]byte(
				`{"error":{"code":"VALIDATION_FAILED","message":"generic validation failed","validation_errors":[{"field":"password","message":"is required"}]}}`,
			),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("full_name=&email=&password=&confirm_password=")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/signup", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "Full name is required.")
}

func TestRouter_HandleSignupSubmit_WhenSelfServiceRegistrationDisabledDoesNotCallBackend(t *testing.T) {
	t.Parallel()

	apiCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalled = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)
	router.selfServiceRegistrationEnabled = false

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("full_name=Test+User&email=test@example.com&password=secret123&confirm_password=secret123")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/signup", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, string(body), "Account creation is currently unavailable.")
	require.False(t, apiCalled)
}

func TestRouter_HandleSignupSubmit_WhenSelfServiceRegistrationDisabledHTMXRendersFragmentStatus(t *testing.T) {
	t.Parallel()

	apiCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalled = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)
	router.selfServiceRegistrationEnabled = false

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("full_name=Test+User&email=test@example.com&password=secret123&confirm_password=secret123&return_to=%2Fdashboard")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/signup", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Account creation is currently unavailable.")
	require.Contains(t, string(body), `href="/auth/login?return_to=%2Fdashboard"`)
	require.False(t, apiCalled)
}

func TestRouter_HandleSignupSubmit_ValidationError_HTMXRendersError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("full_name=&email=&password=&confirm_password=")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/signup", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Full name is required.")
}

func TestRouter_HandleLoginSubmit_ValidationError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("email=&password=")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "Email is required.")
	require.Contains(t, string(body), `aria-errormessage="login-feedback"`)
	require.Contains(t, string(body), `aria-invalid="true"`)
	require.Empty(t, resp.Header.Get("HX-Redirect"))
}

func TestRouter_HandleLoginSubmit_ValidationError_HTMXRendersError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("email=&password=")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Email is required.")
	require.Contains(t, string(body), `aria-errormessage="login-feedback"`)
	require.Contains(t, string(body), `aria-invalid="true"`)
	require.Empty(t, resp.Header.Get("HX-Redirect"))
}

func TestRouter_HandleLoginSubmit_SuccessRedirectsToDashboard(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/sessions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(
			[]byte(
				`{"authentication_version":1,"slug":"test-user","full_name":"Test User","email":"test@example.com","role":"user","locale":"en-US","created_at":"2026-03-04T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}`,
			),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("email=test@example.com&password=secret123")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/dashboard", resp.Header.Get("Location"))
}

func TestRouter_HandleLoginSubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/sessions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(
			[]byte(
				`{"authentication_version":1,"slug":"test-user","full_name":"Test User","email":"test@example.com","role":"user","locale":"en-US","created_at":"2026-03-04T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}`,
			),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("email=test@example.com&password=secret123")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/dashboard", resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleLoginSubmit_EstablishesSessionForProtectedRoutes(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			assert.Equal(t, http.MethodPost, r.Method)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(
				[]byte(
					`{"authentication_version":1,"slug":"test-user","full_name":"Test User","email":"test@example.com","role":"user","locale":"en-US","created_at":"2026-03-04T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}`,
				),
			)
		case "/api/organizations":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"next_cursor":null,"prev_cursor":null,"has_more":false}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	loginForm := strings.NewReader("email=test@example.com&password=secret123")
	loginReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", loginForm)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, loginReq)

	loginResp, err := app.Test(loginReq)
	require.NoError(t, err)
	defer func() { _ = loginResp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, loginResp.StatusCode)
	require.Equal(t, "/dashboard", loginResp.Header.Get("Location"))

	sessionCookie := activeSessionCookieHeader(t, loginResp)

	dashboardReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", nil)
	dashboardReq.Header.Set("Cookie", sessionCookie)

	dashboardResp, err := app.Test(dashboardReq)
	require.NoError(t, err)
	defer func() { _ = dashboardResp.Body.Close() }()

	require.Equal(t, http.StatusOK, dashboardResp.StatusCode)
	require.NotEqual(t, "/auth/login?return_to=%2Fdashboard", dashboardResp.Header.Get("Location"))
}

func TestRouter_HandleLoginSubmit_EstablishesSessionForProtectedRoutesWithCookieJar(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			assert.Equal(t, http.MethodPost, r.Method)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(
				[]byte(
					`{"authentication_version":1,"slug":"test-user","full_name":"Test User","email":"test@example.com","role":"user","locale":"en-US","created_at":"2026-03-04T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}`,
				),
			)
		case "/api/organizations":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"next_cursor":null,"prev_cursor":null,"has_more":false}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(session.Config{
			CookieHTTPOnly: true,
			CookieSameSite: "Lax",
			CookiePath:     "/",
		}),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})
	baseURL := startFiberTestServer(t, app)

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	loginReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/auth/login?return_to=%2Fdashboard", nil)
	require.NoError(t, err)
	loginResp, err := client.Do(loginReq)
	require.NoError(t, err)
	loginBody, err := io.ReadAll(loginResp.Body)
	require.NoError(t, err)
	require.NoError(t, loginResp.Body.Close())
	require.Equal(t, http.StatusOK, loginResp.StatusCode)

	loginToken := extractCSRFToken(t, string(loginBody))
	form := url.Values{}
	form.Set("_csrf", loginToken)
	form.Set("email", "test@example.com")
	form.Set("password", "secret123")
	form.Set("return_to", "/dashboard")

	postReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/auth/login", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postResp, err := client.Do(postReq)
	require.NoError(t, err)
	require.NoError(t, postResp.Body.Close())
	require.Equal(t, http.StatusSeeOther, postResp.StatusCode)
	require.Equal(t, "/dashboard", postResp.Header.Get("Location"))

	dashboardReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/dashboard", nil)
	require.NoError(t, err)
	dashboardResp, err := client.Do(dashboardReq)
	require.NoError(t, err)
	require.NoError(t, dashboardResp.Body.Close())
	require.Equal(t, http.StatusOK, dashboardResp.StatusCode)
	require.NotEqual(t, "/auth/login?return_to=%2Fdashboard", dashboardResp.Header.Get("Location"))
}

func TestRouter_HandleLoginSubmit_RegeneratesPreAuthSession(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/sessions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(
			[]byte(
				`{"authentication_version":1,"slug":"test-user","full_name":"Test User","email":"test@example.com","role":"user","locale":"en-US","created_at":"2026-03-04T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}`,
			),
		)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
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
	router.RegisterRoutes(app, CommunityPresentation{})

	preAuthResp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/_test/preauth-session", nil))
	require.NoError(t, err)
	defer func() { _ = preAuthResp.Body.Close() }()
	oldSessionCookie := activeSessionCookieHeader(t, preAuthResp)

	loginForm := strings.NewReader("email=test@example.com&password=secret123")
	loginReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", loginForm)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginReq.Header.Set("Cookie", oldSessionCookie)
	addCSRFToken(t, app, loginReq)

	loginResp, err := app.Test(loginReq)
	require.NoError(t, err)
	defer func() { _ = loginResp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, loginResp.StatusCode)
	newSessionCookie := activeSessionCookieHeader(t, loginResp)
	require.NotEqual(t, oldSessionCookie, newSessionCookie)

	oldSessionReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/session-user", nil)
	oldSessionReq.Header.Set("Cookie", oldSessionCookie)
	oldSessionResp, err := app.Test(oldSessionReq)
	require.NoError(t, err)
	defer func() { _ = oldSessionResp.Body.Close() }()
	body, err := io.ReadAll(oldSessionResp.Body)
	require.NoError(t, err)
	require.Empty(t, string(body))

	newSessionReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/session-user", nil)
	newSessionReq.Header.Set("Cookie", newSessionCookie)
	newSessionResp, err := app.Test(newSessionReq)
	require.NoError(t, err)
	defer func() { _ = newSessionResp.Body.Close() }()
	body, err = io.ReadAll(newSessionResp.Body)
	require.NoError(t, err)
	require.Equal(t, "test-user", string(body))
}

func TestRouter_HandleLoginSubmit_SuccessRedirectsToSafeReturnTo(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/sessions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(
			[]byte(
				`{"authentication_version":1,"slug":"test-user","full_name":"Test User","email":"test@example.com","role":"user","locale":"en-US","created_at":"2026-03-04T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}`,
			),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader(
		"email=test@example.com&password=secret123&return_to=%2Forganizations%2Facme-hq",
	)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq", resp.Header.Get("Location"))
}

func TestRouter_HandleLoginSubmit_MFACarriesReturnTo(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/sessions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(
			[]byte(
				`{"challenge_token":"challenge-token","expires_at":"2026-05-27T12:00:00Z"}`,
			),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader(
		"email=test@example.com&password=secret123&return_to=%2Forganizations%2Facme-hq%2Fprojects%3Fcursor%3Dnext",
	)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Check your email")
	require.Contains(t, html, `name="challenge_token" value="challenge-token"`)
	require.Contains(t, html, `name="return_to" value="/organizations/acme-hq/projects?cursor=next"`)
}

func TestRouter_HandleMFASubmit_SuccessRedirectsToSafeReturnTo(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/sessions/mfa/verify", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(
			[]byte(
				`{"authentication_version":1,"slug":"test-user","full_name":"Test User","email":"test@example.com","role":"user","locale":"en-US","created_at":"2026-03-04T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}`,
			),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader(
		"challenge_token=challenge-token&code=123456&return_to=%2Forganizations%2Facme-hq%2Fprojects",
	)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/mfa", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/projects", resp.Header.Get("Location"))
}

func TestRouter_HandleLoginSubmit_RejectsUnsafeReturnTo(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/sessions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(
			[]byte(
				`{"authentication_version":1,"slug":"test-user","full_name":"Test User","email":"test@example.com","role":"user","locale":"en-US","created_at":"2026-03-04T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}`,
			),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader(
		"email=test@example.com&password=secret123&return_to=https%3A%2F%2Fevil.example",
	)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/dashboard", resp.Header.Get("Location"))
}

func TestRouter_HandleAuthErrorPage_KnownErrorCode(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	tests := map[string]string{
		"invalid_state":            "Sign-in session expired or became invalid",
		"registration_disabled":    "Account creation is currently unavailable",
		"external_access_disabled": "Existing users can still sign in and accept organization invitations",
	}
	for code, wantMessage := range tests {
		t.Run(code, func(t *testing.T) {
			t.Parallel()

			router, err := NewRouter(
				apiServer.URL,
				slog.New(slog.DiscardHandler),
				session.NewStore(),
			)
			require.NoError(t, err)

			app := fiber.New()
			router.RegisterRoutes(app, CommunityPresentation{})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/error?error="+code, nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Contains(t, string(body), "Authentication failed")
			require.Contains(t, string(body), wantMessage)
			require.Contains(t, string(body), "href=\"/auth/login\"")
		})
	}
}

func TestRouter_HandleLoginSubmit_InvalidCredentials(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(
			[]byte(`{"error":{"code":"INVALID_CREDENTIALS","message":"invalid credentials"}}`),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("email=test@example.com&password=wrongpass")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(body), "Invalid email or password.")
	require.Contains(t, string(body), "value=\"test@example.com\"")
}

func TestRouter_HandleLoginSubmit_EmailNotVerified(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(
			[]byte(`{"error":{"code":"EMAIL_NOT_VERIFIED","message":"email not verified"}}`),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("email=test@example.com&password=secret123")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(body), "Email not verified.")
	require.Contains(
		t,
		string(body),
		"Check your inbox and verify your account before signing in.",
	)
}

func TestRouter_HandleSignupSubmit_Success(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/users", r.URL.Path)
		var body map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.NotContains(t, body, "usage_type")
		assert.NotContains(t, body, "organization_workspace_name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(
			[]byte(
				`{"authentication_version":1,"slug":"test-user","full_name":"Test User","email":"test@example.com","role":"user","locale":"en-US","created_at":"2026-03-04T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}`,
			),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader(
		"full_name=Test+User&email=test@example.com&password=secret123&confirm_password=secret123&return_to=%2Fmember-invitations%2Faccept%3Ftoken%3Draw-token",
	)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/signup", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Verify your email")
	require.Contains(t, string(body), "test@example.com")
	require.Contains(t, string(body), `href="/auth/login?return_to=%2Fmember-invitations%2Faccept%3Ftoken%3Draw-token"`)
	require.Contains(t, string(body), `name="return_to" value="/member-invitations/accept?token=raw-token"`)
}

func TestRouter_HandleSignupSubmit_IgnoresPostedWorkspaceIntent(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/users", r.URL.Path)
		var body map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.NotContains(t, body, "usage_type")
		assert.NotContains(t, body, "organization_workspace_name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(
			[]byte(
				`{"authentication_version":1,"slug":"test-user","full_name":"Test User","email":"test@example.com","role":"user","locale":"en-US","created_at":"2026-03-04T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}`,
			),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader(
		"usage_type=organization&organization_workspace_name=Acme+Operations&full_name=Test+User&email=test@example.com&password=secret123&confirm_password=secret123",
	)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/signup", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Verify your email")
	require.Contains(t, string(body), "test@example.com")
}

func TestRouter_HandleSignupSubmit_BackendValidationIncludesDetails(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(
			[]byte(
				`{"error":{"code":"VALIDATION_FAILED","message":"request validation failed","detailed_error":"backend stack line 1\nbackend stack line 2","validation_errors":[{"field":"email","message":"already exists"}]}}`,
			),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader(
		"full_name=Test+User&email=test@example.com&password=secret123&confirm_password=secret123",
	)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/signup", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "Email: already exists")
	require.Contains(t, string(body), "request validation failed")
	require.Contains(t, string(body), "More details")
	require.Contains(t, string(body), "backend stack line 1")
}

func TestRouter_HandleSignupSubmit_BackendValidationIncludesDetails_HTMXRendersError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write(
			[]byte(
				`{"error":{"code":"EMAIL_ALREADY_EXISTS","message":"email already exists"}}`,
			),
		)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader(
		"full_name=Test+User&email=test@example.com&password=secret123&confirm_password=secret123",
	)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/signup", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "email already exists")
}

func TestRouter_HandleVerifyEmail_MissingToken(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/verify-email", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "Invalid or missing verification token.")
}

func TestRouter_HandleVerifyEmail_Success(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/auth/email/verify", r.URL.Path)

		var reqBody map[string]string
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
		assert.Equal(t, "valid-token", reqBody["token"])

		w.WriteHeader(http.StatusNoContent)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/verify-email?token=valid-token", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Email verified")
	require.Contains(t, string(body), "Your email has been verified. You can now sign in.")
}

func TestRouter_HandleVerifyEmail_APIError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_TOKEN","message":"invalid token"}}`))
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/verify-email?token=invalid-token", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "Verification failed")
	require.Contains(t, string(body), "invalid token")
}

func TestRouter_HandleVerifyEmail_APIErrorValidationMessage(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		errResp := api.ErrorResponse{Error: api.Error{
			Code:    "VALIDATION_FAILED",
			Message: "request validation failed",
		}}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		assert.NoError(t, json.NewEncoder(w).Encode(errResp))
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/verify-email?token=bad", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "request validation failed")
}

func TestRouter_HandleLogout_RequiresAuth(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(
		apiServer.URL,
		slog.New(slog.DiscardHandler),
		session.NewStore(),
	)
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/logout", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login?return_to=%2Fauth%2Flogout", resp.Header.Get("Location"))
}

func TestRouter_HandleLogout_ClearsSessionAndRedirects(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	// Seed active organization to validate both keys are cleared by logout.
	switchReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/organizations/acme-hq/switch", nil)
	switchReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, switchReq)
	switchResp, err := app.Test(switchReq)
	require.NoError(t, err)
	defer func() { _ = switchResp.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, switchResp.StatusCode)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login", resp.Header.Get("Location"))

	userSlug := getUserSlugFromSession(t, app, authCookie)
	require.Empty(t, userSlug)

	activeOrg := getActiveOrganizationSlugFromSession(t, app, authCookie)
	require.Empty(t, activeOrg)
}

func TestRouter_HandleLogout_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/auth/login", resp.Header.Get("HX-Redirect"))
}

func TestRouter_HandleForgotPasswordPage_ReturnsHTML(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/auth/forgot-password", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Forgot password")
	require.Contains(t, string(body), "action=\"/auth/forgot-password\"")
}

func TestRouter_HandleForgotPasswordSubmit_Success(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/auth/password/forgot", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("email=test%40example.com")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/forgot-password", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "reset link has been sent")
}

func TestRouter_HandleResetPasswordSubmit_Success(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/auth/password/reset", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("token=reset-token&password=secret123&confirm_password=secret123")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/reset-password", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Password reset successful")
	require.Contains(t, string(body), "href=\"/auth/login\"")
}

func TestRouter_HandleResetPasswordSubmit_InvalidToken(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/auth/password/reset", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_TOKEN","message":"invalid token"}}`))
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("token=invalid-token&password=secret123&confirm_password=secret123")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/reset-password", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "invalid token")
	require.Contains(t, string(body), `href="/auth/login"`)
	require.NotContains(t, string(body), "Password reset successful")
}

func TestRouter_HandleResendVerificationEmail_Success(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/auth/email/resend-verification", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := strings.NewReader("email=test%40example.com")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/resend-verification", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Verification link resent")
}

func activeSessionCookieHeader(t *testing.T, resp *http.Response) string {
	t.Helper()

	cookies := resp.Cookies()
	require.NotEmpty(t, cookies)
	for i := len(cookies) - 1; i >= 0; i-- {
		cookie := cookies[i]
		if cookie.Value == "" || cookie.MaxAge < 0 {
			continue
		}
		if strings.Contains(strings.ToLower(cookie.Name), "session") {
			return cookie.Name + "=" + cookie.Value
		}
	}

	require.Failf(t, "session cookie not found", "cookies: %#v", cookies)
	return ""
}

func startFiberTestServer(t *testing.T, app *fiber.App) string {
	t.Helper()

	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() {
		_ = app.Listener(listener)
	}()
	t.Cleanup(func() {
		require.NoError(t, app.Shutdown())
	})

	return "http://" + listener.Addr().String()
}
