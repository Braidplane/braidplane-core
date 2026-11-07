package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func TestAccountDeletionBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		body      any
		principal string
		allowed   bool
		status    int
	}{
		{"Anonymous", map[string]string{"confirmation": "DELETE MY ACCOUNT"}, "", true, http.StatusUnauthorized},
		{"PAT", map[string]string{"confirmation": "DELETE MY ACCOUNT"}, "pat", true, http.StatusForbidden},
		{"APIKey", map[string]string{"confirmation": "DELETE MY ACCOUNT"}, "key", true, http.StatusForbidden},
		{"Denied", map[string]string{"confirmation": "DELETE MY ACCOUNT"}, "session", false, http.StatusForbidden},
		{"MissingConfirmation", map[string]string{}, "session", true, http.StatusBadRequest},
		{"WrongConfirmation", map[string]string{"confirmation": "delete"}, "session", true, http.StatusBadRequest},
		{"InvalidBody", "not-json", "session", true, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var app *fiber.App
			var store *session.Store
			app, store = newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.Delete, input.Action)
					require.Equal(t, model.ResourceProfile, input.Resource)
					require.Equal(t, input.UserSlug, *input.ResourceOwnerSlug)
					return tc.allowed, nil
				}},
				middleware: []fiber.Handler{func(c fiber.Ctx) error {
					if tc.principal == "pat" {
						reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{Slug: "alice", Source: reqctx.UserPrincipalSourcePersonalAccessToken})
					}
					if tc.principal == "key" {
						reqctx.SetAPIKeyPrincipal(c, reqctx.APIKeyPrincipal{Slug: "key", OrgSlug: "org", Role: "admin"})
					}
					return c.Next()
				}},
			})
			request := jsonRequest(t, http.MethodPost, "/users/profile/deletion", tc.body)
			var response *http.Response
			if tc.principal == "session" {
				response = authenticatedRequest(t, app, store, "alice", request)
			} else {
				var err error
				response, err = app.Test(request)
				require.NoError(t, err)
			}
			defer func() { require.NoError(t, response.Body.Close()) }()
			require.Equal(t, tc.status, response.StatusCode)
		})
	}
}

func TestAccountDeletionRequiresCSRF(t *testing.T) {
	t.Parallel()
	app, store := newSecuredBackendTestApp(t, securedBackendTestConfig{})
	response := authenticatedRequest(t, app, store, "alice", jsonRequest(t, http.MethodPost, "/users/profile/deletion", map[string]string{"confirmation": "DELETE MY ACCOUNT"}))
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	assertErrorResponse(t, response, apierror.CSRFInvalid())
}

func TestAccountDeletionUsesAuthThrottle(t *testing.T) {
	t.Parallel()
	app, _ := newSecuredBackendTestApp(t, securedBackendTestConfig{
		authThrottleMax: 1,
		authService:     fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }},
	})
	cookies := backendSecurityLoginCookies(t, app)
	csrf, csrfCookies := backendSecurityCSRFToken(t, app, cookies)
	for _, expected := range []int{http.StatusBadRequest, http.StatusTooManyRequests} {
		request := jsonRequest(t, http.MethodPost, "/users/profile/deletion", map[string]string{"confirmation": "wrong"})
		for _, cookie := range append(cookies, csrfCookies...) {
			request.AddCookie(cookie)
		}
		request.Header.Set("X-CSRF-Token", csrf)
		response, err := app.Test(request)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, expected, response.StatusCode)
	}
}
