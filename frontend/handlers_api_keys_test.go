package frontend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouter_HandleOrganizationSecurityPage_RequiresAuth(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/security", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(
		t,
		"/auth/login?return_to=%2Forganizations%2Facme-hq%2Fsecurity",
		resp.Header.Get("Location"),
	)
}

func TestRouter_HandleOrganizationSecurityPage_RendersAPIKeys(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			writeJSON(
				w,
				`{"api_keys":[{"name":"Deploy Bot","slug":"deploy-bot","description":"prod deploys","role":"admin","token_prefix":"xto_live_abcd","can_manage_api_keys":true,"expires_at":null,"last_used_at":null,"revoked_at":null,"created_by_user_slug":"test-user","created_at":"2026-05-13T10:00:00Z","updated_at":"2026-05-13T10:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/security", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Organization API keys are machine-scoped bearer tokens.")
	require.Contains(t, html, "Use personal access tokens for user-owned workflows.")
	require.Contains(t, html, "Create API key")
	require.Contains(t, html, `hx-post="/organizations/acme-hq/security/api-keys"`)
	require.Contains(t, html, `hx-swap="outerHTML show:none focus-scroll:false"`)
	require.Contains(t, html, `data-preserve-scroll`)
	require.Contains(t, html, "Deploy Bot")
	require.Contains(t, html, "xto_live_abcd")
	require.Contains(t, html, "prod deploys")
	require.Contains(t, html, "Role changes require creating a replacement key.")
	require.Contains(t, html, "Allow this key to manage API keys")
	require.Contains(t, html, "Can manage API keys")
	require.Contains(t, html, `name="can_manage_api_keys" checked`)
	require.Contains(t, html, `name="api_key_confirmation"`)
	require.Contains(t, html, `data-protected-action-modal`)
	require.Contains(t, html, `data-confirm-slug="deploy-bot"`)
	require.Contains(t, html, `aria-controls="organization-api-key-deploy-bot-edit"`)
	require.Contains(t, html, `data-dirty-form="api-key-deploy-bot-update"`)
	require.Contains(t, html, `data-dirty-submit disabled`)
	require.Contains(t, html, "Expires")
	require.Contains(t, html, "Edit")
	require.Contains(t, html, "Rotate")
	require.Contains(t, html, "Rotate API key")
	require.Contains(t, html, "Revoke")
	require.Contains(t, html, "Revoke API key")
	require.Contains(t, html, "Save")
	require.NotContains(t, html, "api-key-secret")
}

func TestRouter_HandleOrganizationSecurityPage_ForwardsAPIKeyPaginationDirection(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			assert.Equal(t, "20", r.URL.Query().Get("limit"))
			assert.Equal(t, "page-2", r.URL.Query().Get("cursor"))
			assert.Equal(t, "prev", r.URL.Query().Get("direction"))
			writeJSON(
				w,
				`{"api_keys":[{"name":"Previous Bot","slug":"previous-bot","role":"viewer","token_prefix":"xto_live_prev","expires_at":null,"last_used_at":null,"revoked_at":null,"created_at":"2026-05-13T10:00:00Z","updated_at":"2026-05-13T10:00:00Z"}],"has_more":true,"next_cursor":"page-3","prev_cursor":"page-1"}`,
			)
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/organizations/acme-hq/security?cursor=page-2&direction=prev",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Previous Bot")
	require.Contains(
		t,
		html,
		`href="/organizations/acme-hq/security?cursor=page-1&amp;direction=prev"`,
	)
	require.Contains(
		t,
		html,
		`href="/organizations/acme-hq/security?cursor=page-3&amp;direction=next"`,
	)
}

func TestRouter_HandleOrganizationSecurityPage_GatesAPIKeyActionsFromPermissions(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "viewer") {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			writeJSON(
				w,
				`{"api_keys":[{"name":"Read Bot","slug":"read-bot","role":"viewer","token_prefix":"xto_live_read","expires_at":null,"last_used_at":null,"revoked_at":null,"created_at":"2026-05-13T10:00:00Z","updated_at":"2026-05-13T10:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/security", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Read Bot")
	require.NotContains(t, html, "Create API key")
	require.NotContains(t, html, "Rotate")
	require.NotContains(t, html, "Revoke")
	require.NotContains(t, html, "Save")
}

func TestRouter_HandleOrganizationSecurityPage_ShowsRevokedKeyAsFinalState(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			writeJSON(
				w,
				`{"api_keys":[{"name":"Old Bot","slug":"old-bot","role":"viewer","token_prefix":"xto_live_old","expires_at":null,"last_used_at":null,"revoked_at":"2026-05-14T12:30:00Z","created_at":"2026-05-13T10:00:00Z","updated_at":"2026-05-14T12:30:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/security", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Old Bot")
	require.Contains(t, html, "Revoked")
	require.Contains(t, html, "Remove")
	require.Contains(t, html, `data-protected-action-trigger="organization-api-key-old-bot-delete"`)
	require.NotContains(t, html, "Rotate API key")
	require.NotContains(t, html, "Revoke API key")
	require.Contains(t, html, "Remove API key")
}

func TestRouter_HandleOrganizationSecurityPage_RendersAPIKeyAuthorizationError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "viewer") {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			writeStatusJSON(w, http.StatusForbidden, `{"error":{"code":"FORBIDDEN","message":"forbidden"}}`)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/security", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, html, "You do not have access to this organization&#39;s API keys.")
	require.Contains(t, html, "API key metadata is unavailable.")
	require.NotContains(t, html, "No API keys yet.")
	require.NotContains(t, html, "Create API key")
	require.NotContains(t, html, "Rotate")
	require.NotContains(t, html, "Revoke")
}

func TestRouter_HandleCreateAPIKeySubmit_RendersValidationFailure(t *testing.T) {
	t.Parallel()

	createCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			writeJSON(w, `{"api_keys":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			createCalled = true
			w.WriteHeader(http.StatusInternalServerError)
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
	form := strings.NewReader("name=&role=admin")
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/security/api-keys",
		form)

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, html, "API key could not be created.")
	require.Contains(t, html, "Validation: name and role are required.")
	require.False(t, createCalled)
}

func TestRouter_HandleCreateAPIKeySubmit_ShowsRawTokenOnce(t *testing.T) {
	t.Parallel()

	created := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			if created {
				writeJSON(
					w,
					`{"api_keys":[{"name":"Deploy Bot","slug":"deploy-bot","role":"admin","token_prefix":"xto_live_abcd","last_used_at":null,"revoked_at":null,"created_at":"2026-05-13T10:00:00Z","updated_at":"2026-05-13T10:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				)
				return
			}
			writeJSON(w, `{"api_keys":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var payload map[string]any
			if !assert.NoError(t, json.Unmarshal(body, &payload)) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			assert.Equal(t, "Deploy Bot", payload["name"])
			assert.Equal(t, "admin", payload["role"])
			assert.Equal(t, "2026-05-14T12:30:00Z", payload["expires_at"])
			assert.Equal(t, true, payload["can_manage_api_keys"])

			created = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"name":"Deploy Bot","slug":"deploy-bot","role":"admin","token":"api-key-secret-created","token_prefix":"xto_live_abcd","expires_at":"2026-05-14T12:30:00Z","last_used_at":null,"revoked_at":null,"created_at":"2026-05-13T10:00:00Z","updated_at":"2026-05-13T10:00:00Z"}`)
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
	form := strings.NewReader("name=Deploy+Bot&role=admin&expires_at=2026-05-14T12%3A30%3A00Z&can_manage_api_keys=on")
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/security/api-keys",
		form)

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Copy this token now. It will not be shown again.")
	require.Contains(t, html, "api-key-secret-created")
	require.Contains(t, html, "Deploy Bot")
}

func TestRouter_HandleCreateAPIKeySubmit_RendersAuthorizationError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			writeJSON(w, `{"api_keys":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			writeStatusJSON(w, http.StatusForbidden, `{"error":{"code":"FORBIDDEN","message":"forbidden"}}`)
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
	form := strings.NewReader("name=Deploy+Bot&role=admin")
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/security/api-keys",
		form)

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, html, "API key could not be created.")
	require.Contains(t, html, "backend_code=FORBIDDEN")
	require.NotContains(t, html, "api-key-secret")
}

func TestRouter_HandleRotateAPIKeySubmit_ShowsRawTokenOnce(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			writeJSON(
				w,
				`{"api_keys":[{"name":"Deploy Bot","slug":"deploy-bot","role":"admin","token_prefix":"xto_live_rotated","expires_at":null,"last_used_at":null,"revoked_at":null,"created_at":"2026-05-13T10:00:00Z","updated_at":"2026-05-13T11:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/api-keys/deploy-bot/rotate":
			writeJSON(
				w,
				`{"name":"Deploy Bot","slug":"deploy-bot","role":"admin","token":"api-key-secret-rotated","token_prefix":"xto_live_rotated","expires_at":null,"last_used_at":null,"revoked_at":null,"created_at":"2026-05-13T10:00:00Z","updated_at":"2026-05-13T11:00:00Z"}`,
			)
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/security/api-keys/deploy-bot/rotate",
		strings.NewReader("api_key_confirmation=deploy-bot"))

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Rotated token for Deploy Bot")
	require.Contains(t, html, "api-key-secret-rotated")
	require.Contains(t, html, "xto_live_rotated")
}

func TestRouter_HandleRotateAPIKeySubmit_RequiresTypedConfirmation(t *testing.T) {
	t.Parallel()

	rotated := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			writeJSON(
				w,
				`{"api_keys":[{"name":"Deploy Bot","slug":"deploy-bot","role":"admin","token_prefix":"xto_live_abcd","expires_at":null,"last_used_at":null,"revoked_at":null,"created_at":"2026-05-13T10:00:00Z","updated_at":"2026-05-13T10:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/api-keys/deploy-bot/rotate":
			rotated = true
			w.WriteHeader(http.StatusInternalServerError)
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/security/api-keys/deploy-bot/rotate",
		strings.NewReader("api_key_confirmation=wrong"))

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, html, "API key could not be rotated.")
	require.Contains(t, html, "Validation: type the API key slug to confirm rotation.")
	require.False(t, rotated)
}

func TestRouter_HandleUpdateAPIKeySubmit_RenamesKey(t *testing.T) {
	t.Parallel()

	updated := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/api-keys/deploy-bot":
			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var payload map[string]any
			if !assert.NoError(t, json.Unmarshal(body, &payload)) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			assert.Equal(t, "Deploy Bot v2", payload["name"])
			assert.Equal(t, "2026-05-14T12:30:00Z", payload["expires_at"])
			assert.Equal(t, true, payload["can_manage_api_keys"])

			updated = true
			writeJSON(
				w,
				`{"name":"Deploy Bot v2","slug":"deploy-bot","role":"admin","token_prefix":"xto_live_abcd","expires_at":"2026-05-14T12:30:00Z","last_used_at":null,"revoked_at":null,"created_at":"2026-05-13T10:00:00Z","updated_at":"2026-05-13T11:00:00Z"}`,
			)
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
	form := strings.NewReader("name=Deploy+Bot+v2&expires_at=2026-05-14T12%3A30%3A00Z&can_manage_api_keys=on")
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/security/api-keys/deploy-bot/update",
		form)

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/security", resp.Header.Get("Location"))
	require.True(t, updated)
}

func TestRouter_HandleRevokeAPIKeySubmit_RevokesKey(t *testing.T) {
	t.Parallel()

	revoked := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/api-keys/deploy-bot":
			revoked = true
			w.WriteHeader(http.StatusNoContent)
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/security/api-keys/deploy-bot/revoke",
		strings.NewReader("api_key_confirmation=deploy-bot"))

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/security", resp.Header.Get("Location"))
	require.True(t, revoked)
}

func TestRouter_HandleDeleteAPIKeySubmit_RemovesRevokedKey(t *testing.T) {
	t.Parallel()

	deleted := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/api-keys/deploy-bot/delete":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/security/api-keys/deploy-bot/delete",
		strings.NewReader("api_key_confirmation=deploy-bot"))

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/security", resp.Header.Get("Location"))
	require.True(t, deleted)
}

func TestRouter_HandleRevokeAPIKeySubmit_RequiresTypedConfirmation(t *testing.T) {
	t.Parallel()

	revoked := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleAPIKeySecurityCommonTestAPI(w, r, "admin") {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/api-keys":
			writeJSON(
				w,
				`{"api_keys":[{"name":"Deploy Bot","slug":"deploy-bot","role":"admin","token_prefix":"xto_live_abcd","expires_at":null,"last_used_at":null,"revoked_at":null,"created_at":"2026-05-13T10:00:00Z","updated_at":"2026-05-13T10:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/api-keys/deploy-bot":
			revoked = true
			w.WriteHeader(http.StatusNoContent)
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/security/api-keys/deploy-bot/revoke",
		strings.NewReader("api_key_confirmation=wrong"))

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, html, "API key could not be revoked.")
	require.Contains(t, html, "Validation: type the API key slug to confirm revocation.")
	require.False(t, revoked)
}

func handleAPIKeySecurityCommonTestAPI(w http.ResponseWriter, r *http.Request, role string) bool {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
		writeJSON(
			w,
			`{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
		writeJSON(
			w,
			`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"`+role+`","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles":
		writeJSON(
			w,
			`{"roles":[{"slug":"admin","name":"Admin","is_default":true,"permissions":[],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","deleted_at":null},{"slug":"viewer","name":"Viewer","is_default":true,"permissions":[],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","deleted_at":null}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/admin":
		writeJSON(
			w,
			`{"slug":"admin","name":"Admin","is_default":true,"permissions":[{"resource":"api_keys","action":"create"},{"resource":"api_keys","action":"read"},{"resource":"api_keys","action":"update"},{"resource":"api_keys","action":"delete"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","deleted_at":null}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/viewer":
		writeJSON(
			w,
			`{"slug":"viewer","name":"Viewer","is_default":true,"permissions":[{"resource":"api_keys","action":"read"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","deleted_at":null}`,
		)
		return true
	default:
		return false
	}
}

// writeStatusJSON writes a JSON response with an explicit HTTP status for frontend handler tests.
func writeStatusJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
