package frontend

import (
	"html"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
)

const (
	defaultAuthThrottleMax    = 5
	defaultAuthThrottleWindow = time.Minute
)

var postFormPattern = regexp.MustCompile(`(?i)<form\b[^>]*\bmethod=["']post["'][^>]*>`)

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

		if !httpsecurity.SafeMethod(c.Method()) && shouldThrottleFrontendAuth(c) {
			routeKey := c.Method() + " " + c.Path()
			clientKey := routeKey + "|ip:" + c.IP()
			if !ipThrottle.Allow(clientKey, time.Now()) {
				r.logAuthSecurityEvent(c, slog.LevelWarn, "auth.rate_limited", "rate_limited", fiber.StatusTooManyRequests, clientKey)
				return frontendRateLimited(c)
			}
			if subject := frontendAuthThrottleSubject(c); subject != "" {
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
	case "", "/privacy", "/solutions", "/solutions/data-engineers", "/solutions/engineering-leaders", "/solutions/business-teams", "/solutions/settlement-review", "/solutions/supplier-price-desk", "/pricing", "/robots.txt", "/sitemap.xml":
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
func shouldThrottleFrontendAuth(c fiber.Ctx) bool {
	switch c.Method() + " " + c.Path() {
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
func frontendAuthThrottleSubject(c fiber.Ctx) string {
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
