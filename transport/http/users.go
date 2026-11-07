package http

import (
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/locale"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
)

func (s *server) PostUsers(c fiber.Ctx) error {
	if !s.externalOnboardingEnabled {
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.signup.rejected", "external_access_disabled", fiber.StatusForbidden, "")
		return apierror.New(
			fiber.StatusForbidden,
			"EXTERNAL_ACCESS_DISABLED",
			"external access is disabled",
		)
	}
	if !s.selfServiceRegistrationEnabled {
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.signup.rejected", "registration_disabled", fiber.StatusForbidden, "")
		return apierror.New(
			fiber.StatusForbidden,
			"SELF_SERVICE_REGISTRATION_DISABLED",
			"self-service registration is disabled",
		)
	}

	newUser := new(api.NewUser)
	if err := c.Bind().Body(newUser); err != nil {
		return apierror.InvalidRequestBody()
	}

	// Parse Accept-Language header to get user's preferred locale
	acceptLanguage := c.Get("Accept-Language", "")
	userLocale := locale.ParseAcceptLanguage(acceptLanguage)

	user, err := s.usersService.CreateUser(c.Context(), model.NewUser{
		FullName: newUser.FullName,
		Email:    string(newUser.Email),
		Password: newUser.Password,
		Role:     model.RoleUser, // Default role for user should be always user
		Locale:   userLocale,
	})
	if err != nil {
		return mapServiceError(err)
	}

	logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelInfo, "auth.signup.succeeded", "success", fiber.StatusCreated, "")
	return c.Status(fiber.StatusCreated).JSON(userToHTTP(user))
}

func (s *server) CreateUser(c fiber.Ctx) error {
	return s.PostUsers(c)
}

func userToHTTP(user *model.User) *api.User {
	return &api.User{
		Slug:             api.Slug(user.Slug),
		Role:             api.Role(user.Role.String()),
		Email:            api.Email(user.Email),
		FullName:         user.FullName,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
		Locale:           user.Locale,
		AvatarUrl:        optionalProfileString(user.AvatarURL),
		PhoneNumber:      optionalProfileString(user.PhoneNumber),
		JobTitle:         optionalProfileString(user.JobTitle),
		Timezone:         optionalProfileString(user.Timezone),
		MfaEmailEnabled:  user.MFAEmailEnabled,
		HasLocalPassword: user.HasLocalPassword,
	}
}

func optionalProfileString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (s *server) VerifyEmail(c fiber.Ctx) error {
	var req api.EmailVerificationRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	err := s.usersService.VerifyEmail(c.Context(), req.Token)
	if err != nil {
		return mapServiceError(err)
	}

	logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelInfo, "auth.email_verification.completed", "success", fiber.StatusNoContent, "")
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) RequestPasswordReset(c fiber.Ctx) error {
	var req api.PasswordForgotRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	// Bound the fast missing-account path as well as enqueue failures.
	deadline := time.NewTimer(500 * time.Millisecond)
	defer deadline.Stop()
	defer func() {
		select {
		case <-deadline.C:
		case <-c.Context().Done():
		}
	}()
	err := s.usersService.RequestPasswordReset(c.Context(), string(req.Email))
	if err != nil {
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelError, "auth.password_reset.requested", "failure", fiber.StatusNoContent, "")
		return c.SendStatus(fiber.StatusNoContent)
	}

	logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelInfo, "auth.password_reset.requested", "success", fiber.StatusNoContent, "")
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) ResetPassword(c fiber.Ctx) error {
	var req api.PasswordResetRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	err := s.usersService.ResetPassword(c.Context(), req.Token, req.Password)
	if err != nil {
		return mapServiceError(err)
	}

	logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelInfo, "auth.password_reset.sessions_revoked", "success", fiber.StatusNoContent, "")
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) ResendVerificationEmail(c fiber.Ctx) error {
	var req api.ResendVerificationEmailRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	err := s.usersService.ResendVerificationEmail(c.Context(), string(req.Email))
	if err != nil {
		slog.Error("resend verification email failed", "error", err)
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelError, "auth.email_verification.resent", "failure", fiber.StatusNoContent, "")
		return c.SendStatus(fiber.StatusNoContent)
	}

	logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelInfo, "auth.email_verification.resent", "success", fiber.StatusNoContent, "")
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) GetUsersProfile(c fiber.Ctx) error {
	if err := s.canProfile(c, model.Read); err != nil {
		return err
	}

	userSlug := reqctx.GetUserSlug(c, s.sessionStore)
	user, err := s.usersService.GetUserBySlug(c.Context(), userSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(userToHTTP(user))
}

func (s *server) PatchUsersProfile(c fiber.Ctx) error {
	if err := s.canProfile(c, model.Update); err != nil {
		return err
	}

	userSlug := reqctx.GetUserSlug(c, s.sessionStore)

	var req api.UpdateUserProfile
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	// MFA toggles are account security changes and require a browser session;
	// PAT-authenticated profile clients may still edit non-security fields.
	if req.MfaEmailEnabled != nil {
		if _, err := s.canSessionProfile(c, model.Update); err != nil {
			return err
		}
		_, authenticatedAt := reqctx.SessionAuthentication(c, s.sessionStore)
		age := time.Since(authenticatedAt)
		if age < 0 || age > 5*time.Minute {
			return apierror.New(fiber.StatusForbidden, "RECENT_AUTHENTICATION_REQUIRED", "Sign in again with your password or MFA before changing MFA settings.")
		}
	}

	// Normalize locale if provided
	var normalizedLocale *string
	if req.Locale != nil {
		localeValue := locale.Normalize(*req.Locale)
		normalizedLocale = &localeValue
	}

	var fullName *string
	if req.FullName != nil {
		fullNameValue := *req.FullName
		fullName = &fullNameValue
	}

	user, err := s.usersService.UpdateProfile(c.Context(), userSlug, model.UpdateUserProfile{
		FullName:        fullName,
		Locale:          normalizedLocale,
		AvatarURL:       req.AvatarUrl,
		PhoneNumber:     req.PhoneNumber,
		JobTitle:        req.JobTitle,
		Timezone:        req.Timezone,
		MFAEmailEnabled: req.MfaEmailEnabled,
	})
	if err != nil {
		return mapServiceError(err)
	}

	if req.MfaEmailEnabled != nil {
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelInfo, "auth.mfa.changed", "success", fiber.StatusOK, "")
	}
	return c.JSON(userToHTTP(user))
}

// UpdateUserPassword changes the authenticated session user's local password.
func (s *server) UpdateUserPassword(c fiber.Ctx) error {
	userSlug, err := s.canSessionProfile(c, model.Update)
	if err != nil {
		return err
	}

	var req api.UpdateUserPassword
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	if err := s.usersService.ChangePassword(
		c.Context(),
		userSlug,
		req.CurrentPassword,
		req.NewPassword,
	); err != nil {
		return mapServiceError(err)
	}

	logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelInfo, "auth.password_change.sessions_revoked", "success", fiber.StatusNoContent, "")
	return c.SendStatus(fiber.StatusNoContent)
}
