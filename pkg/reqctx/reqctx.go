package reqctx

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/gofiber/fiber/v3/middleware/session"

	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type contextKey string

const (
	SessionUserKey                  = "user"
	SessionAuthenticationVersionKey = "authentication_version"
	SessionAuthenticatedAtKey       = "authenticated_at"
	SessionGoogleSignupKey          = "google_signup"
	SessionGoogleSignupExpiresKey   = "google_signup_expires"
	activeOrgKey                    = "active_org"
	requestIDKey                    = contextKey("requestid")
	clientIPKey                     = contextKey("client_ip")
	apiKeyKey                       = "api_key_principal"
	userPrincipalKey                = "user_principal"
)

type APIKeyPrincipal struct {
	Slug             slug.Slug
	OrgSlug          slug.Slug
	Role             slug.Slug
	CanManageAPIKeys bool
}

type UserPrincipalSource string

const (
	UserPrincipalSourceSession             UserPrincipalSource = "session"
	UserPrincipalSourcePersonalAccessToken UserPrincipalSource = "personal_access_token"
)

type UserPrincipal struct {
	Slug                          slug.Slug
	Source                        UserPrincipalSource
	PersonalAccessTokenSlug       slug.Slug
	CanManagePersonalAccessTokens bool
}

// WithRequestID returns a derived context carrying the request ID when available.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil || requestID == "" {
		return ctx
	}

	return context.WithValue(ctx, requestIDKey, requestID)
}

// WithClientIP returns a derived context carrying the trusted browser client IP for an internal frontend-to-backend call.
func WithClientIP(ctx context.Context, clientIP string) context.Context {
	if ctx == nil || clientIP == "" {
		return ctx
	}

	return context.WithValue(ctx, clientIPKey, clientIP)
}

// GetClientIP retrieves the trusted browser client IP from context when available.
func GetClientIP(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	clientIP, _ := ctx.Value(clientIPKey).(string)
	return clientIP
}

// GetRequestID retrieves the request ID from context when available.
func GetRequestID(ctx context.Context) *string {
	if ctx == nil {
		return nil
	}

	requestID, _ := ctx.Value(requestIDKey).(string)
	if requestID == "" {
		return nil
	}

	return &requestID
}

// FiberRequestID reads the middleware ID or the application-owned fallback.
func FiberRequestID(c fiber.Ctx) string {
	if c == nil {
		return ""
	}

	if id := requestid.FromContext(c); id != "" {
		return id
	}
	requestID, _ := c.Locals("requestid").(string)
	return requestID
}

func SetAPIKeyPrincipal(c fiber.Ctx, principal APIKeyPrincipal) {
	c.Locals(apiKeyKey, principal)
}

func GetAPIKeyPrincipal(c fiber.Ctx) (APIKeyPrincipal, bool) {
	if c == nil {
		return APIKeyPrincipal{}, false
	}

	principal, ok := c.Locals(apiKeyKey).(APIKeyPrincipal)
	return principal, ok
}

func SetUserPrincipal(c fiber.Ctx, principal UserPrincipal) {
	c.Locals(userPrincipalKey, principal)
}

func GetUserPrincipal(c fiber.Ctx, store *session.Store) (UserPrincipal, bool) {
	if c == nil {
		return UserPrincipal{}, false
	}

	if principal, ok := c.Locals(userPrincipalKey).(UserPrincipal); ok {
		return principal, true
	}

	userSlug := getSessionUserSlug(c, store)
	if userSlug == "" {
		return UserPrincipal{}, false
	}

	return UserPrincipal{
		Slug:   userSlug,
		Source: UserPrincipalSourceSession,
	}, true
}

// GetUserSlug retrieves the authenticated user's slug from the session.
func GetUserSlug(c fiber.Ctx, store *session.Store) slug.Slug {
	principal, ok := GetUserPrincipal(c, store)
	if ok {
		return principal.Slug
	}

	return ""
}

func getSessionUserSlug(c fiber.Ctx, store *session.Store) slug.Slug {
	if store == nil {
		return ""
	}

	sess, err := store.Get(c)
	if err != nil {
		return ""
	}
	defer sess.Release()

	userSlug, _ := sess.Get(SessionUserKey).(string)
	newSlug, _ := slug.New(userSlug)
	return newSlug
}

// SetUserSlug stores the user's slug in the session.
func SetUserSlug(c fiber.Ctx, store *session.Store, userSlug slug.Slug) error {
	sess, err := store.Get(c)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to get session")
	}
	defer sess.Release()

	sess.Set(SessionUserKey, userSlug.String())

	if err := sess.Save(); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save session")
	}

	return nil
}

// ResetAndSetUserSlug rotates the session identifier and stores the authenticated user.
func ResetAndSetUserSlug(c fiber.Ctx, store *session.Store, userSlug slug.Slug) error {
	sess, err := store.Get(c)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to get session")
	}
	defer sess.Release()

	if err := sess.Reset(); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to reset session")
	}

	sess.Set(SessionUserKey, userSlug.String())

	if err := sess.Save(); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save session")
	}

	return nil
}

// ResetAndSetAuthenticatedUser captures the credential generation proven at sign-in.
func ResetAndSetAuthenticatedUser(c fiber.Ctx, store *session.Store, userSlug slug.Slug, version int64, passwordOrMFA bool) error {
	sess, err := store.Get(c)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to get session")
	}
	defer sess.Release()
	if err := sess.Reset(); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to reset session")
	}
	sess.Set(SessionUserKey, userSlug.String())
	sess.Set(SessionAuthenticationVersionKey, version)
	if passwordOrMFA {
		sess.Set(SessionAuthenticatedAtKey, time.Now().Unix())
	}
	if err := sess.Save(); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save session")
	}
	return nil
}

// SessionAuthentication reads server-owned proof; missing generations fail closed.
func SessionAuthentication(c fiber.Ctx, store *session.Store) (int64, time.Time) {
	sess, err := store.Get(c)
	if err != nil {
		return 0, time.Time{}
	}
	defer sess.Release()
	version, _ := sess.Get(SessionAuthenticationVersionKey).(int64)
	authenticatedAt, _ := sess.Get(SessionAuthenticatedAtKey).(int64)
	return version, time.Unix(authenticatedAt, 0)
}

// GetActiveOrganizationSlug retrieves the selected organization slug from the session.
func GetActiveOrganizationSlug(c fiber.Ctx, store *session.Store) slug.Slug {
	sess, err := store.Get(c)
	if err != nil {
		return ""
	}
	defer sess.Release()

	orgSlug, _ := sess.Get(activeOrgKey).(string)
	if orgSlug == "" {
		return ""
	}

	return must.Fn(slug.New(orgSlug))
}

// SetActiveOrganizationSlug stores the selected organization slug in the session.
func SetActiveOrganizationSlug(c fiber.Ctx, store *session.Store, orgSlug slug.Slug) error {
	sess, err := store.Get(c)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to get session")
	}
	defer sess.Release()

	sess.Set(activeOrgKey, orgSlug.String())

	if err := sess.Save(); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save session")
	}

	return nil
}
