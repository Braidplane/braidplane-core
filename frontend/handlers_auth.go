package frontend

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	slugpkg "github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (r *Router) loadAuthenticationMethods(c fiber.Ctx) error {
	c.SetContext(views.WithGoogleSignInLookup(c.Context(), func(ctx context.Context) bool {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		response, err := r.apiClient.GetAuthenticationMethodsWithResponse(ctx)
		if err != nil || response == nil || response.JSON200 == nil {
			r.logger.WarnContext(ctx, "authentication methods unavailable")
			return false
		}
		return response.JSON200.GoogleSignInEnabled
	}))
	return c.Next()
}

func (r *Router) handleLogout(c fiber.Ctx) error {
	if reqctx.GetUserSlug(c, r.sessionStore) == "" {
		r.setBrowserSessionEnded(c)
		return redirectToLogin(c, authenticatedReturnTo(c))
	}
	sess, err := r.sessionStore.Get(c)
	if err != nil {
		r.logger.ErrorContext(c.Context(), "failed to get session during logout", logging.ErrorAttr(err))
		return c.Status(fiber.StatusInternalServerError).SendString("failed to logout")
	}
	defer sess.Release()

	if err := sess.Destroy(); err != nil {
		r.logger.ErrorContext(c.Context(), "failed to destroy session during logout", logging.ErrorAttr(err))
		return c.Status(fiber.StatusInternalServerError).SendString("failed to logout")
	}

	r.setBrowserSessionEnded(c)
	return redirectHTMXOrSeeOther(c, "/auth/login")
}

func (r *Router) handleLoginPage(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	if reqctx.GetUserSlug(c, r.sessionStore) == "" {
		r.setBrowserSessionEnded(c)
	}
	returnTo := safeReturnTo(c.Query("return_to"))
	return renderTempl(
		c,
		views.LoginPage(
			"Sign in",
			"",
			"",
			"",
			returnTo,
			r.selfServiceRegistrationEnabled,
			"",
		),
	)
}

func (r *Router) handleForgotPasswordPage(c fiber.Ctx) error {
	return renderTempl(
		c,
		views.ForgotPasswordPage("Forgot password", "", "", "", ""),
	)
}

func (r *Router) handleResetPasswordPage(c fiber.Ctx) error {
	setAccountCodeResponseHeaders(c)
	return renderTempl(
		c,
		views.ResetPasswordPage("Reset password", "", "", ""),
	)
}

func (r *Router) handleLoginSubmit(c fiber.Ctx) error {
	email := strings.TrimSpace(c.FormValue("email"))
	password := c.FormValue("password")
	returnTo := safeReturnTo(c.FormValue("return_to"))

	validationMessage, invalidField := validateLoginInput(email, password)
	if validationMessage != "" {
		setHTMLResponseStatus(c, fiber.StatusBadRequest)
		return renderTempl(
			c,
			views.LoginPage(
				"Sign in",
				email,
				validationMessage,
				"",
				returnTo,
				r.selfServiceRegistrationEnabled,
				invalidField,
			),
		)
	}

	resp, err := r.apiClient.CreateSessionWithResponse(
		c.Context(),
		api.CreateSessionJSONRequestBody{
			Email:    api.Email(email),
			Password: password,
		},
		frontendRequestEditor(c.Context(), ""),
	)
	if err != nil {
		r.logger.ErrorContext(c.Context(), "frontend signin failed",
			slog.Int("status", fiber.StatusInternalServerError),
			slog.String("message", "Sign in failed with status 500."),
			logging.ErrorAttr(err),
		)

		setHTMLResponseStatus(c, fiber.StatusInternalServerError)
		return renderTempl(
			c,
			views.LoginPage(
				"Sign in",
				email,
				"Sign in failed with status 500.",
				"",
				returnTo,
				r.selfServiceRegistrationEnabled,
				"",
			),
		)
	}

	if resp.StatusCode() == http.StatusOK {
		if resp.JSON200 == nil || resp.JSON200.AuthenticationVersion <= 0 {
			r.logger.ErrorContext(c.Context(), "frontend signin failed",
				slog.Int("status", fiber.StatusInternalServerError),
				slog.String("message", "Sign in response missing user payload."),
			)

			setHTMLResponseStatus(c, fiber.StatusInternalServerError)
			return renderTempl(
				c,
				views.LoginPage(
					"Sign in",
					email,
					"Sign in failed with status 500.",
					"",
					returnTo,
					r.selfServiceRegistrationEnabled,
					"",
				),
			)
		}

		userSlug, err := slugpkg.New(resp.JSON200.Slug)
		if err != nil {
			r.logger.ErrorContext(c.Context(), "frontend signin failed",
				slog.Int("status", fiber.StatusInternalServerError),
				slog.String("message", "Sign in response contained invalid user slug."),
				logging.ErrorAttr(err),
			)

			c.Status(fiber.StatusInternalServerError)
			return renderTempl(
				c,
				views.LoginPage(
					"Sign in",
					email,
					"Sign in failed with status 500.",
					"",
					returnTo,
					r.selfServiceRegistrationEnabled,
					"",
				),
			)
		}

		if err := reqctx.ResetAndSetAuthenticatedUser(c, r.sessionStore, userSlug, resp.JSON200.AuthenticationVersion, true); err != nil {
			r.logger.ErrorContext(c.Context(), "frontend signin failed",
				slog.Int("status", fiber.StatusInternalServerError),
				slog.String("message", "Failed to persist frontend session."),
				logging.ErrorAttr(err),
			)

			c.Status(fiber.StatusInternalServerError)
			return renderTempl(
				c,
				views.LoginPage(
					"Sign in",
					email,
					"Sign in failed with status 500.",
					"",
					returnTo,
					r.selfServiceRegistrationEnabled,
					"",
				),
			)
		}

		return redirectHTMXOrSeeOther(c, resolvePostLoginRedirect(returnTo))
	}

	if resp.StatusCode() == http.StatusAccepted {
		if resp.JSON202 == nil {
			r.logger.ErrorContext(c.Context(), "frontend signin failed",
				slog.Int("status", fiber.StatusInternalServerError),
				slog.String("message", "Sign in response missing MFA challenge payload."),
			)

			setHTMLResponseStatus(c, fiber.StatusInternalServerError)
			return renderTempl(
				c,
				views.LoginPage(
					"Sign in",
					email,
					"Sign in failed with status 500.",
					"",
					returnTo,
					r.selfServiceRegistrationEnabled,
					"",
				),
			)
		}

		return renderTempl(
			c,
			views.MFAPage(
				"Verify sign in",
				resp.JSON202.ChallengeToken,
				returnTo,
				"",
				"",
			),
		)
	}

	status := resp.StatusCode()
	errorResponse := parseAPIErrorResponse(resp.Body)
	setHTMLResponseStatus(c, status)
	pageError := loginErrorMessage(status, errorResponse)

	r.logger.ErrorContext(c.Context(), "frontend signin failed", slog.Int("status", status))

	return renderTempl(
		c,
		views.LoginPage(
			"Sign in",
			email,
			pageError,
			detailedErrorFromErrorResponse(errorResponse),
			returnTo,
			r.selfServiceRegistrationEnabled,
			"",
		),
	)
}

func (r *Router) handleMFASubmit(c fiber.Ctx) error {
	challengeToken := strings.TrimSpace(c.FormValue("challenge_token"))
	code := strings.TrimSpace(c.FormValue("code"))
	returnTo := safeReturnTo(c.FormValue("return_to"))

	if challengeToken == "" || code == "" {
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(
			c,
			views.MFAPage(
				"Verify sign in",
				challengeToken,
				returnTo,
				"Enter the code from your email.",
				"",
			),
		)
	}

	resp, err := r.apiClient.VerifySessionMFAWithResponse(
		c.Context(),
		api.VerifySessionMFAJSONRequestBody{
			ChallengeToken: challengeToken,
			Code:           code,
		},
		frontendRequestEditor(c.Context(), ""),
	)
	if err != nil {
		r.logger.ErrorContext(c.Context(), "frontend mfa verification failed", logging.ErrorAttr(err))
		setHTMXAwareErrorStatus(c, fiber.StatusInternalServerError)
		return renderTempl(
			c,
			views.MFAPage(
				"Verify sign in",
				challengeToken,
				returnTo,
				"Verification failed with status 500.",
				"",
			),
		)
	}

	if resp.StatusCode() == http.StatusOK {
		if resp.JSON200 == nil || resp.JSON200.AuthenticationVersion <= 0 {
			r.logger.ErrorContext(c.Context(), "frontend mfa verification failed",
				slog.Int("status", fiber.StatusInternalServerError),
				slog.String("message", "MFA response missing user payload."),
			)
			setHTMXAwareErrorStatus(c, fiber.StatusInternalServerError)
			return renderTempl(
				c,
				views.MFAPage(
					"Verify sign in",
					challengeToken,
					returnTo,
					"Verification failed with status 500.",
					"",
				),
			)
		}

		userSlug, err := slugpkg.New(resp.JSON200.Slug)
		if err != nil {
			r.logger.ErrorContext(c.Context(), "frontend mfa verification failed",
				slog.Int("status", fiber.StatusInternalServerError),
				slog.String("message", "MFA response contained invalid user slug."),
				logging.ErrorAttr(err),
			)
			setHTMXAwareErrorStatus(c, fiber.StatusInternalServerError)
			return renderTempl(
				c,
				views.MFAPage(
					"Verify sign in",
					challengeToken,
					returnTo,
					"Verification failed with status 500.",
					"",
				),
			)
		}

		if err := reqctx.ResetAndSetAuthenticatedUser(c, r.sessionStore, userSlug, resp.JSON200.AuthenticationVersion, true); err != nil {
			r.logger.ErrorContext(c.Context(), "frontend mfa verification failed",
				slog.Int("status", fiber.StatusInternalServerError),
				slog.String("message", "Failed to persist frontend session."),
				logging.ErrorAttr(err),
			)
			setHTMXAwareErrorStatus(c, fiber.StatusInternalServerError)
			return renderTempl(
				c,
				views.MFAPage(
					"Verify sign in",
					challengeToken,
					returnTo,
					"Verification failed with status 500.",
					"",
				),
			)
		}

		return redirectHTMXOrSeeOther(c, resolvePostLoginRedirect(returnTo))
	}

	errorResponse := parseAPIErrorResponse(resp.Body)
	message := messageFromErrorResponse(errorResponse)
	if message == "" {
		message = "Invalid or expired code."
	}

	setHTMXAwareErrorStatus(c, resp.StatusCode())
	return renderTempl(
		c,
		views.MFAPage(
			"Verify sign in",
			challengeToken,
			returnTo,
			message,
			detailedErrorFromErrorResponse(errorResponse),
		),
	)
}

func loginErrorMessage(status int, payload *api.ErrorResponse) string {
	if payload != nil && payload.Error.Code == "INVALID_CREDENTIALS" {
		return "Invalid email or password."
	}

	if payload != nil && payload.Error.Code == "EMAIL_NOT_VERIFIED" {
		return "Email not verified. Check your inbox and verify your account before signing in."
	}

	if status == http.StatusUnauthorized {
		return "Invalid email or password."
	}

	message := messageFromErrorResponse(payload)
	if message != "" {
		return message
	}

	return fallbackLoginMessageFromStatus(status)
}

func (r *Router) handleAuthErrorPage(c fiber.Ctx) error {
	errCode := strings.TrimSpace(c.Query("error"))
	message := mapAuthErrorMessage(errCode)
	return renderTempl(c, views.AuthErrorPage("Authentication error", message))
}

func mapAuthErrorMessage(errCode string) string {
	switch errCode {
	case "access_denied":
		return "Google sign-in was cancelled. Try again when you are ready."
	case "email_not_verified":
		return "Your Google account email is not verified. Verify your email with Google and try again."
	case "email_already_exists":
		return "An account with this email already exists. Sign in with email/password or use the same Google account that created it."
	case "registration_disabled":
		return "Account creation is currently unavailable. Existing users can still sign in."
	case "external_access_disabled":
		return "External account creation is currently unavailable. Existing users can still sign in and accept organization invitations."
	case "invalid_state":
		return "Sign-in session expired or became invalid. Please try signing in with Google again."
	case "missing_code", "authentication_failed", "session_error":
		return "Google sign-in failed. Please try again."
	case "":
		return "Authentication failed. Please try again."
	default:
		return "Authentication failed. Please try again."
	}
}

func (r *Router) handleSignupPage(c fiber.Ctx) error {
	returnTo := safeReturnTo(c.Query("return_to"))
	terms := r.signupState()
	pageError := ""
	sess, err := r.sessionStore.Get(c)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	defer sess.Release()
	pending, _ := sess.Get(reqctx.SessionGoogleSignupKey).(string)
	expires, _ := sess.Get(reqctx.SessionGoogleSignupExpiresKey).(int64)
	terms.GoogleSignupPending = pending != "" && time.Now().Unix() < expires
	if pending != "" && !terms.GoogleSignupPending {
		sess.Delete(reqctx.SessionGoogleSignupKey)
		sess.Delete(reqctx.SessionGoogleSignupExpiresKey)
		sess.Delete("oauth_return_to")
		if err := sess.Save(); err != nil {
			return xerror.Unexpected().With(err)
		}
		pageError = "Your Google sign-in expired. Please sign in again."
	}
	return renderTempl(
		c,
		views.SignupPage(
			"Create account",
			"",
			"",
			pageError,
			"",
			returnTo,
			r.selfServiceRegistrationEnabled,
			terms,
		),
	)
}

func setHTMXAwareErrorStatus(c fiber.Ctx, status int) {
	if isHTMXRequest(c) {
		c.Status(fiber.StatusOK)
		return
	}

	c.Status(status)
}

func (r *Router) handleSignupSubmit(c fiber.Ctx) error {
	returnTo := safeReturnTo(c.FormValue("return_to"))

	if !r.selfServiceRegistrationEnabled {
		setHTMXAwareErrorStatus(c, fiber.StatusForbidden)
		return renderTempl(
			c,
			views.SignupPage(
				"Create account",
				"",
				"",
				"",
				"",
				returnTo,
				false,
				r.signupState(),
			),
		)
	}

	fullName := strings.TrimSpace(c.FormValue("full_name"))
	email := strings.TrimSpace(c.FormValue("email"))
	password := c.FormValue("password")
	confirmPassword := c.FormValue("confirm_password")

	if password != confirmPassword {
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(
			c,
			views.SignupPage(
				"Create account",
				fullName,
				email,
				"Passwords do not match.",
				"",
				returnTo,
				r.selfServiceRegistrationEnabled,
				r.signupState(),
			),
		)
	}

	validationMessage := validateSignupInput(fullName, email, password)
	if validationMessage != "" {
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(
			c,
			views.SignupPage(
				"Create account",
				fullName,
				email,
				validationMessage,
				"",
				returnTo,
				r.selfServiceRegistrationEnabled,
				r.signupState(),
			),
		)
	}

	createUserPayload := api.CreateUserJSONRequestBody{
		FullName: fullName,
		Email:    api.Email(email),
		Password: password,
	}
	status, errorResponse, err := r.createUserViaAPI(c.Context(), createUserPayload)
	if err != nil {
		if status < 400 || status >= 600 {
			status = fiber.StatusInternalServerError
		}

		setHTMXAwareErrorStatus(c, status)
		pageError := messageFromErrorResponse(errorResponse)
		if pageError == "" {
			pageError = fallbackMessageFromStatus(status)
		}

		r.logger.ErrorContext(c.Context(), "frontend signup failed", slog.Int("status", status))

		return renderTempl(
			c,
			views.SignupPage(
				"Create account",
				fullName,
				email,
				pageError,
				detailedErrorFromErrorResponse(errorResponse),
				returnTo,
				r.selfServiceRegistrationEnabled,
				r.signupState(),
			),
		)
	}

	return renderTempl(
		c,
		views.EmailVerificationPage("Verify your email", email, "", returnTo),
	)
}

func (r *Router) handleForgotPasswordSubmit(c fiber.Ctx) error {
	email := strings.TrimSpace(c.FormValue("email"))

	validationMessage := validateForgotPasswordInput(email)
	if validationMessage != "" {
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(
			c,
			views.ForgotPasswordPage(
				"Forgot password",
				email,
				validationMessage,
				"",
				"",
			),
		)
	}

	resp, err := r.apiClient.RequestPasswordResetWithResponse(
		c.Context(),
		api.RequestPasswordResetJSONRequestBody{Email: api.Email(email)},
		frontendRequestEditor(c.Context(), ""),
	)
	if err != nil {
		r.logger.ErrorContext(c.Context(), "frontend forgot password failed", logging.ErrorAttr(err))
		setHTMXAwareErrorStatus(c, fiber.StatusInternalServerError)
		return renderTempl(
			c,
			views.ForgotPasswordPage(
				"Forgot password",
				email,
				"Password reset request failed with status 500.",
				"",
				"",
			),
		)
	}

	if resp.StatusCode() != http.StatusNoContent {
		errorResponse := parseAPIErrorResponse(resp.Body)
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = fmt.Sprintf(
				"Password reset request failed with status %d.",
				resp.StatusCode(),
			)
		}

		setHTMXAwareErrorStatus(c, resp.StatusCode())
		return renderTempl(
			c,
			views.ForgotPasswordPage(
				"Forgot password",
				email,
				message,
				detailedErrorFromErrorResponse(errorResponse),
				"",
			),
		)
	}

	return renderTempl(
		c,
		views.ForgotPasswordPage(
			"Forgot password",
			email,
			"",
			"",
			"If an account exists for this email, a reset code has been sent.",
		),
	)
}

func (r *Router) handleResetPasswordSubmit(c fiber.Ctx) error {
	setAccountCodeResponseHeaders(c)
	form := c.Request().PostArgs()
	token := strings.TrimSpace(string(form.Peek("token")))
	password := string(form.Peek("password"))
	confirmPassword := string(form.Peek("confirm_password"))

	if password != confirmPassword {
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(
			c,
			views.ResetPasswordPage(
				"Reset password",
				"Passwords do not match.",
				"",
				"",
			),
		)
	}

	validationMessage := validateResetPasswordInput(token, password)
	if validationMessage != "" {
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(
			c,
			views.ResetPasswordPage(
				"Reset password",
				validationMessage,
				"",
				"",
			),
		)
	}

	resp, err := r.apiClient.ResetPasswordWithResponse(
		c.Context(),
		api.ResetPasswordJSONRequestBody{
			Token:    token,
			Password: password,
		},
		frontendRequestEditor(c.Context(), ""),
	)
	if err != nil {
		r.logger.ErrorContext(c.Context(), "frontend reset password failed", logging.ErrorAttr(err))
		setHTMXAwareErrorStatus(c, fiber.StatusInternalServerError)
		return renderTempl(
			c,
			views.ResetPasswordPage(
				"Reset password",
				"Password reset failed with status 500.",
				"",
				"",
			),
		)
	}

	if resp.StatusCode() != http.StatusNoContent {
		errorResponse := parseAPIErrorResponse(resp.Body)
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = fmt.Sprintf("Password reset failed with status %d.", resp.StatusCode())
		}

		setHTMXAwareErrorStatus(c, resp.StatusCode())
		return renderTempl(
			c,
			views.ResetPasswordPage(
				"Reset password",
				message,
				detailedErrorFromErrorResponse(errorResponse),
				"",
			),
		)
	}

	return renderTempl(
		c,
		views.ResetPasswordPage(
			"Reset password",
			"",
			"",
			"Password reset successful. You can now sign in with your new password.",
		),
	)
}

func (r *Router) handleResendVerificationEmail(c fiber.Ctx) error {
	email := strings.TrimSpace(c.FormValue("email"))
	returnTo := safeReturnTo(c.FormValue("return_to"))

	validationMessage := validateForgotPasswordInput(email)
	if validationMessage != "" {
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(
			c,
			views.EmailVerificationPage(
				"Verify your email",
				email,
				validationMessage,
				returnTo,
			),
		)
	}

	resp, err := r.apiClient.ResendVerificationEmailWithResponse(
		c.Context(),
		api.ResendVerificationEmailJSONRequestBody{Email: api.Email(email)},
		frontendRequestEditor(c.Context(), ""),
	)
	if err != nil {
		r.logger.ErrorContext(c.Context(), "frontend resend verification failed", logging.ErrorAttr(err))
		setHTMXAwareErrorStatus(c, fiber.StatusInternalServerError)
		return renderTempl(
			c,
			views.EmailVerificationPage(
				"Verify your email",
				email,
				"Resend verification failed with status 500.",
				returnTo,
			),
		)
	}

	if resp.StatusCode() != http.StatusNoContent {
		errorResponse := parseAPIErrorResponse(resp.Body)
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = fmt.Sprintf("Resend verification failed with status %d.", resp.StatusCode())
		}

		setHTMXAwareErrorStatus(c, resp.StatusCode())
		return renderTempl(
			c,
			views.EmailVerificationPage("Verify your email", email, message, returnTo),
		)
	}

	return renderTempl(
		c,
		views.EmailVerificationPage(
			"Verify your email",
			email,
			"Verification link resent. Please check your inbox.",
			returnTo,
		),
	)
}

func (r *Router) createUserViaAPI(
	ctx context.Context,
	payload api.CreateUserJSONRequestBody,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreateUserWithResponse(ctx, payload, frontendRequestEditor(ctx, ""))
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() >= 200 && resp.StatusCode() < 300 {
		return http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"create user api returned status %d",
		resp.StatusCode(),
	)
}

func setAccountCodeResponseHeaders(c fiber.Ctx) {
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set(fiber.HeaderReferrerPolicy, "no-referrer")
}

func (r *Router) handleVerifyEmailPage(c fiber.Ctx) error {
	setAccountCodeResponseHeaders(c)
	return renderTempl(c, views.EmailVerificationConfirmationPage())
}

func (r *Router) handleVerifyEmailSubmit(c fiber.Ctx) error {
	setAccountCodeResponseHeaders(c)
	token := strings.TrimSpace(string(c.Request().PostArgs().Peek("token")))
	if token == "" || token == "{{token}}" {
		return invalidEmailVerificationToken(c)
	}

	status, errorResponse, err := r.verifyEmailViaAPI(c.Context(), token)
	if err != nil {
		if status < 400 || status >= 600 {
			status = fiber.StatusInternalServerError
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = fmt.Sprintf("Email verification failed with status %d.", status)
		}

		c.Status(status)
		return renderTempl(
			c,
			views.EmailVerificationResultPage(
				"Verify email",
				false,
				message,
				detailedErrorFromErrorResponse(errorResponse),
			),
		)
	}

	return renderTempl(c, views.EmailVerificationResultPage(
		"Verify email",
		true,
		"Your email has been verified. You can now sign in.",
		"",
	))
}

func invalidEmailVerificationToken(c fiber.Ctx) error {
	c.Status(fiber.StatusBadRequest)
	return renderTempl(c, views.EmailVerificationResultPage("Verify email", false, "Invalid or missing verification token.", ""))
}

func (r *Router) verifyEmailViaAPI(
	ctx context.Context,
	token string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.VerifyEmailWithResponse(
		ctx,
		api.VerifyEmailJSONRequestBody{Token: token},
		frontendRequestEditor(ctx, ""),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"verify email api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) signupState() views.SignupState { return views.SignupState{} }
