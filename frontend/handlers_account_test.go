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
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

const (
	accountPATPaginationFormValues               = "pat_cursor=cursor-page-2&pat_direction=next"
	accountPATPaginationFormValuesWithPrevCursor = accountPATPaginationFormValues + "&pat_prev_cursor=cursor-page-1"
)

func TestAccountPATPreviousPageForEmptyResult(t *testing.T) {
	t.Parallel()

	apiPrevCursor := "api-prev-cursor"
	page, ok := accountPATPreviousPageForEmptyResult(
		accountPATPage{Cursor: "cursor-page-2", Direction: pageDirectionNext},
		&api.PersonalAccessTokenListResponse{PrevCursor: &apiPrevCursor},
		"fallback-prev-cursor",
	)
	require.True(t, ok)
	assert.Equal(t, "api-prev-cursor", page.Cursor)
	assert.Equal(t, pageDirectionPrev, page.Direction)

	page, ok = accountPATPreviousPageForEmptyResult(
		accountPATPage{Cursor: "cursor-page-2", Direction: pageDirectionNext},
		&api.PersonalAccessTokenListResponse{},
		"fallback-prev-cursor",
	)
	require.True(t, ok)
	assert.Equal(t, "fallback-prev-cursor", page.Cursor)
	assert.Equal(t, pageDirectionPrev, page.Direction)

	_, ok = accountPATPreviousPageForEmptyResult(
		accountPATPage{Cursor: "cursor-page-2", Direction: pageDirectionNext},
		&api.PersonalAccessTokenListResponse{PersonalAccessTokens: make([]api.PersonalAccessToken, 1)},
		"fallback-prev-cursor",
	)
	assert.False(t, ok)
}

func TestRouter_HandleAccountPage_RendersProfileFields(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		assert.NotEmpty(t, r.Header.Get(fiber.HeaderXRequestID))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/account", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	html := string(body)
	assert.Contains(t, html, `href="/account"`)
	assert.Contains(t, html, `href="/account?tab=invitations"`)
	assert.Contains(t, html, `href="/account?tab=security"`)
	assert.Contains(t, html, `class="space-y-6" id="account-profile-tab"`)
	assert.Contains(t, html, `class="hidden" id="account-invitations-tab"`)
	assert.Contains(t, html, `class="hidden" id="account-security-tab"`)
	assert.Contains(t, html, `value="Test User"`)
	assert.Contains(t, html, `value="https://example.com/avatar.png"`)
	assert.Contains(t, html, `value="+48123123123"`)
	assert.Contains(t, html, `value="Operations Lead"`)
	assert.Contains(t, html, `value="Europe/Warsaw"`)
	assert.NotContains(t, html, "Profile address")
	assert.NotContains(t, html, "No card or billing details needed")
	assert.NotContains(t, html, "Contact us about an upgrade")
	assert.NotContains(t, html, `value="billing@example.com"`)
	assert.NotContains(t, html, `data-dirty-form="account-billing"`)
	assert.Contains(t, html, `name="mfa_email_enabled" value="true" checked`)
}

func TestRouter_HandleAccountPage_KeepsProjectRuntimeNavEnabled(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, emptyAccountPATListJSON())
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme-hq/projects":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"projects":[{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme-hq/projects/data-migration/environments":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"project_environments":[{"environment_slug":"prod","environment_name":"Production","environment_description":"Production target","environment_protected":true,"is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		default:
			t.Fatalf("unexpected API request %s %s", r.Method, r.URL.Path)
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
	setActiveOrganizationSlugInSession(t, app, authCookie, "acme-hq")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/account", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	html := string(body)
	assert.Contains(t, html, `href="/orgs/acme-hq/projects/data-migration/pipelines?environment_slug=prod"`)
	assert.Contains(t, html, `href="/orgs/acme-hq/projects/data-migration/schedules?environment_slug=prod"`)
	assert.Contains(t, html, `href="/orgs/acme-hq/projects/data-migration/runs?environment_slug=prod"`)
}

func TestRouter_HandleAccountPage_RendersPendingInvitations(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(
				w,
				`{"invitations":[`+memberInvitationJSON("test@example.com", "pending")+`],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/account?tab=invitations", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	html := string(body)
	assert.Contains(t, html, `class="hidden" id="account-profile-tab"`)
	assert.Contains(t, html, `class="space-y-6" id="account-invitations-tab"`)
	assert.Contains(t, html, `class="hidden" id="account-security-tab"`)
	assert.Contains(t, html, "Pending invitations")
	assert.Contains(t, html, "acme-hq")
	assert.Contains(t, html, "test@example.com")
	assert.Contains(t, html, "pending")
	assert.NotContains(t, html, `/account/member-invitations/acme-hq/test-example-com-abc123/accept`)
	assert.NotContains(t, html, `name="terms_accepted"`)
	assert.Contains(t, html, `>Decline</button>`)
	assert.Contains(t, html, `data-destructive-confirm="Decline member invitation &#34;test@example.com (acme-hq)&#34;? You will not join this organization."`)
}

func TestRouter_HandleAccountPage_RendersUnverifiedInvitationError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"EMAIL_NOT_VERIFIED","message":"email not verified"}}`))
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/account", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("Location"))
	html := string(body)
	assert.Contains(t, html, `value="Test User"`)
	assert.Contains(t, html, "Verify your email address to view pending invitations.")
}

func TestRouter_HandleAccountSecuritySubmit_SavesMFASetting(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodPatch, r.Method)
			var body map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, true, body["mfa_email_enabled"])
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(
				w,
				`{"invitations":[`+memberInvitationJSON("test@example.com", "pending")+`],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
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

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/account/security",
		strings.NewReader("mfa_email_enabled=true"),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	html := string(body)
	assert.Contains(t, html, "Security settings saved.")
	assert.Contains(t, html, `class="space-y-6" id="account-security-tab"`)
	assert.NotContains(t, html, "No card or billing details needed")
	assert.NotContains(t, html, `>Save billing profile<`)
	assert.Contains(t, html, "Pending invitations")
	assert.Contains(t, html, "test@example.com")
}

func TestRouter_HandleAccountProfileSubmit_OmitsBlankOptionalFields(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodPatch, r.Method)
			var body map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, "Test User", body["full_name"])
			assert.NotContains(t, body, "locale")
			assert.NotContains(t, body, "avatar_url")
			assert.NotContains(t, body, "phone_number")
			assert.NotContains(t, body, "job_title")
			assert.NotContains(t, body, "timezone")
			assert.NotContains(t, body, "billing_address")
			writeJSON(w, accountUserJSON(false))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(
				w,
				`{"invitations":[`+memberInvitationJSON("test@example.com", "pending")+`],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
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

	form := "full_name=Test+User&locale=&avatar_url=&phone_number=&job_title=&timezone="
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/profile", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	html := string(body)
	assert.Contains(t, html, "Profile saved.")
	assert.NotContains(t, html, "No card or billing details needed")
	assert.NotContains(t, html, `>Save billing profile<`)
	assert.Contains(t, html, "Pending invitations")
	assert.Contains(t, html, "test@example.com")
}

func TestRouter_HandleAccountProfileSubmit_ErrorPreservesMFAAndSwapsForHTMX(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			if r.Method == http.MethodPatch {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"VALIDATION_FAILED","message":"bad profile"}}`))
				return
			}
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
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

	form := "full_name=Test+User&avatar_url=not-a-url"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/profile", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, html, "bad profile")
	assert.Contains(t, html, `value="not-a-url"`)
	assert.Contains(t, html, `name="mfa_email_enabled" value="true" checked`)
}

func TestRouter_HandleAccountPage_RendersPersonalAccessTokens(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			assertAccountPATBackendPageQuery(t, r)
			writeJSON(w, accountPATListWithPrevCursorJSON("cursor-page-1"))
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

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/account?tab=security&pat_cursor=cursor-page-2&pat_direction=next",
		nil,
	)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	html := string(body)
	assert.Contains(t, html, `class="hidden" id="account-profile-tab"`)
	assert.Contains(t, html, `class="hidden" id="account-invitations-tab"`)
	assert.Contains(t, html, `class="space-y-6" id="account-security-tab"`)
	assert.Contains(t, html, "Personal access tokens")
	assert.Contains(t, html, "PATs authenticate as you for CLI and member-owned workflows.")
	assert.Contains(t, html, "Allow this token to manage personal access tokens")
	assert.Contains(t, html, "Can manage PATs")
	assert.Contains(t, html, `name="can_manage_personal_access_tokens" checked`)
	assert.Contains(t, html, "Developer CLI")
	assert.Contains(t, html, "xto_pat_abcd123")
	assert.Contains(t, html, `>Edit</button>`)
	assert.Contains(t, html, `id="account-pat-developer-cli-edit"`)
	assert.Contains(t, html, `hx-post="/account/security/personal-access-tokens/developer-cli/update"`)
	assert.Contains(t, html, `name="pat_cursor" value="cursor-page-2"`)
	assert.Contains(t, html, `name="pat_direction" value="next"`)
	assert.Contains(t, html, `name="pat_prev_cursor" value="cursor-page-1"`)
	assert.Contains(t, html, `hx-indicator="#account-pat-list-loading"`)
	assert.Contains(t, html, "Loading personal access tokens")
	assert.Contains(t, html, `hx-swap="outerHTML show:none focus-scroll:false"`)
	assert.Contains(t, html, `data-preserve-scroll`)
	assert.Contains(t, html, "Rotate")
	assert.Contains(t, html, `hx-confirm="Rotate personal access token &#34;Developer CLI (developer-cli)&#34;? The current token will stop working immediately."`)
	assert.Contains(t, html, `data-destructive-confirm="Rotate personal access token &#34;Developer CLI (developer-cli)&#34;? The current token will stop working immediately."`)
	assert.Contains(t, html, "Revoke")
	assert.Contains(t, html, `hx-confirm="Revoke personal access token &#34;Developer CLI (developer-cli)&#34;? Requests using it will be rejected immediately."`)
	assert.Contains(t, html, `data-destructive-confirm="Revoke personal access token &#34;Developer CLI (developer-cli)&#34;? Requests using it will be rejected immediately."`)
	assert.NotContains(t, html, "account-pat-secret")
}

func TestRouter_HandleAccountPage_RendersRemoveActionForRevokedPersonalAccessTokens(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, revokedAccountPATListJSON())
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/account?tab=security", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	html := string(body)
	assert.Contains(t, html, `class="hidden" id="account-profile-tab"`)
	assert.Contains(t, html, `class="hidden" id="account-invitations-tab"`)
	assert.Contains(t, html, `class="space-y-6" id="account-security-tab"`)
	assert.Contains(t, html, `id="account-password-security"`)
	assert.Contains(t, html, `action="/account/security/password"`)
	assert.Contains(t, html, `name="current_password"`)
	assert.Contains(t, html, `name="new_password"`)
	assert.Contains(t, html, `name="confirm_password"`)
	assert.Contains(t, html, "Personal access tokens and organization API keys are managed separately.")
	assert.Contains(t, html, "Personal access tokens")
	assert.Contains(t, html, "PATs authenticate as you for CLI and member-owned workflows.")
	assert.Contains(t, html, "Developer CLI")
	assert.Contains(t, html, "xto_pat_abcd123")
	assert.Contains(t, html, "Revoked")
	assert.Contains(t, html, `hx-post="/account/security/personal-access-tokens/developer-cli/delete"`)
	assert.Contains(t, html, `hx-swap="outerHTML show:none focus-scroll:false"`)
	assert.Contains(t, html, `data-preserve-scroll`)
	assert.Contains(t, html, "Remove")
	assert.Contains(t, html, `hx-confirm="Remove revoked personal access token metadata &#34;Developer CLI (developer-cli)&#34;? This cannot be undone."`)
	assert.Contains(t, html, `data-destructive-confirm="Remove revoked personal access token metadata &#34;Developer CLI (developer-cli)&#34;? This cannot be undone."`)
	assert.NotContains(t, html, `>Edit</button>`)
	assert.NotContains(t, html, `/account/security/personal-access-tokens/developer-cli/update`)
	assert.NotContains(t, html, "Rotate personal access token")
	assert.NotContains(t, html, "Revoke personal access token")
	assert.NotContains(t, html, "account-pat-secret")
}

func TestRouter_HandleAccountPage_RendersPasswordDisabledForNoLocalPassword(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSONWithLocalPassword(true, false))
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/account?tab=security", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	html := string(body)
	assert.Contains(t, html, `id="account-password-security"`)
	assert.Contains(t, html, "Password sign-in is not enabled for this account.")
	assert.Contains(t, html, "Use your configured identity provider to manage sign-in credentials.")
	assert.NotContains(t, html, `action="/account/security/password"`)
	assert.NotContains(t, html, `name="current_password"`)
}

func TestRouter_HandleAccountPasswordSubmit_UpdatesPassword(t *testing.T) {
	t.Parallel()

	updated := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/users/profile/password":
			assert.Equal(t, http.MethodPut, r.Method)
			assert.NotEmpty(t, r.Header.Get("X-CSRF-Token"))
			var body map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, "currentpass", body["current_password"])
			assert.Equal(t, "newsecurepass", body["new_password"])
			updated = true
			w.WriteHeader(http.StatusNoContent)
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

	form := "current_password=currentpass&new_password=newsecurepass&confirm_password=newsecurepass"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/password", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
	require.True(t, updated)
	html := string(body)
	assert.Empty(t, getUserSlugFromSession(t, app, req.Header.Get("Cookie")))

	assert.NotContains(t, html, "currentpass")
	assert.NotContains(t, html, "newsecurepass")
}

func TestRouter_HandleAccountPasswordSubmit_OptionalReadsDoNotBlockUpdate(t *testing.T) {
	t.Parallel()

	updated := false
	invitationsCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/users/profile/password":
			assert.Equal(t, http.MethodPut, r.Method)
			updated = true
			w.WriteHeader(http.StatusNoContent)
		case "/api/member-invitations":
			invitationsCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL_SERVER_ERROR","message":"internal server error"}}`))
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

	form := "current_password=currentpass&new_password=newsecurepass&confirm_password=newsecurepass"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/password", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/auth/login", resp.Header.Get("Location"))
	require.True(t, updated)
	require.False(t, invitationsCalled)
	html := string(body)
	assert.Empty(t, getUserSlugFromSession(t, app, req.Header.Get("Cookie")))
	assert.NotContains(t, html, "currentpass")
	assert.NotContains(t, html, "newsecurepass")
}

func TestRouter_HandleAccountPasswordSubmit_ValidationFailureDoesNotCallAPI(t *testing.T) {
	t.Parallel()

	updateCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/users/profile/password":
			updateCalled = true
			w.WriteHeader(http.StatusInternalServerError)
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

	form := "current_password=currentpass&new_password=newsecurepass&confirm_password=nomatchpass"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/password", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.False(t, updateCalled)
	html := string(body)
	assert.Contains(t, html, accountPasswordUpdateError)
	assert.Contains(t, html, "New password and confirmation must match.")
	assert.NotContains(t, html, "currentpass")
	assert.NotContains(t, html, "newsecurepass")
	assert.NotContains(t, html, "nomatchpass")
}

func TestRouter_HandleAccountPasswordSubmit_BackendCurrentPasswordFailure(t *testing.T) {
	t.Parallel()

	updateCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/users/profile/password":
			assert.Equal(t, http.MethodPut, r.Method)
			updateCalled = true
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

	form := "current_password=wrongpass&new_password=newsecurepass&confirm_password=newsecurepass"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/password", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.True(t, updateCalled)
	html := string(body)
	assert.Contains(t, html, accountPasswordUpdateError)
	assert.Contains(t, html, "Current password is incorrect.")
	assert.NotContains(t, html, "wrongpass")
	assert.NotContains(t, html, "newsecurepass")
}

func TestRouter_HandleAccountPasswordSubmit_LoadUnauthorizedRedirectsToAccountSecurity(t *testing.T) {
	t.Parallel()

	updateCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"AUTH_UNAUTHORIZED","message":"unauthorized"}}`))
		case "/api/users/profile/password":
			updateCalled = true
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

	form := "current_password=currentpass&new_password=newsecurepass&confirm_password=newsecurepass"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/password", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login?return_to=%2Faccount%3Ftab%3Dsecurity", resp.Header.Get("Location"))
	require.False(t, updateCalled)
}

func TestRouter_HandleAccountPasswordSubmit_UpdateUnauthorizedRedirectsToAccountSecurity(t *testing.T) {
	t.Parallel()

	updateCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/users/profile/password":
			assert.Equal(t, http.MethodPut, r.Method)
			updateCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"AUTH_UNAUTHORIZED","message":"unauthorized"}}`))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, emptyAccountPATListJSON())
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

	form := "current_password=currentpass&new_password=newsecurepass&confirm_password=newsecurepass"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/password", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login?return_to=%2Faccount%3Ftab%3Dsecurity", resp.Header.Get("Location"))
	require.True(t, updateCalled)
}

func TestRouter_HandleAccountPasswordSubmit_LoadFailureDoesNotCallAPI(t *testing.T) {
	t.Parallel()

	updateCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL_SERVER_ERROR","message":"internal server error"}}`))
		case "/api/users/profile/password":
			updateCalled = true
			w.WriteHeader(http.StatusNoContent)
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

	form := "current_password=currentpass&new_password=newsecurepass&confirm_password=newsecurepass"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/password", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.False(t, updateCalled)
	html := string(body)
	assert.Contains(t, html, accountPasswordUpdateError)
	assert.Contains(t, html, "load account security")
	assert.NotContains(t, html, "Password updated.")
	assert.NotContains(t, html, "currentpass")
	assert.NotContains(t, html, "newsecurepass")
}

func TestRouter_HandleCreateAccountPATSubmit_ShowsRawTokenOnce(t *testing.T) {
	t.Parallel()

	created := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			switch r.Method {
			case http.MethodGet:
				if created {
					writeJSON(w, accountPATListJSON())
					return
				}
				writeJSON(w, emptyAccountPATListJSON())
			case http.MethodPost:
				var body map[string]any
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				assert.Equal(t, "Developer CLI", body["name"])
				assert.Equal(t, "Draft workflow token", body["description"])
				assert.Equal(t, "2026-05-14T12:30:00Z", body["expires_at"])
				assert.Equal(t, true, body["can_manage_personal_access_tokens"])
				created = true
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, accountPATWithTokenJSON("account-pat-secret-created"))
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
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
	form := strings.NewReader("name=Developer+CLI&description=Draft+workflow+token&expires_at=2026-05-14T12%3A30%3A00Z&can_manage_personal_access_tokens=on")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/personal-access-tokens", form)
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
	require.True(t, created)
	assert.Contains(t, html, "Copy this token now. It will not be shown again after this page changes.")
	assert.Contains(t, html, "account-pat-secret-created")
	assert.Contains(t, html, "Developer CLI")
	assert.Contains(t, html, "xto_pat_abcd123")

	reloadReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/account", nil)
	reloadReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, reloadReq)
	reloadResp, err := app.Test(reloadReq)
	require.NoError(t, err)
	defer func() { _ = reloadResp.Body.Close() }()
	reloadBody, err := io.ReadAll(reloadResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, reloadResp.StatusCode)
	assert.NotContains(t, string(reloadBody), "account-pat-secret-created")
	assert.Contains(t, string(reloadBody), "xto_pat_abcd123")
}

func TestRouter_HandleCreateAccountPATSubmit_ValidationFailureDoesNotCallAPI(t *testing.T) {
	t.Parallel()

	createCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			if r.Method == http.MethodPost {
				createCalled = true
			}
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/personal-access-tokens", strings.NewReader("description=Draft"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.False(t, createCalled)
	assert.Contains(t, string(body), "Validation: name is required.")
}

func TestRouter_HandleUpdateAccountPATSubmit_UpdatesTokenMetadata(t *testing.T) {
	t.Parallel()

	updated := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			assertAccountPATBackendPageQuery(t, r)
			if updated {
				writeJSON(w, updatedAccountPATListJSON())
				return
			}
			writeJSON(w, accountPATListJSON())
		case "/api/users/profile/personal-access-tokens/developer-cli":
			assert.Equal(t, http.MethodPut, r.Method)
			var body map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, "Developer CLI renamed", body["name"])
			assert.Equal(t, "Updated local CLI token", body["description"])
			assert.Equal(t, "2026-06-01T09:15:00Z", body["expires_at"])
			assert.Equal(t, true, body["can_manage_personal_access_tokens"])
			updated = true
			writeJSON(w, updatedAccountPATJSON())
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

	form := strings.NewReader("name=Developer+CLI+renamed&description=Updated+local+CLI+token&expires_at=2026-06-01T09%3A15&can_manage_personal_access_tokens=on&" + accountPATPaginationFormValues)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/personal-access-tokens/developer-cli/update", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, updated)
	assert.Contains(t, html, "Personal access token updated.")
	assert.Contains(t, html, "Developer CLI renamed")
	assert.Contains(t, html, "Updated local CLI token")
	assert.NotContains(t, html, "account-pat-secret")
	assert.NotContains(t, html, "token_hash")
}

func TestRouter_HandleUpdateAccountPATSubmit_SendsNullsForClearedNullableFields(t *testing.T) {
	t.Parallel()

	updated := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, updatedAccountPATListJSON())
		case "/api/users/profile/personal-access-tokens/developer-cli":
			assert.Equal(t, http.MethodPut, r.Method)
			var body map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, "Developer CLI renamed", body["name"])
			assert.Contains(t, body, "description")
			assert.Nil(t, body["description"])
			assert.Contains(t, body, "expires_at")
			assert.Nil(t, body["expires_at"])
			assert.Equal(t, false, body["can_manage_personal_access_tokens"])
			updated = true
			writeJSON(w, updatedAccountPATJSON())
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

	form := strings.NewReader("name=Developer+CLI+renamed&description=&expires_at=")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/personal-access-tokens/developer-cli/update", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, updated)
}

// This test mutates process-global time.Local.
func TestParseAccountPATExpiresAtUsesUTCForDatetimeLocal(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("ReviewLocal", 3*60*60)
	defer func() {
		time.Local = originalLocal
	}()

	expiresAt, err := parseAccountPATExpiresAt("2026-05-14T12:30")
	require.NoError(t, err)
	require.NotNil(t, expiresAt.value)
	assert.Equal(t, time.Date(2026, time.May, 14, 12, 30, 0, 0, time.UTC), *expiresAt.value)

	rfc3339ExpiresAt, err := parseAccountPATExpiresAt("2026-05-14T12:30:00+02:00")
	require.NoError(t, err)
	require.NotNil(t, rfc3339ExpiresAt.value)
	assert.Equal(t, "2026-05-14T12:30:00+02:00", rfc3339ExpiresAt.value.Format(time.RFC3339))
}

func TestRouter_HandleRotateAccountPATSubmit_ShowsRawTokenOnce(t *testing.T) {
	t.Parallel()

	rotated := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			assertAccountPATBackendPageQuery(t, r)
			writeJSON(w, accountPATListJSON())
		case "/api/users/profile/personal-access-tokens/developer-cli/rotate":
			assert.Equal(t, http.MethodPost, r.Method)
			rotated = true
			writeJSON(w, accountPATWithTokenJSON("account-pat-secret-rotated"))
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

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/account/security/personal-access-tokens/developer-cli/rotate",
		strings.NewReader(accountPATPaginationFormValues),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, rotated)
	assert.Contains(t, string(body), "Rotated token for Developer CLI")
	assert.Contains(t, string(body), "account-pat-secret-rotated")
}

func TestRouter_HandleRevokeAccountPATSubmit_RevokesToken(t *testing.T) {
	t.Parallel()

	revoked := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			assertAccountPATBackendPageQuery(t, r)
			if revoked {
				writeJSON(w, emptyAccountPATListJSON())
				return
			}
			writeJSON(w, accountPATListJSON())
		case "/api/users/profile/personal-access-tokens/developer-cli":
			assert.Equal(t, http.MethodDelete, r.Method)
			revoked = true
			w.WriteHeader(http.StatusNoContent)
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

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/account/security/personal-access-tokens/developer-cli/revoke",
		strings.NewReader(accountPATPaginationFormValues),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, revoked)
	assert.Contains(t, string(body), "Personal access token revoked.")
	assert.Contains(t, string(body), "No personal access tokens on this page.")
	assert.NotContains(t, string(body), "No personal access tokens yet.")
}

func TestRouter_HandleDeleteAccountPATSubmit_RemovesRevokedToken(t *testing.T) {
	t.Parallel()

	deleted := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			if deleted {
				assert.Empty(t, r.URL.Query().Get("cursor"))
				assert.Empty(t, r.URL.Query().Get("direction"))
				writeJSON(w, emptyAccountPATListJSON())
				return
			}
			writeJSON(w, revokedAccountPATListJSON())
		case "/api/users/profile/personal-access-tokens/developer-cli/delete":
			assert.Equal(t, http.MethodPost, r.Method)
			deleted = true
			w.WriteHeader(http.StatusNoContent)
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

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/account/security/personal-access-tokens/developer-cli/delete",
		nil,
	)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, deleted)
	assert.Contains(t, string(body), "Personal access token removed.")
	assert.Contains(t, string(body), "No personal access tokens yet.")
	assert.NotContains(t, string(body), "No personal access tokens on this page.")
	assert.NotContains(t, string(body), "account-pat-secret")
}

func TestRouter_HandleDeleteAccountPATSubmit_StepsBackFromEmptyPATPage(t *testing.T) {
	t.Parallel()

	deleted := false
	listRequests := []string{}
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, accountUserJSON(true))
		case "/api/member-invitations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile/personal-access-tokens":
			assert.Equal(t, http.MethodGet, r.Method)
			if !assert.True(t, deleted) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			requestKey := r.URL.Query().Get("cursor") + ":" + r.URL.Query().Get("direction")
			listRequests = append(listRequests, requestKey)
			switch requestKey {
			case "cursor-page-2:next":
				writeJSON(w, emptyAccountPATListJSON())
			case "cursor-page-1:prev":
				writeJSON(w, previousAccountPATListJSON())
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/api/users/profile/personal-access-tokens/developer-cli/delete":
			assert.Equal(t, http.MethodPost, r.Method)
			deleted = true
			w.WriteHeader(http.StatusNoContent)
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

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/account/security/personal-access-tokens/developer-cli/delete",
		strings.NewReader(accountPATPaginationFormValuesWithPrevCursor),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, deleted)
	assert.Equal(t, []string{"cursor-page-2:next", "cursor-page-1:prev"}, listRequests)
	assert.Contains(t, html, "Personal access token removed.")
	assert.Contains(t, html, "Workflow CLI")
	assert.Contains(t, html, `name="pat_cursor" value="cursor-page-1"`)
	assert.Contains(t, html, `name="pat_direction" value="prev"`)
	assert.NotContains(t, html, "No personal access tokens yet.")
	assert.NotContains(t, html, "No personal access tokens on this page.")
	assert.NotContains(t, html, "account-pat-secret")
}

func TestRouter_HandleCreateAccountPATSubmit_RequiresAuthenticatedSession(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/personal-access-tokens", strings.NewReader("name=Developer+CLI"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login?return_to=%2Faccount%2Fsecurity%2Fpersonal-access-tokens", resp.Header.Get("Location"))
}

func TestRouter_HandleAccountPasswordSubmit_RequiresAuthenticatedSession(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	form := "current_password=currentpass&new_password=newsecurepass&confirm_password=newsecurepass"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/account/security/password", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login?return_to=%2Faccount%3Ftab%3Dsecurity", resp.Header.Get("Location"))
}

func accountUserJSON(mfaEnabled bool) string {
	return accountUserJSONWithLocalPassword(mfaEnabled, true)
}

func accountUserJSONWithLocalPassword(mfaEnabled, hasLocalPassword bool) string {
	mfaValue := "false"
	if mfaEnabled {
		mfaValue = "true"
	}
	localPasswordValue := "false"
	if hasLocalPassword {
		localPasswordValue = "true"
	}
	return `{
		"slug":"test-user",
		"full_name":"Test User",
		"email":"test@example.com",
		"role":"user",
		"locale":"en-US",
		"avatar_url":"https://example.com/avatar.png",
		"phone_number":"+48123123123",
		"job_title":"Operations Lead",
		"timezone":"Europe/Warsaw",
		"mfa_email_enabled":` + mfaValue + `,
		"has_local_password":` + localPasswordValue + `,
		"created_at":"2026-03-04T00:00:00Z",
		"updated_at":"2026-03-04T00:00:00Z"
	}`
}

func emptyAccountPATListJSON() string {
	return `{"personal_access_tokens":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`
}

func accountPATListJSON() string {
	return `{
		"personal_access_tokens":[{
			"slug":"developer-cli",
			"name":"Developer CLI",
			"description":"Draft workflow token",
			"token_prefix":"xto_pat_abcd123",
			"user_slug":"test-user",
			"can_manage_personal_access_tokens":true,
			"expires_at":"2026-12-31T23:59:59Z",
			"revoked_at":null,
			"last_used_at":null,
			"created_at":"2026-05-13T10:00:00Z",
			"updated_at":"2026-05-13T10:00:00Z"
		}],
		"has_more":false,
		"next_cursor":null,
		"prev_cursor":null
	}`
}

func accountPATListWithPrevCursorJSON(prevCursor string) string {
	return `{
		"personal_access_tokens":[{
			"slug":"developer-cli",
			"name":"Developer CLI",
			"description":"Draft workflow token",
			"token_prefix":"xto_pat_abcd123",
			"user_slug":"test-user",
			"can_manage_personal_access_tokens":true,
			"expires_at":"2026-12-31T23:59:59Z",
			"revoked_at":null,
			"last_used_at":null,
			"created_at":"2026-05-13T10:00:00Z",
			"updated_at":"2026-05-13T10:00:00Z"
		}],
		"has_more":false,
		"next_cursor":null,
		"prev_cursor":"` + prevCursor + `"
	}`
}

func previousAccountPATListJSON() string {
	return `{
		"personal_access_tokens":[{
			"slug":"workflow-cli",
			"name":"Workflow CLI",
			"description":"Previous page token",
			"token_prefix":"xto_pat_prev123",
			"user_slug":"test-user",
			"expires_at":"2026-12-31T23:59:59Z",
			"revoked_at":null,
			"last_used_at":null,
			"created_at":"2026-05-13T10:00:00Z",
			"updated_at":"2026-05-13T10:00:00Z"
		}],
		"has_more":true,
		"next_cursor":"cursor-page-2",
		"prev_cursor":null
	}`
}

func revokedAccountPATListJSON() string {
	return `{
		"personal_access_tokens":[{
			"slug":"developer-cli",
			"name":"Developer CLI",
			"description":"Draft workflow token",
			"token_prefix":"xto_pat_abcd123",
			"user_slug":"test-user",
			"expires_at":"2026-12-31T23:59:59Z",
			"revoked_at":"2026-05-14T12:30:00Z",
			"last_used_at":null,
			"created_at":"2026-05-13T10:00:00Z",
			"updated_at":"2026-05-14T12:30:00Z"
		}],
		"has_more":false,
		"next_cursor":null,
		"prev_cursor":null
	}`
}

func updatedAccountPATListJSON() string {
	return `{
		"personal_access_tokens":[` + updatedAccountPATJSON() + `],
		"has_more":false,
		"next_cursor":null,
		"prev_cursor":null
	}`
}

func updatedAccountPATJSON() string {
	return `{
		"slug":"developer-cli",
		"name":"Developer CLI renamed",
		"description":"Updated local CLI token",
		"token_prefix":"xto_pat_abcd123",
		"user_slug":"test-user",
		"expires_at":"2026-06-01T09:15:00Z",
		"revoked_at":null,
		"last_used_at":null,
		"created_at":"2026-05-13T10:00:00Z",
		"updated_at":"2026-06-01T09:15:00Z"
	}`
}

func accountPATWithTokenJSON(token string) string {
	return `{
		"slug":"developer-cli",
		"name":"Developer CLI",
		"description":"Draft workflow token",
		"token_prefix":"xto_pat_abcd123",
		"user_slug":"test-user",
		"expires_at":"2026-12-31T23:59:59Z",
		"revoked_at":null,
		"last_used_at":null,
		"created_at":"2026-05-13T10:00:00Z",
		"updated_at":"2026-05-13T10:00:00Z",
		"token":"` + token + `"
	}`
}

func assertAccountPATBackendPageQuery(t *testing.T, r *http.Request) {
	t.Helper()
	assert.Equal(t, "cursor-page-2", r.URL.Query().Get("cursor"))
	assert.Equal(t, "next", r.URL.Query().Get("direction"))
}
