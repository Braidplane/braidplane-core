package frontend

import (
	"crypto/sha256"
	"encoding/hex"
	"html"
	"log/slog"
	"mime"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	defaultAuthThrottleMax    = 5
	defaultAuthThrottleWindow = time.Minute
	browserSessionEndedHeader = "X-Braidplane-Session-Ended"
	browserSessionTTLHeader   = "X-Braidplane-Session-Expiry"
	browserSessionStatusPath  = "/auth/session-status"
)

type browserPageContextKey struct{}

var authenticatedBrowserPage browserPageContextKey

func (r *Router) setBrowserSessionEnded(c fiber.Ctx) {
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set(browserSessionEndedHeader, "1")
	r.expireDashboardScopeCookie(c, dashboardScopeProjectCookieName)
	r.expireDashboardScopeCookie(c, dashboardScopeEnvironmentCookieName)
	r.setGitHubInstallCookie(c, githubInstallProjectCookie, "", -1)
	r.setGitHubInstallCookie(c, githubInstallOrganizationCookie, "", -1)
}

func (r *Router) rejectBrowserSession(c fiber.Ctx) error {
	r.setBrowserSessionEnded(c)
	return redirectToLogin(c, authenticatedReturnTo(c))
}

func (r *Router) browserSessionObservation(c fiber.Ctx) (string, error) {
	sess, err := r.sessionStore.Get(c)
	if err != nil {
		return "", xerror.Wrapf("read browser session: %w", err)
	}
	defer sess.Release()
	raw, _ := sess.Get(reqctx.SessionUserKey).(string)
	user, err := slug.New(raw)
	if err != nil || user == "" {
		return "", nil
	}
	expiry, err := r.browserSessionExpiry(c.Context(), sess.ID())
	remaining := time.Until(time.Unix(expiry, 0)).Milliseconds()
	if err != nil || remaining <= 0 {
		return "", err
	}
	// The browser needs to distinguish consecutive logins without receiving the session ID.
	generation := sha256.Sum256([]byte("braidplane browser session " + sess.ID()))
	return hex.EncodeToString(generation[:16]) + ":" + strconv.FormatInt(remaining, 10), nil
}

func (r *Router) handleBrowserSessionStatus(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	if r.browserSessionExpiry == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	observation, err := r.browserSessionObservation(c)
	if err != nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	status := fiber.StatusUnauthorized
	if observation != "" {
		status = r.backendSessionStatus(c, reqctx.GetUserSlug(c, r.sessionStore))
	}
	if status == fiber.StatusUnauthorized {
		c.Set(browserSessionEndedHeader, "1")
	}
	if status != fiber.StatusOK {
		return c.SendStatus(status)
	}
	c.Set(browserSessionTTLHeader, observation)
	return c.SendStatus(fiber.StatusNoContent)
}

var postFormPattern = regexp.MustCompile(`(?i)<form\b[^>]*\bmethod=["']post["'][^>]*>`)

// requireTopLevelHTML adds the Community browser policy to rendered HTML.
func requireTopLevelHTML(c fiber.Ctx) error {
	if err := c.Next(); err != nil {
		return err
	}
	mediaType, _, _ := mime.ParseMediaType(c.GetRespHeader(fiber.HeaderContentType))
	if mediaType == "text/html" {
		c.Append(fiber.HeaderContentSecurityPolicy, "frame-ancestors 'none'")
	}
	return nil
}

// securityMiddleware owns frontend browser protections: it creates the session
// CSRF token, validates unsafe form requests, throttles auth attempts, and
// injects the token into rendered HTML.
func (r *Router) securityMiddleware() fiber.Handler {
	ipThrottle := httpsecurity.NewThrottle(defaultAuthThrottleMax, defaultAuthThrottleWindow)
	emailThrottle := httpsecurity.NewThrottle(defaultAuthThrottleMax, defaultAuthThrottleWindow)

	return func(c fiber.Ctx) error {
		// Preserve the already trusted browser IP for the frontend's internal API request.
		c.SetContext(reqctx.WithClientIP(c.Context(), c.IP()))
		if !frontendRouteNeedsSecuritySession(c) {
			return c.Next()
		}

		token, err := httpsecurity.EnsureCSRFToken(c, r.sessionStore)
		if err != nil {
			return err
		}
		c.SetContext(httpsecurity.WithCSRFToken(c.Context(), token))

		if shouldValidateFrontendCSRF(c, r) {
			ok, err := httpsecurity.ValidateCSRFToken(c, r.sessionStore)
			if err != nil {
				return err
			}
			if !ok {
				status := csrfRejectedStatus(c)
				r.logAuthSecurityEvent(c, slog.LevelWarn, "auth.csrf.rejected", "rejected", status, "")
				return csrfRejectedWithStatus(c, status)
			}
		}

		authPath := strings.ToLower(strings.TrimRight(c.Path(), "/"))
		if shouldThrottleFrontendAuth(c.Method(), authPath) {
			routeKey := c.Method() + " " + authPath
			clientKey := routeKey + "|ip:" + c.IP()
			if !ipThrottle.Allow(clientKey, time.Now()) {
				r.logAuthSecurityEvent(c, slog.LevelWarn, "auth.rate_limited", "rate_limited", fiber.StatusTooManyRequests, clientKey)
				return frontendRateLimited(c)
			}
			if subject := frontendAuthThrottleSubject(c, authPath); subject != "" {
				subjectKey := routeKey + "|subject:" + subject
				if !emailThrottle.Allow(subjectKey, time.Now()) {
					r.logAuthSecurityEvent(c, slog.LevelWarn, "auth.rate_limited", "rate_limited", fiber.StatusTooManyRequests, subjectKey)
					return frontendRateLimited(c)
				}
			}
		}

		if err := c.Next(); err != nil {
			return err
		}

		if shouldInjectCSRF(c) {
			body := string(c.Response().Body())
			if c.GetRespHeader(browserSessionEndedHeader) == "1" {
				body = strings.Replace(body, "</head>", `<meta name="braidplane-session-ended"></head>`, 1)
			} else if state, _ := c.Locals(authenticatedBrowserPage).(bool); state && strings.Contains(body, "</head>") {
				metadata := `<meta name="braidplane-authenticated-page">`
				if r.browserSessionExpiry != nil {
					observation, err := r.browserSessionObservation(c)
					switch {
					case err != nil:
						// A successful mutation may have returned a one-time result.
						if httpsecurity.SafeMethod(c.Method()) {
							c.Response().ResetBody()
							return c.SendStatus(fiber.StatusServiceUnavailable)
						}
					case observation == "":
						c.Response().ResetBody()
						return r.rejectBrowserSession(c)
					default:
						metadata += `<meta name="braidplane-session-expiry" content="` + observation + `">`
					}
				}
				body = strings.Replace(body, "</head>", metadata+"</head>", 1)
			}
			c.Response().SetBodyString(injectCSRFTokenIntoHTML(body, token))
		}
		return nil
	}
}

// frontendRouteNeedsSecuritySession excludes public, read-only pages that have
// no state-changing form and therefore need neither a CSRF token nor a session.
func frontendRouteNeedsSecuritySession(c fiber.Ctx) bool {
	if isRetiredPublicPath(c.Path()) {
		return false
	}
	if !httpsecurity.SafeMethod(c.Method()) {
		return true
	}

	path := strings.TrimRight(c.Path(), "/")
	switch path {
	case "", browserSessionStatusPath, "/privacy", "/solutions", "/solutions/data-engineers", "/solutions/engineering-leaders", "/solutions/business-teams", "/solutions/settlement-review", "/solutions/supplier-price-desk", "/pricing", "/robots.txt", "/sitemap.xml":
		return false
	default:
		return true
	}
}

// shouldValidateFrontendCSRF protects every unsafe browser route. The logout
// unauthenticated exception preserves the existing redirect-to-login behavior.
func shouldValidateFrontendCSRF(c fiber.Ctx, r *Router) bool {
	if httpsecurity.SafeMethod(c.Method()) || isRetiredPublicPath(c.Path()) {
		return false
	}
	if c.Path() == "/auth/logout" && reqctx.GetUserSlug(c, r.sessionStore) == "" {
		return false
	}
	return true
}

func isRetiredPublicPath(path string) bool {
	return strings.TrimRight(path, "/") == "/roadmap" || path == "/wishlist" || strings.HasPrefix(path, "/wishlist/") ||
		path == "/api/wishlist-registrations" || strings.HasPrefix(path, "/api/wishlist-registrations/")
}

// shouldThrottleFrontendAuth mirrors the backend auth throttle on browser form
// endpoints so abuse is slowed before the frontend calls the API.
func shouldThrottleFrontendAuth(method, path string) bool {
	switch method + " " + path {
	case "POST /auth/login",
		"POST /auth/mfa",
		"POST /account/security/password",
		"POST /account/deletion",
		"POST /auth/signup",
		"POST /auth/forgot-password",
		"POST /auth/reset-password",
		"POST /auth/resend-verification":
		return true
	default:
		return false
	}
}

// frontendAuthThrottleSubject mirrors the backend second throttle key while
// hashing MFA challenge tokens before they are retained in memory.
func frontendAuthThrottleSubject(c fiber.Ctx, path string) string {
	if path == "/auth/login" {
		return ""
	}
	if email := httpsecurity.NormalizeEmail(c.FormValue("email")); email != "" {
		return "email:" + email
	}
	challengeToken := strings.TrimSpace(c.FormValue("challenge_token"))
	if challengeToken == "" {
		return ""
	}
	return "mfa:" + httpsecurity.HashKey(challengeToken)
}

func csrfRejectedStatus(c fiber.Ctx) int {
	status := fiber.StatusForbidden
	if isHTMXRequest(c) {
		status = fiber.StatusOK
	}
	return status
}

func csrfRejectedWithStatus(c fiber.Ctx, status int) error {
	return c.Status(status).Type("html").SendString("Security check failed. Refresh the page and try again.")
}

func frontendRateLimited(c fiber.Ctx) error {
	return c.Status(fiber.StatusTooManyRequests).Type("html").SendString("Too many attempts. Try again later.")
}

func shouldInjectCSRF(c fiber.Ctx) bool {
	status := c.Response().StatusCode()
	if status >= fiber.StatusMultipleChoices && status < fiber.StatusBadRequest {
		return false
	}
	contentType := strings.ToLower(string(c.Response().Header.ContentType()))
	return strings.Contains(contentType, "text/html")
}

// injectCSRFTokenIntoHTML keeps CSRF coverage centralized for Templ pages and
// prevents each form component from manually threading the same hidden field.
func injectCSRFTokenIntoHTML(body, token string) string {
	if body == "" || token == "" {
		return body
	}

	escapedToken := html.EscapeString(token)
	withMeta := body
	if strings.Contains(strings.ToLower(withMeta), "</head>") &&
		!strings.Contains(withMeta, `name="csrf-token"`) {
		meta := `<meta name="csrf-token" content="` + escapedToken + `">`
		withMeta = strings.Replace(withMeta, "</head>", meta+"</head>", 1)
	}

	return postFormPattern.ReplaceAllStringFunc(withMeta, func(formTag string) string {
		if strings.Contains(formTag, `name="_csrf"`) {
			return formTag
		}
		return formTag + `<input type="hidden" name="_csrf" value="` + escapedToken + `">`
	})
}

// logAuthSecurityEvent mirrors backend audit logging for frontend-only
// rejections. Keep the field list narrow and free of tokens, cookies, and email.
func (r *Router) logAuthSecurityEvent(
	c fiber.Ctx,
	level slog.Level,
	event string,
	outcome string,
	status int,
	key string,
) {
	attrs := []slog.Attr{
		slog.String("braidplane.event", event),
		slog.String("http.method", c.Method()),
		slog.String("http.route", c.Path()),
		slog.String("client.address", c.IP()),
		slog.String("outcome", outcome),
	}
	if status > 0 {
		attrs = append(attrs, slog.Int("http.status_code", status))
	}
	if requestID := reqctx.FiberRequestID(c); requestID != "" {
		attrs = append(attrs, slog.String("request_id", requestID))
	}
	if userSlug := reqctx.GetUserSlug(c, r.sessionStore); userSlug != "" {
		attrs = append(attrs, slog.String("user.slug", userSlug.String()))
	}
	if key != "" {
		attrs = append(attrs, slog.String("throttle.key_hash", httpsecurity.HashKey(key)))
	}
	r.logger.LogAttrs(c.Context(), level, "auth security event", attrs...)
}
