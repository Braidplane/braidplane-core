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

func TestRouter_HandleOrganizationRolesPage_RequiresAuth(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/roles", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(
		t,
		"/auth/login?return_to=%2Forganizations%2Facme-hq%2Froles",
		resp.Header.Get("Location"),
	)
}

func TestRouter_HandleOrganizationRolesPage_RendersRoles(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleRolesCommonTestAPI(w, r, "admin", true) {
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/roles", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, `href="/organizations/acme-hq/roles"`)
	require.Contains(t, html, "Create role")
	require.Contains(t, html, "Roles grant organization-scoped permissions")
	require.Contains(t, html, "Editor")
	require.Contains(t, html, "members:read")
	require.Contains(t, html, "All permissions")
	require.Contains(t, html, "pipelines:execute")
	require.Contains(t, html, "System role")
	require.Contains(t, html, `data-dirty-form="organization-role-editor-update"`)
	require.Contains(t, html, `data-protected-action-modal`)
	require.Contains(t, html, `data-confirm-slug="editor"`)
	require.Contains(t, html, `name="role_confirmation"`)
	require.Contains(t, html, `hx-target="#organization-roles-content"`)
	require.Contains(t, html, "Archive role")
	require.Contains(t, html, "Archived roles are hidden from assignment")
	require.NotContains(t, html, `organization-role-admin-edit`)
	require.NotContains(t, html, `/organizations/acme-hq/roles/admin/archive`)
}

func TestRouter_HandleOrganizationRolesPage_RendersArchivedRoles(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleRolesCommonTestAPI(w, r, "admin", true) {
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/roles?deleted=true", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Archived roles")
	require.Contains(t, html, "Legacy Auditor")
	require.Contains(t, html, "Archived 2026-03-03")
	require.Contains(t, html, `action="/organizations/acme-hq/roles/legacy-auditor/restore"`)
	require.Contains(t, html, "Restore")
	require.Contains(t, html, "events:read")
	require.NotContains(t, html, "Create role")
	require.NotContains(t, html, `data-dirty-form="organization-role-legacy-auditor-update"`)
}

func TestRouter_HandleOrganizationRolesPage_GatesActionsFromPermissions(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleRolesCommonTestAPI(w, r, "viewer", true) {
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/roles", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "Editor")
	require.Contains(t, html, "No actions")
	require.Contains(t, html, "Details")
	require.NotContains(t, html, "Create role")
	require.NotContains(t, html, `data-dirty-form="organization-role-editor-update"`)
	require.NotContains(t, html, "Archive role")
}

func TestRouter_HandleOrganizationRolesPage_RendersAuthorizationError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleRolesCommonTestAPI(w, r, "viewer", false) {
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/roles", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, html, "You do not have access to this organization&#39;s roles.")
	require.Contains(t, html, "Organization roles are unavailable.")
	require.NotContains(t, html, "Create role")
}

func TestRouter_HandleCreateRoleSubmit_RendersValidationFailure(t *testing.T) {
	t.Parallel()

	createCalled := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleRolesCommonTestAPI(w, r, "admin", true) {
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/roles" {
			createCalled = true
			w.WriteHeader(http.StatusInternalServerError)
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
	form := strings.NewReader("name=&slug=editor&permission_resource=members&permission_action=read")
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/roles",
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
	require.Contains(t, html, "Role could not be created.")
	require.Contains(t, html, "Validation: role name is required.")
	require.False(t, createCalled)
}

func TestRouter_HandleCreateRoleSubmit_CreatesRole(t *testing.T) {
	t.Parallel()

	created := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleRolesCommonTestAPI(w, r, "admin", true) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/roles":
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
			assert.Equal(t, "Pipeline Operator", payload["name"])
			assert.Equal(t, "pipeline-operator", payload["slug"])
			permissions, ok := payload["permissions"].([]any)
			if assert.True(t, ok) && assert.Len(t, permissions, 1) {
				permission := permissions[0].(map[string]any)
				assert.Equal(t, "pipelines", permission["resource"])
				assert.Equal(t, "execute", permission["action"])
			}

			created = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"slug":"pipeline-operator","name":"Pipeline Operator","is_default":false,"permissions":[{"resource":"pipelines","action":"execute"}],"created_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null}`)
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
	form := strings.NewReader("name=Pipeline+Operator&slug=pipeline-operator&permission_resource=pipelines&permission_action=execute")
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/roles",
		form)

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/roles", resp.Header.Get("Location"))
	require.True(t, created)
}

func TestRouter_HandleUpdateRoleSubmit_UpdatesRole(t *testing.T) {
	t.Parallel()

	updated := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleRolesCommonTestAPI(w, r, "admin", true) {
			return
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/roles/editor":
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
			assert.Equal(t, "Senior Editor", payload["name"])
			permissions, ok := payload["permissions"].([]any)
			if assert.True(t, ok) && assert.Len(t, permissions, 1) {
				permission := permissions[0].(map[string]any)
				assert.Equal(t, "roles", permission["resource"])
				assert.Equal(t, "update", permission["action"])
			}

			updated = true
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
	form := strings.NewReader("name=Senior+Editor&permission_resource=roles&permission_action=update")
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/roles/editor/update",
		form)

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/roles", resp.Header.Get("Location"))
	require.True(t, updated)
}

func TestRouter_HandleArchiveRoleSubmit_ArchivesRole(t *testing.T) {
	t.Parallel()

	archived := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleRolesCommonTestAPI(w, r, "admin", true) {
			return
		}
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/roles/editor":
			archived = true
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
		"/organizations/acme-hq/roles/editor/archive",
		strings.NewReader("role_confirmation=editor"))

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/roles", resp.Header.Get("Location"))
	require.True(t, archived)
}

func TestRouter_HandleArchiveRoleSubmit_RequiresTypedConfirmation(t *testing.T) {
	t.Parallel()

	archived := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleRolesCommonTestAPI(w, r, "admin", true) {
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/roles/editor" {
			archived = true
			w.WriteHeader(http.StatusNoContent)
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
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/organizations/acme-hq/roles/editor/archive",
		strings.NewReader("role_confirmation=wrong"))

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
	require.Contains(t, html, "Role could not be archived.")
	require.Contains(t, html, "Validation: type the role slug to confirm archival.")
	require.False(t, archived)
}

func TestRouter_HandleRestoreRoleSubmit_RestoresRole(t *testing.T) {
	t.Parallel()

	restored := false
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleRolesCommonTestAPI(w, r, "admin", true) {
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/roles/legacy-auditor/restore":
			restored = true
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
		"/organizations/acme-hq/roles/legacy-auditor/restore",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/roles?deleted=true", resp.Header.Get("Location"))
	require.True(t, restored)
}

// handleRolesCommonTestAPI serves shared organization and role fixtures for roles page tests.
func handleRolesCommonTestAPI(w http.ResponseWriter, r *http.Request, currentRole string, allowList bool) bool {
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
			`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"`+currentRole+`","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles":
		if !allowList {
			writeStatusJSON(w, http.StatusForbidden, `{"error":{"code":"FORBIDDEN","message":"forbidden"}}`)
			return true
		}
		if r.URL.Query().Get("only_deleted") == "true" {
			if r.URL.Query().Get("include_deleted") != "true" {
				writeStatusJSON(w, http.StatusBadRequest, `{"error":{"code":"VALIDATION_FAILED","message":"include_deleted=true is required with only_deleted=true"}}`)
				return true
			}
			writeJSON(
				w,
				`{"roles":[{"slug":"legacy-auditor","name":"Legacy Auditor","is_default":false,"permissions":[{"resource":"events","action":"read"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-03T00:00:00Z","deleted_at":"2026-03-03T12:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
			return true
		}
		if r.URL.Query().Get("include_deleted") == "true" {
			writeStatusJSON(w, http.StatusBadRequest, `{"error":{"code":"VALIDATION_FAILED","message":"only_deleted=true is required for the archived roles view"}}`)
			return true
		}
		writeJSON(
			w,
			`{"roles":[{"slug":"admin","name":"Admin","is_default":true,"permissions":[{"resource":"*","action":"*"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","deleted_at":null},{"slug":"editor","name":"Editor","is_default":false,"permissions":[{"resource":"members","action":"read"},{"resource":"roles","action":"update"},{"resource":"pipelines","action":"execute"},{"resource":"api_keys","action":"create"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","deleted_at":null}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/admin":
		writeJSON(
			w,
			`{"slug":"admin","name":"Admin","is_default":true,"permissions":[{"resource":"*","action":"*"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","deleted_at":null}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/viewer":
		writeJSON(
			w,
			`{"slug":"viewer","name":"Viewer","is_default":true,"permissions":[{"resource":"roles","action":"read"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","deleted_at":null}`,
		)
		return true
	default:
		return false
	}
}
