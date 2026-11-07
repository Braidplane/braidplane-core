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

func TestRouter_HandleMemberInvitationLandingPage_RequiresAuth(t *testing.T) {
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/member-invitations/accept?token=raw-token", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login?return_to=%2Fmember-invitations%2Faccept%3Ftoken%3Draw-token", resp.Header.Get("Location"))
}

func TestRouter_HandleAcceptMemberInvitationSubmit_RequiresAuthWithTokenReturn(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	app := fiber.New()
	router.RegisterRoutes(app, CommunityPresentation{})

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/member-invitations/accept?token=raw-token",
		strings.NewReader("token=raw-token"),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/auth/login?return_to=%2Fmember-invitations%2Faccept%3Ftoken%3Draw-token", resp.Header.Get("Location"))
}

func TestRouter_HandleMemberInvitationLandingPage_RendersReviewActions(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		if r.URL.Path == "/api/organizations" {
			writeJSON(w, `{"organizations":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/member-invitations/accept?token=raw-token", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `action="/member-invitations/accept?token=raw-token"`)
	require.Contains(t, string(body), `action="/member-invitations/decline?token=raw-token"`)
	require.Contains(t, string(body), `data-destructive-confirm="Decline this member invitation? You will not join this organization."`)
	require.Contains(t, string(body), `name="token" value="raw-token"`)
	require.NotContains(t, string(body), "Invitation acceptance is currently unavailable.")
}

func TestRouter_HandleAcceptMemberInvitationSubmit_RendersAcceptedMembership(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/member-invitations/accept":
			assert.Equal(t, http.MethodPost, r.Method)
			var payload map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			writeJSON(
				w,
				`{"invitation":`+memberInvitationJSON("jane@example.com", "accepted")+`,"member":{"user_slug":"jane","email":"jane@example.com","role":"viewer","created_at":"2026-02-11T00:00:00Z","updated_at":"2026-02-11T00:00:00Z"}}`,
			)
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
		"/member-invitations/accept",
		strings.NewReader("token=raw-token"),
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
	require.Contains(t, string(body), "Invitation accepted.")
	require.Contains(t, string(body), "Membership is active for jane@example.com.")
	require.Contains(t, string(body), `href="/organizations/acme-hq"`)
}

func TestRouter_HandleAcceptMemberInvitationSubmit_RendersUnusableTokenState(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/member-invitations/accept":
			assert.Equal(t, http.MethodPost, r.Method)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"MEMBER_INVITATION_NOT_FOUND","message":"member invitation not found"}}`))
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
		"/member-invitations/accept",
		strings.NewReader("token=raw-token"),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, string(body), "expired, revoked, declined, already accepted")
	require.Contains(t, string(body), "backend_code=MEMBER_INVITATION_NOT_FOUND")
}

func TestRouter_HandleMemberInvitationSubmit_RendersEmailVerificationPrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		frontendURL string
		apiPath     string
		wantMessage string
	}{
		{
			name:        "Accept",
			frontendURL: "/member-invitations/accept",
			apiPath:     "/api/member-invitations/accept",
			wantMessage: "Verify your email address before accepting this invitation.",
		},
		{
			name:        "Decline",
			frontendURL: "/member-invitations/decline",
			apiPath:     "/api/member-invitations/decline",
			wantMessage: "Verify your email address before declining this invitation.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case tt.apiPath:
					assert.Equal(t, http.MethodPost, r.Method)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"code":"EMAIL_NOT_VERIFIED","message":"email not verified"}}`))
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
				tt.frontendURL,
				strings.NewReader("token=raw-token"),
			)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
			addCSRFToken(t, app, req)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			require.Empty(t, resp.Header.Get("Location"))
			require.Contains(t, string(body), tt.wantMessage)
			require.Contains(t, string(body), "backend_code=EMAIL_NOT_VERIFIED")
		})
	}
}
