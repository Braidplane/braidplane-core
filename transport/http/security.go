package http

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
)

type backendSecurityConfig struct {
	sessionStore       *session.Store
	logger             *slog.Logger
	csrfEnabled        bool
	authThrottleMax    int
	authThrottleWindow time.Duration
	expensiveClientMax int
	expensiveTenantMax int
	credentialMax      int
	expensiveWindow    time.Duration
	abuseLimiter       httpsecurity.AbuseLimiter
}

func NewBackendSecurityMiddlewareForApp(
	sessionStore *session.Store,
	logger *slog.Logger,
	abuseLimiter httpsecurity.AbuseLimiter,
	authAttempts int,
	authWindow time.Duration,
	expensiveClientAttempts int,
	expensiveTenantAttempts int,
	credentialAttempts int,
	expensiveWindow time.Duration,
) fiber.Handler {
	if abuseLimiter == nil {
		panic("backend abuse limiter is required")
	}
	return newBackendSecurityMiddleware(backendSecurityConfig{
		sessionStore:       sessionStore,
		logger:             logger,
		csrfEnabled:        true,
		authThrottleMax:    authAttempts,
		authThrottleWindow: authWindow,
		expensiveClientMax: expensiveClientAttempts,
		expensiveTenantMax: expensiveTenantAttempts,
		credentialMax:      credentialAttempts,
		expensiveWindow:    expensiveWindow,
		abuseLimiter:       abuseLimiter,
	})
}

const authorizedAbuseControlLocalKey = "braidplane.authorized_abuse_control"

// authorizedAbuseControl is installed by the production middleware and used by
// handlers after authorization succeeds. Keeping the organization key out of
// pre-auth middleware prevents an arbitrary path segment from spending another
// organization's quota.
type authorizedAbuseControl struct {
	limiter            httpsecurity.AbuseLimiter
	logger             *slog.Logger
	expensiveTenantMax int
	credentialMax      int
	expensiveWindow    time.Duration
}

// newBackendSecurityMiddleware applies shared abuse controls before generated
// API handlers. Fiber's trusted-proxy configuration owns client IP resolution;
// this middleware never trusts forwarding headers directly.
func newBackendSecurityMiddleware(cfg backendSecurityConfig) fiber.Handler {
	limiter := cfg.abuseLimiter
	if limiter == nil {
		limiter = httpsecurity.NewMemoryAbuseLimiter()
	}
	logger := cfg.logger
	if logger == nil {
		logger = slog.Default()
	}

	return func(c fiber.Ctx) error {
		if httpsecurity.HasSessionLookup(c, cfg.sessionStore) {
			token, err := backendCSRFTokenForRequest(c, cfg.sessionStore)
			if err != nil {
				return err
			}
			if token != "" {
				c.SetContext(httpsecurity.WithCSRFToken(c.Context(), token))
			}
		}

		c.Locals(authorizedAbuseControlLocalKey, &authorizedAbuseControl{
			limiter:            limiter,
			logger:             logger,
			expensiveTenantMax: cfg.expensiveTenantMax,
			credentialMax:      cfg.credentialMax,
			expensiveWindow:    cfg.expensiveWindow,
		})

		for _, budget := range backendAbuseBudgets(c, cfg) {
			decision, err := limiter.Attempt(c.Context(), budget)
			if err != nil {
				logger.ErrorContext(c.Context(), "shared abuse control failed",
					slog.String("throttle.scope", budget.Scope),
					slog.String("http.route", c.Path()),
					slog.Any("error", err),
				)
				return apierror.InternalServerError()
			}
			if !decision.Allowed {
				if retryAfter := time.Until(decision.ResetAt); retryAfter > 0 {
					c.Set(fiber.HeaderRetryAfter, strconv.FormatInt(max(1, int64(retryAfter.Seconds()+1)), 10))
				}
				event := "abuse.rate_limited"
				if strings.HasPrefix(budget.Scope, "auth.") {
					event = "auth.rate_limited"
				}
				logAuthSecurityEvent(c, cfg.sessionStore, logger, slog.LevelWarn, event, "rate_limited", fiber.StatusTooManyRequests, budget.Scope+"|"+budget.Key)
				return apierror.RateLimited()
			}
		}

		if cfg.csrfEnabled && shouldValidateBackendCSRF(c, cfg.sessionStore) {
			ok, err := httpsecurity.ValidateCSRFToken(c, cfg.sessionStore)
			if err != nil {
				return err
			}
			if !ok {
				logAuthSecurityEvent(c, cfg.sessionStore, logger, slog.LevelWarn, "auth.csrf.rejected", "rejected", fiber.StatusForbidden, "")
				return apierror.CSRFInvalid()
			}
		}

		return c.Next()
	}
}

// shouldValidateBackendCSRF scopes API CSRF checks to cookie-authenticated
// browser requests. Token-based or unauthenticated API clients are left alone
// for this slice because they do not rely on ambient browser cookies.
func shouldValidateBackendCSRF(c fiber.Ctx, store *session.Store) bool {
	if httpsecurity.SafeMethod(c.Method()) {
		return false
	}
	if !httpsecurity.HasSessionLookup(c, store) {
		return false
	}
	return reqctx.GetUserSlug(c, store) != ""
}

func backendCSRFTokenForRequest(c fiber.Ctx, store *session.Store) (string, error) {
	if httpsecurity.SafeMethod(c.Method()) {
		if reqctx.GetUserSlug(c, store) == "" {
			return "", nil
		}
		token, err := httpsecurity.EnsureCSRFToken(c, store)
		if err != nil {
			return "", err
		}
		c.Set(httpsecurity.CSRFHeader, token)
		return token, nil
	}
	return httpsecurity.ExistingCSRFToken(c, store)
}

// backendAbuseBudgets returns pre-auth budgets. Organization budgets are
// deliberately charged by handlers only after canOrg has succeeded.
func backendAbuseBudgets(c fiber.Ctx, cfg backendSecurityConfig) []httpsecurity.AbuseLimit {
	routeKey := c.Method() + " " + backendSecurityPath(c.Path())
	if shouldThrottleBackendAuth(c) {
		limit := max(1, cfg.authThrottleMax)
		window := max(time.Nanosecond, cfg.authThrottleWindow)
		budgets := []httpsecurity.AbuseLimit{{
			Scope: "auth.client",
			Key:   routeKey + "|ip:" + c.IP(), Limit: limit, Window: window,
		}}
		if subject := backendAuthThrottleSubject(c); subject != "" {
			budgets = append(budgets, httpsecurity.AbuseLimit{
				Scope: "auth.subject",
				Key:   routeKey + "|subject:" + subject, Limit: limit, Window: window,
			})
		}
		return budgets
	}

	scope, credentialOperation := backendExpensiveOperation(c.Method(), c.Path())
	if scope == "" {
		return nil
	}
	clientLimit := max(1, cfg.expensiveClientMax)
	if credentialOperation {
		clientLimit = max(1, cfg.credentialMax)
	}
	window := max(time.Nanosecond, cfg.expensiveWindow)
	budgets := []httpsecurity.AbuseLimit{{
		Scope: scope + ".client",
		Key:   "ip:" + c.IP(), Limit: clientLimit, Window: window,
	}}
	return budgets
}

func chargeAuthorizedOrganizationAbuseControl(
	c fiber.Ctx,
	orgSlug string,
	scope string,
	credentialOperation bool,
) error {
	return chargeAuthorizedAbuseControl(c, "organization:"+orgSlug, scope, credentialOperation)
}

func chargeAuthorizedUserAbuseControl(c fiber.Ctx, userSlug string) error {
	return chargeAuthorizedAbuseControl(c, "user:"+userSlug, "credential.mutation", true)
}

func chargeAuthorizedAbuseControl(
	c fiber.Ctx,
	subject string,
	scope string,
	credentialOperation bool,
) error {
	control, ok := c.Locals(authorizedAbuseControlLocalKey).(*authorizedAbuseControl)
	if !ok || control == nil {
		// Handler tests intentionally exercise transport behavior without the
		// production middleware. The production API always installs it.
		return nil
	}
	limit := max(1, control.expensiveTenantMax)
	if credentialOperation {
		limit = max(1, control.credentialMax)
	}
	decision, err := control.limiter.Attempt(c.Context(), httpsecurity.AbuseLimit{
		Scope:  scope + ".tenant",
		Key:    subject,
		Limit:  limit,
		Window: max(time.Nanosecond, control.expensiveWindow),
	})
	if err != nil {
		control.logger.ErrorContext(c.Context(), "shared abuse control failed",
			slog.String("throttle.scope", scope+".tenant"),
			slog.String("http.route", c.Path()),
			slog.Any("error", err),
		)
		return apierror.InternalServerError()
	}
	if decision.Allowed {
		return nil
	}
	if retryAfter := time.Until(decision.ResetAt); retryAfter > 0 {
		c.Set(fiber.HeaderRetryAfter, strconv.FormatInt(max(1, int64(retryAfter.Seconds()+1)), 10))
	}
	logAuthSecurityEvent(c, nil, control.logger, slog.LevelWarn, "abuse.rate_limited", "rate_limited", fiber.StatusTooManyRequests, scope+".tenant|"+subject)
	return apierror.RateLimited()
}

type backendExpensiveRoute struct {
	method              string
	pattern             []string
	scope               string
	credentialOperation bool
}

var backendExpensiveRoutes = []backendExpensiveRoute{
	{fiber.MethodPost, []string{"webhooks", "pipelines", "*"}, "pipeline.webhook", false},
	{fiber.MethodPost, []string{"users", "profile", "personal-access-tokens"}, "credential.mutation", true},
	{fiber.MethodPost, []string{"users", "profile", "personal-access-tokens", "*", "rotate"}, "credential.mutation", true},
	{fiber.MethodGet, []string{"connectors", "provider", "*", "callback"}, "credential.mutation", true},
	{fiber.MethodPost, []string{"organizations", "*", "api-keys"}, "credential.mutation", true},
	{fiber.MethodPost, []string{"organizations", "*", "api-keys", "*", "rotate"}, "credential.mutation", true},
	{fiber.MethodPost, []string{"organizations", "*", "data-sources"}, "connector.connection_test", false},
	{fiber.MethodPost, []string{"organizations", "*", "data-sources", "test-connection"}, "connector.connection_test", false},
	{fiber.MethodPost, []string{"organizations", "*", "data-sources", "*", "test-connection"}, "connector.connection_test", false},
	{fiber.MethodGet, []string{"organizations", "*", "data-sources", "*", "branches"}, "connector.remote_metadata", false},
	{fiber.MethodPatch, []string{"organizations", "*", "data-sources", "*", "credentials"}, "credential.mutation", true},
	{fiber.MethodPatch, []string{"organizations", "*", "data-sources", "*"}, "connector.connection_test", false},
	{fiber.MethodPost, []string{"organizations", "*", "data-sources", "*", "schema"}, "connector.schema_import", false},
	{fiber.MethodPost, []string{"organizations", "*", "data-sources", "*", "provider-grant", "refresh"}, "credential.mutation", true},
	{fiber.MethodPost, []string{"organizations", "*", "data-sources", "*", "provider-grant", "revoke"}, "credential.mutation", true},
	{fiber.MethodPost, []string{"organizations", "*", "data-sources", "*", "queries", "*", "execute"}, "query.execute", false},
	{fiber.MethodPost, []string{"organizations", "*", "projects", "*", "environments", "*", "pipelines", "*:execute"}, "pipeline.run", false},
	{fiber.MethodPut, []string{"organizations", "*", "projects", "*", "pipelines", "*"}, "pipeline.git", false},
	{fiber.MethodPost, []string{"organizations", "*", "projects", "*", "pipelines:reconcile"}, "pipeline.git", false},
	{fiber.MethodPost, []string{"organizations", "*", "projects", "*", "pipelines", "*", "draft:publish"}, "pipeline.git", false},
	{fiber.MethodDelete, []string{"organizations", "*", "projects", "*"}, "pipeline.git", false},
	{fiber.MethodPut, []string{"organizations", "*", "projects", "*", "pipeline-backend"}, "pipeline.reconcile", false},
	{fiber.MethodPost, []string{"organizations", "*", "alert-destinations", "*", "test"}, "notification.delivery_test", false},
}

// backendExpensiveOperation classifies only explicit, side-effecting routes.
// Pattern matching is segment based so a user-controlled substring cannot add
// or evade a class. Cached reads and local OAuth URL/state creation are absent.
func backendExpensiveOperation(method, path string) (scope string, credentialOperation bool) {
	parts := strings.Split(strings.Trim(backendSecurityPath(path), "/"), "/")
	for _, route := range backendExpensiveRoutes {
		if route.method == method && matchesBackendExpensiveRoute(parts, route.pattern) {
			return route.scope, route.credentialOperation
		}
	}
	return "", false
}

func matchesBackendExpensiveRoute(parts, pattern []string) bool {
	if len(parts) != len(pattern) {
		return false
	}
	for i, expected := range pattern {
		switch {
		case expected == "*":
			continue
		case strings.HasPrefix(expected, "*"):
			if !strings.HasSuffix(parts[i], strings.TrimPrefix(expected, "*")) || len(parts[i]) == len(expected)-1 {
				return false
			}
		case parts[i] != expected:
			return false
		}
	}
	return true
}

// shouldThrottleBackendAuth lists auth-sensitive API endpoints where repeated
// attempts are useful signals even when individual requests are otherwise valid.
func shouldThrottleBackendAuth(c fiber.Ctx) bool {
	switch c.Method() + " " + backendSecurityPath(c.Path()) {
	case "POST /sessions",
		"POST /sessions/mfa/verify",
		"PUT /users/profile/password",
		"POST /users/profile/deletion",
		"POST /users",
		"POST /auth/password/forgot",
		"POST /auth/password/reset",
		"POST /auth/email/resend-verification",
		"POST /auth/email/verify",
		"GET /auth/google",
		"POST /auth/google",
		"GET /auth/google/callback":
		return true
	default:
		return false
	}
}

// Match Fiber's default case-insensitive, non-strict routing and API mount.
func backendSecurityPath(path string) string {
	path = strings.TrimRight(strings.ToLower(path), "/")
	if strings.HasPrefix(path, "/api/") {
		path = strings.TrimPrefix(path, "/api")
	}
	return path
}

// backendAuthThrottleSubject extracts an optional second throttle key for auth
// routes. Email routes use normalized email; MFA verification uses a token hash
// so raw challenge tokens are not stored in throttle keys or logs.
func backendAuthThrottleSubject(c fiber.Ctx) string {
	var payload struct {
		Email          string `json:"email"`
		ChallengeToken string `json:"challenge_token"`
	}
	if strings.Contains(strings.ToLower(c.Get(fiber.HeaderContentType)), fiber.MIMEApplicationJSON) {
		_ = json.Unmarshal(c.Body(), &payload)
		if email := httpsecurity.NormalizeEmail(payload.Email); email != "" {
			return "email:" + email
		}
		// MFA-only JSON payloads fall back to a hashed challenge-token subject.
		return backendMFAChallengeThrottleSubject(payload.ChallengeToken)
	}
	// Form submissions mirror the JSON precedence for browser-submitted routes.
	if email := httpsecurity.NormalizeEmail(c.FormValue("email")); email != "" {
		return "email:" + email
	}
	// MFA-only form submissions fall back after both email subject fields miss.
	return backendMFAChallengeThrottleSubject(c.FormValue("challenge_token"))
}

// backendMFAChallengeThrottleSubject derives a non-secret throttle subject from
// the opaque MFA challenge token without logging or retaining the raw token.
func backendMFAChallengeThrottleSubject(challengeToken string) string {
	challengeToken = strings.TrimSpace(challengeToken)
	if challengeToken == "" {
		return ""
	}
	return "mfa:" + httpsecurity.HashKey(challengeToken)
}

// logAuthSecurityEvent emits operational audit records for auth controls using
// only allowlisted fields. Do not add tokens, cookies, session IDs, passwords,
// OAuth codes, or raw email addresses here.
func logAuthSecurityEvent(
	c fiber.Ctx,
	store *session.Store,
	logger *slog.Logger,
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
	if store != nil {
		if httpsecurity.HasSessionLookup(c, store) {
			if userSlug := reqctx.GetUserSlug(c, store); userSlug != "" {
				attrs = append(attrs, slog.String("user.slug", userSlug.String()))
			}
		}
	}
	if key != "" {
		attrs = append(attrs, slog.String("throttle.key_hash", httpsecurity.HashKey(key)))
	}
	logger.LogAttrs(c.Context(), level, "auth security event", attrs...)
}
