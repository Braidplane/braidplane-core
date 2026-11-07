package http

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func TestSessionAuthenticationVersion(t *testing.T) {
	for _, tt := range []struct {
		name              string
		captured, current int64
		want              int
	}{
		{"current", 1, 1, 200}, {"revoked", 1, 2, 401}, {"legacy", 0, 1, 401},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
			store := session.NewStore()
			app.Get("/login", func(c fiber.Ctx) error {
				return reqctx.ResetAndSetAuthenticatedUser(c, store, slug.Slug("alice"), tt.captured, true)
			})
			app.Use(NewSessionAuthenticationMiddleware(&fakeUsersService{GetUserBySlugFn: func(context.Context, slug.Slug) (*model.User, error) {
				return &model.User{AuthenticationVersion: tt.current}, nil
			}}, store))
			app.Get("/private", func(c fiber.Ctx) error {
				if reqctx.GetUserSlug(c, store) == "" {
					return apierror.Unauthorized()
				}
				return c.SendStatus(200)
			})
			login, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/login", nil))
			require.NoError(t, err)
			defer func() { require.NoError(t, login.Body.Close()) }()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/private", nil)
			for _, cookie := range login.Cookies() {
				req.AddCookie(cookie)
			}
			res, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, res.Body.Close()) }()
			require.Equal(t, tt.want, res.StatusCode)
		})
	}
}

func TestMFAChangesRequireRecentProof(t *testing.T) {
	for _, tt := range []struct {
		name  string
		proof time.Time
		want  int
	}{
		{"fresh", time.Now(), 200}, {"expired", time.Now().Add(-6 * time.Minute), 403}, {"missing", time.Time{}, 403}, {"future", time.Now().Add(time.Minute), 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			enabled := false
			app, store := newTestApp(t, testServerConfig{usersService: &fakeUsersService{UpdateProfileFn: func(context.Context, slug.Slug, model.UpdateUserProfile) (*model.User, error) {
				require.Equal(t, 200, tt.want, "mutation must not run without fresh proof")
				return testUser, nil
			}}, authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }}})
			app.Get("/_test/proof", func(c fiber.Ctx) error {
				sess, err := store.Get(c)
				if err != nil {
					return err
				}
				defer sess.Release()
				sess.Set(reqctx.SessionUserKey, "alice")
				if !tt.proof.IsZero() {
					sess.Set(reqctx.SessionAuthenticatedAtKey, tt.proof.Unix())
				}
				return sess.Save()
			})
			login, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_test/proof", nil))
			require.NoError(t, err)
			defer func() { require.NoError(t, login.Body.Close()) }()
			req := jsonRequest(t, http.MethodPatch, "/users/profile", api.UpdateUserProfile{MfaEmailEnabled: &enabled})
			for _, cookie := range login.Cookies() {
				req.AddCookie(cookie)
			}
			res, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, res.Body.Close()) }()
			require.Equal(t, tt.want, res.StatusCode)
		})
	}
}

func TestPasswordRecoveryResponseDoesNotRevealLookupOrEnqueueResult(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			app, _ := newTestApp(t, testServerConfig{usersService: &fakeUsersService{RequestPasswordResetFn: func(context.Context, string) error {
				if failed {
					return fmt.Errorf("enqueue unavailable")
				}
				return nil
			}}})
			started := time.Now()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/password/forgot", strings.NewReader(`{"email":"alice@example.com"}`))
			req.Header.Set("Content-Type", "application/json")
			res, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, res.Body.Close()) }()
			require.Equal(t, http.StatusNoContent, res.StatusCode)
			require.GreaterOrEqual(t, time.Since(started), 500*time.Millisecond)
		})
	}
}

func TestProfileOmitsAuthenticationVersion(t *testing.T) {
	require.Nil(t, userToHTTP(&model.User{AuthenticationVersion: 7}).AuthenticationVersion)
}
