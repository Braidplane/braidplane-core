package http

import (
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

// NewSessionAuthenticationMiddleware checks shared credential state on every request.
func NewSessionAuthenticationMiddleware(users userssvc.UsersService, store *session.Store) fiber.Handler {
	return func(c fiber.Ctx) error {
		principal, ok := reqctx.GetUserPrincipal(c, store)
		if !ok || principal.Source != reqctx.UserPrincipalSourceSession {
			return c.Next()
		}
		version, _ := reqctx.SessionAuthentication(c, store)
		user, err := users.GetUserBySlug(c.Context(), principal.Slug)
		if err != nil && !xerror.Is(err, userssvc.ErrUserNotFound) {
			return mapServiceError(err)
		}
		if err == nil && version > 0 && version == user.AuthenticationVersion {
			return c.Next()
		}
		sess, err := store.Get(c)
		if err != nil {
			return apierror.InternalServerError()
		}
		defer sess.Release()
		if err := sess.Destroy(); err != nil {
			return apierror.InternalServerError()
		}
		// Keep the expired subject from falling back to the cookie during this request.
		reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{})
		return c.Next()
	}
}
