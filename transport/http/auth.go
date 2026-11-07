package http

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"net"
	"net/url"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

// GetAuthenticationMethods reports deployment configuration before authentication.
func (s *server) GetAuthenticationMethods(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(api.GetAuthenticationMethods200JSONResponse{GoogleSignInEnabled: s.oidcService.Configured()})
}

func (s *server) RegisterGoogleAccount(c fiber.Ctx) error {
	if !s.externalOnboardingEnabled || !s.selfServiceRegistrationEnabled {
		return apierror.New(fiber.StatusForbidden, "SELF_SERVICE_REGISTRATION_DISABLED", "Account creation is currently unavailable.")
	}
	sess, err := s.sessionStore.Get(c)
	if err != nil {
		return apierror.InternalServerError()
	}
	defer sess.Release()
	if pending, ok := sess.Get(reqctx.SessionGoogleSignupKey).(string); ok {
		validCSRF, err := httpsecurity.ValidateCSRFToken(c, s.sessionStore)
		if err != nil {
			return apierror.InternalServerError()
		}
		if !validCSRF {
			return apierror.CSRFInvalid()
		}
		expires, _ := sess.Get(reqctx.SessionGoogleSignupExpiresKey).(int64)
		returnTo, _ := sess.Get("oauth_return_to").(string)
		sess.Delete(reqctx.SessionGoogleSignupKey)
		sess.Delete(reqctx.SessionGoogleSignupExpiresKey)
		sess.Delete("oauth_return_to")
		if err := sess.Save(); err != nil {
			return apierror.InternalServerError()
		}
		var identity model.OIDCUserInfo
		if time.Now().Unix() >= expires || json.Unmarshal([]byte(pending), &identity) != nil {
			return c.Redirect().Status(fiber.StatusFound).To(s.frontendURL + "/auth/error?error=invalid_state")
		}
		return s.completeGoogleSignup(c, &identity, returnTo)
	}
	return s.initiateGoogleAuth(c)
}

// InitiateGoogleAuth initiates the Google OAuth flow
// (GET /auth/google)
func (s *server) InitiateGoogleAuth(c fiber.Ctx) error {
	return s.initiateGoogleAuth(c)
}

func (s *server) initiateGoogleAuth(c fiber.Ctx) error {
	// Generate a random state for CSRF protection
	state, err := oauthflow.RandomState()
	if err != nil {
		slog.Error("Failed to generate OAuth state", slog.Any("error", err))
		return apierror.InternalServerError()
	}
	authURL, err := s.oidcService.GetAuthURL(c.Context(), state, s.googleRedirectURI)
	if err != nil {
		slog.WarnContext(c.Context(), "Google sign-in is unavailable", slog.String("error.category", googleAuthFailureCategory(err)))
		return apierror.New(fiber.StatusServiceUnavailable, "GOOGLE_AUTH_UNAVAILABLE", "Google sign-in is currently unavailable.")
	}

	// Store state in session for validation in callback
	sess, err := s.sessionStore.Get(c)
	if err != nil {
		slog.Error("Failed to get session", slog.Any("error", err))
		return apierror.InternalServerError()
	}
	defer sess.Release()

	sess.Set("oauth_state", state)
	sess.Set("oauth_expires_at", time.Now().Add(10*time.Minute).Unix())
	sess.Delete(reqctx.SessionGoogleSignupKey)
	sess.Delete(reqctx.SessionGoogleSignupExpiresKey)
	sess.Set("oauth_return_to", c.Query("return_to", "/dashboard"))

	if err := sess.Save(); err != nil {
		slog.Error("Failed to save session", slog.Any("error", err))
		return apierror.InternalServerError()
	}

	// Redirect user to Google's consent screen
	return c.Redirect().Status(fiber.StatusFound).To(authURL)
}

// Only bounded classifications cross into logs; discovery errors can contain provider text.
func googleAuthFailureCategory(err error) string {
	if xerror.Is(err, context.Canceled) {
		return "request_canceled"
	}
	if xerror.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var dns *net.DNSError
	if xerror.As(err, &dns) {
		return "dns"
	}
	var certificate *tls.CertificateVerificationError
	if xerror.As(err, &certificate) {
		return "tls"
	}
	var network net.Error
	if xerror.As(err, &network) {
		if network.Timeout() {
			return "timeout"
		}
		return "network"
	}
	if xerror.Is(err, xerror.ValidationFailed()) {
		return "configuration"
	}
	return "provider_discovery"
}

// HandleGoogleCallback handles the OAuth callback from Google
// (GET /auth/google/callback)
func (s *server) HandleGoogleCallback(c fiber.Ctx, params api.HandleGoogleCallbackParams) error {
	// Get session to validate state
	sess, err := s.sessionStore.Get(c)
	if err != nil {
		slog.Error("Failed to get session", slog.Any("error", err))
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.oauth.callback.failed", "error", fiber.StatusFound, "")
		errorURL := s.frontendURL + "/auth/error?error=session_error"
		return c.Redirect().Status(fiber.StatusFound).To(errorURL)
	}
	defer sess.Release()

	var storedStateStr string
	expiresAt, _ := sess.Get("oauth_expires_at").(int64)
	if storedState, ok := sess.Get("oauth_state").(string); ok {
		storedStateStr = storedState
	}
	// Require both incoming and stored state to be present and match
	if params.State == nil || *params.State == "" || storedStateStr == "" ||
		*params.State != storedStateStr || time.Now().Unix() >= expiresAt {
		slog.Error("State missing or mismatch - possible CSRF attack")
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.oauth.callback.failed", "error", fiber.StatusFound, "")
		errorURL := s.frontendURL + "/auth/error?error=invalid_state"
		return c.Redirect().Status(fiber.StatusFound).To(errorURL)
	}
	// Clear stored state after validation to prevent replay attacks
	sess.Delete("oauth_state")
	sess.Delete("oauth_expires_at")
	returnTo, _ := sess.Get("oauth_return_to").(string)
	if returnTo == "" {
		returnTo = "/dashboard"
	}
	sess.Delete("oauth_return_to")
	if err := sess.Save(); err != nil {
		return apierror.InternalServerError()
	}

	// Check if Google returned an error
	if params.Error != nil && *params.Error != "" {
		slog.Warn("Google OAuth error", slog.String("error", *params.Error))
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.oauth.callback.failed", "error", fiber.StatusFound, "")
		// Redirect to frontend error page
		errorURL := s.frontendURL + "/auth/error?error=" + url.QueryEscape(*params.Error)
		return c.Redirect().Status(fiber.StatusFound).To(errorURL)
	}

	// Validate required code parameter
	if params.Code == nil || *params.Code == "" {
		slog.Error("Missing authorization code")
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.oauth.callback.failed", "error", fiber.StatusFound, "")
		errorURL := s.frontendURL + "/auth/error?error=missing_code"
		return c.Redirect().Status(fiber.StatusFound).To(errorURL)
	}

	// Exchange authorization code for user info (use backend redirect URI)
	oidcUser, err := s.oidcService.ExchangeCodeForToken(
		c.Context(),
		*params.Code,
		s.googleRedirectURI,
	)
	if err != nil {
		slog.Error("Failed to exchange authorization code", slog.Any("error", err))
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.oauth.callback.failed", "error", fiber.StatusFound, "")
		errorURL := s.frontendURL + "/auth/error?error=authentication_failed"
		return c.Redirect().Status(fiber.StatusFound).To(errorURL)
	}

	// Check if email is verified
	if !oidcUser.EmailVerified {
		slog.Warn("OIDC email not verified")
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.oauth.callback.failed", "error", fiber.StatusFound, "")
		errorURL := s.frontendURL + "/auth/error?error=email_not_verified"
		return c.Redirect().Status(fiber.StatusFound).To(errorURL)
	}

	oidcInfo := &model.OIDCUserInfo{
		ProviderID:        oidcUser.ProviderID,
		Email:             oidcUser.Email,
		Name:              oidcUser.Name,
		Picture:           oidcUser.Picture,
		Locale:            oidcUser.Locale,
		Provider:          "google",
		PreferredUsername: oidcUser.PreferredUsername,
	}

	// Login existing linked OIDC users when public registration is closed; only
	// the HTTP boundary can decide whether first-time OIDC auto-create is open.
	var user *model.User
	if s.selfServiceRegistrationEnabled && s.externalOnboardingEnabled {
		user, err = s.usersService.LoginOrCreateOIDCUser(c.Context(), oidcInfo)
	} else {
		user, err = s.usersService.LoginOIDCUser(c.Context(), oidcInfo)
	}
	if err != nil {
		if xerror.Is(err, userssvc.ErrEmailAlreadyExists) {
			slog.Warn("OIDC email already registered")
			logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.oauth.callback.failed", "error", fiber.StatusFound, "")
			errorURL := s.frontendURL + "/auth/error?error=email_already_exists"
			return c.Redirect().Status(fiber.StatusFound).To(errorURL)
		}
		if (!s.selfServiceRegistrationEnabled || !s.externalOnboardingEnabled) &&
			xerror.Is(err, userssvc.ErrUserNotFound) {
			if !s.externalOnboardingEnabled {
				slog.Warn("OIDC external onboarding disabled")
				logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.oauth.callback.failed", "external_access_disabled", fiber.StatusFound, "")
				errorURL := s.frontendURL + "/auth/error?error=external_access_disabled"
				return c.Redirect().Status(fiber.StatusFound).To(errorURL)
			}
			slog.Warn("OIDC self-service registration disabled")
			logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.oauth.callback.failed", "registration_disabled", fiber.StatusFound, "")
			errorURL := s.frontendURL + "/auth/error?error=registration_disabled"
			return c.Redirect().Status(fiber.StatusFound).To(errorURL)
		}
		if xerror.Is(err, userssvc.ErrUserNotFound) {
			// Keep verified identity server-side until explicit consent; it grants no tenant access.
			pending, marshalErr := json.Marshal(oidcInfo)
			if marshalErr != nil {
				return apierror.InternalServerError()
			}
			sess.Set(reqctx.SessionGoogleSignupKey, string(pending))
			sess.Set(reqctx.SessionGoogleSignupExpiresKey, time.Now().Add(10*time.Minute).Unix())
			sess.Set("oauth_return_to", returnTo)
			if err := sess.Save(); err != nil {
				return apierror.InternalServerError()
			}
			return c.Redirect().Status(fiber.StatusFound).To(s.frontendURL + "/auth/signup?return_to=" + url.QueryEscape(returnTo))
		}
		slog.Error("Failed to login/create OIDC user", slog.Any("error", err))
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.oauth.callback.failed", "error", fiber.StatusFound, "")
		errorURL := s.frontendURL + "/auth/error?error=authentication_failed"
		return c.Redirect().Status(fiber.StatusFound).To(errorURL)
	}

	return s.finishGoogleAuthentication(c, user, returnTo)
}

func (s *server) completeGoogleSignup(c fiber.Ctx, identity *model.OIDCUserInfo, returnTo string) error {
	user, err := s.usersService.LoginOrCreateOIDCUser(c.Context(), identity)
	if err != nil {
		code := "authentication_failed"
		if xerror.Is(err, userssvc.ErrEmailAlreadyExists) {
			code = "email_already_exists"
			slog.Warn("OIDC email already registered")
		} else {
			slog.Error("Failed to create OIDC user from pending signup", slog.Any("error", err))
		}
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.oauth.callback.failed", "error", fiber.StatusFound, "")
		return c.Redirect().Status(fiber.StatusFound).To(s.frontendURL + "/auth/error?error=" + code)
	}
	return s.finishGoogleAuthentication(c, user, returnTo)
}

func (s *server) finishGoogleAuthentication(c fiber.Ctx, user *model.User, returnTo string) error {
	if returnTo == "" {
		returnTo = "/dashboard"
	}
	// Rotate the pre-auth session before storing authenticated state.
	if err := reqctx.ResetAndSetAuthenticatedUser(c, s.sessionStore, user.Slug, user.AuthenticationVersion, false); err != nil {
		slog.Error("Failed to save session", slog.Any("error", err))
		errorURL := s.frontendURL + "/auth/error?error=session_error"
		return c.Redirect().Status(fiber.StatusFound).To(errorURL)
	}

	// Route through the post-auth workspace gate so no-membership OIDC users
	// cannot enter product surfaces before organization setup.
	return c.Redirect().Status(fiber.StatusFound).To(s.frontendURL + "/workspace?return_to=" + url.QueryEscape(returnTo))
}
