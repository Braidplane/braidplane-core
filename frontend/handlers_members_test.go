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

func writeEmptyMemberInvitations(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || r.URL.Path != "/api/organizations/acme-hq/member-invitations" {
		return false
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(
		[]byte(`{"invitations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
	)
	return true
}

func memberInvitationJSON(email, status string) string {
	return `{"slug":"jane-example-com-abc123","organization_slug":"acme-hq","email":"` + email + `","role":"viewer","status":"` + status + `","invited_by_user_slug":"owner","accepted_user_slug":null,"expires_at":"2026-02-18T00:00:00Z","last_sent_at":"2026-02-11T00:00:00Z","accepted_at":null,"declined_at":null,"revoked_at":null,"expired_at":null,"created_at":"2026-02-11T00:00:00Z","updated_at":"2026-02-11T00:00:00Z"}`
}

func TestRouter_HandleMembersPage_RequiresAuth(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/members", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(
		t,
		"/auth/login?return_to=%2Forganizations%2Facme-hq%2Fmembers",
		resp.Header.Get("Location"),
	)
}

func TestRouter_HandleMembersPage_RendersMembersList(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		assert.NotEmpty(t, r.Header.Get(fiber.HeaderXRequestID))
		if writeEmptyMemberInvitations(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations/acme-hq/members":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"members":[{"user_slug":"alex-johnson","email":"alex@acme.com","role":"admin","created_at":"2026-02-11T00:00:00Z","updated_at":"2026-02-11T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
			return
		case "/api/organizations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
			return
		case "/api/organizations/acme-hq/roles/admin":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"slug":"admin","name":"Admin","is_default":true,"permissions":[{"resource":"members","action":"create"},{"resource":"members","action":"update"},{"resource":"members","action":"delete"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			return
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/organizations/acme-hq/members", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Members")
	require.NotContains(t, string(body), "Invite teammates by email and manage accepted organization roles.")
	require.Contains(t, string(body), "Acme HQ")
	require.Contains(t, string(body), `<span class="sr-only">Switch organization: </span>`)
	require.Contains(t, string(body), `href="/organizations/acme-hq"`)
	require.Contains(t, string(body), `aria-label="Breadcrumb"`)
	require.Contains(t, string(body), `aria-label="Organization detail sections"`)
	require.Contains(t, string(body), `href="/dashboard"`)
	require.Contains(t, string(body), `href="/organizations"`)
	require.Contains(t, string(body), `href="/organizations/acme-hq/members"`)
	require.Contains(t, string(body), `href="/organizations/acme-hq/security"`)
	require.NotContains(t, string(body), `href="/organizations/acme-hq/billing"`)
	require.Contains(t, string(body), "Members")
	require.Contains(t, string(body), "alex@acme.com")
	require.Contains(t, string(body), "alex-johnson")
	require.Contains(t, string(body), "Invite Member")
	require.NotContains(t, string(body), "Search accepted members")
	require.Contains(
		t,
		string(body),
		"hx-confirm=\"Remove member &#34;alex@acme.com&#34;? They will lose access immediately.\"",
	)
	require.Contains(
		t,
		string(body),
		"data-destructive-confirm=\"Remove member &#34;alex@acme.com&#34;? They will lose access immediately.\"",
	)
	require.Contains(t, string(body), "id=\"member-role-alex-johnson\"")
}

func TestRouter_HandleMembersPage_RendersPendingInvitationsSeparately(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/organizations/acme-hq/members":
			writeJSON(w, `{"members":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme-hq/member-invitations":
			assert.Equal(t, "pending", r.URL.Query().Get("status"))
			writeJSON(
				w,
				`{"invitations":[`+
					memberInvitationJSON("invitee@acme.com", "pending")+`,`+
					memberInvitationJSON("accepted@acme.com", "accepted")+
					`],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case "/api/organizations/acme-hq/roles/admin":
			writeJSON(
				w,
				`{"slug":"admin","name":"Admin","is_default":true,"permissions":[{"resource":"members","action":"create"},{"resource":"members","action":"update"},{"resource":"members","action":"delete"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/members", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, "No accepted members yet.")
	require.Contains(t, html, "id=\"member-invitations-list\"")
	require.Contains(t, html, "invitee@acme.com")
	require.NotContains(t, html, "accepted@acme.com")
	require.NotContains(t, html, ">accepted<")
	require.Contains(t, html, "Resend")
	require.Contains(t, html, "Revoke")
	require.NotContains(t, html, "id=\"members-list\"")
}

func TestRouter_HandleMembersPage_PaginatesInvitationsSeparately(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/api/organizations/acme-hq/members":
			assert.Empty(t, r.URL.Query().Get("cursor"))
			writeJSON(w, `{"members":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
		case "/api/organizations/acme-hq/member-invitations":
			assert.Equal(t, "pending", r.URL.Query().Get("status"))
			assert.Equal(t, "invite-page-2", r.URL.Query().Get("cursor"))
			assert.Equal(t, "prev", r.URL.Query().Get("direction"))
			writeJSON(
				w,
				`{"invitations":[`+memberInvitationJSON("invitee@acme.com", "pending")+`],"has_more":true,"next_cursor":"invite-page-3","prev_cursor":"invite-page-1"}`,
			)
		case "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
			)
		case "/api/organizations/acme-hq/roles/admin":
			writeJSON(
				w,
				`{"slug":"admin","name":"Admin","is_default":true,"permissions":[{"resource":"members","action":"create"},{"resource":"members","action":"update"},{"resource":"members","action":"delete"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/members?invitation_cursor=invite-page-2&direction=prev", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, html, `href="/organizations/acme-hq/members?invitation_cursor=invite-page-1&amp;direction=prev"`)
	require.Contains(t, html, `href="/organizations/acme-hq/members?invitation_cursor=invite-page-3&amp;direction=next"`)
	require.Contains(t, html, "Previous invitations")
	require.Contains(t, html, "Next invitations")
}

func TestRouter_HandleMembersPage_GatesActionsFromRolePermissions(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.NotEmpty(t, r.Header.Get("Cookie"))
		if writeEmptyMemberInvitations(w, r) {
			return
		}

		switch r.URL.Path {
		case "/api/organizations/acme-hq/members":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"members":[{"user_slug":"alex-johnson","email":"alex@acme.com","role":"viewer","created_at":"2026-02-11T00:00:00Z","updated_at":"2026-02-11T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
			return
		case "/api/organizations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"viewer","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
			return
		case "/api/organizations/acme-hq/roles/viewer":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"slug":"viewer","name":"Viewer","is_default":true,"permissions":[{"resource":"members","action":"read"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			return
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/members", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, string(body), "Invite Member")
	require.NotContains(t, string(body), ">Update</button>")
	require.NotContains(
		t,
		string(body),
		"hx-confirm=\"Remove member &#34;alex@acme.com&#34;? They will lose access immediately.\"",
	)
	require.NotContains(
		t,
		string(body),
		"data-destructive-confirm=\"Remove member &#34;alex@acme.com&#34;? They will lose access immediately.\"",
	)
}

func TestRouter_HandleMembersPage_ForbiddenRendersErrorPage(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		switch r.URL.Path {
		case "/api/organizations/acme-hq/members":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"forbidden"}}`))
			return
		case "/api/organizations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			return
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme-hq/members", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, string(body), "You do not have access to this organization")
	require.NotContains(t, resp.Header.Get("Location"), "/auth/login")
}

func TestRouter_HandleCreateMemberSubmit_Success(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/organizations/acme-hq/member-invitations", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(memberInvitationJSON("alex@acme.com", "pending")))
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := strings.NewReader("email=alex%40acme.com&role=viewer")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/invite", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/members", resp.Header.Get("Location"))
}

func TestRouter_HandleCreateMemberSubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/organizations/acme-hq/member-invitations" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(memberInvitationJSON("alex@acme.com", "pending")))
			return
		}

		if writeEmptyMemberInvitations(w, r) {
			return
		}

		if r.URL.Path == "/api/organizations/acme-hq/members" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"members":[{"user_slug":"alex-johnson","email":"alex@acme.com","role":"viewer","created_at":"2026-02-11T00:00:00Z","updated_at":"2026-02-11T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
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
	form := strings.NewReader("email=alex%40acme.com&role=viewer")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/invite", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "id=\"members-content\"")
	require.Contains(t, string(body), "alex@acme.com")
	require.Contains(t, string(body), "id=\"members-invite-success-popup\"")
	require.Contains(t, string(body), "Invitation sent to alex@acme.com.")
	require.Empty(t, resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleCreateMemberSubmit_InvalidInputRendersPopup(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeEmptyMemberInvitations(w, r) {
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/members" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(`{"members":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
			)
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/admin" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"slug":"admin","name":"Admin","is_default":true,"permissions":[{"resource":"members","action":"create"},{"resource":"members","action":"update"},{"resource":"members","action":"delete"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
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
	form := strings.NewReader("email=alex%40acme.com&role=")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/invite", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "Something went wrong")
	require.Contains(t, string(body), "More")
	require.Contains(t, string(body), "Validation: Role is required.")
	require.Contains(t, string(body), "name=\"email\"")
	require.Contains(t, string(body), "value=\"alex@acme.com\"")
	require.Contains(t, string(body), "option selected value=\"viewer\"")
}

func TestRouter_HandleCreateMemberSubmit_InvalidInputHTMXRendersInviteSection(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeEmptyMemberInvitations(w, r) {
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/members" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(`{"members":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
			)
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/admin" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"slug":"admin","name":"Admin","is_default":true,"permissions":[{"resource":"members","action":"create"},{"resource":"members","action":"update"},{"resource":"members","action":"delete"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
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
	form := strings.NewReader("email=alex%40acme.com&role=")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/invite", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "id=\"members-content\"")
	require.Contains(t, string(body), "id=\"invite-member\"")
	require.Contains(t, string(body), "id=\"members-invite-error-popup\"")
	require.Contains(t, string(body), "Validation: Role is required.")
	require.NotContains(t, string(body), "<html")
}

func TestRouter_HandleCreateMemberSubmit_APIErrorHTMXShowsInviteError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/organizations/acme-hq/member-invitations" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write(
				[]byte(`{"error":{"code":"CONFLICT","message":"invitation already pending"}}`),
			)
			return
		}

		if writeEmptyMemberInvitations(w, r) {
			return
		}

		if r.URL.Path == "/api/organizations/acme-hq/members" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(`{"members":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`),
			)
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"organizations":[{"name":"Acme","slug":"acme-hq","member_role":"admin"}]}`))
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/admin" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"slug":"admin","permissions":[{"resource":"members","action":"create"}]}`))
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
	form := strings.NewReader("email=alex%40acme.com&role=admin")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/invite", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "id=\"members-content\"")
	require.Contains(t, string(body), "id=\"members-invite-error-popup\"")
	require.Contains(t, string(body), "Something went wrong while inviting a member.")
	require.Contains(t, string(body), "backend_message=invitation already pending")
	require.Contains(t, string(body), `option selected value="admin"`)
	require.NotContains(t, string(body), `option selected value="viewer"`)
	require.NotContains(t, string(body), "<html")
}

func TestRouter_HandleCreateMemberSubmit_Backend500HTMXShowsError(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/organizations/acme-hq/member-invitations" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"INTERNAL_SERVER_ERROR","message":"An unexpected error occurred"}}`,
				),
			)
			return
		}

		if writeEmptyMemberInvitations(w, r) {
			return
		}

		if r.URL.Path == "/api/organizations/acme-hq/members" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"members":[{"user_slug":"alex-johnson","email":"alex@acme.com","role":"viewer","created_at":"2026-02-11T00:00:00Z","updated_at":"2026-02-11T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
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
	form := strings.NewReader("email=alex%40acme.com&role=viewer")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/invite", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "id=\"members-invite-error-popup\"")
	require.Contains(t, string(body), "backend_message=An unexpected error occurred")
	require.NotContains(t, string(body), "id=\"members-invite-success-popup\"")
}

func TestRouter_HandleResendMemberInvitationSubmit_HTMXShowsSuccess(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/organizations/acme-hq/member-invitations/jane-example-com-abc123/resend" {
			assert.Equal(t, http.MethodPost, r.Method)
			writeJSON(w, memberInvitationJSON("jane@example.com", "pending"))
			return
		}
		if writeEmptyMemberInvitations(w, r) {
			return
		}
		if r.URL.Path == "/api/organizations/acme-hq/members" && r.Method == http.MethodGet {
			writeJSON(w, `{"members":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
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
		"/orgs/acme-hq/member-invitations/jane-example-com-abc123/resend",
		nil,
	)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "id=\"members-invite-success-popup\"")
	require.Contains(t, string(body), "Invitation resent to jane@example.com.")
}

func TestRouter_HandleRevokeMemberInvitationSubmit_HTMXShowsSuccess(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/organizations/acme-hq/member-invitations/jane-example-com-abc123/revoke" {
			assert.Equal(t, http.MethodPost, r.Method)
			writeJSON(w, memberInvitationJSON("jane@example.com", "revoked"))
			return
		}
		if writeEmptyMemberInvitations(w, r) {
			return
		}
		if r.URL.Path == "/api/organizations/acme-hq/members" && r.Method == http.MethodGet {
			writeJSON(w, `{"members":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
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
		"/orgs/acme-hq/member-invitations/jane-example-com-abc123/revoke",
		nil,
	)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "id=\"members-invite-success-popup\"")
	require.Contains(t, string(body), "Invitation for jane@example.com revoked.")
}

func TestRouter_HandleUpdateMemberRoleSubmit_ConflictRendersPopup(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeEmptyMemberInvitations(w, r) {
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/members" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(`{"members":[{"user_slug":"alex-johnson","email":"alex@example.com","role":"admin"}],"has_more":false}`),
			)
			return
		}

		if r.Method == http.MethodPut &&
			r.URL.Path == "/api/organizations/acme-hq/members/alex-johnson" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write(
				[]byte(`{"error":{"code":"CONFLICT","message":"Cannot demote last admin"}}`),
			)
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"organizations":[{"name":"Acme","slug":"acme-hq","member_role":"admin"}]}`))
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/admin" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"slug":"admin","permissions":[{"resource":"members","action":"read"},{"resource":"members","action":"update"}]}`))
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
	form := strings.NewReader("role=viewer")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/alex-johnson/role", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, string(body), "Something went wrong while updating a member.")
	require.Contains(t, string(body), "backend_message=Cannot demote last admin")
	require.Contains(t, string(body), `option selected value="viewer"`)
	require.NotContains(t, string(body), `option selected value="admin"`)
	require.Contains(t, string(body), `badge badge-outline">admin</div>`)
	require.Contains(t, string(body), "action=update member")
}

func TestRouter_HandleUpdateMemberRoleSubmit_ConflictHTMXRendersPopup(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeEmptyMemberInvitations(w, r) {
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/members" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(`{"members":[{"user_slug":"alex-johnson","email":"alex@example.com","role":"admin"}],"has_more":false}`),
			)
			return
		}

		if r.Method == http.MethodPut &&
			r.URL.Path == "/api/organizations/acme-hq/members/alex-johnson" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write(
				[]byte(`{"error":{"code":"CONFLICT","message":"Cannot demote last admin"}}`),
			)
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"organizations":[{"name":"Acme","slug":"acme-hq","member_role":"admin"}]}`))
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/admin" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"slug":"admin","permissions":[{"resource":"members","action":"read"},{"resource":"members","action":"update"}]}`))
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
	form := strings.NewReader("role=viewer")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/alex-johnson/role", form)
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
	require.Contains(t, string(body), "Something went wrong while updating a member.")
	require.Contains(t, string(body), "backend_message=Cannot demote last admin")
	require.Contains(t, string(body), `option selected value="viewer"`)
	require.NotContains(t, string(body), `option selected value="admin"`)
	require.Contains(t, string(body), `badge badge-outline">admin</div>`)
	require.NotContains(t, string(body), "<html")
	require.Equal(t, "#members-content", resp.Header.Get("HX-Retarget"))
	require.Equal(t, "outerHTML", resp.Header.Get("HX-Reswap"))
}

func TestRouter_HandleUpdateMemberRoleSubmit_Success(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/api/organizations/acme-hq/members/alex-johnson", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	form := strings.NewReader("role=admin")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/alex-johnson/role", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/members", resp.Header.Get("Location"))
}

func TestRouter_HandleUpdateMemberRoleSubmit_HTMXRendersRoleCell(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"admin", "viewer"} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()

			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPut, r.Method)
				assert.Equal(t, "/api/organizations/acme-hq/members/alex-johnson", r.URL.Path)
				var payload map[string]string
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				assert.Equal(t, role, payload["role"])
				w.WriteHeader(http.StatusNoContent)
			}))
			defer apiServer.Close()

			store := session.NewStore()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)

			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})

			authCookie := getAuthenticatedSessionCookie(t, app)
			form := strings.NewReader("role=" + role)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/alex-johnson/role", form)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)
			req.Header.Set("HX-Request", "true")

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Contains(t, string(body), "id=\"member-role-alex-johnson\"")
			require.Contains(t, string(body), "badge badge-outline\">"+role+"</div>")
			require.NotContains(t, string(body), "<html")
			require.Empty(t, resp.Header.Get("HX-Redirect"))
			require.Empty(t, resp.Header.Get("Location"))
		})
	}
}

func TestRouter_HandleRemoveMemberSubmit_Success(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/api/organizations/acme-hq/members/alex-johnson", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/alex-johnson/remove", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/organizations/acme-hq/members", resp.Header.Get("Location"))
}

func TestRouter_HandleRemoveMemberSubmit_HTMXRedirectHeader(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/organizations/acme-hq/members/alex-johnson" {
			assert.Equal(t, http.MethodDelete, r.Method)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if writeEmptyMemberInvitations(w, r) {
			return
		}

		if r.URL.Path == "/api/organizations/acme-hq/members" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"members":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
			return
		}

		if r.URL.Path == "/api/organizations" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
			return
		}

		if r.URL.Path == "/api/organizations/acme-hq/roles/admin" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"slug":"admin","name":"Admin","is_default":true,"permissions":[{"resource":"members","action":"create"},{"resource":"members","action":"update"},{"resource":"members","action":"delete"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/alex-johnson/remove", nil)
	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("HX-Request", "true")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "id=\"members-content\"")
	require.Contains(t, string(body), "No accepted members yet.")
	require.Empty(t, resp.Header.Get("HX-Redirect"))
	require.Empty(t, resp.Header.Get("Location"))
}

func TestRouter_HandleRemoveMemberSubmit_HTMXRendersFailure(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/organizations/acme-hq/members/alex-johnson" {
			assert.Equal(t, http.MethodDelete, r.Method)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"CONFLICT","message":"Cannot remove last admin"}}`))
			return
		}

		if writeEmptyMemberInvitations(w, r) {
			return
		}

		if r.URL.Path == "/api/organizations/acme-hq/members" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"members":[{"user_slug":"alex-johnson","email":"alex@acme.com","role":"admin","created_at":"2026-02-11T00:00:00Z","updated_at":"2026-02-11T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
			return
		}

		if r.URL.Path == "/api/organizations" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"organizations":[{"name":"Acme HQ","slug":"acme-hq","member_role":"admin","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
				),
			)
			return
		}

		if r.URL.Path == "/api/organizations/acme-hq/roles/admin" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(
				[]byte(
					`{"slug":"admin","name":"Admin","is_default":true,"permissions":[{"resource":"members","action":"create"},{"resource":"members","action":"update"},{"resource":"members","action":"delete"}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
				),
			)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/members/alex-johnson/remove", nil)
	req.Header.Set("Cookie", authCookie)
	req.Header.Set("HX-Request", "true")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "Something went wrong while removing a member.")
	require.Contains(t, string(body), "backend_message=Cannot remove last admin")
	require.NotContains(t, string(body), "<html")
}
