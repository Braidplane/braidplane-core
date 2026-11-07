package frontend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestNormalizeSelectedProjectSlugClearsStaleSelections(t *testing.T) {
	t.Parallel()

	projects := []api.Project{
		{Slug: "data-pipelines", Name: "Data pipelines"},
		{Slug: "ops", Name: "Operations"},
	}

	require.Equal(t, "ops", normalizeSelectedProjectSlug(projects, " ops "))
	require.Empty(t, normalizeSelectedProjectSlug(projects, "missing"))
	require.Empty(t, normalizeSelectedProjectSlug(projects, ""))
}

func TestRouter_HandleDashboardPage_RequiresAuth(t *testing.T) {
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login?return_to=%2Fdashboard", resp.Header.Get("Location"))
}

func TestRouter_WorkspaceGate_RedirectsNoMembershipProductRoute(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"organizations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"slug":"oidc-user","full_name":"OIDC User","email":"oidc@example.com","role":"user","locale":"en","mfa_email_enabled":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/workspace?return_to=%2Fdashboard", resp.Header.Get("Location"))
}

func TestRouter_WorkspaceGate_ProductRouteRedirectsSignupIntentWithoutCreating(t *testing.T) {
	t.Parallel()

	var created atomic.Bool
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"organizations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile":
			if r.Method == http.MethodPost {
				created.Store(true)
			}
			t.Fatalf("product-route workspace middleware must not load profile on GET %s", r.URL.Path)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/workspace?return_to=%2Forganizations", resp.Header.Get("Location"))
	require.False(t, created.Load())
	require.Empty(t, getActiveOrganizationSlugFromSession(t, app, authCookie))
}

func TestRouter_WorkspaceGate_BlocksProductRouteWhenMembershipLookupFails(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/organizations", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"organization lookup failed"}`))
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Contains(t, string(body), "workspace membership unavailable")
	require.Empty(t, getActiveOrganizationSlugFromSession(t, app, authCookie))
}

func TestRouter_WorkspaceGate_PageDoesNotConsumeIntentWhenMembershipLookupFails(t *testing.T) {
	t.Parallel()

	var created atomic.Bool
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			if r.Method == http.MethodPost {
				created.Store(true)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"organization lookup failed"}`))
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"slug":"jane-doe","full_name":"Jane Doe","email":"jane@example.com","role":"user","locale":"en","mfa_email_enabled":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/workspace?return_to=%2Fdashboard", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Failed to load workspace memberships")
	require.False(t, created.Load())
	require.Empty(t, getActiveOrganizationSlugFromSession(t, app, authCookie))
}

func TestRouter_WorkspaceGate_DoesNotBypassInvalidEnvironmentSlug(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"organizations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"slug":"oidc-user","full_name":"OIDC User","email":"oidc@example.com","role":"user","locale":"en","mfa_email_enabled":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
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
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/orgs/acme-hq/projects/data-migration/runs?environment_slug=not%20valid%21",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "/workspace?return_to=")
	require.Empty(t, getActiveOrganizationSlugFromSession(t, app, authCookie))
}

func TestRouter_WorkspaceGate_NoIntentRendersGateWithoutBillingFields(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"organizations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"slug":"oidc-user","full_name":"OIDC User","email":"oidc@example.com","role":"user","locale":"en","mfa_email_enabled":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/workspace?return_to=%2Fdashboard", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, `name="usage_type" value="personal"`)
	require.NotContains(t, html, `name="organization_workspace_name"`)
	require.Contains(t, html, "OIDC User&#39;s workspace")
	require.NotContains(t, html, `name="billing_name"`)
	require.NotContains(t, html, `name="billing_email"`)
	require.NotContains(t, html, `name="tax_id"`)
}

func TestRouter_WorkspaceGate_PersonalSubmitIgnoresTamperedOrganizationName(t *testing.T) {
	t.Parallel()

	var created atomic.Bool
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			if r.Method == http.MethodGet {
				writeJSON(w, `{"organizations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				return
			}
			assert.Equal(t, http.MethodPost, r.Method)
			var body map[string]string
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, "Jane Doe's workspace", body["name"])
			created.Store(true)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"Jane Doe's workspace","slug":"jane-doe-workspace","created_at":"2026-03-09T00:00:00Z","updated_at":"2026-03-09T00:00:00Z"}`))
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"slug":"jane-doe","full_name":"Jane Doe","email":"jane@example.com","role":"user","locale":"en","mfa_email_enabled":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
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
	form := strings.NewReader("usage_type=personal&organization_workspace_name=Tampered+Workspace&return_to=%2Fdashboard")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/workspace", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/dashboard", resp.Header.Get("Location"))
	require.True(t, created.Load())
	require.Equal(t, "jane-doe-workspace", getActiveOrganizationSlugFromSession(t, app, authCookie))
}

func TestRouter_WorkspaceGate_PageRendersSelectorForUsersWithoutMembership(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"organizations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"slug":"jane-doe","full_name":"Jane Doe","email":"jane@example.com","role":"user","locale":"en","mfa_email_enabled":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/workspace?return_to=%2Forganizations", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, `name="usage_type" value="personal"`)
	require.Contains(t, html, "Jane Doe&#39;s workspace")
	require.Contains(t, html, ">Personal</strong>")
	require.Contains(t, html, ">Organization</strong>")
	require.Contains(t, html, ">Create workspace<")
	require.NotContains(t, html, "Workspace from signup")
	require.NotContains(t, html, `name="organization_workspace_name"`)
}

func TestRouter_WorkspaceGate_PersonalSubmitCreatesDerivedOrganization(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			if r.Method == http.MethodGet {
				writeJSON(w, `{"organizations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				return
			}
			assert.Equal(t, http.MethodPost, r.Method)
			var body map[string]string
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, "Jane Doe's workspace", body["name"])
			assert.NotContains(t, body, "billing_name")
			assert.NotContains(t, body, "billing_email")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"Jane Doe's workspace","slug":"jane-doe-workspace","created_at":"2026-03-09T00:00:00Z","updated_at":"2026-03-09T00:00:00Z"}`))
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"slug":"jane-doe","full_name":"Jane Doe","email":"jane@example.com","role":"user","locale":"en","mfa_email_enabled":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
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
	form := strings.NewReader("usage_type=personal&return_to=%2Forgs%2Fjane-doe-workspace%2Fdata-sources")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/workspace", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/orgs/jane-doe-workspace/data-sources", resp.Header.Get("Location"))

	activeOrg := getActiveOrganizationSlugFromSession(t, app, authCookie)
	require.Equal(t, "jane-doe-workspace", activeOrg)
}

func TestRouter_WorkspaceGate_OrganizationSubmitCreatesNamedOrganization(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			if r.Method == http.MethodGet {
				writeJSON(w, `{"organizations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				return
			}
			assert.Equal(t, http.MethodPost, r.Method)
			var body map[string]string
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, "Acme Operations", body["name"])
			assert.NotContains(t, body, "billing_name")
			assert.NotContains(t, body, "billing_email")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"Acme Operations","slug":"acme-operations","created_at":"2026-03-09T00:00:00Z","updated_at":"2026-03-09T00:00:00Z"}`))
		case "/api/users/profile":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"slug":"jane-doe","full_name":"Jane Doe","email":"jane@example.com","role":"user","locale":"en","mfa_email_enabled":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
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
	form := strings.NewReader("usage_type=organization&organization_workspace_name=Acme+Operations&return_to=%2Fdashboard")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/workspace", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/dashboard", resp.Header.Get("Location"))

	activeOrg := getActiveOrganizationSlugFromSession(t, app, authCookie)
	require.Equal(t, "acme-operations", activeOrg)
}

func TestDerivePersonalWorkspaceName_FallsBackToEmailLocalPart(t *testing.T) {
	t.Parallel()

	name := derivePersonalWorkspaceName(&api.User{
		FullName: "",
		Email:    "casey@example.com",
	})

	require.Equal(t, "casey's workspace", name)
}

func TestRouter_WorkspaceGate_SubmitExistingMembershipBypassesCreate(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/organizations" {
			t.Fatalf("unexpected API request %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := strings.NewReader("usage_type=organization&organization_workspace_name=Duplicate+Workspace&return_to=%2Fdashboard")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/workspace", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/dashboard", resp.Header.Get("Location"))

	activeOrg := getActiveOrganizationSlugFromSession(t, app, authCookie)
	require.Equal(t, "acme-hq", activeOrg)
}

func TestRouter_WorkspaceGate_ExistingMembershipBypassesGateAndSetsActiveOrganization(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme-hq/projects":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"projects":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme-hq/roles/owner":
			writeJSON(w, `{"slug":"owner","name":"Owner","permissions":[{"resource":"*","action":"*"}]}`)
		case "/api/organizations/acme-hq/data-sources":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme-hq/events":
			assert.Equal(t, http.MethodGet, r.Method)
			writeJSON(w, `{"events":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme-hq/query-execution-summary":
			_, _ = w.Write([]byte(`{"successful_execution":null}`))
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("Location"))

	activeOrg := getActiveOrganizationSlugFromSession(t, app, authCookie)
	require.Equal(t, "acme-hq", activeOrg)
}

func TestRouter_HandleDashboardPage_RendersOperationalOverview(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		assert.NotEmpty(t, r.Header.Get(fiber.HeaderXRequestID))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Northwind BI","slug":"northwind-bi","member_role":"admin","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Data migration","slug":"data-migration","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments":
			_, _ = w.Write(
				[]byte(
					`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":null,"is_default":true,"enabled_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"environment_slug":"prod","environment_name":"Production","environment_description":null,"is_default":false,"enabled_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments/dev/runs":
			if r.URL.Query().Get("status") == "failed" {
				_, _ = w.Write(
					[]byte(
						`{"runs":[{"run_slug":"run-failed-001","pipeline_slug":"orders-sync","environment_slug":"dev","status":"failed","created_at":"2026-03-03T09:30:00Z","started_at":"2026-03-03T09:30:01Z","ended_at":"2026-03-03T09:30:04Z","duration_ms":3000,"error_summary":"source query timed out","failure_kind":"data_source","incremental":{"mode":"full","replay_safety_status":"unknown","replay_blocked_reason":"","source_step_slug":""},"pipeline_version_spec_hash":"hash-orders-001","replay_from_step":null,"replay_of_run_slug":null,"request_id":"req-failed-001","source":{"type":"manual","actor_type":"user","actor_slug":"ops-user","attempt_number":1,"entrypoint":"ui","parameter_keys":[],"request_id":"req-failed-001","retry_of_run_slug":null,"root_run_slug":"run-failed-001","source_event_slug":null,"summary":"Manual run from pipeline detail","trace_id":null},"step_count":2,"step_slug":"read-orders"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
					),
				)
				return
			}
			_, _ = w.Write(
				[]byte(
					`{"runs":[{"run_slug":"run-failed-001","pipeline_slug":"orders-sync","environment_slug":"dev","status":"failed","created_at":"2026-03-03T09:30:00Z","started_at":"2026-03-03T09:30:01Z","ended_at":"2026-03-03T09:30:04Z","duration_ms":3000,"error_summary":"source query timed out","failure_kind":"data_source","incremental":{"mode":"full","replay_safety_status":"unknown","replay_blocked_reason":"","source_step_slug":""},"pipeline_version_spec_hash":"hash-orders-001","replay_from_step":null,"replay_of_run_slug":null,"request_id":"req-failed-001","source":{"type":"manual","actor_type":"user","actor_slug":"ops-user","attempt_number":1,"entrypoint":"ui","parameter_keys":[],"request_id":"req-failed-001","retry_of_run_slug":null,"root_run_slug":"run-failed-001","source_event_slug":null,"summary":"Manual run from pipeline detail","trace_id":null},"step_count":2,"step_slug":"read-orders"},{"run_slug":"run-ok-001","pipeline_slug":"inventory-refresh","environment_slug":"dev","status":"succeeded","created_at":"2026-03-03T09:20:00Z","started_at":"2026-03-03T09:20:01Z","ended_at":"2026-03-03T09:20:03Z","duration_ms":2000,"error_summary":null,"failure_kind":null,"incremental":{"mode":"full","replay_safety_status":"unknown","replay_blocked_reason":"","source_step_slug":""},"pipeline_version_spec_hash":"hash-inventory-001","replay_from_step":null,"replay_of_run_slug":null,"request_id":"req-ok-001","source":{"type":"manual","actor_type":"user","actor_slug":"ops-user","attempt_number":1,"entrypoint":"ui","parameter_keys":[],"request_id":"req-ok-001","retry_of_run_slug":null,"root_run_slug":"run-ok-001","source_event_slug":null,"summary":"Manual run from pipeline detail","trace_id":null},"step_count":1,"step_slug":"read-inventory"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/pipelines":
			_, _ = w.Write(
				[]byte(
					`{"pipelines":[{"name":"Orders sync","slug":"orders-sync","has_draft":false,"sync_status":"synced","current_version_spec_hash":"hash-orders-001","last_applied_commit":null,"source_ref":"refs/heads/main","source_path":"pipelines/test.pipeline.yaml","created_at":"2026-03-03T09:00:00Z","updated_at":"2026-03-03T09:00:00Z"},{"name":"Inventory refresh","slug":"inventory-refresh","has_draft":true,"sync_status":"drifted","current_version_spec_hash":"hash-inventory-001","last_applied_commit":null,"source_ref":"refs/heads/main","source_path":"pipelines/test.pipeline.yaml","created_at":"2026-03-03T09:05:00Z","updated_at":"2026-03-03T09:05:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments/dev/pipeline-deployments":
			_, _ = w.Write(
				[]byte(
					`{"deployments":[{"project_slug":"default","environment_slug":"dev","pipeline_slug":"orders-sync","current_version_spec_hash":"hash-orders-001","current_version_short_spec_hash":"hash-orders","previous_version_spec_hash":null,"previous_version_short_spec_hash":null,"deployed_by_actor_type":"user","deployed_by_actor_slug":"ops-user","deployed_at":"2026-03-03T09:10:00Z","updated_at":"2026-03-03T09:10:00Z"}]}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"name":"Orders warehouse","slug":"orders-warehouse","type":"postgres","config":{"type":"postgres"},"is_active":true,"read_only":false,"last_tested_at":"2026-03-03T09:00:00Z","test_error":null,"credential":null,"created_at":"2026-03-03T08:00:00Z","updated_at":"2026-03-03T08:00:00Z"},{"name":"Archive export","slug":"archive-export","type":"s3","config":{"type":"s3"},"is_active":false,"read_only":true,"last_tested_at":null,"test_error":"missing credentials","credential":null,"created_at":"2026-03-03T08:05:00Z","updated_at":"2026-03-03T08:05:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources/orders-warehouse/queries":
			_, _ = w.Write(
				[]byte(
					`{"queries":[{"name":"Recent orders","slug":"recent-orders","data_source_slug":"orders-warehouse","description":"recent order rows","parameters":[],"query_statement":{"sql":"select * from orders"},"row_limit":100,"timeout_seconds":30,"created_at":"2026-03-03T08:15:00Z","updated_at":"2026-03-03T08:15:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/data-sources/archive-export/queries":
			_, _ = w.Write(
				[]byte(`{"queries":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
			)
		case "/api/organizations/acme-hq/projects/default/data-source-refs":
			_, _ = w.Write(
				[]byte(
					`{"data_source_refs":[{"pipeline_slug":"orders-sync","name":"Orders source","slug":"orders-source","data_source_type":"postgres","required_capabilities":["pipeline_source"],"created_at":"2026-03-03T08:20:00Z","updated_at":"2026-03-03T08:20:00Z"},{"pipeline_slug":"orders-sync","name":"Archive sink","slug":"archive-sink","data_source_type":"s3","required_capabilities":["pipeline_sink"],"created_at":"2026-03-03T08:21:00Z","updated_at":"2026-03-03T08:21:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments/dev/data-source-bindings":
			_, _ = w.Write(
				[]byte(
					`{"data_source_bindings":[{"pipeline_slug":"orders-sync","environment_slug":"dev","data_source_ref_slug":"orders-source","data_source_ref_name":"Orders source","data_source_slug":"orders-warehouse","data_source_name":"Orders warehouse","data_source_type":"postgres","data_source_active":true,"data_source_read_only":false,"bound_at":"2026-03-03T08:30:00Z","updated_at":"2026-03-03T08:30:00Z"},{"pipeline_slug":"orders-sync","environment_slug":"dev","data_source_ref_slug":"archive-sink","data_source_ref_name":"Archive sink","data_source_slug":"archive-export","data_source_name":"Archive export","data_source_type":"s3","data_source_active":false,"data_source_read_only":true,"bound_at":"2026-03-03T08:31:00Z","updated_at":"2026-03-03T08:31:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard?project_slug=default&environment_slug=dev", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Overview")
	require.Contains(t, string(body), "Dashboard scope")
	require.Contains(t, string(body), "Recorded failures")
	require.Contains(t, string(body), "A past failure does not establish current health")
	require.NotContains(t, string(body), "Latest failed run")
	require.Contains(t, string(body), "Recent activity")
	require.Contains(t, string(body), "Pipeline status")
	require.Contains(t, string(body), "Data-source readiness")
	require.Contains(t, string(body), "Query inventory")
	require.Contains(t, string(body), "Contract checks")
	require.NotContains(t, string(body), "Project areas")
	require.NotContains(t, string(body), "Organization areas")
	require.NotContains(t, string(body), "Dashboard telemetry")
	require.NotContains(t, string(body), "Quick commands")
	require.Contains(t, string(body), "Acme HQ")
	require.Contains(t, string(body), "Default project")
	require.Contains(t, string(body), "Project operations")
	require.Contains(t, string(body), "Development")
	require.Contains(t, string(body), "Runtime target")
	require.Contains(t, string(body), "run-failed-001")
	require.Contains(t, string(body), "source query timed out")
	require.Contains(t, string(body), "orders-sync")
	require.Contains(t, string(body), "Archive export")
	require.Contains(t, string(body), "Archive sink")
	require.Contains(t, string(body), "Skip to main content")
	require.Contains(t, string(body), `aria-current="page"`)
	require.Contains(t, string(body), ">Overview<")
	require.NotContains(t, string(body), `aria-label="Switch project runtime target"`)
	require.Contains(t, string(body), `aria-label="Choose dashboard project scope"`)
	require.Contains(t, string(body), `aria-label="Choose dashboard runtime target"`)
	require.Contains(t, string(body), `<option selected value="default">Default project</option>`)
	require.Contains(t, string(body), `<option selected value="dev">Target: Development</option>`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/pipelines?environment_slug=dev"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/pipelines?deployment=deployed&amp;environment_slug=dev"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/pipelines?deployment=not_deployed&amp;environment_slug=dev"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/pipelines?draft=draft&amp;environment_slug=dev"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/pipelines?environment_slug=dev&amp;sync_status=drifted"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/runs?environment_slug=dev&amp;status=failed"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/runs?environment_slug=dev&amp;status=succeeded"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/runs/run-failed-001?environment_slug=dev"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources?filter=active"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources?filter=attention"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources?filter=read_only"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources?filter=failed"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources?filter=untested"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources?filter=with_queries"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/data-sources?filter=without_queries"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/queries"`)
	require.Contains(t, string(body), `href="/orgs/acme-hq/projects/default/environments?environment_slug=dev&amp;section=configuration#project-binding-matrix"`)
	require.NotContains(t, string(body), `id="project-switcher"`)
	require.NotContains(t, string(body), `aria-label="Search or run command"`)
	require.NotContains(t, string(body), `aria-label="Open alerts"`)
	require.NotContains(t, string(body), `aria-label="Open help"`)
	require.NotContains(t, string(body), "Create Organization")

	rememberedCookieParts := []string{authCookie}
	foundProjectScopeCookie := false
	foundEnvironmentScopeCookie := false
	for _, cookie := range resp.Cookies() {
		switch cookie.Name {
		case dashboardScopeProjectCookieName:
			foundProjectScopeCookie = true
			rememberedCookieParts = append(rememberedCookieParts, cookie.Name+"="+cookie.Value)
		case dashboardScopeEnvironmentCookieName:
			foundEnvironmentScopeCookie = true
			rememberedCookieParts = append(rememberedCookieParts, cookie.Name+"="+cookie.Value)
		}
	}
	require.True(t, foundProjectScopeCookie)
	require.True(t, foundEnvironmentScopeCookie)

	rememberedReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", nil)
	rememberedReq.Header.Set("Cookie", strings.Join(rememberedCookieParts, "; "))
	addCSRFToken(t, app, rememberedReq)

	rememberedResp, err := app.Test(rememberedReq)
	require.NoError(t, err)
	defer func() { _ = rememberedResp.Body.Close() }()

	rememberedBody, err := io.ReadAll(rememberedResp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, rememberedResp.StatusCode)
	require.Contains(t, string(rememberedBody), `<option selected value="default">Default project</option>`)
	require.Contains(t, string(rememberedBody), `<option selected value="dev">Target: Development</option>`)
	require.Contains(t, string(rememberedBody), "run-failed-001")
}

func TestRouter_HandleOrganizationsPage_RendersOrganizationsList(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		assert.NotEmpty(t, r.Header.Get(fiber.HeaderXRequestID))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `href="/organizations/acme-hq"`)
	require.Contains(t, string(body), `aria-current="page"`)
	require.Contains(t, string(body), `>Organizations<`)
	require.Contains(t, string(body), `href="/organizations/new"`)
}

func TestRouter_HandleOrganizationDetailPage_RendersOrganizationDetails(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
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
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/organizations/acme-hq",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Acme HQ")
	require.Contains(t, string(body), "Organization identity and management metadata.")
	require.Contains(t, string(body), `href="/organizations"`)
	require.Contains(t, string(body), `href="/organizations/acme-hq/members"`)
	require.Contains(t, string(body), `href="/organizations/acme-hq/security"`)
	require.Contains(t, string(body), `href="/organizations/acme-hq/retention"`)
	require.NotContains(t, string(body), `href="/organizations/acme-hq/billing"`)
	require.Contains(t, string(body), `aria-current="page"`)
	require.NotContains(t, string(body), "Data retention")
	require.NotContains(t, string(body), `action="/organizations/acme-hq/retention"`)
	require.NotContains(t, string(body), "Open sections")
	require.NotContains(t, string(body), "Active Context")
	require.NotContains(t, string(body), "Switch Context")
}

func TestRouter_HandleOrganizationRetentionPage_RendersSanitizedMaintenanceHistory(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
			writeJSON(w, `{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/retention":
			writeJSON(w, `{"query_execution_retention_days":7,"replay_checkpoint_retention_days":8,"run_history_retention_days":20,"event_log_retention_days":21}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/retention/runs":
			writeJSON(w, `{
				"schedule":{"cadence":"daily","timezone":"UTC","next_run_at":"2026-07-24T00:00:00Z"},
				"runs":[{
					"slug":"retention-maintenance-a1",
					"status":"succeeded",
					"attempt":1,
					"policy_snapshot":{"query_execution_retention_days":7,"replay_checkpoint_retention_days":8,"run_history_retention_days":20,"event_log_retention_days":21},
					"outcome":{"query_executions_deleted":3,"replay_checkpoints_deleted":4,"pipeline_runs_deleted":5,"event_logs_deleted":6},
					"failure_code":null,
					"started_at":"2026-07-23T00:00:00Z",
					"completed_at":"2026-07-23T00:00:02Z"
				}]
			}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/admin":
			writeJSON(w, `{"slug":"admin","name":"Admin","is_default":true,"permissions":[{"resource":"organizations","action":"update"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
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
		"/organizations/acme-hq/retention?retention=saved",
		nil,
	)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Data retention")
	require.Contains(t, html, "System managed")
	require.Contains(t, html, "Retention policy saved.")
	require.Contains(t, html, `action="/organizations/acme-hq/retention"`)
	require.Contains(t, html, "Maintenance history")
	require.Contains(t, html, "retention-maintenance-a1")
	require.Contains(t, html, "Deleted total")
	require.Contains(t, html, ">18<")
	require.Contains(t, html, "Policy applied")
	require.NotContains(t, html, "maintenance_failed")
	require.NotContains(t, html, "SQL")
	require.NotContains(t, html, "credentials")
	require.NotContains(t, html, ">Execute<")
	require.NotContains(t, html, ">Delete<")
}

func TestRouter_HandleOrganizationRetentionPage_IsReadOnlyWithoutUpdatePermission(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
			writeJSON(w, `{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/retention":
			writeJSON(w, `{"query_execution_retention_days":7,"replay_checkpoint_retention_days":8,"run_history_retention_days":20,"event_log_retention_days":21}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/retention/runs":
			writeJSON(w, `{"schedule":{"cadence":"daily","timezone":"UTC","next_run_at":"2026-07-24T00:00:00Z"},"runs":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"viewer","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/viewer":
			writeJSON(w, `{"slug":"viewer","name":"Viewer","is_default":true,"permissions":[{"resource":"organizations","action":"read"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/retention", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "read-only access")
	require.Contains(t, html, "7 days")
	require.NotContains(t, html, `action="/organizations/acme-hq/retention"`)
	require.NotContains(t, html, `name="query_execution_retention_days"`)
}

func TestRouter_HandleOrganizationRetentionSubmit_RedirectsFailuresToRetentionPage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		form         string
		apiStatus    int
		apiBody      string
		wantMessage  string
		wantAPICalls int32
	}{
		{
			name:         "InvalidDays",
			form:         "query_execution_retention_days=not-a-number&replay_checkpoint_retention_days=7&run_history_retention_days=7&event_log_retention_days=7",
			wantMessage:  "organization invalid input: query_execution_retention_days must be a number",
			wantAPICalls: 0,
		},
		{
			name:         "UpdateFailure",
			form:         "query_execution_retention_days=7&replay_checkpoint_retention_days=7&run_history_retention_days=7&event_log_retention_days=7",
			apiStatus:    http.StatusInternalServerError,
			apiBody:      `{"message":"retention update failed"}`,
			wantMessage:  "Failed to update retention policy.",
			wantAPICalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var apiCalls atomic.Int32
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/retention":
					apiCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.apiStatus)
					_, _ = w.Write([]byte(tt.apiBody))
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
				"/organizations/acme-hq/retention",
				strings.NewReader(tt.form),
			)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			addCSRFToken(t, app, req)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusSeeOther, resp.StatusCode)
			location, err := resp.Location()
			require.NoError(t, err)
			require.Equal(t, "/organizations/acme-hq/retention", location.Path)
			require.Equal(t, "error", location.Query().Get("retention"))
			require.Equal(t, tt.wantMessage, location.Query().Get("retention_error"))
			require.Equal(t, tt.wantAPICalls, apiCalls.Load())
		})
	}
}

func TestRouter_HandleOrganizationRetentionSubmit_RedirectsSuccessToRetentionPage(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/retention" {
			writeJSON(w, `{"query_execution_retention_days":7,"replay_checkpoint_retention_days":8,"run_history_retention_days":20,"event_log_retention_days":21}`)
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

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/retention",
		strings.NewReader("query_execution_retention_days=7&replay_checkpoint_retention_days=8&run_history_retention_days=20&event_log_retention_days=21"),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	location, err := resp.Location()
	require.NoError(t, err)
	require.Equal(t, "/organizations/acme-hq/retention", location.Path)
	require.Equal(t, "saved", location.Query().Get("retention"))
}

func TestRouter_HandleOrganizationProjectsPage_RendersProjects(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
			_, _ = w.Write(
				[]byte(
					`{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case handleTestAdminRolePermissions(w, r):
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			assert.Equal(t, "100", r.URL.Query().Get("limit"))
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","pipeline_backend":{"data_source_slug":"pipelines-git","repo":"https://github.com/acme/pipelines.git","tracked_branch":"main"},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Data migration","slug":"data-migration","pipeline_backend":null,"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default":
			_, _ = w.Write(
				[]byte(
					`{"name":"Default project","slug":"default","pipeline_backend":{"data_source_slug":"pipelines-git","repo":"https://github.com/acme/pipelines.git","tracked_branch":"main"},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			assert.Equal(t, "100", r.URL.Query().Get("limit"))
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"name":"Pipeline GitHub","slug":"pipelines-git","type":"github","config":{"repo_url":"https://github.com/acme/pipelines.git","default_branch":"main"},"is_active":true,"read_only":false,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Warehouse","slug":"warehouse","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"warehouse"},"is_active":true,"read_only":false,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/pipelines-git/branches":
			_, _ = w.Write(
				[]byte(
					`{"branches":[{"name":"main","is_default":true},{"name":"release","is_default":false}]}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments":
			_, _ = w.Write(
				[]byte(
					`{"environments":[{"name":"Development","slug":"dev","description":"Developer target","protected":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Production","slug":"prod","description":"Customer target","protected":true,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Staging","slug":"stg","description":"Review target","protected":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments":
			_, _ = w.Write(
				[]byte(
					`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":"Developer target","is_default":true,"environment_protected":false,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"},{"environment_slug":"prod","environment_name":"Production","environment_description":"Customer target","is_default":false,"environment_protected":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines":
			assert.Equal(t, "100", r.URL.Query().Get("limit"))
			_, _ = w.Write(
				[]byte(
					`{"pipelines":[{"name":"Customer sync","slug":"customer-sync","sync_status":"synced","has_draft":true,"current_version_spec_hash":"abcdef123456","last_applied_commit":"1122334","source_path":"pipelines/customer-sync.yaml","source_ref":"refs/heads/main","created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/customer-sync":
			_, _ = w.Write(
				[]byte(
					`{"slug":"customer-sync","name":"Customer sync","sync_status":"synced","has_draft":true,"current_version_spec_hash":"abcdef123456","last_applied_commit":"1122334","source_path":"pipelines/customer-sync.yaml","source_ref":"refs/heads/main","created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-04T00:00:00Z","spec":{"metadata":{"slug":"customer-sync","name":"Customer sync"},"steps":[{"kind":"query","slug":"load-orders","query":{"data_source_ref":"orders-db","operation_type":"sql","operation_ref":"list-orders"}}]}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/data-source-refs":
			assert.Equal(t, "100", r.URL.Query().Get("limit"))
			_, _ = w.Write(
				[]byte(
					`{"data_source_refs":[{"pipeline_slug":"customer-sync","name":"Orders DB","slug":"orders-db","data_source_type":"postgres","required_capabilities":["pipeline_source"],"created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-03T00:00:00Z"},{"pipeline_slug":"customer-sync","name":"Unused DB","slug":"unused-db","data_source_type":"postgres","required_capabilities":["pipeline_source"],"created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-03T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/dev/data-source-bindings":
			_, _ = w.Write(
				[]byte(
					`{"data_source_bindings":[{"pipeline_slug":"customer-sync","environment_slug":"dev","data_source_ref_slug":"orders-db","data_source_ref_name":"Orders DB","data_source_slug":"warehouse","data_source_name":"Warehouse","data_source_type":"postgres","data_source_active":true,"data_source_read_only":false,"bound_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-03T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/data-source-bindings":
			_, _ = w.Write(
				[]byte(
					`{"data_source_bindings":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/projects", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Projects")
	require.Contains(t, string(body), `id="organization-projects-workspace"`)
	require.Contains(t, string(body), "Project portfolio")
	require.Contains(t, string(body), `aria-label="Breadcrumb"`)
	require.Contains(t, string(body), "Inspect repository and runtime readiness, then open the full page to complete setup.")
	require.Contains(t, string(body), "Setup attention")
	require.Contains(t, string(body), "no Git backend")
	require.Contains(t, string(body), "border-warning/35 bg-warning/10")
	require.Contains(t, string(body), "border-warning/50 bg-warning/15 text-warning")
	require.Contains(t, string(body), "Default project")
	require.Contains(t, string(body), "https://github.com/acme/pipelines.git")
	require.Contains(t, string(body), "main")
	require.Contains(t, string(body), "Data migration")
	require.Contains(t, string(body), `hx-target="#organization-projects-workspace"`)
	require.Contains(t, string(body), `href="/organizations/acme-hq/projects?project_slug=data-migration"`)
	require.NotContains(t, string(body), "Setup checklist")
	require.NotContains(t, string(body), "attention needed")
	require.NotContains(t, string(body), "Organization overview")
	require.NotContains(t, string(body), "setup required")
	require.NotContains(t, string(body), "backend missing")
	require.NotContains(t, string(body), `href="/orgs/acme-hq/projects/default/pipelines"`)
	require.NotContains(t, string(body), `data-dirty-submit disabled`)
	require.Contains(t, string(body), `aria-label="Switch project runtime target"`)
	require.Contains(t, string(body), `option selected value="/organizations/acme-hq/projects?environment_slug=dev"`)
	require.Contains(t, string(body), ">Target: Development</option>")
	require.NotContains(t, string(body), ">No runtime target<")
	require.Contains(t, string(body), `aria-current="page"`)
	require.NotContains(t, string(body), `href="/organizations/acme-hq/security"`)
	require.NotContains(t, string(body), `role="tab"`)

	selectedReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/projects?project_slug=default", nil)
	selectedReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, selectedReq)

	selectedResp, err := app.Test(selectedReq)
	require.NoError(t, err)
	defer func() { _ = selectedResp.Body.Close() }()

	selectedBody, err := io.ReadAll(selectedResp.Body)
	require.NoError(t, err)
	selectedHTML := string(selectedBody)

	require.Equal(t, http.StatusOK, selectedResp.StatusCode)
	require.Contains(t, selectedHTML, "Open full page")
	require.Contains(t, selectedHTML, "Repository validation unavailable")
	require.Contains(t, selectedHTML, "Default target")
	require.NotContains(t, selectedHTML, "Logical bindings need attention")
	previewStart := strings.Index(selectedHTML, "data-entity-preview")
	require.GreaterOrEqual(t, previewStart, 0)
	preview := selectedHTML[previewStart:]
	for _, forbidden := range []string{"<form", "<input", "<table", "Save backend", "Delete project", "Create and connect"} {
		require.NotContains(t, preview, forbidden)
	}
	for _, tt := range []struct{ section, want, absent string }{
		{"overview", "Project readiness", "data-dirty-form=\"project-pipeline-backend\""},
		{"repository", "Use existing repository", "name=\"mode\""},
		{"repository&repository_path=existing", "name=\"tracked_branch\"", "Create and connect"},
		{"repository&repository_path=create", "Pipeline backend", "Save backend"},
		{"pipelines", "Customer sync", "Delete project"},
		{"environments", "Project environments", "Save backend"},
		{"settings", "Delete project", "Save backend"},
	} {
		t.Run(tt.section, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/projects/default?section="+tt.section, nil)
			req.Header.Set("Cookie", authCookie)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Contains(t, string(body), tt.want)
			require.NotContains(t, string(body), tt.absent)
			require.Contains(t, string(body), "Back to project list")
			if tt.section == "environments" {
				for _, marker := range []string{`data-protected-action-modal`, `data-protected-select-form`, `data-confirm-slug="prod"`, `name="protected_environment_confirmation"`, "Logical bindings", "Manage binding rows", "Orders DB", "missing", "data-source-bindings/orders-db/upsert", "data-source-bindings/orders-db/delete", "Save binding"} {
					require.Contains(t, string(body), marker)
				}
				require.Contains(t, string(body), `hx-confirm="Remove data-source binding &#34;orders-db (dev)&#34;? Pipelines using this ref in the runtime target will lose the binding."`)
				require.Contains(t, string(body), `data-destructive-confirm="Remove data-source binding &#34;orders-db (dev)&#34;? Pipelines using this ref in the runtime target will lose the binding."`)
				require.Contains(t, string(body), `hx-target="#organization-project-full-page"`)
				require.NotContains(t, string(body), "Unused DB")
			}
			if tt.section == "settings" {
				require.Contains(t, string(body), `hx-confirm="Delete project &#34;Default project (default)&#34;? Project deletion is permanent and removes project-owned resources."`)
				require.Contains(t, string(body), `data-destructive-confirm="Delete project &#34;Default project (default)&#34;? Project deletion is permanent and removes project-owned resources."`)
			}
		})
	}
}

func TestRouter_HandleProjectOverviewEnvironmentActions_RerenderProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		uiPath        string
		form          string
		wantAPIMethod string
		wantAPIPath   string
		wantDefault   bool
		apiStatus     int
		apiBody       string
		wantStatus    int
		wantContent   string
	}{
		{
			name:          "Enable",
			uiPath:        "/organizations/acme-hq/projects/default/environments/enable",
			form:          "environment_slug=stg",
			wantAPIMethod: http.MethodPut,
			wantAPIPath:   "/api/organizations/acme-hq/projects/default/environments/stg",
			wantDefault:   false,
			wantStatus:    http.StatusOK,
			wantContent:   "Project environment enabled.",
		},
		{
			name:          "SetDefault",
			uiPath:        "/organizations/acme-hq/projects/default/environments/dev/default",
			wantAPIMethod: http.MethodPut,
			wantAPIPath:   "/api/organizations/acme-hq/projects/default/environments/dev",
			wantDefault:   true,
			wantStatus:    http.StatusOK,
			wantContent:   "Default environment updated.",
		},
		{
			name:          "Disable",
			uiPath:        "/organizations/acme-hq/projects/default/environments/dev/disable",
			wantAPIMethod: http.MethodDelete,
			wantAPIPath:   "/api/organizations/acme-hq/projects/default/environments/dev",
			wantStatus:    http.StatusOK,
			wantContent:   "Project environment disabled.",
		},
		{
			name:          "EnableBackendError",
			uiPath:        "/organizations/acme-hq/projects/default/environments/enable",
			form:          "environment_slug=stg",
			wantAPIMethod: http.MethodPut,
			wantAPIPath:   "/api/organizations/acme-hq/projects/default/environments/stg",
			wantDefault:   false,
			apiStatus:     http.StatusInternalServerError,
			apiBody:       `{"message":"enable failed"}`,
			wantStatus:    http.StatusInternalServerError,
			wantContent:   "Project environment could not be enabled.",
		},
		{
			name:          "SetDefaultBackendError",
			uiPath:        "/organizations/acme-hq/projects/default/environments/dev/default",
			wantAPIMethod: http.MethodPut,
			wantAPIPath:   "/api/organizations/acme-hq/projects/default/environments/dev",
			wantDefault:   true,
			apiStatus:     http.StatusInternalServerError,
			apiBody:       `{"message":"default failed"}`,
			wantStatus:    http.StatusInternalServerError,
			wantContent:   "Default environment could not be updated.",
		},
		{
			name:          "DisableBackendError",
			uiPath:        "/organizations/acme-hq/projects/default/environments/dev/disable",
			wantAPIMethod: http.MethodDelete,
			wantAPIPath:   "/api/organizations/acme-hq/projects/default/environments/dev",
			apiStatus:     http.StatusInternalServerError,
			apiBody:       `{"message":"disable failed"}`,
			wantStatus:    http.StatusInternalServerError,
			wantContent:   "Project environment could not be disabled.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var mutationSeen atomic.Bool
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if handleTestAdminRolePermissions(w, r) {
					return
				}

				switch {
				case r.Method == tt.wantAPIMethod && r.URL.Path == tt.wantAPIPath:
					mutationSeen.Store(true)
					if tt.apiStatus != 0 {
						w.WriteHeader(tt.apiStatus)
						_ = json.NewEncoder(w).Encode(json.RawMessage(tt.apiBody))
						return
					}
					if tt.wantAPIMethod == http.MethodPut {
						var body map[string]bool
						if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						assert.Equal(t, tt.wantDefault, body["is_default"])
						writeJSON(w, `{"environment_slug":"dev","environment_name":"Development","environment_description":"Developer target","is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}`)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
					writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
					writeJSON(w, `{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
					writeJSON(w, `{"projects":[{"name":"Default project","slug":"default","pipeline_backend":{"data_source_slug":"pipelines-git","repo":"https://github.com/acme/pipelines.git","tracked_branch":"main"},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default":
					writeJSON(w, `{"name":"Default project","slug":"default","pipeline_backend":{"data_source_slug":"pipelines-git","repo":"https://github.com/acme/pipelines.git","tracked_branch":"main"},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
					writeJSON(w, `{"data_sources":[{"name":"Pipeline GitHub","slug":"pipelines-git","type":"github","config":{"repo_url":"https://github.com/acme/pipelines.git","default_branch":"main"},"is_active":true,"read_only":false,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/pipelines-git/branches":
					writeJSON(w, `{"branches":[{"name":"main","is_default":true}]}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments":
					writeJSON(w, `{"environments":[{"name":"Development","slug":"dev","description":"Developer target","protected":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Staging","slug":"stg","description":"Review target","protected":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments":
					writeJSON(w, `{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":"Developer target","is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines":
					writeJSON(w, `{"pipelines":[{"name":"Customer sync","slug":"customer-sync","sync_status":"synced","has_draft":false,"created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-04T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/customer-sync":
					writeJSON(w, `{"slug":"customer-sync","name":"Customer sync","sync_status":"synced","has_draft":false,"created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-04T00:00:00Z","spec":{"metadata":{"slug":"customer-sync","name":"Customer sync"},"steps":[{"kind":"query","slug":"load-orders","query":{"data_source_ref":"orders-db","operation_type":"sql","operation_ref":"list-orders"}}]}}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/data-source-refs":
					writeJSON(w, `{"data_source_refs":[{"pipeline_slug":"customer-sync","name":"Orders DB","slug":"orders-db","data_source_type":"postgres","required_capabilities":["pipeline_source"],"created_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-03T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/dev/data-source-bindings":
					writeJSON(w, `{"data_source_bindings":[{"pipeline_slug":"customer-sync","environment_slug":"dev","data_source_ref_slug":"orders-db","data_source_ref_name":"Orders DB","data_source_slug":"warehouse","data_source_name":"Warehouse","data_source_type":"postgres","data_source_active":true,"data_source_read_only":false,"bound_at":"2026-03-03T00:00:00Z","updated_at":"2026-03-03T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				default:
					t.Fatalf("unexpected API request %s %s", r.Method, r.URL.String())
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
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, tt.uiPath, strings.NewReader(tt.form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			html := string(body)

			require.True(t, mutationSeen.Load())
			require.Equal(t, tt.wantStatus, resp.StatusCode)
			require.Contains(t, html, tt.wantContent)
			require.Contains(t, html, "Default target")
			require.Contains(t, html, `hx-target="#organization-projects-workspace"`)
			require.NotContains(t, html, `alert alert-success mb-5`)
		})
	}
}

func TestRouter_HandleOrganizationProjectsPage_PaginatesBackendSourcesBeyondFirstPage(t *testing.T) {
	t.Parallel()

	var sourceRequests, projectRequests, environmentRequests atomic.Int32

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/organizations/acme-hq/data-sources" {
			sourceRequests.Add(1)
		}
		if r.URL.Path == "/api/organizations/acme-hq/projects" {
			projectRequests.Add(1)
		}
		if r.URL.Path == "/api/organizations/acme-hq/projects/default/environments" {
			environmentRequests.Add(1)
		}
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
			writeJSON(w, `{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default":
			writeJSON(w, `{"name":"Default project","slug":"default","pipeline_backend":{"data_source_slug":"pipelines-git","repo":"https://github.com/acme/pipelines.git","tracked_branch":"main"},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			writeJSON(w, `{"projects":[{"name":"Default project","slug":"default","pipeline_backend":{"data_source_slug":"pipelines-git","repo":"https://github.com/acme/pipelines.git","tracked_branch":"main"},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet &&
			r.URL.Path == "/api/organizations/acme-hq/data-sources" &&
			r.URL.Query().Get("limit") == "100" &&
			r.URL.Query().Get("cursor") == "":
			writeJSON(w, `{"data_sources":[{"name":"Warehouse","slug":"warehouse","type":"postgres","config":{"host":"db.example.com","port":5432,"database":"warehouse"},"is_active":true,"read_only":false,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":true,"next_cursor":"page-2","prev_cursor":null}`)
		case r.Method == http.MethodGet &&
			r.URL.Path == "/api/organizations/acme-hq/data-sources" &&
			r.URL.Query().Get("limit") == "100" &&
			r.URL.Query().Get("cursor") == "page-2" &&
			r.URL.Query().Get("direction") == "next":
			writeJSON(w, `{"data_sources":[{"name":"Pipeline GitHub","slug":"pipelines-git","type":"github","config":{"repo_url":"https://github.com/acme/pipelines.git","default_branch":"main"},"is_active":true,"read_only":false,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Later Git","slug":"later-git","type":"git","config":{"repo_url":"https://github.com/acme/later.git","default_branch":"main"},"is_active":true,"read_only":false,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/pipelines-git/branches":
			writeJSON(w, `{"branches":[{"name":"main","is_default":true},{"name":"release","is_default":false}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/environments":
			writeJSON(w, `{"environments":[{"name":"Development","slug":"dev","description":"Developer target","protected":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments":
			writeJSON(w, `{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":"Developer target","is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines":
			writeJSON(w, `{"pipelines":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/data-source-refs":
			writeJSON(w, `{"data_source_refs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/dev/data-source-bindings":
			writeJSON(w, `{"data_source_bindings":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		default:
			t.Fatalf("unexpected API request %s %s", r.Method, r.URL.String())
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
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/organizations/acme-hq/projects/default?section=repository&repository_path=existing",
		nil,
	)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Pipeline GitHub")
	require.Contains(t, html, "Later Git")
	require.Contains(t, html, "Loading branches")
	require.NotContains(t, html, "Selected Git data source is not available.")
	require.EqualValues(t, 2, sourceRequests.Load(), "both panels share the same two-page source listing")
	require.EqualValues(t, 1, projectRequests.Load(), "project shell builds navigation once")
	require.EqualValues(t, 2, environmentRequests.Load(), "shell and operational content each load environments once")
}

func TestRouter_HandleOrganizationProjectSubmit_CreatesAndDeletesProject(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/projects":
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.Contains(t, string(body), `"name":"Operations"`)
			assert.Contains(t, string(body), `"slug":"ops"`)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(
				[]byte(
					`{"name":"Operations","slug":"ops","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/projects/ops":
			assert.Equal(t, "false", r.URL.Query().Get("cleanup_remote"))
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
	createReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/projects",
		strings.NewReader("name=Operations&slug=ops"),
	)
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, createReq)

	createResp, err := app.Test(createReq)
	require.NoError(t, err)
	_ = createResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, createResp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/projects?project_slug=ops", createResp.Header.Get("Location"))

	deleteReq := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/projects/ops/delete",
		nil,
	)
	deleteReq.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, deleteReq)

	deleteResp, err := app.Test(deleteReq)
	require.NoError(t, err)
	_ = deleteResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, deleteResp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/projects", deleteResp.Header.Get("Location"))
}

func TestRouter_HandleOrganizationDetailPage_OmitsRedundantActiveContextWhenViewedOrgIsActive(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"},{"name":"Northwind BI","slug":"northwind-bi","member_role":"admin","created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
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
	setActiveOrganizationSlugInSession(t, app, authCookie, "acme-hq")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Organization identity and management metadata.")
	require.NotContains(t, string(body), "Active Context")
	require.NotContains(t, string(body), "Switch Context")
}

func TestRouter_HandleOrganizationDetailPage_NotFoundRendersUnavailablePage(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/missing-org":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/missing-org", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, string(body), "Organization unavailable")
	require.Contains(t, string(body), `href="/organizations"`)
	require.Contains(t, string(body), "organization not found")
	require.NotContains(t, string(body), "organization not found\n")
}

func TestRouter_HandleUpdateProjectPipelineBackendSubmit_Success(t *testing.T) {
	t.Parallel()

	var updatePayload map[string]any
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipeline-backend":
			if err := json.NewDecoder(r.Body).Decode(&updatePayload); err != nil {
				t.Errorf("decode update project payload: %v", err)
			}
			_, _ = w.Write(
				[]byte(
					`{"name":"Fresh project name","slug":"default","pipeline_backend":{"data_source_slug":"pipelines-git","repo":"https://example.com/acme/pipelines.git","tracked_branch":"main"},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
			_, _ = w.Write(
				[]byte(
					`{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Fresh project name","slug":"default","pipeline_backend":{"data_source_slug":"pipelines-git","repo":"https://example.com/acme/pipelines.git","tracked_branch":"main"},"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"name":"Pipeline GitHub","slug":"pipelines-git","type":"github","config":{"repo_url":"https://example.com/acme/pipelines.git","default_branch":"main"},"is_active":true,"read_only":false,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources/pipelines-git/branches":
			_, _ = w.Write([]byte(`{"branches":[{"name":"main","is_default":true}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments":
			_, _ = w.Write(
				[]byte(
					`{"project_environments":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
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
	form := strings.NewReader("data_source_slug=pipelines-git&tracked_branch=main&pipeline_dir=pipelines")
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/projects/default/settings/pipeline-backend",
		form)

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Pipeline repository validated and registered at an exact commit.")
	require.Contains(t, string(body), "Fresh project name")
	require.Contains(t, string(body), "https://example.com/acme/pipelines.git")
	require.Equal(t, "pipelines-git", updatePayload["data_source_slug"])
	require.Equal(t, "main", updatePayload["tracked_branch"])
	require.Equal(t, "pipelines", updatePayload["pipeline_dir"])
	require.Equal(t, "adopt_existing", updatePayload["mode"])
}

func TestRouter_HandleUpdateProjectPipelineBackendSubmit_RequiresDataSourceSlug(
	t *testing.T,
) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
			_, _ = w.Write(
				[]byte(
					`{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[{"name":"Pipeline GitHub","slug":"pipelines-git","type":"github","config":{"repo_url":"https://example.com/acme/pipelines.git","default_branch":"main"},"is_active":true,"read_only":false,"last_tested_at":null,"test_error":null,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
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
	form := strings.NewReader("data_source_slug=&tracked_branch=main&mode=adopt_existing")
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/projects/default/settings/pipeline-backend",
		form)

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "Pipeline backend settings could not be updated.")
	require.Contains(t, string(body), "Data source slug is required.")
	require.Contains(t, string(body), `id="organization-projects-workspace"`)
}

func TestRouter_HandleUpdateProjectPipelineBackendSubmit_RendersErrorOnAPIFailure(
	t *testing.T,
) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipeline-backend":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"VALIDATION_FAILED","message":"validation failed","detailed_error":"Choose a tested Git connection before saving the backend.","validation_errors":[{"field":"data_source_slug","message":"is unavailable"}]}}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
			_, _ = w.Write(
				[]byte(
					`{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default":
			writeJSON(w, `{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(
					`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/repository-setup":
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
	form := strings.NewReader("data_source_slug=missing-backend&tracked_branch=release&pipeline_dir=custom-path&mode=create_from_template&provider_owner=acme&repository_name=custom-repo")
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/projects/default/settings/pipeline-backend?return_to=full&section=repository&repository_path=create",
		form)

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "Choose a tested Git connection before saving the backend.")
	require.Contains(t, string(body), "validation failed")
	require.Contains(t, string(body), "validation=data_source_slug:is unavailable")
	require.Contains(t, string(body), `id="organization-project-full-page"`)
	require.Contains(t, string(body), "Create and connect")
	require.Contains(t, string(body), `value="acme"`)
	require.Contains(t, string(body), `value="custom-repo"`)
	require.Contains(t, string(body), `value="release"`)
	require.Contains(t, string(body), `value="custom-path"`)
	require.Regexp(t, `<details[^>]* open(?:>| )`, string(body))
	require.NotContains(t, string(body), "Save backend")
}

func TestRouter_HandleOrganizationProjectsPage_RendersRedirectedError(
	t *testing.T,
) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		assert.NotEmpty(t, r.Header.Get(fiber.HeaderXRequestID))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq":
			_, _ = w.Write(
				[]byte(
					`{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
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
	setActiveOrganizationSlugInSession(t, app, authCookie, "acme-hq")
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/organizations/acme-hq/projects?error=Pipeline+backend+settings+could+not+be+updated.&error_details=backend+rejected",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `role="alert"`)
	require.NotContains(t, string(body), "data-error-popup")
	require.Contains(t, string(body), "Pipeline backend settings could not be updated.")
	require.Contains(t, string(body), "backend rejected")
}

func TestRouter_HandleDashboardPage_OmitsOrganizationPaginationControls(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":true,"next_cursor":"next-123","prev_cursor":"prev-456"}`,
				),
			)
		case "/api/organizations/acme-hq/projects":
			_, _ = w.Write(
				[]byte(
					`{"projects":[{"name":"Default project","slug":"default","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments":
			_, _ = w.Write(
				[]byte(
					`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":null,"is_default":true,"enabled_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
		case "/api/organizations/acme-hq/projects/default/environments/dev/runs":
			_, _ = w.Write(
				[]byte(`{"runs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
			)
		case "/api/organizations/acme-hq/projects/default/pipelines":
			_, _ = w.Write(
				[]byte(`{"pipelines":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
			)
		case "/api/organizations/acme-hq/projects/default/environments/dev/pipeline-deployments":
			_, _ = w.Write(
				[]byte(`{"deployments":[]}`),
			)
		case "/api/organizations/acme-hq/data-sources":
			_, _ = w.Write(
				[]byte(`{"data_sources":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
			)
		case "/api/organizations/acme-hq/projects/default/data-source-refs":
			_, _ = w.Write(
				[]byte(`{"data_source_refs":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
			)
		case "/api/organizations/acme-hq/projects/default/environments/dev/data-source-bindings":
			_, _ = w.Write(
				[]byte(`{"data_source_bindings":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
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
	setActiveOrganizationSlugInSession(t, app, authCookie, "acme-hq")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard?project_slug=default&environment_slug=dev", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Recorded failures")
	require.Contains(t, string(body), "No failed runs returned for this runtime target.")
	require.Contains(t, string(body), "No pipelines exist in this project.")
	require.Contains(t, string(body), "No data sources exist in this organization.")
	require.Contains(t, string(body), "No logical data-source refs are declared for this project.")
	require.NotContains(t, string(body), "Dashboard telemetry")
	require.NotContains(t, string(body), "href=\"/dashboard?cursor=prev-456\"")
	require.NotContains(t, string(body), "href=\"/dashboard?cursor=next-123\"")
}

func TestRouter_HandleDashboardPage_ShowsErrorOnAPIFailure(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(
			[]byte(`{"error":{"code":"INTERNAL_ERROR","message":"something went wrong"}}`),
		)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "something went wrong")
}

func TestRouter_HandleDashboardPage_RedirectsToLoginOn401(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"session expired"}}`))
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login?return_to=%2Fdashboard", resp.Header.Get("Location"))
}

func TestRouter_HandleDashboardPage_ShowsErrorOn403(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"AUTH_FORBIDDEN","message":"forbidden"}}`))
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "forbidden")
	require.NotContains(t, resp.Header.Get("Location"), "/auth/login")
}

func TestRouter_HandleCreateOrganizationPage_RequiresAuth(t *testing.T) {
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/new", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login?return_to=%2Forganizations%2Fnew", resp.Header.Get("Location"))
}

func TestRouter_HandleCreateOrganizationPage_RendersBreadcrumbs(t *testing.T) {
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
	setActiveOrganizationSlugInSession(t, app, authCookie, "acme-hq")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/new", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `aria-label="Breadcrumb"`)
	require.Contains(t, string(body), `href="/organizations"`)
	require.Contains(t, string(body), "Create organization")
	require.Contains(t, string(body), `aria-current="page"`)
}

func TestRouter_HandleCreateOrganizationSubmit_SuccessRedirectsToOrganizations(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			assert.Equal(t, "/api/organizations", r.URL.Path)
			writeJSON(w, `{"organizations":[{"name":"Existing HQ","slug":"existing-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case http.MethodPost:
			assert.Equal(t, "/api/organizations", r.URL.Path)
			assert.NotEmpty(t, r.Header.Get("Cookie"))
			assert.NotEmpty(t, r.Header.Get(fiber.HeaderXRequestID))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(
				[]byte(
					`{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-09T00:00:00Z","updated_at":"2026-03-09T00:00:00Z"}`,
				),
			)
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
	form := strings.NewReader("name=Acme+HQ")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/organizations", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations", resp.Header.Get("Location"))

	activeOrg := getActiveOrganizationSlugFromSession(t, app, authCookie)
	require.Equal(t, "acme-hq", activeOrg)
}

func TestRouter_HandleCreateOrganizationSubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			assert.Equal(t, "/api/organizations", r.URL.Path)
			writeJSON(w, `{"organizations":[{"name":"Existing HQ","slug":"existing-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case http.MethodPost:
			assert.Equal(t, "/api/organizations", r.URL.Path)
			assert.NotEmpty(t, r.Header.Get("Cookie"))
			assert.NotEmpty(t, r.Header.Get(fiber.HeaderXRequestID))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(
				[]byte(
					`{"name":"Acme HQ","slug":"acme-hq","created_at":"2026-03-09T00:00:00Z","updated_at":"2026-03-09T00:00:00Z"}`,
				),
			)
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
	form := strings.NewReader("name=Acme+HQ")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/organizations", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/organizations", resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))

	activeOrg := getActiveOrganizationSlugFromSession(t, app, authCookie)
	require.Equal(t, "acme-hq", activeOrg)
}

func TestRouter_HandleCreateOrganizationSubmit_ValidationError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
			writeJSON(w, `{"organizations":[{"name":"Existing HQ","slug":"existing-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
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
	form := strings.NewReader("name=&description=Core+analytics+workspace")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/organizations", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "Name is required.")
	require.Contains(t, string(body), "name=\"description\"")
	require.Contains(t, string(body), "Core analytics workspace")
}

func TestRouter_HandleCreateOrganizationSubmit_ValidationError_HTMXRendersError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
			writeJSON(w, `{"organizations":[{"name":"Existing HQ","slug":"existing-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
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
	form := strings.NewReader("name=&description=Core+analytics+workspace")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/organizations", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Name is required.")
	require.Contains(t, string(body), "Core analytics workspace")
}

func newSwitchMembershipAPIServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/organizations", r.URL.Path)
		writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	}))
}

func TestRouter_HandleSwitchOrganization_SetsActiveOrganizationAndRedirects(t *testing.T) {
	t.Parallel()

	apiServer := newSwitchMembershipAPIServer(t)
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/organizations/acme-hq/switch", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/dashboard", resp.Header.Get("Location"))

	activeOrg := getActiveOrganizationSlugFromSession(t, app, authCookie)
	require.Equal(t, "acme-hq", activeOrg)
}

func TestRouter_HandleSwitchOrganization_RejectsOrganizationWithoutMembershipProof(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/organizations", r.URL.Path)
		writeJSON(w, `{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"owner","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/organizations/other-org/switch", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	activeOrg := getActiveOrganizationSlugFromSession(t, app, authCookie)
	require.Empty(t, activeOrg)
}

func TestRouter_HandleSwitchOrganization_UsesSafeReturnTo(t *testing.T) {
	t.Parallel()

	apiServer := newSwitchMembershipAPIServer(t)
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
		"/organizations/acme-hq/switch?return_to=%2Forganizations%2Fnew",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/new", resp.Header.Get("Location"))
}

func TestRouter_HandleSwitchOrganization_RejectsUnsafeReturnTo(t *testing.T) {
	t.Parallel()

	apiServer := newSwitchMembershipAPIServer(t)
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
		"/organizations/acme-hq/switch?return_to=https%3A%2F%2Fevil.example",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/dashboard", resp.Header.Get("Location"))
}

func TestRouter_HandleSwitchOrganization_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := newSwitchMembershipAPIServer(t)
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
		"/organizations/acme-hq/switch?return_to=%2Forganizations%2Fnew",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/organizations/new", resp.Header.Get("HX-Redirect"))
}

func TestProjectBackendBranchesDeferred(t *testing.T) {
	t.Parallel()
	branchCalls := 0
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api/organizations/acme/data-sources":
			_, _ = io.WriteString(w, `{"data_sources":[{"slug":"git","name":"Git","type":"github","config":{"repo_url":"https://github.com/example/repo.git"},"is_active":true}],"has_more":false}`)
		default:
			branchCalls++
			t.Errorf("initial rendering made unexpected request %s", req.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer apiServer.Close()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	app.Get("/", func(c fiber.Ctx) error {
		controls, _, unauthorized := router.loadProjectPipelineBackendControls(c, "acme", []api.Project{{Slug: "project", PipelineBackend: &api.ProjectPipelineBackend{DataSourceSlug: "git", TrackedBranch: "release"}}}, "project", "", "")
		require.False(t, unauthorized)
		require.Empty(t, controls.Branches)
		require.True(t, controls.BranchesPending)
		require.Equal(t, "release", controls.SelectedBranch)
		return c.SendStatus(http.StatusOK)
	})
	resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Zero(t, branchCalls)
}

func TestProjectBackendBranchesFragment(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadGateway, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch req.URL.Path {
				case "/api/organizations/acme/projects/project":
					_, _ = io.WriteString(w, `{"slug":"project","name":"Project","pipeline_backend":{"data_source_slug":"git","tracked_branch":"release","pipeline_dir":"pipelines"}}`)
				case "/api/organizations/acme/data-sources/git/branches":
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = io.WriteString(w, `{"branches":[{"name":"main","is_default":true},{"name":"release","is_default":false}]}`)
					} else {
						_, _ = io.WriteString(w, `{"message":"Branch discovery unavailable"}`)
					}
				default:
					t.Errorf("unexpected request %s", req.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer apiServer.Close()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			app.Get("/organizations/:slug/projects/:projectSlug/branches", router.handleProjectBackendBranches)
			resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme/projects/project/branches", nil))
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			if status == http.StatusUnauthorized {
				require.Equal(t, http.StatusSeeOther, resp.StatusCode)
				return
			}
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Contains(t, string(body), `id="project-backend-branches"`)
			require.Contains(t, string(body), `value="release" selected`)
			require.NotContains(t, string(body), `hx-trigger="load"`)
			if status == http.StatusOK {
				require.Contains(t, string(body), "main (default)")
			} else {
				require.Contains(t, string(body), "Retry branches")
			}
		})
	}
}
